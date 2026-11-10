//go:build linux

package wayland

import (
	"context"
	"image"
	"net"
	"path/filepath"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestAvailable_ReportsFalseWithoutWaylandDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	if Available() {
		t.Error("Available reported a session with WAYLAND_DISPLAY unset")
	}
}

func TestAvailable_ReportsFalseForAMissingSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-does-not-exist")

	if Available() {
		t.Error("Available reported a session whose socket does not exist")
	}
}

func TestAvailable_ReportsTrueForALiveSocket(t *testing.T) {
	dir := t.TempDir()

	listener, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: filepath.Join(dir, "wayland-test"),
		Net:  "unix",
	})
	if err != nil {
		t.Fatalf("listen on a test socket: %v", err)
	}
	defer listener.Close()

	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")

	if !Available() {
		t.Error("Available did not see the listening socket")
	}
}

func TestCaptureRect_RejectsEmptyRegion(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	_, err := CaptureRect(context.Background(), image.Rectangle{})
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("error = %v, want CodeInvalidInput", err)
	}
}

func TestCaptureScreen_WithoutWaylandSessionIsNotSupported(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	_, err := CaptureScreen(context.Background())
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestCaptureScreen_ReportsAnAlreadyCanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CaptureScreen(ctx)
	if !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("error = %v, want CodeContextCanceled", err)
	}
}
