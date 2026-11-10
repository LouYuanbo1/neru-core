//go:build linux

package wlrinject

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The failures a caller can act on, phrased once so every path says the same
// thing.
const (
	// noProtocolsMessage names both protocols and the compositors that expose
	// them, because the alternative on the ones that do not is the portal.
	noProtocolsMessage = "the Wayland compositor implements neither " +
		"wlr-virtual-pointer-unstable-v1 nor virtual-keyboard-unstable-v1, so it " +
		"cannot be driven through the wlroots input protocols; sway, niri, " +
		"Hyprland and wayfire expose them, while KDE, Mutter and cosmic need the " +
		"RemoteDesktop portal (libei) path instead"

	// noSeatMessage explains why a keyboard manager alone is useless.
	noSeatMessage = "the Wayland compositor exposes " +
		"zwp_virtual_keyboard_manager_v1 but no wl_seat, so a virtual keyboard " +
		"cannot be attached"

	noPointerMessage = "this compositor exposes no wlr virtual pointer " +
		"(zwlr_virtual_pointer_manager_v1), so pointer events cannot be injected"

	noKeyboardMessage = "this compositor exposes no wlr virtual keyboard " +
		"(zwp_virtual_keyboard_manager_v1), so key and modifier events cannot be " +
		"injected"

	closedMessage = "the wlroots input client is closed"
)

// Client is one connection to a Wayland compositor, holding a virtual pointer
// and a virtual keyboard.
//
// A Client is safe for concurrent use: every method takes one mutex, because a
// single connection carries both devices and two interleaved gestures would
// merge into one frame. Calls therefore serialize, which is what one input path
// wants anyway. The zero value is not usable; call Dial.
//
// A Client is not reconnectable. A protocol error, a broken socket or a timeout
// poisons it: every later call returns the same error. The caller builds a new
// Client, which re-reads the output layout.
type Client struct {
	// mu serializes whole exchanges, including the sync roundtrip that ends
	// each one.
	mu sync.Mutex

	conn *net.UnixConn
	// reader frames messages; only the goroutine holding mu reads it.
	reader messageReader

	// closeOnce makes the socket close idempotent even when Dial failed halfway
	// through.
	closeOnce sync.Once

	// nextID is the next client-side object id. Ids are never reused.
	nextID uint32
	// start is the clock key and key events are timestamped against. The
	// protocol only requires one clock per object.
	start time.Time

	closed bool
	// poison is the first fatal error. Once set, no method touches the socket
	// again.
	poison error

	// seats are all bound wl_seat objects, in registry order.
	seats []seatBinding
	// seat is the chosen seat: the first with a pointer capability, or the
	// first seat when none has one (the keyboard needs some seat).
	seat uint32
	// seatVersion is the chosen seat's advertised version, which is what decides
	// whether the compositor's wl_pointer supports axis_value120.
	seatVersion uint32
	// seatHasPointer records that the chosen seat really advertised a pointer.
	// Only such a seat is suggested to create_virtual_pointer; otherwise the
	// protocol's nullable seat argument is used.
	seatHasPointer bool
	// highResAxis is true when the compositor can deliver wl_pointer v8's
	// axis_value120, which changes which axis request Scroll sends.
	highResAxis bool

	// layout is the union of the output rectangles, in logical pixels, and the
	// frame MoveTo normalizes against.
	layout   outputRect
	layoutOK bool

	// pointerManager, keyboardManager, pointer and keyboard are protocol object
	// ids. Zero means the object does not exist.
	pointerManager  uint32
	keyboardManager uint32
	pointer         uint32
	keyboard        uint32

	// modifiers is the XKB mod mask last reported to the compositor. The
	// virtual keyboard protocol does not derive modifier state from key events,
	// so this client has to.
	modifiers uint32
	// heldButtons and heldKeys are the codes this client pressed and has not
	// released, so Close can release them instead of leaving a button or key
	// stuck down.
	heldButtons map[uint32]struct{}
	heldKeys    map[uint32]struct{}
}

