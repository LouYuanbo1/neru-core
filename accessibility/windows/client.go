//go:build windows

package windows

import (
	"context"
	"image"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

// DefaultMaxDepth is the deepest control-view level a query reads when neither
// the call nor the client asks for another bound.
//
// The bound exists because a malformed or adversarial provider can present a
// tree of unbounded depth, and because the walk costs one cross-process call
// per element. Real applications nest a handful of levels, so a limit far
// below this would start truncating ordinary dialog boxes.
const DefaultMaxDepth = 50

// DefaultMaxNodes is the most nodes one query returns when neither the call
// nor the client asks for another bound.
//
// Depth alone is a weak bound on cost: a wide tree can hold thousands of
// controls per level, and a screen only has room for a few hundred hints. The
// budget stops the walk once it is spent, so a pathological tree cannot make a
// query run for minutes.
const DefaultMaxNodes = 4000

// Options configures a Client.
type Options struct {
	// ClickableRoles are the native UIA control-type names the client treats
	// as hint targets, such as "Button" or "TabItem". They are matched
	// case-insensitively.
	//
	// Empty means element.DefaultClickableNativeRoles(element.VocabularyUIA),
	// so a caller that passes no filter selects exactly the elements a default
	// configuration selects.
	ClickableRoles []string
	// MaxDepth bounds how many control-view levels below the root a query
	// reads. Zero or negative means DefaultMaxDepth.
	//
	// It is the default for the client: a positive maxDepth argument to
	// ClickableNodes still overrides it for that call.
	MaxDepth int
	// MaxNodes bounds how many nodes one query returns, across every window it
	// scans. Zero or negative means DefaultMaxNodes.
	//
	// The walk stops as soon as the budget is spent, so the limit bounds the
	// work as well as the result.
	MaxNodes int
}

// defaultClickableRoles is the shipped default role list resolved into UIA
// control-type names. It is resolved once, because the vocabulary tables are
// static and every query needs it.
var defaultClickableRoles = element.DefaultClickableNativeRoles(element.VocabularyUIA)

// Client is the Windows accessibility backend. It implements ax.Client.
//
// A Client holds no COM object between calls: every query creates a UIA client
// on a thread that initialized COM for it, copies what it needs into Go
// values, and releases the object before returning. What the Client does keep
// is the role filter, the depth and node bounds, and a node counter, all safe
// for concurrent use.
type Client struct {
	// rolesMu guards roles, so a configuration reload cannot race a query.
	rolesMu sync.RWMutex
	roles   []string

	// maxDepth and maxNodes are the client-wide bounds, resolved at
	// construction so a query never has to re-decide what "no bound" means.
	maxDepth int
	maxNodes int

	// seq numbers the nodes a query returns, so every Node ID this client has
	// produced is unique.
	seq atomic.Uint64

	// stateMu guards closed.
	stateMu sync.Mutex
	closed  bool
}

// New creates a Windows accessibility client.
//
// It makes the process per-monitor-v2 DPI aware before anything else, so the
// rectangles UIA reports and the coordinates this backend later injects are
// both physical pixels. It then creates and releases one UI Automation client,
// so a system where UIA is unavailable fails here rather than on the first
// query.
func New(opts Options) (*Client, error) {
	ensurePerMonitorV2()

	if err := probeAutomation(); err != nil {
		return nil, err
	}

	return &Client{
		roles:    append([]string(nil), opts.ClickableRoles...),
		maxDepth: normalizeMaxDepth(opts.MaxDepth, DefaultMaxDepth),
		maxNodes: normalizeMaxNodes(opts.MaxNodes),
	}, nil
}

// probeAutomation creates and releases one UIA client object.
func probeAutomation() error {
	return (&Client{}).withAutomation(func(unsafe.Pointer) error { return nil })
}

// FrontmostWindow returns the window the user is working in.
//
// It is resolved through GetForegroundWindow, which is what Windows itself
// considers frontmost; when no application has focus, or the desktop does, the
// error carries CodeElementNotFound so a caller can treat it as "nothing to
// scan".
func (c *Client) FrontmostWindow(ctx context.Context) (ax.Window, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	hwnd, err := foregroundWindow()
	if err != nil {
		return nil, err
	}

	window, err := newWindowElement(hwnd)
	if err != nil {
		return nil, err
	}

	return window, nil
}

// AllWindows returns the visible top-level windows of the foreground
// application, the frontmost one first.
//
// The foreground window's process id is what names "the focused application"
// on Windows, since a process is not an accessibility object. A window with no
// title is skipped unless it is the foreground one, which keeps helper windows
// from becoming hint targets.
func (c *Client) AllWindows(ctx context.Context) ([]ax.Window, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	hwnd, err := foregroundWindow()
	if err != nil {
		return nil, err
	}

	pid, err := windowProcessID(hwnd)
	if err != nil {
		return nil, err
	}

	handles := processWindows(pid)

	windows := make([]ax.Window, 0, len(handles))

	for _, handle := range handles {
		window, windowErr := newWindowElement(handle)
		if windowErr != nil {
			// A window can be destroyed between the enumeration and the
			// read; that is not a reason to lose the windows after it.
			continue
		}

		windows = append(windows, window)
	}

	if len(windows) == 0 {
		return nil, errs.Newf(
			errs.CodeElementNotFound,
			"process %d has no visible window",
			pid,
		)
	}

	return windows, nil
}

// FrontmostAndPopoverWindows returns the same list as AllWindows.
//
// Windows has no popover windows distinct from windows: a popup, a menu and a
// dialog are all top-level windows or controls inside one, and UIA enumerates
// them under the window that owns them. The contract exists for macOS, where
// a popover really is a separate surface, so this backend answers it with the
// windows it has.
func (c *Client) FrontmostAndPopoverWindows(ctx context.Context) ([]ax.Window, error) {
	return c.AllWindows(ctx)
}

// FocusedApplication returns the application that owns the foreground window.
//
// The application is identified by its process, and BundleIdentifier reports
// that process's executable base name, which is the Windows analogue of a
// bundle id.
func (c *Client) FocusedApplication(ctx context.Context) (ax.App, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	hwnd, err := foregroundWindow()
	if err != nil {
		return nil, err
	}

	pid, err := windowProcessID(hwnd)
	if err != nil {
		return nil, err
	}

	return &App{pid: pid, exe: processExeName(pid)}, nil
}

// ApplicationByBundleID returns the running application whose executable base
// name matches bundleID.
//
// There is no bundle identifier on Windows, so the executable's name is used
// instead: "notepad.exe" or a full path to it, compared case-insensitively. A
// caller that needs a macOS bundle id ("com.apple.TextEdit") has no Windows
// equivalent; the mapping is the process name, and a name that matches no
// running application reports CodeElementNotFound.
func (c *Client) ApplicationByBundleID(ctx context.Context, bundleID string) (ax.App, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	pid, exe, err := lookupApplication(bundleID)
	if err != nil {
		return nil, err
	}

	return &App{pid: pid, exe: exe}, nil
}

// ClickableNodes returns the clickable controls in the UIA subtree below root.
//
// root must be a *Window or an *App. A window is walked directly; an
// application is walked across every visible top-level window its process
// owns. A caller that has any ax.Element from an earlier query can type-assert
// it back to one of those two types.
//
// roles is the native UIA role filter and overrides the client's own; when it
// is empty the client's ClickableRoles are used. maxDepth bounds how many
// control-view levels below the root are read; a value of zero or less falls
// back to the client's MaxDepth, and that in turn to DefaultMaxDepth. The
// client's MaxNodes bounds the result and the work behind it. readState asks
// for each control's AutomationId and keyboard-focus state: both are cached
// properties read in the same query, so it is a hint about how much to fetch
// rather than a separate pass, and a node queried without it reports an empty
// Identifier and a false IsFocused.
//
// The returned nodes have no COM references and may outlive the query. A
// disabled control is not returned, because a coordinate click on one does
// nothing.
func (c *Client) ClickableNodes(
	ctx context.Context,
	root ax.Element,
	roles []string,
	maxDepth int,
	readState bool,
) ([]ax.Node, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	handles, err := rootWindowHandles(root)
	if err != nil {
		return nil, err
	}

	filter := newRoleFilter(c.resolvedRoles(roles))
	depth := normalizeMaxDepth(maxDepth, c.maxDepth)
	// A zero-value Client has no node bound, so the default is applied here
	// rather than in the loop, where a zero budget would silently return
	// nothing.
	budget := normalizeMaxNodes(c.maxNodes)

	var (
		nodes    []ax.Node
		firstErr error
	)

	queryErr := c.withAutomation(func(automation unsafe.Pointer) error {
		for _, hwnd := range handles {
			if err := ctxErr(ctx); err != nil {
				return err
			}

			remaining := budget - len(nodes)
			if remaining <= 0 {
				break
			}

			controls, findErr := findControls(
				ctx,
				automation,
				hwnd,
				visibleWindowFrame(hwnd),
				filter,
				depth,
				readState,
				remaining,
			)
			if findErr != nil {
				if firstErr == nil {
					firstErr = findErr
				}

				continue
			}

			for _, extracted := range controls {
				nodes = append(nodes, c.newNode(extracted))
			}
		}

		return nil
	})
	if queryErr != nil {
		return nil, queryErr
	}

	if len(nodes) == 0 && firstErr != nil {
		return nil, firstErr
	}

	return nodes, nil
}

// MenuBarClickableElements reports CodeNotSupported.
//
// Windows has no application menu bar of the kind macOS owns: an application's
// menus are either controls inside its own window, which ClickableNodes
// already walks, or a menu-bar control whose identity is not a stable,
// enumerable surface across frameworks. Rather than return a partial answer
// that looks like a working menu scan, this backend declines, and a caller can
// degrade through errs.IsNotSupported.
func (c *Client) MenuBarClickableElements(ctx context.Context, maxDepth int) ([]ax.Node, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"Windows exposes application menus as controls inside each window, not as a menu bar this backend can scan",
	)
}

