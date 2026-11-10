//go:build windows

package windows

import (
	"context"
	"image"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The UI Automation client driven through raw COM vtable calls.
//
// CGo is out of the question for this library, so UIA is reached the way C++
// reaches it: interface pointers whose first word is a vtable pointer, and a
// method at a fixed slot. The slot indices below were read from the
// UIAutomationClient type library's own function descriptors, which is what
// the IDL compiles to, and they have been stable since Windows 7.
//
// Every property is copied into a plain Go value before the object behind it
// is released, so no COM pointer escapes this file, and the COM client object
// is created and released inside a single query on a single locked OS thread.

var (
	ole32    = syscall.NewLazyDLL("ole32.dll")
	oleaut32 = syscall.NewLazyDLL("oleaut32.dll")

	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")
	procVariantClear     = oleaut32.NewProc("VariantClear")
)

const (
	// coinitMultithreaded is COINIT_MULTITHREADED. UIA enumeration runs on a
	// goroutine with no Windows message pump, so the multithreaded apartment
	// is required: an STA caller would need to pump messages, and Microsoft
	// recommends MTA for UIA calls made off the UI thread.
	coinitMultithreaded = 0x0
	clsctxInprocServer  = 0x1

	// hresultOK and hresultFalse are the two successes that mean this call
	// owns COM initialization on the thread, and must balance it with
	// CoUninitialize. hresultChangedMode means the thread already belongs to
	// another apartment and must be left alone.
	hresultOK          = 0
	hresultFalse       = 1
	hresultChangedMode = 0x80010106

	// ePointer is returned by comCall for a nil interface pointer instead of
	// dereferencing it.
	ePointer = 0x80004003
)

// The COM class and interface this backend instantiates.
var (
	// clsidCUIAutomation is CLSID_CUIAutomation, the UI Automation client
	// object. The later CUIAutomation8 adds text-pattern v2 and connection
	// recovery, neither of which this backend uses.
	clsidCUIAutomation = guid{
		data1: 0xFF48DBA4,
		data2: 0x60EF,
		data3: 0x4201,
		data4: [8]byte{0xAA, 0x87, 0x54, 0x10, 0x3E, 0xEF, 0x59, 0x4E},
	}

	// iidIUIAutomation is IID_IUIAutomation.
	iidIUIAutomation = guid{
		data1: 0x30CBE57D,
		data2: 0xD9D0,
		data3: 0x452A,
		data4: [8]byte{0xAB, 0x13, 0x7A, 0xC5, 0xAC, 0x48, 0x25, 0xEE},
	}
)

// guid is the 16-byte layout CoCreateInstance expects.
type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// Vtable slots, with IUnknown occupying 0, 1 and 2. They match
// UIAutomationClient.h, which is what the type library reports.
const (
	slotRelease = 2

	// IUIAutomation.
	slotElementFromHandle    = 6
	slotControlViewWalker    = 14
	slotControlViewCondition = 18
	slotCreateCacheRequest   = 20

	// IUIAutomationTreeWalker.
	slotWalkerGetFirstChild  = 10
	slotWalkerGetNextSibling = 12

	// IUIAutomationElement. Only the cached getters are called, because every
	// property the walk reads was fetched by the cache request; a Current*
	// getter would be a second cross-process call for data already in hand.
	slotCachedPropertyValue     = 12
	slotCachedPattern           = 17
	slotCachedControlType       = 53
	slotCachedName              = 55
	slotCachedHasKeyboardFocus  = 58
	slotCachedIsEnabled         = 60
	slotCachedAutomationID      = 61
	slotCachedHelpText          = 63
	slotCachedIsPassword        = 67
	slotCachedIsOffscreen       = 70
	slotCachedBoundingRectangle = 75

	// IUIAutomationCacheRequest.
	slotCacheAddProperty    = 3
	slotCacheAddPattern     = 4
	slotCachePutTreeFilter  = 9
	slotCachePutElementMode = 11
)

// UIA property ids the cache request prefetches (UIA_*PropertyId).
const (
	propBoundingRectangle = 30001
	propControlType       = 30003
	propName              = 30005
	propHasKeyboardFocus  = 30008
	propIsEnabled         = 30010
	propAutomationID      = 30011
	propHelpText          = 30013
	propIsPassword        = 30019
	propIsOffscreen       = 30022
	propValueValue        = 30045
)

// UIA pattern ids whose presence means the provider can act on the control.
const (
	patternInvoke        = 10000
	patternSelectionItem = 10010
	patternToggle        = 10015
)

// actionPatterns are the patterns a clickable control is expected to expose.
// They are cached rather than queried, so checking all three costs no
// cross-process calls per control.
var actionPatterns = [...]uintptr{patternInvoke, patternToggle, patternSelectionItem}

// vtBSTR is the VARTYPE of a VARIANT holding a BSTR.
const vtBSTR = 8

// automationElementModeFull keeps a live reference back to the provider on
// every cached element, which a walker needs to move from an element to its
// children and siblings. The alternative, AutomationElementMode_None, would
// hand back property-only copies that cannot be walked.
const automationElementModeFull = 1

// UI Automation control-type names referenced from more than one place.
const (
	controlButton   = "Button"
	controlEdit     = "Edit"
	controlCustom   = "Custom"
	controlPane     = "Pane"
	controlCheckBox = "CheckBox"
	controlSplit    = "SplitButton"
)

// controlTypeNames maps UI Automation CONTROLTYPEID values to the programmatic
// names behind the UIA_*ControlTypeId constants. These names are the
// vocabulary a caller addresses with the "uia:" prefix. LocalizedControlType is
// not, because it changes with the system language.
//
// The range is contiguous and stable: 50000 is Button and 50040 is AppBar, the
// last control type added (Windows 8).
var controlTypeNames = map[int32]string{
	50000: controlButton,
	50001: "Calendar",
	50002: controlCheckBox,
	50003: "ComboBox",
	50004: controlEdit,
	50005: "Hyperlink",
	50006: "Image",
	50007: "ListItem",
	50008: "List",
	50009: "Menu",
	50010: "MenuBar",
	50011: "MenuItem",
	50012: "ProgressBar",
	50013: "RadioButton",
	50014: "ScrollBar",
	50015: "Slider",
	50016: "Spinner",
	50017: "StatusBar",
	50018: "Tab",
	50019: "TabItem",
	50020: "Text",
	50021: "ToolBar",
	50022: "ToolTip",
	50023: "Tree",
	50024: "TreeItem",
	50025: controlCustom,
	50026: "Group",
	50027: "Thumb",
	50028: "DataGrid",
	50029: "DataItem",
	50030: "Document",
	50031: controlSplit,
	50032: controlTypeWindow,
	50033: controlPane,
	50034: "Header",
	50035: "HeaderItem",
	50036: "Table",
	50037: "TitleBar",
	50038: "Separator",
	50039: "SemanticZoom",
	50040: "AppBar",
}

// variant mirrors the Win32 VARIANT: a VARTYPE, three reserved words, and a
// union two pointers wide, which is 16 bytes on 386 and 24 on 64-bit targets.
// Only the BSTR member of the union is read.
type variant struct {
	vt       uint16
	reserved [3]uint16
	bstr     *uint16
	_        uintptr
}

// control is the extracted, COM-free description of one control.
type control struct {
	bounds      image.Rectangle
	role        string
	title       string
	description string
	value       string
	identifier  string
	focused     bool
	clickable   bool
}

// roleFilter is the set of UIA control-type names a query keeps.
//
// Names are matched case-insensitively, because a provider's programmatic name
// and a configuration entry can disagree on case ("tabitem" against "TabItem")
// without meaning different things. An empty filter keeps every role, which is
// the safe reading of "no filter".
type roleFilter struct {
	names map[string]struct{}
}

// newRoleFilter builds a filter from native UIA control-type names, dropping
// empty entries.
func newRoleFilter(names []string) roleFilter {
	filter := roleFilter{names: make(map[string]struct{}, len(names))}

	for _, name := range names {
		name = trimLower(name)
		if name == "" {
			continue
		}

		filter.names[name] = struct{}{}
	}

	return filter
}

// matches reports whether a control type name is in the filter.
func (f roleFilter) matches(role string) bool {
	if len(f.names) == 0 {
		return true
	}

	_, ok := f.names[trimLower(role)]

	return ok
}

// comCall invokes the method at vtable slot index on the COM object this. It
// returns the HRESULT (or boolean/handle) the method produced.
//
// The out-parameter addresses in args are uintptrs, which the runtime does not
// track. If the stack grows before SyscallN runs, the caller's variables move
// and COM writes into the old frame. //go:uintptrescapes moves them to the
// heap and keeps them alive for the call, as it does for syscall.Proc.Call.
//
//go:uintptrescapes
func comCall(this unsafe.Pointer, index int, args ...uintptr) uintptr {
	if this == nil {
		return ePointer
	}

	vtable := *(*unsafe.Pointer)(this)
	method := *(*uintptr)(unsafe.Add(vtable, uintptr(index)*unsafe.Sizeof(uintptr(0))))

	full := make([]uintptr, 0, len(args)+1)
	full = append(full, uintptr(this))
	full = append(full, args...)

	result, _, _ := syscall.SyscallN(method, full...)

	return result
}

// hrFailed reports whether an HRESULT indicates failure (high bit set).
func hrFailed(hresult uintptr) bool {
	return int32(hresult) < 0
}

// releaseCom releases one COM interface pointer. A release has no actionable
// failure path, so its result is dropped.
func releaseCom(object unsafe.Pointer) {
	if object != nil {
		comCall(object, slotRelease)
	}
}

// hrError builds the coded error for a failed HRESULT, naming the call and
// keeping the raw value so an unexpected one is diagnosable.
func hrError(operation string, hresult uintptr) error {
	return errs.Newf(
		errs.CodeAccessibilityFailed,
		"%s failed with HRESULT 0x%08X",
		operation,
		uint32(hresult),
	)
}

// coInitialize joins the multithreaded apartment on the calling thread and
// reports whether this call owns the initialization.
//
// A thread already in another apartment reports E_CHANGED_MODE. That is not an
// error: in-process objects can still be created on it, and the caller then
// creates its own UIA object rather than sharing the process-wide one. The
// caller must balance a true result with CoUninitialize.
func coInitialize() (bool, error) {
	hresult, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)

	switch uint32(hresult) {
	case hresultOK, hresultFalse:
		return true, nil
	case hresultChangedMode:
		return false, nil
	default:
		return false, hrError("CoInitializeEx", hresult)
	}
}

