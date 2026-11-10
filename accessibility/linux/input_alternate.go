//go:build linux

package linux

import (
	"context"
	"errors"
	"image"
	"os"
	"sync"
	"time"

	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/libei"
	"github.com/LouYuanbo1/neru-core/internal/uinput"
	"github.com/LouYuanbo1/neru-core/internal/wayland"
	"github.com/LouYuanbo1/neru-core/internal/wlrinject"
)

// The alternate input paths: libei for a native Wayland session and uinput for
// pixel-accurate scrolling on any session.
//
// XTest is the right tool on X11 and the wrong one on Wayland: a native Wayland
// client never sees an XTest event, so a click injected that way reaches only
// XWayland windows. libei is the portal-mediated, compositor-level path that
// does reach them; uinput is a kernel input device, which the compositor treats
// as real hardware and which carries high-resolution wheel deltas the X11 core
// protocol cannot express.
//
// Both are optional and both are loud: a machine without libei, without the
// RemoteDesktop portal, or without a writable /dev/uinput reports
// CodeNotSupported with the remedy, never a silent no-op.

// waylandInjector is one Wayland pointer/keyboard session. Two protocols
// provide it: the wlroots virtual pointer/keyboard (sway, niri, Hyprland) and
// libei (KDE, Mutter, cosmic). The adapters below erase the small signature
// differences so the client code is written once.
type waylandInjector interface {
	moveTo(point image.Point) error
	button(button action.MouseButton, pressed bool) error
	scroll(deltaX, deltaY int) error
	key(code int, pressed bool) error
	hasKeyboard() bool
	close() error
}

// libeiAdapter adapts the libei client.
type libeiAdapter struct {
	client *libei.Client
}

func (a libeiAdapter) moveTo(point image.Point) error {
	return a.client.MoveTo(point.X, point.Y)
}

func (a libeiAdapter) button(button action.MouseButton, pressed bool) error {
	return a.client.Button(waylandButtonNumber(button), pressed)
}

func (a libeiAdapter) scroll(deltaX, deltaY int) error {
	return a.client.Scroll(deltaX, deltaY)
}

func (a libeiAdapter) key(code int, pressed bool) error {
	return a.client.Key(code, pressed)
}

func (a libeiAdapter) hasKeyboard() bool { return a.client.HasKeyboard() }
func (a libeiAdapter) close() error      { return a.client.Close() }

// wlrAdapter adapts the wlroots virtual-pointer client.
type wlrAdapter struct {
	client *wlrinject.Client
}

func (a wlrAdapter) moveTo(point image.Point) error {
	return a.client.MoveTo(point.X, point.Y)
}

func (a wlrAdapter) button(button action.MouseButton, pressed bool) error {
	return a.client.Button(waylandButtonNumber(button), pressed)
}

func (a wlrAdapter) scroll(deltaX, deltaY int) error {
	return a.client.Scroll(deltaX, deltaY)
}

func (a wlrAdapter) key(code int, pressed bool) error {
	return a.client.Key(uint32(code), pressed)
}

func (a wlrAdapter) hasKeyboard() bool { return a.client.HasKeyboard() }
func (a wlrAdapter) close() error      { return a.client.Close() }

// alternateInput is this client's non-X11 injection state.
//
// It is one struct rather than fields on Client so the X11 path's lock order
// stays unchanged: alternateMu is only ever taken when no X11 lock is held.
type alternateInput struct {
	mu sync.Mutex

	// injector is the Wayland pointer/keyboard session, nil until dialled.
	injector waylandInjector
	// dialErr caches a failed Dial, so a refused consent prompt is not shown
	// again for every action.
	dialErr error

	// tracked is the pointer position this client last moved to. Wayland has no
	// global pointer query, so a synthetic cursor has to be remembered; the
	// zero value plus trackedOK=false means "unknown".
	tracked   image.Point
	trackedOK bool

	// held records the buttons this client pressed on the Wayland path, so a
	// toggle knows whether it should press or release.
	held map[action.MouseButton]bool

	// device is the uinput wheel, opened on first use.
	device *uinput.Device
	devErr error

	// bounds caches the compositor layout. Opening a Wayland connection to ask
	// for it is cheap but not free, and a tree scan asks on every call.
	bounds   image.Rectangle
	boundsAt time.Time
	boundsOK bool
}

// newAlternateInput builds the state. No I/O happens here.
func newAlternateInput() *alternateInput {
	return &alternateInput{held: make(map[action.MouseButton]bool, 3)}
}

