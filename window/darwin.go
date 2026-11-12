//go:build darwin

package window

import (
	"bytes"
	"context"
	"image"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
)

// This file is the macOS backend. Window discovery is the accessibility window
// list of every running application, which is what makes the result the real
// top-level window list rather than "one window per process": a browser with
// three windows or an editor with two documents appears three or two times, so a
// caller can pick the right one.
//
// Placement reads and writes the same AXPosition and AXSize attributes the
// accessibility backends read, through an AXValue built from the requested
// rectangle. A window that refuses the write (a full-screen window, or one an
// application manages itself) reports the AXError instead of pretending it moved.
//
// Everything is bound at run time through internal/native, which keeps the
// package CGo-free: the Accessibility API and the values it returns are C, and
// the Objective-C runtime is used only for the two facts AX does not carry —
// which applications are running and what each one is called, and how to ask an
// application to come forward. The rectangles this backend reports are the same
// AXPosition and AXSize values the accessibility backends read, so they are in
// the module's global top-left-origin coordinate space by construction.
//
// Two macOS rules shape the code more than anything else:
//
//   - The accessibility API answers only a process the user has granted
//     Accessibility permission to. Both AXIsProcessTrusted answering false and
//     the kAXErrorAPIDisabled error are reported as errs.CodeAccessibilityDenied
//     rather than as a broken desktop, and the message says what to grant.
//   - AX has no global window identity. A window is addressed by its position in
//     its application's window list, which is what Info.ID carries, and every
//     call re-reads that list instead of caching element references another
//     process may have destroyed.

// Framework paths. Apple's frameworks are always installed, so a load failure
// means the process cannot do window work at all; New reports it once instead of
// panicking on first use. AppKit is opened explicitly because NSWorkspace and
// NSRunningApplication live there and a command-line process has no reason to
// have loaded it already.
const (
	coreFoundationPath      = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	applicationServicesPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	appKitPath              = "/System/Library/Frameworks/AppKit.framework/AppKit"
	objectiveCRuntimePath   = "/usr/lib/libobjc.A.dylib"
)

// CoreFoundation encodings and the AXError values this backend tells apart. The
// encoding and the boolean layout are fixed by the platform ABI; the error codes
// are the negative values in AXError.h.
const (
	cfStringEncodingUTF8 = 0x08000100

	kAXErrorSuccess              int32 = 0
	kAXErrorIllegalArgument      int32 = -25201
	kAXErrorInvalidUIElement     int32 = -25202
	kAXErrorCannotComplete       int32 = -25204
	kAXErrorAttributeUnsupported int32 = -25205
	kAXErrorAPIDisabled          int32 = -25211
	kAXErrorNoValue              int32 = -25212
)

// AXValue types, from AXValue.h.
const (
	axValueCGPointType uint32 = 1
	axValueCGSizeType  uint32 = 2
)

// The accessibility attribute and action names this backend reads and writes.
// They are spelled out because AX addresses attributes by CoreFoundation string;
// the package creates one CFString per name, once, and keeps it.
const (
	windowsAttribute   = "AXWindows"
	titleAttribute     = "AXTitle"
	positionAttribute  = "AXPosition"
	sizeAttribute      = "AXSize"
	minimizedAttribute = "AXMinimized"
	mainAttribute      = "AXMain"
	focusedAttribute   = "AXFocused"
	frontmostAttribute = "AXFrontmost"
	hiddenAttribute    = "AXHidden"
	raiseAction        = "AXRaise"
)

// windowAttributes are the attributes one window read fetches, in the order
// applyWindowValues expects them. They are requested in one
// AXUIElementCopyMultipleAttributeValues round trip, which the accessibility
// server answers far faster than the same number of single reads.
var windowAttributes = [...]string{
	titleAttribute,
	positionAttribute,
	sizeAttribute,
	minimizedAttribute,
	mainAttribute,
	focusedAttribute,
}

// NSApplicationActivationOptions, from NSRunningApplication.h. Activating with
// both raises every window of the application and lets it come forward even
// when another application currently owns the front, which is the case Activate
// exists for.
const (
	nsApplicationActivateAllWindows        uintptr = 1 << 0
	nsApplicationActivateIgnoringOtherApps uintptr = 1 << 1

	activationOptions = nsApplicationActivateAllWindows | nsApplicationActivateIgnoringOtherApps
)

const (
	// activationTimeout bounds how long Activate waits for macOS to make the
	// switch. Activation is asynchronous — the application has to become active
	// and adopt the window — and the wait only has to outlive that.
	activationTimeout = 500 * time.Millisecond
	activationPoll    = 25 * time.Millisecond

	// activationWriteInterval spaces out the writes that raise the window while
	// Activate waits for the application to adopt it. Repeating them on every
	// poll would flood the accessibility server with the same six calls; spacing
	// them still retries fast enough for an application that is only briefly
	// busy.
	activationWriteInterval = 100 * time.Millisecond

	// axMessagingTimeoutSeconds bounds one accessibility round trip, so an
	// application that has stopped answering cannot hold the caller for the
	// accessibility server's own, much longer, default. A refusal costs only the
	// bound, and the default then applies.
	axMessagingTimeoutSeconds float32 = 1
)

// cgPoint and cgSize mirror CoreGraphics' CGFloat structs. CGFloat is a double
// on every 64-bit platform, which is the only place this backend builds.
type cgPoint struct {
	X float64
	Y float64
}

type cgSize struct {
	Width  float64
	Height float64
}

// coreFoundation is the CoreFoundation surface the backend uses.
type coreFoundation struct {
	stringCreateWithCString func(allocator uintptr, value string, encoding uint32) uintptr
	stringGetCString        func(str uintptr, buffer *byte, size int64, encoding uint32) uint8
	stringGetLength         func(str uintptr) int64
	stringGetMaximumSize    func(length int64, encoding uint32) int64
	stringGetTypeID         func() uint64
	arrayCreate             func(allocator uintptr, values []uintptr, count int64, callbacks uintptr) uintptr
	arrayGetCount           func(array uintptr) int64
	arrayGetValueAtIndex    func(array uintptr, index int64) uintptr
	getTypeID               func(value uintptr) uint64
	booleanGetValue         func(value uintptr) uint8
	booleanGetTypeID        func() uint64
	retain                  func(value uintptr) uintptr
	release                 func(value uintptr)

	// typeArrayCallbacks is the address of kCFTypeArrayCallBacks, which makes a
	// created array retain and release its members.
	typeArrayCallbacks uintptr
}

// accessibility is the ApplicationServices surface the backend uses.
type accessibility struct {
	createApplication   func(pid int32) uintptr
	copyAttributeValue  func(element, attribute uintptr, value *uintptr) int32
	copyMultipleValues  func(element, attributes uintptr, options uint32, values *uintptr) int32
	performAction       func(element, action uintptr) int32
	setAttributeValue   func(element, attribute, value uintptr) int32
	setMessagingTimeout func(element uintptr, seconds float32) int32
	valueGetValue       func(value uintptr, kind uint32, out unsafe.Pointer) uint8
	valueGetType        func(value uintptr) uint32
	valueGetTypeID      func() uint64
	valueCreate         func(kind uint32, value unsafe.Pointer) uintptr
	isProcessTrusted    func() uint8
}

