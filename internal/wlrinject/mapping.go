package wlrinject

import "math"

// Linux evdev mouse button codes from <linux/input-event-codes.h>. The wlroots
// virtual pointer carries one of these, exactly like libei and /dev/uinput do,
// and the compositor routes it to the focused client.
const (
	buttonCodeLeft   uint32 = 0x110 // BTN_LEFT, 272
	buttonCodeRight  uint32 = 0x111 // BTN_RIGHT, 273
	buttonCodeMiddle uint32 = 0x112 // BTN_MIDDLE, 274
)

// The small numbering Button documents (1 left, 2 middle, 3 right), the same
// numbering X11 and the rest of the library use.
const (
	buttonSelectorLeft   = 1
	buttonSelectorMiddle = 2
	buttonSelectorRight  = 3
)

// evdevButton maps a caller's button selector to the evdev code the virtual
// pointer carries.
//
// Both numberings are accepted and cannot collide: the selectors are 1..3 and
// every BTN_* code is far above them, so a caller holding either convention
// gets the button it named. Anything else is refused rather than passed
// through as an arbitrary integer, which the compositor would interpret as some
// other button or, worse, as an unmapped one.
func evdevButton(button int) (uint32, bool) {
	switch button {
	case buttonSelectorLeft, int(buttonCodeLeft):
		return buttonCodeLeft, true
	case buttonSelectorMiddle, int(buttonCodeMiddle):
		return buttonCodeMiddle, true
	case buttonSelectorRight, int(buttonCodeRight):
		return buttonCodeRight, true
	default:
		return 0, false
	}
}

// XKB real-modifier masks. The protocol's modifiers request carries these bits,
// not an enum: bit n is the modifier at index n in the keymap's modifier map,
// and every XKB keymap has the same eight real modifiers in the same order
// (Shift 0, Lock 1, Control 2, Mod1 3, Mod2 4, Mod3 5, Mod4 6, Mod5 7). Only
// the four the library holds are named here.
const (
	modifierShift   uint32 = 1 << 0 // Shift
	modifierControl uint32 = 1 << 2 // Control
	modifierMod1    uint32 = 1 << 3 // Mod1, alt on pc105/us
	modifierMod4    uint32 = 1 << 6 // Mod4, meta/super/logo
)

// modifierMask returns the mask a key code contributes to the virtual
// keyboard's modifier state, and whether the code is a modifier this client
// tracks at all.
//
// The codes are evdev KEY_* values: ctrl 29/97, shift 42/54, alt 56/100 and
// meta 125/126. Caps lock is deliberately not tracked: its effect is a locked
// bit toggled by a key the caller may or may not inject, and the four modifier
// holds the library uses never need it. Any other code is an ordinary key and
// changes no mask.
func modifierMask(code uint32) (uint32, bool) {
	switch code {
	case 29, 97: // KEY_LEFTCTRL, KEY_RIGHTCTRL
		return modifierControl, true
	case 42, 54: // KEY_LEFTSHIFT, KEY_RIGHTSHIFT
		return modifierShift, true
	case 56, 100: // KEY_LEFTALT, KEY_RIGHTALT
		return modifierMod1, true
	case 125, 126: // KEY_LEFTMETA, KEY_RIGHTMETA
		return modifierMod4, true
	default:
		return 0, false
	}
}

// xkbKeycode is the xkbcommon keycode for an evdev code: the wire carries the
// evdev code and xkbcommon's namespace is offset by eight. The package's tests
// use it to check the embedded keymap's table.
func xkbKeycode(evdev uint32) uint32 {
	return evdev + 8
}

// Scroll conversion. The library's convention is pixels, 30 px to a notch, and
// the Wayland axis unit is 10 per notch.
const (
	// scrollPixelsPerNotch is the same ratio the X11 and uinput paths use, so
	// one binding travels the same distance whichever backend answers it.
	scrollPixelsPerNotch = 30

	// axisUnitsPerNotch is the wl_pointer.axis value of one wheel notch.
	axisUnitsPerNotch = 10.0
)

