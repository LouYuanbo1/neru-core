//go:build darwin

package darwin

import "testing"

func TestZoomViewportRectangle_MagnifiesAroundTheViewportCenter(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Origin: cgPoint{X: 100, Y: 50}, Size: cgSize{Width: 1000, Height: 800}}
	origin := cgPoint{X: 600, Y: 450}

	got := zoomViewportRectangle(bounds, origin, 2)
	want := cgRect{
		Origin: cgPoint{X: origin.X - 250, Y: origin.Y - 200},
		Size:   cgSize{Width: 500, Height: 400},
	}

	if got != want {
		t.Fatalf("zoomViewportRectangle = %+v, want %+v", got, want)
	}
}

func TestZoomViewportRectangle_RejectsANonPositiveFactor(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Size: cgSize{Width: 1000, Height: 800}}

	for _, factor := range []float64{0, -2} {
		if got := zoomViewportRectangle(bounds, cgPoint{X: 500, Y: 400}, factor); got != (cgRect{}) {
			t.Errorf("factor %v: zoomViewportRectangle = %+v, want the zero rectangle", factor, got)
		}
	}
}

func TestZoomPanTarget_LeavesAVisibleTargetAlone(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Size: cgSize{Width: 1000, Height: 800}}
	origin := cgPoint{X: 500, Y: 400}

	// At factor 2 the viewport is 500x400 with an 8 point margin, so a target
	// 100 points from the center is comfortably inside it.
	target := cgPoint{X: origin.X + 100, Y: origin.Y + 100}

	wanted, pan := zoomPanTarget(bounds, origin, 2, target)
	if pan {
		t.Fatalf("a visible target must not pan the viewport, got %+v", wanted)
	}

	if wanted != origin {
		t.Errorf("zoomPanTarget = %+v, want the unchanged origin %+v", wanted, origin)
	}
}

func TestZoomPanTarget_PansByTheSmallestAmount(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Size: cgSize{Width: 1000, Height: 800}}
	origin := cgPoint{X: 500, Y: 400}

	tests := []struct {
		name   string
		target cgPoint
		want   cgPoint
	}{
		{
			// The right edge of the viewport sits at 500+242=742, so the pan
			// moves the viewport 8 points right of the target.
			name:   "to the right",
			target: cgPoint{X: 750, Y: 400},
			want:   cgPoint{X: 508, Y: 400},
		},
		{
			name:   "to the left",
			target: cgPoint{X: 250, Y: 400},
			want:   cgPoint{X: 492, Y: 400},
		},
		{
			// The viewport cannot leave its display, so the requested center is
			// clamped to the last position that keeps it inside.
			name:   "past the right edge of the display",
			target: cgPoint{X: 999, Y: 400},
			want:   cgPoint{X: 750, Y: 400},
		},
		{
			name:   "past the left edge of the display",
			target: cgPoint{X: -500, Y: 400},
			want:   cgPoint{X: 250, Y: 400},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			wanted, pan := zoomPanTarget(bounds, origin, 2, testCase.target)
			if !pan {
				t.Fatalf("target %+v must pan the viewport", testCase.target)
			}

			if wanted != testCase.want {
				t.Errorf("zoomPanTarget = %+v, want %+v", wanted, testCase.want)
			}
		})
	}
}

func TestZoomPanTarget_StopsRepinningAtTheEdge(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Size: cgSize{Width: 1000, Height: 800}}

	first, pan := zoomPanTarget(bounds, cgPoint{X: 500, Y: 400}, 2, cgPoint{X: -500, Y: 400})
	if !pan || first != (cgPoint{X: 250, Y: 400}) {
		t.Fatalf("first pan = %+v, %v; want the left edge", first, pan)
	}

	// A second move toward the same unreachable target must not pan again; the
	// viewport is already as far as it can go.
	if again, panAgain := zoomPanTarget(bounds, first, 2, cgPoint{X: -500, Y: 400}); panAgain {
		t.Errorf("the viewport was pinned again to %+v", again)
	}
}

func TestZoomPanTarget_IgnoresAnUnmagnifiedDisplay(t *testing.T) {
	t.Parallel()

	bounds := cgRect{Size: cgSize{Width: 1000, Height: 800}}
	origin := cgPoint{X: 500, Y: 400}

	for _, factor := range []float64{1, 0, -2} {
		if wanted, pan := zoomPanTarget(bounds, origin, factor, cgPoint{X: 0, Y: 0}); pan {
			t.Errorf("factor %v: pan = true (%+v), want no pan", factor, wanted)
		}
	}
}
