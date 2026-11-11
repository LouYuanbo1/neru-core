//go:build darwin

package capture

import (
	"context"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// The macOS backend captures through ScreenCaptureKit first, which is
// noticeably faster than a process launch and can leave this process's own
// windows out of the picture (screencapture_sck_darwin.go). Anything that
// stops that path (a macOS older than 14, a missing permission, a timeout)
// falls back to the built-in `screencapture` utility below, so the library
// stays pure Go with no CGo and nothing beyond the purego dependency the module
// already has. The region coordinates are points (logical pixels); a Retina
// display returns a larger bitmap, so the shot's scale is derived from the
// pixels actually captured.
//
// Screen Recording permission is required by macOS for either path to read the
// screen; when it is missing macOS refuses with a message on stderr and the
// fallback fails here as CodeAccessibilityDenied.

// screenshotArgs builds the `screencapture` invocation. A non-empty region is
// turned into a -R x,y,w,h request; the zero region means the main display.
func screenshotArgs(region image.Rectangle, file string) []string {
	args := []string{"-x", "-t", "png"}
	if !region.Empty() {
		args = append(args, "-R", strings.Join([]string{
			strconv.Itoa(region.Min.X),
			strconv.Itoa(region.Min.Y),
			strconv.Itoa(region.Dx()),
			strconv.Itoa(region.Dy()),
		}, ","))
	}

	return append(args, file)
}

// runCapture shells out to `screencapture` and decodes its PNG into RGBA. The
// returned image always covers exactly what was asked for, its own bounds
// starting at (0,0).
func runCapture(ctx context.Context, region image.Rectangle) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	}

	dir, err := os.MkdirTemp("", "neru-core-capture-*")
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not create a capture scratch dir")
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "shot.png")

	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "screencapture", screenshotArgs(region, out)...)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// macOS names the missing permission on stderr; keep it for the caller.
		return nil, errs.Newf(
			errs.CodeAccessibilityDenied,
			"screencapture failed: %v: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}

	f, err := os.Open(out)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not open the captured image")
	}
	defer f.Close()

	src, err := png.Decode(f)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not decode the captured image")
	}

	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errs.New(errs.CodeInternal, "the captured image came back empty")
	}

	if rgba, ok := src.(*image.RGBA); ok {
		return rgba, nil
	}

	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)

	return rgba, nil
}

// captureScreen captures the main display. macOS puts the main display at the
// global origin (0,0), so the shot's Origin is that and its Region is the
// display's logical bounds. ScreenCaptureKit reports the display's backing
// scale, so a full-screen shot is measured in real frame pixels per logical
// pixel; the screencapture fallback cannot know the logical size and reports
// the captured bitmap as the region with scale 1.
func captureScreen(ctx context.Context) (pixels.Frame, error) {
	if shot, err := captureWithScreenCaptureKit(ctx, image.Rectangle{}); err == nil {
		return shot, nil
	}

	// The fast path is an optimization, not part of the contract: a failure
	// leaves the caller with what this backend has always returned. If the
	// fallback fails too, its error is the one reported, not the fast path's,
	// because only the fallback's error names something the caller can act on
	// (macOS puts the missing permission on stderr).
	img, err := runCapture(ctx, image.Rectangle{})
	if err != nil {
		return pixels.Frame{}, err
	}

	return pixels.Frame{
		Img:    img,
		Origin: image.Point{},
		Scale:  1,
		Region: img.Rect,
	}, nil
}

// captureRect captures exactly the given logical region. ScreenCaptureKit
// clips the region to the display that holds it, and the returned bitmap is the
// region at the display scale, so the shot's scale is derived from the ratio of
// captured to requested pixels; this is exact on both retina and non-retina
// displays. Origin is the region's top-left corner and Region the region
// itself. A failure falls back to the same screencapture call this backend has
// always used, whose error is the one reported.
func captureRect(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	if err := ctx.Err(); err != nil {
		return pixels.Frame{}, errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	}

	region = region.Canon()
	if region.Empty() {
		return pixels.Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region capture needs a region to capture; %v is empty",
			region,
		)
	}

	if shot, err := captureWithScreenCaptureKit(ctx, region); err == nil {
		return shot, nil
	}

	img, err := runCapture(ctx, region)
	if err != nil {
		return pixels.Frame{}, err
	}

	scale := float64(img.Rect.Dx()) / float64(region.Dx())
	if scale <= 0 {
		scale = 1
	}

	return pixels.Frame{
		Img:    img,
		Origin: region.Min,
		Scale:  scale,
		Region: region,
	}, nil
}
