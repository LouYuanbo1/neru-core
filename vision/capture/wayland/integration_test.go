//go:build integration && linux

package wayland

import (
	"context"
	"testing"
	"time"
)

func TestCaptureScreen_LiveCompositor(t *testing.T) {
	if !Available() {
		t.Skip("no Wayland compositor socket in this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	frame, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	if frame.Img == nil || frame.Img.Rect.Empty() {
		t.Fatal("CaptureScreen returned an empty image")
	}

	if frame.Region.Empty() {
		t.Fatalf("region = %v, want the output's logical bounds", frame.Region)
	}

	if frame.Scale < 1 {
		t.Errorf("scale = %v, want at least 1", frame.Scale)
	}

	if !frame.Origin.In(frame.Region) {
		t.Errorf("origin %v is outside region %v", frame.Origin, frame.Region)
	}
}

func TestCaptureRect_LiveCompositor(t *testing.T) {
	if !Available() {
		t.Skip("no Wayland compositor socket in this session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	screen, err := CaptureScreen(ctx)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	region := screen.Region
	region.Max = region.Min.Add(region.Size().Div(4))
	if region.Empty() {
		t.Skip("the output is too small to crop")
	}

	frame, err := CaptureRect(ctx, region)
	if err != nil {
		t.Fatalf("CaptureRect: %v", err)
	}

	if frame.Img == nil || frame.Img.Rect.Empty() {
		t.Fatal("CaptureRect returned an empty image")
	}

	if frame.Origin != region.Min {
		t.Errorf("origin = %v, want %v", frame.Origin, region.Min)
	}
}