// scrollUnits converts one axis' pixel delta into the two values the virtual
// pointer protocol can carry:
//
//   - value is the continuous wl_pointer.axis length in wl_fixed_t, at 10 units
//     per notch, so a fraction of a notch stays a fraction.
//   - discrete is the whole number of notches, truncated toward zero. A wlroots
//     compositor turns it into wl_pointer.axis_value120 at 120 units per notch
//     for v8+ clients.
//
// Both are zero for a zero delta. The caller sends axis_discrete when the
// compositor has value120 and the delta covers a whole notch; a fractional
// delta is sent as a plain axis event so its fraction reaches the compositor
// instead of being rounded into a detent.
func scrollUnits(pixels int) (value int32, discrete int32) {
	notches := pixels / scrollPixelsPerNotch

	switch {
	case notches > math.MaxInt32:
		discrete = math.MaxInt32
	case notches < math.MinInt32:
		discrete = math.MinInt32
	default:
		discrete = int32(notches)
	}

	return fixedFromDouble(
		float64(pixels) / scrollPixelsPerNotch * axisUnitsPerNotch,
	), discrete
}

// axisDelta is one axis of a scroll, in pixels and in the library's sign
// convention (positive right for horizontal, positive down for vertical).
type axisDelta struct {
	axis   uint32
	pixels int
}

// scrollAxes maps the library's scroll convention onto Wayland axes.
//
// The library says dx is positive left and dy is positive up; wl_pointer's
// horizontal axis is positive right and its vertical axis is positive down, so
// both flip. Vertical comes first so a diagonal scroll arrives in the order a
// wheel would produce it. A zero component is omitted rather than sent as a
// zero-valued axis event.
func scrollAxes(dx, dy int) []axisDelta {
	axes := make([]axisDelta, 0, 2)

	if dy != 0 {
		axes = append(axes, axisDelta{axis: pointerAxisVertical, pixels: -dy})
	}

	if dx != 0 {
		axes = append(axes, axisDelta{axis: pointerAxisHorizontal, pixels: -dx})
	}

	return axes
}

// normalizeCoordinate turns a desktop coordinate into the fraction
// motion_absolute expects: a value relative to the top-left corner of the
// layout and the extent it is measured against.
//
// The layout can start at a negative coordinate (a monitor left of or above the
// origin), which is why origin is subtracted rather than assumed to be zero.
// Values outside the layout are clamped to its edge, and the maximum is
// extent-1 so the fraction never reaches 1.0: a fraction of exactly 1.0 means
// "the far edge", which a compositor may treat as outside the layout. ok is
// false for a layout with no positive extent, which cannot be described.
func normalizeCoordinate(value, origin, extent int) (relative uint32, size uint32, ok bool) {
	if extent <= 0 {
		return 0, 0, false
	}

	// Layout extents are built from int32 output sizes, so this only documents
	// the bound. MaxInt32 rather than MaxUint32 keeps the assignment valid
	// where int is 32 bits wide.
	const maxCoordinateExtent = math.MaxInt32

	if extent > maxCoordinateExtent {
		extent = maxCoordinateExtent
	}

	relative64 := int64(value) - int64(origin)
	if relative64 < 0 {
		relative64 = 0
	}

	if relative64 >= int64(extent) {
		relative64 = int64(extent) - 1
	}

	return uint32(relative64), uint32(extent), true
}

// outputRect is one output's logical rectangle in the compositor's layout.
type outputRect struct {
	X, Y, Width, Height int
}

// layoutBounds returns the union of rects, which is the coordinate space the
// virtual pointer's absolute motion is normalized against. Outputs with no
// positive size are ignored, because a mode that was never reported cannot
// describe where the output's edges are. ok is false when nothing measurable
// was left.
func layoutBounds(rects []outputRect) (outputRect, bool) {
	var (
		minX, minY int
		maxX, maxY int
		have       bool
	)

	for _, rect := range rects {
		if rect.Width <= 0 || rect.Height <= 0 {
			continue
		}

		right := rect.X + rect.Width
		bottom := rect.Y + rect.Height

		if !have {
			minX, minY, maxX, maxY = rect.X, rect.Y, right, bottom
			have = true

			continue
		}

		minX = min(minX, rect.X)
		minY = min(minY, rect.Y)
		maxX = max(maxX, right)
		maxY = max(maxY, bottom)
	}

	if !have {
		return outputRect{}, false
	}

	return outputRect{X: minX, Y: minY, Width: maxX - minX, Height: maxY - minY}, true
}
