//go:build linux

package linux

import (
	"errors"
	"image"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
)

// This file injects pointer, wheel and keyboard input through the X server's
// XTEST extension. XTEST is the only cgo-free way to move the pointer and press
// a button on X11: it takes a Display* and asks the server to synthesize the
// event as if the hardware had produced it, so the receiving client cannot tell
// it apart from a real click.
//
// The Xlib and XTest entry points are bound with purego, which calls them
// through generated assembly trampolines. Nothing here needs a C toolchain, and
// nothing here links against libc at build time: the libraries are opened at run
// time on first use.
//
// X11 button numbers are not the domain's: 1 is left, 2 is middle and 3 is
// right, while 4..7 are the two wheel axes, which X models as buttons that are
// pressed and released once per notch.

// Xlib and XTest function signatures. Pointers are uintptr because that is what
// purego passes to a C pointer argument, and Display, Window, Atom and KeySym
// are all pointer-sized or smaller on every Linux ABI.
type x11Funcs struct {
	openDisplay       func(name uintptr) uintptr
	closeDisplay      func(display uintptr) int32
	flush             func(display uintptr) int32
	defaultRootWindow func(display uintptr) uintptr
	getGeometry       func(display, drawable uintptr, root *uintptr, x, y *int32, width, height, border, depth *uint32) int32
	queryPointer      func(display, window uintptr, root, child *uintptr, rootX, rootY, winX, winY *int32, mask *uint32) int32
	keysymToKeycode   func(display uintptr, keysym uintptr) uint8
	queryKeymap       func(display uintptr, keymap unsafe.Pointer) int32
	fakeMotion        func(display uintptr, screen int32, x, y int32, delay uintptr) int32
	fakeButton        func(display uintptr, button uint32, press int32, delay uintptr) int32
	fakeKey           func(display uintptr, keycode uint32, press int32, delay uintptr) int32
}

// x11Libraries is the pair of shared objects input injection needs, opened once
// per process and never closed: a library handle is cheap to keep and the whole
// point of caching it is that a second injection must not pay for a dlopen.
type x11Libraries struct {
	x11  *native.Library
	xtst *native.Library
	fn   x11Funcs
}

var (
	x11Once     sync.Once
	x11Loaded   *x11Libraries
	x11LoadFail error
)

// loadX11 opens and binds libX11 and libXtst, once.
//
// The soname comes first and the unversioned name second, because a system with
// only the development symlink installed (or only the runtime package) should
// still work. A failure is remembered: the libraries do not appear while the
// process runs.
func loadX11() (*x11Libraries, error) {
	x11Once.Do(func() {
		x11Loaded, x11LoadFail = openX11Libraries()
	})

	return x11Loaded, x11LoadFail
}

// openX11Libraries opens both libraries and binds every symbol this backend
// calls.
func openX11Libraries() (*x11Libraries, error) {
	x11, err := native.Open("libX11.so.6", "libX11.so")
	if err != nil {
		return nil, err
	}

	xtst, err := native.Open("libXtst.so.6", "libXtst.so")
	if err != nil {
		_ = x11.Close()

		return nil, err
	}

	libraries := &x11Libraries{x11: x11, xtst: xtst}

	if err := libraries.bind(); err != nil {
		_ = x11.Close()
		_ = xtst.Close()

		return nil, err
	}

	return libraries, nil
}

// bind resolves every symbol the backend uses. The table keeps the symbol name
// next to the function variable it fills, so a missing one is reported by name
// rather than as a nil call later.
func (l *x11Libraries) bind() error {
	bindings := []struct {
		library *native.Library
		symbol  string
		target  any
	}{
		{l.x11, "XOpenDisplay", &l.fn.openDisplay},
		{l.x11, "XCloseDisplay", &l.fn.closeDisplay},
		{l.x11, "XFlush", &l.fn.flush},
		{l.x11, "XDefaultRootWindow", &l.fn.defaultRootWindow},
		{l.x11, "XGetGeometry", &l.fn.getGeometry},
		{l.x11, "XQueryPointer", &l.fn.queryPointer},
		{l.x11, "XKeysymToKeycode", &l.fn.keysymToKeycode},
		{l.x11, "XQueryKeymap", &l.fn.queryKeymap},
		{l.xtst, "XTestFakeMotionEvent", &l.fn.fakeMotion},
		{l.xtst, "XTestFakeButtonEvent", &l.fn.fakeButton},
		{l.xtst, "XTestFakeKeyEvent", &l.fn.fakeKey},
	}

	for _, binding := range bindings {
		if err := binding.library.Func(binding.symbol, binding.target); err != nil {
			return err
		}
	}

	return nil
}

