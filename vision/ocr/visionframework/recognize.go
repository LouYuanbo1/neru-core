//go:build darwin

package visionframework

import (
	"context"
	"image"
	"slices"
	"strings"

	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// Options are a Recognizer's defaults. The ocr.Options passed to Recognize win
// where they ask for something: a non-empty language list replaces Languages,
// and a word-level request turns word level on. The recognition level lives
// here because the call has no say over it; the confidence floor lives on the
// call.
type Options struct {
	// Languages is the preferred recognition order, as BCP 47 tags such as
	// "en-US". Empty leaves Vision's own default order in place. A tag Vision
	// does not support is dropped, and if none of them are supported
	// Recognize fails with errs.CodeNotSupported.
	Languages []string

	// Accurate selects Vision's accurate recognition model instead of the
	// fast one. The accurate model reads small UI text more reliably, but its
	// first use in a process loads a model that can take seconds and holds up
	// fast requests meanwhile. The zero value selects the fast model, which is
	// what reading screen text wants.
	Accurate bool

	// WordLevel asks for one ocr.Word per word instead of one per line.
	// Recognize answers at word level if the per-call ocr.Options asks for it,
	// whatever this is set to, so this is a default and not a cap.
	WordLevel bool
}

// Recognizer recognizes text with the macOS Vision framework. It implements
// ocr.Recognizer and ocr.HealthChecker.
//
// A Recognizer holds no native state: it is a set of defaults over a framework
// bound once per process, so it is safe for concurrent use and costs nothing to
// keep for the life of an application.
type Recognizer struct {
	options Options
}

var (
	_ ocr.Recognizer    = (*Recognizer)(nil)
	_ ocr.HealthChecker = (*Recognizer)(nil)
)

// New returns a Recognizer with the given defaults.
//
// It returns errs.CodeNotSupported when this macOS has no Vision framework to
// call, which means macOS older than 10.15; the error names that remedy.
func New(options Options) (*Recognizer, error) {
	options.Languages = slices.Clone(options.Languages)

	if _, err := load(); err != nil {
		return nil, err
	}

	return &Recognizer{options: options}, nil
}

// Health reports whether Vision is loadable on this machine. It sends no
// request and reads no pixels, so it is cheap after the first call, and it is
// what New checks.
func (r *Recognizer) Health(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}

	_, err := load()

	return err
}

// Close releases the recognizer. Nothing native survives a Recognize call, and
// the frameworks are bound once for the process and are never unloaded (a
// dlclose could leave the classes Vision created dangling), so Close has
// nothing to free: it always returns nil, is safe to call more than once, and
// leaves the recognizer usable.
func (r *Recognizer) Close() error {
	return nil
}

// Recognize reads the text in img and returns it in reading order, as words in
// the image's own pixel space.
//
// img's alpha is treated as straight RGBA; a capture from vision/capture is
// opaque, which is the case Vision reads text from anyway. Results honour ctx
// as documented on the package: a request already running is not interrupted,
// but a finished or expired context returns errs.CodeTimeout or
// errs.CodeContextCanceled and no words. A recognizable image with no text
// returns no words and no error.
func (r *Recognizer) Recognize(ctx context.Context, img *image.RGBA, opts ocr.Options) ([]ocr.Word, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	api, err := load()
	if err != nil {
		return nil, err
	}

	bounds, err := imageBounds(img)
	if err != nil {
		return nil, err
	}

	level := textRecognitionLevelFast
	if r.options.Accurate {
		level = textRecognitionLevelAccurate
	}

	languages := r.options.Languages
	if len(opts.Languages) > 0 {
		languages = opts.Languages
	}

	wordLevel := r.options.WordLevel || opts.WordLevel
	minConfidence := opts.MinConfidence

	var words []ocr.Word

	err = api.withPool(func() error {
		accepted, langErr := api.normalizeLanguages(languages)
		if langErr != nil {
			return langErr
		}

		return api.withCGImage(img, func(cgImage uintptr) error {
			var readErr error

			words, readErr = api.recognizeText(
				ctx,
				cgImage,
				bounds,
				level,
				accepted,
				wordLevel,
				minConfidence,
			)

			return readErr
		})
	})
	if err != nil {
		return nil, err
	}

	sortReadingOrder(words)

	return words, nil
}

