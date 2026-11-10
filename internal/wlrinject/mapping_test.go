package wlrinject

import "testing"

func TestEvdevButton_MapsBothNumberings(t *testing.T) {
	tests := []struct {
		name   string
		button int
		want   uint32
	}{
		{name: "selector left", button: 1, want: buttonCodeLeft},
		{name: "selector middle", button: 2, want: buttonCodeMiddle},
		{name: "selector right", button: 3, want: buttonCodeRight},
		{name: "evdev left", button: 272, want: buttonCodeLeft},
		{name: "evdev right", button: 273, want: buttonCodeRight},
		{name: "evdev middle", button: 274, want: buttonCodeMiddle},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := evdevButton(test.button)
			if !ok {
				t.Fatalf("evdevButton(%d) refused a documented button", test.button)
			}

			if got != test.want {
				t.Errorf("evdevButton(%d) = %#x, want %#x", test.button, got, test.want)
			}
		})
	}
}

func TestEvdevButton_RefusesUnknownButton(t *testing.T) {
	for _, button := range []int{0, 4, -1, 271, 275, 0x150} {
		if _, ok := evdevButton(button); ok {
			t.Errorf("evdevButton(%d) accepted a code no mouse button uses", button)
		}
	}
}

func TestModifierMask_MapsTheModifiersAndTheirRightHandTwins(t *testing.T) {
	tests := []struct {
		name string
		code uint32
		want uint32
	}{
		{name: "left ctrl", code: 29, want: modifierControl},
		{name: "right ctrl", code: 97, want: modifierControl},
		{name: "left shift", code: 42, want: modifierShift},
		{name: "right shift", code: 54, want: modifierShift},
		{name: "left alt", code: 56, want: modifierMod1},
		{name: "right alt", code: 100, want: modifierMod1},
		{name: "left meta", code: 125, want: modifierMod4},
		{name: "right meta", code: 126, want: modifierMod4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := modifierMask(test.code)
			if !ok {
				t.Fatalf("modifierMask(%d) did not recognise a modifier", test.code)
			}

			if got != test.want {
				t.Errorf("modifierMask(%d) = %#x, want %#x", test.code, got, test.want)
			}
		})
	}
}

func TestModifierMask_LeavesOrdinaryKeysAlone(t *testing.T) {
	for _, code := range []uint32{0, 1, 2, 30, 57, 58, 96, 103, 108, 127, 255} {
		if mask, ok := modifierMask(code); ok || mask != 0 {
			t.Errorf("modifierMask(%d) = %#x, %v, want 0, false", code, mask, ok)
		}
	}
}

func TestScrollUnits_ConvertsPixelsToAxisAndNotches(t *testing.T) {
	tests := []struct {
		name         string
		pixels       int
		wantValue    int32
		wantDiscrete int32
	}{
		{name: "one notch down", pixels: 30, wantValue: 10 * 256, wantDiscrete: 1},
		{name: "three notches down", pixels: 90, wantValue: 30 * 256, wantDiscrete: 3},
		{name: "one notch up", pixels: -30, wantValue: -10 * 256, wantDiscrete: -1},
		{name: "half a notch down", pixels: 15, wantValue: 5 * 256, wantDiscrete: 0},
		{name: "half a notch up", pixels: -15, wantValue: -5 * 256, wantDiscrete: 0},
		{name: "less than half a notch", pixels: 5, wantValue: 427, wantDiscrete: 0},
		{name: "zero", pixels: 0, wantValue: 0, wantDiscrete: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, discrete := scrollUnits(test.pixels)

			if value != test.wantValue {
				t.Errorf("scrollUnits(%d) value = %d, want %d", test.pixels, value, test.wantValue)
			}

			if discrete != test.wantDiscrete {
				t.Errorf(
					"scrollUnits(%d) discrete = %d, want %d",
					test.pixels,
					discrete,
					test.wantDiscrete,
				)
			}
		})
	}
}

