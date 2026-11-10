package compositor

import (
	"image"
	"strings"
)

// This file holds the geometry and identity rules every compositor source
// shares, as pure functions. Keeping them free of D-Bus, os/exec and process
// state is what lets them be tested on a host with no desktop, and it keeps each
// compositor file to the part that is genuinely that compositor's.

// coordPairLen is the length of an [x,y] or [w,h] pair in a compositor's JSON.
const coordPairLen = 2

// originSizeTolerance is the largest per-dimension difference, in pixels,
// between the size a compositor reports for the focused window and the AT-SPI
// frame's extents before the rectangle is taken for another window. Focus can
// change between the AT-SPI read and the query, and a wrong origin would place
// every hint in the window at another window's screen position.
//
// The value is 32 because Qt reports the content size exactly while GTK and
// other client-side-decoration toolkits can differ by a title bar.
const originSizeTolerance = 32

// absInt returns the magnitude of an int, for the tolerance comparisons.
func absInt(value int) int {
	if value < 0 {
		return -value
	}

	return value
}

// sizeMatches reports whether a compositor rectangle describes a window of the
// AT-SPI frame's extents, within originSizeTolerance. An empty frame (no
// extents read) never matches, so a source cannot be talked into answering for a
// window nobody measured.
func sizeMatches(frame, window image.Rectangle) bool {
	if frame.Empty() || window.Empty() {
		return false
	}

	return absInt(frame.Dx()-window.Dx()) <= originSizeTolerance &&
		absInt(frame.Dy()-window.Dy()) <= originSizeTolerance
}

// Placement is how a window's AT-SPI extents map to screen pixels once the
// compositor's origin is known.
type Placement struct {
	// WindowRelative is true when element extents must be read relative to the
	// top-level window, which is exact; false means they are read relative to the
	// screen and only offset.
	WindowRelative bool

	// Offset is added to an extents origin to reach screen pixels.
	Offset image.Point
}

// PlacementFor decides how a frame's element extents map to screen pixels from
// the compositor's origin.
//
// Window-relative extents are preferred, because they stay correct when a
// toolkit's screen-coordinate answer is wrong: a GTK 4 client on Wayland reports
// a zero screen origin for every element. The offset is the origin less the
// frame's own window-relative position, so an element extent lands exactly where
// the compositor says the window is.
//
// A toolkit that cannot answer in window coordinates (haveWindowExtents false)
// keeps its screen extents and offsets them by the origin less the frame's own
// screen origin. That is exactly zero on a toolkit that reports screen
// coordinates correctly, and the whole window position on one that reports a
// zero origin for every element.
//
// frameWindow is ignored when haveWindowExtents is false. An empty frameScreen
// has no origin to offset and yields a zero placement, which is the caller's
// "leave the extents alone" answer.
func PlacementFor(
	origin image.Point,
	frameScreen, frameWindow image.Rectangle,
	haveWindowExtents bool,
) Placement {
	if frameScreen.Empty() {
		return Placement{}
	}

	if haveWindowExtents && !frameWindow.Empty() {
		return Placement{
			WindowRelative: true,
			Offset:         origin.Sub(frameWindow.Min),
		}
	}

	return Placement{Offset: origin.Sub(frameScreen.Min)}
}

// identifierMatches reports whether one spelling of an application identity
// names the same application as another.
//
// A compositor's app_id is often reverse-DNS ("org.mozilla.firefox") where
// AT-SPI and KWin's resourceClass report "firefox", so the last dot-separated
// segment of either side is compared too. Case is folded because a WM_CLASS is
// commonly capitalized while an app_id is not.
func identifierMatches(candidate, want string) bool {
	name := strings.ToLower(strings.TrimSpace(candidate))
	focusedID := strings.ToLower(strings.TrimSpace(want))

	if name == "" || focusedID == "" {
		return false
	}

	if name == focusedID {
		return true
	}

	if segment := lastDotSegment(focusedID); segment != "" && segment == name {
		return true
	}

	if segment := lastDotSegment(name); segment != "" && segment == focusedID {
		return true
	}

	return false
}

