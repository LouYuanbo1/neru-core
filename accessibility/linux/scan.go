//go:build linux

package linux

import (
	"context"
	"image"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/linux/compositor"
	"github.com/LouYuanbo1/neru-core/errs"
)

// This file turns the primitive AT-SPI reads into the two things the contract
// asks of a Linux tree: which application and window are focused, and which
// elements inside a window a hint can land on.

// scannedFrame is one top-level window of an application, with the state bits
// focus selection needs.
type scannedFrame struct {
	ref     accessible
	role    string
	active  bool
	showing bool
}

// scannedApplication is one application on the accessibility bus, with the
// identity that names it and the windows it exposes.
type scannedApplication struct {
	ref    accessible
	name   string
	pid    int32
	frames []scannedFrame
}

// focusIdentity is the compositor's snapshot of the focused window: the
// application identity and title it reported, as one read, so a frame selected
// under it cannot be paired with a newer window's identity.
type focusIdentity struct {
	appID string
	title string
}

// frameTarget is one top-level window to walk, with the compositor identity it
// was selected under. The identity is empty when the walk started from an
// application rather than from focus.
type frameTarget struct {
	ref   accessible
	role  string
	appID string
	title string
}

// scanOptions is one tree walk's fixed decisions, resolved once so no level of
// the recursion can disagree with another.
type scanOptions struct {
	roles     map[string]struct{}
	limit     int
	maxNodes  int
	readState bool
	screen    image.Rectangle
}

// scanApplications enumerates every application on the accessibility bus and its
// windows.
//
// It is the expensive call in this backend: one round trip per application for
// its name, one for its process id, then one per child to find the windows and
// two more per window for its state. The per-application work is therefore run
// concurrently, which is safe because godbus multiplexes overlapping calls over
// the one connection.
func (c *Client) scanApplications(
	ctx context.Context,
	conn *dbus.Conn,
) ([]scannedApplication, error) {
	apps, err := c.applicationIdentities(ctx, conn)
	if err != nil {
		return nil, err
	}

	forEachConcurrently(len(apps), walkWorkers, func(index int) {
		apps[index].frames = c.framesOf(ctx, conn, apps[index].ref)
	})

	return apps, nil
}

// applicationIdentities enumerates the applications on the accessibility bus
// with their names and process ids, without reading their windows. Bundle-id
// lookup needs only this, and skipping the windows halves the round trips.
func (c *Client) applicationIdentities(
	ctx context.Context,
	conn *dbus.Conn,
) ([]scannedApplication, error) {
	refs, err := children(ctx, conn, registryRoot())
	if err != nil {
		return nil, err
	}

	apps := make([]scannedApplication, len(refs))
	for index := range refs {
		apps[index].ref = refs[index]
	}

	forEachConcurrently(len(apps), walkWorkers, func(index int) {
		apps[index].name = propertyString(
			ctx, conn, apps[index].ref, ifaceAccessible, propertyName,
		)
		apps[index].pid = applicationPID(ctx, conn, apps[index].ref)
	})

	return apps, nil
}

// framesOf returns the top-level windows an application exposes, in tree order.
//
// A window whose state cannot be read is still returned, with both state bits
// clear, so window enumeration reports it; it simply never wins focus selection
// on the strength of a state nobody could read.
func (c *Client) framesOf(
	ctx context.Context,
	conn *dbus.Conn,
	app accessible,
) []scannedFrame {
	kids, err := children(ctx, conn, app)
	if err != nil {
		return nil
	}

	frames := make([]scannedFrame, 0, len(kids))

	for _, kid := range kids {
		role := roleName(ctx, conn, kid)
		if !isWindowRole(role) {
			continue
		}

		frame := scannedFrame{ref: kid, role: role}

		if states, ok := statesRead(ctx, conn, kid); ok {
			frame.active = atspiStateBit(states, stateActive)
			frame.showing = atspiStateBit(states, stateShowing)
		}

		frames = append(frames, frame)
	}

	return frames
}

// focusedFrame returns the application and window that best identify the focused
// window. The ranking itself is selectFocusedFrame in policy.go; this only wires
// the scan's shape onto it.
func focusedFrame(apps []scannedApplication) (int, int, bool) {
	candidates := make([]frameCandidate, 0, 8)
	positions := make([][2]int, 0, 8)

	for appIndex := range apps {
		for frameIndex, frame := range apps[appIndex].frames {
			candidates = append(candidates, frameCandidate{
				active:  frame.active,
				showing: frame.showing,
			})
			positions = append(positions, [2]int{appIndex, frameIndex})
		}
	}

	index, ok := selectFocusedFrame(candidates)
	if !ok {
		return 0, 0, false
	}

	position := positions[index]

	return position[0], position[1], true
}

