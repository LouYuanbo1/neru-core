//go:build darwin

package darwin

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/action"
)

func TestSmoothPoints_EndsExactlyOnTheTarget(t *testing.T) {
	t.Parallel()

	start := image.Pt(10, 20)
	end := image.Pt(210, 120)

	for _, steps := range []int{1, 2, 5, 16} {
		points := smoothPoints(start, end, steps)

		if len(points) != steps {
			t.Fatalf("steps %d: got %d points", steps, len(points))
		}

		if points[len(points)-1] != end {
			t.Errorf("steps %d: last point = %v, want the target %v", steps, points[len(points)-1], end)
		}

		// Every point lies between the start and the end, and progress never
		// goes backwards.
		previous := start
		for index, point := range points {
			if point.X < start.X || point.X > end.X || point.Y < start.Y || point.Y > end.Y {
				t.Fatalf("steps %d: point %d = %v is outside the segment", steps, index, point)
			}

			if point.X < previous.X || point.Y < previous.Y {
				t.Fatalf("steps %d: point %d = %v went backwards from %v", steps, index, point, previous)
			}

			previous = point
		}
	}
}

func TestSmoothPoints_PostsOnePointWithoutSteps(t *testing.T) {
	t.Parallel()

	end := image.Pt(40, 50)

	points := smoothPoints(image.Pt(0, 0), end, 0)
	if len(points) != 1 || points[0] != end {
		t.Fatalf("smoothPoints with no steps = %v, want just the target", points)
	}

	points = smoothPoints(image.Pt(0, 0), end, -3)
	if len(points) != 1 || points[0] != end {
		t.Fatalf("smoothPoints with negative steps = %v, want just the target", points)
	}
}

func TestSmoothPoints_AdvancesByOneStepPerEvent(t *testing.T) {
	t.Parallel()

	points := smoothPoints(image.Pt(0, 0), image.Pt(100, 0), 4)

	want := []image.Point{
		image.Pt(25, 0),
		image.Pt(50, 0),
		image.Pt(75, 0),
		image.Pt(100, 0),
	}

	for index, point := range points {
		if point != want[index] {
			t.Fatalf("point %d = %v, want %v", index, point, want[index])
		}
	}
}

func TestDraggedEventKind_MapsEveryButton(t *testing.T) {
	t.Parallel()

	tests := []struct {
		button   action.MouseButton
		wantKind uint32
		wantCG   uint32
	}{
		{button: action.ButtonLeft, wantKind: cgEventLeftMouseDragged, wantCG: cgMouseButtonLeft},
		{button: action.ButtonRight, wantKind: cgEventRightMouseDragged, wantCG: cgMouseButtonRight},
		{button: action.ButtonMiddle, wantKind: cgEventOtherMouseDragged, wantCG: cgMouseButtonCenter},
	}

	for _, testCase := range tests {
		kind, cgButton, ok := draggedEventKind(testCase.button)
		if !ok || kind != testCase.wantKind || cgButton != testCase.wantCG {
			t.Errorf("draggedEventKind(%v) = %d, %d, %v; want %d, %d",
				testCase.button, kind, cgButton, ok, testCase.wantKind, testCase.wantCG)
		}
	}

	if _, _, ok := draggedEventKind(action.MouseButton(99)); ok {
		t.Error("an unknown button must not map to a drag event")
	}
}

func TestDragEvent_PrefersTheLeftButtonAndFallsBackToAMove(t *testing.T) {
	t.Parallel()

	client := &Client{held: make(map[action.MouseButton]bool)}

	if kind, button := client.dragEvent(); kind != cgEventMouseMoved || button != cgMouseButtonLeft {
		t.Errorf("dragEvent with nothing held = %d, %d; want a plain move", kind, button)
	}

	client.setHeld(action.ButtonRight, true)

	if kind, _ := client.dragEvent(); kind != cgEventRightMouseDragged {
		t.Errorf("dragEvent with the right button held = %d, want the right drag", kind)
	}

	client.setHeld(action.ButtonLeft, true)

	if kind, _ := client.dragEvent(); kind != cgEventLeftMouseDragged {
		t.Errorf("dragEvent with both buttons held = %d, want the left drag", kind)
	}

	client.setHeld(action.ButtonLeft, false)
	client.setHeld(action.ButtonRight, false)

	if kind, _ := client.dragEvent(); kind != cgEventMouseMoved {
		t.Errorf("dragEvent after both releases = %d, want a plain move", kind)
	}
}