// lastDotSegment returns the substring after the final '.', or "" when there is
// no interior dot. A value with no segment to compare reports "", never an empty
// match: comparing the empty string would make every identity equal.
func lastDotSegment(value string) string {
	index := strings.LastIndexByte(value, '.')
	if index < 0 || index == len(value)-1 {
		return ""
	}

	return value[index+1:]
}

// titleMatches reports whether a compositor's caption and an AT-SPI window title
// describe one window.
//
// A prefix either way counts. KWin appends its own suffix to a caption (a
// window shortcut, a hostname, "<2>" for a shared title) and the
// foreign-toplevel title may predate it; exact matching would unoffset such a
// window for good. A trimmed exact match or prefix still separates two windows
// of one application showing different documents, which size cannot.
//
// An empty title on either side is not a mismatch: a compositor that exposes no
// title, or a frame whose title nobody could read, has not thereby reported a
// different window. Refusing there would unoffset every hint on a whole session
// to close a rare misplacement.
func titleMatches(caption, focused string) bool {
	caption = strings.TrimSpace(caption)
	focused = strings.TrimSpace(focused)

	if caption == "" || focused == "" {
		return true
	}

	return strings.HasPrefix(caption, focused) || strings.HasPrefix(focused, caption)
}

// appMatches reports whether a compositor-reported identity names the frame's
// application. Either side may be empty, which is not a mismatch.
func appMatches(windowAppID, frameAppID string) bool {
	if strings.TrimSpace(windowAppID) == "" || strings.TrimSpace(frameAppID) == "" {
		return true
	}

	return identifierMatches(windowAppID, frameAppID)
}

// kwinOrigin applies the KWin acceptance rules to a cached window: the window
// must be the frame's application (matching either of KWin's two identifiers),
// carry the same title, and have the frame's size.
func kwinOrigin(window kwinWindow, frame Window) (image.Point, bool) {
	if !kwinAppMatches(window, frame.AppID) {
		return image.Point{}, false
	}

	if !titleMatches(window.Title, frame.Title) {
		return image.Point{}, false
	}

	if !sizeMatches(frame.Rect, window.Rect) {
		return image.Point{}, false
	}

	return window.Rect.Min, true
}

// kwinAppMatches reports whether either of KWin's identifiers for a window names
// the frame's application. KWin's resourceClass and resourceName disagree for
// some windows (an XWayland Firefox is class "firefox", name "navigator"), and
// the foreign-toplevel app_id is built from whichever KWin chose, so matching
// either is what keeps that choice from being something this check guesses.
func kwinAppMatches(window kwinWindow, frameAppID string) bool {
	if strings.TrimSpace(frameAppID) == "" ||
		(strings.TrimSpace(window.Class) == "" && strings.TrimSpace(window.Name) == "") {
		return true
	}

	return identifierMatches(window.Class, frameAppID) ||
		identifierMatches(window.Name, frameAppID)
}

// gnomeOrigin applies the GNOME acceptance rules to the extension's report: the
// app id and title must name the frame's window, and the size must match. The
// app id is Mutter's WM_CLASS, the same vocabulary foreign-toplevel speaks.
func gnomeOrigin(window Window, frame Window) (image.Point, bool) {
	if !appMatches(window.AppID, frame.AppID) || !titleMatches(window.Title, frame.Title) {
		return image.Point{}, false
	}

	if !sizeMatches(frame.Rect, window.Rect) {
		return image.Point{}, false
	}

	return window.Rect.Min, true
}

