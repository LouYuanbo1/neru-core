//go:build linux

// Package uinput creates and drives a virtual input device through the Linux
// kernel's /dev/uinput interface, without cgo.
//
// It exists for smooth scrolling. The X11 backend this library already has can
// only press and release wheel buttons, so a pixel delta becomes a whole number
// of notches and an animation becomes a series of jumps. A uinput wheel accepts
// REL_WHEEL_HI_RES, whose 120 units per notch carry a fraction of a notch, so
// the scroll animator's eased chunks arrive as movement. The same device
// carries the modifier keys a modified scroll needs, which keeps the pointer
// and the held key on one device instead of asking the compositor to merge two.
//
// # How the device is created
//
// /dev/uinput is a character device configured with ioctls and then fed with
// write(2). Open performs the sequence the kernel UAPI requires:
//
//   - UI_SET_EVBIT for EV_SYN, EV_REL and EV_KEY: the event types.
//   - UI_SET_RELBIT for REL_WHEEL and REL_HWHEEL, and for the high-resolution
//     pair where the kernel accepts them.
//   - UI_SET_KEYBIT for the eight modifier key codes.
//   - UI_DEV_SETUP with a struct uinput_setup carrying the device's identity.
//   - UI_DEV_CREATE, after which the kernel publishes an event node and the
//     device can be written to.
//
// The ioctl numbers, the struct layout and the 24-byte input_event record are
// written down in ioctl.go and event.go with the kernels' arithmetic, so a unit
// test can check them on any host. The numbers are also the reason the code
// looks low level: _IOC packs a direction, a size, a type byte and a number
// into one word, and the kernel compares the whole word.
//
// # Scrolling
//
// Scroll takes pixel deltas in the library's shared convention (+y up, +x left)
// and drives two axes at once, the same ratio the X11 path uses (30 pixels per
// notch) so one binding travels the same distance on either backend:
//
//   - REL_WHEEL_HI_RES carries the exact movement at four units per pixel (120
//     units per notch), which is what makes a sub-notch animation chunk arrive
//     as a fraction of a notch.
//   - REL_WHEEL carries whole notches, accumulated across calls, so a client
//     that only understands notches still travels the right total distance and
//     a stream never rounds upward.
//
// A kernel without the high-resolution axes gets only the second, and then a
// sub-notch call writes nothing and returns nil while the remainder is carried
// into the next call. Granularity reports which of the two a device carries (1
// pixel or one notch), so an animator can size its chunks without asking again.
// One call is one complete evdev frame, written in one syscall, so two callers
// can never interleave half-frames.
//
// # What is degraded, and why
//
// A missing node returns errs.CodeNotSupported and a node this process may not
// open returns errs.CodeAccessibilityDenied, each naming the remedy (the uinput
// module, a udev rule or the input group). A kernel without the high-resolution
// axes is not an error: Open records the absence and Scroll falls back to whole
// notches, which is what every client understood before REL_WHEEL_HI_RES.
//
// Key carries the eight modifiers only. The device is a wheel that can hold a
// modifier for a modified scroll, not a general keyboard, and a code it does not
// advertise is refused rather than silently dropped.
//
// # Availability
//
// Available opens and closes the node, so it answers "could this process create
// a device right now" rather than "does the file exist". A caller should still
// treat Open as able to fail, because the ioctls can refuse on a node that
// opened, and because permission can change between the two calls.
package uinput
