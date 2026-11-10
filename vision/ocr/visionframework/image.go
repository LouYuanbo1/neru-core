//go:build darwin

package visionframework

import (
	"image"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// withCGImage builds a CGImage over img's pixels, runs fn with it, and
// releases the image and the CoreGraphics and CoreFoundation objects behind
// it when fn returns.
//
// The pixels are copied into a CFData, which the data provider retains and the
// image reads from, so the image owns its storage: nothing a caller does to
// img afterwards, and no garbage collection of img, can change what Vision
// sees. The copy also avoids the alternative provider, CGDataProviderCreateWithData,
// whose release callback would have to be native code.
func (api *frameworks) withCGImage(img *image.RGBA, fn func(cgImage uintptr) error) error {
	if _, err := imageBounds(img); err != nil {
		return err
	}

	width, height := img.Rect.Dx(), img.Rect.Dy()
	if img.Stride < width*4 || len(img.Pix) < (height-1)*img.Stride+width*4 {
		return errs.Newf(
			errs.CodeInvalidInput,
			"Vision needs a well-formed RGBA image; %v with stride %d has too little pixel data",
			img.Rect,
			img.Stride,
		)
	}

	pixels, stride := rgbaBytes(img)

	data := api.cf.dataCreate(0, unsafe.SliceData(pixels), int(len(pixels)))
	if data == 0 {
		return errs.New(errs.CodeInternal, "Vision could not hold a copy of the image pixels")
	}

	defer api.cf.release(data)

	provider := api.cg.dataProviderCreate(data)
	if provider == 0 {
		return errs.New(errs.CodeInternal, "Vision could not build a CGDataProvider for the image")
	}

	defer api.cg.dataProviderRelease(provider)

	space := api.cg.colorSpaceCreateDeviceRGB()
	if space == 0 {
		return errs.New(errs.CodeInternal, "Vision could not build an RGB color space")
	}

	defer api.cg.colorSpaceRelease(space)

	cgImage := api.cg.imageCreate(
		uintptr(width),
		uintptr(height),
		8,
		32,
		uintptr(stride),
		space,
		uintptr(cgImageAlphaLast),
		provider,
		0,
		false,
		cgRenderingIntentDefault,
	)
	if cgImage == 0 {
		return errs.New(errs.CodeInternal, "Vision could not make a CGImage from the image pixels")
	}

	defer api.cg.imageRelease(cgImage)

	return fn(cgImage)
}

// imageBounds returns the pixel bounds Vision should answer in, refusing an
// image it cannot read at all. It is the shared preamble of every entry point.
func imageBounds(img *image.RGBA) (image.Rectangle, error) {
	if img == nil {
		return image.Rectangle{}, errs.New(errs.CodeInvalidInput, "Vision needs an image to read")
	}

	if img.Rect.Empty() {
		return image.Rectangle{}, errs.Newf(
			errs.CodeInvalidInput,
			"Vision needs an image with an area; %v is empty",
			img.Rect,
		)
	}

	return img.Bounds(), nil
}

// rgbaBytes returns img's pixels as one buffer of height rows of width RGBA
// pixels, and the row stride in bytes.
//
// It hands back img.Pix itself when the rows are already tight and long
// enough, which is what a decoded screenshot looks like, and copies otherwise.
// A copy is needed because CGImageCreate reads stride*height bytes, while the
// Pix of a subimage that starts partway across its parent is shorter than
// that; reading past it would be a bug, not a slightly wrong image.
func rgbaBytes(img *image.RGBA) ([]byte, int) {
	width, height := img.Rect.Dx(), img.Rect.Dy()
	stride := width * 4

	if img.Stride == stride && len(img.Pix) >= stride*height {
		return img.Pix[:stride*height], stride
	}

	pixels := make([]byte, stride*height)
	for row := 0; row < height; row++ {
		start := row * img.Stride
		copy(pixels[row*stride:(row+1)*stride], img.Pix[start:start+stride])
	}

	return pixels, stride
}
