//go:build windows

package windows

import (
	"errors"
	"image"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file is the Win32 half of the backend. It answers the questions the UIA
// tree needs about the desktop around it: which window is in front, what its
// visible frame is, which process owns it, and how big the virtual screen is.
// It also makes the process per-monitor DPI aware, so the rectangles UIA
// reports are in the same physical pixel space as the coordinates input
// injection addresses. Everything is bound through stdlib syscall, which keeps
// the package CGo-free.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	procGetDesktopWindow       = user32.NewProc("GetDesktopWindow")
	procIsWindow               = user32.NewProc("IsWindow")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcess = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW         = user32.NewProc("GetWindowTextW")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procEnumWindows            = user32.NewProc("EnumWindows")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")

	// procSetProcessDpiAwarenessContext is missing before Windows 10 1703, so
	// it is resolved with Find before it is called. The other entry points
	// have existed since Windows 7 or earlier and are called directly.
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procDwmGetWindowAttribute         = dwmapi.NewProc("DwmGetWindowAttribute")

	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

// Window and metric constants. They are spelled out rather than imported
// because stdlib syscall does not carry the user32 constants.
const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79

	// dwmExtendedFrameBounds is DWMWA_EXTENDED_FRAME_BOUNDS: the frame a user
	// sees, without the invisible resize borders GetWindowRect counts.
	dwmExtendedFrameBounds = 9

	// dpiAwarenessPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
	// which is (HANDLE)-4: every monitor reports its own physical pixels.
	dpiAwarenessPerMonitorV2 = ^uintptr(3)

	// processQueryLimitedInformation is the narrowest right that still allows
	// QueryFullProcessImageName, so the backend never asks for more access to
	// another process than reading its image path needs.
	processQueryLimitedInformation = 0x1000
)

// win32Rect mirrors the Win32 RECT the window and element queries fill.
type win32Rect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

// rect returns the rectangle as an image.Rectangle, keeping the sign of a
// monitor left of or above the primary.
func (r win32Rect) rect() image.Rectangle {
	return image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom))
}

// win32Point mirrors the Win32 POINT the cursor and hit-test queries fill.
type win32Point struct {
	x int32
	y int32
}

// dpiOnce makes the process per-monitor-v2 DPI aware exactly once. Setting the
// awareness twice is harmless but pointless, and the call is not free.
var dpiOnce sync.Once

// ensurePerMonitorV2 marks the process per-monitor-v2 DPI aware, so that UIA
// reports rectangles and Win32 reports the cursor in physical pixels.
//
// It is safe to call repeatedly. A failure is ignored on purpose: on Windows
// older than 10 1703 there is no such context, and the process then keeps its
// manifest or the system default awareness. Coordinates stay consistent with
// each other in that case, just not per monitor.
func ensurePerMonitorV2() {
	dpiOnce.Do(func() {
		if err := procSetProcessDpiAwarenessContext.Find(); err != nil {
			return
		}

		procSetProcessDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2)
	})
}

// virtualScreenBounds returns the whole virtual desktop in physical pixels:
// every monitor's rectangle unioned, with a negative origin when a monitor
// sits left of or above the primary.
func virtualScreenBounds() image.Rectangle {
	left := sysMetric(smXVirtualScreen)
	top := sysMetric(smYVirtualScreen)
	width := sysMetric(smCxVirtualScreen)
	height := sysMetric(smCyVirtualScreen)

	return image.Rect(left, top, left+width, top+height)
}

// sysMetric reads one GetSystemMetrics value in the process's DPI-aware units.
// The value is sign-extended from 32 bits because the virtual screen origin is
// negative on a multi-monitor desktop with a display left of the primary.
func sysMetric(index uintptr) int {
	value, _, _ := procGetSystemMetrics.Call(index)

	return int(int32(value))
}

// desktopWindow is the handle that stands for the desktop itself, which is
// never an application window.
func desktopWindow() uintptr {
	hwnd, _, _ := procGetDesktopWindow.Call()

	return hwnd
}

// foregroundWindow returns the window the user is working in.
//
// A null handle, a stale handle and the desktop all mean "no application has
// focus", and each is reported as CodeElementNotFound so a caller can treat
// it as "nothing to scan" rather than as a failure.
func foregroundWindow() (uintptr, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0, errs.New(errs.CodeElementNotFound, "no foreground window")
	}

	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return 0, errs.New(errs.CodeElementNotFound, "foreground handle is not a window")
	}

	if hwnd == desktopWindow() {
		return 0, errs.New(errs.CodeElementNotFound, "the desktop is focused")
	}

	return hwnd, nil
}