func TestScrollAxes_FlipsBothLibraryDirections(t *testing.T) {
	tests := []struct {
		name    string
		dx      int
		dy      int
		want    []axisDelta
		wantNil bool
	}{
		{
			name: "up",
			dy:   30,
			want: []axisDelta{{axis: pointerAxisVertical, pixels: -30}},
		},
		{
			name: "left",
			dx:   60,
			want: []axisDelta{{axis: pointerAxisHorizontal, pixels: -60}},
		},
		{
			name: "down and right",
			dx:   -60,
			dy:   -30,
			want: []axisDelta{
				{axis: pointerAxisVertical, pixels: 30},
				{axis: pointerAxisHorizontal, pixels: 60},
			},
		},
		{
			name:    "nothing",
			wantNil: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := scrollAxes(test.dx, test.dy)

			if test.wantNil {
				if len(got) != 0 {
					t.Fatalf("scrollAxes(%d, %d) = %v, want no axes", test.dx, test.dy, got)
				}

				return
			}

			if len(got) != len(test.want) {
				t.Fatalf("scrollAxes(%d, %d) = %v, want %v", test.dx, test.dy, got, test.want)
			}

			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("axis %d = %+v, want %+v", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestNormalizeCoordinate_SubtractsLayoutOrigin(t *testing.T) {
	tests := []struct {
		name         string
		value        int
		origin       int
		extent       int
		wantRelative uint32
		wantExtent   uint32
	}{
		{name: "origin at zero", value: 100, origin: 0, extent: 1920, wantRelative: 100, wantExtent: 1920},
		{name: "negative origin", value: 5, origin: -1920, extent: 3840, wantRelative: 1925, wantExtent: 3840},
		{name: "left edge", value: -1920, origin: -1920, extent: 3840, wantRelative: 0, wantExtent: 3840},
		{name: "last pixel", value: 1919, origin: -1920, extent: 3840, wantRelative: 3839, wantExtent: 3840},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relative, extent, ok := normalizeCoordinate(test.value, test.origin, test.extent)
			if !ok {
				t.Fatal("normalizeCoordinate refused a measurable layout")
			}

			if relative != test.wantRelative || extent != test.wantExtent {
				t.Errorf(
					"normalizeCoordinate(%d, %d, %d) = (%d, %d), want (%d, %d)",
					test.value,
					test.origin,
					test.extent,
					relative,
					extent,
					test.wantRelative,
					test.wantExtent,
				)
			}
		})
	}
}

func TestNormalizeCoordinate_ClampsOutsideTheLayout(t *testing.T) {
	tests := []struct {
		name         string
		value        int
		wantRelative uint32
	}{
		{name: "far left", value: -10000, wantRelative: 0},
		{name: "far right", value: 10000, wantRelative: 1919},
		{name: "exactly the right edge", value: 1920, wantRelative: 1919},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relative, extent, ok := normalizeCoordinate(test.value, 0, 1920)
			if !ok {
				t.Fatal("normalizeCoordinate refused a measurable layout")
			}

			if relative != test.wantRelative || extent != 1920 {
				t.Errorf(
					"normalizeCoordinate(%d, 0, 1920) = (%d, %d), want (%d, 1920)",
					test.value,
					relative,
					extent,
					test.wantRelative,
				)
			}
		})
	}
}

func TestNormalizeCoordinate_RefusesEmptyExtent(t *testing.T) {
	for _, extent := range []int{0, -1} {
		if _, _, ok := normalizeCoordinate(10, 0, extent); ok {
			t.Errorf("normalizeCoordinate accepted extent %d", extent)
		}
	}
}

func TestLayoutBounds_UnionsOutputs(t *testing.T) {
	tests := []struct {
		name  string
		rects []outputRect
		want  outputRect
	}{
		{
			name:  "single output at the origin",
			rects: []outputRect{{X: 0, Y: 0, Width: 1920, Height: 1080}},
			want:  outputRect{X: 0, Y: 0, Width: 1920, Height: 1080},
		},
		{
			name: "monitor to the left",
			rects: []outputRect{
				{X: 0, Y: 0, Width: 1920, Height: 1080},
				{X: -2560, Y: 0, Width: 2560, Height: 1440},
			},
			want: outputRect{X: -2560, Y: 0, Width: 4480, Height: 1440},
		},
		{
			name: "stacked with a vertical offset",
			rects: []outputRect{
				{X: 0, Y: -200, Width: 1920, Height: 1080},
				{X: 1920, Y: 300, Width: 1280, Height: 1024},
			},
			want: outputRect{X: 0, Y: -200, Width: 3200, Height: 1524},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := layoutBounds(test.rects)
			if !ok {
				t.Fatal("layoutBounds refused measurable outputs")
			}

			if got != test.want {
				t.Errorf("layoutBounds = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLayoutBounds_RefusesUnmeasurableOutputs(t *testing.T) {
	tests := []struct {
		name  string
		rects []outputRect
	}{
		{name: "none"},
		{name: "zero sized", rects: []outputRect{{X: 0, Y: 0}}},
		{name: "negative", rects: []outputRect{{X: 0, Y: 0, Width: -1920, Height: 1080}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := layoutBounds(test.rects); ok {
				t.Errorf("layoutBounds(%v) reported a layout with no measurable output", test.rects)
			}
		})
	}
}

func TestXKBKeycode_OffsetsTheEvdevCode(t *testing.T) {
	tests := []struct {
		name  string
		evdev uint32
		want  uint32
	}{
		{name: "left ctrl", evdev: 29, want: 37},
		{name: "left shift", evdev: 42, want: 50},
		{name: "left alt", evdev: 56, want: 64},
		{name: "left meta", evdev: 125, want: 133},
	}

	for _, test := range tests {
		if got := xkbKeycode(test.evdev); got != test.want {
			t.Errorf("xkbKeycode(%d) = %d, want %d", test.evdev, got, test.want)
		}
	}
}
