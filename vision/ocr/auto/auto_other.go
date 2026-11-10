//go:build !linux && !darwin && !windows

package auto

import (
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// New reports that this platform has no built-in recognizer. A caller can
// still pass its own vision/ocr.Recognizer to the pipeline.
func New() (ocr.Recognizer, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"this platform has no built-in OCR engine",
	)
}
