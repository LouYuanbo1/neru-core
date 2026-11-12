//go:build linux

package window

import (
	"encoding/binary"
	"image"
	"testing"
)

func TestMoveResizeData(t *testing.T) {
	// A negative origin exercises the 32-bit encoding: x and y travel as CARDINAL
	// wire fields read from the low 32 bits of each long.
	bounds := image.Rect(-100, 50, 900, 650)

	data := moveResizeData(bounds)

	wantFlags := uint64(
		moveResizeX|moveResizeY|moveResizeWidth|moveResizeHeight|gravityStatic,
	) | uint64(sourcePager)<<moveResizeSource

	if data[0] != wantFlags {
		t.Fatalf("flags = %#x, want %#x", data[0], wantFlags)
	}

	if gravity := data[0] & 0xff; gravity != gravityStatic {
		t.Fatalf("gravity = %d, want %d", gravity, gravityStatic)
	}

	if source := (data[0] >> moveResizeSource) & 0xf; source != uint64(sourcePager) {
		t.Fatalf("source indication = %d, want %d", source, sourcePager)
	}

	for index, want := range []int32{-100, 50, 1000, 600} {
		if got := int32(data[index+1]); got != want {
			t.Fatalf("field %d = %d, want %d", index, got, want)
		}
	}
}

func TestPropertyItemWidth(t *testing.T) {
	cases := map[string]struct {
		format int32
		want   int
	}{
		"byte string":   {format: 8, want: 1},
		"short string":  {format: 16, want: 2},
		"widened long":  {format: 32, want: longSize},
		"unknown width": {format: 0, want: 0},
	}

	for name, test := range cases {
		if got := propertyItemWidth(test.format); got != test.want {
			t.Fatalf("%s: propertyItemWidth(%d) = %d, want %d", name, test.format, got, test.want)
		}
	}
}

// TestPropertyValue_LongsReadsTheWidenedItems is the regression test for a
// format-32 property whose buffer was sized from the server's 32 bits instead of
// the client's long: reading the second item walked off the copy.
func TestPropertyValue_LongsReadsTheWidenedItems(t *testing.T) {
	value := propertyValue{format: 32, items: 2}
	value.data = make([]byte, value.items*propertyItemWidth(value.format))

	for index, item := range []uint64{0x3f0b58, 0x20772} {
		if longSize == 8 {
			binary.NativeEndian.PutUint64(
				value.data[index*longSize:],
				item,
			)
		} else {
			binary.NativeEndian.PutUint32(
				value.data[index*longSize:],
				uint32(item),
			)
		}
	}

	got := value.longs()

	if len(got) != 2 {
		t.Fatalf("longs() returned %d items, want 2", len(got))
	}

	if got[0] != 0x3f0b58 || got[1] != 0x20772 {
		t.Fatalf("longs() = %#x, want [0x3f0b58 0x20772]", got)
	}
}

func TestPropertyValue_AbsentAndTruncated(t *testing.T) {
	if !(propertyValue{}).absent() {
		t.Fatal("zero value should be absent")
	}

	if !(propertyValue{format: 32, items: 0}).absent() {
		t.Fatal("a property with no items should be absent")
	}

	if !(propertyValue{format: 32, items: 1, remaining: 8}).truncated() {
		t.Fatal("bytes_after should mark the value truncated")
	}

	if (propertyValue{format: 32, items: 1}).truncated() {
		t.Fatal("a fully read property should not be truncated")
	}
}

func TestFormatWindowID_ParseRoundTrip(t *testing.T) {
	for _, win := range []uintptr{1, 0x3f0b58, 1 << 40} {
		formatted := formatWindowID(win)

		parsed, ok := parseWindowID(formatted)
		if !ok || parsed != win {
			t.Fatalf("parseWindowID(%q) = %#x, %v; want %#x", formatted, parsed, ok, win)
		}
	}
}

func TestParseWindowID_RejectsMalformedIDs(t *testing.T) {
	for _, value := range []string{"", "0x", "0x0", "zz"} {
		if parsed, ok := parseWindowID(value); ok {
			t.Fatalf("parseWindowID(%q) = %#x, want a rejection", value, parsed)
		}
	}
}
