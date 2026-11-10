//go:build darwin

package darwin

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/internal/native"
)

// This file binds the C functions the backend needs from CoreFoundation,
// ApplicationServices (Accessibility), CoreGraphics and the Objective-C
// runtime. Every call goes through purego, so the package compiles and runs
// without CGo while still talking to the same frameworks AppKit uses.
//
// Apple's frameworks are always installed, so a load failure means the process
// cannot do accessibility work at all; New reports it once instead of panicking
// on first use.

// Framework paths. A missing ApplicationServices symbol on an old macOS is
// reported by the individual call, which is enough for the one API that is
// version-gated.
const (
	coreFoundationPath    = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	applicationServices   = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	coreGraphicsPath      = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	objectiveCRuntimePath = "/usr/lib/libobjc.A.dylib"
)

// CoreFoundation and Accessibility constants. The encodings and BOOL layouts
// are fixed by the platform ABI; the AX error codes are the negative values in
// AXError.h.
const (
	cfStringEncodingUTF8 = 0x08000100

	// CFNumberType values, from CFNumber.h. SInt32 is what the window server
	// and WindowManager's undocumented window id both use; Float64 is how a
	// property-list number is read when its exact type does not matter.
	cfNumberSInt32Type  uint32 = 3
	cfNumberFloat64Type uint32 = 6

	kAXErrorSuccess         int32 = 0
	kAXErrorIllegalArgument int32 = -25201
	kAXErrorCannotComplete  int32 = -25204
)

// AXValue types, from AXValue.h.
const (
	axValueCGPointType uint32 = 1
	axValueCGSizeType  uint32 = 2
)

// CGEvent types and fields the input path posts.
const (
	cgEventLeftMouseDown     uint32 = 1
	cgEventLeftMouseUp       uint32 = 2
	cgEventRightMouseDown    uint32 = 3
	cgEventRightMouseUp      uint32 = 4
	cgEventMouseMoved        uint32 = 5
	cgEventLeftMouseDragged  uint32 = 6
	cgEventRightMouseDragged uint32 = 7
	cgEventOtherMouseDown    uint32 = 25
	cgEventOtherMouseUp      uint32 = 26
	cgEventOtherMouseDragged uint32 = 27

	cgEventTapSession uint32 = 1 // kCGSessionEventTap, above Accessibility Zoom's transform

	// cgEventSourceStateCombined is
	// kCGEventSourceStateCombinedSessionState: the sum of every source's
	// buttons and modifiers, which is what "is the user holding this button"
	// means.
	cgEventSourceStateCombined uint32 = 0

	cgMouseButtonLeft   uint32 = 0
	cgMouseButtonRight  uint32 = 1
	cgMouseButtonCenter uint32 = 2

	cgScrollUnitPixel uint32 = 0

	cgMouseEventClickState uint32 = 1

	cgErrorSuccess int32 = 0

	cgEventFlagShift     uint64 = 0x00020000
	cgEventFlagControl   uint64 = 0x00040000
	cgEventFlagAlternate uint64 = 0x00080000
	cgEventFlagCommand   uint64 = 0x00100000
)

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

// coreFoundation is the CoreFoundation surface the backend uses.
type coreFoundation struct {
	stringCreateWithCString func(allocator uintptr, value string, encoding uint32) uintptr
	stringGetCString        func(str uintptr, buffer *byte, size int64, encoding uint32) uint8
	stringGetLength         func(str uintptr) int64
	stringGetMaximumSize    func(length int64, encoding uint32) int64
	stringGetTypeID         func() uint64
	arrayGetCount           func(array uintptr) int64
	arrayGetTypeID          func() uint64
	arrayGetValueAtIndex    func(array uintptr, index int64) uintptr
	arrayCreate             func(allocator uintptr, values []uintptr, count int64, callbacks uintptr) uintptr
	arrayCreateMutable      func(allocator uintptr, capacity int64, callbacks uintptr) uintptr
	arrayAppendValue        func(array, value uintptr)
	getTypeID               func(value uintptr) uint64
	booleanGetValue         func(value uintptr) uint8
	booleanGetTypeID        func() uint64
	release                 func(value uintptr)
	retain                  func(value uintptr) uintptr
	equal                   func(left, right uintptr) uint8

	// Numbers and dictionaries. A Mission Control thumbnail names its window
	// in an undocumented attribute holding a CFNumber, and a window
	// description is a CFDictionary keyed by CFString.
	numberCreate               func(allocator uintptr, kind uint32, value unsafe.Pointer) uintptr
	numberGetTypeID            func() uint64
	numberGetValue             func(number uintptr, kind uint32, out unsafe.Pointer) uint8
	dictionaryGetCount         func(dict uintptr) int64
	dictionaryGetValue         func(dict, key uintptr) uintptr
	dictionaryGetTypeID        func() uint64
	dictionaryGetKeysAndValues func(dict uintptr, keys, values *uintptr)

	// Property lists, so a binary Info.plist is read the way the loader reads
	// it instead of being guessed from its bytes.
	dataCreate                 func(allocator uintptr, bytes *byte, length int64) uintptr
	propertyListCreateWithData func(allocator, data uintptr, options uint64, format *uint64, plistError *uintptr) uintptr

	// typeArrayCallbacks is the address of kCFTypeArrayCallBacks, which makes
	// a created array retain and release its members.
	typeArrayCallbacks uintptr
}

