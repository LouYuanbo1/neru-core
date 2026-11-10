package uinput

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestAppendInputEvent_EncodesTheKernelLayout pins the 24-byte record from
// <linux/input.h>: a zeroed 16-byte timeval, then type, code and value as
// little-endian 16/16/32-bit fields. A wrong offset here writes an event the
// input core interprets as a different type, which is invisible until a client
// scrolls the wrong way.
func TestAppendInputEvent_EncodesTheKernelLayout(t *testing.T) {
	encoded := appendInputEvent(nil, evRel, relWheel, -3)

	if len(encoded) != inputEventSize {
		t.Fatalf("encoded length = %d, want %d", len(encoded), inputEventSize)
	}

	for offset, value := range encoded[:16] {
		if value != 0 {
			t.Fatalf("timestamp byte %d = %#x, want zero (the kernel stamps the event)", offset, value)
		}
	}

	if got := binary.LittleEndian.Uint16(encoded[16:18]); got != evRel {
		t.Errorf("type = %#x, want %#x", got, evRel)
	}

	if got := binary.LittleEndian.Uint16(encoded[18:20]); got != relWheel {
		t.Errorf("code = %d, want %d", got, relWheel)
	}

	if got := int32(binary.LittleEndian.Uint32(encoded[20:24])); got != -3 {
		t.Errorf("value = %d, want -3", got)
	}
}

// TestAppendInputEvent_AppendsFramesWithoutSharingBytes pins that a frame's
// events concatenate: one write carries both, and the second record starts
// exactly where the first ended so a short write can be completed from the
// same buffer.
func TestAppendInputEvent_AppendsFramesWithoutSharingBytes(t *testing.T) {
	encoded := appendInputEvent(nil, evKey, 29, 1)
	encoded = appendInputEvent(encoded, evSyn, synReport, 0)

	if len(encoded) != 2*inputEventSize {
		t.Fatalf("encoded length = %d, want %d", len(encoded), 2*inputEventSize)
	}

	second := encoded[inputEventSize:]

	if got := binary.LittleEndian.Uint16(second[16:18]); got != evSyn {
		t.Errorf("second event type = %#x, want %#x", got, evSyn)
	}
}

// TestIoctlNumbers_MatchTheKernelHeader pins the _IOC encoding against the
// literal values in <linux/uinput.h>. The kernel compares the whole number,
// including the encoded argument size, so a mistyped size fails the ioctl
// rather than anything the compiler can see.
func TestIoctlNumbers_MatchTheKernelHeader(t *testing.T) {
	tests := []struct {
		name string
		got  uint
		want uint
	}{
		{"UI_DEV_CREATE", uiDevCreate, 0x5501},
		{"UI_DEV_DESTROY", uiDevDestroy, 0x5502},
		{"UI_DEV_SETUP", uiDevSetup, 0x405c5503},
		{"UI_SET_EVBIT", uiSetEvBit, 0x40045564},
		{"UI_SET_KEYBIT", uiSetKeyBit, 0x40045565},
		{"UI_SET_RELBIT", uiSetRelBit, 0x40045566},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("%s = %#x, want %#x", testCase.name, testCase.got, testCase.want)
			}
		})
	}
}

// TestUinputSetup_IsTheKernelStruct pins the UI_DEV_SETUP argument layout: four
// uint16 id fields, an 80-byte name and one uint32, with the name at offset 8
// and the u32 at 88. The ioctl's encoded size is 92, so any padding added to
// the Go struct would make the kernel reject it.
func TestUinputSetup_IsTheKernelStruct(t *testing.T) {
	var setup uinputSetup

	if size := unsafe.Sizeof(setup); size != uinputSetupSize {
		t.Fatalf("sizeof(uinput_setup) = %d, want %d", size, uinputSetupSize)
	}

	if offset := unsafe.Offsetof(setup.Name); offset != 8 {
		t.Errorf("name offset = %d, want 8", offset)
	}

	if offset := unsafe.Offsetof(setup.FFEffectsMax); offset != 88 {
		t.Errorf("ff_effects_max offset = %d, want 88", offset)
	}

	if got := unsafe.Sizeof(setup.ID); got != 8 {
		t.Errorf("sizeof(input_id) = %d, want 8", got)
	}
}

