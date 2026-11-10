//go:build darwin

package visionframework

import (
	"fmt"
	"sync"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
)

// The frameworks this backend talks to. Vision is the one that matters; the
// others are its dependencies, opened explicitly because a pure-Go process has
// linked none of them and objc_getClass cannot find a class in a framework
// that was never loaded.
const (
	visionFrameworkPath   = "/System/Library/Frameworks/Vision.framework/Vision"
	foundationPath        = "/System/Library/Frameworks/Foundation.framework/Foundation"
	coreGraphicsPath      = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundationPath    = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	objectiveCRuntimePath = "/usr/lib/libobjc.A.dylib"
)

// CoreGraphics image description constants.
const (
	// cgImageAlphaLast is kCGImageAlphaLast: four 8-bit components in memory
	// order R, G, B and then alpha, with the alpha straight rather than
	// premultiplied. That is exactly image.RGBA's layout, so the pixels go to
	// Vision without a conversion. Core Graphics creates images with straight
	// alpha itself (PNG-backed ones), so an image like this is ordinary.
	cgImageAlphaLast uint32 = 3

	// cgRenderingIntentDefault is kCGRenderingIntentDefault.
	cgRenderingIntentDefault uint32 = 0
)

// Vision's text recognition levels, VNRequestTextRecognitionLevel.
const (
	textRecognitionLevelAccurate int = 0
	textRecognitionLevelFast     int = 1
)

// nsUTF8StringEncoding is NSUTF8StringEncoding, which is what
// lengthOfBytesUsingEncoding: takes. It is not CoreFoundation's UTF-8 encoding
// constant (0x08000100); the two enums are different.
const nsUTF8StringEncoding uintptr = 4

// cgPoint, cgSize and cgRect mirror CoreGraphics' CGFloat structs. CGFloat is
// a double on every 64-bit platform, which is the only place this backend
// builds.
type cgPoint struct {
	X float64
	Y float64
}

type cgSize struct {
	Width  float64
	Height float64
}

type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

// nsRange mirrors Foundation's NSRange, the range boundingBoxForRange: takes:
// a location and a length in UTF-16 code units.
type nsRange struct {
	Location uintptr
	Length   uintptr
}

// coreFoundation is the slice of CoreFoundation this backend uses: one way to
// put the pixels in a CoreFoundation object CoreGraphics can read, and the
// release every created object needs.
type coreFoundation struct {
	dataCreate func(allocator uintptr, bytes *byte, length int) uintptr
	release    func(value uintptr)
}

// coreGraphics is the CoreGraphics surface that turns a CFData into a
// CGImage. The bitmap layout is fixed by the caller's pixels, so nothing here
// draws or converts.
type coreGraphics struct {
	colorSpaceCreateDeviceRGB func() uintptr
	colorSpaceRelease         func(space uintptr)
	dataProviderCreate        func(data uintptr) uintptr
	dataProviderRelease       func(provider uintptr)
	imageCreate               func(
		width, height, bitsPerComponent, bitsPerPixel, bytesPerRow uintptr,
		space, bitmapInfo, provider, decode uintptr,
		shouldInterpolate bool,
		intent uint32,
	) uintptr
	imageRelease func(image uintptr)
}

