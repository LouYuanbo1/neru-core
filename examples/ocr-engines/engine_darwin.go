//go:build darwin

package main

import (
	"context"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/tesseract"
	"github.com/LouYuanbo1/neru-core/vision/ocr/visionframework"
)

func newRecognizer() (*selected, error) {
	vision, visionErr := visionframework.New(visionframework.Options{Accurate: true})
	if visionErr == nil {
		return &selected{
			Recognizer: vision,
			name:       "visionframework (Apple Vision, accurate)",
			close:      vision.Close,
		}, nil
	}

	library, libraryErr := tesseract.NewLibrary(tesseract.LibraryOptions{})
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
		visionErr,
		errs.CodeNotSupported,
		"no OCR engine: Vision=%v, libtesseract=%v",
		visionErr,
		libraryErr,
	)
}