// x11Conn is one connection to the X server, used for input injection and for
// the two screen queries the contract needs.
type x11Conn struct {
	display uintptr
	root    uintptr
	fn      *x11Funcs
}

// isWaylandSession reports whether this session is a Wayland one.
//
// XDG_SESSION_TYPE is what the display manager sets. WAYLAND_DISPLAY is checked
// as well, because a session started from a nested compositor often has the
// former unset while the latter proves the compositor is Wayland.
func isWaylandSession() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")), "wayland") {
		return true
	}

	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// openX11Conn connects to the display $DISPLAY names.
//
// On a Wayland session the X server that answers is XWayland, which serves only
// X11 clients: an XTEST event injected there reaches XWayland windows and nothing
// else, so a native Wayland application never sees it. That is a limitation of
// the injection mechanism rather than of this client, and it is reported as
// CodeNotSupported so a caller can fall back instead of believing a click
// happened.
func openX11Conn() (*x11Conn, error) {
	displayName := os.Getenv("DISPLAY")

	if displayName == "" {
		if isWaylandSession() {
			return nil, errs.New(
				errs.CodeNotSupported,
				"pointer injection needs an X11 display: this is a Wayland session with "+
					"DISPLAY unset, and XTest can only reach XWayland clients",
			)
		}

		return nil, errs.New(
			errs.CodeNotSupported,
			"pointer injection needs an X11 display and DISPLAY is not set",
		)
	}

	libraries, err := loadX11()
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeNotSupported,
			"pointer injection needs libX11 and libXtst, which could not be loaded",
		)
	}

	// A null display name makes Xlib read $DISPLAY itself, which is both simpler
	// and more correct than re-parsing it here (a remote or proxy display string
	// is not always parseable by hand).
	display := libraries.fn.openDisplay(0)
	if display == 0 {
		if isWaylandSession() {
			return nil, errs.Newf(
				errs.CodeNotSupported,
				"pointer injection needs an X11 display: DISPLAY=%q could not be opened on "+
					"this Wayland session, and XTest can only reach XWayland clients",
				displayName,
			)
		}

		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not open the X display %q for pointer injection",
			displayName,
		)
	}

	conn := &x11Conn{display: display, fn: &libraries.fn}
	conn.root = conn.fn.defaultRootWindow(display)

	if conn.root == 0 {
		conn.close()

		return nil, errs.New(
			errs.CodeAccessibilityFailed,
			"the X display has no default root window",
		)
	}

	return conn, nil
}

// close releases the X connection. The libraries stay loaded for the process, so
// a later client does not pay for another dlopen.
func (c *x11Conn) close() error {
	if c.display == 0 {
		return nil
	}

	display := c.display
	c.display = 0

	if c.fn.closeDisplay(display) == 0 {
		return errs.New(errs.CodeAccessibilityFailed, "could not close the X display")
	}

	return nil
}

// flush hands every queued request to the server. XTest calls only queue events,
// so an injection that forgets this returns success while nothing happened.
func (c *x11Conn) flush() error {
	if c.fn.flush(c.display) == 0 {
		return errs.New(errs.CodeAccessibilityFailed, "could not flush the X connection")
	}

	return nil
}

// move places the pointer at p.
//
// The screen number is -1, which XTest documents as "the screen the pointer is
// already on". That is exactly right for a multi-monitor X screen: absolute
// coordinates are screen coordinates, and the event must be delivered to
// whichever screen currently owns the pointer.
func (c *x11Conn) move(p image.Point) error {
	if c.fn.fakeMotion(c.display, -1, int32(p.X), int32(p.Y), 0) == 0 {
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not move the X pointer to (%d, %d)",
			p.X,
			p.Y,
		)
	}

	return c.flush()
}