// focusedWindow scans the bus and reports the application, the window and the
// compositor identity that best identify the focused window. The application is
// returned with all of its windows, so a caller that also wants them does not
// scan twice.
//
// The compositor is asked first, because AT-SPI's ACTIVE state is set by the
// toolkit rather than the compositor and a Chromium or Electron window can claim
// it from the background. When the compositor cannot answer (X11, or a Wayland
// session with no bridge) the AT-SPI ranking in selectFocusedFrame decides.
func (c *Client) focusedWindow(
	ctx context.Context,
	conn *dbus.Conn,
) (scannedApplication, scannedFrame, focusIdentity, bool, error) {
	apps, err := c.scanApplications(ctx, conn)
	if err != nil {
		return scannedApplication{}, scannedFrame{}, focusIdentity{}, false, err
	}

	identity, haveIdentity := c.compositorIdentity(ctx)
	if haveIdentity {
		appIndex, frameIndex, ok := c.selectCompositorFrame(ctx, conn, apps, identity)
		if !ok {
			// The compositor named a window AT-SPI cannot single out (a tree
			// that has not built yet, or two frames it cannot tell apart).
			// Guessing another window would put hints in the wrong one, so
			// nothing is reported as focused.
			return scannedApplication{}, scannedFrame{}, identity, false, nil
		}

		return apps[appIndex], apps[appIndex].frames[frameIndex], identity, true, nil
	}

	appIndex, frameIndex, ok := focusedFrame(apps)
	if !ok {
		return scannedApplication{}, scannedFrame{}, focusIdentity{}, false, nil
	}

	return apps[appIndex], apps[appIndex].frames[frameIndex], focusIdentity{}, true, nil
}

// compositorIdentity returns the compositor's focused-window identity, and false
// when there is no compositor, it cannot answer, or it named no window.
//
// A compositor that answers with no identity at all is treated as no answer:
// without one there is nothing to select a frame with, and falling back to
// AT-SPI is better than refusing every hint.
func (c *Client) compositorIdentity(ctx context.Context) (focusIdentity, bool) {
	if c.compositor == nil {
		return focusIdentity{}, false
	}

	window, found, err := c.compositor.FocusedWindow(ctx)
	if err != nil || !found || (window.AppID == "" && window.Title == "") {
		return focusIdentity{}, false
	}

	return focusIdentity{appID: window.AppID, title: window.Title}, true
}

// activeStateIdentifiesFocus reports whether AT-SPI's ACTIVE state reliably marks
// the compositor-focused window. KWin sets it on the window the compositor has
// focused; on wlroots it is set inconsistently across frames, so a unique ACTIVE
// sibling there may be a background window and selectAppFrame does not trust it.
func (c *Client) activeStateIdentifiesFocus() bool {
	return c.compositor != nil && c.compositor.Backend() == compositor.BackendKWin.String()
}

