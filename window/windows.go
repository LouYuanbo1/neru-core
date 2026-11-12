//go:build windows

package window

import (
	"context"
	"image"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file is the Win32 backend. Window discovery is one EnumWindows pass,
// which is what makes the result the real top-level window list rather than
// "one window per process": a browser with three windows or an editor with two
// documents appears three or two times, so a caller can pick the right one.
//
// Placement is a SetWindowPos, preceded by SW_RESTORE so a maximized or iconic
// window can take a new frame. SetWindowPos addresses GetWindowRect while this
// package reports DWM's visible frame, so the invisible resize borders DWM draws
// around a window are measured and compensated before the call: the frame the
// caller names is the frame the user gets.
//
// Everything is bound through the standard library's syscall package, which
// keeps the package CGo-free, and the process is made per-monitor-v2 DPI aware
// so the rectangles this backend reports are in the same physical pixels as the
// coordinates the accessibility backends inject.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procIsWindow                 = user32.NewProc("IsWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowLongW           = user32.NewProc("GetWindowLongW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetDesktopWindow         = user32.NewProc("GetDesktopWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procSetFocus                 = user32.NewProc("SetFocus")
	procPeekMessageW             = user32.NewProc("PeekMessageW")

	procGetCurrentThreadID         = kernel32.NewProc("GetCurrentThreadId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")

	// procSetProcessDpiAwarenessContext and the DWM calls are version-gated
	// (Windows 10 1703 and Vista respectively), so they are resolved before
	// use and a missing one costs only the behavior it provides.
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procDwmGetWindowAttribute         = dwmapi.NewProc("DwmGetWindowAttribute")
)

// Win32 constants. They are spelled out because the standard library's syscall
// package carries no user32 constants.
const (
	// gwlExStyle is GWL_EXSTYLE, the index GetWindowLongW reads a window's
	// extended style at (-20 as a 32-bit signed value).
	gwlExStyle = 0xFFFFFFEC

	// wsExToolWindow marks a helper window; wsExAppWindow is an application's
	// explicit request to appear in the taskbar. A window with both is a real
	// window, which is why the pair is tested together.
	wsExToolWindow = 0x00000080
	wsExAppWindow  = 0x00040000

	// dwmExtendedFrameBounds is DWMWA_EXTENDED_FRAME_BOUNDS: the frame the
	// user sees, without the invisible resize borders GetWindowRect counts.
	dwmExtendedFrameBounds = 9

	// dwmCloaked is DWMWA_CLOAKED. Windows 10 keeps a UWP application's
	// windows around while they are not on screen and marks them cloaked
	// instead of hidden, so IsWindowVisible alone would list ghost windows.
	dwmCloaked = 14

	swShow    = 5
	swRestore = 9

	// SetWindowPos flags. HWND_TOP (a zero insert-after handle) puts the window
	// at the top of the non-topmost stack, which is the raise the placement
	// wants; SWP_NOACTIVATE leaves the foreground handoff to requestForeground,
	// and SWP_NOOWNERZORDER keeps a dialog from being moved relative to its
	// owner.
	swpNoActivate    = 0x0010
	swpNoOwnerZOrder = 0x0200

	// frameSettleTimeout bounds how long SetBounds waits for DWM to report the
	// frame it just moved. SetWindowPos is synchronous, but the extended frame
	// bounds DWM answers with are computed from the window's current frame and
	// can lag one compositor pass.
	frameSettleTimeout = 300 * time.Millisecond
	frameSettlePoll    = 10 * time.Millisecond

	// dpiAwarenessPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
	// which is (HANDLE)-4.
	dpiAwarenessPerMonitorV2 = ^uintptr(3)

	// processQueryLimitedInformation is the narrowest right that still allows
	// QueryFullProcessImageName, so reading another application's executable
	// name never asks for more access than that.
	processQueryLimitedInformation = 0x1000

	// pmNoRemove is PM_NOREMOVE: PeekMessage looks at the queue without taking
	// the message, which is enough to make Windows create the queue.
	pmNoRemove = 0

	// maxWindowTitle bounds the buffer a title read allocates. GetWindowTextW
	// truncates rather than failing, and a real title is far shorter; the cap
	// keeps a hostile window from asking for an enormous allocation.
	maxWindowTitle = 4096

	// foregroundTimeout is how long Activate waits for the window manager to
	// hand over the foreground. Windows moves the foreground synchronously in
	// the common case; the wait only covers a shell that is busy.
	foregroundTimeout = 500 * time.Millisecond
	foregroundPoll    = 10 * time.Millisecond
)

