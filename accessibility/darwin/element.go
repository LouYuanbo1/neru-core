//go:build darwin

package darwin

import (
	"image"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
)

// The concrete types must satisfy the contract the ax package states, or a
// caller's type assertion against ax.Element would fail at run time.
var (
	_ ax.Element         = (*windowElement)(nil)
	_ ax.Window          = (*windowElement)(nil)
	_ ax.Element         = (*appElement)(nil)
	_ ax.App             = (*appElement)(nil)
	_ ax.Element         = (*node)(nil)
	_ ax.Node            = (*node)(nil)
	_ ax.Client          = (*Client)(nil)
	_ ax.SurfaceProvider = (*Client)(nil)
)

// elementRef is the ownership handle every object in this package shares. AX
// hands out +1 references and the Go side owns each one, so Release must be
// called exactly once per reference the API handed over.
type elementRef struct {
	ref uintptr
}

// Release drops the AX reference. It is idempotent, so a caller that releases
// a node and then releases the window it came from cannot double-free the
// underlying element.
func (e *elementRef) Release() {
	if e == nil || e.ref == 0 {
		return
	}

	ref := e.ref
	e.ref = 0

	cfRelease(ref)
}

// cfRelease releases a CFTypeRef using the process-wide framework table. It is
// a package function so the small value types can release without holding a
// client.
func cfRelease(ref uintptr) {
	if api := frameworksAPI; api != nil && ref != 0 {
		api.cf.release(ref)
	}
}

// retype wraps a reference the caller already owns.
func retype(ref uintptr) elementRef {
	return elementRef{ref: ref}
}

// windowElement is an AX window. Role is read once when the window is
// returned, because a caller that asks for the role wants what it was rather
// than another IPC round trip.
type windowElement struct {
	elementRef

	role string
}

// Role returns the window's native AX role, typically "AXWindow".
func (w *windowElement) Role() string {
	return w.role
}

// appElement is an AX application object.
type appElement struct {
	elementRef

	client *Client
	pid    int32
}

// BundleIdentifier returns the application's bundle identifier, or "" when the
// process is gone or has none.
func (a *appElement) BundleIdentifier() string {
	return runningApplicationBundleID(a.pid)
}

// Info returns the application's native role and title.
func (a *appElement) Info() (*ax.AppInfo, error) {
	info, err := a.client.readInfo(a.ref)
	if err != nil {
		return nil, err
	}

	return &ax.AppInfo{Role: info.role, Title: info.title}, nil
}

// node is one clickable element found in a tree walk. Every field is copied
// out of the accessibility server before the node is returned, except the
// reference, which Release drops.
type node struct {
	elementRef

	id          string
	bounds      image.Rectangle
	role        string
	subrole     string
	title       string
	description string
	value       string
	identifier  string
	searchText  string
	focused     bool
	clickable   bool
}

// ID returns an identifier unique within the call that produced the node.
func (n *node) ID() string { return n.id }

// Bounds returns the element's global top-left-origin rectangle.
func (n *node) Bounds() image.Rectangle { return n.bounds }

// Role returns the element's native AX role.
func (n *node) Role() string { return n.role }

// Subrole returns the element's native AX subrole, when it has one. It is the
// attribute that distinguishes a search field from a text field and a switch
// from a check box.
func (n *node) Subrole() string { return n.subrole }

// Title returns the element's native title.
func (n *node) Title() string { return n.title }

// Description returns the element's native description.
func (n *node) Description() string { return n.description }

// Value returns the element's native value.
func (n *node) Value() string { return n.value }

// IsFocused reports whether the element has keyboard focus.
func (n *node) IsFocused() bool { return n.focused }

// Identifier returns the identifier the application's developer gave the
// element, when the caller asked for element state.
func (n *node) Identifier() string { return n.identifier }

// IsClickable reports the result of the visibility and role checks the walk
// applied.
func (n *node) IsClickable() bool { return n.clickable }

// SearchText returns the text of the element and of the descendants that were
// walked, so a consumer can match a hint by what its subtree says.
func (n *node) SearchText() string { return n.searchText }

// extractElement returns the AX reference behind any element a caller passes
// back in. The reference stays owned by the caller's value.
func extractElement(value ax.Element) uintptr {
	switch typed := value.(type) {
	case *windowElement:
		return typed.ref
	case *appElement:
		return typed.ref
	case *node:
		return typed.ref
	default:
		return 0
	}
}