// accessibility is the ApplicationServices surface the backend uses.
type accessibility struct {
	createSystemWide    func() uintptr
	createApplication   func(pid int32) uintptr
	copyAttributeValue  func(element, attribute uintptr, value *uintptr) int32
	copyMultipleValues  func(element, attributes uintptr, options uint32, values *uintptr) int32
	copyElementAtPoint  func(element uintptr, x, y float32, result *uintptr) int32
	copyActionNames     func(element uintptr, names *uintptr) int32
	performAction       func(element, action uintptr) int32
	setAttributeValue   func(element, attribute, value uintptr) int32
	getPid              func(element uintptr, pid *int32) int32
	setMessagingTimeout func(element uintptr, seconds float32) int32
	valueGetValue       func(value uintptr, kind uint32, out unsafe.Pointer) uint8
	valueGetType        func(value uintptr) uint32
	valueGetTypeID      func() uint64
	isProcessTrusted    func() uint8

	// Accessibility Zoom, from Universal Access. Version-gated: a macOS without
	// them leaves the field nil, which turns the viewport follow off instead of
	// failing the backend.
	zoomEnabled     func() uint8
	zoomChangeFocus func(rect, highlight *cgRect, kind uint32) int32
}

// coreGraphics is the window-server surface the backend uses.
type coreGraphics struct {
	mainDisplayID     func() uint32
	displayBounds     func(display uint32) cgRect
	getActiveDisplays func(max uint32, displays *uint32, count *uint32) int32
	displayPixelsWide func(display uint32) uintptr
	displayPixelsHigh func(display uint32) uintptr

	eventCreate               func(source uintptr) uintptr
	eventCreateMouseEvent     func(source uintptr, kind uint32, point cgPoint, button uint32) uintptr
	eventCreateScrollWheelTwo func(source uintptr, units uint32, wheelCount uint32, wheel1, wheel2, wheel3 int32) uintptr
	eventPost                 func(tap uint32, event uintptr)
	eventSetFlags             func(event uintptr, flags uint64)
	eventSetLocation          func(event uintptr, point cgPoint)
	eventGetLocation          func(event uintptr) cgPoint
	eventSetIntegerField      func(event uintptr, field uint32, value int64)
	eventSourceButtonState    func(state uint32, button uint32) uint8
	warpCursor                func(point cgPoint) int32
	associateCursor           func(connected uint32) int32

	// Window-server queries the zoom viewport and Mission Control filtering
	// need: which display a point is on, and what the window server knows about
	// a window id.
	displaysWithPoint func(point cgPoint, maxDisplays uint32, displays *uint32, count *uint32) int32
	windowDescription func(windowIDs uintptr) uintptr
}

// objcRuntime is the Objective-C messaging surface. NSRunningApplication is
// the only class the backend talks to, and only for the facts AX does not
// carry: an application's bundle identifier and the process behind a bundle
// identifier.
type objcRuntime struct {
	getClass     func(name string) uintptr
	registerName func(name string) uintptr
	poolPush     func() uintptr
	poolPop      func(pool uintptr)

	// msgSend is bound once per signature. Objective-C methods are called by
	// sending a selector to a receiver, and the ABI decides where each
	// argument goes, so a distinct Go function type is needed per shape.
	msgSendID           func(receiver, selector uintptr) uintptr
	msgSendIDWithID     func(receiver, selector, argument uintptr) uintptr
	msgSendIDWithCStr   func(receiver, selector uintptr, argument string) uintptr
	msgSendIDWithInt    func(receiver, selector uintptr, argument int32) uintptr
	msgSendInt          func(receiver, selector uintptr) int32
	msgSendUint         func(receiver, selector uintptr) uintptr
	msgSendUintWithUint func(receiver, selector, argument uintptr) uintptr
	msgSendCStr         func(receiver, selector uintptr) *byte
}

