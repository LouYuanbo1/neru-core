//go:build linux

package wlrinject

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestAvailable_ReportsNoSessionWithoutWaylandDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	if Available() {
		t.Error("Available reported a Wayland session with WAYLAND_DISPLAY unset")
	}
}

func TestAvailable_ReportsNoSessionForAMissingSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-missing")

	if Available() {
		t.Error("Available reported a session for a socket that does not exist")
	}
}

func TestAvailable_ReportsASocketFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wayland-9")

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("creating the stand-in socket: %v", err)
	}

	defer func() { _ = listener.Close() }()

	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")

	if !Available() {
		t.Error("Available rejected a live unix socket")
	}
}

func TestAvailable_RejectsARegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wayland-9")

	if err := writeFile(path); err != nil {
		t.Fatalf("creating the regular file: %v", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")

	if Available() {
		t.Error("Available accepted a regular file as the Wayland socket")
	}
}

func TestAvailable_RejectsADirectory(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", ".")

	if Available() {
		t.Error("Available accepted a directory as the Wayland socket")
	}
}

func TestDisplaySocket_ResolvesARelativeNameUnderTheRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	path, err := displaySocket("wayland-0")
	if err != nil {
		t.Fatalf("displaySocket: %v", err)
	}

	if want := filepath.Join("/run/user/1000", "wayland-0"); path != want {
		t.Errorf("displaySocket = %q, want %q", path, want)
	}
}

func TestDisplaySocket_PassesAnAbsoluteNameThrough(t *testing.T) {
	path, err := displaySocket("/tmp/wayland-0")
	if err != nil {
		t.Fatalf("displaySocket: %v", err)
	}

	if path != "/tmp/wayland-0" {
		t.Errorf("displaySocket = %q, want %q", path, "/tmp/wayland-0")
	}
}

func TestDisplaySocket_RejectsAMissingName(t *testing.T) {
	if _, err := displaySocket(""); !errs.IsNotSupported(err) {
		t.Errorf("displaySocket(\"\") error = %v, want CodeNotSupported", err)
	}
}

func TestDisplaySocket_RejectsARelativeNameWithoutARuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, err := displaySocket("wayland-0"); !errs.IsNotSupported(err) {
		t.Errorf("displaySocket error = %v, want CodeNotSupported", err)
	}
}

func TestDial_RefusesWithoutAWaylandSession(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")

	client, err := Dial()
	if client != nil {
		_ = client.Close()

		t.Fatal("Dial returned a client without a Wayland session")
	}

	if !errs.IsNotSupported(err) {
		t.Errorf("Dial error = %v, want CodeNotSupported", err)
	}
}

// writeFile creates an empty regular file at path.
func writeFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}

	return file.Close()
}
