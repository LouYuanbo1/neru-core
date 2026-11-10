//go:build linux

package portal

import (
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"

	"github.com/LouYuanbo1/neru-core/errs"
)

// maxScreenshotSide bounds the dimensions this package will decode. A desktop
// is far narrower than this even at 8K; the bound exists so a corrupt or
// hostile PNG header cannot make the decoder allocate an unbounded image from a
// file this process did not write.
const maxScreenshotSide = 1 << 16

// decodeFile reads the PNG the portal wrote and returns its pixels in RGBA
// anchored at (0,0).
//
// The file is read only, and it is left in place afterwards: the portal wrote
// it, and on a document-portal mount the path it hands out is not this
// process's to remove. Nothing about the path or the pixels is logged.
func decodeFile(path string) (*image.RGBA, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errs.Wrapf(err, errs.CodeInternal, "could not open the screenshot the portal wrote at %q", path)
	}
	defer file.Close()

	header, err := png.DecodeConfig(file)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "the file the screenshot portal wrote is not a PNG")
	}

	if header.Width <= 0 || header.Height <= 0 ||
		header.Width > maxScreenshotSide || header.Height > maxScreenshotSide {
		return nil, errs.Newf(
			errs.CodeInternal,
			"the screenshot the portal wrote is %dx%d pixels, which is not a desktop's size",
			header.Width,
			header.Height,
		)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not rewind the screenshot the portal wrote")
	}

	decoded, err := png.Decode(file)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not decode the screenshot the portal wrote")
	}

	return toRGBA(decoded), nil
}

// toRGBA converts a decoded image into RGBA anchored at (0,0). A picture the
// standard library already decoded into RGBA at the origin is passed through
// rather than copied, since a 4K frame is 33 MB.
//
// The conversion matters because the PNG decoder returns whichever Go image
// type matches the file's color type: NRGBA for truecolor with alpha, RGBA for
// truecolor, Paletted for an indexed image. Only the first of those is a layout
// a detector can read directly.
func toRGBA(decoded image.Image) *image.RGBA {
	if rgba, ok := decoded.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba
	}

	bounds := decoded.Bounds()
	img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(img, img.Rect, decoded, bounds.Min, draw.Src)

	return img
}

// cropImage copies pixels out of src into a new image anchored at (0,0), so a
// cropped frame does not keep the whole screenshot alive and its rectangle
// starts where a caller expects it to.
func cropImage(src *image.RGBA, pixels image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, pixels.Dx(), pixels.Dy()))
	draw.Draw(dst, dst.Rect, src, pixels.Min, draw.Src)

	return dst
}
