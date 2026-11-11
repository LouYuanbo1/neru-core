//go:build darwin

package darwin

import (
	"context"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Depths each surface is walked to. They are the values the daemon this backend
// was extracted from uses, and each is the shallowest depth that reaches the
// surface's controls: the Dock is flat under its application element, the
// WindowManager strip is one level deeper, and the single-window agents are
// reached three levels down.
const (
	dockTreeDepth         = 5
	stageManagerTreeDepth = 4
	flatAppTreeDepth      = 3
)

// Application bundle identifiers of the surfaces whose UI is not the frontmost
// application's.
const (
	dockBundleID               = "com.apple.dock"
	notificationCenterBundleID = "com.apple.notificationcenterui"
	pipBundleID                = "com.apple.PIPAgent"
	screenCaptureBundleID      = "com.apple.screencaptureui"
)

// surfaceSpec says how one surface is collected.
type surfaceSpec struct {
	// bundleID is the process that draws the surface.
	bundleID string
	// extraRoles are roles only this surface exposes, added to the caller's
	// roles because a hint on the surface would otherwise find nothing.
	extraRoles []string
	// depth is the walk depth used when the caller passes none.
	depth int
	// appRoot walks the application element itself rather than each of its
	// windows, which is what a surface with no window of its own needs.
	appRoot bool
}

// specForSurface returns the collection rules for a surface.
func specForSurface(surface ax.Surface) (surfaceSpec, bool) {
	switch surface {
	case ax.SurfaceDock:
		// The Dock is a flat application tree of AXDockItem children, and a
		// hint on one of them launches or switches to the application.
		return surfaceSpec{
			bundleID:   dockBundleID,
			extraRoles: []string{"AXDockItem"},
			depth:      dockTreeDepth,
			appRoot:    true,
		}, true
	case ax.SurfaceNotificationCenter:
		// Most of a notification is drawn as AXGroup, which is not a clickable
		// role by default, so it is added here.
		return surfaceSpec{
			bundleID:   notificationCenterBundleID,
			extraRoles: []string{"AXGroup"},
		}, true
	case ax.SurfaceStageManager:
		return surfaceSpec{
			bundleID: windowManagerBundleID,
			depth:    stageManagerTreeDepth,
			appRoot:  true,
		}, true
	case ax.SurfacePictureInPicture:
		return surfaceSpec{bundleID: pipBundleID, depth: flatAppTreeDepth}, true
	case ax.SurfaceScreenCaptureUI:
		return surfaceSpec{bundleID: screenCaptureBundleID, depth: flatAppTreeDepth}, true
	default:
		return surfaceSpec{}, false
	}
}

// SurfaceNodes returns the clickable nodes of one supplementary surface: the
// Dock, Notification Center, Stage Manager, Picture-in-Picture or the
// screen-capture UI, named with the ax.Surface constants. Each one is a real
// accessibility tree, so a hint placed on it clicks the same control a user
// would.
//
// roles replaces the client's configured clickable roles for this call; empty
// means the client's roles plus the extra roles the surface itself exposes
// (AXDockItem for the Dock, AXGroup for Notification Center). maxDepth overrides
// the surface's own walk depth when positive.
//
// A surface whose process is not running reports errs.CodeElementNotFound, the
// same as ApplicationByBundleID, and an unrecognized surface reports
// errs.CodeInvalidInput.
func (c *Client) SurfaceNodes(
	ctx context.Context,
	surface ax.Surface,
	roles []string,
	maxDepth int,
) ([]ax.Node, error) {
	spec, ok := specForSurface(surface)
	if !ok {
		return nil, errs.Newf(errs.CodeInvalidInput, "unknown supplementary surface %q", surface)
	}

	allowed := c.surfaceRoles(roles, spec.extraRoles)

	depth := maxDepth
	if depth <= 0 {
		depth = spec.depth
	}

	if !spec.appRoot {
		return c.ClickableElementsFromBundleID(ctx, spec.bundleID, allowed, depth)
	}

	app, err := c.ApplicationByBundleID(ctx, spec.bundleID)
	if err != nil {
		return nil, err
	}

	defer app.Release()

	appRef := extractElement(app)

	rootInfo, err := c.readInfo(appRef)
	if err != nil && rootInfo.role == "" {
		return nil, err
	}

	// A surface whose bundle identifier resolved to something that is not an
	// application would walk an unrelated tree, and every hint on it would
	// click the wrong control.
	if rootInfo.role != "AXApplication" {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the %s surface answered role %q instead of AXApplication",
			surface,
			rootInfo.role,
		)
	}

	return c.walkNodes(ctx, walkRequest{
		root:       appRef,
		rootInfo:   rootInfo,
		roles:      c.roleSetFor(allowed),
		depth:      depth,
		bundleType: c.bundleTypeFor(spec.bundleID),
	})
}

// surfaceRoles resolves the role list one surface is walked with: the caller's
// roles when it named any, otherwise the client's configured roles, plus the
// roles only that surface exposes. A role named twice appears once.
func (c *Client) surfaceRoles(roles, extra []string) []string {
	base := roles
	if len(base) == 0 {
		base = c.ClickableRoles()
	}

	combined := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(base)+len(extra))

	for _, role := range append(append([]string(nil), base...), extra...) {
		if role == "" {
			continue
		}

		if _, duplicate := seen[role]; duplicate {
			continue
		}

		seen[role] = struct{}{}
		combined = append(combined, role)
	}

	return combined
}
