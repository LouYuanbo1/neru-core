//go:build darwin

package auto

import (
	"context"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
	"github.com/LouYuanbo1/neru-core/vision/ocr/tesseract"
	"github.com/LouYuanbo1/neru-core/vision/ocr/visionframework"
)

// New returns the best available recognizer on macOS.
//
// Apple's Vision framework is preferred: it is the system engine, needs no
// extra installation, and is the same one vision/ocr/visionframework also
// exposes for rectangle and saliency detection. When Vision cannot be bound
// (an ancient macOS), a local tesseract is used instead, and the error names
// both options when neither exists.
func New() (ocr.Recognizer, error) {
	vision, visionErr := visionframework.New(visionframework.Options{})
	if visionErr == nil {
		return vision, nil
	}

	library, libraryErr := tesseract.NewLibrary(tesseract.LibraryOptions{})
	if libraryErr == nil {
		return library, nil
	}

	cli := tesseract.NewCLI(tesseract.Options{})
	if healthErr := cli.Health(context.Background()); healthErr == nil {
		return cli, nil
	}

	return nil, errs.Wrapf(
		visionErr,
		errs.CodeNotSupported,
		"no OCR engine is available: the Vision framework could not be bound (%v) and no libtesseract or tesseract executable was found",
		libraryErr,
	)
}