// onWayland reports whether the session is a Wayland one. The variable is the
// session's own statement; a Wayland session may still have DISPLAY set for
// XWayland, which is exactly why the check is WAYLAND_DISPLAY and not DISPLAY.
func onWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// close releases the libei session and the uinput device, releasing buttons the
// client still holds first.
func (a *alternateInput) close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var failures []error

	if a.injector != nil {
		for button, down := range a.held {
			if down {
				_ = a.injector.button(button, false)
			}
		}

		failures = append(failures, a.injector.close())
		a.injector = nil
	}

	if a.device != nil {
		failures = append(failures, a.device.Close())
		a.device = nil
	}

	return errors.Join(failures...)
}

// waylandClient returns the Wayland input session, dialling once. The error is
// cached: a missing protocol or a refused prompt is a property of the session,
// not of one action, and re-dialling would re-prompt.
func (a *alternateInput) waylandClient(ctx context.Context) (waylandInjector, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.waylandClientLocked(ctx)
}

// waylandClientLocked picks and dials the session's injector.
//
// The wlroots virtual pointer comes first because it is the compositor's own
// protocol where it exists, needs no portal consent and cannot be revoked by a
// portal restart. libei is the fallback for the compositors that do not expose
// it (KWin, Mutter, cosmic). Both report CodeNotSupported when absent, and the
// joined error preserves each reason so a user can tell a missing libei from a
// compositor without the wlroots protocol.
func (a *alternateInput) waylandClientLocked(ctx context.Context) (waylandInjector, error) {
	if a.injector != nil {
		return a.injector, nil
	}

	if a.dialErr != nil {
		return nil, a.dialErr
	}

	var failures []error

	if wlrinject.Available() {
		client, err := wlrinject.Dial()
		if err == nil {
			a.injector = wlrAdapter{client: client}

			return a.injector, nil
		}

		failures = append(failures, err)
	}

	if libei.Available() {
		client, err := libei.Dial(ctx)
		if err == nil {
			a.injector = libeiAdapter{client: client}

			return a.injector, nil
		}

		failures = append(failures, err)
	} else {
		failures = append(failures, errs.New(
			errs.CodeNotSupported,
			"libei is not installed: install libei and xdg-desktop-portal >= 1.18",
		))
	}

	if len(failures) == 0 {
		failures = append(failures, errs.New(
			errs.CodeNotSupported,
			"this Wayland compositor exposes neither the wlroots virtual pointer nor a libei RemoteDesktop portal",
		))
	}

	a.dialErr = errs.Wrapf(
		errors.Join(failures...),
		errs.CodeNotSupported,
		"no Wayland input injection path is available",
	)

	return nil, a.dialErr
}

// uinputDevice returns the virtual wheel, opening it once.
func (a *alternateInput) uinputDevice() (*uinput.Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.device != nil {
		return a.device, nil
	}

	if a.devErr != nil {
		return nil, a.devErr
	}

	if !uinput.Available() {
		a.devErr = errs.New(
			errs.CodeNotSupported,
			"the kernel uinput device is not writable: load the uinput module and grant the input group access to /dev/uinput",
		)

		return nil, a.devErr
	}

	device, err := uinput.Open()
	if err != nil {
		a.devErr = err

		return nil, err
	}

	a.device = device

	return device, nil
}

// resetOnFailure drops a Wayland injector that has failed in a way that leaves
// it unusable (a revoked grant, a compositor restart, a poisoned connection).
// The next action re-establishes a session; a refusal to dial is cached
// separately and is not cleared here, so a missing portal is not retried for
// every action.
func (a *alternateInput) resetOnFailure(err error) {
	if err == nil {
		return
	}

	if !errs.IsCode(err, errs.CodeTimeout) && !errs.IsCode(err, errs.CodeAccessibilityFailed) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.injector != nil {
		_ = a.injector.close()
		a.injector = nil
	}
}

// trackedPoint returns the remembered pointer position, falling back to the X
// server's answer so a session that started before this client can still be
// tracked from its real position.
func (c *Client) trackedPoint() (image.Point, bool) {
	c.alternates.mu.Lock()
	point, known := c.alternates.tracked, c.alternates.trackedOK
	c.alternates.mu.Unlock()

	if known {
		return point, true
	}

	// XWayland's XQueryPointer mirrors the real pointer position, so it is a
	// usable seed even though injection does not go through X.
	seeded := image.Point{}
	ok := c.withX11(func(conn *x11Conn) error {
		seeded = conn.cursorPosition()

		return nil
	}) == nil

	if ok {
		c.trackCursor(seeded)

		return seeded, true
	}

	return image.Point{}, false
}