// objcRuntime is the Objective-C messaging surface. objc_msgSend is bound once
// per signature: Objective-C methods are called by sending a selector to a
// receiver, and the ABI decides where each argument and return value goes, so
// a distinct Go function type is needed per shape.
type objcRuntime struct {
	getClass     func(name string) uintptr
	registerName func(name string) uintptr
	poolPush     func() uintptr
	poolPop      func(pool uintptr)

	msgSendID               func(receiver, selector uintptr) uintptr
	msgSendIDWithID         func(receiver, selector, argument uintptr) uintptr
	msgSendIDWithTwoIDs     func(receiver, selector, first, second uintptr) uintptr
	msgSendIDWithCStr       func(receiver, selector uintptr, argument string) uintptr
	msgSendIDWithUint       func(receiver, selector uintptr, argument uintptr) uintptr
	msgSendIDWithError      func(receiver, selector uintptr, errorRef *uintptr) uintptr
	msgSendIDWithRangeError func(receiver, selector uintptr, span nsRange, errorRef *uintptr) uintptr
	msgSendArrayWithObjects func(receiver, selector uintptr, objects []uintptr, count uintptr) uintptr
	msgSendVoid             func(receiver, selector uintptr)
	msgSendVoidWithID       func(receiver, selector, argument uintptr)
	msgSendVoidWithInt      func(receiver, selector uintptr, argument int)
	msgSendVoidWithUint     func(receiver, selector uintptr, argument uintptr)
	// msgSendVoidWithFloat sets a 32-bit float property. Vision declares its
	// rectangle knobs and its confidences as C float, not CGFloat, so a double
	// would put the wrong bits in the argument register.
	msgSendVoidWithFloat   func(receiver, selector uintptr, argument float32)
	msgSendVoidWithBool    func(receiver, selector uintptr, argument bool)
	msgSendPerformRequests func(receiver, selector, requests uintptr, errorRef *uintptr) bool
	msgSendInt             func(receiver, selector uintptr) int
	msgSendUint            func(receiver, selector uintptr) uintptr
	msgSendUintWithUint    func(receiver, selector, argument uintptr) uintptr
	// msgSendFloat reads a 32-bit float return, which is what Vision's
	// VNConfidence is. Reading it as a double would reinterpret the low half
	// of a float register as the exponent of a double.
	msgSendFloat func(receiver, selector uintptr) float32
	msgSendCStr  func(receiver, selector uintptr) *byte
	msgSendRect  func(receiver, selector uintptr) cgRect
}

// frameworks is the bound surface shared by every call in the process.
type frameworks struct {
	cf        coreFoundation
	cg        coreGraphics
	objc      objcRuntime
	classes   map[string]uintptr
	selectors map[string]uintptr
}

var (
	frameworksOnce sync.Once
	frameworksAPI  *frameworks
	frameworksErr  error
)

// load binds everything once and returns the same answer to every caller. A
// machine without the Vision framework gets errs.CodeNotSupported, which is
// what New and Health report and what a caller degrades on.
func load() (*frameworks, error) {
	frameworksOnce.Do(func() {
		frameworksAPI, frameworksErr = bindFrameworks()
	})

	if frameworksErr != nil {
		return nil, errs.Wrapf(
			frameworksErr,
			errs.CodeNotSupported,
			"macOS Vision framework is unavailable; text recognition, rectangle detection and saliency need macOS 10.15 or later",
		)
	}

	return frameworksAPI, nil
}

// class returns the Objective-C class registered under name. It is resolved at
// bind time, so a nil result means the framework does not define the class.
func (api *frameworks) class(name string) uintptr {
	return api.classes[name]
}

// selector returns the interned selector for name. Selectors are resolved at
// bind time and cached for the life of the process.
func (api *frameworks) selector(name string) uintptr {
	return api.selectors[name]
}

// classNames are every Objective-C class this backend messages. Resolving them
// at bind time is also the availability check Health reports: objc_getClass
// only finds a class once the framework defining it is loaded.
var classNames = []string{
	"NSString",
	"NSArray",
	"NSDictionary",
	"VNImageRequestHandler",
	"VNRecognizeTextRequest",
	"VNDetectRectanglesRequest",
	"VNGenerateAttentionBasedSaliencyImageRequest",
}

// selectorNames are every selector this backend sends. Binding them eagerly
// keeps message sends free of map writes, so several goroutines can recognize
// at once.
var selectorNames = []string{
	"stringWithUTF8String:",
	"arrayWithObjects:count:",
	"dictionary",
	"alloc",
	"init",
	"release",
	"lengthOfBytesUsingEncoding:",
	"UTF8String",
	"count",
	"objectAtIndex:",
	"firstObject",
	"initWithCGImage:options:",
	"performRequests:error:",
	"results",
	"localizedDescription",
	"code",
	"setRecognitionLevel:",
	"setUsesLanguageCorrection:",
	"setRecognitionLanguages:",
	"supportedRecognitionLanguagesAndReturnError:",
	"setMaximumObservations:",
	"setMinimumSize:",
	"setMinimumAspectRatio:",
	"setMaximumAspectRatio:",
	"setQuadratureTolerance:",
	"boundingBox",
	"confidence",
	"topCandidates:",
	"string",
	"boundingBoxForRange:error:",
	"salientObjects",
}