// manager is the Win32 window backend. It holds no native resource between
// calls — every query opens and closes its own handles — so the only state is
// the configuration and the closed flag.
type manager struct {
	includeUntitled bool

	mu     sync.Mutex
	closed bool
}

var _ Manager = (*manager)(nil)

// New returns the Win32 window backend.
//
// It makes the process per-monitor-v2 DPI aware before anything else, so the
// frames this backend reports and the coordinates the rest of this module
// reports are both physical pixels. The call cannot fail today; the error is
// part of the contract so a future backend can report a missing system
// component without changing every caller.
func New(opts Options) (Manager, error) {
	ensurePerMonitorV2()

	return &manager{includeUntitled: opts.IncludeUntitled}, nil
}

// List returns every visible top-level window, front to back.
func (m *manager) List(ctx context.Context) ([]Info, error) {
	if err := m.ensureOpen(); err != nil {
		return nil, err
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	ensurePerMonitorV2()

	windows, err := enumerateWindows(m.includeUntitled)
	if err != nil {
		return nil, err
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	return windows, nil
}

// Frontmost returns the foreground window.
func (m *manager) Frontmost(ctx context.Context) (Info, error) {
	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	hwnd := foregroundWindowOrZero()
	if hwnd == 0 {
		return Info{}, errs.New(errs.CodeElementNotFound, "no application window has focus")
	}

	// The foreground window is kept even when it has no title yet: an
	// application that is still loading is still the window the user is in.
	info, ok := readWindow(hwnd, hwnd, true)
	if !ok {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the foreground handle is not a top-level application window",
		)
	}

	info.Focused = true

	return info, nil
}

// Activate brings the window target selects to the foreground and verifies it.
func (m *manager) Activate(ctx context.Context, target Target) (Info, error) {
	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := validateTarget(target); err != nil {
		return Info{}, err
	}

	// Resolving through a fresh List is what makes a repeated call work after
	// the target window has been recreated: the caller's target describes a
	// window, not a handle that has to still be alive.
	windows, err := m.List(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := Find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	hwnd, ok := parseWindowHandle(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not a Win32 window handle",
			selected.ID,
		)
	}

	showWindow(hwnd, selected.Minimized)
	requestForeground(hwnd)

	deadline := time.Now().Add(foregroundTimeout)

	for foregroundWindowOrZero() != hwnd {
		if err := contextError(ctx); err != nil {
			return Info{}, err
		}

		if time.Now().After(deadline) {
			return Info{}, errs.Newf(
				errs.CodeAccessibilityFailed,
				"could not bring %q (%s) to the foreground; %q is still in front",
				selected.Title,
				selected.Application,
				foregroundTitle(),
			)
		}

		time.Sleep(foregroundPoll)
	}

	activated, ok := readWindow(hwnd, hwnd, true)
	if !ok {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared while it was being activated",
		)
	}

	activated.Focused = true

	return activated, nil
}

