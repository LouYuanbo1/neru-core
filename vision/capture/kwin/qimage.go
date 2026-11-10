package kwin

import (
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The QImage::Format values this package converts, numbered as Qt numbers them.
// The byte order of each is documented in Qt's QImage documentation; the
// values below are the ones KWin's screenshot effect has produced and the ones
// an older KWin produced before it moved to RGBX8888.
//
// Format_ARGB32_Premultiplied is 6, not 4: Qt's enum starts with
// Format_Invalid, Format_Mono, Format_MonoLSB, Format_Indexed8, Format_RGB32
// and Format_ARGB32 before it. Handling the wrong numbers would read the
// channels of a real capture in the wrong order, so the names are spelled out.
const (
	formatRGB32                 = 4
	formatARGB32                = 5
	formatARGB32Premultiplied   = 6
	formatRGB888                = 13
	formatRGBX8888              = 16
	formatRGBA8888              = 17
	formatRGBA8888Premultiplied = 18
	formatBGR888                = 29
)

// supportedFormats names the QImage formats this package converts, for the
// refusal an unsupported one gets.
const supportedFormats = "4 (RGB32), 5 (ARGB32), 6 (ARGB32_Premultiplied), " +
	"13 (RGB888), 16 (RGBX8888), 17 (RGBA8888), 18 (RGBA8888_Premultiplied) " +
	"and 29 (BGR888)"

// pixelLayout describes where a format keeps each channel in a pixel.
type pixelLayout struct {
	// bpp is the pixel's size in bytes.
	bpp int
	// red, green, blue and alpha are the byte offsets of each channel inside
	// the pixel. alpha is -1 for a format that carries no alpha, which is
	// opaque by definition.
	red, green, blue, alpha int
	// premultiplied reports whether the colour channels were multiplied by the
	// alpha channel, which they have to be divided by again to get the straight
	// RGBA a detector expects.
	premultiplied bool
}

// pixelLayoutFor resolves a QImage format into its channel layout.
func pixelLayoutFor(format int) (pixelLayout, error) {
	switch format {
	case formatRGB32:
		// 0xffRRGGBB stored as a 32-bit word: little-endian memory is B,G,R,X.
		return pixelLayout{bpp: 4, red: 2, green: 1, blue: 0, alpha: -1}, nil
	case formatARGB32:
		return pixelLayout{bpp: 4, red: 2, green: 1, blue: 0, alpha: 3}, nil
	case formatARGB32Premultiplied:
		return pixelLayout{bpp: 4, red: 2, green: 1, blue: 0, alpha: 3, premultiplied: true}, nil
	case formatRGBX8888:
		// Byte-ordered RGBA with an unused fourth byte.
		return pixelLayout{bpp: 4, red: 0, green: 1, blue: 2, alpha: -1}, nil
	case formatRGBA8888:
		return pixelLayout{bpp: 4, red: 0, green: 1, blue: 2, alpha: 3}, nil
	case formatRGBA8888Premultiplied:
		return pixelLayout{bpp: 4, red: 0, green: 1, blue: 2, alpha: 3, premultiplied: true}, nil
	case formatRGB888:
		return pixelLayout{bpp: 3, red: 0, green: 1, blue: 2, alpha: -1}, nil
	case formatBGR888:
		return pixelLayout{bpp: 3, red: 2, green: 1, blue: 0, alpha: -1}, nil
	default:
		return pixelLayout{}, errs.Newf(
			errs.CodeNotSupported,
			"KWin wrote a QImage of format %d, which this package cannot convert to RGBA; "+
				"the formats it reads are %s",
			format,
			supportedFormats,
		)
	}
}

// rgba converts the raw pixels KWin wrote into an RGBA image anchored at (0,0).
// The rows are read at the stride the reply reported, so a padded row does not
// shear the image, and every channel is placed where image.RGBA keeps it.
func (m metadata) rgba(data []byte) (*image.RGBA, error) {
	layout, err := pixelLayoutFor(m.format)
	if err != nil {
		return nil, err
	}

	needed := m.stride * m.height
	if len(data) < needed {
		return nil, errs.Newf(
			errs.CodeInternal,
			"KWin described %dx%d pixels at %d bytes per row but wrote only %d of the %d bytes that needs",
			m.width,
			m.height,
			m.stride,
			len(data),
			needed,
		)
	}

	img := image.NewRGBA(image.Rect(0, 0, m.width, m.height))

	for y := range m.height {
		row := data[y*m.stride:]

		destination := img.Pix[y*img.Stride:]

		for x := range m.width {
			source := row[x*layout.bpp:]
			pixel := destination[x*4:]

			alpha := byte(0xff)
			if layout.alpha >= 0 {
				alpha = source[layout.alpha]
			}

			red := source[layout.red]
			green := source[layout.green]
			blue := source[layout.blue]

			if layout.premultiplied {
				red = unpremultiply(red, alpha)
				green = unpremultiply(green, alpha)
				blue = unpremultiply(blue, alpha)
			}

			pixel[0] = red
			pixel[1] = green
			pixel[2] = blue
			pixel[3] = alpha
		}
	}

	return img, nil
}

// unpremultiply divides a premultiplied colour channel by its alpha channel,
// which is what turns QImage's premultiplied ARGB into the straight RGBA the
// library works in. A screen capture is opaque, so this is usually the
// identity; it matters for a window capture with a translucent edge.
func unpremultiply(colour, alpha byte) byte {
	if alpha == 0 {
		return 0
	}

	if alpha == 0xff {
		return colour
	}

	straight := (int(colour)*0xff + int(alpha)/2) / int(alpha)
	if straight > 0xff {
		return 0xff
	}

	return byte(straight)
}
