//go:build linux

// Package portal captures the screen through the XDG desktop portal's
// org.freedesktop.portal.Screenshot interface, over D-Bus.
//
// It is the capture path for a Wayland session whose compositor implements no
// wlr-screencopy. KWin and Mutter both expose the portal and neither
// implements zwlr_screencopy_v1, so the wlroots backend in
// vision/capture/wayland refuses them; this package asks the desktop's portal
// instead, and the portal answers with a file URI to a PNG that this package
// decodes. Everything is pure Go: godbus for the transport, image/png for the
// pixels. No PipeWire, no cgo.
//
// The handshake is the portal's usual one. Screenshot returns a Request object
// immediately, and the answer arrives later as that object's Response signal,
// which is why the subscription is installed before the call and every wait is
// bounded by the caller's context. The method's options ask for no interactive
// area picker: a picker would hand back a picture of some part of the screen
// with nothing saying which part, and the caller's region is cropped locally
// from the whole desktop instead.
//
// Two costs are inherent to this path and are documented rather than worked
// around:
//
//   - The portal may show a permission prompt the first time a session asks
//     for a screenshot. That is the price of capturing on a compositor with no
//     screencopy protocol, and a refusal is reported as
//     errs.CodeAccessibilityDenied so a caller can tell it from a broken
//     desktop. The wait defaults to 30 seconds (requestBudget) and reports
//     errs.CodeTimeout when it runs out.
//   - The portal reports a picture and no geometry at all: no origin, no size,
//     no scale. The logical desktop is therefore read from the Wayland
//     compositor's wl_output layout when it is readable, and the scale is the
//     ratio between the PNG's width and that layout's width. When no layout is
//     readable the frame is reported at scale 1 anchored at the origin, which
//     is exact for an unscaled desktop and an approximation otherwise (see
//     place and desktop).
//
// The file the portal writes is read and left in place: it belongs to the
// portal, and on a document-portal mount it is not this process's to remove.
// No pixel, path or file content is logged, and no frame outlives the call that
// asked for it.
//
// This package deliberately does not import vision/capture. The wiring in
// vision/capture/linux_dispatch.go converts a Frame into that package's Shot,
// and importing back would be a cycle.
package portal

import (
	"context"
	"image"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// requestBudget bounds everything one capture waits for: the session bus, the
// portal's answer and the file decode. A screenshot through the portal crosses
// a consent dialog, a desktop backend and a PNG encode, so the budget is
// seconds where a compositor-side grab needs milliseconds.
const requestBudget = 30 * time.Second

// probeBudget bounds Available, which capability checks and startup paths call
// and which must therefore never stall on a wedged bus.
const probeBudget = 2 * time.Second

// Available reports whether this session can take the portal path: a session
// bus is reachable and the portal exports the Screenshot interface.
//
// It performs real D-Bus roundtrips, so it costs one connection and one
// property read, and it never caches: reading the interface's version also
// activates the portal service on a desktop that starts it on demand, and a
// portal that was not running when a caller first probed may answer later.
//
// A bus or portal that does not answer within probeBudget reports false, so a
// wedged bus reads as "not available" rather than as a hang. It says nothing
// about the session type: the portal also exists on X11 sessions, where it is
// a working capture path in its own right.
func Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return false
	}
	defer conn.Close()

	return screenshotsSupported(ctx, conn) == nil
}

// CaptureScreen asks the portal for a screenshot of the whole desktop and
// returns it as a Frame whose Region is the desktop's logical bounds.
//
// The portal may show a permission prompt on first use; that is the documented
// price of this path. A refusal or cancellation reports
// errs.CodeAccessibilityDenied, a portal that never answers reports
// errs.CodeTimeout, and a session with no portal reports errs.CodeNotSupported
// naming the portal so a caller can fall back.
func CaptureScreen(ctx context.Context) (Frame, error) {
	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	return grab(ctx)
}

// CaptureRect captures the screen and crops it to region, in logical desktop
// coordinates.
//
// Region in the returned Frame is region intersected with the desktop the
// portal's pixels cover, and Origin is that intersection's top-left, so a
// region that is partly off the desktop yields the part that exists and says
// so. Scale is the frame's own, so a 2x desktop returns twice as many pixels as
// the region has logical columns.
//
// An empty region is refused with errs.CodeInvalidInput rather than widened to
// the whole screen, as the wlroots backend refuses it, because vision bounds
// its results by the region and an empty one would silently read as "the whole
// screen". A region entirely outside the desktop is refused the same way.
func CaptureRect(ctx context.Context, region image.Rectangle) (Frame, error) {
	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	region = region.Canon()
	if region.Empty() {
		return Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region capture needs a region to capture; %v is empty",
			region,
		)
	}

	frame, err := grab(ctx)
	if err != nil {
		return Frame{}, err
	}

	return crop(frame, region)
}

// withBudget applies requestBudget when the caller set no deadline, so a portal
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
			return errs.Wrap(waitErr, errs.CodeTimeout, "the screenshot portal did not answer in time")
		}

		return errs.Wrap(waitErr, errs.CodeContextCanceled, "the screenshot was canceled before the portal answered")
	}

	return nil
}
