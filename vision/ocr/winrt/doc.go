// Package winrt recognizes text through Windows.Media.Ocr, the first-party
// WinRT engine every Windows 10 and 11 desktop ships.
//
// It is one implementation of the ocr.Recognizer contract, and the Windows half
// of that contract's backend set. A caller imports it instead of the interface
// only when it wants the engine Windows already has, without a tesseract
// installation beside it.
//
// # The engine
//
// The engine is created once per language selection and kept for as long as a
// Recognizer needs it. With no language named, it is created from the account's
// own language profile (OcrEngine::TryCreateFromUserProfileLanguages), which is
// what makes it read text in the language the user reads. With languages named,
// each tag is tried in order through OcrEngine::TryCreateFromLanguage and the
// first one the machine has a pack for wins.
//
// Windows installs OCR separately from the language itself, as the language's
// "Basic typing" feature. A machine with no pack for the requested languages is
// not broken, so it is reported as errs.CodeNotSupported with the install steps
// in the message, from Health and from Recognize alike, rather than as a
// recognition that silently finds nothing.
//
// # Threading
//
// The engine lives on one OS thread that the process keeps in the WinRT
// multithreaded apartment, because RoUninitialize on the last thread of an
// apartment tears down every object created in it. That thread serves one
// request at a time for the process's lifetime, which serializes the engine:
// Windows.Media.Ocr is not reentrant. Calls made from any goroutine and any
// number of Recognizers share it, so a process pays for one apartment, not one
// per call.
//
// # Coordinates
//
// Word.Bounds are in the pixel space of the image passed to Recognize, with its
// top-left corner at Min. WinRT reports boxes against the bitmap it read, so
// this package scales them back when the frame had to be shrunk to fit the
// engine's MaxImageDimension, and clips them to the image. They are never
// desktop coordinates: the caller places them, since only the caller knows where
// the frame came from.
//
// # Confidence
//
// Windows.Media.Ocr reports no per-word score. Every run therefore carries a
// Confidence of 1, which is what neru's Windows backend reported: the engine has
// already dropped what it could not read, and a caller's MinConfidence floor
// keeps everything it might have dropped. A caller must not read meaning into
// the value beyond that.
//
// # Time
//
// Recognize treats the context's deadline as a hard budget for one recognition
// and cancels the engine's operation when it expires, reporting
// errs.CodeTimeout. A context with no deadline is still bounded by
// DefaultTimeout, so a wedged engine cannot occupy the shared thread forever.
//
// # Privacy
//
// The frame and the text read from it are screen content. They reach the caller
// and nowhere else: nothing here logs, counts characters into a message, or
// writes them anywhere, and the pixel buffer handed to the engine is wiped as
// soon as the engine has its own copy.
//
// # Platforms
//
// This package builds everywhere, but only Windows can recognize. Every other
// target gets a Recognizer that refuses with errs.CodeNotSupported, so a
// cross-platform program can select the backend it needs at one place.
package winrt
