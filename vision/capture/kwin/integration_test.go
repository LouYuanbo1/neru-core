//go:build integration && linux

package kwin

import (
	"context"
	"image"
	"testing"
	"time"
)

// TestLiveCaptureScreen takes a real screenshot through KWin. It runs only under
// the integration build tag and only on a KDE session whose interface answers;
// everywhere else it skips, because a machine without KWin is not a failure of
// this package.
func TestLiveCaptureScreen(t *testing.T) {
	if !Available() {
		t.Skip("KWin's ScreenShot2 interface is not available on this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	frame, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	assertFrame(t, frame)
}

// TestLiveCaptureRect takes a real region capture and checks that the frame
// reports the region that was asked for, at a scale of at least one.
func TestLiveCaptureRect(t *testing.T) {
	if !Available() {
		t.Skip("KWin's ScreenShot2 interface is not available on this session")
	}

	region := image.Rect(0, 0, 64, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	frame, err := CaptureRect(ctx, region)
	if err != nil {
		t.Fatalf("CaptureRect: %v", err)
	}

	assertFrame(t, frame)

	if frame.Region != region {
		t.Errorf("region = %v, want %v", frame.Region, region)
	}

	if frame.Img.Rect.Dx() < region.Dx() {
		t.Errorf("image is %d pixels wide for a %d pixel region at scale %v",
			frame.Img.Rect.Dx(), region.Dx(), frame.Scale)
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
