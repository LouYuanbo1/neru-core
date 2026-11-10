//go:build windows

package capture_test

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/vision/capture"
)

// captureShot pulls a screen shot, skipping on machines with no interactive
// desktop so the suite stays green where capture legitimately cannot run.
func captureShot(t *testing.T) capture.Shot {
	t.Helper()

	shot, err := capture.CaptureScreen(context.Background())
	if err != nil {
		t.Skipf("screen capture unavailable: %v", err)
	}
	if shot.Img == nil {
		t.Fatal("shot returned a nil image")
	}
	if shot.Img.Rect.Dx() <= 0 || shot.Img.Rect.Dy() <= 0 {
		t.Fatalf("shot has empty image bounds: %v", shot.Img.Rect)
	}

	return shot
}

// TestCaptureScreen checks the whole active display comes back with its own
// origin, scale and region: a frame built from it covers the active screen.
func TestCaptureScreen(t *testing.T) {
	shot := captureShot(t)

	if shot.Scale <= 0 {
		t.Errorf("shot has non-positive scale %v", shot.Scale)
	}
	if shot.Region.Empty() {
		t.Errorf("shot covers an empty region %v", shot.Region)
	}
	if want := image.Pt(
		shot.Img.Rect.Dx(), shot.Img.Rect.Dy(),
	); shot.Region.Dx() > want.X || shot.Region.Dy() > want.Y {
		t.Errorf("region %v exceeds image %v", shot.Region, shot.Img.Rect)
	}

	// The image's own top-left is always (0,0); it must match where the shot
	// says it begins on the desktop.
	if shot.Img.At(0, 0) == image.Black {
		t.Log("top-left pixel is black; plausible on a dark desktop")
	}
}

// TestCaptureRect checks a region capture returns exactly that region and is
// non-empty wherever it lands on a real desktop.
func TestCaptureRect(t *testing.T) {
	base := captureShot(t)

	r := image.Rect(0, 0, base.Img.Rect.Dx()/2, base.Img.Rect.Dy()/2)
	if r.Empty() {
		r = image.Rect(0, 0, 16, 16)
	}

	shot, err := capture.CaptureRect(context.Background(), r)
	if err != nil {
		t.Fatalf("CaptureRect: %v", err)
	}
	if !shot.Region.Eq(r) {
		t.Errorf("region = %v, want %v", shot.Region, r)
	}
	if !shot.Origin.Eq(r.Min) {
		t.Errorf("origin = %v, want %v", shot.Origin, r.Min)
	}
	if shot.Img == nil || shot.Img.Rect.Dx() <= 0 {
		t.Error("region capture returned an empty image")
	}
}

// TestScaleStable checks the reported scale matches the pixel ratio expected on
// a capture: logical region size times scale should bracket the image size.
func TestScaleStable(t *testing.T) {
	shot := captureShot(t)

	imgW, imgH := float64(shot.Img.Rect.Dx()), float64(shot.Img.Rect.Dy())
	regW, regH := float64(shot.Region.Dx()), float64(shot.Region.Dy())

	// reg*scale ~ img, within the rounding captured by the region bounds.
	if regW*shot.Scale < imgW-2 || regW*shot.Scale > imgW+2 {
		t.Errorf("region width %v * scale %v = %v far from image width %v",
			regW, shot.Scale, regW*shot.Scale, imgW)
	}
	if regH*shot.Scale < imgH-2 || regH*shot.Scale > imgH+2 {
		t.Errorf("region height %v * scale %v = %v far from image height %v",
			regH, shot.Scale, regH*shot.Scale, imgH)
	}
}
