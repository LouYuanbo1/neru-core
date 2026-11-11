//go:build darwin

package capture

import (
	"context"
	"fmt"
	"image"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
	"github.com/ebitengine/purego"
)

// This file is the fast macOS capture path. It asks ScreenCaptureKit for one
// screenshot and leaves this process's own windows out of it, which the
// `screencapture` utility cannot do. Everything is reached through purego, so
// the package still builds without CGo: the framework is loaded with dlopen,
// its classes are looked up by name, and messages are sent to them with
// objc_msgSend bound once per argument shape.
//
// Capture is synchronous. A capture waits for the two completion handlers
// ScreenCaptureKit hands back on its own dispatch queue, so captures are
// serialized by a mutex and a single pending-call slot. The completion handlers
// are Objective-C blocks, which purego cannot construct; a Go-built global
// block literal is used instead (see blockLiteral below).
//
// When any of this cannot be done the function returns an error and the caller
// falls back to `screencapture`. That includes running on a macOS older than
// 14, where SCScreenshotManager does not exist: the class lookup fails, which
// is reported as CodeNotSupported. Every error here is internal to the
// optimization; darwin.go discards it and reports whatever the fallback says,
// because that is the outcome a caller can act on.
//
// Two deliberate choices:
//
//   - A region is clipped to the display that holds most of it. ScreenCaptureKit
//     takes a source rect in one display's points, so it cannot express a rect
//     spanning displays; the shot then covers the clipped rect, and Origin and
//     Region describe exactly that.
//
//   - If the shareable content's window list cannot be read, the capture
//     proceeds with an empty exclusion list rather than failing. The fallback
//     cannot exclude this process's windows either, so a capture that may show
//     the caller's overlay is still no worse than what would run instead.

// Paths of the libraries this path uses. ScreenCaptureKit has to be loaded
// explicitly: nothing in a pure Go binary references it, and objc_getClass only
// finds a class once its framework is in the process.
const (
	sckFrameworkPath    = "/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit"
	sckCoreGraphicsPath = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	sckObjectiveCPath   = "/usr/lib/libobjc.A.dylib"
	sckSystemPath       = "/usr/lib/libSystem.B.dylib"
)

const (
	// sckDefaultTimeout bounds a capture whose caller set no deadline.
	sckDefaultTimeout = 10 * time.Second

	// sckMaxDisplays is the largest display list CGGetActiveDisplayList is
	// given. macOS supports far fewer.
	sckMaxDisplays = 32
)

const (
	// sckBlockFlagIsGlobal is BLOCK_IS_GLOBAL from Block.h. A literal without
	// it is treated as a stack block: _Block_copy mallocs a copy and the block
	// machinery later frees it. Neither breaks a literal that captures nothing,
	// but the flag is what makes it genuinely static and allocation-free.
	sckBlockFlagIsGlobal int32 = 1 << 28

	// sckPixelFormat32BGRA is kCVPixelFormatType_32BGRA, the native layout
	// ScreenCaptureKit hands back for a display capture.
	sckPixelFormat32BGRA uint32 = 0x42475241

	// Bitmap info for 8 bits per component RGBA: alpha premultiplied last, with
	// the components in big-endian order. That is the byte order image.RGBA
	// expects, so Core Graphics does the channel swizzle while drawing.
	sckBitmapAlphaPremultipliedLast uint32 = 1
	sckBitmapByteOrder32Big         uint32 = 4 << 12

	// sckColorSpaceModelRGB is kCGColorSpaceModelRGB from CGColorSpace.h.
	sckColorSpaceModelRGB int32 = 1

	// ScreenCaptureKit's denial codes, from SCError.h. UserDeclined is the one
	// macOS reports when the process has not been granted Screen Recording;
	// MissingEntitlements is the same refusal from a sandboxed process.
	sckErrorUserDeclined        int64 = -3801
	sckErrorMissingEntitlements int64 = -3803
)

// cgPoint, cgSize and cgRect mirror CoreGraphics' CGFloat structs. CGFloat is a
// double on every 64-bit platform, which is the only place this file builds.
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

// blockDescriptor is Block_descriptor_1 from Block.h: the tail of a block
// literal. A block with no captured state and no copy or dispose helpers has
// just these two words.
type blockDescriptor struct {
	reserved uintptr
	size     uintptr
}

// blockLiteral is Block_layout from Block.h: the head of a block literal. The
// fields are in the order and at the offsets the runtime reads, which is what
// the layout test in this package pins down.
type blockLiteral struct {
	isa        uintptr
	flags      int32
	reserved   int32
	invoke     uintptr
	descriptor uintptr
}

// The two completion handlers are global blocks: literals that capture nothing,
// built once and passed to every call. They are separate literals so each Go
// callback knows which call it is completing without the block having to carry
// state; both are package-level so the addresses stay valid and Go's garbage
// collector sees them as live for the life of the process.
var (
	contentBlock blockLiteral
	screenBlock  blockLiteral

	contentBlockDescriptor = blockDescriptor{size: unsafe.Sizeof(blockLiteral{})}
	screenBlockDescriptor  = blockDescriptor{size: unsafe.Sizeof(blockLiteral{})}
)

// sckStage names the call a completion handler belongs to.
type sckStage uint8

const (
	sckStageContent sckStage = iota + 1
	sckStageImage
)

