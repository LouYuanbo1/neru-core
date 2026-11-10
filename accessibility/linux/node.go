//go:build linux

package linux

import (
	"image"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
)

// The ax.Window, ax.App and ax.Node implementations. All three are value types
// around an AT-SPI element reference: AT-SPI holds no client-side handle that
// has to be released, so Release is a no-op and the reference is simply dropped
// when the value is collected.

// atspiWindow is one AT-SPI top-level window. It is the root ClickableNodes
// walks.
type atspiWindow struct {
	// ref addresses the window on the accessibility bus.
	ref accessible
	// role is the AT-SPI role name the window reported, which is "frame" for a
	// normal top-level window and "dialog" or "window" for the older spellings.
	role string
	// appID and title are the compositor's identity for this window, read when it
	// was selected as the focused one. They let a clickable scan ask the
	// compositor for that window's origin instead of reconstructing it from
	// AT-SPI. Both are empty for a window that came from an application walk,
	// where no compositor snapshot was taken.
	appID string
	title string
}

// Release implements ax.Element. AT-SPI elements are references, not handles, so
// there is nothing to free.
func (w *atspiWindow) Release() {}

// Role implements ax.Window, and reports the native AT-SPI role name so a caller
// can compare it against the same vocabulary it configured.
func (w *atspiWindow) Role() string { return w.role }

// atspiApp is one AT-SPI application, which is one entry under the registry
// root.
//
// Linux has no bundle identifiers. AT-SPI identifies an application by the bus
// connection it owns and the display name it reports, and the closest thing to
// a stable identifier is the executable name behind the owning process. All
// three are kept so BundleIdentifier can answer with the most identifier-like
// one that exists on this session.
type atspiApp struct {
	// ref addresses the application's root accessible.
	ref accessible
	// name is the display name the application reported, such as "Firefox".
	name string
	// pid is the owning process, or 0 when the toolkit does not report it.
	pid int32
	// bundleID is the identity this backend reports for the application: the
	// display name, else the process name, else the bus name.
	bundleID string
}

// Release implements ax.Element.
func (a *atspiApp) Release() {}

// BundleIdentifier implements ax.App. See atspiApp for what the identifier is on
// Linux.
func (a *atspiApp) BundleIdentifier() string { return a.bundleID }

// Info implements ax.App.
func (a *atspiApp) Info() (*ax.AppInfo, error) {
	return &ax.AppInfo{
		Role:  roleApplication,
		Title: a.name,
	}, nil
}

// atspiNode is one clickable element captured during a walk. Every field is read
// while the element is reachable, because AT-SPI has no cheap way to re-resolve
// a reference later: an application may have rebuilt its tree in between.
type atspiNode struct {
	// id identifies the node across scans, as the element's object path plus the
	// bus name that owns it.
	id string
	// role is the AT-SPI role name, after effectiveRole has rewritten editable
	// text into an entry.
	role string
	// bounds is the element's rectangle in screen pixels, clipped to the screen.
	bounds image.Rectangle
	// title, description and value are the element's accessible texts. Value is
	// read only for the roles that have one, so a password never leaves the
	// application.
	title       string
	description string
	value       string
	// identifier is the developer-assigned accessible id, read only when the
	// caller asked for state.
	identifier string
	// focused is true when the element holds keyboard focus, read only when the
	// caller asked for state.
	focused bool
}

// ID implements ax.Node.
func (n *atspiNode) ID() string { return n.id }

// Bounds implements ax.Node.
func (n *atspiNode) Bounds() image.Rectangle { return n.bounds }

// Role implements ax.Node. It is a native AT-SPI role name.
func (n *atspiNode) Role() string { return n.role }

// Title implements ax.Node.
func (n *atspiNode) Title() string { return n.title }

// Description implements ax.Node.
func (n *atspiNode) Description() string { return n.description }

// Value implements ax.Node.
func (n *atspiNode) Value() string { return n.value }

// IsFocused implements ax.Node. It is false unless ClickableNodes was asked to
// read state.
func (n *atspiNode) IsFocused() bool { return n.focused }

// Identifier implements ax.Node. It is empty unless ClickableNodes was asked to
// read state.
func (n *atspiNode) Identifier() string { return n.identifier }

// IsClickable implements ax.Node. A node only reaches a caller because the walk
// matched its role, found it showing and enabled, and measured a non-empty
// on-screen rectangle, so it is clickable by construction.
func (n *atspiNode) IsClickable() bool { return true }

// Release implements ax.Element.
func (n *atspiNode) Release() {}
