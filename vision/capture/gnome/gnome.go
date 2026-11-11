//go:build linux

// Package gnome captures the screen through GNOME Shell's own D-Bus interface,
// org.gnome.Shell.Screenshot, on a GNOME session.
//
// Mutter implements no wlr-screencopy, so the wlroots backend refuses it, and
// the alternative in this library is the desktop portal, which may show a
// permission prompt for a picture the shell already takes for itself. This
// package asks the shell directly: Screenshot(include_cursor, flash, filename)
// renders the whole desktop, writes it as a PNG and reports whether it did, so a
// capture costs one D-Bus roundtrip, one PNG decode and no prompt.
//
// # What it calls
//
// The interface lives at /org/gnome/Shell/Screenshot on org.gnome.Shell. This
// package reads its introspection document at run time and uses only
// Screenshot(b include_cursor, b flash, s filename) -> (b success, s
// filename_used), which is the one method that captures the whole desktop. The
// shell writes the PNG to an absolute path this package hands it, inside a
// private temporary directory; the file is decoded and then deleted with that
// directory, and nothing about it is logged. The other methods the interface
// declares (ScreenshotWindow, ScreenshotArea, SelectArea, FlashArea, PickColor,
// InteractiveScreenshot) are not used: ScreenshotWindow captures the focused
// window rather than a region, and ScreenshotArea's documentation does not say
// whether its rectangle is logical or physical nor what scale its file comes back
// at, so a frame built from it would have to guess its own geometry. A region is
// therefore cropped locally out of the whole-desktop picture, where the geometry
// is at least known to be uniform.
//
// # Geometry, and what it costs
//
// The interface reports no geometry at all: no origin, no size, no scale. Mutter
// captures the whole stage at one scale (the largest of its monitors' scales) and
// encodes it as a PNG whose pixel size is the only evidence of that scale. This
// package derives the scale from the compositor's wl_output layout when it can be
// read, and reports the frame at scale 1 otherwise (see placeDesktop).
//
// That layout probe is best effort, and on the sessions this package exists for
// it usually fails: internal/wayland refuses a compositor that implements no
// wlr-screencopy, which is exactly Mutter. Without a layout, a 2x desktop
// therefore reports a frame twice as wide as the logical desktop, anchored at the
// origin, and a caller's logical region names only the top-left part of the
// picture. The pixels are correct either way, CaptureRect's crop is consistent
// with the frame it reports, and this is the same gap the portal path has on this
// desktop; a caller that needs exact logical geometry on a scaled GNOME desktop
// has to scale the region by the output scale itself.
//
// # Failures
//
// Recent GNOME releases restrict this interface to a small allowlist of senders
// (the shell's own screenshot tool, the desktop portal's backends and the
// media-keys daemon). A caller outside it is answered with
// org.freedesktop.DBus.Error.AccessDenied, and the shell also answers success
// false when it declines a screenshot for any other reason. Both are reported as
// errs.CodeAccessibilityDenied, and a caller should treat that as a reason to
// fall back to the portal rather than as a hard failure: the portal is on the
// allowlist and takes the same picture through the shell, at the cost of its
// permission prompt. A session without the interface reports
// errs.CodeNotSupported naming org.gnome.Shell.Screenshot; a shell that stops
// answering reports errs.CodeTimeout; a malformed result, a file that is not a
// PNG or a file the shell wrote somewhere this package did not name is
// errs.CodeInternal, and that last file is left alone rather than read or
// deleted.
//
// This package deliberately does not import vision/capture: the capture
// facade imports this package to select it, so importing back would be a
// cycle. What both sides exchange is pixels.Frame, the type both depend on.
package gnome

