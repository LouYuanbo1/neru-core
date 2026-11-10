//go:build linux

package kwin

import (
	"image"

	wl "github.com/LouYuanbo1/neru-core/internal/wayland"
)

// desktop reads the compositor's output layout over Wayland: the union of the
// rectangles its wl_outputs report, in logical pixels, and each rectangle on its
// own.
//
// KWin's metadata places the pixels on a scale but not on the desktop, so this
// is the only description of where its outputs sit that the library can reach
// without another KWin interface. It is read even though this session's pixels
// come from KWin's D-Bus interface.
//
// It is a best-effort read, and on the sessions this package exists for it
// usually fails. internal/wayland binds wl_shm and wlr-screencopy as part of
// discovery and refuses a compositor that implements no screencopy, which is
// exactly KWin; an X11 session has no Wayland socket at all. Either way the
// answer is layout{} with known false, and placeScreen falls back to the
// metadata scale and the image's own logical size. A missing layout therefore
// costs the origin, not the capture.
//
// The caller runs this alongside the D-Bus exchange (startDesktop), so the
// protocol roundtrips it costs are covered by a wait the capture was making
// anyway.
func desktop() layout {
	conn, err := wl.Dial()
	if err != nil {
		return layout{}
	}
	defer conn.Close()

	outputs, err := conn.Outputs()
	if err != nil {
		return layout{}
	}

	var bounds image.Rectangle

	var placed []image.Rectangle

	known := false

	for _, output := range outputs {
		rect := image.Rect(output.X, output.Y, output.X+output.Width, output.Y+output.Height)
		if rect.Empty() {
			continue
		}

		if known {
			bounds = bounds.Union(rect)
		} else {
			bounds = rect
			known = true
		}

		placed = append(placed, rect)
	}

	if !known {
		return layout{}
	}

	return layout{bounds: bounds, outputs: placed, known: true}
}

// startDesktop begins reading the layout on its own goroutine, so the capture's
// own wait covers the protocol roundtrips.
//
// The probe bounds itself: internal/wayland's discovery carries its own
// two-second deadline and Dial fails immediately on a session with no
// compositor socket. The channel is buffered, so a capture that fails before
// reading the result leaves one goroutine to finish and exit rather than one
// blocked on a send.
func startDesktop() <-chan layout {
	probed := make(chan layout, 1)

	go func() { probed <- desktop() }()

	return probed
}