// MissionControlClickableNodes reports CodeNotSupported, because Mission
// Control is a macOS surface with no Windows equivalent.
func (c *Client) MissionControlClickableNodes(ctx context.Context, maxDepth int) ([]ax.Node, error) {
	return nil, errs.New(errs.CodeNotSupported, "Mission Control is macOS-only")
}

// ClickableElementsFromBundleID returns the clickable controls of the
// application whose executable base name matches bundleID.
//
// It is the supplementary-surface entry point of the contract. Windows has
// none of the macOS auxiliary surfaces, so the only thing it can resolve this
// way is another application's own windows, which is what it does.
// readState is not available through this method, so the nodes it returns
// report no identifier and no focus state.
func (c *Client) ClickableElementsFromBundleID(
	ctx context.Context,
	bundleID string,
	roles []string,
	maxDepth int,
) ([]ax.Node, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	pid, exe, err := lookupApplication(bundleID)
	if err != nil {
		return nil, err
	}

	return c.ClickableNodes(ctx, &App{pid: pid, exe: exe}, roles, maxDepth, false)
}

// ActiveScreenBounds returns the whole virtual desktop in physical pixels:
// every monitor unioned, with a negative origin when a display sits left of or
// above the primary.
func (c *Client) ActiveScreenBounds() image.Rectangle {
	ensurePerMonitorV2()

	return virtualScreenBounds()
}