// trackCursor remembers point as the pointer position.
func (c *Client) trackCursor(point image.Point) {
	c.alternates.mu.Lock()
	defer c.alternates.mu.Unlock()

	c.alternates.tracked = point
	c.alternates.trackedOK = true
}

// WarmUp opens whatever the session's input path needs before it is needed.
//
// On Wayland that means dialling libei, which can show the RemoteDesktop consent
// prompt; doing it at startup keeps a hint activation from blocking on a
// dialog. On any session it opens the uinput wheel if the device is available.
// A missing facility is not an error here: the failing action will report it
// with the remedy.
func (c *Client) WarmUp(ctx context.Context) error {
	var failures []error

	if onWayland() {
		if _, err := c.alternates.waylandClient(ctx); err != nil && !errs.IsNotSupported(err) {
			failures = append(failures, err)
		}
	}

	if _, err := c.alternates.uinputDevice(); err != nil && !errs.IsNotSupported(err) {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

// performWaylandAction injects one pointer action through the session's
// Wayland injector (wlroots virtual pointer or libei).
//
// A timeout or protocol failure poisons the injected session (a revoked portal
// grant, a compositor restart); the injector is dropped so the next action
// re-establishes one instead of failing forever.
func (c *Client) performWaylandAction(
	actionType action.Type,
	point image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) (err error) {
	defer func() { c.alternates.resetOnFailure(err) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := c.alternates.waylandClient(ctx)
	if err != nil {
		return err
	}

	switch actionType {
	case action.TypeMoveMouse:
		return c.waylandMove(client, point)
	case action.TypeMoveMouseRelative:
		current, known := c.trackedPoint()
		if !known {
			current = image.Point{}
		}

		return c.waylandMove(client, current.Add(point))
	}

	button, phase, ok := actionType.MouseButtonPhase()
	if !ok {
		return errs.Newf(errs.CodeInvalidInput, "unsupported action type: %s", actionType)
	}

	if phase == action.PhaseToggle {
		if c.waylandHeld(button) {
			phase = action.PhaseUp
		} else {
			phase = action.PhaseDown
		}
	}

	release, err := c.waylandHoldModifiers(client, modifiers)
	if err != nil {
		return err
	}
	defer release()

	var origin image.Point

	haveOrigin := false
	if restoreCursor {
		origin, haveOrigin = c.trackedPoint()
	}

	if phase == action.PhaseUp {
		if err := client.button(button, false); err != nil {
			return err
		}

		c.setWaylandHeld(button, false)

		return nil
	}

	if err := c.waylandMove(client, point); err != nil {
		return err
	}

	if err := client.button(button, true); err != nil {
		return err
	}

	if phase == action.PhaseDown {
		c.setWaylandHeld(button, true)

		return nil
	}

	time.Sleep(10 * time.Millisecond)

	if err := client.button(button, false); err != nil {
		return err
	}

	c.setWaylandHeld(button, false)

	if restoreCursor && haveOrigin {
		time.Sleep(40 * time.Millisecond)

		return c.waylandMove(client, origin)
	}

	return nil
}

// waylandMove moves the pointer and remembers where it went.
func (c *Client) waylandMove(client waylandInjector, point image.Point) error {
	if err := client.moveTo(point); err != nil {
		return err
	}

	c.trackCursor(point)

	return nil
}

// waylandHoldModifiers presses modifiers as real keys on the session's keyboard
// and returns the release function. An injector without a keyboard cannot hold a
// modifier; the remedy names the consent setting.
func (c *Client) waylandHoldModifiers(client waylandInjector, modifiers action.Modifiers) (func(), error) {
	codes := evdevModifierCodes(modifiers)
	if len(codes) == 0 {
		return func() {}, nil
	}

	if !client.hasKeyboard() {
		return nil, errs.New(
			errs.CodeNotSupported,
			"the Wayland input session granted no keyboard device, so modifiers cannot be held: enable Keyboard in the RemoteDesktop consent dialog (libei) or use a compositor with the wlroots virtual keyboard",
		)
	}

	pressed := make([]int, 0, len(codes))

	for _, code := range codes {
		if err := client.key(code, true); err != nil {
			for _, done := range pressed {
				_ = client.key(done, false)
			}

			return nil, err
		}

		pressed = append(pressed, code)
	}

	return func() {
		for index := len(pressed) - 1; index >= 0; index-- {
			_ = client.key(pressed[index], false)
		}
	}, nil
}

// waylandScroll scrolls through the session's continuous axis, which carries
// pixel deltas rather than notches.
func (c *Client) waylandScroll(deltaX, deltaY int, modifiers action.Modifiers) (err error) {
	defer func() { c.alternates.resetOnFailure(err) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := c.alternates.waylandClient(ctx)
	if err != nil {
		return err
	}

	release, err := c.waylandHoldModifiers(client, modifiers)
	if err != nil {
		return err
	}
	defer release()

	return client.scroll(deltaX, deltaY)
}

// uinputScroll scrolls through the virtual wheel, which carries sub-notch
// pixels on a kernel with REL_WHEEL_HI_RES and whole notches otherwise.
func (c *Client) uinputScroll(deltaX, deltaY int, modifiers action.Modifiers) error {
	device, err := c.alternates.uinputDevice()
	if err != nil {
		return err
	}

	codes := evdevModifierCodes(modifiers)

	for _, code := range codes {
		if err := device.Key(code, true); err != nil {
			return err
		}
	}

	scrollErr := device.Scroll(deltaX, deltaY)

	for index := len(codes) - 1; index >= 0; index-- {
		_ = device.Key(codes[index], false)
	}

	return scrollErr
}

// waylandButtonNumber maps an action button to libei's numbering (1 left,
// 2 middle, 3 right).
func waylandButtonNumber(button action.MouseButton) int {
	switch button {
	case action.ButtonLeft:
		return 1
	case action.ButtonRight:
		return 3
	default:
		return 2
	}
}

// waylandHeld reports whether this client pressed button on the Wayland path.
func (c *Client) waylandHeld(button action.MouseButton) bool {
	c.alternates.mu.Lock()
	defer c.alternates.mu.Unlock()

	return c.alternates.held[button]
}

// setWaylandHeld records a Wayland button press or release.
func (c *Client) setWaylandHeld(button action.MouseButton, down bool) {
	c.alternates.mu.Lock()
	defer c.alternates.mu.Unlock()

	c.alternates.held[button] = down
}

// evdevModifierCodes returns the evdev keycodes for the modifiers, in a stable
// press order (ctrl, shift, alt, meta).
func evdevModifierCodes(modifiers action.Modifiers) []int {
	codes := make([]int, 0, 4)

	if modifiers.Has(action.ModCtrl) {
		codes = append(codes, 29)
	}

	if modifiers.Has(action.ModShift) {
		codes = append(codes, 42)
	}

	if modifiers.Has(action.ModAlt) {
		codes = append(codes, 56)
	}

	if modifiers.Has(action.ModCmd) {
		codes = append(codes, 125)
	}

	return codes
}

// waylandScreenBounds returns the union of the compositor's outputs, which is
// the only way a Wayland client can learn the desktop size. The answer is
// cached for a few seconds: a tree scan asks on every call, and opening a
// Wayland connection for each one would cost more than a stale rectangle after
// a monitor change is worth.
func (c *Client) waylandScreenBounds() (image.Rectangle, bool) {
	c.alternates.mu.Lock()
	if c.alternates.boundsOK && time.Since(c.alternates.boundsAt) < 5*time.Second {
		bounds := c.alternates.bounds
		c.alternates.mu.Unlock()

		return bounds, true
	}
	c.alternates.mu.Unlock()

	bounds, ok := queryWaylandBounds()
	if !ok {
		return image.Rectangle{}, false
	}

	c.alternates.mu.Lock()
	c.alternates.bounds = bounds
	c.alternates.boundsAt = time.Now()
	c.alternates.boundsOK = true
	c.alternates.mu.Unlock()

	return bounds, true
}

// queryWaylandBounds asks the compositor for its output layout.
func queryWaylandBounds() (image.Rectangle, bool) {
	connection, err := wayland.Dial()
	if err != nil {
		return image.Rectangle{}, false
	}

	defer connection.Close()

	outputs, err := connection.Outputs()
	if err != nil || len(outputs) == 0 {
		return image.Rectangle{}, false
	}

	bounds := image.Rectangle{}

	for _, output := range outputs {
		rect := image.Rect(output.X, output.Y, output.X+output.Width, output.Y+output.Height)
		if rect.Empty() {
			continue
		}

		if bounds.Empty() {
			bounds = rect

			continue
		}

		bounds = bounds.Union(rect)
	}

	return bounds, !bounds.Empty()
}
