// Package pixels is the shared pixel layer of the vision half: the Frame a
// capture produces and a detector consumes, the crop and scale geometry every
// capture backend needs, and the pixel operations the pure-Go detectors share.
//
// A frame is screen content. Nothing here logs, copies past the call, or
// retains the pixels.
package pixels

import (
	"image"
	"image/draw"
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Frame is one capture placed on the desktop: the pixels, where their top-left
// corner sits, the logical region they cover, and how many frame pixels make one
// logical pixel. A capture backend returns one, and the detectors and pipeline
// in the vision package read one.
//
// The frame may be wider than the region the caller asked for (a capture backend
// may win back a whole display) but never narrower.
type Frame struct {
	// Img is the captured pixels in RGBA, its rectangle anchored at (0,0).
	Img *image.RGBA
	// Origin is where the frame's top-left corner sits on the desktop, in
	// logical pixels. It can be negative on a layout with an output left of or
	// above the primary one.
	Origin image.Point
	// Region is the logical desktop area the frame covers. Img holds that area
	// at Scale, so Region.Dx()*Scale is the image's width whenever the pixels and
	// the desktop layout agree.
	Region image.Rectangle
	// Scale is frame pixels per logical pixel, at least 1.
	Scale float64
}

// Crop returns the part of f that covers region, a logical desktop rectangle.
// Region is region intersected with the frame's own region and Origin is that
// intersection's top-left, so a caller that asked for a window hanging off the
// desktop gets the pixels that exist and can see that they were clipped.
//
// The pixel rectangle is derived from the logical offset through the frame's
// scale and then clipped to the image, because rounding at a fractional scale
// can ask for a row or column the image does not have.
func Crop(f Frame, region image.Rectangle) (Frame, error) {
	clipped := region.Canon().Intersect(f.Region)
	if clipped.Empty() {
		return Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region %v lies outside the captured desktop %v",
			region,
			f.Region,
		)
	}

	pixels := pixelRect(clipped, f.Region.Min, f.Scale, f.Img.Rect)
	if pixels.Empty() {
		return Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region %v maps to no pixel of the captured desktop %v at scale %v",
			region,
			f.Region,
			f.Scale,
		)
	}

	return Frame{
		Img:    cropImage(f.Img, pixels),
		Origin: clipped.Min,
		Scale:  f.Scale,
		Region: clipped,
	}, nil
}

// DeriveScale computes frame pixels per logical pixel from the image's width and
// the logical width it covers. The desktop's logical size is a quotient of the
// compositor's mode and the output scale, rounded to a whole pixel, so the ratio
// is rounded to two decimals to recover 1.5 rather than 1.4997.
//
// A ratio below 1 is clamped to 1, which is what Frame promises. That only
// happens when the image has fewer pixels than the area has logical columns, and
// a capture backend then reports the smaller region the pixels actually cover.
func DeriveScale(frameWidth, logicalWidth int) float64 {
	if frameWidth <= 0 || logicalWidth <= 0 {
		return 1
	}

	scale := float64(frameWidth) / float64(logicalWidth)
	if scale < 1 {
		return 1
	}

	return math.Round(scale*100) / 100
}

// pixelRect maps a logical rectangle inside a frame onto the frame's pixels. min
// is the frame's logical origin and bounds is the image, which the result is
// clipped to.
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

// cropImage copies pixels out of src into a new image anchored at (0,0), so a
// cropped frame does not keep the whole screenshot alive and its rectangle
// starts where a caller expects it to.
func cropImage(src *image.RGBA, pixels image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, pixels.Dx(), pixels.Dy()))
	draw.Draw(dst, dst.Rect, src, pixels.Min, draw.Src)

	return dst
}
