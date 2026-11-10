//go:build linux

// Package wayland speaks just enough of the Wayland wire protocol to capture a
// compositor output into an *image.RGBA, in pure Go.
//
// A Wayland session has no global screen the way X11 has a root window. A
// wlroots compositor instead offers zwlr_screencopy_manager_v1, which copies a
// frame into a client-provided shared-memory buffer. This package implements
// only what such a one-shot grab needs:
//
//   - wl_display: sync, get_registry
//   - wl_registry: bind
//   - wl_output: geometry, mode, scale, done
//   - wl_shm: create_pool and the format event
//   - wl_shm_pool: create_buffer, destroy
//   - wl_buffer: destroy
//   - wl_callback: done
//   - zwlr_screencopy_manager_v1: capture_output, destroy
//   - zwlr_screencopy_frame_v1: buffer, flags, ready, failed, damage,
//     linux_dmabuf, buffer_done, copy, destroy
//
// No surface, seat or input object is bound, so opening a connection cannot
// disturb the session. Each capture opens its own connection: the reader is a
// single goroutine that queues decoded messages, and every exported call drains
// that queue until its reply arrives or its context ends. Calls on one Conn are
// serialized by a mutex.
//
// Shared memory is a wl_shm buffer only, allocated as an unlinked file mapped
// into this process; its descriptor travels to the compositor as SCM_RIGHTS
// ancillary data on wl_shm.create_pool. linux-dmabuf-only compositors and every
// pixel format except XRGB8888/ARGB8888 are refused with
// errs.CodeNotSupported naming what was offered.
//
// Rotated outputs (wl_output transform other than normal) are refused as well:
// screencopy returns the frame in the output's buffer orientation, and Output
// carries no transform for a caller to undo, so returning those pixels would
// silently misplace everything in them.
package wayland

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Available reports whether this process looks like it can reach a Wayland
// compositor: $WAYLAND_DISPLAY names a socket that exists. It performs no
// roundtrip, so it is safe as a capability check; a compositor that lacks
// wlr-screencopy still answers true here and refuses at capture time.
//
// Abstract sockets (a name beginning with '@') cannot be inspected with stat,
// so they report false even though Dial can use them.
func Available() bool {
	path, err := displaySocket(os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return false
	}

	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

// displaySocket resolves a $WAYLAND_DISPLAY value to the unix socket address
// Wayland names. An absolute path is used as-is. A relative name lives in
// $XDG_RUNTIME_DIR, as the Wayland client library assumes. A name beginning
// with '@' is an abstract socket and is passed through for net to interpret.
func displaySocket(name string) (string, error) {
	if name == "" {
		return "", errs.New(
			errs.CodeNotSupported,
			"screen capture needs a Wayland session, but WAYLAND_DISPLAY is not set",
		)
	}

	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "@") {
		return name, nil
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", errs.New(
			errs.CodeNotSupported,
			"screen capture needs a Wayland session, but XDG_RUNTIME_DIR is not set",
		)
	}

	return filepath.Join(runtimeDir, name), nil
}
