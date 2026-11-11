//go:build linux

package ocr

import (
	"context"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
	"github.com/LouYuanbo1/neru-core/vision/ocr/tesseract"
)

// New returns the best available recognizer on this system.
//
// The in-process libtesseract is preferred, because it matches the original
// application's engine and needs no subprocess. When the library is not
// installed but a `tesseract` executable is, the CLI recognizer is used
// instead; when neither exists the error names both remedies and is
// errs.CodeNotSupported, so a caller can degrade deliberately.
func New() (engine.Recognizer, error) {
	library, libraryErr := tesseract.NewLibrary(tesseract.LibraryOptions{})
	if libraryErr == nil {
		return library, nil
	}

	cli := tesseract.NewCLI(tesseract.Options{})
	if healthErr := cli.Health(context.Background()); healthErr == nil {
		return cli, nil
	}

	return nil, errs.Wrapf(
		libraryErr,
		errs.CodeNotSupported,
		"no OCR engine is available: install the libtesseract runtime or a tesseract executable",
	)
}