// objcRuntime is the Objective-C messaging surface. NSWorkspace and
// NSRunningApplication are the only classes the backend talks to: they answer
// which applications are running, how each one is named, and how to ask one to
// come forward. Nothing about a window comes from Objective-C.
type objcRuntime struct {
	getClass     func(name string) uintptr
	registerName func(name string) uintptr
	poolPush     func() uintptr
	poolPop      func(pool uintptr)

	// msgSend is bound once per signature. Objective-C methods are called by
	// sending a selector to a receiver, and the ABI decides where each argument
	// goes, so a distinct Go function type is needed per shape.
	msgSendID           func(receiver, selector uintptr) uintptr
	msgSendIDWithUint   func(receiver, selector, argument uintptr) uintptr
	msgSendIDWithInt    func(receiver, selector uintptr, argument int32) uintptr
	msgSendUint         func(receiver, selector uintptr) uintptr
	msgSendInt          func(receiver, selector uintptr) int32
	msgSendBoolWithUint func(receiver, selector, argument uintptr) uint8
	msgSendCStr         func(receiver, selector uintptr) *byte
}

// frameworks is the bound surface shared by every manager in the process.
type frameworks struct {
	cf   coreFoundation
	ax   accessibility
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

// bindFrameworks opens the libraries and binds every symbol. It stops at the
// first failure: a backend missing one core call cannot work, and the error
// names the exact symbol.
//
// The loader handles are never closed. The frameworks are shared with the rest
// of the process — AppKit draws every window the desktop shows — so unloading
// one of them would be worse than the few bytes its handle costs, and the
// symbols bound from them stay valid for as long as the process runs.
func bindFrameworks() (*frameworks, error) {
	cfLib, err := native.Open(coreFoundationPath)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS window support is unavailable")
	}

	axLib, err := native.Open(applicationServicesPath)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS window support is unavailable")
	}

	// AppKit exports no C symbol this backend calls; the classes it needs are
	// looked up in the Objective-C runtime once AppKit is loaded. It is opened
	// explicitly because a command-line process has no other reason to load it.
	if _, err := native.Open(appKitPath); err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS window support is unavailable")
	}

	objcLib, err := native.Open(objectiveCRuntimePath)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS window support is unavailable")
	}

	api := &frameworks{}

	bind := func(lib *native.Library, name string, target any) error {
		if err := lib.Func(name, target); err != nil {
			return errs.Wrapf(err, errs.CodeAccessibilityFailed, "macOS window support cannot bind %s", name)
		}

		return nil
	}

	for _, bindErr := range []error{
		bind(cfLib, "CFStringCreateWithCString", &api.cf.stringCreateWithCString),
		bind(cfLib, "CFStringGetCString", &api.cf.stringGetCString),
		bind(cfLib, "CFStringGetLength", &api.cf.stringGetLength),
		bind(cfLib, "CFStringGetMaximumSizeForEncoding", &api.cf.stringGetMaximumSize),
		bind(cfLib, "CFStringGetTypeID", &api.cf.stringGetTypeID),
		bind(cfLib, "CFArrayCreate", &api.cf.arrayCreate),
		bind(cfLib, "CFArrayGetCount", &api.cf.arrayGetCount),
		bind(cfLib, "CFArrayGetValueAtIndex", &api.cf.arrayGetValueAtIndex),
		bind(cfLib, "CFGetTypeID", &api.cf.getTypeID),
		bind(cfLib, "CFBooleanGetValue", &api.cf.booleanGetValue),
		bind(cfLib, "CFBooleanGetTypeID", &api.cf.booleanGetTypeID),
		bind(cfLib, "CFRetain", &api.cf.retain),
		bind(cfLib, "CFRelease", &api.cf.release),
		bind(axLib, "AXUIElementCreateApplication", &api.ax.createApplication),
		bind(axLib, "AXUIElementCopyAttributeValue", &api.ax.copyAttributeValue),
		bind(axLib, "AXUIElementCopyMultipleAttributeValues", &api.ax.copyMultipleValues),
		bind(axLib, "AXUIElementPerformAction", &api.ax.performAction),
		bind(axLib, "AXUIElementSetAttributeValue", &api.ax.setAttributeValue),
		bind(axLib, "AXUIElementSetMessagingTimeout", &api.ax.setMessagingTimeout),
		bind(axLib, "AXValueGetValue", &api.ax.valueGetValue),
		bind(axLib, "AXValueGetType", &api.ax.valueGetType),
		bind(axLib, "AXValueGetTypeID", &api.ax.valueGetTypeID),
		bind(axLib, "AXValueCreate", &api.ax.valueCreate),
		bind(axLib, "AXIsProcessTrusted", &api.ax.isProcessTrusted),
		bind(objcLib, "objc_getClass", &api.objc.getClass),
		bind(objcLib, "sel_registerName", &api.objc.registerName),
		bind(objcLib, "objc_autoreleasePoolPush", &api.objc.poolPush),
		bind(objcLib, "objc_autoreleasePoolPop", &api.objc.poolPop),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendID),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendIDWithUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendIDWithInt),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendInt),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendBoolWithUint),
		bind(objcLib, "objc_msgSend", &api.objc.msgSendCStr),
	} {
		if bindErr != nil {
			return nil, bindErr
		}
	}

	callbacks, err := cfLib.Data("kCFTypeArrayCallBacks")
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS window support cannot bind kCFTypeArrayCallBacks")
	}

	api.cf.typeArrayCallbacks = callbacks

	return api, nil
}

// verifyAppKit confirms the two AppKit classes the backend needs are present.
// After AppKit is loaded they always are; the check exists so a missing class is
// an error from New instead of an empty window list from every later call.
func (f *frameworks) verifyAppKit() error {
	for _, name := range []string{"NSWorkspace", "NSRunningApplication"} {
		if f.class(name) == 0 {
			return errs.Newf(errs.CodeAccessibilityFailed, "AppKit does not provide the %s class", name)
		}
	}

	return nil
}

// manager is the macOS window backend. It holds no native resource between
// calls — every query creates and releases its own accessibility elements — so
// the only state is the configuration and the closed flag.
type manager struct {
	api *frameworks

	includeUntitled bool

	mu     sync.Mutex
	closed bool
}

var _ Manager = (*manager)(nil)

// New returns the macOS window backend.
//
// It binds the frameworks once for the process — later calls reuse the binding —
// and confirms the AppKit classes the backend needs are present. Accessibility
// permission is not required yet: it is checked when the backend uses the API,
// so a caller can construct the manager, tell the user what to grant, and retry
// the operation afterwards.
func New(opts Options) (Manager, error) {
	api, err := loadFrameworks()
	if err != nil {
		return nil, err
	}

	if err := api.verifyAppKit(); err != nil {
		return nil, err
	}

	return &manager{api: api, includeUntitled: opts.IncludeUntitled}, nil
}