// sckEvent is one completion handler's result: the object it handed back,
// retained, and the NSError, retained. Exactly one of the two is non-zero on a
// successful call.
type sckEvent struct {
	stage   sckStage
	object  uintptr
	failure uintptr
}

// coreGraphics is the CoreGraphics surface this path uses. Everything is a
// plain C function, so it is bound once and never released.
type coreGraphics struct {
	mainDisplayID     func() uint32
	activeDisplayList func(max uint32, displays *uint32, count *uint32) int32
	displayBounds     func(display uint32) cgRect
	displayPixelsWide func(display uint32) uintptr
	displayPixelsHigh func(display uint32) uintptr

	imageGetWidth      func(image uintptr) uintptr
	imageGetHeight     func(image uintptr) uintptr
	imageGetColorSpace func(image uintptr) uintptr
	imageRetain        func(image uintptr) uintptr
	imageRelease       func(image uintptr)

	colourSpaceCreateDeviceRGB func() uintptr
	colourSpaceGetModel        func(space uintptr) int32
	colourSpaceRelease         func(space uintptr)

	bitmapContextCreate func(data, width, height, bitsPerComponent, bytesPerRow, space uintptr, bitmapInfo uint32) uintptr
	contextDrawImage    func(context uintptr, rect cgRect, image uintptr)
	contextRelease      func(context uintptr)
}

// objcRuntime is the Objective-C messaging surface. The ABI decides where each
// argument goes, so objc_msgSend is bound once per argument shape.
type objcRuntime struct {
	getClass func(name string) uintptr
	selector func(name string) uintptr
	poolPush func() uintptr
	poolPop  func(pool uintptr)

	msgSendID     func(receiver, selector uintptr) uintptr
	msgSendID1    func(receiver, selector, argument uintptr) uintptr
	msgSendID2    func(receiver, selector, first, second uintptr) uintptr
	msgSendID3    func(receiver, selector, first, second, third uintptr) uintptr
	msgSendUint   func(receiver, selector uintptr) uintptr
	msgSendUint32 func(receiver, selector uintptr) uint32
	msgSendInt32  func(receiver, selector uintptr) int32
	msgSendInt64  func(receiver, selector uintptr) int64

	msgSendCStr     func(receiver, selector uintptr) *byte
	msgSendVoid     func(receiver, selector uintptr)
	msgSendVoid1    func(receiver, selector, argument uintptr)
	msgSendVoid3    func(receiver, selector, first, second, third uintptr)
	msgSendVoidRect func(receiver, selector uintptr, rect cgRect)
}

// selectors are the message names this path sends, interned once at bind time
// so a capture pays no lookup cost.
type selectors struct {
	shareableContent  uintptr
	displays          uintptr
	windows           uintptr
	displayID         uintptr
	owningApplication uintptr
	processID         uintptr
	new               uintptr
	alloc             uintptr
	initFilter        uintptr
	count             uintptr
	objectAtIndex     uintptr
	addObject         uintptr
	setWidth          uintptr
	setHeight         uintptr
	setShowsCursor    uintptr
	setCapturesAudio  uintptr
	setPixelFormat    uintptr
	setSourceRect     uintptr
	captureImage      uintptr
	retain            uintptr
	release           uintptr
	domain            uintptr
	code              uintptr
	utf8String        uintptr
}

// sck is the bound ScreenCaptureKit surface, with the classes and selectors
// already resolved.
type sck struct {
	// framework is the loaded ScreenCaptureKit binary. It is kept, not because
	// its symbols are called, but because loading it is what registers the
	// classes above with the Objective-C runtime.
	framework *native.Library

	cg   coreGraphics
	objc objcRuntime

	getPid func() int32

	// globalBlockISA is _NSConcreteGlobalBlock, or the class looked up by name
	// when the symbol is not exported.
	globalBlockISA uintptr

	classShareableContent    uintptr
	classScreenshotManager   uintptr
	classContentFilter       uintptr
	classStreamConfiguration uintptr
	classMutableArray        uintptr

	sel selectors

	selfPID int32
}

var (
	sckOnce     sync.Once
	sckInstance *sck
	sckLoadErr  error
)

// loadSCK binds the framework once. A missing library, class or symbol is
// reported as CodeNotSupported, which is what the caller turns into the
// screencapture fallback.
func loadSCK() (*sck, error) {
	sckOnce.Do(func() {
		bound, err := bindSCK()
		if err != nil {
			sckLoadErr = errs.Wrap(err, errs.CodeNotSupported, "ScreenCaptureKit is unavailable")

			return
		}

		sckInstance = bound
	})

	if sckLoadErr != nil {
		return nil, sckLoadErr
	}

	return sckInstance, nil
}

