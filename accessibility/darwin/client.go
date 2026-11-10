//go:build darwin

// Package darwin implements the accessibility contract on macOS without CGo.
//
// The macOS accessibility API is plain C: the AXUIElement functions live in
// ApplicationServices, the values they return are CoreFoundation objects, and
// synthetic input is a CGEvent posted to the window server. This package binds
// those symbols at run time with purego and never links Objective-C, so a
// program can import it and still cross-compile a pure-Go binary. The one
// Objective-C surface it touches is NSRunningApplication, used only to map a
// bundle identifier to a process id; the rest is C.
//
// The package is macOS only. It walks a window or application tree, the menu
// bar, and the supplementary surfaces macOS draws in their own processes (the
// Dock, Notification Center, Stage Manager, Picture-in-Picture, the
// screen-capture UI and Mission Control), and it posts cursor, button and
// scroll input while keeping the Accessibility Zoom viewport in step.
package darwin

import (
	"context"
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Library defaults. A depth of fifty is deeper than any real interface; the
// node cap bounds a pathological web tree that would otherwise answer with
// tens of thousands of elements.
const (
	DefaultMaxDepth = 50
	DefaultMaxNodes = 4000
)

// Options configures a Client.
type Options struct {
	// ClickableRoles are native AX role names ("AXButton", "AXLink"). Empty
	// means element.DefaultClickableNativeRoles(element.VocabularyAX).
	ClickableRoles []string
	// MaxDepth bounds the walk below the root. Zero or negative means
	// DefaultMaxDepth.
	MaxDepth int
	// MaxNodes bounds how many elements one walk visits. Zero or negative
	// means DefaultMaxNodes.
	MaxNodes int
	// SkipHitTest disables the per-element visibility hit test. The test asks
	// the window server what is actually drawn at the element's center, which
	// is what prevents hints on covered controls; it costs one accessibility
	// round trip per candidate. Disable it only when latency matters more than
	// precision.
	SkipHitTest bool
	// BundleType forces the browser engine the walk treats the application as
	// ("electron", "chromium", "firefox", "webkit"). Empty means autodetect
	// from the root application's bundle identifier, which is what a caller
	// normally wants; the override exists for a browser whose bundle cannot be
	// identified at run time.
	BundleType string
	// SmoothSteps is how many intermediate move events a cursor move posts.
	// Zero (the default) moves instantly, which is the historical behavior.
	SmoothSteps int
	// SmoothDelay is the pause between those events. Zero or negative means
	// DefaultSmoothDelay when SmoothSteps is positive, and has no effect
	// otherwise.
	SmoothDelay time.Duration
}

// DefaultSmoothDelay is the pause a smooth cursor move leaves between step
// events when Options.SmoothDelay is not set. Each posted event costs a window
// server round trip, so the pause only has to keep the steps distinguishable
// without stretching the whole move.
const DefaultSmoothDelay = 4 * time.Millisecond

// Client implements ax.Client on macOS.
type Client struct {
	f *frameworks

	rolesMu sync.RWMutex
	roles   []string
	roleSet map[string]struct{}

	maxDepth    int
	maxNodes    int
	skipHitTest bool
	bundleType  string
	smoothSteps int
	smoothDelay time.Duration

	systemMu   sync.Mutex
	systemWide uintptr

	ids atomic.Uint64

	heldMu sync.Mutex
	held   map[action.MouseButton]bool

	// motionMu serializes whole cursor moves, so two concurrent smooth moves
	// cannot interleave their steps and fight over the cursor.
	motionMu sync.Mutex
	// postMu makes "pan the zoom viewport, then post the event" indivisible.
	// Cursor operations arrive on unrelated goroutines, and two interleaved ones
	// could otherwise pan for one target and post the other, leaving the cursor
	// at a correct coordinate outside the magnified region.
	postMu sync.Mutex

	clickMu        sync.Mutex
	lastClickAt    time.Time
	lastClickPoint image.Point
	clickCount     int

	closeOnce sync.Once
}

// New bounds the platform frameworks and returns a client. The first call
// performs the symbol binding; later calls reuse it.
func New(opts Options) (*Client, error) {
	// An override naming an engine this backend does not know would silently
	// turn every filter off, which is worse than refusing it.
	switch opts.BundleType {
	case "", BundleTypeElectron, BundleTypeChromium, BundleTypeFirefox, BundleTypeWebKit:
	default:
		return nil, errs.Newf(errs.CodeInvalidInput, "unknown bundle type %q", opts.BundleType)
	}

	api, err := loadFrameworks()
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "macOS accessibility is unavailable")
	}

	client := &Client{
		f:           api,
		maxDepth:    opts.MaxDepth,
		maxNodes:    opts.MaxNodes,
		skipHitTest: opts.SkipHitTest,
		bundleType:  opts.BundleType,
		smoothSteps: opts.SmoothSteps,
		smoothDelay: opts.SmoothDelay,
		held:        make(map[action.MouseButton]bool, 3),
	}

	if client.maxDepth <= 0 {
		client.maxDepth = DefaultMaxDepth
	}

	if client.maxNodes <= 0 {
		client.maxNodes = DefaultMaxNodes
	}

	if client.smoothSteps > 0 && client.smoothDelay <= 0 {
		client.smoothDelay = DefaultSmoothDelay
	}

	roles := opts.ClickableRoles
	if len(roles) == 0 {
		roles = element.DefaultClickableNativeRoles(element.VocabularyAX)
	}

	client.SetClickableRoles(roles)

	return client, nil
}

