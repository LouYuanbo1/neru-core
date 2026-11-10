//go:build integration && darwin

package capture

import (
	"context"
	"image"
	"math"
	"testing"
	"time"
)

// This file holds the live tests: they read the real screen of the machine
// running them, so they are behind the integration tag and need Screen
// Recording permission granted to the test binary.
//
// Note on own-window exclusion: the test binary has no visible window of its
// own, and showing one needs an application, an activated window and a running
// run loop, which a `go test` process does not have. The exclusion is therefore
// checked by hand: with Neru's overlay on screen, compare the captured pixels
// under it with and without the overlay. What is asserted here automatically is
// that a capture succeeds, covers the display it names, comes back at the
// display's backing scale, and clips a region the way the Shot contract says.

// wholeShot captures the main display and fails the test when the fast path is
// unavailable, so the region test below can be sure it is measuring the same
// path a caller gets.
func wholeShot(t *testing.T) Shot {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	shot, err := captureWithScreenCaptureKit(ctx, image.Rectangle{})
	if err != nil {
		t.Fatalf("captureWithScreenCaptureKit: %v (is Screen Recording granted to this binary?)", err)
	}

	return shot
}

func TestScreenCaptureKit_WholeDisplay(t *testing.T) {
	shot := wholeShot(t)

	if shot.Img == nil {
		t.Fatal("the shot has no image")
	}

	bounds := shot.Img.Bounds()
	if bounds.Min != (image.Point{}) {
		t.Errorf("image origin = %v, want (0,0)", bounds.Min)
	}

	if bounds.Dx() < 100 || bounds.Dy() < 100 {
		t.Fatalf("image = %v, smaller than any real display", bounds)
	}

	if shot.Region.Empty() {
		t.Fatal("the shot covers no desktop region")
	}

	if shot.Origin != shot.Region.Min {
		t.Errorf("origin = %v, want the region's corner %v", shot.Origin, shot.Region.Min)
	}

	// The main display is the window-server origin.
	if shot.Origin != (image.Point{}) {
		t.Errorf("origin = %v, want the main display's origin (0,0)", shot.Origin)
	}

	if shot.Scale < 1 {
		t.Errorf("scale = %v, want at least one frame pixel per point", shot.Scale)
	}

	wantScale := float64(bounds.Dx()) / float64(shot.Region.Dx())
	if math.Abs(shot.Scale-wantScale) > 0.01 {
		t.Errorf("scale = %v, want %v (image width over region width)", shot.Scale, wantScale)
	}

	// A screenshot of a real desktop is opaque. Sampling a grid keeps a huge
	// Retina image cheap to check.
	opaque := 0
	for y := bounds.Min.Y + 1; y < bounds.Max.Y; y += max(1, bounds.Dy()/8) {
		for x := bounds.Min.X + 1; x < bounds.Max.X; x += max(1, bounds.Dx()/8) {
			if _, _, _, alpha := shot.Img.At(x, y).RGBA(); alpha == 0xffff {
				opaque++
			}
		}
	}

	if opaque == 0 {
		t.Error("every sampled pixel is transparent; the image is not a screen capture")
	}

	t.Logf("captured %v at scale %v covering %v", bounds, shot.Scale, shot.Region)
}

func TestScreenCaptureKit_Region(t *testing.T) {
	whole := wholeShot(t)

	// A region fully inside the display, so nothing can be clipped.
	region := image.Rect(
		whole.Region.Min.X+whole.Region.Dx()/4,
		whole.Region.Min.Y+whole.Region.Dy()/4,
		whole.Region.Min.X+whole.Region.Dx()/4+200,
		whole.Region.Min.Y+whole.Region.Dy()/4+120,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	shot, err := captureWithScreenCaptureKit(ctx, region)
	if err != nil {
		t.Fatalf("captureWithScreenCaptureKit(%v): %v", region, err)
	}

	if shot.Region != region {
		t.Errorf("region = %v, want %v", shot.Region, region)
	}

	if shot.Origin != region.Min {
		t.Errorf("origin = %v, want %v", shot.Origin, region.Min)
	}

	if shot.Img == nil || shot.Img.Bounds().Empty() {
		t.Fatal("the region shot has no image")
	}

	wantWidth := int(math.Round(float64(region.Dx()) * shot.Scale))
	wantHeight := int(math.Round(float64(region.Dy()) * shot.Scale))

	if got := shot.Img.Bounds().Dx(); got != wantWidth {
		t.Errorf("image width = %d, want %d for %d points at scale %v",
			got, wantWidth, region.Dx(), shot.Scale)
	}

	if got := shot.Img.Bounds().Dy(); got != wantHeight {
		t.Errorf("image height = %d, want %d for %d points at scale %v",
			got, wantHeight, region.Dy(), shot.Scale)
	}

	if math.Abs(shot.Scale-whole.Scale) > 0.01 {
		t.Errorf("region scale = %v, want the display's %v", shot.Scale, whole.Scale)
	}
}

// TestCaptureRect_ClipsToTheDisplay pins the documented behavior for a region
// that runs off the edge of the desktop: the shot covers what the display can
// show and says so through Region.
func TestCaptureRect_ClipsToTheDisplay(t *testing.T) {
	whole := wholeShot(t)

	region := image.Rect(
		whole.Region.Max.X-80,
		whole.Region.Max.Y-60,
		whole.Region.Max.X+80,
		whole.Region.Max.Y+60,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	shot, err := captureWithScreenCaptureKit(ctx, region)
	if err != nil {
		t.Fatalf("captureWithScreenCaptureKit(%v): %v", region, err)
	}

	if shot.Region != whole.Region.Intersect(region) {
		t.Errorf("region = %v, want the clipped %v", shot.Region, whole.Region.Intersect(region))
	}

	if shot.Img.Bounds().Dx() < 1 || shot.Img.Bounds().Dy() < 1 {
		t.Errorf("image = %v, empty", shot.Img.Bounds())
	}
}

// TestCaptureScreen_PublicContract goes through the exported API, which is the
// path a caller takes: ScreenCaptureKit first, screencapture when it fails.
// Only what both paths promise is asserted.
func TestCaptureScreen_PublicContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	shot, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	if shot.Img == nil || shot.Img.Bounds().Empty() {
		t.Fatal("CaptureScreen returned no image")
	}

	if shot.Img.Bounds().Min != (image.Point{}) {
		t.Errorf("image origin = %v, want (0,0)", shot.Img.Bounds().Min)
	}

	if shot.Scale <= 0 {
		t.Errorf("scale = %v, want a positive scale", shot.Scale)
	}

	if shot.Region.Empty() {
		t.Error("CaptureScreen returned an empty region")
	}

	rect := image.Rect(shot.Region.Min.X+10, shot.Region.Min.Y+10, shot.Region.Min.X+110, shot.Region.Min.Y+60)

	regionShot, err := CaptureRect(ctx, rect)
	if err != nil {
		t.Fatalf("CaptureRect(%v): %v", rect, err)
	}

	if regionShot.Region.Empty() || regionShot.Img == nil {
		t.Fatal("CaptureRect returned an empty shot")
	}

	if regionShot.Origin != regionShot.Region.Min {
		t.Errorf("CaptureRect origin = %v, want %v", regionShot.Origin, regionShot.Region.Min)
	}

	if got := regionShot.Img.Bounds().Dx(); got < 1 {
		t.Errorf("CaptureRect image width = %d", got)
	}
}