// seatBinding is one bound wl_seat and the capability state read from it.
type seatBinding struct {
	object       uint32
	version      uint32 // version bound
	advertised   uint32 // version from the registry
	capabilities uint32
}

// outputState accumulates the wl_output events for one bound output.
type outputState struct {
	x, y int

	modeWidth, modeHeight int
	hasMode               bool

	scale int
}

// rect is the output's logical rectangle in the compositor's layout. A mode
// that was never reported cannot describe a size, so such an output is omitted.
// Fractional scales round to the nearest logical pixel.
func (o *outputState) rect() (outputRect, bool) {
	if !o.hasMode || o.modeWidth <= 0 || o.modeHeight <= 0 {
		return outputRect{}, false
	}

	scale := max(o.scale, 1)

	return outputRect{
		X:      o.x,
		Y:      o.y,
		Width:  (o.modeWidth + scale/2) / scale,
		Height: (o.modeHeight + scale/2) / scale,
	}, true
}

// apply consumes one event addressed to this output.
func (o *outputState) apply(ev event) error {
	switch ev.opcode {
	case outputEventGeometry:
		d := decoder{buf: ev.body}

		x, err := d.int32()
		if err != nil {
			return err
		}

		y, err := d.int32()
		if err != nil {
			return err
		}

		// Physical size and subpixel order are not needed.
		for range 3 {
			if _, err := d.int32(); err != nil {
				return err
			}
		}

		// The make and model name the output for diagnostics this package never
		// prints, but their strings still have to be consumed to stay in step
		// with the event's arguments.
		if _, err := d.string(); err != nil {
			return err
		}

		if _, err := d.string(); err != nil {
			return err
		}

		if _, err := d.int32(); err != nil { // transform
			return err
		}

		o.x, o.y = int(x), int(y)
	case outputEventMode:
		values, err := decodeUint32s(ev.body, 4)
		if err != nil {
			return err
		}

		flags := values[0]

		// Only the current mode describes the output; the others are extra
		// resolutions the compositor happens to advertise.
		if flags&outputModeCurrent != 0 || !o.hasMode {
			o.modeWidth, o.modeHeight = int(int32(values[1])), int(int32(values[2]))
			o.hasMode = true
		}
	case outputEventScale:
		values, err := decodeUint32s(ev.body, 1)
		if err != nil {
			return err
		}

		o.scale = max(int(int32(values[0])), 1)
	case outputEventDone:
		// Nothing to finalize: the roundtrip that delivered this event is what
		// guarantees the initial burst is complete.
	}

	return nil
}

// Available reports whether $WAYLAND_DISPLAY names a reachable socket: a
// filesystem socket that exists, or an abstract socket this process can
// connect to. It performs no Wayland roundtrip and creates no protocol object,
// so it is safe as a capability check. A compositor that is reachable but
// implements neither wlroots protocol still answers true here and refuses at
// Dial.
func Available() bool {
	path, err := displaySocket(os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return false
	}

	if strings.HasPrefix(path, "@") {
		conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
		if err != nil {
			return false
		}

		_ = conn.Close()

		return true
	}

	info, err := os.Stat(path)

	return err == nil && info.Mode()&os.ModeSocket != 0
}

// displaySocket resolves a $WAYLAND_DISPLAY value to the unix socket address
// Wayland names. An absolute path is used as-is. A relative name lives in
// $XDG_RUNTIME_DIR, as the Wayland client library assumes. A name beginning
// with '@' is an abstract socket and is passed through for net to interpret.
func displaySocket(name string) (string, error) {
	if name == "" {
		return "", errs.New(
			errs.CodeNotSupported,
			"the wlroots input path needs a Wayland session, but WAYLAND_DISPLAY is not set",
		)
	}

	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "@") {
		return name, nil
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", errs.New(
			errs.CodeNotSupported,
			"the wlroots input path needs a Wayland session, but XDG_RUNTIME_DIR is not set and WAYLAND_DISPLAY is not an absolute path",
		)
	}

	return filepath.Join(runtimeDir, name), nil
}

