//go:build linux

package portal

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// layout is the logical desktop the compositor reports, the union of its
// outputs' rectangles. known is false when no layout could be read, which is
// the usual case on the KDE and GNOME sessions this package exists for; the
// comment on desktop explains why.
type layout struct {
	bounds image.Rectangle
	known  bool
}

// place builds the Frame for a decoded screenshot. img must not be nil.
//
// The portal reports no geometry, so the desktop has to come from somewhere
// else:
//
//   - With a layout, the frame sits at the layout's origin, covers the layout's
//     bounds (or the part of them its pixels describe) and reports the ratio
//     between the PNG's width and the layout's logical width as its scale. A 2x
//     desktop therefore reports Scale 2 with Region in logical pixels, which is
//     the coordinate space a caller's regions are already in.
//   - Without one, the frame covers the PNG itself at scale 1 anchored at the
//     origin. That is exact on an unscaled desktop, where logical and frame
//     pixels are the same thing, and an approximation everywhere else: the
//     pixels are then denser than the region claims, so a caller cropping that
//     frame reads only the top-left part of the desktop. It is reported rather
//     than refused because every other frame source is also unavailable on such
//     a session, and a readable picture of part of the desktop beats
//     errs.CodeNotSupported.
func place(img *image.RGBA, l layout) pixels.Frame {
	size := img.Rect.Size()

	if !l.known || l.bounds.Empty() {
		return pixels.Frame{
			Img:    img,
			Origin: image.Point{},
			Scale:  1,
			Region: image.Rect(0, 0, size.X, size.Y),
		}
	}

	scale := pixels.DeriveScale(size.X, l.bounds.Dx())

	// The pixels describe this much of the layout at that scale. Clipping to
	// the layout keeps a picture that is smaller than it, because a backend
	// scaled it down or the outputs mix scales, from claiming desktop it does
	// not cover.
	covered := image.Rect(
		l.bounds.Min.X,
		l.bounds.Min.Y,
		l.bounds.Min.X+int(math.Round(float64(size.X)/scale)),
		l.bounds.Min.Y+int(math.Round(float64(size.Y)/scale)),
	)

	return pixels.Frame{
		Img:    img,
		Origin: l.bounds.Min,
		Scale:  scale,
		Region: covered.Intersect(l.bounds),
	}
}
