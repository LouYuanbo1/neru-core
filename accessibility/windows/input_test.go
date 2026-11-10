//go:build windows

package windows

import (
	"image"
	"testing"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/action"
)

// Unit tests for the input layer's pure parts: the flag words SendInput needs,
// the absolute-coordinate mapping, and the pixel-to-wheel conversion. Nothing
// here injects input.

// wheelData is the unsigned mouseData field a signed wheel delta is carried in.
// The conversion goes through a variable because it is a two's-complement
// reinterpretation, which a constant conversion refuses.
func wheelData(delta int32) uint32 {
	return uint32(delta)
}

func TestFlagsForButton(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		button action.MouseButton
		want   buttonFlags
	}{
		{
			name:   "left",
			button: action.ButtonLeft,
			want:   buttonFlags{down: mouseeventfLeftDown, up: mouseeventfLeftUp},
		},
		{
			name:   "right",
			button: action.ButtonRight,
			want:   buttonFlags{down: mouseeventfRightDown, up: mouseeventfRightUp},
		},
		{
			name:   "middle",
			button: action.ButtonMiddle,
			want:   buttonFlags{down: mouseeventfMiddleDown, up: mouseeventfMiddleUp},
		},
		{
			name:   "unknown falls back to left",
			button: action.MouseButton(99),
			want:   buttonFlags{down: mouseeventfLeftDown, up: mouseeventfLeftUp},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := flagsForButton(testCase.button); got != testCase.want {
				t.Errorf("flagsForButton(%v) = %+v, want %+v", testCase.button, got, testCase.want)
			}
		})
	}
}

func TestVirtualKeyForButton(t *testing.T) {
	t.Parallel()

	tests := []struct {
		button action.MouseButton
		want   uint16
	}{
		{button: action.ButtonLeft, want: vkLButton},
		{button: action.ButtonRight, want: vkRButton},
		{button: action.ButtonMiddle, want: vkMButton},
		{button: action.MouseButton(99), want: vkLButton},
	}

	for _, testCase := range tests {
		if got := virtualKeyForButton(testCase.button); got != testCase.want {
			t.Errorf("virtualKeyForButton(%v) = %#x, want %#x", testCase.button, got, testCase.want)
		}
	}
}

// TestModifierFamilies_PresentEverySide checks the mapping from the neutral
// modifier bits to the virtual keys this platform presents and suppresses.
// Missing the right-hand key would leave a user-held right shift presenting a
// modifier the caller did not ask for.
func TestModifierFamilies_PresentEverySide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		modifier action.Modifiers
		left     uint16
		right    uint16
	}{
		{modifier: action.ModCmd, left: vkLWin, right: vkRWin},
		{modifier: action.ModCtrl, left: vkLControl, right: vkRControl},
		{modifier: action.ModAlt, left: vkLMenu, right: vkRMenu},
		{modifier: action.ModShift, left: vkLShift, right: vkRShift},
	}

	if len(modifierFamilies) != len(tests) {
		t.Fatalf("modifierFamilies has %d entries, want %d", len(modifierFamilies), len(tests))
	}

	for index, testCase := range tests {
		family := modifierFamilies[index]
		if family.modifier != testCase.modifier {
			t.Errorf("family %d is %v, want %v", index, family.modifier, testCase.modifier)
		}

		if family.left != testCase.left || family.right != testCase.right {
			t.Errorf("family %v presents %#x/%#x, want %#x/%#x",
				family.modifier, family.left, family.right, testCase.left, testCase.right)
		}
	}
}

