//go:build windows

package capture

import (
	"context"
	"image"
	"math"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file captures the screen through GDI only: BitBlt off the desktop DC
// and GetDIBits into an RGBA strip. It talks to user32, gdi32 and shcore
// through stdlib syscall, so the library stays pure Go with no CGo and no
// third-party dependency. The process is made per-monitor-v2 DPI aware once,
// so sizes are physical pixels and the scale returned with each shot is real
// pixels per logical pixel.

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	shcore = syscall.NewLazyDLL("shcore.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo                = user32.NewProc("GetMonitorInfoW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procGetDeviceCaps          = gdi32.NewProc("GetDeviceCaps")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")

	procGetDpiForMonitor = shcore.NewProc("GetDpiForMonitor")
)

// Virtual-screen extents for GetSystemMetrics when a monitor resolves empty,
// and the physical-pixel size of the whole virtual desktop.
const (
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is (HANDLE)-4; in Go that is
// ^uintptr(3). Sizes then come back in physical pixels tied to each monitor.
const (
	dpiAwarenessPerMonitorV2 = ^uintptr(3)
	monitorDefaultToNearest  = 2
	srcCopy                  = 0x00CC0020
	dibRGBColors             = 0
	logPixelsX               = 88
	effectiveDPIType         = 0
	mdtNone                  = 0xFFFFFFFF
)

type point struct{ X, Y int32 }

type rect struct{ left, top, right, bottom int32 }

type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
	bmiColors [3]uint32
}

// ensurePerMonitorV2 marks the process DPI aware so that monitors report
// physical pixel sizes. It is safe to call repeatedly; once set the call is a
// no-op. Errors are ignored because falling back to the system-default
// awareness keeps the capture working at 96 DPI.
func ensurePerMonitorV2() {
	procSetProcessDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2)
}

// dpiScale returns the monitor's scale as frame pixels per logical pixel,
// preferring the Win10-get DPI API and falling back to the DC's.
func dpiScale(hmon uintptr) float64 {
	if hmon != 0 && hmon != mdtNone {
		var dx, dy uint32
		// GetDpiForMonitor is called with shcore's MDT_EFFECTIVE_DPI.
		r, _, _ := procGetDpiForMonitor.Call(
			hmon, uintptr(effectiveDPIType),
			uintptr(unsafe.Pointer(&dx)),
			uintptr(unsafe.Pointer(&dy)),
		)
		if r == 0 && dx > 0 {
			return float64(dx) / 96.0
		}
	}

	ensurePerMonitorV2()
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 1
	}
	defer procReleaseDC.Call(0, hdc)

	dpi, _, _ := procGetDeviceCaps.Call(hdc, logPixelsX)
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96.0
}

// dpiScaleAt returns the monitor-under-point's scale. The point is a logical
// pixel; it is first turned into a physical one through the cursor monitor's
// scale, which is exact on the active display and approximate on a mixed-DPI
// multi-monitor setup. That approximation is noted so a caller on such a setup
// captures by monitor, not by guessed scale.
func dpiScaleAt(pt image.Point) float64 {
	monitors := allMonitors()
	if len(monitors) == 0 {
		return 1
	}

	// Prefer the monitor whose logical bounds contain the point.
	for _, m := range monitors {
		if pointInRect(pt, m.logical) {
			return m.scale
		}
	}
	// Fall back to the nearest, taken as the one under the cursor.
	return monitors[0].scale
}

type screen struct {
	hmon    uintptr
	phys    image.Rectangle
	logical image.Rectangle
	scale   float64
}

// allMonitors returns the displays the capture can read. This build reads the
// active one, the display under the cursor; a region whose center anyway falls
// on it is measured with the same scale, which is exact on a single-display
// or single-DPI setup.
func allMonitors() []screen {
	ensurePerMonitorV2()
	return []screen{monitorAt(cursorMonitor())}
}

func monitorAt(hmon uintptr) screen {
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	scale := dpiScale(hmon)

	if hmon != 0 {
		if r, _, _ := procGetMonitorInfo.Call(
			hmon, uintptr(unsafe.Pointer(&mi)),
		); r == 0 {
			hmon = 0
		}
	}

	phys := rectImage(mi.rcMonitor)
	if phys.Empty() {
		// No monitor info: fall back to the virtual screen in physical px.
		phys = image.Rect(0, 0, sysMetric(smCXVirtualScreen), sysMetric(smCYVirtualScreen))
		scale = dpiScale(0)
	}

	return screen{
		hmon: hmon,
		// phys.Min keeps its own sign: it is negative for a monitor left or
		// above the primary, and dividing it back into logical global pixels
		// must preserve that.
		phys:    phys,
		scale:   scale,
		logical: divRect(phys, scale),
	}
}

// captureScreen captures the whole display under the cursor. Origin is that
// display's logical top-left, Region its logical bounds, and Scale its own, so
// the shot is exactly the active screen.
func captureScreen(ctx context.Context) (Shot, error) {
	if err := ctxCancelled(ctx); err != nil {
		return Shot{}, err
	}

	ensurePerMonitorV2()
	mon := monitorAt(cursorMonitor())

	img, err := capturePhysical(ctx, mon.phys)
	if err != nil {
		return Shot{}, err
	}

	return Shot{
		Img:    img,
		Origin: divPoint(mon.phys.Min, mon.scale),
		Scale:  mon.scale,
		Region: mon.logical,
	}, nil
}