// Boxes implements the rectangle pass vision.BoxFinder asks a backend for, so
// a Recognizer can be installed as a pipeline's native box finder. It uses the
// defaults the surrounding pipeline was tuned on; a caller that wants other
// settings calls Rectangles directly.
//
// Rectangles are in the image's pixel space, clipped to it, in Vision's order
// (most confident first).
func (r *Recognizer) Boxes(ctx context.Context, img *image.RGBA) ([]image.Rectangle, error) {
	return Rectangles(ctx, img, boxesOptions())
}

// boxesOptions is what Boxes asks Vision for. The values match
// vision.DefaultRectangleMinSize, DefaultRectangleMinAspect,
// DefaultRectangleMaxAspect and DefaultMaxRectangles: many candidates, small
// rectangles, and aspect ratios far from square, because a screen holds wide
// toolbars and tall buttons where a document scanner expects text blocks.
func boxesOptions() RectanglesOptions {
	return RectanglesOptions{
		MinimumSize:         0.01,
		MinimumAspectRatio:  0.3,
		MaximumAspectRatio:  10,
		MaximumObservations: 100,
	}
}

// recognizeText runs one VNRecognizeTextRequest and copies its observations
// into words. It must be called inside a pool, with a request-free cgImage.
func (api *frameworks) recognizeText(
	ctx context.Context,
	cgImage uintptr,
	bounds image.Rectangle,
	level int,
	languages []string,
	wordLevel bool,
	minConfidence float64,
) ([]ocr.Word, error) {
	request, err := api.newRequest("VNRecognizeTextRequest")
	if err != nil {
		return nil, err
	}

	defer api.objc.msgSendVoid(request, api.selector("release"))

	api.objc.msgSendVoidWithInt(request, api.selector("setRecognitionLevel:"), level)
	// Language correction rewrites identifiers, paths, flags and URLs into
	// dictionary words, which is the wrong answer for screen content.
	api.objc.msgSendVoidWithBool(request, api.selector("setUsesLanguageCorrection:"), false)

	if len(languages) > 0 {
		if array := api.stringArray(languages); array != 0 {
			api.objc.msgSendVoidWithID(request, api.selector("setRecognitionLanguages:"), array)
		}
	}

	results, err := api.perform(ctx, cgImage, request, "VNRecognizeTextRequest")
	if err != nil {
		return nil, err
	}

	observations := api.arrayItems(results)
	words := make([]ocr.Word, 0, len(observations))

	for _, observation := range observations {
		// VNConfidence is a 32-bit float, unlike Vision's rectangle options,
		// which are CGFloat.
		confidence := float64(api.objc.msgSendFloat(observation, api.selector("confidence")))
		if confidence < minConfidence {
			continue
		}

		candidate := api.objc.msgSendID(
			api.objc.msgSendIDWithUint(observation, api.selector("topCandidates:"), 1),
			api.selector("firstObject"),
		)
		if candidate == 0 {
			continue
		}

		text := api.goString(api.objc.msgSendID(candidate, api.selector("string")))
		if strings.TrimSpace(text) == "" {
			continue
		}

		if !wordLevel {
			box := api.objc.msgSendRect(observation, api.selector("boundingBox"))
			if rect := pixelRect(normalizedRectOf(box), bounds); !rect.Empty() {
				words = append(words, ocr.Word{
					Text:       strings.TrimSpace(text),
					Bounds:     rect,
					Confidence: confidence,
				})
			}

			continue
		}

		for _, span := range wordSpans(text) {
			box, ok := api.wordRect(candidate, span)
			if !ok {
				continue
			}

			if rect := pixelRect(normalizedRectOf(box), bounds); !rect.Empty() {
				words = append(words, ocr.Word{
					Text:       span.Text,
					Bounds:     rect,
					Confidence: confidence,
				})
			}
		}
	}

	return words, nil
}

// wordRect reads the box Vision has for one word's UTF-16 range. It reports
// false when Vision refuses the range or reports no box for it, which is how a
// range that spans nothing visible comes back.
func (api *frameworks) wordRect(candidate uintptr, span wordSpan) (cgRect, bool) {
	var errorRef uintptr

	observation := api.objc.msgSendIDWithRangeError(
		candidate,
		api.selector("boundingBoxForRange:error:"),
		nsRange{Location: span.Location, Length: span.Length},
		&errorRef,
	)
	if observation == 0 || errorRef != 0 {
		return cgRect{}, false
	}

	return api.objc.msgSendRect(observation, api.selector("boundingBox")), true
}