// SetBounds restores the window, moves and resizes it, and brings it to the
// front.
func (m *manager) SetBounds(
	ctx context.Context,
	target Target,
	bounds image.Rectangle,
) (Info, error) {
	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := validateTarget(target); err != nil {
		return Info{}, err
	}

	if err := validateBounds(bounds); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	windows, err := m.List(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := Find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	hwnd, ok := parseWindowHandle(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not a Win32 window handle",
			selected.ID,
		)
	}

	// SW_RESTORE un-minimizes and un-maximizes in one call, which is exactly
	// what a maximized or iconic window needs before SetWindowPos can give it a
	// new frame. On an ordinary window it changes nothing.
	restoreWindow(hwnd)

	// The frame to compare against is the one the window has after the restore:
	// a maximized window's pre-restore frame is not the frame the move changes.
	before := windowFrame(hwnd)

	if err := setWindowFrame(hwnd, bounds); err != nil {
		return Info{}, err
	}

	// Best effort by design: the rectangle is what the caller asked for, and a
	// foreground the platform refuses is reported through Info.Focused.
	requestForeground(hwnd)

	waitForFrame(ctx, hwnd, before, bounds)

	placed, ok := readWindow(hwnd, hwnd, true)
	if !ok {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared while it was being placed",
		)
	}

	return placed, nil
}

// Close marks the manager closed. There is no native resource to release.
func (m *manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true

	return nil
}

// ensureOpen reports the error a closed manager answers with.
func (m *manager) ensureOpen() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return errs.New(errs.CodeInternal, "window manager is closed")
	}

	return nil
}

// showWindow un-minimizes the window when it is minimized and otherwise asks
// for it to be shown, which is what makes a hidden-but-listed window visible
// before the foreground request.
func showWindow(hwnd uintptr, minimized bool) {
	command := uintptr(swShow)
	if minimized {
		command = swRestore
	}

	_, _, _ = procShowWindow.Call(hwnd, command)
}

// restoreWindow takes a window out of the minimized or maximized state. On an
// ordinary window it changes nothing.
func restoreWindow(hwnd uintptr) {
	_, _, _ = procShowWindow.Call(hwnd, swRestore)
}

