package wlrinject

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The Wayland wire format is a stream of messages, each an 8-byte header
// followed by the arguments its interface signature declares:
//
//	bytes 0..3: object id (uint32, little-endian)
//	bytes 4..5: opcode (uint16, little-endian)
//	bytes 6..7: message size in bytes including the header (uint16)
//
// Integers are 32-bit and little-endian. A string is a uint32 length that
// includes the terminating NUL, then the bytes and padding to a 4-byte
// boundary; a length of zero means a null string. An array is a uint32 byte
// length followed by the bytes and the same padding. File descriptors are not
// in the body at all: they travel as SCM_RIGHTS ancillary data attached to the
// message's first byte (see Client.sendWithFD).
//
// These helpers are not Linux-specific, so their tests run on any host. Only
// the socket transport and the protocol objects are built on Linux.
const (
	headerSize = 8

	// The size field is 16 bits wide, header included.
	maxMessageSize = 1<<16 - 1
)

var (
	errShortBody  = errors.New("wlrinject: message body is shorter than its signature requires")
	errMissingNUL = errors.New("wlrinject: string argument is not NUL-terminated")
)

// messageHeader is a decoded Wayland message header.
type messageHeader struct {
	object uint32
	opcode uint16
	size   int
}

// decodeHeader decodes the 8-byte header at the start of b.
func decodeHeader(b []byte) (messageHeader, error) {
	if len(b) < headerSize {
		return messageHeader{}, fmt.Errorf(
			"wlrinject: %d bytes is too short for a message header",
			len(b),
		)
	}

	object := binary.LittleEndian.Uint32(b[0:4])
	word := binary.LittleEndian.Uint32(b[4:8])
	opcode := uint16(word)
	size := int(word >> 16)
	if size < headerSize {
		return messageHeader{}, fmt.Errorf(
			"wlrinject: message size %d is smaller than the %d-byte header",
			size,
			headerSize,
		)
	}

	return messageHeader{object: object, opcode: opcode, size: size}, nil
}

// encodeRequest builds one request. Bodies are a handful of integers, far below
// the 16-bit size ceiling; Client.send rejects anything larger.
func encodeRequest(object uint32, opcode uint16, body []byte) []byte {
	message := make([]byte, headerSize, headerSize+len(body))
	binary.LittleEndian.PutUint32(message[0:4], object)
	binary.LittleEndian.PutUint32(
		message[4:8],
		uint32(opcode)|uint32(headerSize+len(body))<<16,
	)

	return append(message, body...)
}

// encoder appends the argument bytes of one request.
type encoder struct {
	buf []byte
}

// uint32 appends one unsigned 32-bit argument.
func (e *encoder) uint32(v uint32) {
	e.buf = binary.LittleEndian.AppendUint32(e.buf, v)
}

// int32 appends one signed 32-bit argument.
func (e *encoder) int32(v int32) {
	e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(v))
}

// fixed appends one wl_fixed_t argument: a signed 24.8 fixed-point number.
func (e *encoder) fixed(v int32) {
	e.uint32(uint32(v))
}

// string appends one string argument: length including the NUL, the bytes, the
// NUL, then zero padding to a 4-byte boundary.
func (e *encoder) string(s string) {
	length := uint32(len(s) + 1)
	e.uint32(length)
	e.buf = append(e.buf, s...)
	e.buf = append(e.buf, 0)

	for padding := pad4(int(length)) - int(length); padding > 0; padding-- {
		e.buf = append(e.buf, 0)
	}
}

// decoder reads the arguments of one message body, in signature order.
type decoder struct {
	buf []byte
	off int
}

// uint32 reads one unsigned 32-bit argument.
func (d *decoder) uint32() (uint32, error) {
	if len(d.buf)-d.off < 4 {
		return 0, errShortBody
	}

	value := binary.LittleEndian.Uint32(d.buf[d.off : d.off+4])
	d.off += 4

	return value, nil
}

// int32 reads one signed 32-bit argument.
func (d *decoder) int32() (int32, error) {
	v, err := d.uint32()
	if err != nil {
		return 0, err
	}

	return int32(v), nil
}

// string reads one string argument. A zero length is a null string and decodes
// to "". The padding that follows the NUL is consumed.
func (d *decoder) string() (string, error) {
	length, err := d.uint32()
	if err != nil {
		return "", err
	}

	if length == 0 {
		return "", nil
	}

	if int64(length) > int64(len(d.buf)-d.off) {
		return "", errShortBody
	}

	raw := d.buf[d.off : d.off+int(length)]
	d.off += int(length)

	if raw[len(raw)-1] != 0 {
		return "", errMissingNUL
	}

	padding := pad4(int(length)) - int(length)
	if padding > len(d.buf)-d.off {
		return "", errShortBody
	}

	d.off += padding

	return string(raw[:len(raw)-1]), nil
}

// decodeUint32s decodes exactly n consecutive uint32 arguments. The events this
// package decodes (wl_seat.capabilities, wl_output.mode/scale) are all unsigned.
func decodeUint32s(body []byte, n int) ([]uint32, error) {
	d := decoder{buf: body}
	values := make([]uint32, n)

	for i := range values {
		v, err := d.uint32()
		if err != nil {
			return nil, err
		}

		values[i] = v
	}

	return values, nil
}

// fixedFromDouble converts a floating-point value to wl_fixed_t's 24.8
// fixed-point representation, rounding to nearest. Values outside int32 are
// clamped rather than wrapped, which keeps a nonsensical scroll delta from
// turning into an axis event in the opposite direction.
func fixedFromDouble(v float64) int32 {
	scaled := math.Round(v * 256)

	switch {
	case scaled > math.MaxInt32:
		return math.MaxInt32
	case scaled < math.MinInt32:
		return math.MinInt32
	default:
		return int32(scaled)
	}
}

// pad4 rounds n up to the next multiple of four.
func pad4(n int) int { return (n + 3) &^ 3 }
