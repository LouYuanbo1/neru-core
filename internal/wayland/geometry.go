//go:build linux

package wayland

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// outputRect is o's logical rectangle on the compositor's desktop layout.
func outputRect(o Output) image.Rectangle {
	return image.Rect(o.X, o.Y, o.X+o.Width, o.Y+o.Height)
}

// outputAt returns the output whose layout rectangle contains p. Rectangles are
// half-open, so a point on a shared edge belongs to the output whose top-left
// corner it is, never to both.
func outputAt(outputs []Output, p image.Point) (Output, bool) {
	for _, o := range outputs {
		if p.In(outputRect(o)) {
			return o, true
		}
	}

	return Output{}, false
}

// regionCenter is the layout point that decides which output a region belongs
// to. Using the centre rather than a corner is what makes a region that starts
// at an output's right edge belong to the next output.
func regionCenter(r image.Rectangle) image.Point {
	return image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
}

// logicalToPixel converts a logical desktop rectangle into the pixel
// coordinates of one output. Each edge is rounded to the nearest pixel, so a
// scaled capture is the logical size times scale, give or take the rounding a
// fractional scale forces.
func logicalToPixel(r image.Rectangle, origin image.Point, scale float64) image.Rectangle {
	if scale <= 0 {
		scale = 1
	}

	round := func(v, offset int) int {
		return int(math.Round(float64(v-offset) * scale))
	}

	return image.Rect(
		round(r.Min.X, origin.X),
		round(r.Min.Y, origin.Y),
		round(r.Max.X, origin.X),
		round(r.Max.Y, origin.Y),
	)
}

// cropImage returns the part of img inside r, anchored at (0,0). r is clipped
// to the image; an empty result is an error because a capture must fail rather
// than hand back pixels the caller did not ask for.
func cropImage(img *image.RGBA, r image.Rectangle) (*image.RGBA, error) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return nil, errs.New(
			errs.CodeInternal,
			"the captured frame does not cover the requested region",
		)
	}

	if r == img.Bounds() {
		return img, nil
	}

	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))

	for y := 0; y < r.Dy(); y++ {
		src := (r.Min.Y-img.Rect.Min.Y+y)*img.Stride + (r.Min.X-img.Rect.Min.X)*4
		dst := y * out.Stride
		copy(out.Pix[dst:dst+r.Dx()*4], img.Pix[src:src+r.Dx()*4])
	}

	return out, nil
}
