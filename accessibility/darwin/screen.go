//go:build darwin

package darwin

import (
	"image"
	"math"
)

// activeScreenBounds returns the global bounds of the display the cursor is
// on. CoreGraphics already reports display bounds in the accessibility
// coordinate space (top-left origin of the main display, Y growing down), so
// no flip is needed here.
func activeScreenBounds() image.Rectangle {
	displays := displayBounds()
	if len(displays) == 0 {
		return image.Rectangle{}
	}

	cursor := cursorPoint()

	for _, bounds := range displays {
		if !bounds.Empty() && image.Pt(cursor.X, cursor.Y).In(bounds) {
			return bounds
		}
	}

	return displays[0]
}

// allScreenBounds is the union of every active display. A tree walk uses it
// rather than the active display's bounds, because the window being walked may
// live on another monitor.
func allScreenBounds() image.Rectangle {
	displays := displayBounds()
	if len(displays) == 0 {
		return image.Rectangle{}
	}

	union := displays[0]

	for _, bounds := range displays[1:] {
		union = union.Union(bounds)
	}

	return union
}

// displayBounds returns every active display's bounds, in CoreGraphics'
// top-left-origin coordinate space. The main display is first when CoreGraphics
// reports it, but callers must not depend on the order.
func displayBounds() []image.Rectangle {
	api, err := loadFrameworks()
	if err != nil {
		return nil
	}

	mainDisplay := api.cg.mainDisplayID()
	fallback := rectFromCG(api.cg.displayBounds(mainDisplay))

	count := uint32(0)

	status := api.cg.getActiveDisplays(0, nil, &count)
	if status != 0 || count == 0 {
		return []image.Rectangle{fallback}
	}

	displays := make([]uint32, count)
	if api.cg.getActiveDisplays(count, &displays[0], &count) != 0 {
		return []image.Rectangle{fallback}
	}

	bounds := make([]image.Rectangle, 0, count)

	for _, display := range displays[:count] {
		rect := rectFromCG(api.cg.displayBounds(display))
		if !rect.Empty() {
			bounds = append(bounds, rect)
		}
	}

	if len(bounds) == 0 {
		return []image.Rectangle{fallback}
	}

	return bounds
}

// cursorPoint returns the pointer position without needing a Client, so screen
// bounds can be computed before a client exists.
func cursorPoint() image.Point {
	api, err := loadFrameworks()
	if err != nil {
		return image.Point{}
	}

	event := api.cg.eventCreate(0)
	if event == 0 {
		return image.Point{}
	}

	defer api.cf.release(event)

	location := api.cg.eventGetLocation(event)

	return image.Pt(int(math.Round(location.X)), int(math.Round(location.Y)))
}

// rectFromCG converts a CoreGraphics rectangle to an image rectangle, rounding
// each edge to the nearest point.
func rectFromCG(value cgRect) image.Rectangle {
	return image.Rect(
		int(math.Round(value.Origin.X)),
		int(math.Round(value.Origin.Y)),
		int(math.Round(value.Origin.X+value.Size.Width)),
		int(math.Round(value.Origin.Y+value.Size.Height)),
	)
}
