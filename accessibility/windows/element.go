//go:build windows

package windows

import (
	"image"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/errs"
)

// errNilApp is what Info reports for a nil receiver, which can only happen
// through a nil *App stored in an ax.App interface.
var errNilApp = errs.New(errs.CodeInvalidInput, "application is nil")

// controlTypeWindow is the UIA control-type name a top-level window reports.
// The ax contract asks a window for its native role, and on this platform that
// is a UIA name rather than an AX one.
const controlTypeWindow = "Window"

// controlTypeApplication is the name reported for an application. UI
// Automation has no application control type, so this is the neutral name the
// element vocabulary maps RoleApplication onto, kept here so an App answers
// with something stable instead of an empty string.
const controlTypeApplication = "Application"

// Window is one top-level window on the desktop.
//
// It is a Win32 window handle plus the metadata a caller needs to identify it,
// not a live UIA element: a Window does not keep the window open, and a handle
// that has been destroyed makes a later query against it fail rather than
// misbehave.
type Window struct {
	hwnd  uintptr
	pid   uint32
	exe   string
	title string
}

// Role returns the window's native role, which on Windows is always "Window".
func (w *Window) Role() string {
	if w == nil {
		return ""
	}

	return controlTypeWindow
}

// Title returns the text in the window's title bar, which may be empty for a
// window that has not finished loading.
func (w *Window) Title() string {
	if w == nil {
		return ""
	}

	return w.title
}

// BundleIdentifier returns the base name of the executable that owns the
// window, such as "notepad.exe". It is the Windows analogue of a macOS bundle
// identifier and is what ApplicationByBundleID looks up.
func (w *Window) BundleIdentifier() string {
	if w == nil {
		return ""
	}

	return w.exe
}

// Release is a no-op. A Window holds a handle rather than a COM reference, so
// there is nothing to free; it is safe to call more than once.
func (w *Window) Release() {}

// App is an application: a process, identified by its executable.
//
// UI Automation has no application object. A process is addressed instead, and
// every visible top-level window it owns stands in for the application's
// windows.
type App struct {
	pid uint32
	exe string
}

// BundleIdentifier returns the base name of the executable that was launched,
// such as "notepad.exe".
func (a *App) BundleIdentifier() string {
	if a == nil {
		return ""
	}

	return a.exe
}

// Info returns the application's role and title. The role is the neutral
// "Application" name, since UI Automation has no application control type, and
// the title is the executable's base name: a process has no title of its own.
func (a *App) Info() (*ax.AppInfo, error) {
	if a == nil {
		return nil, errNilApp
	}

	return &ax.AppInfo{Role: controlTypeApplication, Title: a.exe}, nil
}

// Release is a no-op. An App holds a process id rather than a COM reference,
// so there is nothing to free; it is safe to call more than once.
func (a *App) Release() {}

// Node is one control found in a UIA subtree.
//
// Every field is a copy of a cached UIA property taken while the query ran, so
// the node keeps nothing alive. Two nodes describe the same control only if
// their data is identical: UIA runtime ids are not exposed, so a node's ID is
// unique to the node rather than to the control behind it.
type Node struct {
	id          string
	bounds      image.Rectangle
	role        string
	title       string
	description string
	value       string
	identifier  string
	focused     bool
	clickable   bool
}

// ID returns an identifier that is unique among the nodes this client has
// produced. It exists so a caller can key a hint by node; it is not stable
// across queries, because UIA offers no comparable identity for cached data.
func (n *Node) ID() string {
	if n == nil {
		return ""
	}

	return n.id
}

// Bounds returns the control's rectangle in physical screen pixels, clipped to
// the window frame and to the virtual screen. It is never empty: a control
// with no usable rectangle is dropped instead of returned.
func (n *Node) Bounds() image.Rectangle {
	if n == nil {
		return image.Rectangle{}
	}

	return n.bounds
}

// Role returns the control's UIA control-type programmatic name, such as
// "Button" or "Edit". A control type outside the documented range is reported
// as its numeric id, which keeps it addressable rather than silently unnamed.
func (n *Node) Role() string {
	if n == nil {
		return ""
	}

	return n.role
}

// Title returns the control's UIA Name, which is what a screen reader
// announces: a button's caption, a field's label, or a link's text.
func (n *Node) Title() string {
	if n == nil {
		return ""
	}

	return n.title
}

// Description returns the control's UIA HelpText, which providers fill from a
// tooltip or an ARIA description. An ordinary "..." for an ellipsis menu is a
// typical value.
func (n *Node) Description() string {
	if n == nil {
		return ""
	}

	return n.description
}

// Value returns the control's UIA Value as a string. It is always empty for a
// password field, so a secret never reaches a caller or a log.
func (n *Node) Value() string {
	if n == nil {
		return ""
	}

	return n.value
}

// IsFocused reports whether the control had keyboard focus when the query ran.
// It is only read when the query asked for state: the property is a cached one
// like any other, and reading it costs the provider work for every control in
// the tree.
func (n *Node) IsFocused() bool {
	if n == nil {
		return false
	}

	return n.focused
}

// Identifier returns the control's UIA AutomationId, the identifier the
// application's developer gave it. Like IsFocused it is only read when the
// query asked for state, and is empty otherwise.
func (n *Node) Identifier() string {
	if n == nil {
		return ""
	}

	return n.identifier
}

// IsClickable reports whether the node is a click target. Every node this
// package returns is one: a control is kept only if it matches the role
// filter and has a rectangle on screen, and a coordinate click lands on any
// such rectangle whether or not the provider implements an action pattern.
func (n *Node) IsClickable() bool {
	if n == nil {
		return false
	}

	return n.clickable
}

// Release drops the node's data. A Node holds no COM reference, so there is
// nothing to free; it is safe to call more than once.
func (n *Node) Release() {}

// The concrete types must satisfy the contract the ax package states, or a
// caller's type assertion against ax.Element would fail at run time.
var (
	_ ax.Element = (*Window)(nil)
	_ ax.Window  = (*Window)(nil)
	_ ax.Element = (*App)(nil)
	_ ax.App     = (*App)(nil)
	_ ax.Element = (*Node)(nil)
	_ ax.Node    = (*Node)(nil)
	_ ax.Client  = (*Client)(nil)
)
