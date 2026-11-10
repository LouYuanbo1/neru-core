//go:build linux

package uinput

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file creates the virtual wheel through /dev/uinput and writes evdev
// events to it. There is no C here: the device is configured with the same
// ioctls the kernel's own uinput examples use, and events are written with
// write(2) as the 24-byte records event.go encodes.
//
// The reason a library wants this at all is smooth scroll. X11's core protocol
// can only press and release wheel buttons, so a pixel delta becomes whole
// notches and an animation becomes a series of jumps. A uinput wheel accepts
// REL_WHEEL_HI_RES, whose 120 units per notch carry a fraction of a notch, so
// the animator's eased chunks arrive as movement.
//
// The device is deliberately shaped like the one the neru daemon creates: the
// same name prefix, vendor id and bus type. Neru's evdev keyboard proxy skips
// devices whose name starts with "neru-" and whose vendor is 0x1234, and that
// check is how an injected event is kept out of its own event tap. A device
// with a different identity would be read back as physical input.

// uinputPaths are the nodes the kernel exposes, in the order udev normally
// creates them. /dev/input/uinput is the older location some distributions
// still use.
var uinputPaths = [...]string{"/dev/uinput", "/dev/input/uinput"}

// timevalSize is sizeof(struct timeval) on this build. It decides the
// input_event record's length (16 + 8 on a 64-bit ABI), and it is a variable
// rather than an inline constant so the 32-bit refusal in Open is a real branch.
var timevalSize = unsafe.Sizeof(syscall.Timeval{})

// littleEndian reports whether this build's byte order is little-endian. The
// encoder writes fields little-endian, which is what the kernel UAPI uses on
// every architecture this backend targets; s390x is the 64-bit big-endian
// counter-example and is refused rather than written backwards.
var littleEndian = func() bool {
	var probe uint16 = 1

	return *(*byte)(unsafe.Pointer(&probe)) == 1
}()

const (
	// uinputVendor is the vendor id every Neru injection device carries, so the
	// evdev keyboard proxy can tell its own output from a physical keyboard.
	uinputVendor = 0x1234
	// uinputProduct identifies this wheel among those devices.
	uinputProduct = 0x5678
	// uinputDeviceName is the published name. The "neru-" prefix is part of the
	// same self-identification as the vendor id.
	uinputDeviceName = "neru-scroll"
)

// Available reports whether a virtual uinput device can be created: a uinput
// node exists and this process may open it for writing.
//
// It opens and closes the node, which is a cheap check and a stronger one than
// looking at its mode bits, since a file can be writable by root alone while
// still looking available. It says nothing about whether the ioctls that
// configure the device will be accepted; only Open finds that out. A 32-bit or
// big-endian build answers false, because Open refuses it (see there).
func Available() bool {
	if timevalSize != 16 || !littleEndian {
		return false
	}

	fd, err := openUinput()
	if err != nil {
		return false
	}

	_ = syscall.Close(fd)

	return true
}

// Device is a virtual pointer whose only motion is a scroll wheel.
//
// It carries REL_WHEEL and REL_HWHEEL for clients that understand whole
// notches, REL_WHEEL_HI_RES and REL_HWHEEL_HI_RES where the kernel accepts them
// so a fraction of a notch can be expressed, and the eight modifier keys
// EV_KEY needs for a modified scroll. It has no REL_X or REL_Y: the pointer's
// position is the compositor's, and this device exists for the wheel.
//
// A Device serializes its own writes. One call is one complete evdev frame,
// which matters because two interleaved frames would merge axes into movement
// the caller never asked for.
type Device struct {
	// mu guards the descriptor and the accumulators, and makes each write
	// contiguous.
	mu sync.Mutex
	// fd is the open /dev/uinput descriptor, or -1 once closed.
	fd int
	// hiRes records whether the kernel accepted the high-resolution axis bits.
	// An older kernel without them still scrolls, one whole notch at a time.
	hiRes bool
	// accX and accY carry the sub-notch remainder of each axis in pixels.
	accX   notchAccumulator
	accY   notchAccumulator
	closed bool
}

