//go:build linux

package linux

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/linux/compositor"
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Client is the Linux implementation of ax.Client.
//
// It holds no operating-system handle until one is first needed. The
// accessibility bus connection is opened by the first tree call and the X
// display connection by the first input call, so a client that is constructed
// and never used touches nothing and costs nothing.
//
// A Client is safe for concurrent use. The two connections are guarded by their
// own mutexes and godbus multiplexes overlapping calls, so a scan does not block
// a click. The lock order, where two are held at once, is connection, then
// input, then held buttons.
type Client struct {
	// connMu guards the accessibility bus connection and the activation state
	// that goes with it.
	connMu            sync.Mutex
	conn              *dbus.Conn
	a11yEnabled       bool
	savedIsEnabled    bool
	savedScreenReader bool

	// rolesMu guards the clickable role list, which a caller may replace while
	// another goroutine is scanning.
	rolesMu sync.RWMutex
	roles   []string

	// depth and nodes are this client's configured walk bounds. They are set by
	// New and never change, so they need no lock.
	depth int
	nodes int

	// compositor answers the two questions AT-SPI cannot: which window the
	// compositor has focused, and where it sits on screen. It is nil when the
	// caller disabled it, and the field never changes after New, so a scan reads
	// it without a lock. ownsCompositor is true when Close must release it.
	compositor     *compositor.Resolver
	ownsCompositor bool

	// x11Mu guards the X display connection and serializes input injection, so
	// two actions cannot interleave their moves and button events.
	x11Mu   sync.Mutex
	x11Conn *x11Conn
	held    *heldButtons

	// alternates is the non-X11 injection state: the libei session used on a
	// Wayland desktop, the tracked pointer position that path needs, and the
	// uinput wheel used for pixel-accurate scrolling on any session.
	alternates *alternateInput

	// closed is set once by Close. Every entry point checks it, so a closed
	// client reports an error instead of quietly reconnecting.
	closed atomic.Bool
}

// Options configures a Linux accessibility client.
type Options struct {
	// ClickableRoles are native AT-SPI role names, exactly as
	// Accessible.GetRoleName returns them: lowercase and space separated, such as
	// "push button", "entry" or "page tab list".
	//
	// Empty means element.DefaultClickableNativeRoles(element.VocabularyATSPI),
	// the library's shipped default, so a caller with no configuration gets the
	// same targets it would get on any other platform.
	ClickableRoles []string

	// MaxDepth is this client's default depth bound: the deepest level of a tree
	// ClickableNodes reads below the root window. A call that passes its own
	// non-positive maxDepth uses this, and a non-positive value here means the
	// package default. See walkDepth for the ceiling.
	MaxDepth int

	// MaxNodes bounds how many elements one walk reads, so a lazily growing page
	// (a virtualized list, an infinite scroll) cannot turn one scan into
	// thousands of D-Bus round trips. A non-positive value means the package
	// default.
	MaxNodes int

	// DisableCompositor turns off the compositor bridge. Focus selection then
	// falls back to AT-SPI's ACTIVE and SHOWING ranking, and element extents are
	// placed by the AT-SPI reconstruction in policy.go. It is the opt-out for an
	// embedder that does not want this library talking to KWin, GNOME Shell or a
	// compositor CLI.
	//
	// DisableCompositor and Compositor are mutually exclusive.
	DisableCompositor bool

	// Compositor replaces the automatically detected compositor resolver. Use it
	// to share one resolver between clients, or to supply COSMIC's geometry
	// source through compositor.Options. The client uses it but does not close
	// it; the caller keeps ownership.
	//
	// When both this and DisableCompositor are set, New reports
	// errs.CodeInvalidInput rather than silently ignoring one.
	Compositor *compositor.Resolver
}

