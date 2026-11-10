package uinput

import "encoding/binary"

// This file is the evdev wire format, free of build tags so its encoding can be
// tested on any host.
//
// # struct input_event
//
// The kernel's input_event is, in order:
//
//	struct timeval time;   // 16 bytes on a 64-bit Linux ABI
//	__u16 type;
//	__u16 code;
//	__s32 value;
//
// On a 64-bit ABI struct timeval is two 64-bit longs, so the whole struct is
// 8 + 8 + 2 + 2 + 4 = 24 bytes with no padding. On a 32-bit ABI timeval is two
// 32-bit longs and the struct is 16 bytes; this package encodes the 24-byte
// form, and Open refuses a 32-bit build rather than write a record the kernel
// would split into two events.
//
// The timestamp is written as zero on purpose. A uinput write is not a replay:
// the input core stamps the event with the current time as it queues it, so the
// timeval we pass is ignored. Writing a zero timeval keeps the encoder simple
// and makes the bytes deterministic in tests.
//
// # Scroll units
//
// A mouse wheel is reported as EV_REL with code REL_WHEEL (vertical) or
// REL_HWHEEL (horizontal), and the value is one unit per notch. The "hi-res"
// codes REL_WHEEL_HI_RES and REL_HWHEEL_HI_RES carry the same movement at 120
// units per notch, which is what lets a client with a smooth-scrolling stack
// reproduce a fraction of a notch. Both are emitted for one movement: the
// coarse value keeps every client that only knows REL_WHEEL working, and the
// hi-res value carries the finer detail. Sending 120 on REL_WHEEL itself would
// ask for 120 notches and is not what the kernel means by that code.
const (
	// Event types from <linux/input-event-codes.h>.
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02

	// SYN_REPORT ends one hardware frame.
	synReport = 0x00

	// Relative-axis codes.
	relHWheel      = 0x06
	relWheel       = 0x08
	relWheelHiRes  = 0x0b
	relHWheelHiRes = 0x0c
)

const (
	// inputEventSize is sizeof(struct input_event) on a 64-bit Linux ABI.
	inputEventSize = 24
	// hiResUnitsPerNotch is how many REL_*_HI_RES units one wheel notch is.
	hiResUnitsPerNotch = 120
)

// appendInputEvent appends one encoded input_event to buf and returns the
// extended slice. All three fields are little-endian: that is the kernel UAPI's
// byte order on amd64 and arm64, the architectures this backend targets, and
// Open refuses a big-endian build rather than writing the fields backwards.
func appendInputEvent(buf []byte, eventType, code uint16, value int32) []byte {
	var event [inputEventSize]byte

	// event[0:16] stays zero: the timestamp is the kernel's to fill in.
	binary.LittleEndian.PutUint16(event[16:18], eventType)
	binary.LittleEndian.PutUint16(event[18:20], code)
	binary.LittleEndian.PutUint32(event[20:24], uint32(value))

	return append(buf, event[:]...)
}

// hiResUnits converts a pixel delta to the units REL_*_HI_RES counts in.
//
// One notch is scrollPixelsPerNotch pixels and hiResUnitsPerNotch units, so the
// conversion is exactly four units per pixel and loses nothing: a 15-pixel delta
// becomes 60 units, which is half a notch a client can act on. This is the axis
// that makes an eased animation arrive as movement rather than as a jump; the
// coarse REL_WHEEL axis still gets whole notches, accumulated in notch.go.
func hiResUnits(pixels int) int {
	return pixels * hiResUnitsPerNotch / scrollPixelsPerNotch
}