// Open creates the virtual wheel.
//
// The device is configured and created in the same order the kernel UAPI
// requires: the capability bits first, then UI_DEV_SETUP with the identity, then
// UI_DEV_CREATE. A failure at any step closes the descriptor and reports which
// capability was refused, so a node that opens but is not really uinput, or a
// kernel built without a feature, is diagnosable.
//
// A missing node returns errs.CodeNotSupported; a node this process may not
// open returns errs.CodeAccessibilityDenied naming the udev rule that fixes it.
//
// A 32-bit build and a big-endian build are refused: input_event is 24 bytes on
// a 64-bit Linux ABI and 16 bytes on a 32-bit one, and this package encodes the
// little-endian 64-bit layout. Writing the wider record on a 32-bit kernel would
// make it read two half-frames, and writing it little-endian on a big-endian
// kernel would swap the fields, so the build says so instead.
func Open() (*Device, error) {
	if timevalSize != 16 {
		return nil, errs.New(
			errs.CodeNotSupported,
			"uinput injection needs a 64-bit Linux ABI; this build's input_event "+
				"record is 16 bytes, not 24",
		)
	}

	if !littleEndian {
		return nil, errs.New(
			errs.CodeNotSupported,
			"uinput injection needs a little-endian Linux ABI; this build's "+
				"input_event fields would be written in the wrong byte order",
		)
	}

	fd, err := openUinput()
	if err != nil {
		return nil, err
	}

	hiRes, err := configure(fd)
	if err != nil {
		_ = syscall.Close(fd)

		return nil, err
	}

	return &Device{fd: fd, hiRes: hiRes}, nil
}

// Close destroys the device and closes its descriptor.
//
// UI_DEV_DESTROY makes the kernel remove the event node immediately instead of
// at process exit, which matters for a daemon that can reopen the device: a
// stale node would still be delivering the last frame. Closing an already
// closed Device is not an error.
func (d *Device) Close() error {
	if d == nil {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil
	}

	d.closed = true
	fd := d.fd
	d.fd = -1

	destroyErr := ioctlSetInt(fd, uiDevDestroy, 0)
	closeErr := syscall.Close(fd)

	switch {
	case destroyErr != nil && closeErr != nil:
		return errs.Wrapf(errors.Join(destroyErr, closeErr), errs.CodeInternal,
			"could not tear down the uinput device")
	case destroyErr != nil:
		return errs.Wrapf(destroyErr, errs.CodeInternal,
			"could not destroy the uinput device")
	case closeErr != nil:
		return errs.Wrapf(closeErr, errs.CodeInternal,
			"could not close the uinput device")
	default:
		return nil
	}
}

// Scroll moves the wheel by dx and dy logical pixels.
//
// dy is positive up and dx is positive left, the library's shared scroll
// convention. REL_WHEEL counts notches up, so the vertical axis goes out
// unchanged; REL_HWHEEL counts notches right, so the horizontal axis flips.
//
// Two axes carry one movement. REL_WHEEL_HI_RES, where the kernel accepts it,
// gets the exact pixel delta at four units per pixel (120 units per notch), so a
// sub-notch animation chunk reaches a client that understands high-resolution
// scrolling as a fraction of a notch. REL_WHEEL gets whole notches, accumulated
// across calls (see notchAccumulator) so the coarse view travels the same total
// distance and never rounds a stream upward. A kernel without the hi-res axes
// gets only the coarse view, and a call whose accumulated delta is still below
// one notch then writes nothing and returns nil: that is a sub-notch movement,
// not a failure.
//
// A caller that wants the discrete behavior the X11 path has (nearest notch,
// never fewer than one) should send whole notches, which is what the X11 path's
// scrollNotches computes; one call then emits exactly that many.
//
// A whole frame goes out in one write, so the axes of one call cannot interleave
// with another caller's.
func (d *Device) Scroll(dx, dy int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ready(); err != nil {
		return err
	}

	// The pixel deltas are what the hi-res axis carries; the accumulators produce
	// the coarse notches for the same movement.
	pixelsY := dy
	pixelsX := -dx
	notchesY := d.accY.add(dy)
	notchesX := -d.accX.add(dx)

	return d.emitScroll(pixelsX, pixelsY, notchesX, notchesY)
}