// requestForeground asks Windows to make hwnd the foreground window.
//
// A plain SetForegroundWindow succeeds only when the calling process already
// owns the foreground; otherwise Windows' foreground lock refuses it and only
// flashes the taskbar button. Joining the current foreground thread's input
// queue for the duration of the call is the documented workaround: while the
// queues are attached, this thread is treated as the foreground thread, so the
// request is granted. AttachThreadInput applies to the calling thread, which is
// why the sequence is pinned to one OS thread.
func requestForeground(hwnd uintptr) {
	_, _, _ = procSetForegroundWindow.Call(hwnd)

	if foregroundWindowOrZero() == hwnd {
		return
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// A thread must own a message queue before AttachThreadInput can join it to
	// another thread's queue. Go's threads have none until something asks for
	// one, and PeekMessage is the cheapest way to create it without consuming a
	// message.
	var message [msgSize]byte

	_, _, _ = procPeekMessageW.Call(
		uintptr(unsafe.Pointer(&message[0])),
		0,
		0,
		0,
		pmNoRemove,
	)

	ourThread, _, _ := procGetCurrentThreadID.Call()

	var foregroundThread uintptr

	if foreground := foregroundWindowOrZero(); foreground != 0 {
		foregroundThread, _, _ = procGetWindowThreadProcessID.Call(foreground, 0)
	}

	attached := foregroundThread != 0 && foregroundThread != ourThread &&
		attachInputThreads(ourThread, foregroundThread)

	_, _, _ = procBringWindowToTop.Call(hwnd)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
	_, _, _ = procSetFocus.Call(hwnd)

	if attached {
		_, _, _ = procAttachThreadInput.Call(ourThread, foregroundThread, 0)
	}
}

// attachInputThreads joins one thread's input queue to another and reports
// whether Windows accepted the request.
func attachInputThreads(thread, attachTo uintptr) bool {
	ok, _, _ := procAttachThreadInput.Call(thread, attachTo, 1)

	return ok != 0
}

// enumeration collects one EnumWindows pass.
//
// The collector is installed in a package variable rather than passed as
// EnumWindows' lParam because a Go pointer cannot be handed to the callback
// safely when it points at memory the garbage collector moves or scans. The
// mutex serializes passes, and Windows invokes the callback synchronously, on
// the goroutine that made the call, before EnumWindows returns.
type enumeration struct {
	includeUntitled bool
	foreground      uintptr
	windows         []Info
}

var (
	enumerationMu      sync.Mutex
	enumerationCurrent *enumeration

	// enumerationProc is the WNDENUMPROC. Go's runtime never frees a callback
	// slot and the process has a fixed number of them, so it is allocated once
	// for the process rather than once per pass.
	enumerationProc = sync.OnceValue(func() uintptr {
		return syscall.NewCallback(collectWindow)
	})
)

// collectWindow is the WNDENUMPROC Windows calls once per top-level window. It
// keeps the windows keepWindow accepts and always returns 1, so one window that
// cannot be read never costs the caller the windows after it.
func collectWindow(hwnd, _ uintptr) uintptr {
	current := enumerationCurrent
	if current == nil {
		return 1
	}

	if info, ok := readWindow(hwnd, current.foreground, current.includeUntitled); ok {
		current.windows = append(current.windows, info)
	}

	return 1
}

// enumerateWindows runs one EnumWindows pass and returns what it kept.
func enumerateWindows(includeUntitled bool) ([]Info, error) {
	current := &enumeration{
		includeUntitled: includeUntitled,
		foreground:      foregroundWindowOrZero(),
	}

	enumerationMu.Lock()

	defer func() {
		enumerationCurrent = nil
		enumerationMu.Unlock()
	}()

	enumerationCurrent = current

	ok, _, callErr := procEnumWindows.Call(enumerationProc(), 0)
	if ok == 0 {
		// The callback always returns 1, so a zero result is a failed pass
		// rather than an early stop.
		return nil, win32Failure("EnumWindows", callErr)
	}

	return focusedFirst(current.windows), nil
}

// focusedFirst moves the focused window to the front of a front-to-back list.
//
// EnumWindows walks the stack in Z order, but an always-on-top utility window can
// sit above the focused one. The contract promises the focused window first, so a
// caller can take List()[0] as "where the user is" without scanning for the flag.
func focusedFirst(windows []Info) []Info {
	for index, info := range windows {
		if !info.Focused || index == 0 {
			continue
		}

		ordered := make([]Info, 0, len(windows))
		ordered = append(ordered, info)
		ordered = append(ordered, windows[:index]...)
		ordered = append(ordered, windows[index+1:]...)

		return ordered
	}

	return windows
}

// readWindow describes one top-level window, and reports false when the window
// is not one a caller should see: hidden, cloaked, a helper window, or
// untitled when untitled windows were not asked for.
func readWindow(hwnd, foreground uintptr, includeUntitled bool) (Info, bool) {
	if hwnd == 0 || hwnd == desktopWindow() {
		return Info{}, false
	}

	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		return Info{}, false
	}

	if windowCloaked(hwnd) {
		return Info{}, false
	}

	exStyle, _, _ := procGetWindowLongW.Call(hwnd, uintptr(gwlExStyle))
	if exStyle&wsExToolWindow != 0 && exStyle&wsExAppWindow == 0 {
		return Info{}, false
	}

	title := strings.TrimSpace(windowTitle(hwnd))
	if title == "" && !includeUntitled && hwnd != foreground {
		return Info{}, false
	}

	pid, ok := windowProcessID(hwnd)
	if !ok {
		return Info{}, false
	}

	minimized, _, _ := procIsIconic.Call(hwnd)

	info := Info{
		ID:          formatWindowHandle(hwnd),
		PID:         int(pid),
		Title:       title,
		Application: processExeName(pid),
		Minimized:   minimized != 0,
		Focused:     hwnd == foreground,
	}

	// A minimized window's frame is its pre-minimize rectangle, often parked
	// off screen; reporting it would be a lie about where the window is.
	if !info.Minimized {
		info.Bounds = windowFrame(hwnd)
	}

	return info, true
}

