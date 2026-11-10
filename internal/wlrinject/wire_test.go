package wlrinject

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// encodeArguments builds the argument bytes a test event or request needs.
func encodeArguments(fill func(*encoder)) []byte {
	var e encoder

	fill(&e)

	return e.buf
}

func TestEncodeRequest_CarriesObjectOpcodeAndSize(t *testing.T) {
	message := encodeRequest(7, 3, []byte{1, 2, 3, 4})

	if got := binary.LittleEndian.Uint32(message[0:4]); got != 7 {
		t.Errorf("object id = %d, want 7", got)
	}

	word := binary.LittleEndian.Uint32(message[4:8])
	if got := uint16(word); got != 3 {
		t.Errorf("opcode = %d, want 3", got)
	}

	if got := int(word >> 16); got != 12 {
		t.Errorf("size = %d, want 12", got)
	}

	if len(message) != 12 {
		t.Errorf("message length = %d, want 12", len(message))
	}
}

func TestEncodeRequest_IsParsedBackByDecodeHeader(t *testing.T) {
	message := encodeRequest(displayID, displayOpGetRegistry, encodeArguments(func(e *encoder) {
		e.uint32(9)
	}))

	header, err := decodeHeader(message)
	if err != nil {
		t.Fatalf("decodeHeader: %v", err)
	}

	if header.object != displayID || header.opcode != displayOpGetRegistry || header.size != len(message) {
		t.Errorf(
			"header = %+v, want object %d opcode %d size %d",
			header,
			displayID,
			displayOpGetRegistry,
			len(message),
		)
	}
}

func TestEncoder_StringIsNULTerminatedAndPadded(t *testing.T) {
	body := encodeArguments(func(e *encoder) { e.string("wl_output") })

	// Four bytes of length plus 9 characters, the NUL and padding to 12.
	if len(body) != 16 {
		t.Fatalf("body length = %d, want 16", len(body))
	}

	if got := binary.LittleEndian.Uint32(body[0:4]); got != 10 {
		t.Errorf("length field = %d, want 10", got)
	}

	if got := string(body[4:13]); got != "wl_output" {
		t.Errorf("string = %q, want %q", got, "wl_output")
	}

	if body[13] != 0 {
		t.Errorf("byte after the string = %d, want the NUL terminator", body[13])
	}

	if !bytes.Equal(body[14:], []byte{0, 0}) {
		t.Errorf("padding = %v, want two zero bytes", body[14:])
	}
}

func TestDecoder_ReadsSignatureArgumentsInOrder(t *testing.T) {
	body := encodeArguments(func(e *encoder) {
		e.uint32(11)
		e.int32(-2)
		e.fixed(-2560)
		e.string("wl_seat")
	})

	d := decoder{buf: body}

	unsigned, err := d.uint32()
	if err != nil {
		t.Fatalf("uint32: %v", err)
	}

	signed, err := d.int32()
	if err != nil {
		t.Fatalf("int32: %v", err)
	}

	fixedValue, err := d.int32()
	if err != nil {
		t.Fatalf("fixed: %v", err)
	}

	text, err := d.string()
	if err != nil {
		t.Fatalf("string: %v", err)
	}

	if unsigned != 11 || signed != -2 || fixedValue != -2560 || text != "wl_seat" {
		t.Errorf(
			"decoded (%d, %d, %d, %q), want (11, -2, -2560, %q)",
			unsigned,
			signed,
			fixedValue,
			text,
			"wl_seat",
		)
	}

	if d.off != len(body) {
		t.Errorf("consumed %d bytes, want %d", d.off, len(body))
	}
}

func TestDecodeHeader_RejectsSizeBelowHeader(t *testing.T) {
	header := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(header[0:4], displayID)
	binary.LittleEndian.PutUint32(header[4:8], 3) // opcode 3, size 0

	if _, err := decodeHeader(header); err == nil {
		t.Fatal("decodeHeader accepted a message smaller than its own header")
	}
}

func TestDecoder_RejectsTruncatedArgument(t *testing.T) {
	d := decoder{buf: []byte{1, 2, 3}}

	if _, err := d.uint32(); !errors.Is(err, errShortBody) {
		t.Errorf("uint32 error = %v, want errShortBody", err)
	}
}

func TestDecoder_RejectsStringWithoutNUL(t *testing.T) {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 2)
	body = append(body, 'a', 'b')

	d := decoder{buf: body}

	if _, err := d.string(); !errors.Is(err, errMissingNUL) {
		t.Errorf("string error = %v, want errMissingNUL", err)
	}
}

func TestFixedFromDouble_RoundsTo24Dot8(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want int32
	}{
		{name: "one notch", in: 10, want: 2560},
		{name: "half a unit", in: 0.5, want: 128},
		{name: "negative fraction", in: -2.5, want: -640},
		{name: "beyond positive range", in: 1e12, want: 1<<31 - 1},
		{name: "beyond negative range", in: -1e12, want: -1 << 31},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fixedFromDouble(test.in); got != test.want {
				t.Errorf("fixedFromDouble(%v) = %d, want %d", test.in, got, test.want)
			}
		})
	}
}

func TestParseRegistryGlobal_DecodesNameInterfaceAndVersion(t *testing.T) {
	body := encodeArguments(func(e *encoder) {
		e.uint32(42)
		e.string("zwlr_virtual_pointer_manager_v1")
		e.uint32(2)
	})

	global, err := parseRegistryGlobal(body)
	if err != nil {
		t.Fatalf("parseRegistryGlobal: %v", err)
	}

	if global.name != 42 || global.iface != "zwlr_virtual_pointer_manager_v1" || global.version != 2 {
		t.Errorf("global = %+v, want name 42, the pointer manager and version 2", global)
	}
}

func TestParseRegistryGlobal_RejectsTruncatedEvent(t *testing.T) {
	body := encodeArguments(func(e *encoder) {
		e.uint32(42)
		e.string("wl_seat")
	})

	// The version argument is missing.
	if _, err := parseRegistryGlobal(body); !errors.Is(err, errShortBody) {
		t.Errorf("parseRegistryGlobal error = %v, want errShortBody", err)
	}
}

func TestDecodeUint32s_ReadsEveryArgument(t *testing.T) {
	body := encodeArguments(func(e *encoder) {
		e.uint32(1)
		e.uint32(0)
		e.uint32(3840)
		e.uint32(2160)
	})

	values, err := decodeUint32s(body, 4)
	if err != nil {
		t.Fatalf("decodeUint32s: %v", err)
	}

	want := []uint32{1, 0, 3840, 2160}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("value[%d] = %d, want %d", i, values[i], want[i])
		}
	}
}

func TestPad4_RoundsUpToAWordBoundary(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{in: 0, want: 0},
		{in: 1, want: 4},
		{in: 4, want: 4},
		{in: 5, want: 8},
		{in: 13, want: 16},
	}

	for _, test := range tests {
		if got := pad4(test.in); got != test.want {
			t.Errorf("pad4(%d) = %d, want %d", test.in, got, test.want)
		}
	}
}
