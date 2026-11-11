//go:build windows

package ocr

import (
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
	"github.com/LouYuanbo1/neru-core/vision/ocr/winrt"
)

// New returns a Windows.Media.Ocr recognizer, the engine every Windows 10 and
// 11 desktop provides once an OCR language pack is installed.
func New() (engine.Recognizer, error) {
	return winrt.New()
}