// Granularity returns the smallest scroll distance one Scroll call can express,
// in logical pixels: 1 when the kernel accepted the high-resolution wheel axes,
// and scrollPixelsPerNotch (30) when it did not, because then only whole notches
// can be written.
//
// A smooth-scroll animator uses this the way the X11 path uses its own
// granularity: chunks that are multiples of it travel exactly. A device that
// reports 30 is animated in whole notches rather than pretending to carry a
// fraction, and a caller that wants sub-notch movement on one that reports 1
// sends the pixel delta unchanged.
//
// The value is fixed when the device is created, so it can be read without the
// device lock.
func (d *Device) Granularity() int {
	if d.hiRes {
		return 1
	}

	return scrollPixelsPerNotch
}

// Key presses or releases one modifier key by evdev key code.
//
// The device advertises the eight modifiers (29, 42, 54, 56, 97, 100, 125 and
// 126: left and right ctrl, shift, alt and meta). That is the set a modified
// scroll needs, and holding a modifier on the device that also scrolls keeps
// the compositor from having to merge two devices' state. Any other code is
// refused with errs.CodeNotSupported rather than dropped, because a key that
// silently does not arrive is worse than a refusal the caller can report.
//
// The key event and its SYN_REPORT go out in one write. Split into two, a
// key-down whose SYN failed would latch until some later frame.
func (d *Device) Key(code int, pressed bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ready(); err != nil {
		return err
	}

	if !isModifierKeycode(code) {
		return errs.Newf(
			errs.CodeNotSupported,
			"key code %d is not advertised by the uinput scroll device, which "+
				"carries the eight modifier keys; inject other keys through a "+
				"keyboard backend",
			code,
		)
	}

	value := int32(0)
	if pressed {
		value = 1
	}

	events := appendInputEvent(nil, evKey, uint16(code), value)
	events = appendInputEvent(events, evSyn, synReport, 0)

	return d.write(events)
}

// emitScroll writes one frame carrying both axes of one movement.
//
// pixelsX and pixelsY are the movement in the kernel's direction (right and up,
// which the caller flipped into before calling). notchesX and notchesY are the
// whole notches the accumulators produced for the same movement. They can
// disagree on one frame — 15 pixels is 60 hi-res units and no whole notch — and
// that is exactly how a high-resolution wheel behaves: the fine axis reports
// every fraction, the coarse axis clicks over when it reaches 120 units.
func (d *Device) emitScroll(pixelsX, pixelsY, notchesX, notchesY int) error {
	if pixelsX == 0 && pixelsY == 0 {
		return nil
	}

	var events []byte

	appendAxis := func(wheel, hiResCode, pixels, notches int) {
		if pixels == 0 && notches == 0 {
			return
		}

		if d.hiRes && pixels != 0 {
			events = appendInputEvent(events, evRel, uint16(hiResCode),
				int32(hiResUnits(pixels)))
		}

		if notches != 0 {
			events = appendInputEvent(events, evRel, uint16(wheel), int32(notches))
		}
	}

	appendAxis(relWheel, relWheelHiRes, pixelsY, notchesY)
	appendAxis(relHWheel, relHWheelHiRes, pixelsX, notchesX)

	// A sub-notch movement on a kernel without the hi-res axes produces no events
	// at all; the accumulator has already kept it, so there is nothing to write.
	if len(events) == 0 {
		return nil
	}

	// One SYN_REPORT closes the frame, whichever axes contributed to it.
	events = appendInputEvent(events, evSyn, synReport, 0)

	return d.write(events)
}

// write hands events to the kernel, retrying a short or interrupted write so a
// frame is never left half-written.
func (d *Device) write(events []byte) error {
	for len(events) > 0 {
		written, err := syscall.Write(d.fd, events)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}

			if errors.Is(err, syscall.ENODEV) {
				return errs.Wrap(err, errs.CodeNotSupported,
					"the uinput device disappeared; reopen it")
			}

			return errs.Wrapf(err, errs.CodeAccessibilityFailed,
				"could not write an input event to the uinput device")
		}

		events = events[written:]
	}

	return nil
}

// ready reports whether the device can still be written to. The caller holds
// the lock.
func (d *Device) ready() error {
	if d == nil || d.closed || d.fd < 0 {
		return errs.New(errs.CodeNotSupported, "the uinput device is closed")
	}

	return nil
}

