//go:build linux

// Package wayland captures the Wayland desktop through
// zwlr_screencopy_manager_v1, the protocol wlroots compositors implement.
//
// It is the frame source the capture package wires in beside its X11 backend:
// Available reports whether this session looks like Wayland, and the two
// capture functions return a pixels.Frame with the pixels, their origin on the
// desktop and the scale that maps logical to frame pixels. When the session is
// not Wayland, or the compositor does not implement screencopy, every call fails
// with errs.CodeNotSupported so a caller can fall back instead of treating it
// as a broken desktop.
//
// The pixels come from the compositor's own framebuffer; nothing here reads
// another client's window, and nothing logs or retains a frame past the call.
package wayland

import (
	"context"
	"image"
	"math"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
	wl "github.com/LouYuanbo1/neru-core/internal/wayland"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// captureTimeout bounds one capture exchange. A local compositor answers a 4K
// grab in tens of milliseconds; this is only a bound for a wedged one. Output
// discovery performed by the lower layer carries the same bound.
const captureTimeout = 2 * time.Second

// Available reports whether this process looks like it is on a Wayland session:
// $WAYLAND_DISPLAY names a socket that exists. It performs no protocol
// roundtrip, so a compositor without wlr-screencopy still answers true here and
// refuses the capture itself.
func Available() bool {
	return wl.Available()
}

// CaptureScreen captures one output at its native pixel size. Wayland has no
// primary output, so this uses the first output the compositor advertises
// (ordered by wl_registry name, which is stable for the session). Origin and
// Region are that output's logical layout rectangle.
func CaptureScreen(ctx context.Context) (pixels.Frame, error) {
	if err := checkContext(ctx); err != nil {
		return pixels.Frame{}, err
	}

	if !Available() {
		return pixels.Frame{}, notAvailable()
	}

	captureCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	conn, err := wl.Dial()
	if err != nil {
		return pixels.Frame{}, err
	}
	defer conn.Close()

	outputs, err := conn.Outputs()
	if err != nil {
		return pixels.Frame{}, err
	}

	if len(outputs) == 0 {
		return pixels.Frame{}, errs.New(
			errs.CodeNotSupported,
			"the Wayland compositor reports no outputs to capture",
		)
	}

	output := outputs[0]

	img, origin, scale, err := conn.CaptureOutput(captureCtx, output)
	if err != nil {
		return pixels.Frame{}, err
	}

	return pixels.Frame{
		Img:    img,
		Origin: origin,
		Scale:  scale,
		Region: image.Rect(origin.X, origin.Y, origin.X+output.Width, origin.Y+output.Height),
	}, nil
}

// CaptureRect captures the given logical desktop region, clipped to the output
// that contains the region's centre. Origin is the clipped region's logical
// top-left and Region is the area actually returned, so a caller can place the
// pixels exactly. An empty region is refused with CodeInvalidInput rather than
// widened to the whole screen.
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

	if !Available() {
		return pixels.Frame{}, notAvailable()
	}

	captureCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	conn, err := wl.Dial()
	if err != nil {
		return pixels.Frame{}, err
	}
	defer conn.Close()

	img, origin, scale, err := conn.CaptureRegion(captureCtx, region)
	if err != nil {
		return pixels.Frame{}, err
	}

	if scale <= 0 {
		return pixels.Frame{}, errs.New(
			errs.CodeInternal,
			"the Wayland capture reported a non-positive scale",
		)
	}

	logicalWidth := int(math.Round(float64(img.Rect.Dx()) / scale))
	logicalHeight := int(math.Round(float64(img.Rect.Dy()) / scale))

	return pixels.Frame{
		Img:    img,
		Origin: origin,
		Scale:  scale,
		Region: image.Rect(
			origin.X,
			origin.Y,
			origin.X+logicalWidth,
			origin.Y+logicalHeight,
		),
	}, nil
}

// notAvailable is the CodeNotSupported answer for a session with no Wayland
// compositor socket.
func notAvailable() error {
	return errs.New(
		errs.CodeNotSupported,
		"screen capture needs a Wayland session, but no compositor socket was found",
	)
}

// checkContext reports an already-finished context before any socket is opened.
func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return errs.Wrap(err, errs.CodeTimeout, "screen capture timed out before it started")
		}

		return errs.Wrap(err, errs.CodeContextCanceled, "screen capture canceled")
	}

	return nil
}