// Dial connects to the compositor, binds wl_seat, wl_output,
// zwlr_virtual_pointer_manager_v1 and zwp_virtual_keyboard_manager_v1, creates
// the virtual devices, and uploads the virtual keyboard's keymap.
//
// It succeeds when at least one of the two devices could be created: a
// compositor that exposes only one manager yields a Client whose HasPointer or
// HasKeyboard is false, and the other device keeps working. It returns
// errs.CodeNotSupported when the compositor exposes neither protocol, when it
// exposes only the keyboard manager without a seat, or when there is no Wayland
// socket to connect to, and it names the missing protocol or setting.
// errs.CodeAccessibilityFailed is a protocol or transport failure, and
// errs.CodeTimeout means the compositor did not finish the exchange within the
// internal two-second bound.
//
// The call never prompts: these protocols have no consent dialog. It does read
// the output layout, so a session whose outputs change afterwards needs a new
// Client.
func Dial() (*Client, error) {
	path, err := displaySocket(os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{Timeout: opTimeout}

	raw, err := dialer.Dial("unix", path)
	if err != nil {
		return nil, errs.Wrapf(
			err,
			errs.CodeNotSupported,
			"the wlroots input path could not connect to the Wayland compositor socket %q",
			path,
		)
	}

	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()

		return nil, errs.New(
			errs.CodeNotSupported,
			"the Wayland compositor address is not a unix socket",
		)
	}

	client := &Client{
		conn:        conn,
		reader:      messageReader{conn: conn},
		nextID:      2,
		start:       time.Now(),
		heldButtons: make(map[uint32]struct{}),
		heldKeys:    make(map[uint32]struct{}),
	}

	if err := client.discover(); err != nil {
		client.closeSocket()

		return nil, err
	}

	return client, nil
}