// List returns every visible top-level window of every running application,
// front to back.
func (m *manager) List(ctx context.Context) ([]Info, error) {
	if err := m.ensureOpen(); err != nil {
		return nil, err
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	if err := m.api.checkTrusted(); err != nil {
		return nil, err
	}

	frontmostPID, _ := m.api.frontmostApplicationPID()
	applications := orderApplications(m.api.runningApplications(), frontmostPID)

	// A desktop always has running applications, so none at all means the
	// workspace could not be read — a process without a window server session,
	// for instance. An empty list would claim the desktop has no windows instead.
	if len(applications) == 0 {
		return nil, errs.New(
			errs.CodeAccessibilityFailed,
			"the window server reports no running applications",
		)
	}

	windows := make([]Info, 0, len(applications))

	var firstFailure error

	answered := 0

	for _, application := range applications {
		if err := contextError(ctx); err != nil {
			return nil, err
		}

		infos, _, err := m.api.applicationWindows(
			application.pid,
			application.identity(),
			application.pid == frontmostPID,
			m.includeUntitled,
		)
		if err != nil {
			// A revoked permission applies to the whole desktop, so it stops
			// the enumeration. A single application that will not answer is
			// skipped instead: one unresponsive process must not hide every
			// window of the desktop.
			if errs.IsCode(err, errs.CodeAccessibilityDenied) {
				return nil, err
			}

			if firstFailure == nil {
				firstFailure = err
			}

			continue
		}

		answered++

		windows = append(windows, infos...)
	}

	// Nothing answered at all is a broken reading rather than an empty desktop,
	// and an empty list would tell the caller the desktop has no windows.
	if len(windows) == 0 && answered == 0 && firstFailure != nil {
		return nil, firstFailure
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	return windows, nil
}

// Frontmost returns the window the user is working in.
func (m *manager) Frontmost(ctx context.Context) (Info, error) {
	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	if err := m.api.checkTrusted(); err != nil {
		return Info{}, err
	}

	pid, ok := m.api.frontmostApplicationPID()
	if !ok {
		return Info{}, errs.New(errs.CodeElementNotFound, "no application window has focus")
	}

	application, _ := m.api.application(pid)

	// Untitled windows are asked for because the focused window is returned even
	// when it has no title yet: an application that is still loading is still
	// the window the user is in.
	infos, focused, err := m.api.applicationWindows(pid, application.identity(), true, true)
	if err != nil {
		return Info{}, err
	}

	if focused < 0 || focused >= len(infos) {
		return Info{}, errs.New(errs.CodeElementNotFound, "no application window has focus")
	}

	return infos[focused], nil
}

// Activate brings the window target selects to the foreground and verifies it.
func (m *manager) Activate(ctx context.Context, target Target) (Info, error) {
	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := validateTarget(target); err != nil {
		return Info{}, err
	}

	if err := m.api.checkTrusted(); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	// Resolving through a fresh List is what makes a repeated call work after
	// the target window has been recreated: the caller's target describes a
	// window, not a handle that has to still be alive.
	windows, err := m.List(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := m.api.find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	pid, index, ok := parseWindowID(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not a macOS window id",
			selected.ID,
		)
	}

	return m.api.activate(ctx, selected, pid, index, m.includeUntitled)
}

// SetBounds places the window target selects and returns it as it ended up.
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

	if err := m.api.checkTrusted(); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	// One fresh list is what makes a target a description of a window rather
	// than a handle that must still be alive.
	windows, err := m.List(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := m.api.find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	pid, index, ok := parseWindowID(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not a macOS window id",
			selected.ID,
		)
	}

	return m.api.place(ctx, selected, pid, index, bounds, m.includeUntitled)
}

// Close marks the manager closed. There is no per-manager native resource to
// release: the frameworks are opened once for the process and shared, and the
// accessibility elements a call creates are released before it returns.
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

// runningApplication is one application the window server knows about.
type runningApplication struct {
	pid      int32
	bundleID string
	name     string
}

// identity returns how this backend names the application in Info.Application:
// the bundle identifier when macOS has one, and the localized application name
// otherwise. A process without a bundle, such as a command-line tool that owns
// a window, is named by the name the workspace reports for it.
func (a runningApplication) identity() string {
	if a.bundleID != "" {
		return a.bundleID
	}

	return a.name
}

// display returns the name an error message uses for the application, falling
// back to the process id when macOS reports no name at all.
func (a runningApplication) display() string {
	if a.name != "" {
		return a.name
	}

	if a.bundleID != "" {
		return a.bundleID
	}

	return strconv.Itoa(int(a.pid))
}

// runningApplications returns every application the workspace reports.
func (f *frameworks) runningApplications() []runningApplication {
	applications := make([]runningApplication, 0, 32)

	f.withPool(func() {
		workspaceClass := f.class("NSWorkspace")
		if workspaceClass == 0 {
			return
		}

		workspace := f.objc.msgSendID(workspaceClass, f.selector("sharedWorkspace"))
		if workspace == 0 {
			return
		}

		list := f.objc.msgSendID(workspace, f.selector("runningApplications"))
		if list == 0 {
			return
		}

		count := f.objc.msgSendUint(list, f.selector("count"))

		for index := uintptr(0); index < count; index++ {
			application := f.objc.msgSendIDWithUint(list, f.selector("objectAtIndex:"), index)
			if application == 0 {
				continue
			}

			pid := f.objc.msgSendInt(application, f.selector("processIdentifier"))
			if pid <= 0 {
				continue
			}

			applications = append(applications, runningApplication{
				pid:      pid,
				bundleID: f.nsString(f.objc.msgSendID(application, f.selector("bundleIdentifier"))),
				name:     f.nsString(f.objc.msgSendID(application, f.selector("localizedName"))),
			})
		}
	})

	return applications
}

// frontmostApplicationPID returns the process the window server considers
// frontmost, and reports whether one was named at all.
func (f *frameworks) frontmostApplicationPID() (int32, bool) {
	var pid int32

	f.withPool(func() {
		workspaceClass := f.class("NSWorkspace")
		if workspaceClass == 0 {
			return
		}

		workspace := f.objc.msgSendID(workspaceClass, f.selector("sharedWorkspace"))
		if workspace == 0 {
			return
		}

		application := f.objc.msgSendID(workspace, f.selector("frontmostApplication"))
		if application == 0 {
			return
		}

		pid = f.objc.msgSendInt(application, f.selector("processIdentifier"))
	})

	return pid, pid > 0
}

// application returns the workspace's record for one process.
func (f *frameworks) application(pid int32) (runningApplication, bool) {
	if pid <= 0 {
		return runningApplication{}, false
	}

	for _, application := range f.runningApplications() {
		if application.pid == pid {
			return application, true
		}
	}

	return runningApplication{}, false
}

// orderApplications puts the frontmost application first.
//
// The workspace lists running applications in launch order, not in stacking
// order, and AX exposes no z-order across applications, so the frontmost
// application's windows are the only ones that can be placed truthfully: they
// come first, followed by the rest in workspace order. Within one application AX
// returns its windows front to back, and that is the ordering a caller can
// actually rely on.
func orderApplications(applications []runningApplication, frontmostPID int32) []runningApplication {
	if frontmostPID <= 0 {
		return applications
	}

	ordered := make([]runningApplication, 0, len(applications))

	for _, application := range applications {
		if application.pid == frontmostPID {
			ordered = append(ordered, application)
		}
	}

	for _, application := range applications {
		if application.pid == frontmostPID {
			continue
		}

		ordered = append(ordered, application)
	}

	return ordered
}

// resolveApplicationTarget substitutes the bundle identifier of a running
// application whose localized name matches target.Application.
//
// This is a macOS courtesy: the identity List reports is the bundle identifier
// ("com.google.Chrome"), but a model naming an application naturally says what
// the Dock says ("Google Chrome"). Matching the localized name against the
// running applications turns the second into the first without asking the caller
// to know anything about bundles. The target is returned unchanged when no
// application answers to the name.
func (f *frameworks) resolveApplicationTarget(target Target) Target {
	name := strings.TrimSuffix(normalizeApplication(target.Application), ".app")
	if name == "" {
		return target
	}

	for _, application := range f.runningApplications() {
		if application.bundleID == "" || normalizeApplication(application.name) != name {
			continue
		}

		target.Application = application.bundleID

		return target
	}

	return target
}

// find resolves a target against a window list.
//
// A localized application name is resolved to a bundle identifier first, so
// "Google Chrome" selects the application List calls "com.google.Chrome". The
// generic rule is still the last word: when the resolved target matches nothing,
// Find runs again on the target the caller passed, exactly as it would on any
// other platform.
func (f *frameworks) find(windows []Info, target Target) (Info, bool) {
	// An application name only matters when it is the field Target precedence
	// reaches; resolving it otherwise would cost a workspace scan for nothing.
	if target.ID == "" && target.PID <= 0 && strings.TrimSpace(target.Application) != "" {
		resolved := f.resolveApplicationTarget(target)
		if resolved.Application != target.Application {
			if selected, ok := Find(windows, resolved); ok {
				return selected, true
			}

			return Find(windows, target)
		}
	}

	return Find(windows, target)
}

// applicationWindows reads one application's windows.
//
// The returned windows are in the order AX lists them, which is front to back
// for that application. The second result is the position of the window the
// application reports as focused, or -1 when it has no windows. frontmost marks
// the application the workspace reports in front: only its windows are marked
// Focused, because a background application's main window is not the window the
// user is working in.
//
// A window whose title cannot be read at all is kept, because an unreadable
// title is not the same as an empty title bar; see windowState.untitled.
func (f *frameworks) applicationWindows(
	pid int32,
	application string,
	frontmost bool,
	includeUntitled bool,
) ([]Info, int, error) {
	element, err := f.applicationElement(pid)
	if err != nil {
		return nil, -1, err
	}

	defer f.cf.release(element)

	windows, err := f.copyWindowElements(element, pid)
	if err != nil {
		return nil, -1, err
	}

	for _, window := range windows {
		if window != 0 {
			defer f.cf.release(window)
		}
	}

	infos := make([]Info, 0, len(windows))
	states := make([]windowState, 0, len(windows))

	for index, window := range windows {
		if window == 0 {
			continue
		}

		f.ax.setMessagingTimeout(window, axMessagingTimeoutSeconds)

		state, stateErr := f.readWindowState(window)
		if stateErr != nil {
			return nil, -1, stateErr
		}

		states = append(states, state)
		infos = append(infos, windowInfo(pid, application, index, state))
	}

	focused := -1
	if frontmost {
		focused = focusedWindowIndex(states)
	}

	kept := make([]Info, 0, len(infos))

	keptFocus := -1

	for position := range infos {
		info := infos[position]
		info.Focused = position == focused

		if !includeUntitled && !info.Focused && states[position].untitled() {
			continue
		}

		if info.Focused {
			keptFocus = len(kept)
		}

		kept = append(kept, info)
	}

	return kept, keptFocus, nil
}

// focusedWindowIndex returns the window an application reports as the one the
// user is working in.
//
// AXMain is the answer every AppKit application gives; AXFocused covers an
// application that reports a focused sheet or popover as a separate window. An
// application that implements neither is represented by the first window AX
// lists, which is the frontmost one, because returning "nothing is focused"
// while the user is plainly working in a window would be worse than naming it.
func focusedWindowIndex(states []windowState) int {
	if len(states) == 0 {
		return -1
	}

	for index := range states {
		if states[index].main {
			return index
		}
	}

	for index := range states {
		if states[index].focused {
			return index
		}
	}

	return 0
}

// copyWindowElements returns the accessibility window elements of one
// application, each retained by the caller.
//
// The position of a window in this list is what Info.ID encodes, so a zero entry
// keeps its position in the returned slice rather than being dropped: the
// indexes of the windows after it must not shift.
func (f *frameworks) copyWindowElements(element uintptr, pid int32) ([]uintptr, error) {
	value, status := f.copyAttribute(element, windowsAttribute)
	if status != kAXErrorSuccess {
		switch status {
		case kAXErrorAPIDisabled:
			return nil, permissionError("AXUIElementCopyAttributeValue returned kAXErrorAPIDisabled")
		case kAXErrorAttributeUnsupported, kAXErrorNoValue, kAXErrorInvalidUIElement:
			// A process that has no windows (a background service) and a process
			// that has exited both answer this way. Neither is an error the
			// caller can act on, and both mean "no windows".
			return nil, nil
		case kAXErrorCannotComplete:
			return nil, errs.Newf(
				errs.CodeAccessibilityFailed,
				"process %d did not answer %s for its windows (AXError %d); it may be unresponsive",
				pid,
				windowsAttribute,
				status,
			)
		default:
			return nil, axCallFailure("AXUIElementCopyAttributeValue", windowsAttribute, status, pid)
		}
	}

	if value == 0 {
		return nil, nil
	}

	defer f.cf.release(value)

	reported := f.arrayValues(value)
	windows := make([]uintptr, len(reported))

	for index, window := range reported {
		if window == 0 {
			continue
		}

		windows[index] = f.cf.retain(window)
	}

	return windows, nil
}

// pickWindow returns the element and state of the window an ID names.
//
// The ID carries the window's position in the application's AX window list, but
// raising a window reorders that list and an application may have rearranged its
// windows between the List that produced the ID and this call, so a window whose
// frame matches the frame List reported is preferred: the frame is the one
// property that survives a reorder. The index is the fallback, and the only
// handle a minimized window has, since a minimized window reports an empty
// Bounds.
func (f *frameworks) pickWindow(
	windows []uintptr,
	index int,
	bounds image.Rectangle,
) (uintptr, windowState, bool) {
	if bounds != (image.Rectangle{}) {
		var (
			match   uintptr
			state   windowState
			matches int
		)

		for _, window := range windows {
			if window == 0 {
				continue
			}

			candidate, _ := f.readWindowState(window)
			if candidate.minimized || candidate.bounds() != bounds {
				continue
			}

			match, state = window, candidate
			matches++
		}

		// Two windows sharing a frame leave the index to decide, because a
		// guess between them would activate the wrong one.
		if matches == 1 {
			return match, state, true
		}
	}

	if index < 0 || index >= len(windows) || windows[index] == 0 {
		return 0, windowState{}, false
	}

	window := windows[index]

	state, _ := f.readWindowState(window)

	return window, state, true
}

// windowState is what one AX window reports about itself.
//
// The has flags keep "the attribute said nothing" apart from "the attribute said
// zero": a window at the origin has a position, and a window that would not
// answer has none.
type windowState struct {
	title       string
	position    cgPoint
	size        cgSize
	hasTitle    bool
	hasPosition bool
	hasSize     bool
	minimized   bool
	main        bool
	focused     bool
}

// untitled reports whether the window is known to have an empty title bar. A
// title the window would not report is not an empty one, so an unreadable title
// does not make a window untitled.
func (s windowState) untitled() bool {
	return s.hasTitle && strings.TrimSpace(s.title) == ""
}

// bounds converts the window's AXPosition and AXSize into a global
// top-left-origin rectangle. A fractional point is rounded to the nearest pixel,
// so adjacent windows do not develop one-pixel gaps.
func (s windowState) bounds() image.Rectangle {
	if !s.hasPosition || !s.hasSize {
		return image.Rectangle{}
	}

	minX := int(math.Round(s.position.X))
	minY := int(math.Round(s.position.Y))
	width := int(math.Round(s.size.Width))
	height := int(math.Round(s.size.Height))

	return image.Rect(minX, minY, minX+width, minY+height)
}

// windowInfo renders one window's state as the package's Info.
func windowInfo(pid int32, application string, index int, state windowState) Info {
	info := Info{
		ID:          formatWindowID(pid, index),
		PID:         int(pid),
		Title:       strings.TrimSpace(state.title),
		Application: application,
		Minimized:   state.minimized,
	}

	// A minimized window's frame is its pre-minimize rectangle, often parked off
	// screen; reporting it would be a lie about where the window is.
	if !info.Minimized {
		info.Bounds = state.bounds()
	}

	return info
}

// readWindowState reads the attributes of one window.
//
// The six attributes are requested in one AXUIElementCopyMultipleAttributeValues
// round trip. Only a revoked permission is an error: a window that will not
// report an attribute is described by the attributes it does report, and a batch
// the server rejects as a whole is retried one attribute at a time, which is what
// an application does when one of its windows answers an attribute with
// kAXErrorIllegalArgument.
func (f *frameworks) readWindowState(element uintptr) (windowState, error) {
	state := windowState{}

	if attributes := f.windowAttributeArray(); attributes != 0 {
		var values uintptr

		status := f.ax.copyMultipleValues(element, attributes, 0, &values)
		if status == kAXErrorSuccess && values != 0 {
			defer f.cf.release(values)

			f.applyWindowValues(&state, f.arrayValues(values))

			return state, nil
		}

		if values != 0 {
			f.cf.release(values)
		}

		if status == kAXErrorAPIDisabled {
			return state, permissionError(
				"AXUIElementCopyMultipleAttributeValues returned kAXErrorAPIDisabled",
			)
		}

		if status != kAXErrorIllegalArgument {
			// kAXErrorCannotComplete and kAXErrorInvalidUIElement land here: the
			// window did not answer, so it is listed with what could be read
			// rather than dropped. Reading the attributes one at a time would
			// only wait out the same timeout five more times.
			return state, nil
		}
	}

	f.readWindowValuesOneByOne(element, &state)

	return state, nil
}

// readWindowValuesOneByOne refills state with one AX call per attribute. A read
// that fails leaves that attribute at its zero value, which is the same state as
// a batch that reported an error placeholder for it.
func (f *frameworks) readWindowValuesOneByOne(element uintptr, state *windowState) {
	values := make([]uintptr, len(windowAttributes))

	for index, name := range windowAttributes {
		value, status := f.copyAttribute(element, name)
		if status != kAXErrorSuccess || value == 0 {
			continue
		}

		values[index] = value

		defer f.cf.release(value)
	}

	f.applyWindowValues(state, values)
}

// applyWindowValues turns one batch of window values into a state. A value of an
// unexpected type is skipped: AX answers an attribute it cannot produce with an
// error placeholder, and reading that as text or a boolean would invent data.
func (f *frameworks) applyWindowValues(state *windowState, values []uintptr) {
	value := func(index int) uintptr {
		if index >= len(values) {
			return 0
		}

		return values[index]
	}

	if f.isCFString(value(0)) {
		state.title = f.goString(value(0))
		state.hasTitle = true
	}

	if point, ok := f.pointValue(value(1)); ok {
		state.position = point
		state.hasPosition = true
	}

	if size, ok := f.sizeValue(value(2)); ok {
		state.size = size
		state.hasSize = true
	}

	if minimized, ok := f.cfBool(value(3)); ok {
		state.minimized = minimized
	}

	if main, ok := f.cfBool(value(4)); ok {
		state.main = main
	}

	if focused, ok := f.cfBool(value(5)); ok {
		state.focused = focused
	}
}

// activate brings one resolved window forward, verifies the switch, and returns
// the window's refreshed Info.
func (f *frameworks) activate(
	ctx context.Context,
	selected Info,
	pid int32,
	index int,
	includeUntitled bool,
) (Info, error) {
	element, err := f.applicationElement(pid)
	if err != nil {
		return Info{}, err
	}

	defer f.cf.release(element)

	windows, err := f.copyWindowElements(element, pid)
	if err != nil {
		return Info{}, err
	}

	for _, window := range windows {
		if window != 0 {
			defer f.cf.release(window)
		}
	}

	window, state, ok := f.pickWindow(windows, index, selected.Bounds)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeElementNotFound,
			"window %q (%s) is no longer in its application's window list",
			selected.Title,
			selected.Application,
		)
	}

	if err := f.requestActivation(pid); err != nil {
		return Info{}, err
	}

	if err := f.waitForActivation(ctx, pid, element, window, state.minimized, selected); err != nil {
		return Info{}, err
	}

	// The position a window occupies in its application's window list is what
	// its ID encodes, and raising a window reorders that list, so the refreshed
	// Info comes from a fresh read rather than from the ID the caller passed in.
	infos, focused, err := f.applicationWindows(pid, selected.Application, true, includeUntitled)
	if err != nil {
		return Info{}, err
	}

	if focused < 0 || focused >= len(infos) {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared while it was being activated",
		)
	}

	return infos[focused], nil
}

