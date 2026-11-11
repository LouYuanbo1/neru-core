//go:build linux

// Package kwin captures the screen through KWin's own D-Bus interface,
// org.kde.KWin.ScreenShot2, on a KDE Plasma session.
//
// KWin implements no wlr-screencopy, so the wlroots backend refuses it, and the
// alternative in this library is the desktop portal, which may show a
// permission prompt and reports no geometry. ScreenShot2 has neither problem:
// KWin renders the frame itself, writes its pixels to a pipe this package
// drains, and answers with the image's width, height, row stride, QImage format
// and scale. A capture therefore costs one D-Bus roundtrip and one pipe
// transfer, with no dialog and no file on disk.
//
// # What it calls
//
// The interface lives at /org/kde/KWin/ScreenShot2, an object KWin registers
// under the dedicated org.kde.KWin.ScreenShot2 service name. KWin's own
// org.kde.KWin name reaches the same object, because both names belong to the
// compositor's one bus connection, so both are probed (see kwinServices). Which
// methods exist is read from the object's introspection document at run time
// rather than assumed, because the interface grew with KWin's releases:
//
//   - CaptureActiveScreen(a{sv} options, h pipe) captures the active output. It
//     is what CaptureScreen uses.
//   - CaptureWorkspace(a{sv} options, h pipe) captures every output united. It is
//     the fallback for an interface old enough to lack CaptureActiveScreen.
//   - CaptureArea(i x, i y, u width, u height, a{sv} options, h pipe) captures an
//     explicit logical rectangle. It is what CaptureRect uses.
//   - CaptureScreen(s name, a{sv} options, h pipe) is part of the interface but
//     is never called: it addresses an output by connector name in Plasma 5.27
//     and later, and by screen number in early 5.x releases, and nothing readable
//     by this package maps either to a rectangle. A KWin that offers only
//     CaptureScreen is refused with errs.CodeNotSupported rather than captured at
//     random.
//
// Every call returns an a{sv} result dictionary followed by the raw image on
// the pipe. The reply is sent before the pixels are written, so the pixels are
// read concurrently with a deadline after the reply arrives. The metadata keys
// are type ("raw"), width, height, stride, format (a QImage::Format) and scale;
// screen and windowId are informative. A reply that does not describe an image
// this package can convert is refused with errs.CodeNotSupported, naming the
// format, instead of being decoded on a guess.
//
// # Geometry
//
// KWin's metadata carries the image's scale but not the captured output's
// position on the desktop, so the origin and the frame's logical region come
// from two sources:
//
//   - an explicit region, for CaptureRect: the region that was asked for is the
//     frame's origin and its extent, narrowed if the image turns out to be
//     smaller;
//   - the compositor's wl_output layout, read through internal/wayland, for
//     CaptureScreen: the image's logical size is matched against the layout's
//     outputs and then the whole desktop. A match places the frame exactly; no
//     match anchors it at the layout's origin and reports only the part of the
//     desktop it covers.
//
// That layout probe is best effort. internal/wayland refuses a compositor that
// implements no wlr-screencopy, which is exactly KWin, so on a KWin Wayland
// session the probe usually fails; on a KWin X11 session there is no Wayland
// socket at all. Without a layout, CaptureScreen reports the frame at the
// origin with the image's own logical size, because KWin's own scale still makes
// the pixels correct and only the output's position is missing. That is exact
// for a single output at the origin and an approximation for a multi-output or
// scaled desktop; CaptureRect is unaffected because its region is explicit.
//
// # Failures
//
// A session with no KWin, or a KWin whose interface is absent, reports
// errs.CodeNotSupported naming org.kde.KWin.ScreenShot2, so a caller falls back
// to the portal. KWin's org.kde.KWin.ScreenShot2.Error.Cancelled reports
// errs.CodeAccessibilityDenied: on this interface it is KWin refusing to produce
// the frame, and a caller degrades the same way as for a refused prompt. So does
// org.kde.KWin.ScreenShot2.Error.NoAuthorized, which KWin 5.27-era releases
// answered for a caller whose desktop file does not declare
// X-KDE-DBUS-Restricted-Interfaces with the ScreenShot2 interface; the portal is
// the path that stays open there. A KWin that stops answering reports
// errs.CodeTimeout. Malformed metadata, a short pipe read or a rejected pipe
// descriptor is errs.CodeInternal. Nothing here logs pixels, image bytes or
// window contents, and no frame outlives the call that asked for it.
//
// This package deliberately does not import vision/capture: the capture
// facade imports this package to select it, so importing back would be a
// cycle. What both sides exchange is pixels.Frame, the type both depend on.
package kwin

import (
	"context"
	"image"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// requestBudget bounds one capture: the bus connection, the interface probe, the
// method call and the pipe transfer. KWin renders a 4K frame in tens of
// milliseconds, so this only bounds a wedged compositor.
const requestBudget = 10 * time.Second

// probeBudget bounds Available, which capability checks and startup paths call
// and which must therefore never stall on a wedged bus.
const probeBudget = 2 * time.Second

// Available reports whether this session can take the KWin path: a session bus
// is reachable and KWin exports its ScreenShot2 interface on one of the bus
// names this package knows.
//
// It performs real D-Bus roundtrips (one connection and one introspection
// document) and it never caches: KWin exports the interface when its screenshot
// plugin loads, which happens after the session bus is up, and a probe that ran
// too early must not disable the backend for the rest of the process.
//
// A bus or interface that does not answer within probeBudget reports false, so
// a wedged bus reads as "not available" rather than as a hang. It says nothing
// about the session type: KWin also runs, and exports the interface, on X11,
// where this package is a working capture path in its own right.
func Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return false
	}
	defer conn.Close()

	_, _, err = screenShotService(ctx, conn)

	return err == nil
}

// CaptureScreen captures the output KWin considers active and returns it as a
// Frame.
//
// On a Plasma 6 KWin that is CaptureActiveScreen's output, which is the one with
// the focused window. On an interface too old to export that method it is
// CaptureWorkspace's whole desktop. Scale comes from KWin's metadata; Origin and
// Region come from the compositor's layout when it can be read, and otherwise
// describe the image's own logical size at the origin (see the package comment).
//
// A session without KWin reports errs.CodeNotSupported, KWin refusing the frame
// reports errs.CodeAccessibilityDenied, and a KWin that stops answering reports
// errs.CodeTimeout.
func CaptureScreen(ctx context.Context) (pixels.Frame, error) {
	if err := checkContext(ctx); err != nil {
		return pixels.Frame{}, err
	}

	return grabScreen(ctx)
}

// CaptureRect captures exactly region, in logical desktop coordinates.
//
// The region is clipped to the compositor's layout when that layout can be read,
// so a rectangle that hangs off the desktop asks KWin only for the part that
// exists. Region in the returned pixels.Frame is the area the pixels actually cover,
// which is the clipped request narrowed again if KWin returned fewer pixels than
// it promised, and Origin is that area's top-left.
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

	return grabRegion(ctx, region)
}

// withBudget applies requestBudget when the caller set no deadline, so a KWin
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
// running out, whether the caller set it or withBudget did; anything else is
// the caller canceling.
func waitError(ctx context.Context) error {
	if waitErr := ctx.Err(); waitErr != nil {
		if waitErr == context.DeadlineExceeded {
			return errs.Wrap(waitErr, errs.CodeTimeout, "KWin did not answer the screenshot in time")
		}

		return errs.Wrap(waitErr, errs.CodeContextCanceled, "the screenshot was canceled before KWin answered")
	}

	return nil
}