// bindSCK opens the four libraries and binds every symbol, class and selector
// this path uses. It stops at the first failure and names it. A panic is turned
// into an error too: purego reports a signature it cannot express that way, and
// a capture that cannot bind must degrade to the fallback rather than take the
// process down.
func bindSCK() (api *sck, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			api = nil
			err = fmt.Errorf("darwin: bind ScreenCaptureKit: %v", recovered)
		}
	}()

	api = &sck{}

	cgLib, err := native.Open(sckCoreGraphicsPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load CoreGraphics: %w", err)
	}

	objcLib, err := native.Open(sckObjectiveCPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load the Objective-C runtime: %w", err)
	}

	systemLib, err := native.Open(sckSystemPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load libSystem: %w", err)
	}

	frameworkLib, err := native.Open(sckFrameworkPath)
	if err != nil {
		return nil, fmt.Errorf("darwin: load ScreenCaptureKit: %w", err)
	}

	api.framework = frameworkLib

	bind := func(lib *native.Library, name string, target any) error {
		if err := lib.Func(name, target); err != nil {
			return fmt.Errorf("darwin: bind %s: %w", name, err)
		}

		return nil
	}

	for _, bindErr := range []error{
		bind(cgLib, "CGMainDisplayID", &api.cg.mainDisplayID),
		bind(cgLib, "CGGetActiveDisplayList", &api.cg.activeDisplayList),
		bind(cgLib, "CGDisplayBounds", &api.cg.displayBounds),
		bind(cgLib, "CGDisplayPixelsWide", &api.cg.displayPixelsWide),
		bind(cgLib, "CGDisplayPixelsHigh", &api.cg.displayPixelsHigh),
		bind(cgLib, "CGImageGetWidth", &api.cg.imageGetWidth),
		bind(cgLib, "CGImageGetHeight", &api.cg.imageGetHeight),
		bind(cgLib, "CGImageGetColorSpace", &api.cg.imageGetColorSpace),
		bind(cgLib, "CGImageRetain", &api.cg.imageRetain),
		bind(cgLib, "CGImageRelease", &api.cg.imageRelease),
		bind(cgLib, "CGColorSpaceCreateDeviceRGB", &api.cg.colourSpaceCreateDeviceRGB),
		bind(cgLib, "CGColorSpaceGetModel", &api.cg.colourSpaceGetModel),
		bind(cgLib, "CGColorSpaceRelease", &api.cg.colourSpaceRelease),
		bind(cgLib, "CGBitmapContextCreate", &api.cg.bitmapContextCreate),
		bind(cgLib, "CGContextDrawImage", &api.cg.contextDrawImage),
		bind(cgLib, "CGContextRelease", &api.cg.contextRelease),
		bind(objcLib, "objc_getClass", &api.objc.getClass),
		bind(objcLib, "sel_registerName", &api.objc.selector),
		bind(objcLib, "objc_autoreleasePoolPush", &api.objc.poolPush),
		bind(objcLib, "objc_autoreleasePoolPop", &api.objc.poolPop),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID1),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID2),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID3),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendUint32),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendInt32),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendInt64),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendCStr),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendVoid),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendVoid1),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendVoid3),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendVoidRect),
		bind(systemLib, "getpid", &api.getPid),
	} {
		if bindErr != nil {
			return nil, bindErr
		}
	}

	// _NSConcreteGlobalBlock is the isa a literal in static storage carries. The
	// symbol is the class object itself, not a pointer to it, so its address is
	// what a block's isa field wants. Some builds do not export it; the block
	// class can be looked up by name instead, which gives the same object.
	if address, addressErr := systemLib.Data("_NSConcreteGlobalBlock"); addressErr == nil {
		api.globalBlockISA = address
	}

	for _, binding := range []struct {
		name   string
		target *uintptr
	}{
		{"SCShareableContent", &api.classShareableContent},
		{"SCScreenshotManager", &api.classScreenshotManager},
		{"SCContentFilter", &api.classContentFilter},
		{"SCStreamConfiguration", &api.classStreamConfiguration},
		{"NSMutableArray", &api.classMutableArray},
	} {
		*binding.target = api.objc.getClass(binding.name)
		if *binding.target == 0 {
			return nil, fmt.Errorf("darwin: class %s is missing", binding.name)
		}
	}

	if api.globalBlockISA == 0 {
		api.globalBlockISA = api.objc.getClass("__NSGlobalBlock__")
		if api.globalBlockISA == 0 {
			return nil, fmt.Errorf("darwin: no global block class")
		}
	}

	for _, binding := range []struct {
		name   string
		target *uintptr
	}{
		{"getShareableContentExcludingDesktopWindows:onScreenWindowsOnly:completionHandler:", &api.sel.shareableContent},
		{"displays", &api.sel.displays},
		{"windows", &api.sel.windows},
		{"displayID", &api.sel.displayID},
		{"owningApplication", &api.sel.owningApplication},
		{"processID", &api.sel.processID},
		{"new", &api.sel.new},
		{"alloc", &api.sel.alloc},
		{"initWithDisplay:excludingWindows:", &api.sel.initFilter},
		{"count", &api.sel.count},
		{"objectAtIndex:", &api.sel.objectAtIndex},
		{"addObject:", &api.sel.addObject},
		{"setWidth:", &api.sel.setWidth},
		{"setHeight:", &api.sel.setHeight},
		{"setShowsCursor:", &api.sel.setShowsCursor},
		{"setCapturesAudio:", &api.sel.setCapturesAudio},
		{"setPixelFormat:", &api.sel.setPixelFormat},
		{"setSourceRect:", &api.sel.setSourceRect},
		{"captureImageWithFilter:configuration:completionHandler:", &api.sel.captureImage},
		{"retain", &api.sel.retain},
		{"release", &api.sel.release},
		{"domain", &api.sel.domain},
		{"code", &api.sel.code},
		{"UTF8String", &api.sel.utf8String},
	} {
		*binding.target = api.objc.selector(binding.name)
		if *binding.target == 0 {
			return nil, fmt.Errorf("darwin: selector %s is missing", binding.name)
		}
	}

	api.selfPID = api.getPid()
	if api.selfPID <= 0 {
		return nil, fmt.Errorf("darwin: getpid returned %d", api.selfPID)
	}

	if err := api.installBlocks(); err != nil {
		return nil, err
	}

	return api, nil
}