// cosmicOrigin accepts a geometry the embedder supplied only when its size
// matches the frame, the same race guard niri and sway apply. COSMIC publishes
// no window identity over the protocol this package could read, so size is the
// only check there is.
func cosmicOrigin(bounds image.Rectangle, frame Window) (image.Point, bool) {
	if !sizeMatches(frame.Rect, bounds) {
		return image.Point{}, false
	}

	return bounds.Min, true
}

// niriTile returns the focused window's workspace-view position. niri sets
// tile_pos_in_workspace_view for floating windows only (niri#2381), so a tiled
// window reports false: the compositor answering that there is no position.
func niriTile(window niriWindow) (tileX, tileY float64, ok bool) {
	tile := window.Layout.TilePosInWorkspaceView
	if len(tile) < coordPairLen {
		return 0, 0, false
	}

	return tile[0], tile[1], true
}

// niriScreenRect combines a window's workspace-view position with its output's
// logical origin. It reports false for a tiled window and for a window whose
// size niri did not report.
func niriScreenRect(window niriWindow, output niriOutput) (image.Rectangle, bool) {
	size := window.Layout.WindowSize
	if len(size) < coordPairLen || size[0] <= 0 || size[1] <= 0 {
		return image.Rectangle{}, false
	}

	tileX, tileY, ok := niriTile(window)
	if !ok {
		return image.Rectangle{}, false
	}

	originX := output.Logical.X + int(tileX)
	originY := output.Logical.Y + int(tileY)

	return image.Rect(
		originX, originY, originX+size[0], originY+size[1],
	), true
}

// niriOrigin derives the focused window's content origin, rejecting a size
// mismatch with the AT-SPI frame.
func niriOrigin(window niriWindow, output niriOutput, frame Window) (image.Point, bool) {
	rect, ok := niriScreenRect(window, output)
	if !ok || !sizeMatches(frame.Rect, rect) {
		return image.Point{}, false
	}

	return rect.Min, true
}

// swayContentRect returns the focused node's content rectangle: rect plus
// window_rect, which excludes server-side decorations and so aligns with the
// AT-SPI content origin. A node whose window_rect carries no size falls back to
// its frame rectangle.
func swayContentRect(node *swayNode) (image.Rectangle, bool) {
	if node == nil {
		return image.Rectangle{}, false
	}

	width, height := node.WindowRect.Width, node.WindowRect.Height
	originX := node.Rect.X + node.WindowRect.X
	originY := node.Rect.Y + node.WindowRect.Y

	if width <= 0 || height <= 0 {
		width, height = node.Rect.Width, node.Rect.Height
		originX, originY = node.Rect.X, node.Rect.Y
	}

	if width <= 0 || height <= 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(originX, originY, originX+width, originY+height), true
}

// swayOrigin derives the focused node's content origin, rejecting a size
// mismatch with the AT-SPI frame.
func swayOrigin(tree *swayNode, frame Window) (image.Point, bool) {
	rect, ok := swayContentRect(findFocused(tree))
	if !ok || !sizeMatches(frame.Rect, rect) {
		return image.Point{}, false
	}

	return rect.Min, true
}

// hyprlandRect returns the active window's absolute screen rectangle. An empty
// "at" or "size" pair is Hyprland's JSON answer for a session with no active
// window, which is not a failure.
func hyprlandRect(window hyprlandWindow) (image.Rectangle, bool) {
	if len(window.At) < coordPairLen || len(window.Size) < coordPairLen {
		return image.Rectangle{}, false
	}

	if window.Size[0] <= 0 || window.Size[1] <= 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(
		window.At[0],
		window.At[1],
		window.At[0]+window.Size[0],
		window.At[1]+window.Size[1],
	), true
}

// hyprlandOrigin derives the active window's screen origin, rejecting a size
// mismatch with the AT-SPI frame.
func hyprlandOrigin(window hyprlandWindow, frame Window) (image.Point, bool) {
	rect, ok := hyprlandRect(window)
	if !ok || !sizeMatches(frame.Rect, rect) {
		return image.Point{}, false
	}

	return rect.Min, true
}