// buttonEvent presses or releases one X button.
func (c *x11Conn) buttonEvent(button uint32, press bool) error {
	pressed := int32(0)
	if press {
		pressed = 1
	}

	if c.fn.fakeButton(c.display, button, pressed, 0) == 0 {
		action := "release"
		if press {
			action = "press"
		}

		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not %s X button %d", action, button,
		)
	}

	return nil
}

// keyEvent presses or releases one keycode.
func (c *x11Conn) keyEvent(keycode uint32, press bool) error {
	pressed := int32(0)
	if press {
		pressed = 1
	}

	if c.fn.fakeKey(c.display, keycode, pressed, 0) == 0 {
		action := "release"
		if press {
			action = "press"
		}

		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not %s X keycode %d", action, keycode,
		)
	}

	return nil
}

// cursorPosition reads the pointer position from the root window's perspective.
func (c *x11Conn) cursorPosition() image.Point {
	var (
		root, child      uintptr
		rootX, rootY     int32
		windowX, windowY int32
		mask             uint32
	)

	if c.fn.queryPointer(
		c.display, c.root, &root, &child, &rootX, &rootY, &windowX, &windowY, &mask,
	) == 0 {
		return image.Point{}
	}

	return image.Pt(int(rootX), int(rootY))
}

// screenBounds returns the root window's rectangle, which is the whole X screen.
func (c *x11Conn) screenBounds() (image.Rectangle, bool) {
	var (
		root          uintptr
		x, y          int32
		width, height uint32
		border, depth uint32
	)

	if c.fn.getGeometry(
		c.display, c.root, &root, &x, &y, &width, &height, &border, &depth,
	) == 0 {
		return image.Rectangle{}, false
	}

	if width == 0 || height == 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(0, 0, int(width), int(height)), true
}

// X11 button numbers. The wheel's are buttons rather than axes: one press and
// release is one notch.
const (
	x11ButtonLeft   = uint32(1)
	x11ButtonMiddle = uint32(2)
	x11ButtonRight  = uint32(3)

	x11WheelUp    = uint32(4)
	x11WheelDown  = uint32(5)
	x11WheelLeft  = uint32(6)
	x11WheelRight = uint32(7)
)

// x11Button maps a domain mouse button to its X11 number.
func x11Button(button action.MouseButton) uint32 {
	switch button {
	case action.ButtonRight:
		return x11ButtonRight
	case action.ButtonMiddle:
		return x11ButtonMiddle
	case action.ButtonLeft:
		return x11ButtonLeft
	default:
		return x11ButtonLeft
	}
}

// X keysyms for the modifier keys, from X11/keysymdef.h. Both sides of each
// modifier are listed because either one presents it, so suppressing a modifier
// the user holds means suppressing whichever side they hold.
const (
	xkShiftLeft    = uintptr(0xffe1)
	xkShiftRight   = uintptr(0xffe2)
	xkControlLeft  = uintptr(0xffe3)
	xkControlRight = uintptr(0xffe4)
	xkAltLeft      = uintptr(0xffe9)
	xkAltRight     = uintptr(0xffea)
	xkSuperLeft    = uintptr(0xffeb)
	xkSuperRight   = uintptr(0xffec)
	xkMetaLeft     = uintptr(0xffe7)
	xkMetaRight    = uintptr(0xffe8)
)

// modifierKeysym is one physical key that presents a modifier.
type modifierKeysym struct {
	keysym   uintptr
	modifier action.Modifiers
}

// modifierKeysyms lists every key that presents a modifier, canonical side
// first. Meta comes last because XKeysymToKeycode usually answers it with a
// keycode another entry already claimed, and a keycode is only acted on once.
var modifierKeysyms = []modifierKeysym{
	{xkShiftLeft, action.ModShift},
	{xkShiftRight, action.ModShift},
	{xkControlLeft, action.ModCtrl},
	{xkControlRight, action.ModCtrl},
	{xkAltLeft, action.ModAlt},
	{xkAltRight, action.ModAlt},
	{xkSuperLeft, action.ModCmd},
	{xkSuperRight, action.ModCmd},
	{xkMetaLeft, action.ModCmd},
	{xkMetaRight, action.ModCmd},
}

