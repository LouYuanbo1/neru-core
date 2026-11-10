// Package ocr defines the text-recognition contract the vision pipeline reads
// screen text through.
//
// A recognizer is a separate, optional import so a caller pays only for the
// engine it uses: vision/ocr/winrt for Windows.Media.Ocr, vision/ocr/tesseract
// for a local tesseract installation, vision/ocr/visionframework for Apple's
// Vision framework. The vision package depends on this interface, never on an
// engine.
//
// A recognizer answers in the captured image's own pixel space: Bounds are
// pixels of the image passed to Recognize, with (0,0) at its top-left corner,
// not desktop coordinates. vision.RegionsFromWords is what puts them back on
// the desktop.
//
// Text is screen content. A recognizer must not log it, count it, or write it
// anywhere; the only destination is the caller.
package ocr

import (
	"context"
	"image"
)

// Word is one run of text an engine reported.
type Word struct {
	// Text is the recognized string, without leading or trailing whitespace.
	Text string
	// Bounds is the run's rectangle in the recognized image's pixel space.
	Bounds image.Rectangle
	// Confidence is 0..1. An engine that reports percentages or log
	// likelihoods must normalize before filling this in.
	Confidence float64
}

// Options are the per-call knobs a recognizer receives.
type Options struct {
	// Languages is the preferred language order, as BCP 47 tags ("en-US") or
	// engine-specific names ("eng"). Empty means the engine's own default.
	Languages []string
	// WordLevel asks for one Word per recognized word instead of one per line
	// or paragraph. An engine that cannot split words may ignore it.
	WordLevel bool
	// MinConfidence is a lower bound the caller intends to apply. An engine
	// may drop runs below it early; it must never raise the floor itself.
	MinConfidence float64
}

// Recognizer recognizes text in a captured frame.
//
// Recognize must honor ctx: a deadline is a hard budget, not a hint. An engine
// that cannot run on this machine (missing language pack, missing tesseract,
// denied permission) reports errs.CodeNotSupported with a message naming what
// to install or grant, so a caller can degrade deliberately. Any other failure
// is errs.CodeAccessibilityFailed or errs.CodeTimeout.
type Recognizer interface {
	Recognize(ctx context.Context, img *image.RGBA, opts Options) ([]Word, error)
}

// HealthChecker is an optional interface a recognizer implements when it can
// answer "could I run right now" without doing the work.
//
// The vision pipeline probes it before capturing, so a machine with no OCR
// engine does not pay for a screenshot only to be told there is nothing to
// read it with. Health must not read screen content and must be cheap after
// the first call.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// Health returns the recognizer's availability. A recognizer that does not
// implement HealthChecker is assumed available; its Recognize reports the real
// failure.
func Health(ctx context.Context, recognizer Recognizer) error {
	if checker, ok := recognizer.(HealthChecker); ok {
		return checker.Health(ctx)
	}

	return nil
}