// discover runs the whole handshake: registry, binds, capabilities, layout and
// device creation. Any error poisons the connection.
func (c *Client) discover() error {
	if err := c.startExchange(); err != nil {
		return err
	}

	registry := c.newID()

	var body encoder

	body.uint32(registry)

	if err := c.send(displayID, displayOpGetRegistry, body.buf); err != nil {
		return err
	}

	var globals []registryGlobal

	if err := c.roundtrip(func(ev event) error {
		if ev.object != registry || ev.opcode != registryEventGlobal {
			return nil
		}

		global, err := parseRegistryGlobal(ev.body)
		if err != nil {
			return err
		}

		globals = append(globals, global)

		return nil
	}); err != nil {
		return err
	}

	var seatGlobals, outputGlobals []registryGlobal

	for _, global := range globals {
		switch global.iface {
		case interfaceVirtualPointerManager:
			if c.pointerManager != 0 {
				continue
			}

			version := min(global.version, pointerManagerVersionCap)
			if version == 0 {
				continue
			}

			id := c.newID()
			if err := c.bind(registry, global.name, interfaceVirtualPointerManager, version, id); err != nil {
				return err
			}

			c.pointerManager = id
		case interfaceVirtualKeyboardManager:
			if c.keyboardManager != 0 {
				continue
			}

			version := min(global.version, keyboardManagerVersionCap)
			if version == 0 {
				continue
			}

			id := c.newID()
			if err := c.bind(registry, global.name, interfaceVirtualKeyboardManager, version, id); err != nil {
				return err
			}

			c.keyboardManager = id
		case interfaceWLSeat:
			seatGlobals = append(seatGlobals, global)
		case interfaceWLOutput:
			outputGlobals = append(outputGlobals, global)
		}
	}

	if c.pointerManager == 0 && c.keyboardManager == 0 {
		return errs.New(errs.CodeNotSupported, noProtocolsMessage)
	}

	for _, global := range seatGlobals {
		version := min(global.version, seatVersionCap)
		if version == 0 {
			continue
		}

		id := c.newID()

		if err := c.bind(registry, global.name, interfaceWLSeat, version, id); err != nil {
			return err
		}

		c.seats = append(c.seats, seatBinding{
			object:     id,
			version:    version,
			advertised: global.version,
		})
	}

	outputStates := make(map[uint32]*outputState, len(outputGlobals))

	for _, global := range outputGlobals {
		version := min(global.version, outputVersionCap)
		if version == 0 {
			continue
		}

		id := c.newID()

		if err := c.bind(registry, global.name, interfaceWLOutput, version, id); err != nil {
			return err
		}

		outputStates[id] = &outputState{scale: 1}
	}

	// A second roundtrip flushes the binds and collects every initial event:
	// wl_seat.capabilities and the wl_output geometry/mode/scale burst.
	err := c.roundtrip(func(ev event) error {
		if state, ok := outputStates[ev.object]; ok {
			return state.apply(ev)
		}

		for index := range c.seats {
			if c.seats[index].object != ev.object {
				continue
			}

			if ev.opcode == seatEventCapabilities {
				values, err := decodeUint32s(ev.body, 1)
				if err != nil {
					return err
				}

				c.seats[index].capabilities = values[0]
			}

			return nil
		}

		return nil
	})
	if err != nil {
		return err
	}

	c.chooseSeat()

	rects := make([]outputRect, 0, len(outputStates))

	for _, state := range outputStates {
		if rect, ok := state.rect(); ok {
			rects = append(rects, rect)
		}
	}

	c.layout, c.layoutOK = layoutBounds(rects)

	pointerSeat := uint32(0)
	if c.seatHasPointer {
		pointerSeat = c.seat
	}

	if c.pointerManager != 0 {
		id := c.newID()

		var create encoder

		create.uint32(pointerSeat)
		create.uint32(id)

		if err := c.send(c.pointerManager, virtualPointerManagerOpCreate, create.buf); err != nil {
			return err
		}

		c.pointer = id
	}

	if c.keyboardManager != 0 && c.seat != 0 {
		id := c.newID()

		var create encoder

		create.uint32(c.seat)
		create.uint32(id)

		if err := c.send(c.keyboardManager, virtualKeyboardManagerOpCreate, create.buf); err != nil {
			return err
		}

		c.keyboard = id

		// The keymap must be set before the first key or modifiers request, and
		// its roundtrip also flushes the virtual pointer creation above.
		if err := c.uploadKeymap(); err != nil {
			return err
		}
	}

	if c.pointer == 0 && c.keyboard == 0 {
		if c.keyboardManager != 0 && c.seat == 0 {
			return errs.New(errs.CodeNotSupported, noSeatMessage)
		}

		return errs.New(errs.CodeNotSupported, noProtocolsMessage)
	}

	// With no keyboard there was no keymap upload to flush the pointer creation,
	// so close the exchange here.
	if c.keyboard == 0 {
		return c.roundtrip(nil)
	}

	return nil
}

// chooseSeat picks the first seat that advertises a pointer, falling back to the
// first seat so the virtual keyboard has something to attach to.
func (c *Client) chooseSeat() {
	for index := range c.seats {
		if c.seats[index].capabilities&seatCapabilityPointer == 0 {
			continue
		}

		c.seat = c.seats[index].object
		c.seatVersion = c.seats[index].advertised
		c.seatHasPointer = true

		break
	}

	if c.seat == 0 && len(c.seats) > 0 {
		c.seat = c.seats[0].object
		c.seatVersion = c.seats[0].advertised
	}

	c.highResAxis = c.seatVersion >= axisValue120Threshold
}

// bind sends wl_registry.bind for one global.
func (c *Client) bind(registry, name uint32, iface string, version, id uint32) error {
	var body encoder

	body.uint32(name)
	body.string(iface)
	body.uint32(version)
	body.uint32(id)

	return c.send(registry, registryOpBind, body.buf)
}

// HasPointer reports whether the compositor gave this client a virtual pointer.
// It answers whether the device exists, so it stays true for the life of the
// Client unless Close ran.
func (c *Client) HasPointer() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return !c.closed && c.pointer != 0
}

