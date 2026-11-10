//go:build integration && linux

package wayland

import (
	"context"
	"image"
	"testing"
	"time"
)

// liveContext skips when the test host has no Wayland compositor and otherwise
// bounds the test's exchange.
func liveContext(t *testing.T) context.Context {
	t.Helper()

	if !Available() {
		t.Skip("no Wayland compositor socket in this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func TestDialAndOutputs_LiveCompositor(t *testing.T) {
	liveContext(t)

	conn, err := Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	outputs, err := conn.Outputs()
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}

	if len(outputs) == 0 {
		t.Fatal("Outputs returned no outputs on a live compositor")
	}

	for _, output := range outputs {
		if output.Width <= 0 || output.Height <= 0 {
			t.Errorf("output %q has logical size %dx%d", output.Name, output.Width, output.Height)
		}

		if output.Scale < 1 {
			t.Errorf("output %q has scale %d", output.Name, output.Scale)
		}
	}
}

func TestCaptureOutput_LiveCompositor(t *testing.T) {
	ctx := liveContext(t)

	conn, err := Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	outputs, err := conn.Outputs()
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}

	if len(outputs) == 0 {
		t.Fatal("Outputs returned no outputs on a live compositor")
	}

	output := outputs[0]

	img, origin, scale, err := conn.CaptureOutput(ctx, output)
	if err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}

	if img == nil || img.Rect.Empty() {
		t.Fatal("CaptureOutput returned an empty image")
	}

	if got, want := origin, image.Pt(output.X, output.Y); got != want {
		t.Errorf("origin = %v, want the output's layout corner %v", got, want)
	}

	if want := float64(output.Scale); scale != want {
		t.Errorf("scale = %v, want %v", scale, want)
	}
}

func TestCaptureRegion_LiveCompositor(t *testing.T) {
	ctx := liveContext(t)

	conn, err := Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	outputs, err := conn.Outputs()
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}

	if len(outputs) == 0 {
		t.Fatal("Outputs returned no outputs on a live compositor")
	}

	output := outputs[0]
	region := image.Rect(output.X+1, output.Y+1, output.X+min(33, output.Width), output.Y+min(33, output.Height))

	img, origin, scale, err := conn.CaptureRegion(ctx, region)
	if err != nil {
		t.Fatalf("CaptureRegion: %v", err)
	}

	if img == nil || img.Rect.Empty() {
		t.Fatal("CaptureRegion returned an empty image")
	}

	if got, want := origin, region.Min; got != want {
		t.Errorf("origin = %v, want the region's corner %v", got, want)
	}

	if scale <= 0 {
		t.Errorf("scale = %v, want a positive factor", scale)
	}
}
