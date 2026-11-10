//go:build linux

package libei

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The libei sender: one context attached to the EIS socket the RemoteDesktop
// portal handed over, with the pointer and keyboard devices the compositor
// granted.
//
// # Lifecycle
//
// Dial runs the portal handshake (portal.go), creates a libei sender with
// ei_new_sender, names it, and attaches it to the socket with
// ei_setup_backend_fd. From that call on libei owns the descriptor; ei_unref
// closes it, which is why Close never closes the EIS fd itself.
//
// Attaching is not the same as being usable. The compositor then sends events
// over the same socket, and the client must dispatch them until the devices it
// asked for exist and have resumed:
//
//	EI_EVENT_SEAT_ADDED      bind capabilities (see below)
//	EI_EVENT_DEVICE_ADDED    notice the absolute pointer and the keyboard
//	EI_EVENT_DEVICE_RESUMED  the device may now be driven
//	EI_EVENT_DEVICE_PAUSED   it may not, until it resumes again
//	EI_EVENT_DEVICE_REMOVED  the handle is gone
//	EI_EVENT_DISCONNECT      the server is gone; everything is obsolete
//
// Waiting for the pointer to resume is bounded by the caller's context and
// reports errs.CodeTimeout. The keyboard gets a short extra window because its
// add and resume events can trail the pointer's; a session without a keyboard
// still succeeds, and Key then reports why.
//
// # Emulating
//
// Before the first event on a device the client announces a transaction with
// ei_device_start_emulating(device, sequence), and each frame is closed with
// ei_device_frame(device, ei_now(ei)). A paused device forgets its transaction,
// so the next emit starts a new one. Close stops emulation on a device that is
// still in one.
//
// # The two-stage capability bind
//
// KWin (bug 520464) silently drops the keyboard when the keyboard and pointer
// capabilities are bound in one ei_seat_bind_capabilities call. So the seat-
// added event binds the keyboard alone, the client dispatches once more, and
// only then binds the pointer, absolute pointer, button and scroll caps. This is
// the same workaround the reference libei_client.c carries, and it is why the
// bind is split across two places in this file.
//
// # Event pumping
//
// libei is not thread-safe, so every exported method takes the client lock, and
// every emit first pumps the socket non-blockingly (ei_dispatch when readable,
// then drain the event queue). That is what notices a pause, a resume or a
// removal between two actions without a background goroutine. After emitting,
// ei_dispatch is called again so the frame is flushed to the socket.

const (
	// clientName is the name libei suggests to the compositor, which may show it
	// in the consent dialog.
	clientName = "neru-core"

	// defaultDialTimeout bounds Dial when the caller's context carries no
	// deadline. A consent dialog is what normally takes the time; the reference
	// implementation allows 30s for the same reason.
	defaultDialTimeout = 30 * time.Second

	// keyboardWait is the extra window a granted keyboard gets after the pointer
	// has resumed, because its events can trail the pointer's. A session without
	// a keyboard is not an error.
	keyboardWait = time.Second
)

// Capability bits from libei's enum ei_device_capability.
const (
	capabilityPointer         uint32 = 1 << 0
	capabilityPointerAbsolute uint32 = 1 << 1
	capabilityKeyboard        uint32 = 1 << 2
	capabilityScroll          uint32 = 1 << 4
	capabilityButton          uint32 = 1 << 5
)

// Event types from libei's enum ei_event_type. Only the ones this client acts
// on are named; everything else is unref'd and ignored.
const (
	eventDisconnect    int32 = 2
	eventSeatAdded     int32 = 3
	eventDeviceAdded   int32 = 5
	eventDeviceRemoved int32 = 6
	eventDevicePaused  int32 = 7
	eventDeviceResumed int32 = 8
)

