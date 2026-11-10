package winrt

import (
	"image"
	"math"
)

// bytesPerPixel is the stride of the RGBA and Bgra8 layouts this package moves
// pixels between.
const bytesPerPixel = 4

// winrtRect mirrors Windows.Foundation.Rect: four float32 edges relative to the
// top-left corner of the bitmap the engine read.
type winrtRect struct {
	X, Y, Width, Height float32
}

// wordBounds converts a WinRT box into a rectangle in img's pixel space,
// undoing the downsample factor the frame went through and clipping the result
// to the image.
//
// The engine reports whole and fractional pixels against the bitmap it was
// given, which is at most MaxImageDimension on its long side. A frame past that
// cap is downsampled by a whole factor before it is handed over (downsample), so
// every box has to be multiplied by the same factor to land on the pixel it was
// read from.
//
// Rounding is to the nearest pixel rather than truncation: the boxes that come
// back from the engine are consistently a fraction of a pixel off, and rounding
// keeps a word's edge on the ink instead of a pixel inside it.
//
// A box with a non-finite edge describes nothing a caller can use, and one that
// falls entirely outside the frame is the engine's answer about a region that
// does not exist. Both clip to an empty rectangle, which the caller drops.
func wordBounds(rect winrtRect, factor int, img image.Rectangle) image.Rectangle {
	scale := float64(factor)
	left := float64(rect.X) * scale
	top := float64(rect.Y) * scale
	right := float64(rect.X+rect.Width) * scale
	bottom := float64(rect.Y+rect.Height) * scale

	if !finite(left) || !finite(top) || !finite(right) || !finite(bottom) {
		return image.Rectangle{}
	}

	bounds := image.Rect(
		int(math.Round(left)),
		int(math.Round(top)),
		int(math.Round(right)),
		int(math.Round(bottom)),
	).Add(img.Min)

	return bounds.Intersect(img)
}

// unionBounds grows box to include add. An empty add changes nothing, and an
// empty box starts at add rather than at the zero rectangle: a line whose first
// word is at (100, 40) must not begin at (0, 0), which a plain image.Rectangle
// union would do.
func unionBounds(box, add image.Rectangle) image.Rectangle {
	if add.Empty() {
		return box
	}

	if box.Empty() {
		return add
	}

	return box.Union(add)
}

// finite reports whether f is a number an int can be derived from.
func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// downsampleFactor is the smallest whole factor that brings img under
// maxDimension on its longest side, or 1 when it already fits.
//
// A factor rather than a scale keeps the answer exact: every output pixel
// averages a full block of input pixels, and every box the engine reports
// multiplies back by the same integer, so a word lands where it was drawn
// instead of a resampling error away from it.
func downsampleFactor(img image.Rectangle, maxDimension int) int {
	if maxDimension <= 0 {
		return 1
	}

	longest := max(img.Dx(), img.Dy())
	if longest <= maxDimension {
		return 1
	}

	return (longest + maxDimension - 1) / maxDimension
}

// downsample box-averages img by a whole factor. The last partial row and column
// are dropped rather than padded, so every output pixel averages a full block
// and the output is exactly Dx/factor by Dy/factor.
//
// The output is anchored at (0,0); wordBounds is what puts its boxes back into
// img's own space.
func downsample(img *image.RGBA, factor int) *image.RGBA {
	bounds := img.Bounds()
	width, height := bounds.Dx()/factor, bounds.Dy()/factor
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	block := factor * factor

	for row := range height {
		for column := range width {
			var sum [bytesPerPixel]int

			for dy := range factor {
				offset := img.PixOffset(bounds.Min.X+column*factor, bounds.Min.Y+row*factor+dy)

				for dx := range factor {
					px := img.Pix[offset+dx*bytesPerPixel : offset+dx*bytesPerPixel+bytesPerPixel]

					for channel := range bytesPerPixel {
						sum[channel] += int(px[channel])
					}
				}
			}

			dst := out.PixOffset(column, row)

			for channel := range bytesPerPixel {
				out.Pix[dst+channel] = byte(sum[channel] / block)
			}
		}
	}

	return out
}
