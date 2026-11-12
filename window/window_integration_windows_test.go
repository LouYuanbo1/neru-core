//go:build integration && windows

package window

import (
	"context"
	"image"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestManager_Activate_SwitchesBetweenRealWindows drives the whole Windows path
// against a real desktop: it starts a normal Win32 application window, switches
// away from it to the window that was frontmost before the test, then switches
// back, checking the foreground after each activation.
//
// The application is winver, which is a plain Win32 dialog: no store package, no
// elevation, and it owns the window itself rather than handing it to a host
// process. It is an integration test because it takes the foreground and creates
// a window; go test ./... does not run it.
//
// It also requires NERU_WINDOW_LIVE=1, because taking the foreground away from
// whatever the user is doing is disruptive, and because a process started by a
// low-integrity sandbox cannot do it at all: UIPI refuses AttachThreadInput
// across integrity levels, so the test would fail on the sandbox rather than on
// a defect. A normal desktop run is the only meaningful one.
func TestManager_Activate_SwitchesBetweenRealWindows(t *testing.T) {
	if os.Getenv("NERU_WINDOW_LIVE") == "" {
		t.Skip("set NERU_WINDOW_LIVE=1 to run a test that takes the foreground")
	}

	manager, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Whatever the user was in before the test is the first target: after
	// winver appears, that window is in the background, so switching to it
	// exercises the foreground-lock path rather than a no-op.
	original, originalErr := manager.Frontmost(ctx)
	if originalErr != nil {
		t.Logf("no foreground window to switch back to: %v", originalErr)
	}

	command := exec.Command("winver.exe")
	if startErr := command.Start(); startErr != nil {
		t.Fatalf("start winver.exe: %v", startErr)
	}

	defer func() {
		if original.ID != "" {
			if _, restoreErr := manager.Activate(
				context.Background(),
				Target{ID: original.ID},
			); restoreErr != nil {
				t.Logf("restore the original foreground window: %v", restoreErr)
			}
		}

		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()

	target := waitForWindow(ctx, t, manager, command.Process.Pid)

	if original.ID != "" && original.ID != target.ID {
		if _, err := manager.Activate(ctx, Target{ID: original.ID}); err != nil {
			t.Fatalf("Activate(%q) away from the new window: %v", original.ID, err)
		}

		assertFrontmost(ctx, t, manager, original.ID)
	}

	activated, err := manager.Activate(ctx, Target{ID: target.ID})
	if err != nil {
		t.Fatalf("Activate(%q): %v", target.ID, err)
	}

	if !activated.Focused {
		t.Fatalf("Activate returned an unfocused window: %+v", activated)
	}

	assertFrontmost(ctx, t, manager, target.ID)
}

// assertFrontmost checks the window Windows itself considers frontmost.
func assertFrontmost(ctx context.Context, t *testing.T, manager Manager, want string) {
	t.Helper()

	front, err := manager.Frontmost(ctx)
	if err != nil {
		t.Fatalf("Frontmost: %v", err)
	}

	if front.ID != want {
		t.Fatalf("frontmost is %q (%q), want %q", front.ID, front.Title, want)
	}
}

// TestManager_SetBounds_PlacesAWindow checks that the rectangle the caller names
// is the rectangle the user sees, which is not the same thing on Windows: DWM
// draws invisible resize borders around a window, so a raw SetWindowPos would
// leave the visible frame a few pixels inside the request.
//
// The window is winver, the same plain Win32 dialog the activation test uses.
// Foreground is requested but not asserted: the placement is what the method
// promises, and Info.Focused reports what the desktop actually granted.
func TestManager_SetBounds_PlacesAWindow(t *testing.T) {
	if os.Getenv("NERU_WINDOW_LIVE") == "" {
		t.Skip("set NERU_WINDOW_LIVE=1 to run a test that creates and moves a window")
	}

	manager, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	original, originalErr := manager.Frontmost(ctx)
	if originalErr != nil {
		t.Logf("no foreground window to switch back to: %v", originalErr)
	}

	command := exec.Command("winver.exe")
	if startErr := command.Start(); startErr != nil {
		t.Fatalf("start winver.exe: %v", startErr)
	}

	defer func() {
		if original.ID != "" {
			if _, restoreErr := manager.Activate(
				context.Background(),
				Target{ID: original.ID},
			); restoreErr != nil {
				t.Logf("restore the original foreground window: %v", restoreErr)
			}
		}

		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()

	target := waitForWindow(ctx, t, manager, command.Process.Pid)

	want := image.Rect(120, 90, 620, 490)

	placed, err := manager.SetBounds(ctx, Target{ID: target.ID}, want)
	if err != nil {
		t.Fatalf("SetBounds(%q, %v): %v", target.ID, want, err)
	}

	if placed.Bounds != want {
		t.Fatalf("placed at %v, want the requested %v", placed.Bounds, want)
	}

	windows, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("List after placement: %v", err)
	}

	found := false

	for _, window := range windows {
		if window.ID != placed.ID {
			continue
		}

		found = true

		if window.Bounds != want {
			t.Fatalf("List reports %v, want %v", window.Bounds, want)
		}
	}

	if !found {
		t.Fatalf("the placed window %q is not in List", placed.ID)
	}
}

// waitForWindow polls List until the process owns a titled window, which is
// what winver's dialog becomes a moment after it starts.
func waitForWindow(
	ctx context.Context,
	t *testing.T,
	manager Manager,
	pid int,
) Info {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		windows, err := manager.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		for _, candidate := range windows {
			if candidate.PID == pid && candidate.Title != "" {
				return candidate
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("process %d never showed a titled window", pid)

	return Info{}
}