// installBlocks fills in the two global block literals and their invoke
// pointers. A Go callback is the block's invoke function; the block itself
// carries no captured state, so the callback reads everything it needs from
// this binding and from the package-level pending-call slot.
func (a *sck) installBlocks() error {
	if a.globalBlockISA == 0 {
		return fmt.Errorf("darwin: no global block class")
	}

	contentBlock = blockLiteral{
		isa:        a.globalBlockISA,
		flags:      sckBlockFlagIsGlobal,
		descriptor: uintptr(unsafe.Pointer(&contentBlockDescriptor)),
	}
	contentBlock.invoke = purego.NewCallback(func(_, content, failure uintptr) uintptr {
		a.deliver(sckStageContent, content, failure)

		return 0
	})

	screenBlock = blockLiteral{
		isa:        a.globalBlockISA,
		flags:      sckBlockFlagIsGlobal,
		descriptor: uintptr(unsafe.Pointer(&screenBlockDescriptor)),
	}
	screenBlock.invoke = purego.NewCallback(func(_, picture, failure uintptr) uintptr {
		a.deliver(sckStageImage, picture, failure)

		return 0
	})

	return nil
}

// displayInfo is one display's geometry: its window-server id, its bounds in
// global logical points, and its backing size in pixels.
type displayInfo struct {
	id     uint32
	bounds image.Rectangle
	pixels image.Point
}

// scale is the display's backing scale, frame pixels per logical pixel.
func (d displayInfo) scale() float64 {
	if d.bounds.Dx() <= 0 || d.bounds.Dy() <= 0 || d.pixels.X <= 0 || d.pixels.Y <= 0 {
		return 1
	}

	scale := float64(d.pixels.X) / float64(d.bounds.Dx())
	if scale <= 0 {
		return 1
	}

	return scale
}

// sckTarget is what one capture asks ScreenCaptureKit for: a display, the
// logical desktop area the shot covers, and whether the whole display is taken.
type sckTarget struct {
	display displayInfo
	region  image.Rectangle
	whole   bool
}

// pixelSize is the output size in pixels for the target's region.
func (t sckTarget) pixelSize() (int, int) {
	if t.whole {
		return t.display.pixels.X, t.display.pixels.Y
	}

	return regionPixelSize(t.region, t.display.scale())
}

// captureWithScreenCaptureKit returns one screen/region shot with this
// process's windows excluded. Any failure returns an error; the caller then
// falls back to screencapture.
func captureWithScreenCaptureKit(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	if err := ctx.Err(); err != nil {
		return pixels.Frame{}, errs.Wrap(err, errs.CodeContextCanceled, "capture canceled")
	}

	deadline := time.Now().Add(sckDefaultTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline
	}

	if time.Until(deadline) <= 0 {
		return pixels.Frame{}, errs.New(errs.CodeTimeout, "the capture deadline has already passed")
	}

	api, err := loadSCK()
	if err != nil {
		return pixels.Frame{}, err
	}

	target, err := api.target(region)
	if err != nil {
		return pixels.Frame{}, err
	}

	// Only one completion handler may be outstanding at a time: the block
	// literals carry no identity, so two captures in flight together would
	// deliver into each other's results.
	if err := sckEnterCapture(ctx, deadline); err != nil {
		return pixels.Frame{}, err
	}
	defer sckLeaveCapture()

	img, err := api.capture(ctx, target, deadline)
	if err != nil {
		return pixels.Frame{}, err
	}

	scale := float64(img.Rect.Dx()) / float64(target.region.Dx())
	if scale <= 0 {
		scale = target.display.scale()
	}

	return pixels.Frame{
		Img:    img,
		Origin: target.region.Min,
		Scale:  scale,
		Region: target.region,
	}, nil
}

// target resolves which display and which area of the desktop the capture
// covers. An empty region is the main display, matching the screencapture path;
// a non-empty one is clipped to the display that holds most of it.
func (a *sck) target(region image.Rectangle) (sckTarget, error) {
	displays, err := a.displays()
	if err != nil {
		return sckTarget{}, err
	}

	region = region.Canon()

	if region.Empty() {
		main := a.cg.mainDisplayID()
		for _, display := range displays {
			if display.id == main {
				return sckTarget{display: display, region: display.bounds, whole: true}, nil
			}
		}

		return sckTarget{}, errs.Newf(
			errs.CodeInternal,
			"the main display %d is not in the shareable content",
			main,
		)
	}

	display, ok := displayContaining(displays, region)
	if !ok {
		return sckTarget{}, errs.Newf(errs.CodeInternal, "no display holds the region %v", region)
	}

	clipped := clipRegion(region, display.bounds)
	if clipped.Empty() {
		return sckTarget{}, errs.Newf(errs.CodeInternal, "the region %v clips to nothing", region)
	}

	return sckTarget{display: display, region: clipped}, nil
}