// PerformAction performs one mouse action at point.
//
// Clicks, presses, releases and toggles move the cursor to point and inject
// the event through SendInput, holding the modifiers the caller passed for the
// duration of the whole action. restoreCursor asks for the cursor to be put
// back where it was once the action has been injected, which is what a hint
// activation wants: the click lands on the target and the pointer does not
// visibly jump. The move back is a warp rather than injected motion, so it
// generates no pointer movement for an application to read as a drag.
//
// A toggle presses the button when it is up and releases it when it is held,
// reading the real button state rather than remembering what this process did.
// move_mouse and move_mouse_relative move the cursor; scroll is rejected here
// because it has its own method and needs a delta rather than a point.
func (c *Client) PerformAction(
	actionType action.Type,
	point image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	switch {
	case actionType.IsMoveMouse():
		_ = moveCursorTo(point)

		return nil
	case actionType == action.TypeScroll:
		return errs.Newf(
			errs.CodeInvalidInput,
			"scroll actions go through Scroll, not PerformAction: %s",
			actionType,
		)
	}

	button, phase, ok := actionType.MouseButtonPhase()
	if !ok {
		return errs.Newf(errs.CodeInvalidInput, "unsupported action type: %s", actionType)
	}

	return performMouseAction(point, button, phase, restoreCursor, modifiers)
}