// selectCompositorFrame picks the AT-SPI frame the compositor's focused window
// names, and reports false when it cannot be singled out.
//
// The application identity is tried first, because it costs one name comparison
// per frame and usually resolves to one application. Only when that application
// has several frames are their titles read, and only when no application matches
// is every frame's title read: a compositor app_id and an AT-SPI application
// name can spell the same application differently ("org.gnome.TextEditor" and
// "gnome-text-editor"), and a unique title is the last identity signal left.
func (c *Client) selectCompositorFrame(
	ctx context.Context,
	conn *dbus.Conn,
	apps []scannedApplication,
	identity focusIdentity,
) (int, int, bool) {
	type position struct{ app, frame int }

	matches := make([]position, 0, 8)

	if identity.appID != "" {
		for appIndex := range apps {
			if !identifierMatches(apps[appIndex].name, identity.appID) {
				continue
			}

			for frameIndex := range apps[appIndex].frames {
				matches = append(matches, position{app: appIndex, frame: frameIndex})
			}
		}
	}

	switch {
	case len(matches) == 1:
		// One window of the focused application: no title read is needed.
		return matches[0].app, matches[0].frame, true
	case len(matches) > 1:
		candidates := make([]frameCandidate, len(matches))

		for index, match := range matches {
			frame := apps[match.app].frames[match.frame]
			candidates[index] = frameCandidate{
				active:     frame.active,
				showing:    frame.showing,
				appMatch:   true,
				titleMatch: c.frameTitleMatches(ctx, conn, frame, identity.title),
			}
		}

		frameIndex, ok := selectAppFrame(candidates, c.activeStateIdentifiesFocus())
		if !ok {
			return 0, 0, false
		}

		return matches[frameIndex].app, matches[frameIndex].frame, true
	}

	// No application matched. A title that names exactly one frame is the only
	// remaining identity signal, and only a compositor that reported a title can
	// offer it.
	if identity.title == "" {
		return 0, 0, false
	}

	candidates := make([]frameCandidate, 0, 8)
	positions := make([]position, 0, 8)

	for appIndex := range apps {
		for frameIndex, frame := range apps[appIndex].frames {
			candidates = append(candidates, frameCandidate{
				active:     frame.active,
				showing:    frame.showing,
				titleMatch: c.frameTitleMatches(ctx, conn, frame, identity.title),
			})
			positions = append(positions, position{app: appIndex, frame: frameIndex})
		}
	}

	frameIndex, ok := selectTitleFrame(candidates)
	if !ok {
		return 0, 0, false
	}

	return positions[frameIndex].app, positions[frameIndex].frame, true
}

// frameTitleMatches reads a window's AT-SPI title and reports whether the
// compositor's title describes it. An absent title on either side is not a
// mismatch, so a toolkit that exposes none does not lose its origin.
func (c *Client) frameTitleMatches(
	ctx context.Context,
	conn *dbus.Conn,
	frame scannedFrame,
	focusedTitle string,
) bool {
	if focusedTitle == "" {
		return false
	}

	title := propertyString(ctx, conn, frame.ref, ifaceAccessible, propertyName)

	return titleMatchesFocused(title, focusedTitle)
}

// titleMatchesFocused reports whether an AT-SPI frame title equals the
// compositor's focused title. Both derive from the window's title, so a
// whitespace-trimmed exact comparison separates two windows of one application
// showing different documents. Empty titles never match.
func titleMatchesFocused(frameTitle, focusedTitle string) bool {
	frameTitle = strings.TrimSpace(frameTitle)

	return frameTitle != "" && frameTitle == strings.TrimSpace(focusedTitle)
}

// placementFor derives, from the compositor when it can answer and from the
// window's own extents otherwise, how its elements' extents become screen pixels.
//
// The compositor's origin is the authoritative answer: it is what lets a
// WINDOW-relative extent be placed exactly, and what corrects a toolkit (GTK 4)
// that reports a zero screen origin for every element. See placementForOrigin for
// how the two coordinate spaces are used. The AT-SPI reconstruction below is the
// fallback for a session with no compositor, where it is exact for toolkits that
// report extents relative to the window and degrades to plain screen coordinates
// otherwise.
func (c *Client) placementFor(
	ctx context.Context,
	conn *dbus.Conn,
	target frameTarget,
) placement {
	frameScreen, ok := extents(ctx, conn, target.ref, coordScreen)
	if !ok {
		return screenPlacement()
	}

	if origin, known := c.windowOrigin(ctx, conn, target, frameScreen); known {
		return placementForOrigin(ctx, conn, target.ref, frameScreen, origin)
	}

	frameWindow, ok := extents(ctx, conn, target.ref, coordWindow)
	if !ok {
		return screenPlacement()
	}

	screen, _ := c.screenBounds()

	return placementFor(frameScreen, frameWindow, screen)
}

// windowOrigin asks the compositor where target's window is, and reports false
// when it cannot answer or its answer belongs to another window.
//
// Only a window that was selected as the focused one carries a compositor
// identity; a window walked from an application root carries none, and asking
// for its origin would risk applying the focused window's position to a sibling
// of the same size. Those walks keep the AT-SPI reconstruction.
//
// The frame's title is read from AT-SPI when the compositor identity did not
// carry one, so a window whose identity arrived without a title is still checked
// by it. A compositor failure is not fatal: the caller keeps the AT-SPI
// reconstruction, which is where hints landed before this bridge existed.
func (c *Client) windowOrigin(
	ctx context.Context,
	conn *dbus.Conn,
	target frameTarget,
	frameScreen image.Rectangle,
) (image.Point, bool) {
	if c.compositor == nil || (target.appID == "" && target.title == "") {
		return image.Point{}, false
	}

	title := target.title
	if title == "" {
		title = propertyString(ctx, conn, target.ref, ifaceAccessible, propertyName)
	}

	origin, known, err := c.compositor.WindowOrigin(ctx, compositor.Window{
		AppID: target.appID,
		Title: title,
		Rect:  frameScreen,
	})
	if err != nil {
		return image.Point{}, false
	}

	return origin, known
}

