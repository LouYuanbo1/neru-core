// Package accessibility is the entry point to this module's accessibility
// backends.
//
// It selects the backend for the running platform and converts what a backend
// returns — nodes carrying the platform's own role names — into the neutral
// element model in the element package. A program that only needs "what can I
// click" can use this package without importing a platform package:
//
//	client, err := accessibility.New(accessibility.Options{})
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
//	window, err := client.FrontmostWindow(ctx)
//	if err != nil {
//		return err
//	}
//	defer window.Release()
//
//	nodes, err := client.ClickableNodes(ctx, window, nil, 0, false)
//	if err != nil {
//		return err
//	}
//	elements, _ := accessibility.FromNodes(nodes)
//
// Every node owns an underlying platform reference; convert it or release it
// before dropping it.
package accessibility

import (
	"context"
	"runtime"
	"time"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Options configures the platform backend. Zero values mean the backend's
// documented defaults.
type Options struct {
	// ClickableRoles are native role names for the running platform
	// ("AXButton" on macOS, "push button" on Linux, "Button" on Windows).
	// Empty means element.DefaultClickableNativeRoles for this platform.
	ClickableRoles []string
	// MaxDepth bounds the tree walk below the root. Zero or negative means
	// the backend's default.
	MaxDepth int
	// MaxNodes bounds how many elements one walk visits. Zero or negative
	// means the backend's default.
	MaxNodes int
	// SkipHitTest disables the per-element visibility hit test. It is honored
	// on macOS only, where that test costs an accessibility round trip per
	// candidate; other platforms have no equivalent.
	SkipHitTest bool
	// SmoothSteps is how many intermediate move events a cursor move posts on
	// macOS. Zero moves instantly. Ignored on platforms whose pointer moves
	// are immediate by nature.
	SmoothSteps int
	// SmoothDelay is the pause between smooth move events. Zero means the
	// backend's default spacing.
	SmoothDelay time.Duration
	// DisableCompositor turns off the Linux compositor bridge, so focus
	// selection falls back to AT-SPI's ACTIVE and SHOWING ranking and extents
	// use the AT-SPI reconstruction. It is honored on Linux only, for a caller
	// that does not want the library talking to KWin, GNOME Shell or a
	// compositor CLI.
	DisableCompositor bool
	// BundleType overrides the macOS browser-engine detection the tree walk
	// uses to filter noisy web content ("electron", "chromium", "firefox",
	// "webkit"). Empty detects it from the root application. Honored on macOS
	// only.
	BundleType string
}

// Supported reports whether this build has an accessibility backend for the
// running platform.
func Supported() bool {
	switch runtime.GOOS {
	case element.GOOSDarwin, element.GOOSLinux, element.GOOSWindows:
		return true
	default:
		return false
	}
}

// Mapper returns the role mapper for the running platform, and false when the
// platform has no accessibility backend.
func Mapper() (element.RoleMapper, bool) {
	return element.MapperForGOOS(runtime.GOOS)
}

// FromNode converts a backend node into a neutral element. The backend's role
// and, on macOS, its subrole are translated through the platform's role
// mapper; an unrecognized role becomes element.RoleUnknown rather than an
// error, because a clickable element whose role is unknown is still clickable.
//
// The node remains owned by the caller; this function copies everything it
// needs.
func FromNode(node ax.Node) (*element.Element, error) {
	if node == nil {
		return nil, errs.New(errs.CodeInvalidInput, "node is nil")
	}

	role := element.RoleUnknown

	if mapper, ok := Mapper(); ok {
		subrole := ""

		if provider, ok := node.(interface{ Subrole() string }); ok {
			subrole = provider.Subrole()
		}

		if mapped, found := element.MapNativeRole(mapper, node.Role(), subrole); found {
			role = mapped
		}
	}

	options := []element.Option{
		element.WithClickable(node.IsClickable()),
		element.WithTitle(node.Title()),
		element.WithDescription(node.Description()),
		element.WithValue(node.Value()),
		element.WithFocused(node.IsFocused()),
		element.WithIdentifier(node.Identifier()),
		element.WithSource(element.SourceAccessibilityTree),
	}

	if provider, ok := node.(interface{ SearchText() string }); ok {
		options = append(options, element.WithSearchText(provider.SearchText()))
	}

	converted, err := element.NewElement(element.ID(node.ID()), node.Bounds(), role, options...)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeAccessibilityFailed, "could not build an element")
	}

	return converted, nil
}

// FromNodes converts a batch of nodes, skipping the ones whose bounds are
// empty. A real tree can contain zero-sized nodes that carry no clickable
// area; dropping them is more useful than failing the whole batch.
func FromNodes(nodes []ax.Node) []*element.Element {
	elements := make([]*element.Element, 0, len(nodes))

	for _, node := range nodes {
		converted, err := FromNode(node)
		if err != nil {
			continue
		}

		elements = append(elements, converted)
	}

	return elements
}

// ReleaseNodes releases a batch of backend nodes, which is the common cleanup
// after converting them with FromNodes.
func ReleaseNodes(nodes []ax.Node) {
	for _, node := range nodes {
		if node != nil {
			node.Release()
		}
	}
}

// SurfaceNames an auxiliary clickable surface, re-exported from the contract so
// a caller does not need to import ax for it.
type Surface = ax.Surface

// The auxiliary surfaces a backend can be asked for.
const (
	SurfaceDock               = ax.SurfaceDock
	SurfaceNotificationCenter = ax.SurfaceNotificationCenter
	SurfaceStageManager       = ax.SurfaceStageManager
	SurfacePictureInPicture   = ax.SurfacePictureInPicture
	SurfaceScreenCaptureUI    = ax.SurfaceScreenCaptureUI
)

// SurfaceNodes asks the backend for one auxiliary surface's clickable nodes.
//
// It is a free function rather than a Client method because only some
// platforms have these surfaces, and the ax.Client contract stays the same on
// all of them. A platform whose backend does not implement
// ax.SurfaceProvider reports errs.CodeNotSupported, so a caller can skip the
// surface deliberately.
func SurfaceNodes(
	ctx context.Context,
	client ax.Client,
	surface Surface,
	roles []string,
	maxDepth int,
) ([]ax.Node, error) {
	if client == nil {
		return nil, errs.New(errs.CodeInvalidInput, "accessibility client is nil")
	}

	provider, ok := client.(ax.SurfaceProvider)
	if !ok {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"the %s backend does not expose the %q surface",
			runtime.GOOS,
			surface,
		)
	}

	return provider.SurfaceNodes(ctx, surface, roles, maxDepth)
}

// Warmer is implemented by a backend whose input path is worth opening before
// the first action. The Linux backend uses it to dial libei (which can show a
// consent dialog) and open the uinput wheel at startup rather than inside a
// hotkey.
type Warmer interface {
	WarmUp(ctx context.Context) error
}

// WarmUp asks the backend to prepare its input path. A backend with nothing to
// prepare returns nil, so a caller can invoke it unconditionally at startup.
func WarmUp(ctx context.Context, client ax.Client) error {
	if client == nil {
		return errs.New(errs.CodeInvalidInput, "accessibility client is nil")
	}

	if warmer, ok := client.(Warmer); ok {
		return warmer.WarmUp(ctx)
	}

	return nil
}
