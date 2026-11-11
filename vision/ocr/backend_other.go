//go:build !linux && !darwin && !windows

package ocr

import (
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

// New reports that this platform has no built-in recognizer. A caller can
// still pass its own engine.Recognizer to the pipeline.
func New() (engine.Recognizer, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"this platform has no built-in OCR engine",
	)
}
