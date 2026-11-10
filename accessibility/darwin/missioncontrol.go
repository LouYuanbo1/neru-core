//go:build darwin

package darwin

import (
	"context"
	"strings"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
)

// Mission Control is drawn by the WindowManager process, which lists the
// windows of every desktop. The attributes below are the ones that identify
// what it shows; neither is documented, and each is the only way to tell a
// thumbnail of the desktop the user is looking at from one of another desktop.
const (
	// windowManagerBundleID is the process that draws Mission Control.
	windowManagerBundleID = "com.apple.WindowManager"
	// spacesBarIdentifier names the group holding the Spaces bar's desktops and
	// its add-desktop button.
	spacesBarIdentifier = "mc.spaces"
	// windowIDAttribute names the window a thumbnail stands for.
	windowIDAttribute = "wid"
	// windowIsOnScreenKey is the name of the window-description key carrying the
	// on-screen flag. CoreGraphics gives that key the same string as its name.
	windowIsOnScreenKey = "kCGWindowIsOnscreen"
	// axRoleButton is the role Mission Control's Spaces bar entries and desktop
	// thumbnails report.
	axRoleButton = "AXButton"
	// missionControlTreeDepth reaches the Spaces bar's desktop buttons, one
	// level below Stage Manager's deepest item, in the WindowManager tree
	// Mission Control draws into.
	missionControlTreeDepth = 5
)

// MissionControlClickableNodes returns what Mission Control draws that a hint
// can land on: the current desktop's window thumbnails, and the Spaces bar
// while it is expanded.
//
// WindowManager lists the windows of every desktop, so a thumbnail is kept only
// while its window is on screen, which during Mission Control is the current
// desktop's alone. A Spaces bar button reports its center as its position, so
// its frame is moved back by half its size before the clickability check reads
// it, since that check hit-tests a frame's center. While the bar is collapsed
// those centers lie above the screen, so none of its buttons is kept until it
// expands.
//
// A positive maxDepth overrides the depth this walk uses, which defaults to
// missionControlTreeDepth. When the WindowManager process is not running there
// is no Mission Control to read and the result is empty with no error.
func (c *Client) MissionControlClickableNodes(ctx context.Context, maxDepth int) ([]ax.Node, error) {
	app, err := c.ApplicationByBundleID(ctx, windowManagerBundleID)
	if err != nil || app == nil {
		return nil, nil
	}

	defer app.Release()

	appRef := extractElement(app)

	rootInfo, infoErr := c.readInfo(appRef)
	if infoErr != nil && rootInfo.role == "" {
		return nil, infoErr
	}

	if maxDepth <= 0 {
		maxDepth = missionControlTreeDepth
	}

	screenTop := float64(c.ActiveScreenBounds().Min.Y)
	barCollapsed := false

	visit := func(node *treeNode, info *elementInfo) bool {
		if info.role != axRoleButton || !inSpacesBar(node) {
			return true
		}

		if info.position.Y < screenTop {
			barCollapsed = true
		}

		info.position = spacesBarFrame(info.position, info.size)

		return true
	}

	drop := func(candidate *treeNode) bool {
		if inSpacesBar(candidate) {
			return barCollapsed
		}

		if hasWindow, onScreen := c.windowOnScreen(candidate.ref); hasWindow && !onScreen {
			return true
		}

		return false
	}

	return c.walkNodes(ctx, walkRequest{
		root:          appRef,
		rootInfo:      rootInfo,
		roles:         c.roleSetFor(nil),
		depth:         maxDepth,
		visit:         visit,
		dropCandidate: drop,
	})
}

// inSpacesBar reports whether node sits in Mission Control's Spaces bar, which
// is the group whose identifier is "mc.spaces".
func inSpacesBar(node *treeNode) bool {
	for ancestor := node.parent; ancestor != nil; ancestor = ancestor.parent {
		if ancestor.info != nil && ancestor.info.identifier == spacesBarIdentifier {
			return true
		}
	}

	return false
}

// spacesBarFrame converts the center a Spaces bar button reports as its position
// into the frame origin the walk measures and hit-tests. The daemon this was
// ported from halves an integral size, which rounds an odd size down; halving
// the fractional size here and rounding when the frame becomes a rectangle lands
// on the same origin.
func spacesBarFrame(center cgPoint, size cgSize) cgPoint {
	return cgPoint{
		X: center.X - size.Width/2,
		Y: center.Y - size.Height/2,
	}
}

// windowOnScreen reports whether the element names a window through
// WindowManager's undocumented "wid" attribute and, when it does, whether that
// window is on screen. An element without the attribute is not a window
// thumbnail, and its answer is "no window, keep it".
func (c *Client) windowOnScreen(ref uintptr) (bool, bool) {
	value, status := c.f.copyAttribute(ref, windowIDAttribute)
	if status != kAXErrorSuccess || value == 0 {
		return false, false
	}

	defer c.f.cf.release(value)

	windowID, ok := c.f.cfNumberSInt32(value)
	if !ok || windowID <= 0 {
		return false, false
	}

	return true, c.f.windowIsOnScreen(uint32(windowID))
}

// windowIsOnScreen asks the window server whether a window id is on screen.
//
// A window id the window server no longer knows answers nothing, and the
// thumbnail is dropped: the window is gone rather than on another desktop.
func (f *frameworks) windowIsOnScreen(windowID uint32) bool {
	// CFArrayCreate with a NULL callbacks table: the array holds a raw
	// CGWindowID rather than a CFType, so nothing is retained or released with
	// it.
	identifiers := []uintptr{uintptr(windowID)}

	array := f.cf.arrayCreate(0, identifiers, 1, 0)
	if array == 0 {
		return false
	}

	defer f.cf.release(array)

	descriptions := f.cg.windowDescription(array)
	if descriptions == 0 {
		return false
	}

	defer f.cf.release(descriptions)

	if f.cf.arrayGetCount(descriptions) == 0 {
		return false
	}

	description := f.cf.arrayGetValueAtIndex(descriptions, 0)
	if description == 0 || !f.isCFDictionary(description) {
		return false
	}

	onScreen, ok := f.onScreenFlag(description)
	if !ok {
		return false
	}

	return onScreen
}

// onScreenFlag reads the on-screen boolean out of one window description
// dictionary.
//
// The key is kCGWindowIsOnscreen, whose CoreFoundation value is that same
// string, so the key can be looked up by name through the string table the
// package already caches. That avoids reading the exported data symbol and
// turning its uintptr back into a pointer, which go vet rejects. When the name
// no longer matches, the description is scanned for the boolean it carries,
// preferring a key whose name mentions "Onscreen" if it carries several.
func (f *frameworks) onScreenFlag(description uintptr) (bool, bool) {
	if flag, ok := f.cfBool(f.cf.dictionaryGetValue(description, f.cfStringRef(windowIsOnScreenKey))); ok {
		return flag, true
	}

	count := f.cf.dictionaryGetCount(description)
	if count <= 0 {
		return false, false
	}

	keys := make([]uintptr, count)
	values := make([]uintptr, count)

	f.cf.dictionaryGetKeysAndValues(description, &keys[0], &values[0])

	candidate := -1

	for index, key := range keys {
		if _, ok := f.cfBool(values[index]); !ok {
			continue
		}

		if f.isCFString(key) && strings.Contains(strings.ToLower(f.goString(key)), "onscreen") {
			candidate = index

			break
		}

		if candidate < 0 {
			candidate = index
		}
	}

	if candidate < 0 {
		return false, false
	}

	return f.cfBool(values[candidate])
}
