// Package ocr returns the text recognizer the operating system provides, or the
// system-wide installation that stands in for it: Windows.Media.Ocr on Windows,
// Apple's Vision framework on macOS, libtesseract (or a `tesseract` executable)
// on Linux.
//
// It is the OCR counterpart of the accessibility package. New picks the engine
// the running system has; the contract that engine implements lives in
// vision/ocr/engine; the engines themselves are vision/ocr/winrt,
// vision/ocr/tesseract and vision/ocr/visionframework. A program that needs a
// specific engine, or no engine at all, imports that engine's package directly
// instead: this package is a convenience, not a dependency of the vision
// pipeline.
//
// The recognizer it returns satisfies engine.Recognizer and, where the engine
// can answer it, engine.HealthChecker.
package ocr