// The failures a caller can act on, phrased once so every path says the same
// thing.
const (
	timeoutMessage = "the compositor did not bring up a libei input device in " +
		"time; approve the RemoteDesktop consent prompt and retry"
	disconnectMessage = "the compositor closed the libei session; the " +
		"RemoteDesktop grant may have been revoked"
	noPointerMessage = "the RemoteDesktop session granted no pointer device; " +
		"approve \"Enable pointer\" in the consent prompt"
	noKeyboardMessage = "the RemoteDesktop session granted no keyboard device, " +
		"so modifier holds cannot be injected; approve \"Enable keyboard\" in " +
		"the consent prompt"
	pausedMessage = "the libei device is paused; retry once the compositor " +
		"resumes it"
	closedMessage = "the libei client is closed"
)

// Client is a libei sender attached to one RemoteDesktop portal session.
//
// A Client is safe for concurrent use: every method takes the same lock, because
// libei itself is not thread-safe and because two interleaved frames would merge
// into one event. Calls therefore serialize, which is what a single input path
// wants anyway.
//
// A Client holds the EIS socket and the portal session until Close. It is not
// reconnectable: once the compositor disconnects or the portal session ends,
// the caller builds a new Client (the reference implementation calls this a
// session reset).
type Client struct {
	// mu serializes every libei call, including the event pump.
	mu sync.Mutex
	// fn is the bound libei symbol table.
	fn *libeiFuncs

	// ei is struct ei *, the sender context. Zero once torn down.
	ei uintptr
	// seat is the ref'd seat the capabilities were bound on.
	seat uintptr
	// capabilitiesBound records that the stage-two bind has run.
	capabilitiesBound bool
	// disconnected records an EI_EVENT_DISCONNECT seen while pumping.
	disconnected bool

	// pointer is the ref'd device with the absolute pointer capability.
	pointer uintptr
	// pointerResumed is whether that device may currently be driven.
	pointerResumed bool
	// pointerEmulating is whether a transaction is open on it.
	pointerEmulating bool

	// keyboard is the ref'd device with the keyboard capability, if granted.
	keyboard uintptr
	// keyboardResumed mirrors pointerResumed for the keyboard.
	keyboardResumed bool
	// keyboardEmulating mirrors pointerEmulating for the keyboard.
	keyboardEmulating bool

	// sequence is the emulation transaction counter. It has to increase by at
	// least one per ei_device_start_emulating call, across both devices.
	sequence uint32

	// closePortal ends the portal session and its bus connection. Nil until Dial
	// has a session.
	closePortal func()
	// closed is set once by Close. Every method checks it.
	closed bool
}

// Dial connects a libei sender to a socket obtained from the RemoteDesktop
// portal.
//
// It runs the whole handshake: CreateSession, SelectDevices, Start (which shows
// the consent dialog on a session with nothing to restore) and ConnectToEIS,
// then attaches libei and waits for the granted devices to resume. The wait is
// bounded by ctx, or by defaultDialTimeout when ctx has no deadline; a timeout
// is errs.CodeTimeout.
//
// A failure names what is missing: errs.CodeNotSupported when libei or the
// portal method is absent or the compositor refuses the connection,
// errs.CodeAccessibilityDenied when the user cancels the prompt or the desktop
// policy refuses it, and errs.CodeAccessibilityFailed for a session that
// granted nothing usable.
//
// It is safe to call once per client. Dial does not prompt twice for one
// session, and it stores no restore token, so each Dial costs one consent
// dialog. Callers that must not prompt mid-action should call it during
// warm-up.
func Dial(ctx context.Context) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	functions, err := loadLibei()
	if err != nil {
		return nil, err
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}

	portal, err := openPortalSession(ctx)
	if err != nil {
		return nil, err
	}

	client := &Client{fn: functions, closePortal: portal.close}

	if err := client.attach(ctx, portal.eisFD); err != nil {
		client.teardown()

		return nil, err
	}

	return client, nil
}

// Close tears down the libei context and the portal session.
//
// Order matters: devices stop emulating, the EIS socket is closed by ei_unref,
// and only then does the portal session end, so libei never emits into a
// transport the portal has already torn down. Close is idempotent and safe on a
// nil *Client.
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
	c.teardown()

	return nil
}

