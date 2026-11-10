package kwin

import (
	"image"
	"image/draw"
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Frame is one captured screen area: the pixels, where they belong on the
// desktop, and the scale between the two.
type Frame struct {
	// Img is the captured pixels in RGBA, its rectangle anchored at (0,0).
	Img *image.RGBA
	// Origin is where the frame's top-left corner sits on the desktop, in
	// logical pixels. It can be negative on a layout with an output left of or
	// above the primary one.
	Origin image.Point
	// Scale is frame pixels per logical pixel, at least 1. KWin's reply carries
	// it, so a 2x output reports 2 whenever the reply is new enough to say so.
	Scale float64
	// Region is the logical desktop area the frame covers. Img holds that area
	// at Scale, so Region.Dx()*Scale is the image's width whenever KWin's
	// pixels and its layout agree.
	Region image.Rectangle
}

// layout is the logical desktop the compositor reports: the union of its
// outputs' rectangles, plus each output's rectangle. known is false when no
// layout could be read, which is the usual case on a KWin Wayland session (see
// the package comment); the frame's scale and region still come from KWin's own
// metadata in that case.
type layout struct {
	bounds  image.Rectangle
	outputs []image.Rectangle
	known   bool
}

// placeScreen builds the Frame for a capture of one whole output, such as
// CaptureActiveScreen's.
//
// KWin's metadata says how large the image is and what scale it was rendered
// at, but not where on the desktop the captured output sits, so the desktop
// has to come from somewhere else:
//
//   - With a layout and a metadata scale, the image's logical size is matched
//     against the layout's outputs and then against the whole desktop. An exact
//     match places the frame on that rectangle, which is exact for a desktop
//     whose outputs all use the same scale and a match in practice whenever
//     KWin's metadata and the compositor's layout describe the same output.
//   - The metadata scale may be missing before ScreenShot2 version 4 (KWin
//     5.27). The scale is then inferred from the layout rectangle the image is
//     an exact multiple of. Two outputs of the same size make that guess
//     ambiguous, and the first match wins.
//   - Without a layout the frame is anchored at the origin and covers the
//     image's own logical size. That is exact for a single unscaled output and
//     an approximation for a multi-output or scaled desktop, where only the
//     pixels are trustworthy and the caller's region may not line up with them.
func placeScreen(img *image.RGBA, meta metadata, l layout) Frame {
	size := img.Rect.Size()

	scale := meta.scaleOr(1)
	logical := logicalSize(size, scale)

	if !l.known || l.bounds.Empty() {
		return Frame{
			Img:    img,
			Origin: image.Point{},
			Scale:  scale,
			Region: image.Rect(0, 0, logical.X, logical.Y),
		}
	}

	if meta.hasScale {
		if rect, ok := l.match(logical); ok {
			return Frame{Img: img, Origin: rect.Min, Scale: scale, Region: rect}
		}

		// The image is not one output or the whole desktop at that scale, for
		// example because the outputs mix scales. Anchoring it at the layout's
		// origin and reporting only the part it covers keeps the frame from
		// claiming desktop it has no pixels for.
		return Frame{
			Img:    img,
			Origin: l.bounds.Min,
			Scale:  scale,
			Region: covered(logical, l.bounds.Min, l.bounds),
		}
	}

	if rect, inferred, ok := l.fit(size); ok {
		return Frame{Img: img, Origin: rect.Min, Scale: inferred, Region: rect}
	}

	scale = 1

	return Frame{
		Img:    img,
		Origin: l.bounds.Min,
		Scale:  scale,
		Region: covered(logicalSize(size, scale), l.bounds.Min, l.bounds),
	}
}

// placeArea builds the Frame for a capture of an explicit logical rectangle,
// such as CaptureArea's.
//
// KWin renders exactly the rectangle it was asked for, at the scale its
// metadata reports (the largest output scale when native-resolution is set),
// so the requested region is authoritative and only the image's actual size
// decides how much of it exists: an image smaller than the request narrows the
// reported region, which keeps Region honest about the pixels.
func placeArea(img *image.RGBA, meta metadata, region image.Rectangle) Frame {
	scale := 1.0

	if meta.hasScale && meta.scale >= 1 {
		scale = meta.scale
	} else if region.Dx() > 0 && img.Rect.Dx() > 0 {
		// A reply without a scale still rendered at some scale; the ratio
		// between the pixels and the requested logical width is it.
		scale = deriveScale(img.Rect.Dx(), region.Dx())
	}

	return Frame{
		Img:    img,
		Origin: region.Min,
		Scale:  scale,
		Region: covered(logicalSize(img.Rect.Size(), scale), region.Min, region),
	}
}

// match finds the layout rectangle whose logical size is exactly size.
func (l layout) match(size image.Point) (image.Rectangle, bool) {
	for _, rect := range l.outputs {
		if rect.Size() == size {
			return rect, true
		}
	}

	if l.bounds.Size() == size {
		return l.bounds, true
	}

	return image.Rectangle{}, false
}

// fit finds the layout rectangle the image is an exact multiple of, and returns
// that multiple as a scale. It is the fallback for a reply that carries no
// scale, where the image's own size is the only evidence of one.
func (l layout) fit(size image.Point) (image.Rectangle, float64, bool) {
	for _, rect := range l.outputs {
		if scale, ok := fitScale(size, rect); ok {
			return rect, scale, true
		}
	}

	if scale, ok := fitScale(size, l.bounds); ok {
		return l.bounds, scale, true
	}

	return image.Rectangle{}, 0, false
}

// fitScale reports the scale at which rect's logical size becomes size, if size
// is exactly rect scaled by at least 1.
func fitScale(size image.Point, rect image.Rectangle) (float64, bool) {
	if rect.Dx() <= 0 || rect.Dy() <= 0 || size.X <= 0 || size.Y <= 0 {
		return 0, false
	}

	horizontal := float64(size.X) / float64(rect.Dx())
	vertical := float64(size.Y) / float64(rect.Dy())

	if horizontal < 1 || math.Abs(horizontal-vertical) > 0.005*math.Max(horizontal, vertical) {
		return 0, false
	}

	// The compositor's layout size is a quotient of the mode and the scale,
	// rounded to a whole pixel, so the ratio is rounded to two decimals to
	// recover 1.5 rather than 1.4997.
	scale := math.Round(horizontal*100) / 100

	if int(math.Round(float64(rect.Dx())*scale)) != size.X ||
		int(math.Round(float64(rect.Dy())*scale)) != size.Y {
		return 0, false
	}

	return scale, true
}

// logicalSize is the logical size of an image rendered at scale.
func logicalSize(size image.Point, scale float64) image.Point {
	if scale <= 0 {
		scale = 1
	}

	return image.Pt(
		int(math.Round(float64(size.X)/scale)),
		int(math.Round(float64(size.Y)/scale)),
	)
}

// covered is the part of within that an image of logical size size at at
// describes.
func covered(size image.Point, at image.Point, within image.Rectangle) image.Rectangle {
	return image.Rect(at.X, at.Y, at.X+size.X, at.Y+size.Y).Intersect(within)
}

// deriveScale computes frame pixels per logical pixel from the image's width and
// the logical width it covers. The layout's logical size is a quotient of the
// compositor's mode and the output scale, rounded to a whole pixel, so the ratio
// is rounded to two decimals to recover 1.5 rather than 1.4997.
//
// A ratio below 1 is clamped to 1, which is what Frame promises. That only
// happens when the image has fewer pixels than the area has logical columns,
// and placeArea then reports the smaller region the pixels actually cover.
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

// cropImage copies pixels out of src into a new image anchored at (0,0), so a
// cropped frame does not keep the whole screenshot alive and its rectangle
// starts where a caller expects it to.
func cropImage(src *image.RGBA, pixels image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, pixels.Dx(), pixels.Dy()))
	draw.Draw(dst, dst.Rect, src, pixels.Min, draw.Src)

	return dst
}