// performMouseAction injects one button action, holding modifiers for its
// duration.
func performMouseAction(
	point image.Point,
	button action.MouseButton,
	phase action.MousePhase,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	hold, err := holdModifiers(modifiers)
	if err != nil {
		return err
	}
	defer hold.release()

	if restoreCursor {
		// A failed read just means the cursor is not restored: the action the
		// caller asked for still has to happen.
		if origin, cursorErr := cursorPosition(); cursorErr == nil {
			defer func() { _ = setCursorPosition(origin) }()
		}
	}

	if err := moveCursorTo(point); err != nil {
		return err
	}

	switch phase {
	case action.PhaseClick:
		return clickButton(button)
	case action.PhaseDown:
		return sendButtonPhase(button, false)
	case action.PhaseUp:
		return sendButtonPhase(button, true)
	case action.PhaseToggle:
		if buttonDown(button) {
			return sendButtonPhase(button, true)
		}

		return sendButtonPhase(button, false)
	default:
		return errs.Newf(errs.CodeInvalidInput, "unsupported mouse phase: %s", phase)
	}
}

// Scroll scrolls at the cursor by a pixel delta, presenting modifiers as held
// for the duration.
//
// The deltas follow the library's shared convention: positive deltaY scrolls
// up and positive deltaX scrolls left. 30 pixels is one wheel notch, so a
// caller that thinks in line counts should pass multiples of 30. Windows
// carries no modifier field on a wheel event, so the modifiers are made real
// by pressing and releasing them around the injection; that also means a
// modifier the user is physically holding and did not ask for is suppressed
// for the duration, which is what keeps a plain scroll from arriving as a
// zoom. Horizontal scrolling is injected with MOUSEEVENTF_HWHEEL.
func (c *Client) Scroll(deltaX, deltaY int, modifiers action.Modifiers) error {
	return scrollWheel(deltaX, deltaY, modifiers)
}

// MoveMouse moves the cursor to point with one absolute SendInput move.
//
// bypassSmooth has no meaning on Windows: the platform has no animated cursor
// to bypass, and this backend never animates a move, so the cursor is always
// where the caller asked once the call returns.
func (c *Client) MoveMouse(p image.Point, bypassSmooth bool) {
	_ = moveCursorTo(p)
}

// CursorPosition returns the cursor position in physical screen pixels.
func (c *Client) CursorPosition() image.Point {
	position, err := cursorPosition()
	if err != nil {
		return image.Point{}
	}

	return position
}

// CheckPermissions reports true: Windows UI Automation needs no accessibility
// permission, and this backend never needs screen-recording consent either.
func (c *Client) CheckPermissions() bool {
	return true
}

// SetClickableRoles replaces the client's role filter with native UIA
// control-type names.
//
// An empty list restores the default list rather than disabling filtering.
// The change is atomic with respect to a running query, which sees either the
// old set or the new one.
func (c *Client) SetClickableRoles(roles []string) {
	c.rolesMu.Lock()
	defer c.rolesMu.Unlock()

	c.roles = append(c.roles[:0:0], roles...)
}