// TestAbsoluteCoordinate pins the mapping Windows reads back with a floor: the
// value chosen has to land on the pixel it stands for, including at the far
// edge of the desktop and on a desktop with a negative origin.
func TestAbsoluteCoordinate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		pixel  int
		origin int
		size   int
		want   int32
	}{
		{name: "first pixel", pixel: 0, origin: 0, size: 1920, want: 0},
		{name: "middle", pixel: 960, origin: 0, size: 1920, want: 32768},
		// The last pixel is not the top of the range: the value has to be the
		// smallest one that floors back to the pixel, which is what keeps the
		// mapping exact rather than off by a pixel at the edge.
		{name: "last pixel", pixel: 1919, origin: 0, size: 1920, want: 65502},
		{name: "negative origin", pixel: -1920, origin: -1920, size: 3840, want: 0},
		{name: "left of the primary", pixel: -1, origin: -1920, size: 3840, want: 32751},
		{name: "beyond the desktop clamps", pixel: 5000, origin: 0, size: 1920, want: 65502},
		{name: "before the desktop clamps", pixel: -50, origin: 0, size: 1920, want: 0},
		{name: "unknown size", pixel: 10, origin: 0, size: 0, want: 0},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := absoluteCoordinate(testCase.pixel, testCase.origin, testCase.size); got != testCase.want {
				t.Errorf("absoluteCoordinate(%d, %d, %d) = %d, want %d",
					testCase.pixel, testCase.origin, testCase.size, got, testCase.want)
			}
		})
	}
}

// TestAbsoluteCoordinate_RoundTripsEveryPixel checks the property the mapping
// exists for: Windows' floor(value * size / 65536) recovers the pixel.
func TestAbsoluteCoordinate_RoundTripsEveryPixel(t *testing.T) {
	t.Parallel()

	for _, size := range []int{1920, 2160, 1366, 3840} {
		for pixel := range size {
			value := absoluteCoordinate(pixel, 0, size)
			back := int(int64(value) * int64(size) / absoluteCoordinateRange)

			if back != pixel {
				t.Fatalf("size %d: pixel %d maps to %d, which reads back as %d",
					size, pixel, value, back)
			}
		}
	}
}

// TestWheelRecords_SplitsLongScrolls checks that a delta larger than the
// signed 16-bit field a wheel record carries is spread over several records
// rather than wrapping into the opposite direction.
func TestWheelRecords_SplitsLongScrolls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		vertical   int32
		horizontal int32
		want       []wheelEvent
	}{
		{name: "nothing", want: nil},
		{
			name:     "one notch up",
			vertical: wheelDelta,
			want:     []wheelEvent{{flags: mouseeventfWheel, data: wheelDelta}},
		},
		{
			name:     "one notch down",
			vertical: -wheelDelta,
			want:     []wheelEvent{{flags: mouseeventfWheel, data: wheelData(-wheelDelta)}},
		},
		{
			name:       "horizontal only",
			horizontal: wheelDelta,
			want:       []wheelEvent{{flags: mouseeventfHWheel, data: wheelDelta}},
		},
		{
			name:       "both axes",
			vertical:   wheelDelta,
			horizontal: -wheelDelta,
			want: []wheelEvent{
				{flags: mouseeventfWheel, data: wheelDelta},
				{flags: mouseeventfHWheel, data: wheelData(-wheelDelta)},
			},
		},
		{
			name:     "long scroll is split",
			vertical: 70000,
			want: []wheelEvent{
				{flags: mouseeventfWheel, data: maxWheelRecord},
				{flags: mouseeventfWheel, data: maxWheelRecord},
				{flags: mouseeventfWheel, data: 70000 - 2*maxWheelRecord},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := wheelRecords(testCase.vertical, testCase.horizontal)
			if len(got) != len(testCase.want) {
				t.Fatalf("wheelRecords(%d, %d) = %v, want %v",
					testCase.vertical, testCase.horizontal, got, testCase.want)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Errorf("record %d = %+v, want %+v", index, got[index], testCase.want[index])
				}
			}
		})
	}
}