// place moves and resizes one resolved window, asks for the front, and returns
// the window as it ended up.
func (f *frameworks) place(
	ctx context.Context,
	selected Info,
	pid int32,
	index int,
	bounds image.Rectangle,
	includeUntitled bool,
) (Info, error) {
	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	element, err := f.applicationElement(pid)
	if err != nil {
		return Info{}, err
	}

	defer f.cf.release(element)

	windows, err := f.copyWindowElements(element, pid)
	if err != nil {
		return Info{}, err
	}

	for _, window := range windows {
		if window != 0 {
			defer f.cf.release(window)
		}
	}

	window, state, ok := f.pickWindow(windows, index, selected.Bounds)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeElementNotFound,
			"window %q (%s) is no longer in its application's window list",
			selected.Title,
			selected.Application,
		)
	}

	// Ask for the application first, then take the window out of the Dock and
	// raise it. The accessibility writes are accepted more reliably once the
	// application is becoming active; requestActivation reports only a process
	// that is not running, and the final Info records whether the front actually
	// changed.
	if err := f.requestActivation(pid); err != nil {
		return Info{}, err
	}

	if err := f.presentWindow(element, window, state.minimized); err != nil {
		return Info{}, err
	}

	if err := f.setWindowBounds(window, bounds); err != nil {
		return Info{}, err
	}

	// Raising can reorder the application's window list, so the returned ID
	// comes from a fresh read rather than from the index the caller passed in.
	// The frame that was just applied identifies the window across that reorder.
	placedState, _ := f.readWindowState(window)
	actual := placedState.bounds()

	infos, focused, err := f.applicationWindows(pid, selected.Application, true, includeUntitled)
	if err != nil {
		return Info{}, err
	}

	if actual != (image.Rectangle{}) {
		for _, info := range infos {
			if info.Bounds == actual {
				return info, nil
			}
		}
	}

	if focused >= 0 && focused < len(infos) {
		return infos[focused], nil
	}

	return Info{}, errs.New(
		errs.CodeElementNotFound,
		"the window disappeared while it was being placed",
	)
}