// HasKeyboard reports whether the compositor gave this client a virtual
// keyboard, whose keymap has already been uploaded.
func (c *Client) HasKeyboard() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return !c.closed && c.keyboard != 0
}

// MoveTo moves the virtual pointer to a desktop coordinate, in the same global
// top-left-origin, Y-down, logical-pixel space the rest of the library uses.
//
// The coordinate is normalized against the union of the outputs: the layout
// origin is subtracted (it can be negative) and the union's width and height are
// sent as the motion_absolute extents. Coordinates outside the layout clamp to
// its edge. The request is always sent, so a caller that re-asserts the same
// position still produces a fresh motion event.
//
// The gesture is one motion_absolute request followed by frame, then the sync
// roundtrip. It reports errs.CodeNotSupported when the compositor has no virtual
// pointer or advertised no output to normalize against.
func (c *Client) MoveTo(x, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.begin(); err != nil {
		return err
	}

	if c.pointer == 0 {
		return errs.New(errs.CodeNotSupported, noPointerMessage)
	}

	if !c.layoutOK {
		return errs.New(
			errs.CodeNotSupported,
			"the Wayland compositor advertised no measurable wl_output, so the virtual pointer's absolute motion has no extents to normalize against",
		)
	}

	relativeX, extentX, okX := normalizeCoordinate(x, c.layout.X, c.layout.Width)
	relativeY, extentY, okY := normalizeCoordinate(y, c.layout.Y, c.layout.Height)
	if !okX || !okY {
		return errs.New(
			errs.CodeNotSupported,
			"the Wayland output layout has no positive extent, so the virtual pointer cannot be moved",
		)
	}

	var body encoder

	body.uint32(c.timestamp())
	body.uint32(relativeX)
	body.uint32(relativeY)
	body.uint32(extentX)
	body.uint32(extentY)

	if err := c.send(c.pointer, virtualPointerOpMotionAbsolute, body.buf); err != nil {
		return err
	}

	if err := c.send(c.pointer, virtualPointerOpFrame, nil); err != nil {
		return err
	}

	return c.roundtrip(nil)
}

