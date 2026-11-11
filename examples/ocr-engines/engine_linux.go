//go:build linux

package main

import (
	"context"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/tesseract"
)

func newRecognizer() (*selected, error) {
	library, libraryErr := tesseract.NewLibrary(tesseract.LibraryOptions{Language: "eng"})
	if libraryErr == nil {
		return &selected{
			Recognizer: library,
			name:       "tesseract (libtesseract)",
			close:      library.Close,
		}, nil
	}

	cli := tesseract.NewCLI(tesseract.Options{})
	if healthErr := cli.Health(context.Background()); healthErr == nil {
		return &selected{Recognizer: cli, name: "tesseract (CLI)"}, nil
	}

	return nil, errs.Wrapf(
		libraryErr,
		errs.CodeNotSupported,
		"no OCR engine: install the libtesseract runtime or a tesseract executable",
	)
}