// setWindowBounds writes AXPosition and AXSize on one window.
//
// Both attributes take an AXValue, which is created from the CoreGraphics point
// and size. A window that refuses the write (a full-screen window, or an
// application that manages its own geometry) reports the AXError rather than
// leaving the caller to believe the frame was applied.
func (f *frameworks) setWindowBounds(window uintptr, bounds image.Rectangle) error {
	point := cgPoint{X: float64(bounds.Min.X), Y: float64(bounds.Min.Y)}
	size := cgSize{Width: float64(bounds.Dx()), Height: float64(bounds.Dy())}

	pointValue := f.ax.valueCreate(axValueCGPointType, unsafe.Pointer(&point))
	if pointValue == 0 {
		return errs.New(
			errs.CodeAccessibilityFailed,
			"could not create the accessibility value for the window position",
		)
	}

	defer f.cf.release(pointValue)

	sizeValue := f.ax.valueCreate(axValueCGSizeType, unsafe.Pointer(&size))
	if sizeValue == 0 {
		return errs.New(
			errs.CodeAccessibilityFailed,
			"could not create the accessibility value for the window size",
		)
	}

	defer f.cf.release(sizeValue)

	for _, write := range [...]struct {
		attribute string
		value     uintptr
	}{
		{positionAttribute, pointValue},
		{sizeAttribute, sizeValue},
	} {
		status := f.ax.setAttributeValue(window, f.cfStringRef(write.attribute), write.value)
		switch status {
		case kAXErrorSuccess:
		case kAXErrorAPIDisabled:
			return permissionError("AXUIElementSetAttributeValue returned kAXErrorAPIDisabled")
		default:
			return errs.Newf(
				errs.CodeAccessibilityFailed,
				"could not set %s on the window (AXError %d)",
				write.attribute,
				status,
			)
		}
	}

	return nil
}