// placementForOrigin maps a frame's elements into screen pixels from the
// compositor's origin.
//
// Window-relative extents are preferred, because they stay correct when a
// toolkit's screen-coordinate answer is wrong (GTK 4 reports a zero origin for
// every element). A toolkit that cannot answer in window coordinates still gets
// its screen coordinates offset by the origin less the window's own screen
// origin, which is exactly zero on a toolkit that reports them right. The
// choice itself is compositor.PlacementFor, so it is testable without a desktop.
func placementForOrigin(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	frameScreen image.Rectangle,
	origin image.Point,
) placement {
	frameWindow, haveWindowExtents := extents(ctx, conn, ref, coordWindow)

	chosen := compositor.PlacementFor(origin, frameScreen, frameWindow, haveWindowExtents)

	coordType := coordScreen
	if chosen.WindowRelative {
		coordType = coordWindow
	}

	return placement{coordType: coordType, offset: chosen.Offset}
}

// scanFrames walks each target and collects the clickable nodes in it.
func (c *Client) scanFrames(
	ctx context.Context,
	conn *dbus.Conn,
	targets []frameTarget,
	roles []string,
	maxDepth int,
	readState bool,
) []ax.Node {
	screen, _ := c.screenBounds()

	opts := scanOptions{
		roles:     c.rolesFor(roles),
		limit:     depthLimit(maxDepth, c.depth),
		maxNodes:  nodeLimit(c.nodes),
		readState: readState,
		screen:    screen,
	}

	var (
		nodes   []ax.Node
		visited int
	)

	for _, target := range targets {
		if ctx.Err() != nil || visited >= opts.maxNodes {
			break
		}

		place := c.placementFor(ctx, conn, target)
		c.walk(ctx, conn, target.ref, 0, place, opts, &nodes, &visited)
	}

	return nodes
}

// walk collects the elements under ref that a hint can land on.
//
// The recursion is depth-first and sequential. A breadth-first or concurrent
// walk would be faster on a huge tree, but it would also interleave extents read
// at different moments, and hints placed from such a mix can disagree with what
// is on screen; one scan is also small enough (a few hundred round trips on a
// busy window) that the simpler order is worth keeping.
func (c *Client) walk(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	depth int,
	place placement,
	opts scanOptions,
	out *[]ax.Node,
	visited *int,
) {
	if ctx.Err() != nil || *visited >= opts.maxNodes || !depthAllowed(depth, opts.limit) {
		return
	}

	*visited = *visited + 1

	role := roleName(ctx, conn, ref)

	// The state bits are only read for a role that could be kept, since they cost
	// a round trip and most of a tree is containers.
	var states []uint32

	if wantsRole(role, opts.roles) {
		states, _ = statesRead(ctx, conn, ref)
		role = effectiveRole(role, states)
	}

	// A disabled element gets no hint: clicking it does nothing, and on macOS the
	// same rule applies. A hidden element is not on screen to click either.
	if wantsRole(role, opts.roles) && atspiStateBit(states, stateShowing) &&
		atspiEnabled(states) {
		if node := c.readNode(ctx, conn, ref, role, states, place, opts); node != nil {
			*out = append(*out, node)
		}
	}

	kids, err := children(ctx, conn, ref)
	if err != nil {
		// The subtree is unreadable, which is routine for an application that is
		// shutting down mid-scan. The rest of the walk is unaffected.
		return
	}

	for _, child := range kids {
		c.walk(ctx, conn, child, depth+1, place, opts, out, visited)
	}
}