// New returns a Linux accessibility client.
//
// It performs no I/O: the accessibility bus is contacted by the first tree call,
// the X display by the first input call, and a compositor bridge by its first
// focus query, so constructing a client on a session with no desktop is not an
// error. The only error today is an Options pair that contradicts itself:
// DisableCompositor together with a Compositor resolver.
//
// Unless opts.DisableCompositor is set, the client detects the session's
// compositor and uses it to select the focused window and to correct element
// coordinates. A session it cannot identify simply keeps the AT-SPI behavior.
func New(opts Options) (*Client, error) {
	if opts.DisableCompositor && opts.Compositor != nil {
		return nil, errs.New(
			errs.CodeInvalidInput,
			"Options.DisableCompositor and Options.Compositor cannot both be set",
		)
	}

	client := &Client{
		held:       newHeldButtons(),
		alternates: newAlternateInput(),
		depth:      opts.MaxDepth,
		nodes:      opts.MaxNodes,
	}

	client.roles = normalizeRoles(opts.ClickableRoles)
	if len(client.roles) == 0 {
		client.roles = defaultClickableRoles()
	}

	switch {
	case opts.DisableCompositor:
		// Explicitly off: no resolver, so everything falls back to AT-SPI.
	case opts.Compositor != nil:
		client.compositor = opts.Compositor
	default:
		client.compositor = compositor.Detect(context.Background())
		client.ownsCompositor = true
	}

	return client, nil
}

// defaultClickableRoles resolves the library's shipped default clickable roles
// into native AT-SPI role names, dropping the neutral roles Linux has no name
// for.
func defaultClickableRoles() []string {
	return normalizeRoles(element.DefaultClickableNativeRoles(element.VocabularyATSPI))
}

// errClientClosed is the error every entry point reports once Close has run. It
// is built per call because an errs.Error carries a context map a caller may
// add to, and a shared value would leak that between callers.
func errClientClosed() error {
	return errs.New(errs.CodeAccessibilityFailed, "the Linux accessibility client is closed")
}

// errNothingFocused is reported by the window and application lookups when
// neither the compositor nor AT-SPI's state bits single out a focused window.
func errNothingFocused() error {
	return errs.New(
		errs.CodeElementNotFound,
		"no focused window could be identified: neither the compositor nor "+
			"AT-SPI's active and showing states named one",
	)
}

// Close releases the accessibility bus connection, restores the assistive-
// technology flags the client turned on, closes the X display after letting go
// of anything it still holds, and releases the compositor bridge this client
// owns (a caller-supplied resolver is left open).
//
// It is idempotent: the second and later calls return nil and do nothing. Close
// does not wait for a scan that is already in flight; that scan fails with a
// closed-connection error, which is the price of not blocking shutdown on an
// application that may never answer.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	return errors.Join(c.closeBus(), c.closeInput(), c.closeAlternates(), c.closeCompositor())
}

// closeAlternates releases the Wayland input session and the uinput wheel.
func (c *Client) closeAlternates() error {
	if c.alternates == nil {
		return nil
	}

	return c.alternates.close()
}

// closeCompositor releases the compositor resolver this client created, and does
// nothing for one the caller supplied.
func (c *Client) closeCompositor() error {
	if !c.ownsCompositor || c.compositor == nil {
		return nil
	}

	return c.compositor.Close()
}

// closeBus releases the accessibility bus connection and restores the session's
// assistive-technology flags.
func (c *Client) closeBus() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	var failures []error

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			failures = append(failures, err)
		}

		c.conn = nil
	}

	if err := c.restoreAccessibility(); err != nil {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

// closeInput releases every button the client still holds and closes the X
// display.
func (c *Client) closeInput() error {
	c.x11Mu.Lock()
	defer c.x11Mu.Unlock()

	conn := c.x11Conn
	c.x11Conn = nil

	if conn == nil {
		return nil
	}

	// Releasing first is not cosmetic: a client closed mid-drag would otherwise
	// leave a button, or a modifier an injection pressed, down on the desktop with
	// no program left to release it.
	return errors.Join(c.held.releaseAll(conn), conn.close())
}

// FrontmostWindow returns the active window of the focused application.
//
// The compositor is asked which window it has focused; when it cannot answer,
// the window is chosen by the ranking in selectFocusedFrame. It returns
// errs.CodeElementNotFound when no window claims focus, rather than an empty
// window, so a caller can tell "nothing focused" from a window with no hintable
// elements.
func (c *Client) FrontmostWindow(ctx context.Context) (ax.Window, error) {
	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	_, frame, identity, ok, err := c.focusedWindow(ctx, conn)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, errNothingFocused()
	}

	return &atspiWindow{
		ref:   frame.ref,
		role:  frame.role,
		appID: identity.appID,
		title: identity.title,
	}, nil
}

