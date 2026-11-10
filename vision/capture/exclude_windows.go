//go:build windows

package capture

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Window capture exclusion on Windows.
//
// Windows 10 2004 added WDA_EXCLUDEFROMCAPTURE: a window marked with it is
// omitted from the DWM-composited image that BitBlt and the Windows.Graphics
// capture APIs see, so a hint overlay the caller draws does not appear in the
// frame it then detects over. Neru marks its own overlay windows this way; this
// package exposes both the per-window call and the "all of this process's
// windows" convenience.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetCurrentProcessID      = kernel32.NewProc("GetCurrentProcessId")
)

// wdaExcludeFromCapture is WDA_EXCLUDEFROMCAPTURE from winuser.h.
const wdaExcludeFromCapture = 0x00000011

// ExclusionError reports one window whose affinity could not be set, so a
// caller can log which window stayed visible without logging anything it
// contains.
type ExclusionError struct {
	HWND uintptr
	Err  error
}

func (e *ExclusionError) Error() string {
	return "capture: window exclusion failed for handle " + itoaHex(e.HWND) + ": " + e.Err.Error()
}

func (e *ExclusionError) Unwrap() error { return e.Err }

// itoaHex formats a handle without pulling strconv into the error path.
func itoaHex(value uintptr) string {
	const digits = "0123456789abcdef"

	if value == 0 {
		return "0"
	}

	buffer := make([]byte, 0, 2*unsafe.Sizeof(value))
	for value > 0 {
		buffer = append([]byte{digits[value&0xf]}, buffer...)
		value >>= 4
	}

	return string(buffer)
}

// ExcludeWindow marks one top-level window so Windows' composited capture
// leaves it out.
//
// The window must belong to the calling process, or the call fails with
// CodeAccessibilityDenied (Windows refuses cross-process affinity). A Windows
// build older than 10 2004 rejects the flag with CodeNotSupported, and the
// message says so rather than leaving a caller wondering why its overlay shows
// up in its own screenshot.
func ExcludeWindow(hwnd uintptr) error {
	if hwnd == 0 {
		return errs.New(errs.CodeInvalidInput, "a window handle is required")
	}

	result, _, callErr := procSetWindowDisplayAffinity.Call(hwnd, wdaExcludeFromCapture)
	if result != 0 {
		return nil
	}

	return errs.Newf(
		errs.CodeNotSupported,
		"SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) failed for window %s: %v (Windows 10 2004 or newer is required, and the window must belong to this process)",
		itoaHex(hwnd),
		callErr,
	)
}

// ownWindows collects the top-level windows of this process.
//
// EnumWindows calls back once per window; the callback is allocated once and
// guarded by a mutex, because syscall.NewCallback slots are never released and
// creating one per call would leak them.
var (
	enumMu     sync.Mutex
	enumPID    uint32
	enumResult []uintptr
)

var enumCallback = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()

	if enumPID == 0 {
		return 1
	}

	var owner uint32

	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	if owner == enumPID {
		enumResult = append(enumResult, hwnd)
	}

	// Continue enumeration.
	return 1
})

// ExcludeOwnWindows marks every top-level window of this process so the
// package's own capture leaves them out.
//
// It is the call a hint overlay makes once it has created its windows. Windows
// with no window of its own returns an empty error, so a headless or console
// process can call it unconditionally. A failure for one window does not stop
// the others; the returned error lists every window that stayed capturable.
func ExcludeOwnWindows() error {
	pid, _, _ := procGetCurrentProcessID.Call()
	if pid == 0 {
		return errs.New(errs.CodeInternal, "GetCurrentProcessId returned zero")
	}

	enumMu.Lock()
	enumPID = uint32(pid)
	enumResult = enumResult[:0]
	enumMu.Unlock()

	result, _, callErr := procEnumWindows.Call(enumCallback, 0)
	if result == 0 {
		enumMu.Lock()
		enumPID = 0
		enumMu.Unlock()

		return errs.Wrap(callErr, errs.CodeInternal, "EnumWindows failed")
	}

	enumMu.Lock()
	handles := append([]uintptr(nil), enumResult...)
	enumPID = 0
	enumResult = enumResult[:0]
	enumMu.Unlock()

	var failures []error

	for _, hwnd := range handles {
		if err := ExcludeWindow(hwnd); err != nil {
			failures = append(failures, &ExclusionError{HWND: hwnd, Err: err})
		}
	}

	return errors.Join(failures...)
}