// openUinput opens the first uinput node this process may use.
//
// The errors are kept apart on purpose: a node that is missing is a deployment
// gap (CodeNotSupported), while one that exists but refuses the open is a
// permission problem the user can fix (CodeAccessibilityDenied), and the two
// want different messages.
func openUinput() (int, error) {
	var (
		firstErr  error
		deniedErr error
	)

	for _, path := range uinputPaths {
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
		if err == nil {
			return fd, nil
		}

		if firstErr == nil {
			firstErr = err
		}

		if deniedErr == nil && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)) {
			deniedErr = err
		}
	}

	if deniedErr != nil {
		return -1, errs.Wrapf(deniedErr, errs.CodeAccessibilityDenied,
			"this process may not open /dev/uinput; grant write access with a "+
				"udev rule or membership of the input group, then log in again")
	}

	return -1, errs.Wrapf(firstErr, errs.CodeNotSupported,
		"no uinput device node is available; load the uinput kernel module "+
			"(modprobe uinput) and make sure /dev/uinput exists")
}

// configure advertises the device's capabilities and creates it, reporting
// whether the high-resolution wheel axes were accepted.
func configure(fd int) (bool, error) {
	// The event types and the axes (and keys) a device can report are separate
	// ioctls: EV_REL says "this device has relative axes", REL_WHEEL says which
	// one, and the kernel drops an event for a code the device has not
	// advertised.
	for _, capability := range [...]struct {
		name    string
		request uint
		code    int
	}{
		{"EV_SYN", uiSetEvBit, evSyn},
		{"EV_REL", uiSetEvBit, evRel},
		{"EV_KEY", uiSetEvBit, evKey},
		{"REL_WHEEL", uiSetRelBit, relWheel},
		{"REL_HWHEEL", uiSetRelBit, relHWheel},
	} {
		if err := ioctlSetInt(fd, capability.request, capability.code); err != nil {
			return false, configureError(err, capability.name)
		}
	}

	for _, keycode := range modifierKeycodes {
		if err := ioctlSetInt(fd, uiSetKeyBit, keycode); err != nil {
			return false, configureError(err, "a modifier key bit")
		}
	}

	// The high-resolution axes are an upgrade, not a requirement. A kernel
	// without them still scrolls through REL_WHEEL, so a refusal disables the
	// finer encoding instead of failing the device.
	hiRes := true

	for _, code := range [...]int{relWheelHiRes, relHWheelHiRes} {
		if err := ioctlSetInt(fd, uiSetRelBit, code); err != nil {
			hiRes = false

			break
		}
	}

	setup := uinputSetup{
		// BUS_USB makes libinput classify the wheel like a real one, and the
		// vendor id is what Neru's own device filter looks for.
		ID: uinputID{BusType: busUSB, Vendor: uinputVendor, Product: uinputProduct},
	}
	copy(setup.Name[:], uinputDeviceName)

	if err := ioctlPointer(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return false, configureError(err, "UI_DEV_SETUP")
	}

	if err := ioctlSetInt(fd, uiDevCreate, 0); err != nil {
		return false, configureError(err, "UI_DEV_CREATE")
	}

	return hiRes, nil
}

// configureError phrases a refused configuration step.
func configureError(err error, step string) error {
	return errs.Wrapf(err, errs.CodeAccessibilityFailed,
		"the uinput device refused %s; the node may not be a real uinput device "+
			"or the kernel may be missing support for it",
		step)
}

// ioctlSetInt performs an ioctl whose argument is an integer, such as
// UI_SET_EVBIT or UI_SET_RELBIT. ioctl(2) takes the argument as one machine
// word, which is why an int travels as uintptr rather than through a pointer.
func ioctlSetInt(fd int, request uint, value int) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(request),
		uintptr(value),
	)
	if errno != 0 {
		return errno
	}

	return nil
}

// ioctlPointer performs an ioctl whose argument is a struct, such as
// UI_DEV_SETUP. syscall.Syscall is marked //go:uintptrescapes, so the object
// behind arg stays alive for the duration of the call.
func ioctlPointer(fd int, request uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(request),
		uintptr(arg),
	)
	if errno != 0 {
		return errno
	}

	return nil
}