// windowCloaked reports whether DWM has cloaked hwnd. A system without DWM
// answers false, which keeps every window visible rather than hiding all of
// them behind a failed call.
func windowCloaked(hwnd uintptr) bool {
	var cloaked int32

	ok, _, _ := procDwmGetWindowAttribute.Call(
		hwnd,
		dwmCloaked,
		uintptr(unsafe.Pointer(&cloaked)),
		unsafe.Sizeof(cloaked),
	)

	return ok != 0 && cloaked != 0
}

// windowFrame returns the frame the user sees, in physical pixels.
//
// DWM's extended frame bounds are asked first because GetWindowRect counts the
// invisible resize borders DWM draws on a themed desktop. GetWindowRect is the
// answer where DWM cannot say, such as a window it does not manage or a desktop
// with composition switched off.
func windowFrame(hwnd uintptr) image.Rectangle {
	var bounds win32Rect

	ok, _, _ := procDwmGetWindowAttribute.Call(
		hwnd,
		dwmExtendedFrameBounds,
		uintptr(unsafe.Pointer(&bounds)),
		unsafe.Sizeof(bounds),
	)
	if ok != 0 && bounds.valid() {
		return bounds.rect()
	}

	rect, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
	if rect != 0 && bounds.valid() {
		return bounds.rect()
	}

	return image.Rectangle{}
}

// windowOuterRect returns the rectangle SetWindowPos sets: GetWindowRect, which
// on a themed desktop includes the invisible resize borders DWM draws around the
// window. windowFrame reports the visible frame instead.
func windowOuterRect(hwnd uintptr) image.Rectangle {
	var bounds win32Rect

	rect, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
	if rect == 0 || !bounds.valid() {
		return image.Rectangle{}
	}

	return bounds.rect()
}

// setWindowFrame gives hwnd the visible frame bounds.
//
// SetWindowPos addresses GetWindowRect, not the frame the user sees: on a themed
// desktop DWM shrinks the visible frame by the invisible resize borders on four
// sides (seven or eight pixels each). Calling SetWindowPos with bounds directly
// would therefore place the visible frame a few pixels inside the requested
// rectangle. The difference between the two rectangles measured right now is
// applied to the request so the frame the caller named is the frame they get.
// Where DWM has no frame, the two rectangles are identical and the offset is
// zero.
func setWindowFrame(hwnd uintptr, bounds image.Rectangle) error {
	outer := windowOuterRect(hwnd)
	frame := windowFrame(hwnd)

	left, top, right, bottom := 0, 0, 0, 0

	if !outer.Empty() && !frame.Empty() {
		left = frame.Min.X - outer.Min.X
		top = frame.Min.Y - outer.Min.Y
		right = frame.Max.X - outer.Max.X
		bottom = frame.Max.Y - outer.Max.Y
	}

	target := image.Rect(
		bounds.Min.X-left,
		bounds.Min.Y-top,
		bounds.Max.X-right,
		bounds.Max.Y-bottom,
	)

	ok, _, callErr := procSetWindowPos.Call(
		hwnd,
		0, // HWND_TOP: raise the window into the non-topmost stack.
		uintptr(target.Min.X),
		uintptr(target.Min.Y),
		uintptr(target.Dx()),
		uintptr(target.Dy()),
		uintptr(swpNoActivate|swpNoOwnerZOrder),
	)
	if ok == 0 {
		return win32Failure("SetWindowPos", callErr)
	}

	return nil
}

// waitForFrame waits until the visible frame has changed from before or reached
// the requested rectangle, and returns whatever the frame is by then.
//
// The wait only absorbs a compositor pass of lag in DWM's extended frame query:
// SetWindowPos itself is synchronous, so the frame is normally already right on
// the first read. A frame the platform clamped never matches the request, which
// is why the loop stops at the first change rather than insisting on equality.
func waitForFrame(ctx context.Context, hwnd uintptr, before, want image.Rectangle) image.Rectangle {
	if before == want {
		return before
	}

	deadline := time.Now().Add(frameSettleTimeout)

	for {
		frame := windowFrame(hwnd)
		if frame != before || contextError(ctx) != nil {
			return frame
		}

		if time.Now().After(deadline) {
			return frame
		}

		time.Sleep(frameSettlePoll)
	}
}