// MoveTo moves the pointer to global compositor coordinates in logical pixels.
//
// The absolute event is followed by a zero-delta relative motion when the device
// also has the relative-pointer capability. That nudge is what makes KWin
// repaint the cursor sprite at the warped position; without it the logical
// pointer moves (so clicks land) while the visible cursor can lag behind.
func (c *Client) MoveTo(x, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.preparePointer(); err != nil {
		return err
	}

	c.fn.pointerMotionAbsolute(c.pointer, float64(x), float64(y))
	c.fn.deviceFrame(c.pointer, c.fn.now(c.ei))

	if c.fn.deviceHasCapability(c.pointer, capabilityPointer) {
		c.fn.pointerMotion(c.pointer, 0, 0)
		c.fn.deviceFrame(c.pointer, c.fn.now(c.ei))
	}

	c.flush()

	return nil
}

// Button presses or releases a mouse button.
//
// button is either the small numbering the library uses elsewhere, 1 left, 2
// middle and 3 right, or the Linux evdev BTN_* code (272 left, 273 right, 274
// middle) that libei and the wlroots virtual pointer both carry. The two cannot
// collide, so a caller holding either convention gets the button it named.
//
// A release that fails leaves the button held, which turns the next movement
// into a drag. The caller is expected to retry a release; this method reports
// the failure rather than retrying, because the retry policy belongs to the
// input path.
func (c *Client) Button(button int, pressed bool) error {
	code, ok := evdevButton(button)
	if !ok {
		return errs.Newf(errs.CodeNotSupported,
			"button %d is not a mouse button; pass 1/2/3 for left/middle/right "+
				"or the evdev BTN_* code", button)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.preparePointer(); err != nil {
		return err
	}

	c.fn.buttonButton(c.pointer, code, pressed)
	c.fn.deviceFrame(c.pointer, c.fn.now(c.ei))
	c.flush()

	return nil
}

// Scroll scrolls by dx and dy logical pixels, positive up and positive left,
// the library's shared scroll convention.
//
// libei's delta is positive right and positive up, the direction the reference
// implementation sends, so the horizontal axis flips once here. The delta is a
// floating-point pixel distance and a fraction of a notch is legal, which is
// what makes an animated scroll arrive as movement instead of a jump.
//
// A scroll with nothing to travel returns nil without touching the device, so a
// paused pointer is not reported for an event that would not have happened.
func (c *Client) Scroll(dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.preparePointer(); err != nil {
		return err
	}

	c.fn.scrollDelta(c.pointer, -float64(dx), float64(dy))
	c.fn.deviceFrame(c.pointer, c.fn.now(c.ei))
	c.flush()

	return nil
}

// Key presses or releases a keyboard key by evdev key code.
//
// This is the modifier-hold path: a modified click or scroll presses the
// modifier here, then releases it. The code is a raw evdev code (29 for
// KEY_LEFTCTRL, 42 for KEY_LEFTSHIFT, 56 for KEY_LEFTALT, 125 for
// KEY_LEFTMETA), not a keysym.
//
// A session the portal granted without a keyboard reports
// errs.CodeNotSupported naming the consent setting; a paused keyboard reports
// errs.CodeAccessibilityFailed so the caller can retry.
func (c *Client) Key(code int, pressed bool) error {
	if code <= 0 || code > 0x2ff {
		return errs.Newf(errs.CodeNotSupported,
			"key code %d is not an evdev key code", code)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.prepareKeyboard(); err != nil {
		return err
	}

	c.fn.keyboardKey(c.keyboard, uint32(code), pressed)
	c.fn.deviceFrame(c.keyboard, c.fn.now(c.ei))
	c.flush()

	return nil
}

// HasKeyboard reports whether the granted session carries a keyboard device.
//
// It answers "does the device exist", not "is it resumed": a compositor that
// briefly pauses a device should not make the caller report a permanent "not
// granted" error. It pumps the socket first, because the keyboard can arrive
// after Dial stopped waiting for it.
func (c *Client) HasKeyboard() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.ei == 0 {
		return false
	}

	if err := c.pump(); err != nil {
		return c.keyboard != 0
	}

	return c.keyboard != 0
}

// attach creates the libei sender, points it at the EIS socket and waits for the
// granted devices.
func (c *Client) attach(ctx context.Context, eisFD int) error {
	ei := c.fn.newSender(0)
	if ei == 0 {
		// libei never took the descriptor, so close it here; nothing else will.
		_ = syscall.Close(eisFD)

		return errs.New(errs.CodeAccessibilityFailed,
			"libei could not create a sender context")
	}

	c.ei = ei

	// Must run before the backend is set up (libei's documented ordering).
	c.fn.configureName(ei, clientName)

	if c.fn.setupBackendFD(ei, int32(eisFD)) != 0 {
		// From this call on libei owns the descriptor, so it is not closed here:
		// closing it as well could close an unrelated descriptor that reused the
		// number. teardown's ei_unref closes it.
		return errs.New(errs.CodeAccessibilityFailed,
			"libei could not attach to the EIS socket the RemoteDesktop portal "+
				"handed over")
	}

	return c.waitForDevices(ctx)
}

// waitForDevices pumps the event loop until the pointer is usable, then gives
// the keyboard a short extra window.
func (c *Client) waitForDevices(ctx context.Context) error {
	fd := int(c.fn.getFD(c.ei))
	if fd < 0 {
		return errs.New(errs.CodeAccessibilityFailed,
			"libei reported no event socket for the EIS connection")
	}

	for !c.pointerResumed {
		if _, err := waitReadable(ctx, fd, 0); err != nil {
			return err
		}

		c.fn.dispatch(c.ei)
		c.drain()

		if c.disconnected {
			return errs.New(errs.CodeNotSupported, disconnectMessage)
		}

		// Stage two of the capability bind: the keyboard is bound and its events
		// drained first, so KWin keeps the keyboard when the pointer caps arrive.
		if c.seat != 0 && !c.capabilitiesBound {
			c.fn.dispatch(c.ei)
			c.drain()
			c.bindCapabilities()
		}
	}

	// The keyboard's events can trail the pointer's, and a grant without a
	// keyboard is legal, so running out of this window is not an error.
	grace := time.Now().Add(keyboardWait)

	for !c.keyboardResumed && time.Now().Before(grace) {
		readable, err := waitReadable(ctx, fd, time.Until(grace))
		if err != nil || !readable {
			break
		}

		c.fn.dispatch(c.ei)
		c.drain()
	}

	return nil
}

// bindCapabilities performs the stage-two seat bind, the one that includes the
// pointer capabilities.
func (c *Client) bindCapabilities() {
	// A trailing zero is the C NULL that terminates the variadic list.
	c.fn.seatBindCapabilities(c.seat, []any{
		uint32(capabilityKeyboard),
		uint32(capabilityPointer),
		uint32(capabilityPointerAbsolute),
		uint32(capabilityButton),
		uint32(capabilityScroll),
		uint32(0),
	})

	c.capabilitiesBound = true
}

// drain consumes every queued event and updates the device bookkeeping.
func (c *Client) drain() {
	for {
		event := c.fn.getEvent(c.ei)
		if event == 0 {
			return
		}

		switch c.fn.eventGetType(event) {
		case eventDisconnect:
			c.disconnected = true
		case eventSeatAdded:
			if seat := c.fn.eventGetSeat(event); seat != 0 && c.seat == 0 {
				c.seat = c.fn.seatRef(seat)

				// Stage one: the keyboard alone (KWin bug 520464).
				c.fn.seatBindCapabilities(seat, []any{
					uint32(capabilityKeyboard),
					uint32(0),
				})
			}
		case eventDeviceAdded:
			device := c.fn.eventGetDevice(event)
			if device != 0 {
				if c.pointer == 0 && c.fn.deviceHasCapability(device, capabilityPointerAbsolute) {
					c.pointer = c.fn.deviceRef(device)
				}

				if c.keyboard == 0 && c.fn.deviceHasCapability(device, capabilityKeyboard) {
					c.keyboard = c.fn.deviceRef(device)
				}
			}
		case eventDeviceResumed:
			switch device := c.fn.eventGetDevice(event); device {
			case c.pointer:
				if device != 0 {
					c.pointerResumed = true
					c.pointerEmulating = false
				}
			case c.keyboard:
				if device != 0 {
					c.keyboardResumed = true
					c.keyboardEmulating = false
				}
			}
		case eventDevicePaused:
			switch device := c.fn.eventGetDevice(event); device {
			case c.pointer:
				if device != 0 {
					c.pointerResumed = false
					c.pointerEmulating = false
				}
			case c.keyboard:
				if device != 0 {
					c.keyboardResumed = false
					c.keyboardEmulating = false
				}
			}
		case eventDeviceRemoved:
			switch device := c.fn.eventGetDevice(event); device {
			case c.pointer:
				if device != 0 {
					c.fn.deviceUnref(c.pointer)
					c.pointer = 0
					c.pointerResumed = false
					c.pointerEmulating = false
				}
			case c.keyboard:
				if device != 0 {
					c.fn.deviceUnref(c.keyboard)
					c.keyboard = 0
					c.keyboardResumed = false
					c.keyboardEmulating = false
				}
			}
		}

		c.fn.eventUnref(event)
	}
}

// pump dispatches anything already waiting on the socket and drains the events.
// Non-blocking, and called before every emit so a pause or removal is noticed.
func (c *Client) pump() error {
	if c.ei == 0 {
		return errs.New(errs.CodeNotSupported, closedMessage)
	}

	fd := int(c.fn.getFD(c.ei))

	readable, err := selectReadable(fd, 0)
	if err != nil && !errors.Is(err, syscall.EINTR) {
		return errs.Wrapf(err, errs.CodeAccessibilityFailed,
			"checking the libei socket failed")
	}

	if readable {
		c.fn.dispatch(c.ei)
	}

	c.drain()

	if c.disconnected {
		return errs.New(errs.CodeNotSupported, disconnectMessage)
	}

	return nil
}

// flush hands queued events to the socket. libei writes on ei_dispatch, so an
// emit is not on the wire until this runs.
func (c *Client) flush() {
	c.fn.dispatch(c.ei)
	c.drain()
}

// preparePointer pumps the socket and makes sure the pointer device exists, has
// resumed and has an open emulation transaction.
func (c *Client) preparePointer() error {
	if c.closed {
		return errs.New(errs.CodeNotSupported, closedMessage)
	}

	if err := c.pump(); err != nil {
		return err
	}

	if c.pointer == 0 {
		return errs.New(errs.CodeNotSupported, noPointerMessage)
	}

	if !c.pointerResumed {
		return errs.New(errs.CodeAccessibilityFailed, pausedMessage)
	}

	if !c.pointerEmulating {
		c.sequence++
		c.fn.deviceStartEmulating(c.pointer, c.sequence)
		c.pointerEmulating = true
	}

	return nil
}

// prepareKeyboard is preparePointer for the keyboard, whose absence is a
// different message.
func (c *Client) prepareKeyboard() error {
	if c.closed {
		return errs.New(errs.CodeNotSupported, closedMessage)
	}

	if err := c.pump(); err != nil {
		return err
	}

	if c.keyboard == 0 {
		return errs.New(errs.CodeNotSupported, noKeyboardMessage)
	}

	if !c.keyboardResumed {
		return errs.New(errs.CodeAccessibilityFailed, pausedMessage)
	}

	if !c.keyboardEmulating {
		c.sequence++
		c.fn.deviceStartEmulating(c.keyboard, c.sequence)
		c.keyboardEmulating = true
	}

	return nil
}

// teardown releases every libei handle and then the portal session. The caller
// holds the lock.
func (c *Client) teardown() {
	if c.ei != 0 {
		if c.pointer != 0 {
			if c.pointerEmulating {
				c.fn.deviceStopEmulating(c.pointer)
			}

			c.fn.deviceUnref(c.pointer)
			c.pointer = 0
		}

		if c.keyboard != 0 {
			if c.keyboardEmulating {
				c.fn.deviceStopEmulating(c.keyboard)
			}

			c.fn.deviceUnref(c.keyboard)
			c.keyboard = 0
		}

		if c.seat != 0 {
			c.fn.seatUnref(c.seat)
			c.seat = 0
		}

		// Last, because libei owns the EIS descriptor and closes it here.
		c.fn.unref(c.ei)
		c.ei = 0
	}

	c.pointerResumed = false
	c.pointerEmulating = false
	c.keyboardResumed = false
	c.keyboardEmulating = false

	if c.closePortal != nil {
		c.closePortal()
		c.closePortal = nil
	}
}

// waitReadable blocks until fd is readable, the caller's limit expires, or ctx
// runs out. A limit of zero means ctx alone. It reports whether the descriptor
// became readable; a limit that expired without one returns false and no error,
// which is how the optional keyboard window ends quietly.
func waitReadable(ctx context.Context, fd int, limit time.Duration) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, errs.Wrapf(err, errs.CodeContextCanceled,
				"waiting for the libei session was canceled")
		}

		wait := limit

		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false, errs.New(errs.CodeTimeout, timeoutMessage)
			}

			if wait <= 0 || remaining < wait {
				wait = remaining
			}
		}

		if wait <= 0 {
			wait = time.Millisecond
		}

		readable, err := selectReadable(fd, wait)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}

			return false, errs.Wrapf(err, errs.CodeAccessibilityFailed,
				"waiting on the libei socket failed")
		}

		if readable {
			return true, nil
		}

		if limit > 0 {
			return false, nil
		}
	}
}