// requestActivation asks macOS to bring a process's application forward.
//
// NSRunningApplication.activateWithOptions: is the supported way to do this from
// another process; NSApplication's own activation methods live in the
// application's process. The call is asynchronous, so its result only reports
// whether macOS accepted the request — the switch itself is verified afterwards
// by waitForActivation.
func (f *frameworks) requestActivation(pid int32) error {
	var found bool

	f.withPool(func() {
		class := f.class("NSRunningApplication")
		if class == 0 {
			return
		}

		application := f.objc.msgSendIDWithInt(
			class,
			f.selector("runningApplicationWithProcessIdentifier:"),
			pid,
		)
		if application == 0 {
			return
		}

		found = true

		_ = f.objc.msgSendBoolWithUint(
			application,
			f.selector("activateWithOptions:"),
			activationOptions,
		)
	})

	if !found {
		return errs.Newf(errs.CodeElementNotFound, "process %d is no longer a running application", pid)
	}

	return nil
}

// waitForActivation polls until the target window is the frontmost
// application's main window, honoring ctx cancellation. minimized says whether
// the window was still in the Dock when it was resolved, so the wait knows
// whether restoring it is part of the switch.
func (f *frameworks) waitForActivation(
	ctx context.Context,
	pid int32,
	element uintptr,
	window uintptr,
	minimized bool,
	selected Info,
) error {
	deadline := time.Now().Add(activationTimeout)

	var lastWrite time.Time

	for {
		if err := contextError(ctx); err != nil {
			return err
		}

		if frontmost, ok := f.frontmostApplicationPID(); ok && frontmost == pid {
			// The writes are repeated while the window is not yet the main one:
			// an application that is still becoming active rejects them with
			// kAXErrorCannotComplete, and a later attempt can succeed.
			if time.Since(lastWrite) >= activationWriteInterval {
				if err := f.presentWindow(element, window, minimized); err != nil {
					return err
				}

				lastWrite = time.Now()
			}

			if main, reported := f.readBooleanAttribute(window, mainAttribute); reported && main {
				return nil
			}
		}

		if time.Now().After(deadline) {
			return f.activationFailure(pid, selected)
		}

		time.Sleep(activationPoll)
	}
}

// presentWindow performs the writes that make an activated window the one the
// user is looking at: it takes the window out of the Dock when it was minimized,
// raises it, and marks it as its application's main and focused window.
//
// The boolean values the writes need are read from the accessibility server. An
// AX boolean attribute is a CFBoolean, and AXUIElementSetAttributeValue accepts
// any CFBoolean carrying the right value, so a true value read from an attribute
// that currently holds true is interchangeable with kCFBooleanTrue. Reading the
// values from AX also keeps this file clear of the exported kCFBooleanTrue
// symbol, whose value can only be reached by converting a uintptr into a pointer,
// which go vet rejects. A window with nothing true or false to read leaves that
// write out; the verification then reports what actually happened.
func (f *frameworks) presentWindow(element, window uintptr, minimized bool) error {
	// Unminimize first: raising a window that is still in the Dock does nothing
	// visible, and an application will not adopt a window that is not on screen.
	if minimized {
		if falseValue := f.copyBoolean(element, window, false); falseValue != 0 {
			status := f.ax.setAttributeValue(window, f.cfStringRef(minimizedAttribute), falseValue)

			f.cf.release(falseValue)

			if status == kAXErrorAPIDisabled {
				return permissionError("AXUIElementSetAttributeValue returned kAXErrorAPIDisabled")
			}
		}
	}

	status := f.ax.performAction(window, f.cfStringRef(raiseAction))
	if status == kAXErrorAPIDisabled {
		return permissionError("AXUIElementPerformAction returned kAXErrorAPIDisabled")
	}

	if trueValue := f.copyBoolean(element, window, true); trueValue != 0 {
		defer f.cf.release(trueValue)

		for _, attribute := range []string{mainAttribute, focusedAttribute} {
			setStatus := f.ax.setAttributeValue(window, f.cfStringRef(attribute), trueValue)
			if setStatus == kAXErrorAPIDisabled {
				return permissionError("AXUIElementSetAttributeValue returned kAXErrorAPIDisabled")
			}
		}
	}

	return nil
}

