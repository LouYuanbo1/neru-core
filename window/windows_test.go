//go:build windows

package window

import (
	"image"
	"testing"
)

func TestFormatWindowHandle_ParseRoundTrip(t *testing.T) {
	for _, handle := range []uintptr{1, 0x3f0b58, 0xdeadbeef, 1 << 40} {
		formatted := formatWindowHandle(handle)

		parsed, ok := parseWindowHandle(formatted)
		if !ok {
			t.Fatalf("parseWindowHandle(%q) failed", formatted)
		}

		if parsed != handle {
			t.Fatalf("parseWindowHandle(%q) = %#x, want %#x", formatted, parsed, handle)
		}
	}
}

func TestParseWindowHandle_AcceptsSpellingVariants(t *testing.T) {
	for _, value := range []string{"0x3f0b58", "0X3F0B58", " 3f0b58 ", "3F0B58"} {
		parsed, ok := parseWindowHandle(value)
		if !ok {
			t.Fatalf("parseWindowHandle(%q) failed", value)
		}

		if parsed != 0x3f0b58 {
			t.Fatalf("parseWindowHandle(%q) = %#x", value, parsed)
		}
	}
}

func TestParseWindowHandle_RejectsMalformedIDs(t *testing.T) {
	for _, value := range []string{"", "  ", "0x", "not-a-handle", "0xzz", "0x0"} {
		if parsed, ok := parseWindowHandle(value); ok {
			t.Fatalf("parseWindowHandle(%q) = %#x, want a rejection", value, parsed)
		}
	}
}

func TestWin32Rect_EmptyRectanglesAreInvalid(t *testing.T) {
	cases := map[string]win32Rect{
		"zero":     {},
		"flat":     {left: 0, top: 0, right: 10, bottom: 0},
		"inverted": {left: 10, top: 10, right: 0, bottom: 0},
	}

	for name, rect := range cases {
		if rect.valid() {
			t.Fatalf("%s: valid() = true", name)
		}
	}
}

func TestWin32Rect_RectKeepsANegativeOrigin(t *testing.T) {
	rect := win32Rect{left: -11, top: -11, right: 2171, bottom: 1379}

	if got, want := rect.rect(), image.Rect(-11, -11, 2171, 1379); got != want {
		t.Fatalf("rect() = %v, want %v", got, want)
	}
}

func TestFocusedFirst_MovesTheFocusedWindowToTheFront(t *testing.T) {
	windows := []Info{
		{ID: "0x1", Title: "overlay"},
		{ID: "0x2", Title: "editor", Focused: true},
		{ID: "0x3", Title: "browser"},
	}

	ordered := focusedFirst(windows)

	if ordered[0].ID != "0x2" {
		t.Fatalf("first window is %q, want the focused one", ordered[0].ID)
	}

	if len(ordered) != len(windows) {
		t.Fatalf("focusedFirst changed the window count: %d -> %d", len(windows), len(ordered))
	}

	// The relative order of the rest is preserved.
	if ordered[1].ID != "0x1" || ordered[2].ID != "0x3" {
		t.Fatalf("unfocused order changed: %+v", ordered)
	}
}

func TestFocusedFirst_LeavesAnAlreadyOrderedListAlone(t *testing.T) {
	windows := []Info{
		{ID: "0x1", Focused: true},
		{ID: "0x2"},
	}

	ordered := focusedFirst(windows)

	if ordered[0].ID != "0x1" || ordered[1].ID != "0x2" {
		t.Fatalf("focusedFirst reordered a correct list: %+v", ordered)
	}
}

func TestFocusedFirst_WithNoFocusedWindow(t *testing.T) {
	windows := []Info{{ID: "0x1"}, {ID: "0x2"}}

	ordered := focusedFirst(windows)

	if ordered[0].ID != "0x1" || ordered[1].ID != "0x2" {
		t.Fatalf("focusedFirst reordered a list with no focused window: %+v", ordered)
	}
}