// selectReadable waits up to timeout for fd to become readable and reports
// whether it did. A zero timeout polls.
//
// select(2) is the portable choice here: the syscall package exposes it on
// every Linux architecture, while poll(2) has no raw syscall number on arm64
// and would need ppoll with its own 32/64-bit time handling.
func selectReadable(fd int, timeout time.Duration) (bool, error) {
	if timeout < 0 {
		timeout = 0
	}

	// Round up, so a sub-microsecond wait still waits rather than spinning.
	if timeout > 0 && timeout < time.Microsecond {
		timeout = time.Microsecond
	}

	var set syscall.FdSet

	// select(2) cannot represent an fd at or past FD_SETSIZE, and setFD would
	// index past the bit array rather than fail, so the limit is checked here.
	if fd < 0 || fd >= len(set.Bits)*int(unsafe.Sizeof(set.Bits[0])*8) {
		return false, syscall.EBADF
	}

	setFD(&set, fd)

	timeval := syscall.NsecToTimeval(timeout.Nanoseconds())

	count, err := syscall.Select(fd+1, &set, nil, nil, &timeval)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// setFD marks fd in set.
//
// syscall.FdSet.Bits is an array of the platform's machine words (int64 on a
// 64-bit Linux ABI, int32 on a 32-bit one), so the bit is addressed through
// uintptr, which always has the platform word's width. That keeps this correct
// without a per-architecture file.
func setFD(set *syscall.FdSet, fd int) {
	const wordBits = int(unsafe.Sizeof(uintptr(0)) * 8)

	words := unsafe.Slice(
		(*uintptr)(unsafe.Pointer(&set.Bits[0])),
		len(set.Bits)*int(unsafe.Sizeof(set.Bits[0]))/int(unsafe.Sizeof(uintptr(0))),
	)

	words[fd/wordBits] |= 1 << (uint(fd) % uint(wordBits))
}