// modifierHold is the modifier state one injection falsified, so it can be put
// back: what the injection pressed, and what it released so the injection would
// not carry it.
type modifierHold struct {
	conn     *x11Conn
	pressed  []uint32
	released []uint32
}

// holdModifiers makes the X server present exactly modifiers for the duration of
// one injection.
//
// An X11 button event carries the server's live modifier state, so a modifier
// the user physically holds is delivered with the click. A plain scroll bound to
// ctrl+j would then arrive as a modified scroll, which most applications read as
// zoom. XTEST has no equivalent of macOS's CGEventSetFlags, so the keyboard has
// to actually hold the requested set and let go of the rest.
//
// owned lists keycodes another action in this client already holds; those are
// left alone so a scroll during a modified drag cannot lift the drag's modifier.
//
// A requested modifier the keymap has no key for (cmd on a keyboard with no
// Super key) is silently not presented: the click is still worth more than a
// refusal.
func (c *x11Conn) holdModifiers(modifiers action.Modifiers, owned map[uint32]struct{}) modifierHold {
	hold := modifierHold{conn: c}

	keymap, keymapKnown := c.heldKeycodes()
	seen := make(map[uint32]struct{}, len(modifierKeysyms))

	for _, entry := range modifierKeysyms {
		keycode := uint32(c.fn.keysymToKeycode(c.display, entry.keysym))
		if keycode == 0 {
			continue
		}

		if _, duplicate := seen[keycode]; duplicate {
			continue
		}

		seen[keycode] = struct{}{}

		if _, held := owned[keycode]; held {
			continue
		}

		held := keymapKnown && keymap[keycode/8]&(1<<(keycode%8)) != 0

		switch {
		case modifiers.Has(entry.modifier) && !held:
			if c.keyEvent(keycode, true) == nil {
				hold.pressed = append(hold.pressed, keycode)
			}
		case !modifiers.Has(entry.modifier) && held:
			if c.keyEvent(keycode, false) == nil {
				hold.released = append(hold.released, keycode)
			}
		}
	}

	if len(hold.pressed) > 0 || len(hold.released) > 0 {
		_ = c.flush()
	}

	return hold
}