// windowTitle reads the text in hwnd's title bar.
//
// GetWindowTextW on another process's window reads the cached title instead of
// sending WM_GETTEXT, so a hung application cannot block the caller here.
func windowTitle(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}

	if length > maxWindowTitle {
		length = maxWindowTitle
	}

	buffer := make([]uint16, length+1)

	copied, _, _ := procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if copied == 0 {
		return ""
	}

	return syscall.UTF16ToString(buffer[:copied])
}

// windowProcessID returns the process that owns hwnd.
func windowProcessID(hwnd uintptr) (uint32, bool) {
	var pid uint32

	thread, _, _ := procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if thread == 0 || pid == 0 {
		return 0, false
	}

	return pid, true
}

// processExeName returns the base name of the executable behind pid, or "" when
// the process cannot be opened. The empty result is deliberate: an executable
// name is metadata, and a window whose process refuses to be read is still a
// usable window.
func processExeName(pid uint32) string {
	path, err := processImagePath(pid)
	if err != nil {
		return ""
	}

	return filepath.Base(path)
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

// foregroundWindowOrZero returns the foreground handle, or 0 when no
// application has focus.
func foregroundWindowOrZero() uintptr {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 || hwnd == desktopWindow() {
		return 0
	}

	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return 0
	}

	return hwnd
}

// foregroundTitle returns the foreground window's title for an error message,
// or "" when there is no foreground window.
func foregroundTitle() string {
	hwnd := foregroundWindowOrZero()
	if hwnd == 0 {
		return ""
	}

	return windowTitle(hwnd)
}

// desktopWindow is the handle that stands for the desktop itself, which is
// never an application window.
func desktopWindow() uintptr {
	hwnd, _, _ := procGetDesktopWindow.Call()

	return hwnd
}

// formatWindowHandle renders a window handle as the opaque ID this package
// returns.
func formatWindowHandle(hwnd uintptr) string {
	return "0x" + strconv.FormatUint(uint64(hwnd), 16)
}

// parseWindowHandle reads back an ID formatWindowHandle produced.
func parseWindowHandle(id string) (uintptr, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	trimmed = strings.TrimPrefix(trimmed, "0x")

	value, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil || value == 0 {
		return 0, false
	}

	return uintptr(value), true
}

// msgSize is sizeof(MSG) on 64-bit Windows: HWND, UINT, WPARAM, LPARAM, DWORD,
// POINT, DWORD, with the padding the ABI requires. It is only ever a scratch
// buffer for PeekMessage.
const msgSize = 48

// win32Rect mirrors the Win32 RECT the window queries fill.
type win32Rect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

// valid reports whether the rectangle describes a real area.
func (r win32Rect) valid() bool {
	return r.right > r.left && r.bottom > r.top
}

// rect returns the rectangle in the global top-left-origin space, keeping the
// sign of a monitor left of or above the primary.
func (r win32Rect) rect() image.Rectangle {
	return image.Rect(int(r.left), int(r.top), int(r.right), int(r.bottom))
}

// dpiOnce makes the process per-monitor-v2 DPI aware exactly once.
var dpiOnce sync.Once

// ensurePerMonitorV2 marks the process per-monitor-v2 DPI aware, so window
// frames are reported in physical pixels.
//
// It is safe to call repeatedly. A failure is ignored on purpose: Windows older
// than 10 1703 has no such context, and the process then keeps its manifest or
// the system default awareness.
func ensurePerMonitorV2() {
	dpiOnce.Do(func() {
		if err := procSetProcessDpiAwarenessContext.Find(); err != nil {
			return
		}

		_, _, _ = procSetProcessDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2)
	})
}

// win32Error drops the "the operation completed successfully" errno the syscall
// layer reports for calls that fail without setting one, so an error cause never
// reads as a success message.
func win32Error(callErr error) error {
	if callErr == nil || callErr == syscall.Errno(0) {
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
