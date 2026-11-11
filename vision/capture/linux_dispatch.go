//go:build linux

package capture

import (
	"context"
	"image"
	"os"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
	gnomecapture "github.com/LouYuanbo1/neru-core/vision/capture/gnome"
	kwincapture "github.com/LouYuanbo1/neru-core/vision/capture/kwin"
	portalcapture "github.com/LouYuanbo1/neru-core/vision/capture/portal"
	waylandcapture "github.com/LouYuanbo1/neru-core/vision/capture/wayland"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// This file chooses the session's capture transport.
//
// A Wayland session has no global X11 root that shows native Wayland clients,
// so its capture comes from the compositor, in this order:
//
//  1. wlr-screencopy, where it exists (sway, niri, Hyprland, wayfire);
//  2. KWin's ScreenShot2 D-Bus interface on KDE;
//  3. GNOME Shell's Screenshot interface on GNOME (usually refused to an
//     ordinary application on current shells, see below);
//  4. the desktop portal's Screenshot interface, which may prompt once.
//
// An X11 session reads the root window over the X11 wire. The choice is the
// session's, not a per-call fallback: reading an XWayland root on a Wayland
// session would hand back a frame missing every native Wayland window.
//
// The compositor-native interfaces can refuse an unprivileged caller (KWin
// 5.27's desktop-file authorization, GNOME's sender allowlist). That refusal is
// not terminal: the portal is allowlisted on both and returns the same picture,
// so a denial falls through to it. Only an error that no later path can fix
// (a protocol fault, a timeout, bad input) stops the chain.

// captureScreen returns the active screen.
func captureScreen(ctx context.Context) (pixels.Frame, error) {
	if !waylandcapture.Available() {
		return captureX11Screen(ctx)
	}

	if f, err := waylandcapture.CaptureScreen(ctx); err == nil {
		return f, nil
	} else if !nativeFallback(err) {
		return pixels.Frame{}, err
	}

	if desktopIs("kde") && kwincapture.Available() {
		if f, err := kwincapture.CaptureScreen(ctx); err == nil {
			return f, nil
		} else if !nativeFallback(err) {
			return pixels.Frame{}, err
		}
	}

	if desktopIs("gnome") && gnomecapture.Available() {
		if f, err := gnomecapture.CaptureScreen(ctx); err == nil {
			return f, nil
		} else if !nativeFallback(err) {
			return pixels.Frame{}, err
		}
	}

	return portalcapture.CaptureScreen(ctx)
}

// captureRect returns exactly region.
func captureRect(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	if !waylandcapture.Available() {
		return captureX11Rect(ctx, region)
	}

	if f, err := waylandcapture.CaptureRect(ctx, region); err == nil {
		return f, nil
	} else if !nativeFallback(err) {
		return pixels.Frame{}, err
	}

	if desktopIs("kde") && kwincapture.Available() {
		if f, err := kwincapture.CaptureRect(ctx, region); err == nil {
			return f, nil
		} else if !nativeFallback(err) {
			return pixels.Frame{}, err
		}
	}

	if desktopIs("gnome") && gnomecapture.Available() {
		if f, err := gnomecapture.CaptureRect(ctx, region); err == nil {
			return f, nil
		} else if !nativeFallback(err) {
			return pixels.Frame{}, err
		}
	}

	return portalcapture.CaptureRect(ctx, region)
}

// nativeFallback reports whether a compositor-native refusal is worth retrying
// through the portal.
//
// CodeNotSupported means the interface or method is absent, and
// CodeAccessibilityDenied means this process is not on its allowlist; the portal
// is available in both cases (it is the allowlisted broker). Any other code is a
// real failure and stops the chain.
func nativeFallback(err error) bool {
	return errs.IsNotSupported(err) || errs.IsCode(err, errs.CodeAccessibilityDenied)
}

// desktopIs reports whether XDG_CURRENT_DESKTOP names the given desktop family.
// An unset variable returns true so the probe still runs on a session that does
// not export it; the probe itself is cheap enough and reports CodeNotSupported
// when the service is absent.
func desktopIs(family string) bool {
	desktop := os.Getenv("XDG_CURRENT_DESKTOP")
	if desktop == "" {
		return true
	}

	return strings.Contains(strings.ToLower(desktop), family)
}