// release puts the modifier state back: what this hold pressed is released, and
// what it suppressed is pressed again.
//
// Restoring is unconditional, which is the safer bias. A key the user lets go of
// mid-injection is pressed back once and then reads as held until they press and
// release it again; the opposite bias would drop a modifier they are holding out
// of everything they do next.
func (h modifierHold) release() error {
	if len(h.pressed) == 0 && len(h.released) == 0 {
		return nil
	}

	var failures []error

	// Releases walk backwards so a chord comes apart in the reverse of the order
	// it went down.
	for index := len(h.pressed) - 1; index >= 0; index-- {
		if err := h.conn.keyEvent(h.pressed[index], false); err != nil {
			failures = append(failures, err)
		}
	}

	for index := len(h.released) - 1; index >= 0; index-- {
		if err := h.conn.keyEvent(h.released[index], true); err != nil {
			failures = append(failures, err)
		}
	}

	if err := h.conn.flush(); err != nil {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

// heldKeycodes reads the server's keymap, which has one bit per keycode for
// "this key is down". It is a round trip, so it is read once per injection.
func (c *x11Conn) heldKeycodes() ([32]byte, bool) {
	var keymap [32]byte

	if c.fn.queryKeymap(c.display, unsafe.Pointer(&keymap[0])) == 0 {
		return keymap, false
	}

	return keymap, true
}

// clickAt moves to p, presses and releases button, and optionally puts the
// pointer back where it was.
//
// The whole gesture happens on one connection with one flush at the end, so no
// other input can slip between the move and the click.
func (c *x11Conn) clickAt(
	p image.Point,
	button uint32,
	restoreCursor bool,
	modifiers action.Modifiers,
	owned map[uint32]struct{},
) (err error) {
	original := c.cursorPosition()

	hold := c.holdModifiers(modifiers, owned)
	defer func() { err = errors.Join(err, hold.release()) }()

	if err := c.move(p); err != nil {
		return err
	}

	if err := c.buttonEvent(button, true); err != nil {
		return err
	}

	if err := c.buttonEvent(button, false); err != nil {
		return err
	}

	if err := c.flush(); err != nil {
		return err
	}

	if restoreCursor {
		_ = c.move(original)
	}

	return nil
}

// scroll emits whole wheel notches, holding the requested modifiers across them.
//
// deltaY is positive up and deltaX is positive left, the library's shared scroll
// convention, which is the opposite of X11's wheel numbering: button 4 is up and
// button 6 is left, so a positive delta selects the lower-numbered button of each
// pair.
//
// Smooth scrolling is not reproduced. X11's core protocol has no sub-notch wheel
// event, and the XTEST pointer exposes only relative X and Y motion, so a pixel
// delta can only become a whole number of notches (see scrollNotches).
//
// A scroll with nothing to travel does not touch the keyboard: taking a modifier
// hold would release and re-press whatever the user is holding, for an event
// that never happens.
func (c *x11Conn) scroll(
	deltaX, deltaY int,
	modifiers action.Modifiers,
	owned map[uint32]struct{},
) (err error) {
	if deltaX == 0 && deltaY == 0 {
		return nil
	}

	hold := c.holdModifiers(modifiers, owned)
	defer func() { err = errors.Join(err, hold.release()) }()

	if deltaY != 0 {
		button := x11WheelUp
		if deltaY < 0 {
			button = x11WheelDown
		}

		if err := c.wheel(button, scrollNotches(deltaY)); err != nil {
			return err
		}
	}

	if deltaX != 0 {
		button := x11WheelLeft
		if deltaX < 0 {
			button = x11WheelRight
		}

		if err := c.wheel(button, scrollNotches(deltaX)); err != nil {
			return err
		}
	}

	return c.flush()
}

// wheel presses and releases one wheel button the given number of times.
func (c *x11Conn) wheel(button uint32, notches int) error {
	for range notches {
		if err := c.buttonEvent(button, true); err != nil {
			return err
		}

		if err := c.buttonEvent(button, false); err != nil {
			return err
		}
	}

	return nil
}

// heldButton is one button this client pressed and has not released, with the
// modifier state its press falsified. The press and the release can be separate
// calls (a drag), so that state has to outlive the call that took it.
type heldButton struct {
	hold modifierHold
}

// heldButtons records the buttons this client is holding.
type heldButtons struct {
	mu   sync.Mutex
	down map[action.MouseButton]*heldButton
}

// newHeldButtons returns an empty button record.
func newHeldButtons() *heldButtons {
	return &heldButtons{down: make(map[action.MouseButton]*heldButton)}
}

// isDown reports whether this client holds button.
func (h *heldButtons) isDown(button action.MouseButton) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	_, held := h.down[button]

	return held
}

// record remembers a press and the modifier state it took.
func (h *heldButtons) record(button action.MouseButton, hold modifierHold) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.down[button] = &heldButton{hold: hold}
}

// take removes and returns the record of a held button, if there is one.
func (h *heldButtons) take(button action.MouseButton) (heldButton, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state, held := h.down[button]
	if !held {
		return heldButton{}, false
	}

	delete(h.down, button)

	return *state, true
}

// ownedKeycodes returns the keycodes held buttons pressed, so an injection made
// while a drag is in progress neither re-presses nor releases them.
func (h *heldButtons) ownedKeycodes() map[uint32]struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()

	owned := make(map[uint32]struct{}, len(h.down)*2)

	for _, state := range h.down {
		for _, keycode := range state.hold.pressed {
			owned[keycode] = struct{}{}
		}
	}

	return owned
}

// releaseAll lets go of every button this client holds, ending any drag in
// progress, and reports every failure while still releasing the rest.
//
// Stopping at the first failure would leave a button down on the desktop, which
// is the one outcome a mouse driver must never produce.
func (h *heldButtons) releaseAll(conn *x11Conn) error {
	var failures []error

	for {
		h.mu.Lock()

		var (
			button action.MouseButton
			state  *heldButton
		)

		for pressed, held := range h.down {
			button, state = pressed, held

			break
		}

		if state == nil {
			h.mu.Unlock()

			break
		}

		delete(h.down, button)
		h.mu.Unlock()

		if err := conn.buttonEvent(x11Button(button), false); err != nil {
			failures = append(failures, err)
		}

		if err := state.hold.release(); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}