// AllWindows returns the top-level windows of the focused application.
//
// A session with nothing focused returns no windows and no error: there is
// nothing to enumerate, which is not a failure.
func (c *Client) AllWindows(ctx context.Context) ([]ax.Window, error) {
	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	app, _, _, ok, err := c.focusedWindow(ctx, conn)
	if err != nil || !ok {
		return nil, err
	}

	return windowsOf(app), nil
}

// FrontmostAndPopoverWindows returns the top-level windows of the focused
// application, which is the same set AllWindows returns.
//
// A popover on Linux is not a separate top-level window: a GTK or Qt popover is
// a mapped child of the window that owns it, so walking the window's tree already
// covers it. The method exists because the contract requires it, and it is
// documented rather than pretending Linux has macOS's window classes.
func (c *Client) FrontmostAndPopoverWindows(ctx context.Context) ([]ax.Window, error) {
	return c.AllWindows(ctx)
}

// windowsOf wraps an application's scanned windows as ax.Windows.
//
// None of them carries a compositor identity, because only the focused window
// was identified. An origin belongs to one window, and applying the focused
// window's origin to a sibling would misplace every hint in it, so those scans
// keep the AT-SPI reconstruction.
func windowsOf(app scannedApplication) []ax.Window {
	windows := make([]ax.Window, 0, len(app.frames))

	for _, frame := range app.frames {
		windows = append(windows, &atspiWindow{ref: frame.ref, role: frame.role})
	}

	return windows
}

// FocusedApplication returns the application that owns the focused window.
func (c *Client) FocusedApplication(ctx context.Context) (ax.App, error) {
	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	app, _, _, ok, err := c.focusedWindow(ctx, conn)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, errNothingFocused()
	}

	return appElement(app), nil
}

// appElement wraps a scanned application as an ax.App.
func appElement(app scannedApplication) *atspiApp {
	return &atspiApp{
		ref:      app.ref,
		name:     app.name,
		pid:      app.pid,
		bundleID: applicationBundleID(app.name, app.pid, app.ref.bus),
	}
}

// ApplicationByBundleID returns the running application that an identifier
// names.
//
// Linux has no bundle identifiers. The AT-SPI display name, the last dot segment
// of a desktop id, and the owning process's name are all matched, so
// "firefox", "Firefox" and "org.mozilla.firefox" all reach the same application.
// When nothing matches, the error is errs.CodeNotSupported rather than "not
// found", because what is missing is the identifier scheme itself: a caller has
// to know the lookup was approximate before deciding what to do about it.
func (c *Client) ApplicationByBundleID(ctx context.Context, bundleID string) (ax.App, error) {
	if strings.TrimSpace(bundleID) == "" {
		return nil, errs.New(
			errs.CodeInvalidInput,
			"ApplicationByBundleID needs a non-empty identifier",
		)
	}

	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	app, ok, err := c.findApplication(ctx, conn, bundleID)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, applicationNotFound(bundleID)
	}

	return appElement(app), nil
}

// ClickableNodes walks root for elements a hint can land on.
//
// root must be a window or application element this backend returned. roles are
// native AT-SPI role names and replace the client's configured list when
// non-empty. maxDepth bounds how deep the walk reads, with a non-positive value
// meaning the default; readState adds each element's identifier and focused
// state, which costs one extra read per element.
//
// Each returned node's bounds are its on-screen rectangle: extents are placed in
// screen pixels and clipped to the screen, so an element that hangs off the edge
// is reported by the part a hint can actually land on, and an element that is
// entirely off screen is dropped.
func (c *Client) ClickableNodes(
	ctx context.Context,
	root ax.Element,
	roles []string,
	maxDepth int,
	readState bool,
) ([]ax.Node, error) {
	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	frames, err := c.scanRoots(ctx, conn, root)
	if err != nil {
		return nil, err
	}

	return c.scanFrames(ctx, conn, frames, roles, maxDepth, readState), nil
}