// TestWheelEvents_MapsPixelsToUnits checks the shared scroll convention: a
// pixel delta becomes WHEEL_DELTA units, positive deltaY scrolls up, and
// positive deltaX scrolls left, which is the opposite of what MOUSEEVENTF_HWHEEL
// reads.
func TestWheelEvents_MapsPixelsToUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		deltaX int
		deltaY int
		want   []wheelEvent
	}{
		{
			name:   "one notch up",
			deltaY: scrollPixelsPerNotch,
			want:   []wheelEvent{{flags: mouseeventfWheel, data: wheelDelta}},
		},
		{
			name:   "one notch down",
			deltaY: -scrollPixelsPerNotch,
			want:   []wheelEvent{{flags: mouseeventfWheel, data: wheelData(-wheelDelta)}},
		},
		{
			name:   "one notch left is a positive HWHEEL",
			deltaX: scrollPixelsPerNotch,
			want:   []wheelEvent{{flags: mouseeventfHWheel, data: wheelData(-wheelDelta)}},
		},
		{
			name:   "one notch right is a negative HWHEEL",
			deltaX: -scrollPixelsPerNotch,
			want:   []wheelEvent{{flags: mouseeventfHWheel, data: wheelDelta}},
		},
		{
			name: "nothing",
			want: nil,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := wheelEvents(testCase.deltaX, testCase.deltaY)
			if len(got) != len(testCase.want) {
				t.Fatalf("wheelEvents(%d, %d) = %v, want %v",
					testCase.deltaX, testCase.deltaY, got, testCase.want)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Errorf("record %d = %+v, want %+v", index, got[index], testCase.want[index])
				}
			}
		})
	}
}

// TestInputRecords_HaveTheSizeSendInputRequires pins the struct layouts. A
// record of the wrong size is not a compile error, it is an injection that
// silently does nothing, which is why the size is asserted here.
func TestInputRecords_HaveTheSizeSendInputRequires(t *testing.T) {
	t.Parallel()

	if got := unsafe.Sizeof(inputRecord{}); got != 40 {
		t.Errorf("sizeof(INPUT with MOUSEINPUT) = %d, want 40", got)
	}

	if got := unsafe.Sizeof(keyInputRecord{}); got != 40 {
		t.Errorf("sizeof(INPUT with KEYBDINPUT) = %d, want 40", got)
	}

	if got := unsafe.Sizeof(mouseInput{}); got != 32 {
		t.Errorf("sizeof(MOUSEINPUT) = %d, want 32", got)
	}

	if got := unsafe.Sizeof(keyboardInput{}); got != 24 {
		t.Errorf("sizeof(KEYBDINPUT) = %d, want 24", got)
	}

	// The absolute range is the 16-bit field's span, and one record must stay
	// inside the signed range an application interprets.
	if absoluteCoordinateRange != 1<<16 {
		t.Errorf("absoluteCoordinateRange = %d, want %d", absoluteCoordinateRange, 1<<16)
	}

	if maxWheelRecord%wheelDelta != 0 || maxWheelRecord > 32767 {
		t.Errorf("maxWheelRecord = %d, which is not a whole number of notches under 32768",
			maxWheelRecord)
	}

	if wheelUnitsPerPixel*wheelUnitsPerPixel == 0 {
		t.Fatalf("wheelUnitsPerPixel = %d, which would make every scroll a no-op", wheelUnitsPerPixel)
	}
}

// TestWheelEventDataIsSigned checks that a negative wheel delta is carried as
// two's complement in the unsigned field, which is how an application reads it
// back as a signed short.
func TestWheelEventDataIsSigned(t *testing.T) {
	t.Parallel()

	events := wheelRecords(-wheelDelta, 0)
	if len(events) != 1 {
		t.Fatalf("wheelRecords(-120, 0) = %v, want one record", events)
	}

	if got := int32(events[0].data); got != -wheelDelta {
		t.Errorf("record data reads back as %d, want %d", got, -wheelDelta)
	}
}

// TestMoveCursorTargetKeepsNegativeCoordinates checks the point a move is built
// from, including a monitor above or left of the primary.
func TestMoveCursorTargetKeepsNegativeCoordinates(t *testing.T) {
	t.Parallel()

	desktop := image.Rect(-1920, 0, 1920, 1080)
	point := image.Pt(-1, 5)

	if !point.In(desktop) {
		t.Fatalf("%v is not inside %v", point, desktop)
	}

	if got := absoluteCoordinate(point.X, desktop.Min.X, desktop.Dx()); got != 32751 {
		t.Errorf("absoluteCoordinate(%d, %d, %d) = %d, want 32751",
			point.X, desktop.Min.X, desktop.Dx(), got)
	}
}
