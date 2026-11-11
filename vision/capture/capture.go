// Package capture turns the platform's screen into the pixels vision reads.
// It is the frame-source half of the library: a platform backend holds one
// capture function and the pure-Go detectors in the vision package hold the
// other half, turning whatever pixels it returns into elements.
//
// A capture is a pixels.Frame: the RGBA pixels, where their top-left corner
// sits on the global desktop (logical pixels), how many frame pixels make one
// logical pixel (the monitor's DPI scale), and the logical desktop bounds the
// frame covers. The frame a capture returns goes straight into vision, so no
// conversion step sits between the two packages:
//
//	f, err := capture.CaptureScreen(ctx)
//	if err != nil {
//		return err
//	}
//	elements, err := vision.DetectContours(ctx, f, contour.DefaultParams())
//
// This package answers with CodeNotSupported on platforms that do not implement
// capture; only the platforms that can read the screen provide a real frame.
//
// The transport is the session's: GDI on Windows, the X11 root window on X11,
// wlr-screencopy on a wlroots Wayland compositor, the compositor's own D-Bus
// screenshot API on KDE or GNOME, and the desktop portal's Screenshot interface
// as a last Wayland resort (which may prompt once). On macOS it is
// ScreenCaptureKit on macOS 14 and newer, falling back to the `screencapture`
// utility. The fast paths exclude the caller's own windows: macOS does so
// implicitly through ScreenCaptureKit's content filter, and Windows offers
// ExcludeOwnWindows (WDA_EXCLUDEFROMCAPTURE). The fallback paths and the other
// platforms include them; ExcludeOwnWindows reports CodeNotSupported there.
package capture

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// CaptureScreen captures the whole screen and returns it as a pixels.Frame. Region is
// the screen's logical bounds and Origin/Scale are its own, so the frame covers
// exactly that screen. Where the platform distinguishes displays the active one
// (under the cursor) is used; on a Wayland session the compositor's whole output
// layout is captured.
func CaptureScreen(ctx context.Context) (pixels.Frame, error) { return captureScreen(ctx) }

// CaptureRect captures the given region of the desktop, in logical global
// pixels, and returns it as a pixels.Frame. Origin is the region's top-left corner, so
// the frame sits exactly where the region asked for. An empty region is refused:
// vision bounds its results by the region, so an empty one would silently read
// as "the whole screen".
func CaptureRect(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	return captureRect(ctx, region)
}