// frameworks is the bound surface shared by every client in the process.
type frameworks struct {
	cf   coreFoundation
	ax   accessibility
	cg   coreGraphics
	objc objcRuntime
}

var (
	frameworksOnce sync.Once
	frameworksErr  error
	frameworksAPI  *frameworks
)

// loadFrameworks binds everything once. It returns the same error to every
// caller, so New can report it and no call site needs to check again.
func loadFrameworks() (*frameworks, error) {
	frameworksOnce.Do(func() {
		frameworksAPI, frameworksErr = bindFrameworks()
	})

	if frameworksErr != nil {
		return nil, frameworksErr
	}

	return frameworksAPI, nil
}

// bindFrameworks opens the four libraries and binds every symbol. It stops at
// the first failure: a backend missing one core call cannot work, and the
// error names the exact symbol.
func bindFrameworks() (*frameworks, error) {
	cfLib, err := native.Open(coreFoundationPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load CoreFoundation: %w", err)
	}

	axLib, err := native.Open(applicationServices)
	if err != nil {
		return nil, fmt.Errorf("darwin: load ApplicationServices: %w", err)
	}

	cgLib, err := native.Open(coreGraphicsPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load CoreGraphics: %w", err)
	}

	objcLib, err := native.Open(objectiveCRuntimePath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load the Objective-C runtime: %w", err)
	}

	api := &frameworks{}

	bind := func(lib *native.Library, name string, target any) error {
		if err := lib.Func(name, target); err != nil {
			return fmt.Errorf("darwin: bind %s: %w", name, err)
		}

		return nil
	}

	for _, bindErr := range []error{
		bind(cfLib, "CFStringCreateWithCString", &api.cf.stringCreateWithCString),
		bind(cfLib, "CFStringGetCString", &api.cf.stringGetCString),
		bind(cfLib, "CFStringGetLength", &api.cf.stringGetLength),
		bind(cfLib, "CFStringGetMaximumSizeForEncoding", &api.cf.stringGetMaximumSize),
		bind(cfLib, "CFStringGetTypeID", &api.cf.stringGetTypeID),
		bind(cfLib, "CFArrayGetCount", &api.cf.arrayGetCount),
		bind(cfLib, "CFArrayGetTypeID", &api.cf.arrayGetTypeID),
		bind(cfLib, "CFArrayGetValueAtIndex", &api.cf.arrayGetValueAtIndex),
		bind(cfLib, "CFArrayCreate", &api.cf.arrayCreate),
		bind(cfLib, "CFArrayCreateMutable", &api.cf.arrayCreateMutable),
		bind(cfLib, "CFArrayAppendValue", &api.cf.arrayAppendValue),
		bind(cfLib, "CFGetTypeID", &api.cf.getTypeID),
		bind(cfLib, "CFBooleanGetValue", &api.cf.booleanGetValue),
		bind(cfLib, "CFBooleanGetTypeID", &api.cf.booleanGetTypeID),
		bind(cfLib, "CFNumberCreate", &api.cf.numberCreate),
		bind(cfLib, "CFNumberGetTypeID", &api.cf.numberGetTypeID),
		bind(cfLib, "CFNumberGetValue", &api.cf.numberGetValue),
		bind(cfLib, "CFDictionaryGetCount", &api.cf.dictionaryGetCount),
		bind(cfLib, "CFDictionaryGetValue", &api.cf.dictionaryGetValue),
		bind(cfLib, "CFDictionaryGetTypeID", &api.cf.dictionaryGetTypeID),
		bind(cfLib, "CFDictionaryGetKeysAndValues", &api.cf.dictionaryGetKeysAndValues),
		bind(cfLib, "CFDataCreate", &api.cf.dataCreate),
		bind(cfLib, "CFPropertyListCreateWithData", &api.cf.propertyListCreateWithData),
		bind(cfLib, "CFRelease", &api.cf.release),
		bind(cfLib, "CFRetain", &api.cf.retain),
		bind(cfLib, "CFEqual", &api.cf.equal),
		bind(axLib, "AXUIElementCreateSystemWide", &api.ax.createSystemWide),
		bind(axLib, "AXUIElementCreateApplication", &api.ax.createApplication),
		bind(axLib, "AXUIElementCopyAttributeValue", &api.ax.copyAttributeValue),
		bind(axLib, "AXUIElementCopyMultipleAttributeValues", &api.ax.copyMultipleValues),
		bind(axLib, "AXUIElementCopyElementAtPosition", &api.ax.copyElementAtPoint),
		bind(axLib, "AXUIElementCopyActionNames", &api.ax.copyActionNames),
		bind(axLib, "AXUIElementPerformAction", &api.ax.performAction),
		bind(axLib, "AXUIElementSetAttributeValue", &api.ax.setAttributeValue),
		bind(axLib, "AXUIElementGetPid", &api.ax.getPid),
		bind(axLib, "AXUIElementSetMessagingTimeout", &api.ax.setMessagingTimeout),
		bind(axLib, "AXValueGetValue", &api.ax.valueGetValue),
		bind(axLib, "AXValueGetType", &api.ax.valueGetType),
		bind(axLib, "AXValueGetTypeID", &api.ax.valueGetTypeID),
		bind(axLib, "AXIsProcessTrusted", &api.ax.isProcessTrusted),
		bind(cgLib, "CGMainDisplayID", &api.cg.mainDisplayID),
		bind(cgLib, "CGDisplayBounds", &api.cg.displayBounds),
		bind(cgLib, "CGGetActiveDisplayList", &api.cg.getActiveDisplays),
		bind(cgLib, "CGDisplayPixelsWide", &api.cg.displayPixelsWide),
		bind(cgLib, "CGDisplayPixelsHigh", &api.cg.displayPixelsHigh),
		bind(cgLib, "CGGetDisplaysWithPoint", &api.cg.displaysWithPoint),
		bind(cgLib, "CGWindowListCreateDescriptionFromArray", &api.cg.windowDescription),
		bind(cgLib, "CGEventCreate", &api.cg.eventCreate),
		bind(cgLib, "CGEventCreateMouseEvent", &api.cg.eventCreateMouseEvent),
		bind(cgLib, "CGEventCreateScrollWheelEvent2", &api.cg.eventCreateScrollWheelTwo),
		bind(cgLib, "CGEventPost", &api.cg.eventPost),
		bind(cgLib, "CGEventSetFlags", &api.cg.eventSetFlags),
		bind(cgLib, "CGEventSetLocation", &api.cg.eventSetLocation),
		bind(cgLib, "CGEventGetLocation", &api.cg.eventGetLocation),
		bind(cgLib, "CGEventSetIntegerValueField", &api.cg.eventSetIntegerField),
		bind(cgLib, "CGEventSourceButtonState", &api.cg.eventSourceButtonState),
		bind(cgLib, "CGWarpMouseCursorPosition", &api.cg.warpCursor),
		bind(cgLib, "CGAssociateMouseAndMouseCursorPosition", &api.cg.associateCursor),
		bind(objcLib, "objc_getClass", &api.objc.getClass),
		bind(objcLib, "sel_registerName", &api.objc.registerName),
		bind(objcLib, "objc_autoreleasePoolPush", &api.objc.poolPush),
		bind(objcLib, "objc_autoreleasePoolPop", &api.objc.poolPop),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendIDWithID),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendIDWithCStr),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendIDWithInt),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendInt),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendUintWithUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendCStr),
	} {
		if bindErr != nil {
			return nil, bindErr
		}
	}

	callbacks, err := cfLib.Data("kCFTypeArrayCallBacks")
	if err != nil {
		return nil, fmt.Errorf("darwin: bind kCFTypeArrayCallBacks: %w", err)
	}

	api.cf.typeArrayCallbacks = callbacks

	// Both Accessibility Zoom entry points are optional: they are the one part of
	// ApplicationServices this backend can live without, and an old or future
	// macOS that stops exporting them costs the zoom viewport follow behavior and
	// nothing else. The fields stay nil when a symbol is missing.
	if zoomErr := axLib.Func("UAZoomEnabled", &api.ax.zoomEnabled); zoomErr != nil {
		api.ax.zoomEnabled = nil
	}

	if zoomErr := axLib.Func("UAZoomChangeFocus", &api.ax.zoomChangeFocus); zoomErr != nil {
		api.ax.zoomChangeFocus = nil
	}

	return api, nil
}