// FrontmostWindow returns the focused window of the focused application.
func (c *Client) FrontmostWindow(ctx context.Context) (ax.Window, error) {
	app, err := c.FocusedApplication(ctx)
	if err != nil {
		return nil, err
	}

	defer app.Release()

	window, err := c.windowAttribute(extractElement(app), "AXFocusedWindow")
	if err != nil {
		return nil, err
	}

	return window, nil
}

// AllWindows returns every window the focused application reports, in the
// order the application lists them.
func (c *Client) AllWindows(ctx context.Context) ([]ax.Window, error) {
	app, err := c.FocusedApplication(ctx)
	if err != nil {
		return nil, err
	}

	defer app.Release()

	return c.windowsOf(extractElement(app))
}

// FrontmostAndPopoverWindows returns the focused window first, then every other
// window of the focused application. AppKit reports popovers and sheets as
// windows of their application, so they are included; there is no separate
// popover list to ask for.
func (c *Client) FrontmostAndPopoverWindows(ctx context.Context) ([]ax.Window, error) {
	windows, err := c.AllWindows(ctx)
	if err != nil {
		return nil, err
	}

	front, frontErr := c.FrontmostWindow(ctx)
	if frontErr != nil || front == nil {
		return windows, nil
	}

	ordered := make([]ax.Window, 0, len(windows)+1)
	ordered = append(ordered, front)

	for _, window := range windows {
		if sameElement(window, front) {
			window.Release()

			continue
		}

		ordered = append(ordered, window)
	}

	return ordered, nil
}

// FocusedApplication returns the application that owns keyboard focus. AX's
// focused-application attribute is the authoritative answer; when it is
// momentarily unavailable, such as while a menu is tracking, the window
// server's frontmost application is used instead.
func (c *Client) FocusedApplication(context.Context) (ax.App, error) {
	system := c.systemWideElement()
	if system == 0 {
		return nil, errs.New(errs.CodeAccessibilityFailed, "the system-wide accessibility element is unavailable")
	}

	value, status := c.f.copyAttribute(system, "AXFocusedApplication")
	if status == kAXErrorSuccess && value != 0 {
		var pid int32

		c.f.ax.getPid(value, &pid)

		return &appElement{elementRef: elementRef{ref: value}, client: c, pid: pid}, nil
	}

	pid, ok := frontmostApplicationPID()
	if !ok {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"no focused application (AXError %d)",
			status,
		)
	}

	ref := c.f.ax.createApplication(pid)
	if ref == 0 {
		return nil, errs.New(errs.CodeAccessibilityFailed, "could not create the application element")
	}

	return &appElement{elementRef: elementRef{ref: ref}, client: c, pid: pid}, nil
}

// ApplicationByBundleID returns a running application by bundle identifier.
func (c *Client) ApplicationByBundleID(_ context.Context, bundleID string) (ax.App, error) {
	if bundleID == "" {
		return nil, errs.New(errs.CodeInvalidInput, "bundle identifier is empty")
	}

	pid, ok := runningApplicationPID(bundleID)
	if !ok {
		return nil, errs.Newf(errs.CodeElementNotFound, "no running application has bundle id %q", bundleID)
	}

	ref := c.f.ax.createApplication(pid)
	if ref == 0 {
		return nil, errs.Newf(errs.CodeAccessibilityFailed, "could not create the element for %q", bundleID)
	}

	return &appElement{elementRef: elementRef{ref: ref}, client: c, pid: pid}, nil
}