// createAutomation creates one IUIAutomation, the UI Automation client object.
func createAutomation() (unsafe.Pointer, error) {
	var automation unsafe.Pointer

	hresult, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)),
		uintptr(unsafe.Pointer(&automation)),
	)
	if hrFailed(hresult) || automation == nil {
		return nil, hrError("CoCreateInstance(CUIAutomation)", hresult)
	}

	return automation, nil
}

// withAutomation runs fn on a locked OS thread with COM initialized and a UIA
// client object available, and releases that object before returning.
//
// The object is created per query rather than kept on the Client because a COM
// object belongs to an apartment: a goroutine can be rescheduled onto another
// thread between calls, and a pointer created in one apartment is not valid in
// another without marshaling. Creating it inside the call means it is always
// used on a thread that initialized COM for it, and there is no shared state to
// release later.
func (c *Client) withAutomation(fn func(automation unsafe.Pointer) error) error {
	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()

	if closed {
		return errs.New(errs.CodeAccessibilityFailed, "accessibility client is closed")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	owns, err := coInitialize()
	if err != nil {
		return err
	}
	if owns {
		defer procCoUninitialize.Call()
	}

	automation, err := createAutomation()
	if err != nil {
		return err
	}
	defer releaseCom(automation)

	return fn(automation)
}

// elementFromHandle returns the UIA element for a top-level window handle.
func elementFromHandle(automation unsafe.Pointer, hwnd uintptr) (unsafe.Pointer, error) {
	var element unsafe.Pointer

	hresult := comCall(automation, slotElementFromHandle, hwnd, uintptr(unsafe.Pointer(&element)))
	if hrFailed(hresult) || element == nil {
		return nil, hrError("IUIAutomation::ElementFromHandle", hresult)
	}

	return element, nil
}

// controlViewWalker returns the walker that traverses the control view: the
// tree a user sees, without the scaffolding a provider exposes to automation.
func controlViewWalker(automation unsafe.Pointer) (unsafe.Pointer, error) {
	var walker unsafe.Pointer

	hresult := comCall(automation, slotControlViewWalker, uintptr(unsafe.Pointer(&walker)))
	if hrFailed(hresult) || walker == nil {
		return nil, hrError("IUIAutomation::ControlViewWalker", hresult)
	}

	return walker, nil
}

// controlViewCondition returns the condition that matches control-view
// elements.
func controlViewCondition(automation unsafe.Pointer) (unsafe.Pointer, error) {
	var condition unsafe.Pointer

	hresult := comCall(automation, slotControlViewCondition, uintptr(unsafe.Pointer(&condition)))
	if hrFailed(hresult) || condition == nil {
		return nil, hrError("IUIAutomation::ControlViewCondition", hresult)
	}

	return condition, nil
}

// createCacheRequest builds the cache request the walker fills for every
// element it returns.
//
// It holds the properties extractControl and readControlDetails read, the three
// action patterns a clickable control is expected to expose, the control-view
// filter, and the Full element mode the walker needs to reach an element's
// children. The identifier and focus properties are added only with readState:
// the provider computes every cached property for every control, so asking for
// state nobody reads would tax every query.
func createCacheRequest(
	automation, filter unsafe.Pointer,
	readState bool,
) (unsafe.Pointer, error) {
	var cache unsafe.Pointer

	hresult := comCall(automation, slotCreateCacheRequest, uintptr(unsafe.Pointer(&cache)))
	if hrFailed(hresult) || cache == nil {
		return nil, hrError("IUIAutomation::CreateCacheRequest", hresult)
	}

	properties := []uintptr{
		propControlType, propName, propBoundingRectangle, propIsOffscreen,
		propIsEnabled, propHelpText, propIsPassword, propValueValue,
	}
	if readState {
		properties = append(properties, propAutomationID, propHasKeyboardFocus)
	}

	for _, property := range properties {
		if hresult := comCall(cache, slotCacheAddProperty, property); hrFailed(hresult) {
			releaseCom(cache)

			return nil, hrError("IUIAutomationCacheRequest::AddProperty", hresult)
		}
	}

	for _, pattern := range actionPatterns {
		if hresult := comCall(cache, slotCacheAddPattern, pattern); hrFailed(hresult) {
			releaseCom(cache)

			return nil, hrError("IUIAutomationCacheRequest::AddPattern", hresult)
		}
	}

	if hresult := comCall(cache, slotCachePutTreeFilter, uintptr(filter)); hrFailed(hresult) {
		releaseCom(cache)

		return nil, hrError("IUIAutomationCacheRequest::put_TreeFilter", hresult)
	}

	// The walker reads children itself, so the filter above is belt and
	// braces, but the element mode is not optional: a cached element with no
	// live reference cannot report its siblings.
	hresult = comCall(
		cache,
		slotCachePutElementMode,
		uintptr(automationElementModeFull),
	)
	if hrFailed(hresult) {
		releaseCom(cache)

		return nil, hrError("IUIAutomationCacheRequest::put_AutomationElementMode", hresult)
	}

	return cache, nil
}

// walkerFirstChild returns the first control-view child of parent, cached as
// cache says. The second result is false when there is none.
func walkerFirstChild(
	walker, parent, cache unsafe.Pointer,
) (unsafe.Pointer, bool) {
	var child unsafe.Pointer

	hresult := comCall(
		walker,
		slotWalkerGetFirstChild,
		uintptr(parent),
		uintptr(cache),
		uintptr(unsafe.Pointer(&child)),
	)
	if hrFailed(hresult) || child == nil {
		return nil, false
	}

	return child, true
}

// walkerNextSibling returns node's next control-view sibling, cached as cache
// says. The second result is false when node is the last one.
func walkerNextSibling(
	walker, node, cache unsafe.Pointer,
) (unsafe.Pointer, bool) {
	var sibling unsafe.Pointer

	hresult := comCall(
		walker,
		slotWalkerGetNextSibling,
		uintptr(node),
		uintptr(cache),
		uintptr(unsafe.Pointer(&sibling)),
	)
	if hrFailed(hresult) || sibling == nil {
		return nil, false
	}

	return sibling, true
}

// walkBounded walks the child tree below root depth first, calling visit for
// every node from depth one down to maxDepth, and release on every node it
// obtained once that node's subtree has been walked. stop is consulted between
// nodes so a canceled query ends promptly.
//
// It is generic over the node type so the depth accounting can be tested
// without a desktop; the production caller uses uintptr-sized COM pointers.
func walkBounded[T comparable](
	root T,
	maxDepth int,
	firstChild func(T) (T, bool),
	nextSibling func(T) (T, bool),
	release func(T),
	stop func() bool,
	visit func(T, int),
) {
	var zero T

	var walk func(parent T, depth int)

	walk = func(parent T, depth int) {
		if depth > maxDepth || stop() {
			return
		}

		child, ok := firstChild(parent)
		for ok && child != zero {
			if stop() {
				release(child)

				return
			}

			visit(child, depth)
			walk(child, depth+1)

			// The sibling must be read while the child is still alive, so the
			// release comes after it rather than with the loop body.
			sibling, hasSibling := nextSibling(child)

			release(child)

			child, ok = sibling, hasSibling
		}
	}

	walk(root, 1)
}

// findControls walks the control view below hwnd and returns the controls that
// match roles and have a usable rectangle inside frame, stopping once limit
// controls have been kept (a limit of zero or less means no limit).
//
// frame is the window's visible frame, already clipped to the screen. An
// empty frame means the frame is unknown and controls are clipped to the
// screen alone. A window with nothing on screen yields no controls at all.
//
// The tree is walked one element at a time rather than fetched in a single
// FindAll, because FindAll cannot stop at a depth: honouring the bound the
// caller asked for is what costs a round trip per element. A caller that
// lowers maxDepth therefore pays less, not more.
func findControls(
	ctx context.Context,
	automation unsafe.Pointer,
	hwnd uintptr,
	frame image.Rectangle,
	roles roleFilter,
	maxDepth int,
	readState bool,
	limit int,
) ([]control, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	screen := virtualScreenBounds()

	visible := frame
	if visible.Empty() {
		visible = screen
	} else {
		visible = visible.Intersect(screen)
	}

	if visible.Empty() {
		return nil, nil
	}

	root, err := elementFromHandle(automation, hwnd)
	if err != nil {
		return nil, err
	}
	defer releaseCom(root)

	walker, err := controlViewWalker(automation)
	if err != nil {
		return nil, err
	}
	defer releaseCom(walker)

	filter, err := controlViewCondition(automation)
	if err != nil {
		return nil, err
	}
	defer releaseCom(filter)

	cache, err := createCacheRequest(automation, filter, readState)
	if err != nil {
		return nil, err
	}
	defer releaseCom(cache)

	var controls []control

	// The walk stops on cancellation and on a spent node budget, so a wide
	// tree costs no more than the caller asked for.
	stop := func() bool {
		return ctx.Err() != nil || (limit > 0 && len(controls) >= limit)
	}

	walkBounded[unsafe.Pointer](
		root,
		maxDepth,
		func(parent unsafe.Pointer) (unsafe.Pointer, bool) {
			return walkerFirstChild(walker, parent, cache)
		},
		func(node unsafe.Pointer) (unsafe.Pointer, bool) {
			return walkerNextSibling(walker, node, cache)
		},
		releaseCom,
		stop,
		func(node unsafe.Pointer, _ int) {
			if extracted, ok := extractControl(node, visible, roles, readState); ok {
				controls = append(controls, extracted)
			}
		},
	)

	return controls, nil
}

// extractControl copies the properties of one element into a control and
// reports whether it is a control this query keeps.
//
// A control is dropped when its role is filtered out, when UIA reports it
// offscreen, when it is disabled (a coordinate click on a disabled control
// does nothing, so it would become a hint that never works), when its
// rectangle is empty, or when its rectangle lies entirely outside the window
// frame and the screen.
//
// Role selection happens before the name and bounds are decoded so a rejected
// element costs one cached read, and the clip to the frame catches the controls
// a provider keeps laying out past the window edge while reporting IsOffscreen
// false, such as the hidden part of a long vertical tab strip.
func extractControl(
	element unsafe.Pointer,
	visible image.Rectangle,
	roles roleFilter,
	readState bool,
) (control, bool) {
	var controlType int32

	if hrFailed(comCall(element, slotCachedControlType, uintptr(unsafe.Pointer(&controlType)))) {
		return control{}, false
	}

	role, known := controlTypeName(controlType)
	if !known {
		// A third-party provider may report a control type outside the
		// documented range. The numeric id keeps it addressable rather than
		// dropping it with no way to name it.
		role = strconv.FormatInt(int64(controlType), 10)
	}

	if !roles.matches(role) {
		return control{}, false
	}

	if cachedBool(element, slotCachedIsOffscreen, false) {
		return control{}, false
	}

	if !cachedBool(element, slotCachedIsEnabled, true) {
		return control{}, false
	}

	var bounds win32Rect
	if hrFailed(
		comCall(element, slotCachedBoundingRectangle, uintptr(unsafe.Pointer(&bounds))),
	) {
		return control{}, false
	}

	clipped, usable := clipToVisible(bounds.rect(), visible)
	if !usable {
		return control{}, false
	}

	extracted := control{
		bounds: clipped,
		role:   role,
		title:  cachedString(element, slotCachedName),
	}

	// A control is clickable when UIA says it can be invoked, toggled or
	// selected, or when it has a rectangle on screen. The second term is the
	// one that matters here: the action path is coordinate injection, so a
	// click lands on any rectangle, and a control whose provider implements no
	// pattern at all (a legacy pane, a custom-drawn button) is still a target.
	// Because a control with no usable rectangle was dropped above, every node
	// this package returns is clickable; the pattern check is what makes that
	// answer UIA's own definition rather than a coincidence of the clip.
	interactive := hasActionPattern(element)
	extracted.clickable = interactive || usable

	extracted.description = cachedString(element, slotCachedHelpText)

	// A password field's value is never read, so a secret cannot reach a
	// caller or a log through a hint's search text.
	if !cachedBool(element, slotCachedIsPassword, true) {
		extracted.value = cachedValue(element)
	}

	if readState {
		extracted.identifier = cachedString(element, slotCachedAutomationID)
		extracted.focused = cachedBool(element, slotCachedHasKeyboardFocus, false)
	}

	return extracted, true
}

// hasActionPattern reports whether the cached element exposes Invoke, Toggle
// or SelectionItem. All three were added to the cache request, so each check
// reads client-side data instead of crossing into the provider.
func hasActionPattern(element unsafe.Pointer) bool {
	for _, pattern := range actionPatterns {
		var object unsafe.Pointer

		hresult := comCall(
			element,
			slotCachedPattern,
			pattern,
			uintptr(unsafe.Pointer(&object)),
		)
		if hrFailed(hresult) || object == nil {
			continue
		}

		releaseCom(object)

		return true
	}

	return false
}

// clipToVisible returns the part of bounds inside visible, and false when
// nothing of it is left.
//
// The clip is what drops a control a provider keeps laying out past the window
// edge: Chromium leaves IsOffscreen false for the hidden part of a long
// vertical tab strip, so the rectangles are the only evidence that no click
// could land there.
func clipToVisible(bounds, visible image.Rectangle) (image.Rectangle, bool) {
	clipped := bounds.Intersect(visible)
	if clipped.Empty() {
		return image.Rectangle{}, false
	}

	return clipped, true
}

// cachedString reads the cached BSTR property behind the getter at slot and
// frees the BSTR the provider allocated.
func cachedString(element unsafe.Pointer, slot int) string {
	var bstr *uint16

	if hrFailed(comCall(element, slot, uintptr(unsafe.Pointer(&bstr)))) || bstr == nil {
		return ""
	}

	text := utf16PtrToString(bstr)

	procSysFreeString.Call(uintptr(unsafe.Pointer(bstr)))

	return text
}

// cachedBool reads the cached BOOL property behind the getter at slot, or
// fallback when the provider cannot answer.
func cachedBool(element unsafe.Pointer, slot int, fallback bool) bool {
	var value int32

	if hrFailed(comCall(element, slot, uintptr(unsafe.Pointer(&value)))) {
		return fallback
	}

	return value != 0
}

// cachedValue reads the Value property's cached string. A control without a
// Value pattern answers with an empty VARIANT, which reads as "".
func cachedValue(element unsafe.Pointer) string {
	var value variant

	hresult := comCall(
		element,
		slotCachedPropertyValue,
		propValueValue,
		uintptr(unsafe.Pointer(&value)),
	)
	if hrFailed(hresult) {
		return ""
	}

	defer procVariantClear.Call(uintptr(unsafe.Pointer(&value)))

	if value.vt != vtBSTR || value.bstr == nil {
		return ""
	}

	return utf16PtrToString(value.bstr)
}

// utf16PtrToString copies a NUL-terminated UTF-16 string, which is what every
// BSTR a UIA getter hands back is. The scan is capped, because a provider that
// forgot the terminator would otherwise walk off its allocation.
func utf16PtrToString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}

	// One mebibyte of UTF-16 is far past any control name, value or help text
	// a provider reports, and it keeps a malformed string from running away.
	const limit = 1 << 19

	for length := range limit {
		if unsafe.Slice(ptr, length+1)[length] == 0 {
			return syscall.UTF16ToString(unsafe.Slice(ptr, length))
		}
	}

	return ""
}

// controlTypeName returns the programmatic name for a UIA CONTROLTYPEID. The
// second result is false for ids outside the documented range.
func controlTypeName(controlType int32) (string, bool) {
	name, ok := controlTypeNames[controlType]
	if !ok {
		return "", false
	}

	return name, true
}

// ctxErr returns a coded cancellation error when ctx is already done.
//
// A UIA query is a blocking cross-process call that cannot be interrupted, so
// the context is checked around it rather than inside it: a deadline that has
// already passed skips the query, and one that passes during it discards the
// result instead of handing nodes to a caller that has stopped waiting.
func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.CodeContextCanceled, "accessibility query canceled")
	}

	return nil
}

// trimLower is the case folding the role filter compares with. It is a named
// function so the filter's rules are stated in one place.
func trimLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