// isWindowVisible reports whether hwnd and every one of its parents carry
// WS_VISIBLE.
func isWindowVisible(hwnd uintptr) bool {
	visible, _, _ := procIsWindowVisible.Call(hwnd)

	return visible != 0
}

// windowProcessID returns the process that owns hwnd.
func windowProcessID(hwnd uintptr) (uint32, error) {
	var pid uint32

	thread, _, callErr := procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if thread == 0 || pid == 0 {
		return 0, win32Failure("GetWindowThreadProcessId", callErr)
	}

	return pid, nil
}

// windowTitle returns the text in hwnd's title bar, or "" when it has none.
//
// GetWindowTextW on another process's window reads the cached title instead of
// sending WM_GETTEXT, so a hung application cannot block the caller here.
func windowTitle(hwnd uintptr) string {
	var buffer [512]uint16

	length, _, _ := procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if length == 0 {
		return ""
	}

	return syscall.UTF16ToString(buffer[:length])
}

// visibleWindowFrame returns the part of hwnd's frame a user sees, in physical
// pixels, clipped to the virtual screen. A window with nothing left on screen
// returns the empty rectangle.
//
// DwmGetWindowAttribute's extended frame bounds are asked first, because
// GetWindowRect counts the invisible resize borders DWM draws around a window
// (seven or eight pixels on a themed desktop on three sides). GetWindowRect is
// the answer where DWM cannot say, such as a window it does not manage or a
// desktop with composition switched off.
func visibleWindowFrame(hwnd uintptr) image.Rectangle {
	var bounds win32Rect

	ok, _, _ := procDwmGetWindowAttribute.Call(
		hwnd,
		dwmExtendedFrameBounds,
		uintptr(unsafe.Pointer(&bounds)),
		unsafe.Sizeof(bounds),
	)
	if ok != 0 && bounds.right > bounds.left && bounds.bottom > bounds.top {
		return bounds.rect().Intersect(virtualScreenBounds())
	}

	rect, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
	if rect == 0 {
		return image.Rectangle{}
	}

	return bounds.rect().Intersect(virtualScreenBounds())
}