// ClickableElementsFromBundleID walks the windows of the application an
// identifier names. See ApplicationByBundleID for what an identifier can be on
// Linux, and ClickableNodes for the walk itself.
func (c *Client) ClickableElementsFromBundleID(
	ctx context.Context,
	bundleID string,
	roles []string,
	maxDepth int,
) ([]ax.Node, error) {
	if strings.TrimSpace(bundleID) == "" {
		return nil, errs.New(
			errs.CodeInvalidInput,
			"ClickableElementsFromBundleID needs a non-empty identifier",
		)
	}

	conn, err := c.atspiSession(ctx)
	if err != nil {
		return nil, err
	}

	app, ok, err := c.findApplication(ctx, conn, bundleID)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, applicationNotFound(bundleID)
	}

	frames := c.framesOf(ctx, conn, app.ref)
	targets := make([]frameTarget, 0, len(frames))

	for _, frame := range frames {
		targets = append(targets, frameTarget{ref: frame.ref, role: frame.role})
	}

	if len(targets) == 0 {
		targets = append(targets, frameTarget{ref: app.ref})
	}

	return c.scanFrames(ctx, conn, targets, roles, maxDepth, false), nil
}

// MenuBarClickableElements is not supported on Linux.
//
// There is no globally addressable menu bar to scan here: an application's menu
// bar — global or in-window — is part of that application's own accessibility
// tree, and the AT-SPI registry exposes no interface that enumerates menu bars
// across applications. A hint scan of the frontmost window already reaches the
// menu items that are on screen.
func (c *Client) MenuBarClickableElements(_ context.Context, _ int) ([]ax.Node, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"Linux has no global menu bar to scan; scan the frontmost window instead",
	)
}

// MissionControlClickableNodes is not supported on Linux.
//
// Mission Control is macOS's window overview. GNOME Shell's and KDE Plasma's
// overviews are compositor surfaces drawn outside the accessibility tree, with no
// AT-SPI equivalent of the windows Mission Control draws, so there is nothing to
// report.
func (c *Client) MissionControlClickableNodes(_ context.Context, _ int) ([]ax.Node, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"Linux has no Mission Control; the compositor overview is not exposed over AT-SPI",
	)
}

// IsMissionControlActive always reports false: Linux has no Mission Control.
func (c *Client) IsMissionControlActive() bool { return false }

// SupportsSupplementaryElements reports false: the auxiliary surfaces the
// contract describes (Dock, menu bar, Notification Center, Stage Manager,
// picture-in-picture, screen-capture UI) are macOS-specific and have no single
// Linux equivalent this backend could enumerate.
func (c *Client) SupportsSupplementaryElements() bool { return false }

// CheckPermissions reports true: AT-SPI needs no user consent on Linux.
//
// There is no Linux equivalent of the macOS accessibility prompt. The
// assistive-technology flags this backend turns on are advisory to toolkits, and
// a session where the accessibility bus cannot be reached is a broken or missing
// at-spi2-core rather than a permission the user must grant, so that failure is
// reported by the tree calls and not asserted here.
func (c *Client) CheckPermissions() bool { return true }

// SetClickableRoles replaces the clickable role list. An empty or blank list
// restores the library's default, so there is no way to configure a client that
// finds nothing.
func (c *Client) SetClickableRoles(roles []string) {
	normalized := normalizeRoles(roles)
	if len(normalized) == 0 {
		normalized = defaultClickableRoles()
	}

	c.rolesMu.Lock()
	defer c.rolesMu.Unlock()

	c.roles = normalized
}

// ClickableRoles returns the roles this client currently filters on: the
// configured list, or the library's default when none was configured. The result
// is a copy, so a caller cannot mutate the client's state through it.
func (c *Client) ClickableRoles() []string {
	c.rolesMu.RLock()
	defer c.rolesMu.RUnlock()

	return append([]string(nil), c.roles...)
}

// rolesFor returns the role set one scan filters on: the caller's list when it
// names anything, else the client's configured list.
func (c *Client) rolesFor(roles []string) map[string]struct{} {
	if len(roles) > 0 {
		if set := roleSet(roles); len(set) > 0 {
			return set
		}
	}

	return roleSet(c.ClickableRoles())
}

// ActiveScreenBounds returns the X screen's rectangle.
//
// X11 has one root window spanning every monitor, and nothing in the core
// protocol names the monitor the pointer is on, so this is the whole screen
// rather than the active output.
//
// Without a usable X display there is no backend-independent way to learn the
// screen size (a Wayland client cannot query it), so a 1x1 rectangle is returned
// as a placeholder that callers can recognise. Tree scans treat a missing screen
// as unknown and clip nothing, so the placeholder never drops a hint.
func (c *Client) ActiveScreenBounds() image.Rectangle {
	if onWayland() {
		if bounds, ok := c.waylandScreenBounds(); ok {
			return bounds
		}
	}

	if bounds, ok := c.screenBounds(); ok {
		return bounds
	}

	return image.Rect(0, 0, 1, 1)
}

