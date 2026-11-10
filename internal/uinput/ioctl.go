package uinput

import "unsafe"

// This file is the uinput kernel ABI written down once, without a build tag, so
// the ioctl numbers and the struct layout can be checked by a unit test on any
// host instead of by reading <linux/uinput.h> on a Linux box.
//
// # ioctl numbers
//
// Every ioctl request is one unsigned int built by the C _IOC macro:
//
//	_IOC(dir, type, nr, size) =
//	    dir<<30 | size<<16 | type<<8 | nr
//
// where dir is 0 for _IO (no argument), 1 for _IOW (the kernel reads the
// argument) and 2 for _IOC_READ, size is the byte length of the argument and
// type is an 8-bit namespace. uinput uses the type byte 'U' (0x55). The kernel
// compares the full number, including its encoded size, so a wrong size in the
// declaration does not fail a little later: the ioctl simply does not match.
//
// The numbers below are those in <linux/uinput.h>:
//
//	UI_DEV_CREATE  _IO ('U',   1)          = 0x5501
//	UI_DEV_DESTROY _IO ('U',   2)          = 0x5502
//	UI_DEV_SETUP   _IOW('U',   3, 92)      = 0x405c5503
//	UI_SET_EVBIT   _IOW('U', 100, int)     = 0x40045564
//	UI_SET_KEYBIT  _IOW('U', 101, int)     = 0x40045565
//	UI_SET_RELBIT  _IOW('U', 102, int)     = 0x40045566
//
// UI_SET_* take the bit number as the integer argument rather than a pointer,
// which is why they are listed with a size of 4: the size describes the type
// the kernel checks, not how the value travels.
const (
	// iocWrite is the _IOC_WRITE direction bit pair; _IO is 0.
	iocWrite = 1
	// uinputIOCTLType is the type byte every uinput ioctl carries.
	uinputIOCTLType = 'U'
	// uinputSetupSize is sizeof(struct uinput_setup): 8 bytes of input_id, an
	// 80-byte name and a uint32, with no padding on any Linux ABI.
	uinputSetupSize = 92
	// uinputMaxNameSize is UINPUT_MAX_NAME_SIZE, the name field's length.
	uinputMaxNameSize = 80
)

// ioctlNumber builds an ioctl request the way the C _IOC macro does.
func ioctlNumber(direction, size, typ, nr uint) uint {
	return direction<<30 | size<<16 | typ<<8 | nr
}

// The uinput ioctls this device uses. They are variables rather than constants
// only because ioctlNumber spells the bit packing out; nothing reassigns them.
var (
	uiDevCreate  = ioctlNumber(0, 0, uinputIOCTLType, 1)
	uiDevDestroy = ioctlNumber(0, 0, uinputIOCTLType, 2)
	uiDevSetup   = ioctlNumber(iocWrite, uinputSetupSize, uinputIOCTLType, 3)
	uiSetEvBit   = ioctlNumber(iocWrite, 4, uinputIOCTLType, 100)
	uiSetKeyBit  = ioctlNumber(iocWrite, 4, uinputIOCTLType, 101)
	uiSetRelBit  = ioctlNumber(iocWrite, 4, uinputIOCTLType, 102)
)

// Bus types from <linux/input.h>. BUS_USB makes libinput treat the virtual
// wheel like a real one instead of an unknown virtual device.
const (
	busUSB = 0x03
)

// uinputID is struct input_id: the identity the kernel publishes for a device
// and the one udev, libinput and Neru's own evdev proxy read back.
type uinputID struct {
	BusType uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

// uinputSetup is struct uinput_setup, the UI_DEV_SETUP argument. Its layout is
// fixed by the kernel UAPI: input_id (4 x uint16), then char name[80], then
// __u32 ff_effects_max. The size is 92 bytes on every Linux ABI, which is what
// uiDevSetup encodes.
type uinputSetup struct {
	ID           uinputID
	Name         [uinputMaxNameSize]byte
	FFEffectsMax uint32
}

// Compile-time proof that the layout above is exactly the 92 bytes UI_DEV_SETUP
// declares: both differences have to be zero for these arrays to exist, so a
// struct that grows padding fails the build instead of the first ioctl.
var (
	_ [uinputSetupSize - unsafe.Sizeof(uinputSetup{})]struct{}
	_ [unsafe.Sizeof(uinputSetup{}) - uinputSetupSize]struct{}
)
