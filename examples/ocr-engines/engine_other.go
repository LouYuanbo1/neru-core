//go:build !windows && !darwin && !linux

package main

import "github.com/LouYuanbo1/neru-core/errs"

func newRecognizer() (*selected, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"this example has no OCR engine on this platform",
	)
}