// libraries holds one handle per framework, so a failed bind can close the
// ones already opened.
type libraries struct {
	coreFoundation *native.Library
	coreGraphics   *native.Library
	foundation     *native.Library
	vision         *native.Library
	objectiveC     *native.Library
}

// close releases every handle that was opened.
func (l *libraries) close() {
	for _, library := range []*native.Library{
		l.coreFoundation,
		l.coreGraphics,
		l.foundation,
		l.vision,
		l.objectiveC,
	} {
		if library != nil {
			_ = library.Close()
		}
	}
}

// openLibraries opens the frameworks in the order they are needed. Vision is
// opened last so that its dependencies are loaded first, which is what makes
// its classes resolvable.
func openLibraries() (*libraries, error) {
	opened := &libraries{}

	for _, step := range []struct {
		label  string
		path   string
		target **native.Library
	}{
		{"CoreFoundation", coreFoundationPath, &opened.coreFoundation},
		{"CoreGraphics", coreGraphicsPath, &opened.coreGraphics},
		{"Foundation", foundationPath, &opened.foundation},
		{"Vision", visionFrameworkPath, &opened.vision},
		{"the Objective-C runtime", objectiveCRuntimePath, &opened.objectiveC},
	} {
		library, err := native.Open(step.path)
		if err != nil {
			opened.close()

			return nil, fmt.Errorf("visionframework: load %s: %w", step.label, err)
		}

		*step.target = library
	}

	return opened, nil
}

// bindFrameworks opens the frameworks, binds every symbol and resolves the
// class and selector names. It stops at the first failure: a backend missing
// one call cannot work, and the error names the exact symbol or class.
func bindFrameworks() (api *frameworks, err error) {
	books, err := openLibraries()
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			books.close()
		}
	}()

	api = &frameworks{
		classes:   make(map[string]uintptr, len(classNames)),
		selectors: make(map[string]uintptr, len(selectorNames)),
	}

	bind := func(library *native.Library, symbol string, target any) error {
		if bindErr := library.Func(symbol, target); bindErr != nil {
			return fmt.Errorf("%s: %w", symbol, bindErr)
		}

		return nil
	}

	for _, bindErr := range []error{
		bind(books.coreFoundation, "CFDataCreate", &api.cf.dataCreate),
		bind(books.coreFoundation, "CFRelease", &api.cf.release),
		bind(books.coreGraphics, "CGColorSpaceCreateDeviceRGB", &api.cg.colorSpaceCreateDeviceRGB),
		bind(books.coreGraphics, "CGColorSpaceRelease", &api.cg.colorSpaceRelease),
		bind(books.coreGraphics, "CGDataProviderCreateWithCFData", &api.cg.dataProviderCreate),
		bind(books.coreGraphics, "CGDataProviderRelease", &api.cg.dataProviderRelease),
		bind(books.coreGraphics, "CGImageCreate", &api.cg.imageCreate),
		bind(books.coreGraphics, "CGImageRelease", &api.cg.imageRelease),
		bind(books.objectiveC, "objc_getClass", &api.objc.getClass),
		bind(books.objectiveC, "sel_registerName", &api.objc.registerName),
		bind(books.objectiveC, "objc_autoreleasePoolPush", &api.objc.poolPush),
		bind(books.objectiveC, "objc_autoreleasePoolPop", &api.objc.poolPop),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendID),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithID),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithTwoIDs),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithCStr),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithUint),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithError),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendIDWithRangeError),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendArrayWithObjects),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoid),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoidWithID),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoidWithInt),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoidWithUint),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoidWithFloat),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendVoidWithBool),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendPerformRequests),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendInt),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendUint),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendUintWithUint),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendFloat),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendCStr),
		bind(books.objectiveC, "objc_msgSend", &api.objc.msgSendRect),
	} {
		if bindErr != nil {
			return nil, fmt.Errorf("visionframework: bind: %w", bindErr)
		}
	}

	for _, name := range classNames {
		class := api.objc.getClass(name)
		if class == 0 {
			return nil, fmt.Errorf(
				"visionframework: the Vision framework has no %s class; macOS 10.15 or later is required",
				name,
			)
		}

		api.classes[name] = class
	}

	for _, name := range selectorNames {
		api.selectors[name] = api.objc.registerName(name)
	}

	return api, nil
}
