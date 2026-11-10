//go:build linux

package portal

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Frame is one captured screen area: the pixels, where they belong on the
// desktop, and the scale between the two.
type Frame struct {
	// Img is the captured pixels in RGBA, its rectangle anchored at (0,0).
	Img *image.RGBA
	// Origin is where the frame's top-left corner sits on the desktop, in
	// logical pixels.
	Origin image.Point
	// Scale is frame pixels per logical pixel, at least 1.
	Scale float64
	// Region is the logical desktop area the frame covers. Img holds that area
	// at Scale, so Region.Dx()*Scale is the image's width whenever the portal's
	// picture and the compositor's layout agree.
	Region image.Rectangle
}

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
//     bounds (or the part of them its pixels describe, see covered below) and
//     reports the ratio between the PNG's width and the layout's logical width
//     as its scale. A 2x desktop therefore reports Scale 2 with
//     Region in logical pixels, which is the coordinate space a caller's
//     regions are already in.
//   - Without one, the frame covers the PNG itself at scale 1 anchored at the
//     origin. That is exact on an unscaled desktop, where logical and frame
//     pixels are the same thing, and an approximation everywhere else: the
//     pixels are then denser than the region claims, so a caller cropping that
//     frame reads only the top-left part of the desktop. It is reported rather
//     than refused because every other frame source is also unavailable on such
//     a session, and a readable picture of part of the desktop beats
//     errs.CodeNotSupported.
func place(img *image.RGBA, l layout) Frame {
	size := img.Rect.Size()

	if !l.known || l.bounds.Empty() {
		return Frame{
			Img:    img,
			Origin: image.Point{},
			Scale:  1,
			Region: image.Rect(0, 0, size.X, size.Y),
		}
	}

	scale := deriveScale(size.X, l.bounds.Dx())

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

	return Frame{
		Img:    img,
		Origin: l.bounds.Min,
		Scale:  scale,
		Region: covered.Intersect(l.bounds),
	}
}

// deriveScale computes frame pixels per logical pixel from the PNG's width and
// the layout's logical width. The layout's logical size is a quotient of the
// compositor's mode and the output scale, rounded to a whole pixel, so the
// ratio is rounded to two decimals to recover 1.5 rather than 1.4997.
//
// A ratio below 1 is clamped to 1, which is what Frame promises. That only
// happens when the portal answered with fewer pixels than the desktop has
// logical columns, and place() then reports the smaller region the pixels
// actually cover.
//
// A desktop whose outputs use different scales has no single ratio. The whole
// width is used, which is exact when every output scales alike and an
// approximation when they do not.
func deriveScale(frameWidth, logicalWidth int) float64 {
	if frameWidth <= 0 || logicalWidth <= 0 {
		return 1
	}

	scale := float64(frameWidth) / float64(logicalWidth)
	if scale < 1 {
		return 1
	}

	return math.Round(scale*100) / 100
}

// crop returns the part of frame that covers region, a logical desktop
// rectangle. Region is region intersected with the frame's own region and
// Origin is that intersection's top-left, so a caller that asked for a window
// hanging off the desktop gets the pixels that exist and can see that they were
// clipped.
//
// The pixel rectangle is derived from the logical offset through the frame's
// scale and then clipped to the image, because rounding at a fractional scale
// can ask for a row or column the image does not have.
func crop(frame Frame, region image.Rectangle) (Frame, error) {
	clipped := region.Canon().Intersect(frame.Region)
	if clipped.Empty() {
		return Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region %v lies outside the captured desktop %v",
			region,
			frame.Region,
		)
	}

	pixels := pixelRect(clipped, frame.Region.Min, frame.Scale, frame.Img.Rect)
	if pixels.Empty() {
		return Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region %v maps to no pixel of the captured desktop %v at scale %v",
			region,
			frame.Region,
			frame.Scale,
		)
	}

	return Frame{
		Img:    cropImage(frame.Img, pixels),
		Origin: clipped.Min,
		Scale:  frame.Scale,
		Region: clipped,
	}, nil
}

// pixelRect maps a logical rectangle inside a frame onto the frame's pixels.
// min is the frame's logical origin and bounds is the image, which the result
// is clipped to.
func pixelRect(logical image.Rectangle, min image.Point, scale float64, bounds image.Rectangle) image.Rectangle {
	if scale <= 0 {
		scale = 1
	}

	pixels := image.Rect(
		int(math.Round(float64(logical.Min.X-min.X)*scale)),
		int(math.Round(float64(logical.Min.Y-min.Y)*scale)),
		int(math.Round(float64(logical.Max.X-min.X)*scale)),
		int(math.Round(float64(logical.Max.Y-min.Y)*scale)),
	)

	return pixels.Intersect(bounds)
}
