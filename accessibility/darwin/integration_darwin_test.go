//go:build integration && darwin

package darwin_test

import (
	"context"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/accessibility/darwin"
)

// TestClient_FrontmostWindowAndClickableNodes exercises the real accessibility
// server. It is an integration test because it reads the desktop the test
// machine is showing, and it skips when the process has not been granted
// accessibility permission.
func TestClient_FrontmostWindowAndClickableNodes(t *testing.T) {
	client, err := darwin.New(darwin.Options{MaxDepth: 6, MaxNodes: 500})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	if !client.CheckPermissions() {
		t.Skip("accessibility permission is not granted")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	window, err := client.FrontmostWindow(ctx)
	if err != nil {
		t.Fatalf("FrontmostWindow: %v", err)
	}
	defer window.Release()

	if window.Role() == "" {
		t.Error("the frontmost window reported no role")
	}

	nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
	if err != nil {
		t.Fatalf("ClickableNodes: %v", err)
	}

	for _, node := range nodes {
		if node.Bounds().Empty() {
			t.Errorf("node %s has empty bounds", node.ID())
		}

		if !node.IsClickable() {
			t.Errorf("node %s was returned but is not clickable", node.ID())
		}

		node.Release()
	}

	t.Logf("frontmost window %s: %d clickable elements", window.Role(), len(nodes))
}

// TestClient_MissionControlDetection calls the Dock query without asserting a
// state: whether Mission Control is up depends on the machine the test runs on.
func TestClient_MissionControlDetection(t *testing.T) {
	client, err := darwin.New(darwin.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	client.IsMissionControlActive()
}

// TestClient_ScreenBounds checks the backend reports a usable display
// rectangle, which every hint overlay depends on.
func TestClient_ScreenBounds(t *testing.T) {
	client, err := darwin.New(darwin.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	bounds := client.ActiveScreenBounds()
	if bounds.Empty() {
		t.Fatal("ActiveScreenBounds() is empty")
	}

	if bounds.Dx() < 100 || bounds.Dy() < 100 {
		t.Errorf("ActiveScreenBounds() = %v, smaller than any real display", bounds)
	}
}
