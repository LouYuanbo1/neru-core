//go:build darwin

package darwin

import (
	"context"
	"slices"
	"testing"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/errs"
)

func TestSpecForSurface_CarriesEachSurfaceRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		surface    ax.Surface
		bundleID   string
		extraRoles []string
		depth      int
		appRoot    bool
	}{
		{surface: ax.SurfaceDock, bundleID: "com.apple.dock", extraRoles: []string{"AXDockItem"}, depth: dockTreeDepth, appRoot: true},
		{
			surface:    ax.SurfaceNotificationCenter,
			bundleID:   "com.apple.notificationcenterui",
			extraRoles: []string{"AXGroup"},
		},
		{surface: ax.SurfaceStageManager, bundleID: windowManagerBundleID, depth: stageManagerTreeDepth, appRoot: true},
		{surface: ax.SurfacePictureInPicture, bundleID: "com.apple.PIPAgent", depth: flatAppTreeDepth},
		{surface: ax.SurfaceScreenCaptureUI, bundleID: "com.apple.screencaptureui", depth: flatAppTreeDepth},
	}

	for _, testCase := range tests {
		t.Run(string(testCase.surface), func(t *testing.T) {
			t.Parallel()

			spec, ok := specForSurface(testCase.surface)
			if !ok {
				t.Fatalf("specForSurface(%q) was not recognised", testCase.surface)
			}

			if spec.bundleID != testCase.bundleID {
				t.Errorf("bundleID = %q, want %q", spec.bundleID, testCase.bundleID)
			}

			if !slices.Equal(spec.extraRoles, testCase.extraRoles) {
				t.Errorf("extraRoles = %v, want %v", spec.extraRoles, testCase.extraRoles)
			}

			if spec.depth != testCase.depth {
				t.Errorf("depth = %d, want %d", spec.depth, testCase.depth)
			}

			if spec.appRoot != testCase.appRoot {
				t.Errorf("appRoot = %v, want %v", spec.appRoot, testCase.appRoot)
			}
		})
	}

	if _, ok := specForSurface(ax.Surface("nope")); ok {
		t.Error("an unknown surface must not resolve to a spec")
	}
}

func TestSurfaceRoles_AddsTheSurfaceRolesOnce(t *testing.T) {
	t.Parallel()

	client := &Client{roles: []string{"AXButton", "AXLink"}}

	got := client.surfaceRoles(nil, []string{"AXDockItem", "AXButton"})
	want := []string{"AXButton", "AXLink", "AXDockItem"}

	if !slices.Equal(got, want) {
		t.Fatalf("surfaceRoles = %v, want %v", got, want)
	}

	// An explicit role list replaces the client's own, and still gains the
	// surface's roles.
	got = client.surfaceRoles([]string{"AXImage"}, []string{"AXDockItem"})
	want = []string{"AXImage", "AXDockItem"}

	if !slices.Equal(got, want) {
		t.Fatalf("surfaceRoles with explicit roles = %v, want %v", got, want)
	}
}

func TestSurfaceNodes_RejectsAnUnknownSurface(t *testing.T) {
	t.Parallel()

	client := &Client{}

	_, err := client.SurfaceNodes(context.Background(), ax.Surface("not-a-surface"), nil, 0)
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Fatalf("SurfaceNodes error = %v, want CodeInvalidInput", err)
	}
}

// TestSurfaceNodes_ReportsAMissingSurfaceProcess pins the error a surface whose
// process is not running returns, which is the case a caller sees on a machine
// without Picture-in-Picture or the screen-capture UI on screen.
func TestSurfaceNodes_ReportsAMissingSurfaceProcess(t *testing.T) {
	client, err := New(Options{})
	if err != nil {
		t.Skipf("the accessibility frameworks are unavailable: %v", err)
	}
	defer client.Close()

	_, err = client.SurfaceNodes(context.Background(), ax.SurfacePictureInPicture, nil, 0)
	if err != nil && !errs.IsCode(err, errs.CodeElementNotFound) {
		t.Fatalf("SurfaceNodes error = %v, want nil or CodeElementNotFound", err)
	}
}