// screenBounds returns the X screen's rectangle, and false when no X display can
// be reached. The false case means "unknown" to the scan, which then clips
// nothing.
func (c *Client) screenBounds() (image.Rectangle, bool) {
	bounds := image.Rectangle{}

	err := c.withX11(func(conn *x11Conn) error {
		bounds, _ = conn.screenBounds()

		return nil
	})

	return bounds, err == nil && !bounds.Empty()
}

// withX11 runs fn on the client's X display connection, holding the input lock
// for the whole call.
//
// The lock is the point: an Xlib Display is not safe to use from two goroutines
// at once unless the process called XInitThreads, which this library does not
// require of its embedder. Holding the lock across the whole gesture also keeps
// one action's move-and-click from being interleaved with another's.
//
// The connection is opened on first use. A failure is not cached: the common
// failure is a Wayland session with no DISPLAY, which is an environment lookup
// rather than a connection attempt, and a display can appear later in a session
// (an X server started for a nested compositor), so a cached refusal would
// outlive its cause.
func (c *Client) withX11(fn func(conn *x11Conn) error) error {
	c.x11Mu.Lock()
	defer c.x11Mu.Unlock()

	if c.closed.Load() {
		return errClientClosed()
	}

	if c.x11Conn == nil {
		conn, err := openX11Conn()
		if err != nil {
			return err
		}

		c.x11Conn = conn
	}

	return fn(c.x11Conn)
}

// PerformAction injects a pointer action through XTest.
//
// Click, press and release actions take the point to act on; a relative move
// takes a delta. Modifiers are presented as real key presses for the duration of
// the injection, because an X11 button event carries the server's live modifier
// state and XTEST cannot set flags on a single event.
//
// A click on a button this client is already holding ends that drag at the click
// point instead of pressing and releasing again, which is what a click while
// dragging has to mean: the drag is complete, and a full click would leave the
// button recorded as held.
//
// Scroll is refused here with errs.CodeInvalidInput; it belongs to Scroll. The
// refusal is made before the X connection is touched, so a caller that confuses
// the two gets the same answer on a session with no display at all.
func (c *Client) PerformAction(
	actionType action.Type,
	p image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	if actionType == action.TypeScroll {
		return errs.Newf(
			errs.CodeInvalidInput,
			"a scroll action goes through Scroll, not PerformAction: %s",
			actionType,
		)
	}

	if c.closed.Load() {
		return errClientClosed()
	}

	// A Wayland desktop needs the compositor-level path; XTest reaches only
	// XWayland windows there. The check is the session's, not the window's.
	if onWayland() {
		return c.performWaylandAction(actionType, p, restoreCursor, modifiers)
	}

	return c.withX11(func(conn *x11Conn) error {
		return c.performAction(conn, actionType, p, restoreCursor, modifiers)
	})
}

// performAction is PerformAction's body, run with the X connection's lock held.
func (c *Client) performAction(
	conn *x11Conn,
	actionType action.Type,
	p image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	switch actionType {
	case action.TypeMoveMouse:
		return conn.move(p)
	case action.TypeMoveMouseRelative:
		return conn.move(conn.cursorPosition().Add(p))
	}

	button, phase, ok := actionType.MouseButtonPhase()
	if !ok {
		return errs.Newf(errs.CodeInvalidInput, "unsupported action type: %s", actionType)
	}

	if phase == action.PhaseToggle {
		if c.held.isDown(button) {
			phase = action.PhaseUp
		} else {
			phase = action.PhaseDown
		}
	}

	switch phase {
	case action.PhaseClick:
		if c.held.isDown(button) {
			return c.releaseButton(conn, p, button, modifiers)
		}

		// Any other button still held belongs to an earlier action, so it is
		// released first: pressing a second button over it would turn a stray
		// press into a chord.
		if err := c.held.releaseAll(conn); err != nil {
			return err
		}

		return conn.clickAt(
			p, x11Button(button), restoreCursor, modifiers, c.held.ownedKeycodes(),
		)
	case action.PhaseDown:
		if err := c.held.releaseAll(conn); err != nil {
			return err
		}

		return c.pressButton(conn, p, button, modifiers)
	default:
		return c.releaseButton(conn, p, button, modifiers)
	}
}