// displays lists the active displays with their bounds and pixel sizes.
func (a *sck) displays() ([]displayInfo, error) {
	// CGMainDisplayID opens the process's window-server connection; the other
	// display queries can fail before one exists.
	if a.cg.mainDisplayID() == 0 {
		return nil, errs.New(errs.CodeInternal, "CoreGraphics reported no main display")
	}

	var (
		ids   [sckMaxDisplays]uint32
		count uint32
	)

	if status := a.cg.activeDisplayList(sckMaxDisplays, &ids[0], &count); status != 0 || count == 0 {
		return nil, errs.Newf(errs.CodeInternal, "CoreGraphics could not list the active displays (error %d)", status)
	}

	if count > sckMaxDisplays {
		count = sckMaxDisplays
	}

	displays := make([]displayInfo, 0, count)
	for _, id := range ids[:count] {
		bounds := a.cg.displayBounds(id)
		displays = append(displays, displayInfo{
			id:     id,
			bounds: rectFromCG(bounds),
			pixels: image.Pt(int(a.cg.displayPixelsWide(id)), int(a.cg.displayPixelsHigh(id))),
		})
	}

	return displays, nil
}

// rectFromCG rounds a CoreGraphics rect into a desktop rectangle. Display
// bounds are integral points in practice, but rounding keeps a fractional
// origin from truncating towards zero.
func rectFromCG(rect cgRect) image.Rectangle {
	x := math.Round(rect.Origin.X)
	y := math.Round(rect.Origin.Y)

	return image.Rect(
		int(x),
		int(y),
		int(x+math.Round(rect.Size.Width)),
		int(y+math.Round(rect.Size.Height)),
	)
}

// displayContaining picks the display that holds the largest part of region.
// Ties keep the earlier display, which is the main display on a mirrored setup
// because CGGetActiveDisplayList lists it first.
func displayContaining(displays []displayInfo, region image.Rectangle) (displayInfo, bool) {
	best := displayInfo{}
	bestArea := 0

	for _, display := range displays {
		overlap := region.Intersect(display.bounds)
		if overlap.Empty() {
			continue
		}

		if area := overlap.Dx() * overlap.Dy(); area > bestArea {
			best, bestArea = display, area
		}
	}

	return best, bestArea > 0
}

// clipRegion is the part of region the display can show.
func clipRegion(region, bounds image.Rectangle) image.Rectangle {
	return region.Intersect(bounds)
}

// sourceRectIn turns a clipped global region into the display-local rect, in
// points, ScreenCaptureKit's sourceRect expects.
func sourceRectIn(region, bounds image.Rectangle) cgRect {
	return cgRect{
		Origin: cgPoint{
			X: float64(region.Min.X - bounds.Min.X),
			Y: float64(region.Min.Y - bounds.Min.Y),
		},
		Size: cgSize{
			Width:  float64(region.Dx()),
			Height: float64(region.Dy()),
		},
	}
}

// regionPixelSize is the pixel size of a logical region at a backing scale,
// rounded away from zero so a region never asks for an empty output.
func regionPixelSize(region image.Rectangle, scale float64) (int, int) {
	if scale <= 0 {
		scale = 1
	}

	width := int(math.Round(float64(region.Dx()) * scale))
	height := int(math.Round(float64(region.Dy()) * scale))

	if width < 1 {
		width = 1
	}

	if height < 1 {
		height = 1
	}

	return width, height
}

// ownWindowIndexes returns the positions in pids whose process is self.
func ownWindowIndexes(pids []int32, self int32) []int {
	var indexes []int

	for index, pid := range pids {
		if pid == self {
			indexes = append(indexes, index)
		}
	}

	return indexes
}

