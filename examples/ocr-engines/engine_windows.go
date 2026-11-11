//go:build windows

package main

import (
	"github.com/LouYuanbo1/neru-core/vision/ocr/winrt"
)

func newRecognizer() (*selected, error) {
	r, err := winrt.New(winrt.Options{Languages: []string{"en-US"}})
	if err != nil {
		return nil, err
	}

	return &selected{
		Recognizer: r,
		name:       "winrt (Windows.Media.Ocr)",
		close:      r.Close,
	}, nil
}