// activationFailure describes why the switch did not happen, naming the
// application that is in front instead so the caller can tell a window manager
// refusal from a target that never existed.
func (f *frameworks) activationFailure(pid int32, selected Info) error {
	frontmost, ok := f.frontmostApplicationPID()

	switch {
	case ok && frontmost == pid:
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"process %d is in front, but %q (%s) did not become its main window within %s",
			pid,
			selected.Title,
			selected.Application,
			activationTimeout,
		)
	case ok:
		application, found := f.application(frontmost)

		other := strconv.Itoa(int(frontmost))
		if found {
			other = application.display()
		}

		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not bring %q (%s) to the front within %s; %s is in front",
			selected.Title,
			selected.Application,
			activationTimeout,
			other,
		)
	default:
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not bring %q (%s) to the front within %s",
			selected.Title,
			selected.Application,
			activationTimeout,
		)
	}
}

// applicationElement creates the accessibility element for a process.
//
// The caller owns the element and releases it. A process that has no windows
// still yields an element; its window list then answers
// kAXErrorAttributeUnsupported, which copyWindowElements reads as "no windows".
//
// Every call made through the element is bounded by axMessagingTimeoutSeconds.
// A refusal only means the accessibility server's own, much longer, default
// applies, so it is not an error.
func (f *frameworks) applicationElement(pid int32) (uintptr, error) {
	if pid <= 0 {
		return 0, errs.Newf(errs.CodeInvalidInput, "process id %d is not a process", pid)
	}

	element := f.ax.createApplication(pid)
	if element == 0 {
		return 0, errs.Newf(
			errs.CodeAccessibilityFailed,
			"AXUIElementCreateApplication failed for process %d",
			pid,
		)
	}

	f.ax.setMessagingTimeout(element, axMessagingTimeoutSeconds)

	return element, nil
}

// checkTrusted reports the permission error a process without Accessibility
// permission answers every operation with.
func (f *frameworks) checkTrusted() error {
	if f.ax.isProcessTrusted() != 0 {
		return nil
	}

	return permissionError("AXIsProcessTrusted returned false")
}

// windowAttributeOnce and windowAttributeList cache the attribute array every
// window read reuses, so it is built once for the process.
var (
	windowAttributeOnce sync.Once
	windowAttributeList uintptr
)

// windowAttributeArray returns the attribute array every window read reuses. The
// names never change and the array is immutable, so one array serves every window
// of every application for the life of the process. A zero result means the array
// could not be built, and the reads fall back to one call per attribute.
func (f *frameworks) windowAttributeArray() uintptr {
	windowAttributeOnce.Do(func() {
		refs := make([]uintptr, 0, len(windowAttributes))

		for _, name := range windowAttributes {
			ref := f.cfStringRef(name)
			if ref == 0 {
				return
			}

			refs = append(refs, ref)
		}

		windowAttributeList = f.cf.arrayCreate(0, refs, int64(len(refs)), f.cf.typeArrayCallbacks)
	})

	return windowAttributeList
}

// copyAttribute reads one attribute and returns the owned value, which the
// caller releases. A non-zero AXError carries the reason, so callers can tell an
// unsupported attribute from a dead element.
func (f *frameworks) copyAttribute(element uintptr, name string) (uintptr, int32) {
	attribute := f.cfStringRef(name)
	if attribute == 0 {
		return 0, kAXErrorIllegalArgument
	}

	var value uintptr

	status := f.ax.copyAttributeValue(element, attribute, &value)
	if status != kAXErrorSuccess {
		return 0, status
	}

	return value, kAXErrorSuccess
}

// readBooleanAttribute reports the value of one boolean attribute and whether the
// element reported it at all.
func (f *frameworks) readBooleanAttribute(element uintptr, name string) (bool, bool) {
	value, status := f.copyAttribute(element, name)
	if status != kAXErrorSuccess || value == 0 {
		return false, false
	}

	defer f.cf.release(value)

	return f.cfBool(value)
}

// copyBoolean returns an owned CFBoolean carrying want, read from the first
// candidate attribute that holds it, or zero when no candidate does.
//
// AX's setters take the value to write, and the accessibility server hands out
// CFBoolean values for its boolean attributes, so reading a boolean that already
// holds the wanted value produces exactly what the setter expects without
// reaching for the exported singleton. The candidates are the attributes an
// activated application and its windows report: AXFrontmost and AXHidden for the
// application, and AXMain, AXFocused and AXMinimized for the window.
func (f *frameworks) copyBoolean(element, window uintptr, want bool) uintptr {
	candidates := [...]struct {
		element   uintptr
		attribute string
	}{
		{element, frontmostAttribute},
		{element, hiddenAttribute},
		{window, focusedAttribute},
		{window, mainAttribute},
		{window, minimizedAttribute},
	}

	for _, candidate := range candidates {
		if candidate.element == 0 {
			continue
		}

		value, status := f.copyAttribute(candidate.element, candidate.attribute)
		if status != kAXErrorSuccess || value == 0 {
			continue
		}

		if boolean, ok := f.cfBool(value); !ok || boolean != want {
			f.cf.release(value)

			continue
		}

		return value
	}

	return 0
}

// attributeMu and attributeCache hold the CoreFoundation strings the backend
// compares attributes by, one per name, for the life of the process.
var (
	attributeMu    sync.Mutex
	attributeCache = make(map[string]uintptr, 16)
)

// cfStringRef returns the CoreFoundation string for name, creating and caching it
// on first use. A zero result means the runtime refused to make the string, which
// no caller can act on; the attribute read then fails like any other.
//
// Accessibility attributes are compared by CFString value, so the backend builds
// its own strings once and keeps them for the process lifetime: there are fewer
// than twenty, and a manager that lives for the whole process would otherwise
// recreate them on every window it reads.
func (f *frameworks) cfStringRef(name string) uintptr {
	attributeMu.Lock()
	defer attributeMu.Unlock()

	if cached, ok := attributeCache[name]; ok {
		return cached
	}

	ref := f.cf.stringCreateWithCString(0, name, cfStringEncodingUTF8)
	attributeCache[name] = ref

	return ref
}

