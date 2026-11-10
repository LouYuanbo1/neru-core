//go:build darwin

// Package visionframework recognizes text in a captured image, detects
// rectangles and finds salient regions with Apple's Vision framework on macOS,
// without CGo.
//
// # Requirements
//
// macOS 10.15 (Catalina) or later, which is where every class this backend
// messages exists. Vision.framework itself ships with macOS; the classes it
// defines are only visible to objc_getClass once the framework is loaded, so
// the backend opens, per process,
//
//   - /System/Library/Frameworks/Vision.framework/Vision,
//   - /System/Library/Frameworks/Foundation.framework/Foundation,
//   - /System/Library/Frameworks/CoreGraphics.framework/CoreGraphics,
//   - /System/Library/Frameworks/CoreFoundation.framework/CoreFoundation,
//   - /usr/lib/libobjc.A.dylib,
//
// and binds the handful of C functions and Objective-C messages it needs with
// github.com/ebitengine/purego, through internal/native. Nothing is linked at
// build time and no C toolchain is needed; the package still compiles only on
// darwin, because every file is build-tagged. A machine without the frameworks
// (or with an older macOS) gets errs.CodeNotSupported from New and Health,
// with the version to upgrade to.
//
// # What it runs
//
// One synchronous Vision request per call, through a VNImageRequestHandler
// built for the caller's pixels:
//
//   - VNRecognizeTextRequest for Recognizer.Recognize.
//   - VNDetectRectanglesRequest for Rectangles and Recognizer.Boxes.
//   - VNGenerateAttentionBasedSaliencyImageRequest for Salient.
//
// Each request is handed to performRequests:error: and read back from its
// results property. No completion handler is passed, so no Objective-C block
// is involved and nothing calls back into Go.
//
// The handler is created with initWithCGImage:options: and an empty options
// dictionary: no orientation hint and no camera intrinsics are supplied, so
// Vision treats the image as upright (orientation 1). A caller holding a
// rotated frame must rotate it before handing it over.
//
// # Request fields honored
//
// VNRecognizeTextRequest:
//
//   - recognitionLevel is set from Options.Accurate: accurate when it is true,
//     fast otherwise. The fast model reads screen text well and needs no model
//     load; the accurate one reads small text better after its first call,
//     which can take seconds.
//   - usesLanguageCorrection is always off. Vision's correction rewrites
//     identifiers, paths, flags and URLs into dictionary words, which is
//     wrong for screen content.
//   - recognitionLanguages is set from Options.Languages, filtered against
//     +[VNRecognizeTextRequest supportedRecognitionLanguagesAndReturnError:].
//     Matching ignores case, and a base tag selects an advertised regional one,
//     so "en" asks Vision for "en-US" when that is what it lists. A requested
//     tag Vision does not list at all is dropped; if none survive, Recognize
//     fails with errs.CodeNotSupported naming what the engine does recognize.
//     An empty list leaves Vision's default order in place.
//   - minimumTextHeight, customWords, automaticallyDetectsLanguage and
//     revision are left at Vision's defaults.
//
// VNDetectRectanglesRequest: maximumObservations, minimumSize,
// minimumAspectRatio, maximumAspectRatio and quadratureTolerance are set from
// RectanglesOptions when the field is non-zero. minimumConfidence is left at
// Vision's default, so a caller filters results afterwards (vision's
// RegionsFromWords takes a confidence floor).
//
// VNGenerateAttentionBasedSaliencyImageRequest: no fields are set; the
// observation's salientObjects are read.
//
// # Word level and line level
//
// Lines are the default: one ocr.Word per recognized text observation, with
// the observation's boundingBox. With WordLevel, each observation's top
// candidate string is split into whitespace-delimited runs in Go, and each
// run's box is read with boundingBoxForRange:error: using UTF-16 ranges, so
// the box is Vision's own for that range rather than an estimate.
//
// The split deliberately differs from NSStringEnumerationByWords, which would
// need a block: punctuation stays attached to the word before it ("don't",
// "a-b" and "1.2" are one run each), and script boundaries do not split a run.
// This changes grouping, never box accuracy. A run Vision will not box is
// dropped, as is a run whose box rounds away to no area.
//
// Results are returned in reading order: lines top to bottom, and left to
// right inside a line. Two boxes share a line when their vertical centers are
// within half the shorter box's height, the tolerance the original daemon
// used, because Vision reports words of one line with different heights.
//
// # Coordinates and confidence
//
// Bounds are pixels of the image passed in, with (0,0) at its top-left
// corner, matching the ocr contract. Vision reports normalized rectangles with
// a bottom-left origin, which pixelRect converts, rounds to whole pixels and
// clips to the image. Only the image's size is used, so a subimage's Rect.Min
// never leaks into a result. A rectangle that clips away to nothing is
// dropped.
//
// Confidence is Vision's observation confidence, 0..1 already. Word-level
// words carry the confidence of the line they came from, because Vision
// reports one confidence per observation; a caller that needs finer scores
// should use line level.
//
// # Deadlines
//
// performRequests:error: is synchronous and Vision offers no cancellation, so
// a call checks ctx before it builds a handler and again once Vision returns.
// A canceled or expired context returns errs.CodeContextCanceled or
// errs.CodeTimeout and no results, and the call can overshoot a deadline by at
// most one request.
//
// # Memory
//
// Every call creates and releases its own CoreFoundation and Objective-C
// objects: a CFData copy of the pixels, a CGDataProvider, a CGColorSpace, a
// CGImage, a VNImageRequestHandler, the request and its result arrays, all
// inside an Objective-C autorelease pool the call pushes and pops itself
// (Go threads have none). Nothing native survives a call.
//
// # Privacy
//
// Recognized text is screen content. This package never logs it, counts it, or
// puts it in an error; the only destination is the caller. Error messages
// carry Vision's own NSError description, which describes the failure, not the
// image.
//
// This package captures nothing. Pair it with vision/capture, or with pixels
// the caller already has.
package visionframework