// ClickableRoles returns the client's role filter, in the native UIA names it
// was set with, or the default list when none was set.
//
// The result is a copy, so a caller cannot change the filter by mutating it.
func (c *Client) ClickableRoles() []string {
	c.rolesMu.RLock()
	configured := append([]string(nil), c.roles...)
	c.rolesMu.RUnlock()

	if len(configured) > 0 {
		return configured
	}

	return append([]string(nil), defaultClickableRoles...)
}

// IsMissionControlActive reports false: Windows has no Mission Control, so it
// is never active.
func (c *Client) IsMissionControlActive() bool {
	return false
}

// SupportsSupplementaryElements reports false.
//
// The supplementary surfaces the contract names (Dock, menu bar, Notification
// Center, Stage Manager, Picture-in-Picture) are macOS shapes. Windows has an
// equivalent for some of them, but not through this backend's contract, and
// claiming support would make a caller scan surfaces that return nothing.
func (c *Client) SupportsSupplementaryElements() bool {
	return false
}

// Close releases the client and makes later queries fail loudly.
//
// Queries create and release their own UIA objects, so there is no COM state
// held between calls to release here; what Close does is stop the client from
// being used, which is what a caller that unloads the library needs. It is
// idempotent.
func (c *Client) Close() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	c.closed = true

	return nil
}

// resolvedRoles returns the role names a query filters by: the explicitly
// requested ones first, then the client's own.
func (c *Client) resolvedRoles(requested []string) []string {
	if len(requested) > 0 {
		return requested
	}

	return c.ClickableRoles()
}

// newNode turns an extracted control into a node with a fresh ID.
func (c *Client) newNode(extracted control) *Node {
	return &Node{
		id:          "uia_" + strconv.FormatUint(c.seq.Add(1), 10),
		bounds:      extracted.bounds,
		role:        extracted.role,
		title:       extracted.title,
		description: extracted.description,
		value:       extracted.value,
		identifier:  extracted.identifier,
		focused:     extracted.focused,
		clickable:   extracted.clickable,
	}
}

// newWindowElement builds a Window from a live window handle.
func newWindowElement(hwnd uintptr) (*Window, error) {
	pid, err := windowProcessID(hwnd)
	if err != nil {
		return nil, err
	}

	return &Window{
		hwnd:  hwnd,
		pid:   pid,
		exe:   processExeName(pid),
		title: windowTitle(hwnd),
	}, nil
}

// rootWindowHandles returns the window handles a query starts from.
func rootWindowHandles(root ax.Element) ([]uintptr, error) {
	switch typed := root.(type) {
	case nil:
		return nil, errs.New(errs.CodeInvalidInput, "root element is nil")
	case *Window:
		if typed == nil || typed.hwnd == 0 {
			return nil, errs.New(errs.CodeInvalidInput, "window element has no window handle")
		}

		return []uintptr{typed.hwnd}, nil
	case *App:
		if typed == nil || typed.pid == 0 {
			return nil, errs.New(errs.CodeInvalidInput, "application element has no process id")
		}

		return processWindows(typed.pid), nil
	default:
		return nil, errs.Newf(
			errs.CodeInvalidInput,
			"unsupported root element type %T, expected *windows.Window or *windows.App",
			root,
		)
	}
}

// normalizeMaxDepth resolves the depth a query walks: the call's own bound
// first, then the client's, then the library default.
func normalizeMaxDepth(requested, configured int) int {
	if requested > 0 {
		return requested
	}

	if configured > 0 {
		return configured
	}

	return DefaultMaxDepth
}

// normalizeMaxNodes applies DefaultMaxNodes to an unbounded request.
func normalizeMaxNodes(maxNodes int) int {
	if maxNodes <= 0 {
		return DefaultMaxNodes
	}

	return maxNodes
}
