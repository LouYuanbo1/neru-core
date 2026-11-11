//go:build linux

package portal

import (
	"image"
	"testing"
)

func TestPlace_DerivesTheFrameFromTheLayout(t *testing.T) {
	tests := []struct {
		name       string
		layout     layout
		img        image.Rectangle
		wantOrigin image.Point
		wantScale  float64
		wantRegion image.Rectangle
	}{
		{
			name:       "a 2x desktop reports scale 2 in logical pixels",
			layout:     layout{bounds: image.Rect(0, 0, 1920, 1080), known: true},
			img:        image.Rect(0, 0, 3840, 2160),
			wantScale:  2,
			wantRegion: image.Rect(0, 0, 1920, 1080),
		},
		{
			name:       "an unscaled desktop reports scale 1",
			layout:     layout{bounds: image.Rect(0, 0, 1920, 1080), known: true},
			img:        image.Rect(0, 0, 1920, 1080),
			wantScale:  1,
			wantRegion: image.Rect(0, 0, 1920, 1080),
		},
		{
			name:       "a desktop starting left of the origin places the frame there",
			layout:     layout{bounds: image.Rect(-1920, 0, 1920, 1080), known: true},
			img:        image.Rect(0, 0, 3840, 1080),
			wantOrigin: image.Pt(-1920, 0),
			wantScale:  1,
			wantRegion: image.Rect(-1920, 0, 1920, 1080),
		},
		{
			name:       "a fractional scale survives the layout's rounded size",
			layout:     layout{bounds: image.Rect(0, 0, 2560, 1440), known: true},
			img:        image.Rect(0, 0, 3840, 2160),
			wantScale:  1.5,
			wantRegion: image.Rect(0, 0, 2560, 1440),
		},
		{
			name:       "no layout anchors at the origin at scale 1",
			layout:     layout{},
			img:        image.Rect(0, 0, 2560, 1440),
			wantScale:  1,
			wantRegion: image.Rect(0, 0, 2560, 1440),
		},
		{
			name:       "an empty layout falls back the same way",
			layout:     layout{known: true},
			img:        image.Rect(0, 0, 800, 600),
			wantScale:  1,
			wantRegion: image.Rect(0, 0, 800, 600),
		},
		{
			name:       "a picture smaller than the layout claims only what it covers",
			layout:     layout{bounds: image.Rect(0, 0, 2000, 1000), known: true},
			img:        image.Rect(0, 0, 1000, 1000),
			wantScale:  1,
			wantRegion: image.Rect(0, 0, 1000, 1000),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			img := image.NewRGBA(test.img)

			got := place(img, test.layout)
			if got.Img != img {
				t.Error("place replaced the decoded image")
			}

			if got.Origin != test.wantOrigin {
				t.Errorf("origin = %v, want %v", got.Origin, test.wantOrigin)
			}

			if got.Scale != test.wantScale {
				t.Errorf("scale = %v, want %v", got.Scale, test.wantScale)
			}

			if got.Region != test.wantRegion {
				t.Errorf("region = %v, want %v", got.Region, test.wantRegion)
			}
		})
	}
}