// Button presses or releases a mouse button.
//
// button is either the small numbering the library uses elsewhere (1 left, 2
// middle, 3 right) or the Linux evdev BTN_* code the protocol carries (272 left,
// 273 right, 274 middle). The two cannot collide, so a caller holding either
// convention gets the button it named; anything else is refused. A release that
// fails leaves the button held, which is why Close releases whatever this client
// still holds.
//
// The press or release and its frame are one gesture, confirmed by the sync
// roundtrip before the call returns.
func (c *Client) Button(button int, pressed bool) error {
	code, ok := evdevButton(button)
	if !ok {
		return errs.Newf(
			errs.CodeInvalidInput,
			"button %d is not a mouse button; pass 1/2/3 for left/middle/right or the evdev BTN_* code",
			button,
		)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.begin(); err != nil {
		return err
	}

	if c.pointer == 0 {
		return errs.New(errs.CodeNotSupported, noPointerMessage)
	}

	var body encoder

	body.uint32(c.timestamp())
	body.uint32(code)

	if pressed {
		body.uint32(pointerButtonPressed)
	} else {
		body.uint32(pointerButtonReleased)
	}

	if err := c.send(c.pointer, virtualPointerOpButton, body.buf); err != nil {
		return err
	}

	if err := c.send(c.pointer, virtualPointerOpFrame, nil); err != nil {
		return err
	}

	if err := c.roundtrip(nil); err != nil {
		return err
	}

	if pressed {
		c.heldButtons[code] = struct{}{}
	} else {
		delete(c.heldButtons, code)
	}

	return nil
}

// Scroll emits one wheel scroll. dx is positive left and dy positive up, the
// library's shared convention; Wayland's horizontal axis is positive right and
// its vertical axis is positive down, so both flip.
//
// Pixels become notches at 30 px per notch. A notch is 10 axis units, and the
// continuous value carries fractions. When the compositor's seat advertises
// version 8 or later, a whole-notch delta is sent as axis_discrete, whose
// whole-notch count wlroots turns into axis_value120 at 120 units per notch for
// v8 clients; a fractional delta keeps the plain axis event so the fraction is
// not rounded into a detent. On an older compositor every delta is a plain axis
// event. A whole notch reports the wheel axis source and a fraction reports
// continuous, which tells a toolkit the delta is not a wheel detent. A scroll
// with nothing to travel returns nil without touching the device, so a missing
// pointer is not reported for an event that would not have happened.
//
// Both axes of a diagonal scroll share one frame, after which the sync
// roundtrip confirms the compositor processed them.
func (c *Client) Scroll(dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.begin(); err != nil {
		return err
	}

	if c.pointer == 0 {
		return errs.New(errs.CodeNotSupported, noPointerMessage)
	}

	for _, axis := range scrollAxes(dx, dy) {
		value, discrete := scrollUnits(axis.pixels)

		var body encoder

		body.uint32(c.timestamp())
		body.uint32(axis.axis)
		body.fixed(value)

		source := uint32(pointerAxisSourceContinuous)

		if c.highResAxis && discrete != 0 {
			body.int32(discrete)
			source = pointerAxisSourceWheel

			if err := c.send(c.pointer, virtualPointerOpAxisDiscrete, body.buf); err != nil {
				return err
			}
		} else {
			if err := c.send(c.pointer, virtualPointerOpAxis, body.buf); err != nil {
				return err
			}
		}

		// wlroots attaches the source to the axis event most recently named, so
		// the source request follows the axis request it describes. A whole
		// notch claims the wheel; a fractional delta claims continuous, which is
		// how a toolkit is told not to snap it back to a detent.
		var sourceEvent encoder

		sourceEvent.uint32(source)

		if err := c.send(c.pointer, virtualPointerOpAxisSource, sourceEvent.buf); err != nil {
			return err
		}
	}

	if err := c.send(c.pointer, virtualPointerOpFrame, nil); err != nil {
		return err
	}

	return c.roundtrip(nil)
}

// Key presses or releases a keyboard key by Linux evdev key code, the same raw
// code wl_keyboard carries and the same convention libei and uinput use. This is
// the modifier-hold path: ctrl is 29, shift 42, alt 56 and meta 125.
//
// The virtual keyboard protocol does not derive modifier state from key events,
// so for those four modifiers (and their right-hand twins 97, 54, 100 and 126)
// the new mask is reported with zwp_virtual_keyboard_v1.modifiers before the key
// event. Other keys change no mask. The modifier bits are XKB real-modifier
// bits: shift 1<<0, control 1<<2, alt (Mod1) 1<<3, meta (Mod4) 1<<6.
//
// Every key method ends with the sync roundtrip, so the compositor has applied
// the key and its modifier state before the method returns.
func (c *Client) Key(code uint32, pressed bool) error {
	if code == 0 || code > 0x2ff {
		return errs.Newf(
			errs.CodeInvalidInput,
			"key code %d is not an evdev key code",
			code,
		)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.begin(); err != nil {
		return err
	}

	if c.keyboard == 0 {
		return errs.New(errs.CodeNotSupported, noKeyboardMessage)
	}

	mask, isModifier := modifierMask(code)
	newModifiers := c.modifiers

	if isModifier {
		if pressed {
			newModifiers |= mask
		} else {
			newModifiers &^= mask
		}

		var modifiers encoder

		modifiers.uint32(newModifiers)
		modifiers.uint32(0) // latched
		modifiers.uint32(0) // locked
		modifiers.uint32(0) // group

		if err := c.send(c.keyboard, virtualKeyboardOpModifiers, modifiers.buf); err != nil {
			return err
		}
	}

	var body encoder

	body.uint32(c.timestamp())
	body.uint32(code)

	if pressed {
		body.uint32(keyboardKeyStatePressed)
	} else {
		body.uint32(keyboardKeyStateReleased)
	}

	if err := c.send(c.keyboard, virtualKeyboardOpKey, body.buf); err != nil {
		return err
	}

	if err := c.roundtrip(nil); err != nil {
		return err
	}

	c.modifiers = newModifiers

	if pressed {
		c.heldKeys[code] = struct{}{}
	} else {
		delete(c.heldKeys, code)
	}

	return nil
}

// Close releases whatever this client still holds down, destroys the protocol
// objects, and closes the connection. It is idempotent and safe on a nil
// *Client. A poisoned Client skips the protocol teardown and just closes its
// socket.
//
// Releases go first: a held key or button outlives the device object's destroy
// on some compositors, and a stuck modifier turns every later action into a
// modified one. The requests are written before the socket closes, but no
// roundtrip waits for them, because there is nothing left to read.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true

	if c.poison == nil {
		_ = c.conn.SetWriteDeadline(time.Now().Add(opTimeout))
		c.releaseHeldInput()
		c.destroyObjects()
	}

	c.closeSocket()

	return nil
}

// releaseHeldInput sends a release for every key and button this client pressed
// and clears the modifier state. Failures are ignored: Close is already tearing
// the connection down, and the first error cannot be acted on.
func (c *Client) releaseHeldInput() {
	if c.keyboard != 0 {
		for code := range c.heldKeys {
			var body encoder

			body.uint32(c.timestamp())
			body.uint32(code)
			body.uint32(keyboardKeyStateReleased)

			_ = c.send(c.keyboard, virtualKeyboardOpKey, body.buf)
		}

		if c.modifiers != 0 {
			var body encoder

			body.uint32(0) // depressed
			body.uint32(0) // latched
			body.uint32(0) // locked
			body.uint32(0) // group

			_ = c.send(c.keyboard, virtualKeyboardOpModifiers, body.buf)
		}
	}

	if c.pointer != 0 && len(c.heldButtons) > 0 {
		for button := range c.heldButtons {
			var body encoder

			body.uint32(c.timestamp())
			body.uint32(button)
			body.uint32(pointerButtonReleased)

			_ = c.send(c.pointer, virtualPointerOpButton, body.buf)
		}

		_ = c.send(c.pointer, virtualPointerOpFrame, nil)
	}

	c.modifiers = 0
	clear(c.heldKeys)
	clear(c.heldButtons)
}

// destroyObjects sends each destructor in child-to-parent order. The virtual
// keyboard manager has no destroy request, and a wl_seat older than version 5
// has no release, so both are skipped when absent.
func (c *Client) destroyObjects() {
	if c.keyboard != 0 {
		_ = c.send(c.keyboard, virtualKeyboardOpDestroy, nil)
		c.keyboard = 0
	}

	if c.pointer != 0 {
		_ = c.send(c.pointer, virtualPointerOpDestroy, nil)
		c.pointer = 0
	}

	if c.pointerManager != 0 {
		_ = c.send(c.pointerManager, virtualPointerManagerOpDestroy, nil)
		c.pointerManager = 0
	}

	for _, seat := range c.seats {
		if seat.version >= 5 {
			_ = c.send(seat.object, seatOpRelease, nil)
		}
	}

	// wl_registry has no destructor; it goes away with the connection.
}

// begin prepares one exchange: it rejects a closed or poisoned client and sets
// the operation deadline that bounds every write and read until the roundtrip
// that ends the exchange.
func (c *Client) begin() error {
	if c.closed {
		return errs.New(errs.CodeNotSupported, closedMessage)
	}

	if c.poison != nil {
		return c.poison
	}

	return c.startExchange()
}

// startExchange sets the socket deadline for one request/response exchange.
func (c *Client) startExchange() error {
	if err := c.conn.SetDeadline(time.Now().Add(opTimeout)); err != nil {
		return c.poisonWith(errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not set a deadline on the Wayland socket",
		))
	}

	return nil
}

// timestamp is the millisecond timestamp key and pointer events carry. The
// protocol only requires one clock per object, so a monotonic offset from Dial
// is sufficient and avoids depending on the machine's wall clock.
func (c *Client) timestamp() uint32 {
	return uint32(time.Since(c.start).Milliseconds())
}
