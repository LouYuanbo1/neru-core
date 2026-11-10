//go:build windows

package auto

import (
	"github.com/LouYuanbo1/neru-core/vision/ocr/winrt"
)

// New returns a Windows.Media.Ocr recognizer, the engine every Windows 10 and
// 11 desktop ships once an OCR language pack is installed.
func New() (Recognizer, error) {
	return winrt.New()
}