// TestNotchAccumulator_CarriesTheSubNotchRemainder pins the conversion every
// animated scroll depends on: only whole notches are emitted, and the pixels
// that did not make one are kept for the next call rather than rounded away.
func TestNotchAccumulator_CarriesTheSubNotchRemainder(t *testing.T) {
	tests := []struct {
		name    string
		deltas  []int
		notches []int
	}{
		{
			name:   "one exact notch",
			deltas: []int{scrollPixelsPerNotch}, notches: []int{1},
		},
		{
			name:   "a fraction waits",
			deltas: []int{10, 10}, notches: []int{0, 0},
		},
		{
			name:   "two fractions make one notch",
			deltas: []int{20, 15}, notches: []int{0, 1},
		},
		{
			name:   "a long delta emits every whole notch",
			deltas: []int{100}, notches: []int{3},
		},
		{
			name:   "negative deltas emit negative notches",
			deltas: []int{-30, -45}, notches: []int{-1, -1},
		},
		{
			name:   "opposite deltas cancel before rounding",
			deltas: []int{20, -10}, notches: []int{0, 0},
		},
		{
			name:   "an exact notch plus the remainder",
			deltas: []int{45}, notches: []int{1},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var accumulator notchAccumulator

			for index, delta := range testCase.deltas {
				if got := accumulator.add(delta); got != testCase.notches[index] {
					t.Fatalf("add(%d) = %d, want %d", delta, got, testCase.notches[index])
				}
			}
		})
	}
}

// TestNotchAccumulator_KeepsTheRemainderSmall pins the invariant that makes the
// accumulator safe to keep for the life of a device: the carried remainder is
// always smaller than one notch, in either direction, so a long stream cannot
// drift into an extra notch.
func TestNotchAccumulator_KeepsTheRemainderSmall(t *testing.T) {
	var accumulator notchAccumulator

	for _, delta := range []int{17, -5, 41, -60, 7, 30, -29, 300, -1} {
		accumulator.add(delta)

		if remainder := accumulator.pixels; remainder <= -scrollPixelsPerNotch || remainder >= scrollPixelsPerNotch {
			t.Fatalf("after add(%d) the remainder is %d, which is not smaller than one notch", delta, remainder)
		}
	}
}

// TestModifierKeycodes_AreTheEightModifiers pins the advertised key set, which
// is what Key validates against. A device that advertises a code it does not
// accept drops the event in the kernel, so the table and the check have to
// agree.
func TestModifierKeycodes_AreTheEightModifiers(t *testing.T) {
	want := []int{29, 97, 42, 54, 56, 100, 125, 126}

	if len(modifierKeycodes) != len(want) {
		t.Fatalf("advertised %d key codes, want %d", len(modifierKeycodes), len(want))
	}

	for index, code := range want {
		if modifierKeycodes[index] != code {
			t.Errorf("modifierKeycodes[%d] = %d, want %d", index, modifierKeycodes[index], code)
		}

		if !isModifierKeycode(code) {
			t.Errorf("isModifierKeycode(%d) = false, want true", code)
		}
	}

	for _, code := range []int{0, 1, 30, 28, 96, 127, 272} {
		if isModifierKeycode(code) {
			t.Errorf("isModifierKeycode(%d) = true, want false", code)
		}
	}
}

// TestHiResUnitsPerNotch pins the hi-res scale. REL_WHEEL counts one per notch
// while REL_WHEEL_HI_RES counts 120, and a client that accumulates the hi-res
// axis sees whole notches only once 120 units have arrived.
func TestHiResUnitsPerNotch(t *testing.T) {
	if hiResUnitsPerNotch != 120 {
		t.Fatalf("hiResUnitsPerNotch = %d, want 120", hiResUnitsPerNotch)
	}
}

// TestHiResUnits_ConvertsPixelsExactly pins the hi-res axis conversion. Four
// units per pixel is exact, so a sub-notch animation chunk reaches a
// high-resolution client as the distance it actually was; a rounded value here
// would make a smooth scroll travel a different distance from the same binding
// on X11.
func TestHiResUnits_ConvertsPixelsExactly(t *testing.T) {
	tests := []struct {
		pixels int
		want   int
	}{
		{pixels: 0, want: 0},
		{pixels: 1, want: 4},
		{pixels: 15, want: 60},
		{pixels: scrollPixelsPerNotch, want: hiResUnitsPerNotch},
		{pixels: -15, want: -60},
		{pixels: -scrollPixelsPerNotch, want: -hiResUnitsPerNotch},
		{pixels: 50, want: 200},
	}

	for _, testCase := range tests {
		if got := hiResUnits(testCase.pixels); got != testCase.want {
			t.Errorf("hiResUnits(%d) = %d, want %d", testCase.pixels, got, testCase.want)
		}
	}
}
