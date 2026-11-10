//go:build linux

package wayland

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// maxFrameDimension is the largest frame edge accepted from a compositor. It is
// far above any real display and small enough that width * height * 4 cannot
// overflow an int on a 32-bit build.
const maxFrameDimension = 32768

// shmBufferInfo is what zwlr_screencopy_frame_v1.buffer described: the format,
// pixel size and stride the compositor wants the client to allocate.
type shmBufferInfo struct {
	format    uint32
	width     int
	height    int
	stride    int
	hasBuffer bool
	yInvert   bool
}

// validate rejects a frame description this package cannot safely allocate.
func (info *shmBufferInfo) validate() error {
	if info.width <= 0 || info.height <= 0 {
		return errs.Newf(
			errs.CodeInternal,
			"the compositor described a %dx%d frame",
			info.width,
			info.height,
		)
	}

	if info.width > maxFrameDimension || info.height > maxFrameDimension {
		return errs.Newf(
			errs.CodeNotSupported,
			"the compositor described a %dx%d frame, larger than the %d-pixel limit",
			info.width,
			info.height,
			maxFrameDimension,
		)
	}

	if info.stride < info.width*4 {
		return errs.Newf(
			errs.CodeNotSupported,
			"the compositor described a %d-byte stride for a %d-pixel-wide frame",
			info.stride,
			info.width,
		)
	}

	if !supportedShmFormat(info.format) {
		return errs.Newf(
			errs.CodeNotSupported,
			"the compositor offered wl_shm format 0x%08x; this pure-Go capture reads only XRGB8888 and ARGB8888",
			info.format,
		)
	}

	// A wl_shm pool size is a signed 32-bit int on the wire.
	if int64(info.stride)*int64(info.height) > math.MaxInt32 {
		return errs.Newf(
			errs.CodeNotSupported,
			"the compositor described a %d-byte frame, larger than a wl_shm pool can hold",
			int64(info.stride)*int64(info.height),
		)
	}

	return nil
}

// supportedShmFormat reports whether convertShmBuffer can read format.
func supportedShmFormat(format uint32) bool {
	return format == shmFormatXRGB8888 || format == shmFormatARGB8888
}

// convertShmBuffer turns a wl_shm frame into an RGBA image. src holds height
// rows of stride bytes; width of those pixels are meaningful. On the
// little-endian machines this package targets, both supported formats store
// each pixel as B, G, R, A, where XRGB's fourth byte is padding rather than
// alpha. ARGB8888 arrives premultiplied, which is exactly what image.RGBA
// stores, so its alpha byte is copied through; XRGB8888 is forced opaque.
//
// yInvert flips the rows, because the y-invert frame flag means the compositor
// stored the image bottom-up.
func convertShmBuffer(
	src []byte,
	width, height, stride int,
	format uint32,
	yInvert bool,
) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, errs.Newf(
			errs.CodeInternal,
			"the compositor described an empty %dx%d frame",
			width,
			height,
		)
	}

	if !supportedShmFormat(format) {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"the compositor offered wl_shm format 0x%08x; this pure-Go capture reads only XRGB8888 and ARGB8888",
			format,
		)
	}

	if stride < width*4 {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"the compositor described a %d-byte stride for a %d-pixel-wide frame",
			stride,
			width,
		)
	}

	if int64(stride)*int64(height) > int64(len(src)) {
		return nil, errs.New(
			errs.CodeInternal,
			"the shared-memory buffer is smaller than the frame the compositor described",
		)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	opaque := format == shmFormatXRGB8888

	for y := 0; y < height; y++ {
		sourceRow := y
		if yInvert {
			sourceRow = height - 1 - y
		}

		sourceLine := src[sourceRow*stride:]
		destLine := img.Pix[y*img.Stride:]

		for x := 0; x < width; x++ {
			srcPixel := x * 4
			dstPixel := x * 4

			// Little-endian 0xAARRGGBB is laid out B, G, R, A in memory.
			destLine[dstPixel+0] = sourceLine[srcPixel+2]
			destLine[dstPixel+1] = sourceLine[srcPixel+1]
			destLine[dstPixel+2] = sourceLine[srcPixel+0]

			if opaque {
				destLine[dstPixel+3] = 0xff
			} else {
				destLine[dstPixel+3] = sourceLine[srcPixel+3]
			}
		}
	}

	return img, nil
}