import (
	"context"
	"image"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// requestBudget bounds one capture: the bus connection, the interface probe, the
// method call and the PNG decode. GNOME Shell renders the whole desktop and
// encodes a PNG before it answers, which takes hundreds of milliseconds on a 4K
// desktop, so this is wider than a compositor-side grab needs; it only bounds a
// shell that stopped answering.
const requestBudget = 15 * time.Second

// probeBudget bounds Available, which capability checks and startup paths call
// and which must therefore never stall on a wedged bus.
const probeBudget = 2 * time.Second

// Available reports whether this session can take the GNOME path: a session bus
// is reachable and GNOME Shell exports its Screenshot interface with the
// whole-screen method this package calls.
//
// It performs real D-Bus roundtrips (one connection and one introspection
// document) and it never caches: the shell exports the interface when its
// screenshot service starts, which happens after the session bus is up, and a
// probe that ran too early must not disable the backend for the rest of the
// process.
//
// A bus or interface that does not answer within probeBudget reports false, so a
// wedged bus reads as "not available" rather than as a hang. A true answer says
// nothing about whether this process may use the interface: recent shells refuse
// callers outside their allowlist, which is reported by the capture functions as
// errs.CodeAccessibilityDenied.
func Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return false
	}
	defer conn.Close()

	return requireScreenshot(ctx, conn) == nil
}

// CaptureScreen captures the whole desktop and returns it as a pixels.Frame.
//
// The frame's pixels are the PNG GNOME Shell wrote, decoded; its Region is the
// desktop's logical bounds when the compositor's layout could be read, and the
// picture's own bounds at scale 1 otherwise (see the package comment). A refusal
// by the shell reports errs.CodeAccessibilityDenied, a session without the
// interface reports errs.CodeNotSupported, and a shell that stops answering
// reports errs.CodeTimeout.
func CaptureScreen(ctx context.Context) (pixels.Frame, error) {
	if err := checkContext(ctx); err != nil {
		return pixels.Frame{}, err
	}

	return grab(ctx)
}

// CaptureRect captures the whole desktop and crops it to region, in logical
// desktop coordinates.
//
// The interface has no area method whose coordinate space this package can rely
// on (see the package comment), so the whole picture is taken and the region is
// cut out of it. Region in the returned pixels.Frame is region intersected with the
// desktop the picture covers, and Origin is that intersection's top-left, so a
// region that is partly off the desktop yields the part that exists and says so.
// Scale is the frame's own, so a 2x desktop returns twice as many pixels as the
// region has logical columns.
//
// An empty region is refused with errs.CodeInvalidInput rather than widened to
// the whole screen, as the wlroots backend refuses it, because vision bounds its
// results by the region and an empty one would silently read as "the whole
// screen". A region entirely outside the desktop is refused the same way.
func CaptureRect(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	if err := checkContext(ctx); err != nil {
		return pixels.Frame{}, err
	}

	region = region.Canon()
	if region.Empty() {
		return pixels.Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region capture needs a region to capture; %v is empty",
			region,
		)
	}

	frame, err := grab(ctx)
	if err != nil {
		return pixels.Frame{}, err
	}

	return pixels.Crop(frame, region)
}

// withBudget applies requestBudget when the caller set no deadline, so a shell
// that never answers cannot hang a capture forever. A caller's own deadline is
// kept however short it is: the caller decides how long a screenshot is worth
// waiting for.
func withBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, requestBudget)
}

// checkContext reports an already-finished context in the shared vocabulary,
// before any bus is dialed.
func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return waitError(ctx)
	}

	return nil
}

// waitError names why a wait ended. A deadline is the capture's own budget
// running out, whether the caller set it or withBudget did; anything else is the
// caller canceling.
func waitError(ctx context.Context) error {
	if waitErr := ctx.Err(); waitErr != nil {
		if waitErr == context.DeadlineExceeded {
			return errs.Wrap(waitErr, errs.CodeTimeout, "GNOME Shell did not answer the screenshot in time")
		}

		return errs.Wrap(waitErr, errs.CodeContextCanceled, "the screenshot was canceled before GNOME Shell answered")
	}

	return nil
}
