//go:build linux

package wayland

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

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
	socket := filepath.Join(dir, "wayland-test")

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
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

func TestDisplaySocket_ResolvesRelativeNameUnderRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	got, err := displaySocket("wayland-0")
	if err != nil {
		t.Fatalf("displaySocket: %v", err)
	}

	if want := filepath.Join("/run/user/1000", "wayland-0"); got != want {
		t.Errorf("socket = %q, want %q", got, want)
	}
}

func TestDisplaySocket_KeepsAnAbsoluteName(t *testing.T) {
	got, err := displaySocket("/tmp/custom-wayland")
	if err != nil {
		t.Fatalf("displaySocket: %v", err)
	}

	if got != "/tmp/custom-wayland" {
		t.Errorf("socket = %q, want the absolute path unchanged", got)
	}
}

func TestDisplaySocket_RejectsARelativeNameWithoutRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, err := displaySocket("wayland-0"); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestDial_WithoutWaylandDisplayIsNotSupported(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	conn, err := Dial()
	if conn != nil {
		conn.Close()

		t.Fatal("Dial returned a connection with WAYLAND_DISPLAY unset")
	}

	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestDial_MissingSocketIsNotSupported(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-does-not-exist")

	conn, err := Dial()
	if conn != nil {
		conn.Close()

		t.Fatal("Dial returned a connection to a missing socket")
	}

	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestContextError_MapsDeadlineToTimeout(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if err := contextError(ctx.Err()); !errs.IsCode(err, errs.CodeTimeout) {
		t.Errorf("error = %v, want CodeTimeout", err)
	}
}

func TestContextError_MapsCancellationToCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := contextError(ctx.Err()); !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("error = %v, want CodeContextCanceled", err)
	}
}

func TestCheckContext_ReportsAnAlreadyCanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := checkContext(ctx); !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("error = %v, want CodeContextCanceled", err)
	}
}