// captureRect captures exactly the given logical region. Origin is the region's
// top-left corner, Region is the region itself, so a frame built from the shot
// sits where the caller asked. The region is measured with the scale of the
// monitor that contains its center.
func captureRect(ctx context.Context, region image.Rectangle) (Shot, error) {
	if err := ctxCancelled(ctx); err != nil {
		return Shot{}, err
	}

	region = region.Canon()
	if region.Empty() {
		return Shot{}, errs.Newf(
			errs.CodeInvalidInput,
			"region capture needs a region to capture; %v is empty",
			region,
		)
	}

	scale := dpiScaleAt(rectCenter(region))
	phys := scaleRect(region, scale)
	img, err := capturePhysical(ctx, phys)
	if err != nil {
		return Shot{}, err
	}

	return Shot{
		Img:    img,
		Origin: region.Min,
		Scale:  scale,
		Region: region,
	}, nil
}

// capturePhysical BitBlts the given physical pixel rect off the desktop into
// an RGBA image. The rect is in the process's now-default physical pixels.
func capturePhysical(ctx context.Context, phys image.Rectangle) (*image.RGBA, error) {
	if err := ctxCancelled(ctx); err != nil {
		return nil, err
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, errs.New(errs.CodeInternal, "GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, errs.New(errs.CodeInternal, "CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	w, h := phys.Dx(), phys.Dy()
	bmp, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, errs.New(errs.CodeInternal, "CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	if old, _, _ := procSelectObject.Call(memDC, bmp); old == 0 {
		return nil, errs.New(errs.CodeInternal, "SelectObject failed")
	}

	// Source coordinates are the rect's global physical position; a monitor
	// left/above the primary has a negative origin, which BitBlt accepts.
	ok, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(w), uintptr(h),
		screenDC, uintptr(phys.Min.X), uintptr(phys.Min.Y), uintptr(srcCopy),
	)
	if ok == 0 {
		return nil, errs.New(errs.CodeInternal, "BitBlt failed")
	}

	// GetDIBits with a negative height reads the strip top-down.
	info := bitmapInfo{}
	info.bmiHeader.biSize = uint32(unsafe.Sizeof(info.bmiHeader))
	info.bmiHeader.biWidth = int32(w)
	info.bmiHeader.biHeight = -int32(h)
	info.bmiHeader.biPlanes = 1
	info.bmiHeader.biBitCount = 32
	info.bmiHeader.biCompression = 0

	buf := make([]byte, w*h*4)
	got, _, _ := procGetDIBits.Call(
		memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&info)),
		uintptr(dibRGBColors),
	)
	if got == 0 {
		return nil, errs.New(errs.CodeInternal, "GetDIBits failed")
	}

	return bgraToRGBA(buf, w, h), nil
}

// bgraToRGBA walks the BGRA strip GDI hands back and writes it into an RGBA
// image (each pixel B,G,R,A-source to R,G,B,A-image).
func bgraToRGBA(src []byte, w, h int) *image.RGBA {
	// Fast path when the source is already tightly the same layout on this
	// loop: on little-endian a BGRA word is R in the top byte when read big,
	// so the byte swap below is the one cost we pay.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+4 <= len(src); i += 4 {
		d := img.Pix[i:]
		d[0], d[1], d[2], d[3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	return img
}

// cursorMonitor returns the physical rect under the cursor's virtual screen
// handle... on a miss it returns zero and the caller reads primary.
func cursorMonitor() uintptr {
	var pt point
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); r == 0 {
		return 0
	}
	hmon, _, _ := procMonitorFromPoint.Call(
		uintptr(pt.X), uintptr(pt.Y), uintptr(monitorDefaultToNearest),
	)
	return hmon
}

func ctxCancelled(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	default:
		return nil
	}
}

func rectImage(r rect) image.Rectangle {
	return image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom))
}

func scaleRect(r image.Rectangle, s float64) image.Rectangle {
	return image.Rect(
		rint(math.Floor(float64(r.Min.X)*s)),
		rint(math.Floor(float64(r.Min.Y)*s)),
		rint(math.Ceil(float64(r.Max.X)*s)),
		rint(math.Ceil(float64(r.Max.Y)*s)),
	)
}

func divRect(r image.Rectangle, s float64) image.Rectangle {
	return image.Rect(
		rint(math.Floor(float64(r.Min.X)/s)),
		rint(math.Floor(float64(r.Min.Y)/s)),
		rint(math.Ceil(float64(r.Max.X)/s)),
		rint(math.Ceil(float64(r.Max.Y)/s)),
	)
}

func divPoint(p image.Point, s float64) image.Point {
	return image.Pt(rint(math.Floor(float64(p.X)/s)), rint(math.Floor(float64(p.Y)/s)))
}

func rint(f float64) int {
	return int(f)
}

// pointInRect reports whether pt falls inside r, treating the rectangle as
// the integer range its bounds span.
func pointInRect(pt image.Point, r image.Rectangle) bool {
	return pt.X >= r.Min.X && pt.X < r.Max.X &&
		pt.Y >= r.Min.Y && pt.Y < r.Max.Y
}

// rectCenter is the midpoint of a rectangle in integer pixel coordinates.
func rectCenter(r image.Rectangle) image.Point {
	return image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2)
}

// sysMetric reads a GetSystemMetrics metric in the process's DPI-aware units.
func sysMetric(index uintptr) int {
	v, _, _ := procGetSystemMetrics.Call(index)
	return int(v)
}
