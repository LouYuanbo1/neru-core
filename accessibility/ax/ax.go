// Package ax defines the accessibility access contract that each platform
// backend implements.
//
// The role strings in this package are *platform-native* (macOS "AXButton",
// Windows UIA names, Linux AT-SPI roles): a backend compares its own native
// tree against them to decide what is clickable. This is deliberately distinct
// from the neutral element.Role the library uses after classification, so the
// two vocabularies do not get merged here.
package ax

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/action"
)

// Element is a generic accessibility element.
type Element interface {
	Release()
}

// Window is a window element.
type Window interface {
	Element
	Role() string
}

// Client is the interface for accessibility operations.
type Client interface {
	// Window and App operations
	FrontmostWindow(ctx context.Context) (Window, error)
	AllWindows(ctx context.Context) ([]Window, error)
	FrontmostAndPopoverWindows(ctx context.Context) ([]Window, error)
	FocusedApplication(ctx context.Context) (App, error)
	ApplicationByBundleID(ctx context.Context, bundleID string) (App, error)
	ClickableNodes(
		ctx context.Context,
		root Element,
		roles []string,
		maxDepth int,
		readState bool,
	) ([]Node, error)
	MenuBarClickableElements(ctx context.Context, maxDepth int) ([]Node, error)
	// MissionControlClickableNodes returns what Mission Control draws that a
	// hint can land on while it is up: the current desktop's windows, and the
	// Spaces bar while it is expanded.
	MissionControlClickableNodes(ctx context.Context, maxDepth int) ([]Node, error)
	ClickableElementsFromBundleID(
		ctx context.Context,
		bundleID string,
		roles []string,
		maxDepth int,
	) ([]Node, error)
	ActiveScreenBounds() image.Rectangle

	// Actions
	PerformAction(
		actionType action.Type,
		p image.Point,
		restoreCursor bool,
		modifiers action.Modifiers,
	) error
	// Scroll posts a wheel event at the current cursor position. Positive
	// deltaY scrolls up and positive deltaX scrolls left; the sign is part of
	// this contract, not a platform's, so callers can feed the same deltas to
	// every backend. A platform that lacks native wheel input reports
	// CodeNotSupported rather than ignoring the call.
	Scroll(deltaX, deltaY int, modifiers action.Modifiers) error
	MoveMouse(p image.Point, bypassSmooth bool)
	CursorPosition() image.Point

	// System
	CheckPermissions() bool
	SetClickableRoles(roles []string)
	ClickableRoles() []string
	IsMissionControlActive() bool
	// SupportsSupplementaryElements reports whether the platform exposes the
	// auxiliary surfaces hints can also scan (Dock, menu bar, Notification
	// Center, Stage Manager, Picture-in-Picture, screen-capture UI).
	SupportsSupplementaryElements() bool

	// Close releases any resources held by the client.
	Close() error
}

// AppInfo contains information about an application.
type AppInfo struct {
	Role  string
	Title string
}

// App is an application element.
type App interface {
	Element
	BundleIdentifier() string
	Info() (*AppInfo, error)
}

// Node is a node in the accessibility tree.
//
// Role returns the platform's own role name, not a neutral element.Role. Use
// element's role mappers (or accessibility.FromNode) to translate it.
//
// A backend may additionally implement:
//
//	Subrole() string       // macOS AXSubrole, where the platform reports one
//	SearchText() string    // text of the node's subtree, for hint search
//
// Both are optional so a backend without the concept need not stub them.
type Node interface {
	ID() string
	Bounds() image.Rectangle
	Role() string
	Title() string
	Description() string
	Value() string
	IsFocused() bool
	Identifier() string
	IsClickable() bool
	Release()
}

// Surface names an auxiliary clickable surface a platform may expose outside
// an application's own window: the macOS Dock, Notification Center, Stage
// Manager strip, Picture-in-Picture controls and the screen-capture UI. A
// platform without the concept reports CodeNotSupported for it.
type Surface string

// The auxiliary surfaces a backend can be asked for.
const (
	SurfaceDock               Surface = "dock"
	SurfaceNotificationCenter Surface = "notification_center"
	SurfaceStageManager       Surface = "stage_manager"
	SurfacePictureInPicture   Surface = "picture_in_picture"
	SurfaceScreenCaptureUI    Surface = "screen_capture_ui"
)

// SurfaceProvider is implemented by a backend that can scan auxiliary
// surfaces. It is separate from Client because only macOS has them today, and
// a backend without the concept should not carry a stub for every surface.
//
// roles are native role names; nil means the backend's configured roles plus
// whatever that surface additionally needs (AXDockItem for the Dock, for
// example). The returned nodes obey the same ownership rules as
// Client.ClickableNodes.
type SurfaceProvider interface {
	SurfaceNodes(ctx context.Context, surface Surface, roles []string, maxDepth int) ([]Node, error)
}