// ClickableNodes walks the tree under root and returns the elements that match
// roles and pass the visibility checks. A role list of nil uses the client's
// configured roles. A positive maxDepth overrides the client's depth for this
// call. readState fills in each node's identifier and focus state; with it
// false those fields stay empty, which is how a latency-sensitive caller opts
// out of reading state it does not use.
//
// The application's browser engine is detected from the root's bundle
// identifier unless Options.BundleType names one.
func (c *Client) ClickableNodes(
	ctx context.Context,
	root ax.Element,
	roles []string,
	maxDepth int,
	readState bool,
) ([]ax.Node, error) {
	return c.clickableNodes(ctx, root, roles, maxDepth, readState, "")
}

// clickableNodes is ClickableNodes with the bundle identifier of the
// application being walked, when the caller already knows it.
// ClickableElementsFromBundleID uses it so the engine detection that drives the
// browser walk filters still runs, even though every root it walks is a window
// rather than the application element.
func (c *Client) clickableNodes(
	ctx context.Context,
	root ax.Element,
	roles []string,
	maxDepth int,
	readState bool,
	bundleID string,
) ([]ax.Node, error) {
	if root == nil {
		return nil, errs.New(errs.CodeInvalidInput, "root element is nil")
	}

	rootRef := extractElement(root)
	if rootRef == 0 {
		return nil, errs.New(errs.CodeInvalidInput, "root element is not a macOS accessibility element")
	}

	rootInfo, err := c.readInfo(rootRef)
	if err != nil && rootInfo.role == "" {
		return nil, err
	}

	if bundleID == "" {
		bundleID = bundleIDOfRoot(root, rootInfo)
	}

	return c.walkNodes(ctx, walkRequest{
		root:       rootRef,
		rootInfo:   rootInfo,
		roles:      c.roleSetFor(roles),
		depth:      maxDepth,
		readState:  readState,
		bundleType: c.bundleTypeFor(bundleID),
	})
}

// walkRequest is one walk of an accessibility tree. The hooks are what Mission
// Control adds on top of a plain walk: a visit that rewrites each element's
// info before anything reads it, and a veto on a candidate after the walk.
type walkRequest struct {
	root       uintptr
	rootInfo   *elementInfo
	roles      map[string]struct{}
	depth      int
	readState  bool
	bundleType string

	visit         func(node *treeNode, info *elementInfo) bool
	dropCandidate func(node *treeNode) bool
}

// walkNodes builds the tree below a root the caller owns, collects the
// clickable elements and returns them as nodes. Every public walk goes through
// it, so the reference accounting and the node shape cannot drift between
// windows, applications and supplementary surfaces.
func (c *Client) walkNodes(ctx context.Context, request walkRequest) ([]ax.Node, error) {
	depth := request.depth
	if depth <= 0 {
		depth = c.maxDepth
	}

	rootInfo := request.rootInfo
	if rootInfo == nil {
		rootInfo, _ = c.readInfo(request.root)
	}

	// The walk keeps elements on every display, not just the active one: the
	// window may be on another monitor even though a hint overlay would only
	// be drawn on the cursor's display.
	screen := allScreenBounds()
	if screen.Empty() {
		screen = c.ActiveScreenBounds()
	}

	// A menu bar's dropdowns render below the bar's own rectangle, so the
	// screen is the clip region for a menu-bar walk.
	clip := rootInfo.bounds()
	if clip.Empty() || rootInfo.role == "AXMenuBar" {
		clip = screen
	}

	opts := &walkOptions{
		roles:             request.roles,
		maxDepth:          depth,
		maxNodes:          c.maxNodes,
		clip:              clip,
		screen:            screen,
		hitTestVisibility: !c.skipHitTest,
		bundleType:        request.bundleType,
		visit:             request.visit,
	}

	tree, stats := c.buildTree(ctx, request.root, rootInfo, opts)

	var clickable []*treeNode

	c.collectClickable(ctx, tree, opts, &clickable, &stats)

	if request.dropCandidate != nil {
		kept := clickable[:0]

		for _, candidate := range clickable {
			if !request.dropCandidate(candidate) {
				kept = append(kept, candidate)

				continue
			}

			// Left unkept, so releaseTree drops the reference with the rest of
			// the tree.
			candidate.kept = false
		}

		clickable = kept
	}

	nodes := make([]ax.Node, 0, len(clickable))

	for _, candidate := range clickable {
		candidate.kept = true

		info := candidate.info

		identifier := info.identifier
		focused := info.focused

		if !request.readState {
			identifier = ""
			focused = false
		}

		nodes = append(nodes, &node{
			elementRef:  elementRef{ref: candidate.ref},
			id:          fmt.Sprintf("ax-%d", c.ids.Add(1)),
			bounds:      info.bounds(),
			role:        info.role,
			subrole:     info.subrole,
			title:       info.title,
			description: info.description,
			value:       info.value,
			identifier:  identifier,
			searchText:  candidate.search,
			focused:     focused,
			clickable:   true,
		})
	}

	releaseTree(tree)

	return nodes, nil
}