// cropRGBA drops pixels outside a width×height rectangle anchored at the
// image's top-left corner. ScreenCaptureKit can round an output size to what
// the display stream can give, and the frame's Region is the region asked for,
// so a slightly larger image is trimmed rather than folded into the scale. An
// image smaller than asked for, or one already the right size, is returned as
// it is.
func cropRGBA(src *image.RGBA, width, height int) *image.RGBA {
	bounds := src.Bounds()

	width = min(width, bounds.Dx())
	height = min(height, bounds.Dy())

	if width <= 0 || height <= 0 || (width == bounds.Dx() && height == bounds.Dy()) {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for row := 0; row < height; row++ {
		copy(
			dst.Pix[row*dst.Stride:row*dst.Stride+width*4],
			src.Pix[row*src.Stride:row*src.Stride+width*4],
		)
	}

	return dst
}

// capture runs the two ScreenCaptureKit calls and returns the shot's pixels.
func (a *sck) capture(ctx context.Context, target sckTarget, deadline time.Time) (*image.RGBA, error) {
	// Objective-C objects are autoreleased into the pool of the thread that
	// created them, and a Go goroutine has no pool. The thread is locked so the
	// pool is popped on the thread that pushed it even though the capture waits
	// on a channel in between.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pool := a.objc.poolPush()
	defer a.objc.poolPop(pool)

	content, err := a.shareableContent(ctx, deadline)
	if err != nil {
		return nil, err
	}
	defer a.objc.msgSendVoid(content, a.sel.release)

	display := a.shareableDisplay(content, target.display.id)
	if display == 0 {
		return nil, errs.Newf(errs.CodeInternal, "ScreenCaptureKit does not list display %d", target.display.id)
	}

	windows := a.ownWindows(content)
	if windows != 0 {
		defer a.objc.msgSendVoid(windows, a.sel.release)
	}

	filter := a.contentFilter(display, windows)
	if filter == 0 {
		return nil, errs.New(errs.CodeInternal, "could not create a ScreenCaptureKit content filter")
	}
	defer a.objc.msgSendVoid(filter, a.sel.release)

	config, err := a.streamConfiguration(target)
	if err != nil {
		return nil, err
	}
	defer a.objc.msgSendVoid(config, a.sel.release)

	picture, err := a.screenshot(ctx, filter, config, deadline)
	if err != nil {
		return nil, err
	}
	defer a.cg.imageRelease(picture)

	img, err := a.rgbaImage(picture)
	if err != nil {
		return nil, err
	}

	if target.whole {
		return img, nil
	}

	width, height := target.pixelSize()

	return cropRGBA(img, width, height), nil
}

// shareableContent fetches the window, display and application list. It is the
// only call that needs the window server's permission, so a refusal surfaces
// here as CodeAccessibilityDenied.
func (a *sck) shareableContent(ctx context.Context, deadline time.Time) (uintptr, error) {
	events, err := sckInstallPending()
	if err != nil {
		return 0, err
	}

	// +[SCShareableContent
	//     getShareableContentExcludingDesktopWindows:NO
	//     onScreenWindowsOnly:YES
	//     completionHandler:block]
	a.objc.msgSendVoid3(
		a.classShareableContent,
		a.sel.shareableContent,
		0,
		1,
		uintptr(unsafe.Pointer(&contentBlock)),
	)

	event, err := a.await(ctx, events, sckStageContent, deadline)
	if err != nil {
		return 0, err
	}

	if event.failure != 0 {
		code, detail := a.errorDetail(event.failure)
		a.discard(event)

		return 0, errs.Newf(code, "ScreenCaptureKit could not list shareable content: %s", detail)
	}

	if event.object == 0 {
		return 0, errs.New(errs.CodeInternal, "ScreenCaptureKit returned no shareable content")
	}

	return event.object, nil
}

// shareableDisplay finds the SCDisplay for a window-server display id.
func (a *sck) shareableDisplay(content uintptr, displayID uint32) uintptr {
	displays := a.objc.msgSendID(content, a.sel.displays)
	if displays == 0 {
		return 0
	}

	count := int(a.objc.msgSendUint(displays, a.sel.count))
	for index := 0; index < count; index++ {
		display := a.objc.msgSendID1(displays, a.sel.objectAtIndex, uintptr(index))
		if display == 0 {
			continue
		}

		if a.objc.msgSendUint32(display, a.sel.displayID) == displayID {
			return display
		}
	}

	return 0
}

// ownWindows builds the array of this process's windows for the content
// filter's exclusion list, or 0 when there is nothing to exclude or the list
// cannot be read. The caller then captures without exclusions; the
// screencapture fallback could not exclude anything either, so the capture is
// still no worse than what would run instead.
func (a *sck) ownWindows(content uintptr) uintptr {
	windows := a.objc.msgSendID(content, a.sel.windows)
	if windows == 0 {
		return 0
	}

	count := int(a.objc.msgSendUint(windows, a.sel.count))
	if count <= 0 {
		return 0
	}

	objects := make([]uintptr, 0, count)
	pids := make([]int32, 0, count)

	for index := 0; index < count; index++ {
		window := a.objc.msgSendID1(windows, a.sel.objectAtIndex, uintptr(index))
		if window == 0 {
			continue
		}

		owner := a.objc.msgSendID(window, a.sel.owningApplication)
		if owner == 0 {
			continue
		}

		objects = append(objects, window)
		pids = append(pids, a.objc.msgSendInt32(owner, a.sel.processID))
	}

	var list uintptr

	for _, index := range ownWindowIndexes(pids, a.selfPID) {
		if list == 0 {
			list = a.objc.msgSendID(a.classMutableArray, a.sel.new)
			if list == 0 {
				return 0
			}
		}

		a.objc.msgSendVoid1(list, a.sel.addObject, objects[index])
	}

	return list
}

// contentFilter makes a filter that captures one display minus the given
// windows.
func (a *sck) contentFilter(display, windows uintptr) uintptr {
	filter := a.objc.msgSendID(a.classContentFilter, a.sel.alloc)
	if filter == 0 {
		return 0
	}

	return a.objc.msgSendID2(filter, a.sel.initFilter, display, windows)
}

// streamConfiguration builds the output configuration for the target. The
// output size is in pixels and the source rect is in the display's points,
// which is what makes a retina display come back at its backing scale.
func (a *sck) streamConfiguration(target sckTarget) (uintptr, error) {
	width, height := target.pixelSize()
	if width <= 0 || height <= 0 {
		return 0, errs.Newf(errs.CodeInternal, "display %d reported no pixel size", target.display.id)
	}

	config := a.objc.msgSendID(a.classStreamConfiguration, a.sel.new)
	if config == 0 {
		return 0, errs.New(errs.CodeInternal, "could not create a ScreenCaptureKit stream configuration")
	}

	if !target.whole {
		a.objc.msgSendVoidRect(config, a.sel.setSourceRect, sourceRectIn(target.region, target.display.bounds))
	}

	a.objc.msgSendVoid1(config, a.sel.setWidth, uintptr(width))
	a.objc.msgSendVoid1(config, a.sel.setHeight, uintptr(height))
	// A pointer drawn over the target reads as a control under the cursor, so
	// it is left out of the pixels.
	a.objc.msgSendVoid1(config, a.sel.setShowsCursor, 0)
	a.objc.msgSendVoid1(config, a.sel.setCapturesAudio, 0)
	a.objc.msgSendVoid1(config, a.sel.setPixelFormat, uintptr(sckPixelFormat32BGRA))

	return config, nil
}

// screenshot runs the single-frame capture and returns the retained CGImage.
func (a *sck) screenshot(ctx context.Context, filter, config uintptr, deadline time.Time) (uintptr, error) {
	events, err := sckInstallPending()
	if err != nil {
		return 0, err
	}

	// +[SCScreenshotManager captureImageWithFilter:configuration:completionHandler:]
	a.objc.msgSendVoid3(
		a.classScreenshotManager,
		a.sel.captureImage,
		filter,
		config,
		uintptr(unsafe.Pointer(&screenBlock)),
	)

	event, err := a.await(ctx, events, sckStageImage, deadline)
	if err != nil {
		return 0, err
	}

	if event.failure != 0 {
		code, detail := a.errorDetail(event.failure)
		a.discard(event)

		return 0, errs.Newf(code, "ScreenCaptureKit could not capture the screen: %s", detail)
	}

	if event.object == 0 {
		return 0, errs.New(errs.CodeInternal, "ScreenCaptureKit returned an empty image")
	}

	return event.object, nil
}

// rgbaImage copies a captured CGImage into Go memory as RGBA. The image is
// drawn into an 8-bit-per-component bitmap context whose layout matches
// image.RGBA, in the image's own colour space so the channels are swizzled
// rather than converted; a screenshot is opaque, so premultiplied alpha equals
// straight alpha.
func (a *sck) rgbaImage(picture uintptr) (*image.RGBA, error) {
	width := int(a.cg.imageGetWidth(picture))
	height := int(a.cg.imageGetHeight(picture))

	if width <= 0 || height <= 0 {
		return nil, errs.New(errs.CodeInternal, "the captured image has zero size")
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	var context uintptr

	// No colour conversion: the image's own RGB space is reused, so drawing only
	// swizzles the channels. A space that cannot back an 8-bit bitmap falls
	// through to device RGB.
	if space := a.cg.imageGetColorSpace(picture); space != 0 && a.cg.colourSpaceGetModel(space) == sckColorSpaceModelRGB {
		context = a.bitmapContext(img, space)
	}

	if context == 0 {
		space := a.cg.colourSpaceCreateDeviceRGB()
		if space == 0 {
			return nil, errs.New(errs.CodeInternal, "could not create an RGB colour space")
		}

		context = a.bitmapContext(img, space)
		a.cg.colourSpaceRelease(space)
	}

	if context == 0 {
		return nil, errs.New(errs.CodeInternal, "could not create a bitmap context")
	}

	a.cg.contextDrawImage(
		context,
		cgRect{Size: cgSize{Width: float64(width), Height: float64(height)}},
		picture,
	)
	a.cg.contextRelease(context)

	// The bitmap context wrote straight into img.Pix; the compiler must not
	// consider img dead while Core Graphics holds its pointer.
	runtime.KeepAlive(img)

	return img, nil
}

// bitmapContext creates an 8-bit-per-component RGBA bitmap context over img's
// pixels in the given colour space.
func (a *sck) bitmapContext(img *image.RGBA, space uintptr) uintptr {
	return a.cg.bitmapContextCreate(
		uintptr(unsafe.Pointer(&img.Pix[0])),
		uintptr(img.Rect.Dx()),
		uintptr(img.Rect.Dy()),
		8,
		uintptr(img.Stride),
		space,
		sckBitmapAlphaPremultipliedLast|sckBitmapByteOrder32Big,
	)
}

// errorDetail names a ScreenCaptureKit failure without logging anything the
// screen contains: just the domain, the code and the one meaning this path
// branches on.
func (a *sck) errorDetail(failure uintptr) (errs.Code, string) {
	code := a.objc.msgSendInt64(failure, a.sel.code)

	domain := ""
	if value := a.objc.msgSendID(failure, a.sel.domain); value != 0 {
		domain = cString(a.objc.msgSendCStr(value, a.sel.utf8String))
	}

	if domain == "" {
		domain = "no error domain"
	}

	switch code {
	case sckErrorUserDeclined, sckErrorMissingEntitlements:
		return errs.CodeAccessibilityDenied, fmt.Sprintf("%s (%d)", domain, code)
	}

	return errs.CodeInternal, fmt.Sprintf("%s (%d)", domain, code)
}

// cString copies a NUL-terminated C string into Go memory. Error domains are
// short; the cap keeps a malformed one from scanning the address space.
func cString(value *byte) string {
	if value == nil {
		return ""
	}

	const maxLength = 512

	buffer := make([]byte, 0, 32)
	for index := 0; index < maxLength; index++ {
		char := *(*byte)(unsafe.Add(unsafe.Pointer(value), index))
		if char == 0 {
			break
		}

		buffer = append(buffer, char)
	}

	return string(buffer)
}

// The pending-call slot. One completion handler may be outstanding at a time,
// which the capture gate enforces, so a single channel identifies the call the
// next callback belongs to.
//
// A capture that gives up waiting does not leave its channel in place: it marks
// the slot stale instead, and no new capture starts until the abandoned
// callback has landed. That keeps a late callback from being mistaken for a
// later call's result, which would hand a CGImage to code expecting shareable
// content. If the abandoned callback never arrives, ScreenCaptureKit stays off
// for the life of the process and every capture uses screencapture.
var (
	sckCaptureGate  = make(chan struct{}, 1)
	sckPendingMu    sync.Mutex
	sckPendingCh    chan sckEvent
	sckPendingStale bool
)

// sckEnterCapture takes the capture gate, so at most one call is in flight. It
// waits no longer than the caller's context allows.
func sckEnterCapture(ctx context.Context, deadline time.Time) error {
	select {
	case sckCaptureGate <- struct{}{}:
		return nil
	default:
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	select {
	case sckCaptureGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	case <-timer.C:
		return errs.New(errs.CodeTimeout, "another ScreenCaptureKit capture is still running")
	}
}

// sckLeaveCapture releases the gate.
func sckLeaveCapture() {
	<-sckCaptureGate
}

// sckInstallPending claims the pending-call slot for one call and returns the
// channel its completion will arrive on.
func sckInstallPending() (chan sckEvent, error) {
	sckPendingMu.Lock()
	defer sckPendingMu.Unlock()

	if sckPendingStale {
		return nil, errs.New(
			errs.CodeTimeout,
			"an earlier ScreenCaptureKit call has not reported back yet",
		)
	}

	if sckPendingCh != nil {
		return nil, errs.New(errs.CodeInternal, "a ScreenCaptureKit capture is already in flight")
	}

	channel := make(chan sckEvent, 1)
	sckPendingCh = channel

	return channel, nil
}

// sckAbandonPending gives up on a call. The channel is dropped and the slot is
// marked stale so the next call waits for the abandoned completion instead of
// accepting it as its own.
func sckAbandonPending(channel chan sckEvent) {
	sckPendingMu.Lock()
	defer sckPendingMu.Unlock()

	if sckPendingCh == channel {
		sckPendingCh = nil
		sckPendingStale = true
	}
}

// await waits for one call's completion, the caller's cancellation, or the
// deadline, in that order of likelihood.
func (a *sck) await(ctx context.Context, events chan sckEvent, stage sckStage, deadline time.Time) (sckEvent, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	select {
	case event := <-events:
		if event.stage == stage {
			return event, nil
		}

		// A completion for another call is not this call's result; waiting for
		// a second one would only burn the deadline.
		a.discard(event)

		return sckEvent{}, errs.New(errs.CodeInternal, "a ScreenCaptureKit completion for another call arrived")
	case <-ctx.Done():
		a.abandon(events)

		return sckEvent{}, errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	case <-timer.C:
		a.abandon(events)

		return sckEvent{}, errs.New(
			errs.CodeTimeout,
			"ScreenCaptureKit did not answer before the capture deadline",
		)
	}
}

// abandon gives up on a call and releases a result that arrived in the same
// breath, so a timeout cannot strand a retained CGImage in an unread channel.
func (a *sck) abandon(channel chan sckEvent) {
	sckAbandonPending(channel)

	select {
	case event := <-channel:
		a.discard(event)
	default:
	}
}

// deliver is the completion handler's body. It runs on ScreenCaptureKit's
// dispatch queue, not on a Go goroutine, so it only retains what it was handed
// and hands it to the waiting call: no teardown, no logging, and nothing that
// can panic.
func (a *sck) deliver(stage sckStage, object, failure uintptr) {
	if object != 0 {
		if stage == sckStageImage {
			object = a.cg.imageRetain(object)
		} else if retained := a.objc.msgSendID(object, a.sel.retain); retained != 0 {
			object = retained
		}
	}

	if failure != 0 {
		if retained := a.objc.msgSendID(failure, a.sel.retain); retained != 0 {
			failure = retained
		}
	}

	// The slot is read, sent to and cleared under one lock so a capture giving
	// up at the same moment either sees the result arrive or leaves the slot
	// stale for this callback to clear. Splitting those steps would let a
	// timeout and a delivery cross and strand the retained object in a channel
	// nobody reads.
	sckPendingMu.Lock()
	channel := sckPendingCh
	sckPendingCh = nil
	stale := sckPendingStale
	sckPendingStale = false

	event := sckEvent{stage: stage, object: object, failure: failure}
	delivered := false

	if !stale && channel != nil {
		select {
		case channel <- event:
			delivered = true
		default:
		}
	}
	sckPendingMu.Unlock()

	if !delivered {
		a.discard(event)
	}
}

// discard releases an event nobody is going to look at.
func (a *sck) discard(event sckEvent) {
	if event.failure != 0 {
		a.objc.msgSendVoid(event.failure, a.sel.release)
	}

	switch {
	case event.object == 0:
	case event.stage == sckStageImage:
		a.cg.imageRelease(event.object)
	default:
		a.objc.msgSendVoid(event.object, a.sel.release)
	}
}
