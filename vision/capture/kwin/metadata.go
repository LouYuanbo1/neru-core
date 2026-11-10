package kwin

import (
	"math"

	"github.com/LouYuanbo1/neru-core/errs"
)

// maxScreenshotSide bounds each dimension this package will decode. A desktop
// is far narrower than this even at 8K; the bound exists so a corrupt or
// hostile reply cannot make the decoder allocate an unbounded image.
const maxScreenshotSide = 1 << 16

// maxImageBytes bounds the raw image this package will read off the pipe. A
// 16K desktop in 32-bit pixels is 132 MB; this leaves room for a large
// multi-output workspace while staying far below a size that could exhaust
// memory.
const maxImageBytes = 1 << 30

// rawImageType is the only image type ScreenShot2 writes. The interface
// reserves the field so it could carry other encodings later.
const rawImageType = "raw"

// The keys of KWin's result vardict, from the ScreenShot2 interface's
// documentation. The reply also carries an informational "screen" name for a
// screen capture and a "windowId" for a window capture; neither says where the
// picture sits on the desktop, so neither is read.
const (
	resultType   = "type"
	resultWidth  = "width"
	resultHeight = "height"
	resultStride = "stride"
	resultFormat = "format"
	resultScale  = "scale"
)

// metadata is what KWin's reply says about the image in the pipe. Everything
// here comes from the a{sv} result dictionary, whose values are the unwrapped
// D-Bus variants; metadataFrom keeps the parsing away from the bus so it can be
// tested on any platform.
type metadata struct {
	// imageType is the "type" entry, "raw" for the one encoding this package
	// reads.
	imageType string
	// width, height and stride describe the raw image: its size in pixels and
	// its row length in bytes, which includes any row padding.
	width, height, stride int
	// format is the QImage::Format of the pixels, an integer from Qt's enum.
	format int
	// scale is QImage::devicePixelRatio(): frame pixels per logical pixel. It
	// is absent from replies older than ScreenShot2 version 4, hence hasScale.
	scale    float64
	hasScale bool
}

// metadataFrom reads KWin's screenshot reply. It refuses anything this package
// cannot turn into pixels rather than reading a buffer whose layout it is
// guessing at.
func metadataFrom(values map[string]any) (metadata, error) {
	if values == nil {
		return metadata{}, errs.New(
			errs.CodeInternal,
			"KWin answered a screenshot request without describing the image it wrote",
		)
	}

	kindValue, present := values[resultType]
	if !present {
		return metadata{}, errs.New(
			errs.CodeInternal,
			"KWin's screenshot reply does not say what kind of image it wrote",
		)
	}

	kind, isString := kindValue.(string)
	if !isString || kind == "" {
		return metadata{}, errs.Newf(
			errs.CodeInternal,
			"KWin's screenshot reply carries a %T where the image type belongs",
			kindValue,
		)
	}

	if kind != rawImageType {
		return metadata{}, errs.Newf(
			errs.CodeNotSupported,
			"KWin wrote a %q screenshot, and this package only reads the %q encoding",
			kind,
			rawImageType,
		)
	}

	meta := metadata{imageType: kind}

	var err error

	if meta.width, err = requiredInt(values, resultWidth); err != nil {
		return metadata{}, err
	}

	if meta.height, err = requiredInt(values, resultHeight); err != nil {
		return metadata{}, err
	}

	if meta.stride, err = requiredInt(values, resultStride); err != nil {
		return metadata{}, err
	}

	if meta.format, err = requiredInt(values, resultFormat); err != nil {
		return metadata{}, err
	}

	if meta.width <= 0 || meta.height <= 0 {
		return metadata{}, errs.Newf(
			errs.CodeInternal,
			"KWin described a %dx%d screenshot, which has no pixels",
			meta.width,
			meta.height,
		)
	}

	if meta.width > maxScreenshotSide || meta.height > maxScreenshotSide {
		return metadata{}, errs.Newf(
			errs.CodeInternal,
			"KWin described a %dx%d screenshot, which is not a desktop's size",
			meta.width,
			meta.height,
		)
	}

	meta.scale, meta.hasScale = floatValue(values[resultScale])
	if meta.hasScale && (meta.scale <= 0 || math.IsNaN(meta.scale) || math.IsInf(meta.scale, 0)) {
		// A scale that cannot describe a ratio is worse than no scale: the
		// frame falls back to deriving one from the pixels.
		meta.hasScale = false
	}

	return meta, nil
}

// scaleOr returns the metadata scale, clamped to the at-least-1 contract, or
// fallback when the reply carried no usable one.
func (m metadata) scaleOr(fallback float64) float64 {
	if !m.hasScale {
		return fallback
	}

	if m.scale < 1 {
		return 1
	}

	return m.scale
}

// byteLen is how many bytes KWin writes for this image. QImage's sizeInBytes is
// the row stride times the height, so the last row is padded like every other
// one.
func (m metadata) byteLen() (int, error) {
	pixels, err := pixelLayoutFor(m.format)
	if err != nil {
		return 0, err
	}

	if m.stride <= 0 {
		return 0, errs.Newf(
			errs.CodeInternal,
			"KWin described a row stride of %d bytes, which is not a row",
			m.stride,
		)
	}

	minimum := m.width * pixels.bpp
	if m.stride < minimum {
		return 0, errs.Newf(
			errs.CodeInternal,
			"KWin described a row stride of %d bytes, too short for %d pixels of %d bytes",
			m.stride,
			m.width,
			pixels.bpp,
		)
	}

	// The multiplication is done in 64 bits so a hostile height cannot wrap the
	// total into a small positive number on a 32-bit platform.
	total := uint64(m.stride) * uint64(m.height)
	if total == 0 || total > maxImageBytes {
		return 0, errs.Newf(
			errs.CodeInternal,
			"KWin's screenshot would need %d bytes, which is more than this package reads",
			total,
		)
	}

	return int(total), nil
}

// requiredInt reads one integer entry out of the reply.
func requiredInt(values map[string]any, key string) (int, error) {
	value, present := values[key]
	if !present {
		return 0, errs.Newf(
			errs.CodeInternal,
			"KWin's screenshot reply does not carry a %q entry",
			key,
		)
	}

	number, ok := intValue(value)
	if !ok {
		return 0, errs.Newf(
			errs.CodeInternal,
			"KWin's screenshot reply carries a %T where a %q number belongs",
			value,
			key,
		)
	}

	return number, nil
}

// intValue reads an integer from an unwrapped D-Bus variant. KWin sends these
// entries as quint32; the wider and signed spellings are accepted so a future
// Qt that maps them to a different D-Bus integer type keeps working.
func intValue(value any) (int, bool) {
	switch number := value.(type) {
	case uint32:
		return int(number), true
	case int32:
		return int(number), true
	case int:
		return number, true
	case int64:
		if number < math.MinInt32 || number > math.MaxInt32 {
			return 0, false
		}

		return int(number), true
	case uint64:
		if number > math.MaxInt32 {
			return 0, false
		}

		return int(number), true
	default:
		return 0, false
	}
}

// floatValue reads a real number from an unwrapped D-Bus variant. KWin sends
// the scale as a double.
func floatValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case uint32:
		return float64(number), true
	case int32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}
