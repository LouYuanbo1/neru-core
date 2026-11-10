// Package capture turns the platform's screen into the pixels vision reads.
// It is the frame-source half of the library: a platform backend holds one
// capture function and the pure-Go detectors in this module's parent package
// hold the other half, turning whatever pixels it returns into elements.
//
// A capture is described by a Shot: the RGBA pixels, where their top-left
// corner sits on the global desktop (logical pixels), how many frame pixels
// make one logical pixel (the monitor's DPI scale), and the logical desktop
// bounds the shot covers. From a Shot a caller builds the parent vision
// package's Frame directly:
//
//	frame := vision.Frame{
//		Img:    shot.Img,
//		Origin: shot.Origin,
//		Region: shot.Region,
//		Scale:  shot.Scale,
//	}
//
// This package answers with CodeNotSupported on platforms that do not implement
// capture; only the platforms that can read the screen provide a real shot.
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
)

// Shot is the platform-neutral answer to a capture: the pixels and where they
// sit on the desktop.
type Shot struct {
	// Img is the captured pixels in RGBA, rect always anchored at (0,0).
	Img *image.RGBA
	// Origin is where the shot's top-left corner sits on the desktop, in
	// logical (scaled) pixels.
	Origin image.Point
	// Scale is frame pixels per logical pixel, the monitor's DPI scale.
	Scale float64
	// Region is the part of the desktop, in logical pixels, the shot covers.
	Region image.Rectangle
}

// CaptureScreen captures the whole screen and returns it as a Shot. Region is
// the screen's logical bounds and Origin/Scale are its own, so a frame built
// from it covers exactly that screen. Where the platform distinguishes displays
// the active one (under the cursor) is used; on a Wayland session the
// compositor's whole output layout is captured.
func CaptureScreen(ctx context.Context) (Shot, error) { return captureScreen(ctx) }

// CaptureRect captures the given region of the desktop, in logical global
// pixels, and returns it as a Shot. Origin is the region's top-left corner, so
// a frame built from the shot sits exactly where the region asked for. An
// empty region is refused: vision bounds its results by the region, so an
// empty one would silently read as "the whole screen".
func CaptureRect(ctx context.Context, region image.Rectangle) (Shot, error) {
	return captureRect(ctx, region)
}
