package vision

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/vision/contour"
)

func TestTargetElements_ClipsToRegionAndDropsOutside(t *testing.T) {
	t.Parallel()

	f := Frame{
		Origin: image.Pt(1920, 0),
		Region: image.Rect(2000, 100, 2800, 700),
	}
	rects := []image.Rectangle{
		image.Rect(100, 150, 200, 190), // inside once offset
		image.Rect(10, 10, 50, 30),     // outside region
		image.Rect(40, 300, 120, 340),  // straddles the region's left edge
	}

	got := targetElements(f, rects, StrategyContour)
	if len(got) != 2 {
		t.Fatalf("len(targetElements) = %d, want 2", len(got))
	}

	want := image.Rect(2020, 150, 2120, 190)
	if got[0].Bounds() != want {
		t.Errorf("Bounds() = %v, want %v", got[0].Bounds(), want)
	}

	clipped := image.Rect(2000, 300, 2040, 340)
	if got[1].Bounds() != clipped {
		t.Errorf("straddling Bounds() = %v, want clipped to %v", got[1].Bounds(), clipped)
	}

	if !got[0].IsClickable() || !got[0].IsVisionOnly() {
		t.Error("element must be clickable and vision-only")
	}

	if got[0].Source() != element.SourceVision {
		t.Errorf("Source() = %q, want %q", got[0].Source(), element.SourceVision)
	}
}

func TestDetect_RejectsEmptyRegionAndCancellation(t *testing.T) {
	t.Parallel()

	f := Frame{Region: image.Rectangle{}}
	if _, err := Detect(context.Background(), f, StrategyContour, "", contour.Params{}); err == nil {
		t.Error("Detect(empty region) should error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f = Frame{Region: image.Rect(0, 0, 100, 100)}
	if _, err := Detect(ctx, f, StrategyContour, "", contour.Params{}); err == nil {
		t.Error("Detect(canceled) should error")
	}
}
