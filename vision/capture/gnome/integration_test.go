//go:build integration && linux

package gnome

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestLiveCaptureScreen takes a real screenshot through GNOME Shell. It runs
// only under the integration build tag and only where the interface answers;
// everywhere else it skips.
//
// A refusal is skipped rather than failed: current GNOME releases only take a
// screenshot for an allowlisted caller, so a denial says something about the
// session's policy, not about this package. The test asserts that the pixels and
// the frame are self-consistent whenever a picture does arrive.
func TestLiveCaptureScreen(t *testing.T) {
	if !Available() {
		t.Skip("GNOME Shell's Screenshot interface is not available on this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frame, err := CaptureScreen(ctx)
	if err != nil {
		if errs.IsCode(err, errs.CodeAccessibilityDenied) {
			t.Skipf("GNOME Shell refused the screenshot: %v", err)
		}

		t.Fatalf("CaptureScreen: %v", err)
	}

	assertFrame(t, frame)
}

// TestLiveCaptureRect takes a real region capture and checks that the frame
// reports the region that was asked for, at a scale of at least one.
func TestLiveCaptureRect(t *testing.T) {
	if !Available() {
		t.Skip("GNOME Shell's Screenshot interface is not available on this session")
	}

	region := image.Rect(0, 0, 64, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frame, err := CaptureRect(ctx, region)
	if err != nil {
		if errs.IsCode(err, errs.CodeAccessibilityDenied) {
			t.Skipf("GNOME Shell refused the screenshot: %v", err)
		}

		t.Fatalf("CaptureRect: %v", err)
	}

	assertFrame(t, frame)

	if frame.Region != region {
		t.Errorf("region = %v, want %v", frame.Region, region)
	}
}

// assertFrame checks the invariants every captured frame must satisfy.
func assertFrame(t *testing.T, frame Frame) {
	t.Helper()

	if frame.Img == nil {
		t.Fatal("frame has no image")
	}

	if frame.Img.Rect.Empty() {
		t.Fatal("frame image is empty")
	}

	if frame.Scale < 1 {
		t.Errorf("scale = %v, want at least 1", frame.Scale)
	}

	if frame.Region.Empty() {
		t.Fatal("frame region is empty")
	}
}