// processImagePath returns the full path of the executable behind pid.
func processImagePath(pid uint32) (string, error) {
	handle, _, callErr := procOpenProcess.Call(
		processQueryLimitedInformation,
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return "", win32Failure("OpenProcess", callErr)
	}

	defer procCloseHandle.Call(handle)

	// The buffer is sized like MAX_PATH but the API takes the length, so a
	// longer path is reported as a failure rather than truncated.
	buffer := make([]uint16, 1024)

	size := uint32(len(buffer))

	ok, _, callErr := procQueryFullProcessImageNameW.Call(
		handle,
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ok == 0 {
		return "", win32Failure("QueryFullProcessImageName", callErr)
	}

	return syscall.UTF16ToString(buffer[:size]), nil
}

// processExeName returns the base name of the executable behind pid, such as
// "notepad.exe", or "" when the process cannot be opened. The empty result is
// deliberate: an executable name is metadata, and a window whose process
// refuses to be read is still a usable window.
func processExeName(pid uint32) string {
	path, err := processImagePath(pid)
	if err != nil {
		return ""
	}

	return filepath.Base(path)
}

// processWindow is a scan for the visible top-level windows of one process. A
// zero pid scans every process.
type processWindow struct {
	pid   uint32
	focus uintptr
	hwnds []uintptr
}

var (
	// windowScanMu serializes enumeration passes. It is held across the whole
	// EnumWindows call, which is what makes the single shared collector below
	// safe: Windows invokes WNDENUMPROC synchronously, on the goroutine that
	// made the call, before the call returns.
	//
	// It is a non-reentrant mutex held across a syscall, so nothing reached
	// from the callback may enumerate again. Nothing does: the callback only
	// reads window properties.
	windowScanMu sync.Mutex

	// windowScanCurrent is where collectWindow appends, installed by the pass
	// holding windowScanMu.
	windowScanCurrent *processWindow

	// windowScanProc is the WNDENUMPROC EnumWindows is handed, allocated on
	// first use and never again.
	//
	// Go's runtime keys registered callbacks on the function value and never
	// frees a slot, and the table is fixed at 2000 entries. Every distinct
	// closure passed to syscall.NewCallback would burn one permanently, and
	// window enumeration runs on every query, so a per-call closure would make
	// a long session a countdown to a runtime throw that cannot be recovered.
	windowScanProc = sync.OnceValue(func() uintptr {
		return syscall.NewCallback(collectWindow)
	})
)

// collectWindow is the WNDENUMPROC Windows calls once per top-level window. It
// appends the windows keepTopLevelWindow accepts.
//
// Returning 1 continues the enumeration, so one window whose properties cannot
// be read is dropped rather than costing the caller every window after it.
func collectWindow(hwnd, _ uintptr) uintptr {
	scan := windowScanCurrent
	if scan == nil {
		return 1
	}

	if keepTopLevelWindow(hwnd, scan) {
		scan.hwnds = append(scan.hwnds, hwnd)
	}

	return 1
}

// keepTopLevelWindow reports whether hwnd is a window a hint could land in.
//
// The desktop, hidden windows and helper windows are rejected. A window with
// no title is a helper or a surface parent rather than something a user picks,
// except for the window that currently has focus, which some applications
// leave untitled until they finish loading.
func keepTopLevelWindow(hwnd uintptr, scan *processWindow) bool {
	if hwnd == 0 || hwnd == desktopWindow() {
		return false
	}

	if !isWindowVisible(hwnd) {
		return false
	}

	if scan.pid != 0 {
		pid, err := windowProcessID(hwnd)
		if err != nil || pid != scan.pid {
			return false
		}
	}

	if windowTitle(hwnd) == "" && hwnd != scan.focus {
		return false
	}

	return true
}

// topLevelWindows runs one EnumWindows pass and returns the windows it kept.
func topLevelWindows(pid uint32) []uintptr {
	scan := &processWindow{pid: pid, focus: foregroundWindowOrZero()}

	windowScanMu.Lock()

	defer func() {
		windowScanCurrent = nil
		windowScanMu.Unlock()
	}()

	windowScanCurrent = scan

	procEnumWindows.Call(windowScanProc(), 0)

	return scan.hwnds
}

// foregroundWindowOrZero returns the foreground handle, or 0 when no
// application has focus. It exists so enumeration does not have to treat "no
// foreground window" as an error.
func foregroundWindowOrZero() uintptr {
	hwnd, err := foregroundWindow()
	if err != nil {
		return 0
	}

	return hwnd
}

// processWindows returns the visible top-level windows of one process.
func processWindows(pid uint32) []uintptr {
	return topLevelWindows(pid)
}

// allWindows returns every visible, titled top-level window on the desktop.
func allWindows() []uintptr {
	return topLevelWindows(0)
}

// lookupApplication finds the running application whose executable base name
// matches bundleID, which is how a Windows "bundle id" is addressed.
//
// The comparison is case-insensitive and accepts either a bare executable name
// ("notepad.exe") or a full path, so a caller that has one form does not have
// to know which the platform wants.
func lookupApplication(bundleID string) (uint32, string, error) {
	trimmed := strings.TrimSpace(bundleID)
	if trimmed == "" {
		return 0, "", errs.New(errs.CodeInvalidInput, "bundle id is empty")
	}

	wanted := strings.ToLower(filepath.Base(trimmed))

	for _, hwnd := range allWindows() {
		pid, err := windowProcessID(hwnd)
		if err != nil {
			continue
		}

		exe := processExeName(pid)
		if exe != "" && strings.ToLower(exe) == wanted {
			return pid, exe, nil
		}
	}

	return 0, "", errs.Newf(
		errs.CodeElementNotFound,
		"no application with executable name %q",
		bundleID,
	)
}

// win32Error drops the "the operation completed successfully" errno the
// syscall layer reports for calls that fail without setting one, so an error
// cause never reads as a success message.
func win32Error(callErr error) error {
	if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
		return nil
	}

	return callErr
}

// win32Failure builds the coded error for a failed Win32 call, naming the call
// so the failure is diagnosable without a stack trace.
func win32Failure(operation string, callErr error) error {
	if cause := win32Error(callErr); cause != nil {
		return errs.Wrapf(cause, errs.CodeAccessibilityFailed, "%s failed", operation)
	}

	return errs.Newf(errs.CodeAccessibilityFailed, "%s failed", operation)
}