// bundleTypeFor answers the browser engine a walk of this application applies.
// An explicit Options.BundleType wins; otherwise the running application's
// bundle is inspected. A detection failure is not an error: it means the walk
// filters cannot help, not that the walk cannot run.
func (c *Client) bundleTypeFor(bundleID string) string {
	if c.bundleType != "" {
		return c.bundleType
	}

	if bundleID == "" {
		return ""
	}

	bundleType, err := DetectBundleType(bundleID)
	if err != nil {
		return ""
	}

	return bundleType
}

// bundleIDOfRoot returns the bundle identifier of the application a root
// element belongs to. The pid comes from the attribute batch the caller read
// anyway, so this costs one Objective-C lookup rather than one accessibility
// round trip.
func bundleIDOfRoot(root ax.Element, info *elementInfo) string {
	if app, ok := root.(*appElement); ok {
		return app.BundleIdentifier()
	}

	if info == nil || info.pid <= 0 {
		return ""
	}

	return runningApplicationBundleID(info.pid)
}

// MenuBarClickableElements walks the frontmost application's menu bar. Menu
// bar items and the menu items they open are the clickable surfaces there.
func (c *Client) MenuBarClickableElements(ctx context.Context, maxDepth int) ([]ax.Node, error) {
	app, err := c.FocusedApplication(ctx)
	if err != nil {
		return nil, err
	}

	defer app.Release()

	menuBar, status := c.f.copyAttribute(extractElement(app), "AXMenuBar")
	if status != kAXErrorSuccess || menuBar == 0 {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the application has no menu bar (AXError %d)",
			status,
		)
	}

	window := &windowElement{elementRef: elementRef{ref: menuBar}}

	info, _ := c.readInfo(menuBar)
	window.role = info.role

	defer window.Release()

	roles := append([]string{"AXMenuBarItem", "AXMenuItem", "AXMenuButton"}, c.ClickableRoles()...)

	return c.ClickableNodes(ctx, window, roles, maxDepth, false)
}

