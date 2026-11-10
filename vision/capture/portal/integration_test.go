//go:build integration && linux

package portal

import (
	"context"
	"image"
	"testing"
	"time"
)

// liveBudget is how long a live capture may take. A portal that has to show its
// consent dialog for the first time in a session takes seconds, not
// milliseconds, so the test budget is generous.
const liveBudget = 2 * time.Minute

func TestCaptureScreen_LivePortal(t *testing.T) {
	if !Available() {
		t.Skip("no screenshot portal on this session's D-Bus")
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveBudget)
	defer cancel()

	frame, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	if frame.Img == nil || frame.Img.Rect.Empty() {
		t.Fatal("CaptureScreen returned an empty image")
	}

	if frame.Img.Rect.Min != (image.Point{}) {
		t.Errorf("image rect starts at %v, want the origin", frame.Img.Rect.Min)
	}

	if frame.Scale < 1 {
		t.Errorf("scale = %v, want at least 1", frame.Scale)
	}

	if frame.Region.Empty() {
		t.Fatal("CaptureScreen reported no region")
	}

	// The pixels and the logical region have to describe the same area, or a
	// caller's frame would be misplaced by the scale factor.
	columns := int(float64(frame.Img.Rect.Dx())/frame.Scale + 0.5)
	if columns != frame.Region.Dx() {
		t.Errorf("image is %d pixels wide at scale %v, but the region is %d logical columns",
			frame.Img.Rect.Dx(), frame.Scale, frame.Region.Dx())
	}
}

func TestCaptureRect_LivePortal(t *testing.T) {
	if !Available() {
		t.Skip("no screenshot portal on this session's D-Bus")
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveBudget)
	defer cancel()

	screen, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	region := image.Rect(
		screen.Region.Min.X,
		screen.Region.Min.Y,
		screen.Region.Min.X+32,
		screen.Region.Min.Y+32,
	)
	if !region.In(screen.Region) {
		t.Skip("the captured desktop is smaller than the 32-pixel test region")
	}

	frame, err := CaptureRect(ctx, region)
	if err != nil {
		t.Fatalf("CaptureRect: %v", err)
	}

	if frame.Region != region {
		t.Errorf("region = %v, want the requested %v", frame.Region, region)
	}

	if frame.Origin != region.Min {
		t.Errorf("origin = %v, want %v", frame.Origin, region.Min)
	}

	want := int(32*frame.Scale + 0.5)
	if frame.Img.Rect.Dx() != want {
		t.Errorf("cropped image is %d pixels wide, want %d at scale %v",
			frame.Img.Rect.Dx(), want, frame.Scale)
	}
}
