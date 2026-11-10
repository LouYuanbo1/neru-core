// Package auto returns the text recognizer the running platform ships with.
//
// It exists so an application can ask for "the built-in OCR" without a build
// tag of its own: Windows.Media.Ocr on Windows, Apple's Vision framework on
// macOS, libtesseract (or a local tesseract executable) on Linux. A program
// that needs a specific engine, or no engine at all, imports that engine's
// package directly instead; this package is a convenience, not a dependency of
// the vision pipeline.
//
// The returned recognizer satisfies vision/ocr.Recognizer and, where the engine
// can answer it, ocr.HealthChecker.
package auto

import (
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// Recognizer is the platform engine's type, widened to the contract.
type Recognizer = ocr.Recognizer
