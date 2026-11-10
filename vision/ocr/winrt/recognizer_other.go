//go:build !windows

package winrt

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// Recognizer is what every non-Windows build provides: a handle that refuses
// every operation with CodeNotSupported.
//
// The type exists on every target so that a cross-platform program can select
// its OCR backend in one place without a build tag of its own. New already
// refuses, so a caller never gets a Recognizer that silently reads nothing.
type Recognizer struct {
	options Options
}

var (
	_ ocr.Recognizer    = (*Recognizer)(nil)
	_ ocr.HealthChecker = (*Recognizer)(nil)
)

// New reports CodeNotSupported: Windows.Media.Ocr is a Windows component.
func New(options ...Options) (*Recognizer, error) {
	return nil, errNotWindows()
}

// Recognize reports CodeNotSupported on this platform.
func (r *Recognizer) Recognize(
	context.Context,
	*image.RGBA,
	ocr.Options,
) ([]ocr.Word, error) {
	return nil, errNotWindows()
}

// Health reports CodeNotSupported on this platform.
func (r *Recognizer) Health(context.Context) error {
	return errNotWindows()
}

// Close releases nothing, because nothing was ever acquired.
func (r *Recognizer) Close() error {
	return nil
}