// readNode captures one matched element, or returns nil when it has no
// on-screen area to point at.
func (c *Client) readNode(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	role string,
	states []uint32,
	place placement,
	opts scanOptions,
) ax.Node {
	rect, ok := extents(ctx, conn, ref, place.coordType)
	if !ok {
		return nil
	}

	bounds, visible := clipToScreen(place.place(rect), opts.screen)
	if !visible {
		return nil
	}

	node := &atspiNode{
		id:          ref.String(),
		role:        role,
		bounds:      bounds,
		title:       propertyString(ctx, conn, ref, ifaceAccessible, propertyName),
		description: propertyString(ctx, conn, ref, ifaceAccessible, propertyDescription),
	}

	// The value is read from the Text interface only for the roles that have one,
	// so a password field's contents are never fetched.
	if isValueRole(role) {
		node.value = textValue(ctx, conn, ref)
	}

	if opts.readState {
		node.identifier = propertyString(
			ctx, conn, ref, ifaceAccessible, propertyAccessibleID,
		)
		node.focused = atspiStateBit(states, stateFocused)
	}

	return node
}

// scanRoots resolves the element a ClickableNodes call was given into the
// windows to walk.
func (c *Client) scanRoots(
	ctx context.Context,
	conn *dbus.Conn,
	root ax.Element,
) ([]frameTarget, error) {
	switch typed := root.(type) {
	case *atspiWindow:
		if typed == nil {
			return nil, errs.New(errs.CodeInvalidInput, "ClickableNodes needs a window, not nil")
		}

		return []frameTarget{{
			ref:   typed.ref,
			role:  typed.role,
			appID: typed.appID,
			title: typed.title,
		}}, nil
	case *atspiApp:
		if typed == nil {
			return nil, errs.New(errs.CodeInvalidInput, "ClickableNodes needs an application, not nil")
		}

		frames := c.framesOf(ctx, conn, typed.ref)
		targets := make([]frameTarget, 0, len(frames))

		for _, frame := range frames {
			targets = append(targets, frameTarget{ref: frame.ref, role: frame.role})
		}

		if len(targets) == 0 {
			// An application that has registered but has not opened a window yet
			// still owns a tree; walking its root keeps that partial state visible
			// instead of reporting an empty success.
			targets = append(targets, frameTarget{ref: typed.ref})
		}

		return targets, nil
	default:
		return nil, errs.New(
			errs.CodeInvalidInput,
			"ClickableNodes needs a window or application element from the Linux backend",
		)
	}
}

// forEachConcurrently runs fn for every index in [0, count) with at most workers
// calls in flight, and returns once all of them have finished.
func forEachConcurrently(count, workers int, fn func(index int)) {
	if count <= 0 {
		return
	}

	sem := make(chan struct{}, max(workers, 1))

	var waitGroup sync.WaitGroup

	for index := range count {
		waitGroup.Add(1)

		sem <- struct{}{}

		go func(index int) {
			defer waitGroup.Done()
			defer func() { <-sem }()

			fn(index)
		}(index)
	}

	waitGroup.Wait()
}

// matchesApplication reports whether bundleID names this application.
//
// Linux has no bundle identifiers, so every identity a desktop actually exposes
// is compared: the AT-SPI display name, the process name, and the bus name.
func matchesApplication(bundleID string, app scannedApplication) bool {
	want := normalizeIdentifier(bundleID)
	if want == "" {
		return false
	}

	candidates := [3]string{app.name, processName(app.pid), app.ref.bus}

	for _, candidate := range candidates {
		if identifierMatches(candidate, want) {
			return true
		}
	}

	return false
}

// applicationBundleID returns the identity this backend reports for an
// application: its display name, else its process name, else its bus name.
func applicationBundleID(name string, pid int32, bus string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}

	if process := processName(pid); process != "" {
		return process
	}

	return bus
}

// findApplication returns the application whose identity bundleID names.
func (c *Client) findApplication(
	ctx context.Context,
	conn *dbus.Conn,
	bundleID string,
) (scannedApplication, bool, error) {
	apps, err := c.applicationIdentities(ctx, conn)
	if err != nil {
		return scannedApplication{}, false, err
	}

	for _, app := range apps {
		if matchesApplication(bundleID, app) {
			return app, true, nil
		}
	}

	return scannedApplication{}, false, nil
}

// applicationNotFound explains why a bundle-id lookup failed, in the vocabulary
// the contract asks for. The message is deliberately CodeNotSupported rather
// than "element not found": what is missing is the identifier scheme itself, so
// a caller on Linux has to know that the lookup was approximate.
func applicationNotFound(bundleID string) error {
	return errs.Newf(
		errs.CodeNotSupported,
		"no running AT-SPI application matches %q: Linux has no bundle identifiers, "+
			"so only an application's AT-SPI name, the last dot segment of a desktop id, "+
			"or its process name can identify it",
		bundleID,
	)
}
