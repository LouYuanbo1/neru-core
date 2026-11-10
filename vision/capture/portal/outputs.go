//go:build linux

package portal

import (
	"image"

	wl "github.com/LouYuanbo1/neru-core/internal/wayland"
)

// desktop reads the compositor's output layout over Wayland: the union of the
// rectangles its wl_outputs report, in logical pixels.
//
// The portal reports no geometry at all, and this is the only description of
// the logical desktop the library can reach without a new protocol, so it is
// read even though this session's pixels come from the portal.
//
// It is a best-effort read, and on the sessions this package exists for it
// usually fails. internal/wayland binds wl_shm and wlr-screencopy as part of
// discovery and refuses a compositor that implements no screencopy, which is
// exactly KWin and Mutter; an X11 session has no Wayland socket at all. Either
// way the answer is layout{} with known false, and place() falls back to the
// PNG's own bounds. A missing layout therefore costs the scale, not the
// capture.
//
// The caller runs this alongside the portal exchange (startDesktop), so the
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

	placed := false

	for _, output := range outputs {
		rect := image.Rect(output.X, output.Y, output.X+output.Width, output.Y+output.Height)
		if rect.Empty() {
			continue
		}

		if placed {
			bounds = bounds.Union(rect)
		} else {
			bounds = rect
			placed = true
		}
	}

	if !placed {
		return layout{}
	}

	return layout{bounds: bounds, known: true}
}

// startDesktop begins reading the layout on its own goroutine, so the portal's
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