// goString copies a CFString into Go memory. The caller keeps ownership of the
// CFString; the copy is what survives the next CFRelease.
func (f *frameworks) goString(ref uintptr) string {
	if ref == 0 {
		return ""
	}

	length := f.cf.stringGetLength(ref)
	if length <= 0 {
		return ""
	}

	maxSize := f.cf.stringGetMaximumSize(length, cfStringEncodingUTF8)
	if maxSize <= 0 {
		return ""
	}

	buffer := make([]byte, maxSize+1)

	if f.cf.stringGetCString(ref, unsafe.SliceData(buffer), int64(len(buffer)), cfStringEncodingUTF8) == 0 {
		return ""
	}

	if end := bytes.IndexByte(buffer, 0); end >= 0 {
		buffer = buffer[:end]
	}

	return string(buffer)
}

// arrayValues returns the borrowed members of a CFArray. The array keeps them
// alive; a caller that needs one beyond the array's lifetime must retain it.
func (f *frameworks) arrayValues(array uintptr) []uintptr {
	if array == 0 {
		return nil
	}

	count := f.cf.arrayGetCount(array)
	if count <= 0 {
		return nil
	}

	values := make([]uintptr, count)
	for index := range values {
		values[index] = f.cf.arrayGetValueAtIndex(array, int64(index))
	}

	return values
}

// isCFString reports whether ref is a CFString, so a batch value of an unexpected
// type (an AX error placeholder, kCFNull) is skipped instead of being read as
// text.
func (f *frameworks) isCFString(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.cf.stringGetTypeID()
}

// cfBool reads a CoreFoundation boolean and reports whether ref really was one.
// An attribute that is missing or of another type stays distinguishable from an
// explicit false, which is what AXMinimized and AXMain need.
func (f *frameworks) cfBool(ref uintptr) (bool, bool) {
	if ref == 0 || f.cf.getTypeID(ref) != f.cf.booleanGetTypeID() {
		return false, false
	}

	return f.cf.booleanGetValue(ref) != 0, true
}

// isAXValue reports whether ref is an AXValue, the box AX uses for a point or a
// size.
func (f *frameworks) isAXValue(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.ax.valueGetTypeID()
}

// pointValue reads an AXValue holding a CGPoint.
func (f *frameworks) pointValue(ref uintptr) (cgPoint, bool) {
	var point cgPoint

	if !f.isAXValue(ref) || f.ax.valueGetType(ref) != axValueCGPointType {
		return point, false
	}

	if f.ax.valueGetValue(ref, axValueCGPointType, unsafe.Pointer(&point)) == 0 {
		return point, false
	}

	return point, true
}

// sizeValue reads an AXValue holding a CGSize.
func (f *frameworks) sizeValue(ref uintptr) (cgSize, bool) {
	var size cgSize

	if !f.isAXValue(ref) || f.ax.valueGetType(ref) != axValueCGSizeType {
		return size, false
	}

	if f.ax.valueGetValue(ref, axValueCGSizeType, unsafe.Pointer(&size)) == 0 {
		return size, false
	}

	return size, true
}

// formatWindowID renders "<pid>:<index>", the opaque ID this package returns.
//
// The index is the window's position in its application's AX window list, which
// is the closest thing to a stable identity AX offers: it is stable for as long
// as the application's window order is, which is from one List to the next on a
// desktop nobody is touching, and not across the activation that raises the
// window to the front.
func formatWindowID(pid int32, index int) string {
	return strconv.FormatInt(int64(pid), 10) + ":" + strconv.Itoa(index)
}

// parseWindowID reads back an ID formatWindowID produced.
func parseWindowID(id string) (int32, int, bool) {
	pidText, indexText, found := strings.Cut(strings.TrimSpace(id), ":")
	if !found {
		return 0, 0, false
	}

	pid, err := strconv.ParseInt(pidText, 10, 32)
	if err != nil || pid <= 0 {
		return 0, 0, false
	}

	index, err := strconv.Atoi(indexText)
	if err != nil || index < 0 {
		return 0, 0, false
	}

	return int32(pid), index, true
}

// Objective-C class and selector names are interned by the runtime, so the
// lookups below are cached and reused for the life of the process. The mutex
// covers the maps and the interning call together, because List runs on
// several goroutines and an unsynchronized map write would fault the process.
var (
	objcMu        sync.Mutex
	objcClasses   = make(map[string]uintptr, 2)
	objcSelectors = make(map[string]uintptr, 12)
)

// class returns the Objective-C class called name, or zero when no loaded image
// provides it.
func (f *frameworks) class(name string) uintptr {
	objcMu.Lock()
	defer objcMu.Unlock()

	if cached, ok := objcClasses[name]; ok {
		return cached
	}

	class := f.objc.getClass(name)
	objcClasses[name] = class

	return class
}

// selector returns the interned selector called name.
func (f *frameworks) selector(name string) uintptr {
	objcMu.Lock()
	defer objcMu.Unlock()

	if cached, ok := objcSelectors[name]; ok {
		return cached
	}

	selector := f.objc.registerName(name)
	objcSelectors[name] = selector

	return selector
}

// withPool runs fn inside an autorelease pool.
//
// Go threads have no run loop and therefore no pool of their own, so every
// Objective-C call that autoreleases (a string returned by a property, an array
// returned by a convenience method) must be bracketed or the process keeps the
// objects until it exits. The OS thread is locked for the duration because a pool
// belongs to the thread that pushed it: a goroutine that migrated to another
// thread between push and pop would pop the wrong one.
func (f *frameworks) withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pool := f.objc.poolPush()
	defer f.objc.poolPop(pool)

	fn()
}

// nsString copies an NSString into Go memory. It must be called inside withPool,
// because the string it reads is autoreleased with the pool.
func (f *frameworks) nsString(value uintptr) string {
	if value == 0 {
		return ""
	}

	return goCString(f.objc.msgSendCStr(value, f.selector("UTF8String")))
}

// goCString copies a NUL-terminated C string into Go memory. The names it carries
// are short; the cap keeps a malformed string from scanning the address space.
func goCString(value *byte) string {
	if value == nil {
		return ""
	}

	const maxLength = 4096

	buffer := make([]byte, 0, 64)

	for index := 0; index < maxLength; index++ {
		char := *(*byte)(unsafe.Add(unsafe.Pointer(value), index))
		if char == 0 {
			break
		}

		buffer = append(buffer, char)
	}

	return string(buffer)
}

// permissionError is the error the backend reports when macOS refuses
// accessibility access to this process. cause names the check that failed, so a
// caller reading the message can tell a missing grant from a revoked one.
func permissionError(cause string) error {
	return errs.Newf(
		errs.CodeAccessibilityDenied,
		"macOS Accessibility permission is not granted (%s); grant Accessibility "+
			"permission to this application in System Settings > Privacy & Security > "+
			"Accessibility",
		cause,
	)
}

// axCallFailure builds the coded error for a failed accessibility call, naming
// the call, the attribute and the process so the failure is diagnosable without a
// stack trace.
func axCallFailure(call, attribute string, status int32, pid int32) error {
	return errs.Newf(
		errs.CodeAccessibilityFailed,
		"%s failed for %s of process %d (AXError %d)",
		call,
		attribute,
		pid,
		status,
	)
}
