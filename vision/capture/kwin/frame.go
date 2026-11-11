package kwin

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

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
func placeScreen(img *image.RGBA, meta metadata, l layout) pixels.Frame {
	size := img.Rect.Size()

	scale := meta.scaleOr(1)
	logical := logicalSize(size, scale)

	if !l.known || l.bounds.Empty() {
		return pixels.Frame{
			Img:    img,
			Origin: image.Point{},
			Scale:  scale,
			Region: image.Rect(0, 0, logical.X, logical.Y),
		}
	}

	if meta.hasScale {
		if rect, ok := l.match(logical); ok {
			return pixels.Frame{Img: img, Origin: rect.Min, Scale: scale, Region: rect}
		}

		// The image is not one output or the whole desktop at that scale, for
		// example because the outputs mix scales. Anchoring it at the layout's
		// origin and reporting only the part it covers keeps the frame from
		// claiming desktop it has no pixels for.
		return pixels.Frame{
			Img:    img,
			Origin: l.bounds.Min,
			Scale:  scale,
			Region: covered(logical, l.bounds.Min, l.bounds),
		}
	}

	if rect, inferred, ok := l.fit(size); ok {
		return pixels.Frame{Img: img, Origin: rect.Min, Scale: inferred, Region: rect}
	}

	scale = 1

	return pixels.Frame{
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
func placeArea(img *image.RGBA, meta metadata, region image.Rectangle) pixels.Frame {
	scale := 1.0

	if meta.hasScale && meta.scale >= 1 {
		scale = meta.scale
	} else if region.Dx() > 0 && img.Rect.Dx() > 0 {
		// A reply without a scale still rendered at some scale; the ratio
		// between the pixels and the requested logical width is it.
		scale = pixels.DeriveScale(img.Rect.Dx(), region.Dx())
	}

	return pixels.Frame{
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