// pressButton presses button at p and keeps its modifier hold for the release
// that ends the drag.
func (c *Client) pressButton(
	conn *x11Conn,
	p image.Point,
	button action.MouseButton,
	modifiers action.Modifiers,
) error {
	hold := conn.holdModifiers(modifiers, c.held.ownedKeycodes())

	if err := conn.move(p); err != nil {
		_ = hold.release()

		return err
	}

	if err := conn.buttonEvent(x11Button(button), true); err != nil {
		_ = hold.release()

		return err
	}

	if err := conn.flush(); err != nil {
		_ = hold.release()

		return err
	}

	// Only a press that reached the server keeps its hold: nothing will come to
	// release a press that never happened, so a failure has to undo its own
	// modifier state rather than leave keys stuck down.
	c.held.record(button, hold)

	return nil
}

// releaseButton releases button at p, undoing the modifier state its press took.
//
// A release with no press behind it (a bare mouse-up, or one whose press this
// client never made) still presents modifiers, for the event alone: the caller
// asked for that state, and there is no earlier hold to reuse.
func (c *Client) releaseButton(
	conn *x11Conn,
	p image.Point,
	button action.MouseButton,
	modifiers action.Modifiers,
) error {
	state, wasHeld := c.held.take(button)

	hold := state.hold
	if !wasHeld {
		hold = conn.holdModifiers(modifiers, c.held.ownedKeycodes())
	}

	if err := conn.move(p); err != nil {
		_ = hold.release()

		return err
	}

	if err := conn.buttonEvent(x11Button(button), false); err != nil {
		_ = hold.release()

		return err
	}

	if err := conn.flush(); err != nil {
		_ = hold.release()

		return err
	}

	return hold.release()
}

// Scroll emits a wheel scroll at the pointer's position.
//
// deltaY is positive up and deltaX is positive left, the library's shared scroll
// convention. The path depends on the session: the uinput virtual wheel is
// preferred whenever it can be opened, because it carries sub-notch
// high-resolution deltas on a modern kernel and therefore reproduces smooth
// scrolling; libei is the Wayland fallback when uinput is unavailable; X11's
// wheel buttons are the last resort, where each axis becomes whole notches (30
// pixels per notch, rounded to the nearest, at least one, capped) because the
// core protocol has no sub-notch event.
//
// A scroll first releases any button this client still holds, so a wheel event
// cannot arrive in the middle of a drag.
func (c *Client) Scroll(deltaX, deltaY int, modifiers action.Modifiers) error {
	if c.closed.Load() {
		return errClientClosed()
	}

	if _, err := c.alternates.uinputDevice(); err == nil {
		return c.uinputScroll(deltaX, deltaY, modifiers)
	}

	if onWayland() {
		return c.waylandScroll(deltaX, deltaY, modifiers)
	}

	return c.withX11(func(conn *x11Conn) error {
		if err := c.held.releaseAll(conn); err != nil {
			return err
		}

		return conn.scroll(deltaX, deltaY, modifiers, c.held.ownedKeycodes())
	})
}

// MoveMouse moves the pointer to p.
//
// bypassSmooth is accepted and ignored: both synthetic paths post one
// instantaneous move, so there is no smoothing to bypass. On a Wayland session
// the move goes through libei; a failure is silent because the method has no
// error result, and the next PerformAction reports the session's real state.
func (c *Client) MoveMouse(p image.Point, _ bool) {
	if onWayland() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if client, err := c.alternates.waylandClient(ctx); err == nil {
			_ = c.waylandMove(client, p)
		}

		return
	}

	_ = c.withX11(func(conn *x11Conn) error {
		return conn.move(p)
	})
}

// CursorPosition returns the pointer's position.
//
// On Wayland no client can query the global pointer, so the answer is the
// position this client last moved to, seeded from XWayland's pointer on first
// use; it is the zero point when neither is known. On X11 it is XQueryPointer's
// answer.
func (c *Client) CursorPosition() image.Point {
	if onWayland() {
		position, known := c.trackedPoint()
		if known {
			return position
		}

		return image.Point{}
	}

	position := image.Point{}

	_ = c.withX11(func(conn *x11Conn) error {
		position = conn.cursorPosition()

		return nil
	})

	return position
}

// Compile-time proof that this backend satisfies the contract.
var _ ax.Client = (*Client)(nil)