// ClickableElementsFromBundleID returns the clickable nodes of every window of
// the named application. It reports CodeElementNotFound when the application
// is not running.
func (c *Client) ClickableElementsFromBundleID(
	ctx context.Context,
	bundleID string,
	roles []string,
	maxDepth int,
) ([]ax.Node, error) {
	app, err := c.ApplicationByBundleID(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	defer app.Release()

	windows, err := c.windowsOf(extractElement(app))
	if err != nil {
		return nil, err
	}

	var nodes []ax.Node

	for _, window := range windows {
		if ctx.Err() != nil {
			window.Release()

			break
		}

		found, nodeErr := c.clickableNodes(ctx, window, roles, maxDepth, false, bundleID)
		window.Release()

		if nodeErr != nil {
			continue
		}

		nodes = append(nodes, found...)
	}

	return nodes, nil
}

// ActiveScreenBounds returns the bounds of the display that contains the
// cursor, in global top-left-origin points.
func (c *Client) ActiveScreenBounds() image.Rectangle {
	return activeScreenBounds()
}

// CheckPermissions reports whether the process may use the accessibility API.
// macOS grants it per application in System Settings; without it every tree
// read fails.
func (c *Client) CheckPermissions() bool {
	return c.f.ax.isProcessTrusted() != 0
}

// SetClickableRoles replaces the native roles a walk looks for.
func (c *Client) SetClickableRoles(roles []string) {
	set := make(map[string]struct{}, len(roles))

	for _, role := range roles {
		if role == "" {
			continue
		}

		set[role] = struct{}{}
	}

	c.rolesMu.Lock()
	defer c.rolesMu.Unlock()

	c.roles = append([]string(nil), roles...)
	c.roleSet = set
}

// ClickableRoles returns the current native role list.
func (c *Client) ClickableRoles() []string {
	c.rolesMu.RLock()
	defer c.rolesMu.RUnlock()

	return append([]string(nil), c.roles...)
}

// roleSetFor returns the set for an explicit override, or the client's own set
// when the caller passed none.
func (c *Client) roleSetFor(roles []string) map[string]struct{} {
	if len(roles) > 0 {
		set := make(map[string]struct{}, len(roles))

		for _, role := range roles {
			if role != "" {
				set[role] = struct{}{}
			}
		}

		if len(set) > 0 {
			return set
		}
	}

	c.rolesMu.RLock()
	defer c.rolesMu.RUnlock()

	return c.roleSet
}

// IsMissionControlActive reports whether Mission Control is up. While it is,
// the Dock's accessibility tree carries a child whose identifier is "mc", and
// at no other time. The Dock is queried with a short messaging timeout so an
// unresponsive Dock cannot stall a hint session.
func (c *Client) IsMissionControlActive() bool {
	pid, ok := runningApplicationPID("com.apple.dock")
	if !ok {
		return false
	}

	dock := c.f.ax.createApplication(pid)
	if dock == 0 {
		return false
	}

	defer cfRelease(dock)

	c.f.ax.setMessagingTimeout(dock, 0.25)

	value, status := c.f.copyAttribute(dock, "AXChildren")
	if status != kAXErrorSuccess || value == 0 {
		return false
	}

	defer c.f.cf.release(value)

	if c.f.cf.getTypeID(value) != c.f.cfArrayGetTypeID() {
		return false
	}

	for _, child := range c.f.arrayValues(value) {
		c.f.ax.setMessagingTimeout(child, 0.25)

		identifier, identifierStatus := c.f.copyAttribute(child, "AXIdentifier")
		if identifierStatus == kAXErrorCannotComplete {
			return false
		}

		if identifierStatus != kAXErrorSuccess || identifier == 0 {
			continue
		}

		active := c.f.isCFString(identifier) && c.f.goString(identifier) == "mc"

		cfRelease(identifier)

		if active {
			return true
		}
	}

	return false
}

// SupportsSupplementaryElements is true: besides a window's tree and the menu
// bar, this backend walks the Dock, Notification Center, Stage Manager, the
// Picture-in-Picture agent and the screen-capture UI through SurfaceNodes, and
// Mission Control through MissionControlClickableNodes.
func (c *Client) SupportsSupplementaryElements() bool {
	return true
}

// Close drops the cached system-wide element. It is safe to call more than
// once, and a later call that needs the element recreates it. Close must not
// run concurrently with another call on the same client: the walk may be using
// the element it releases.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.systemMu.Lock()
		defer c.systemMu.Unlock()

		if c.systemWide != 0 {
			cfRelease(c.systemWide)
			c.systemWide = 0
		}
	})

	return nil
}

// systemWideElement returns the cached system-wide element, creating it on
// first use. The accessibility server keeps it alive across calls, so it is
// created once per client rather than per walk.
func (c *Client) systemWideElement() uintptr {
	c.systemMu.Lock()
	defer c.systemMu.Unlock()

	if c.systemWide == 0 {
		c.systemWide = c.f.ax.createSystemWide()
	}

	return c.systemWide
}

// windowsOf returns every window of an application element.
func (c *Client) windowsOf(appRef uintptr) ([]ax.Window, error) {
	value, status := c.f.copyAttribute(appRef, "AXWindows")
	if status != kAXErrorSuccess || value == 0 {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the application reports no windows (AXError %d)",
			status,
		)
	}

	defer c.f.cf.release(value)

	if c.f.cf.getTypeID(value) != c.f.cfArrayGetTypeID() {
		return nil, errs.New(errs.CodeAccessibilityFailed, "the window list was not an array")
	}

	members := c.f.arrayValues(value)
	windows := make([]ax.Window, 0, len(members))

	for _, member := range members {
		if member == 0 {
			continue
		}

		ref := c.f.cf.retain(member)
		info, err := c.readInfo(ref)
		if err != nil && info.role == "" {
			cfRelease(ref)

			continue
		}

		windows = append(windows, &windowElement{
			elementRef: elementRef{ref: ref},
			role:       info.role,
		})
	}

	return windows, nil
}

// windowAttribute reads one attribute that holds a window element.
func (c *Client) windowAttribute(owner uintptr, attribute string) (ax.Window, error) {
	value, status := c.f.copyAttribute(owner, attribute)
	if status != kAXErrorSuccess || value == 0 {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the application has no %s (AXError %d)",
			attribute,
			status,
		)
	}

	info, err := c.readInfo(value)
	if err != nil && info.role == "" {
		cfRelease(value)

		return nil, err
	}

	return &windowElement{elementRef: elementRef{ref: value}, role: info.role}, nil
}

// sameElement reports whether two returned windows are the same AX element.
func sameElement(left, right ax.Window) bool {
	api := frameworksAPI
	if api == nil {
		return false
	}

	return api.cf.equal(extractElement(left), extractElement(right)) != 0
}
