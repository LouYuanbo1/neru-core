package gnome

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestPlaceDesktop_DerivesTheFrameFromTheLayout(t *testing.T) {
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

			got := placeDesktop(img, test.layout)
			if got.Img != img {
				t.Error("placeDesktop replaced the decoded image")
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

func TestDeriveScale_TurnsThePictureWidthIntoTheDesktopScale(t *testing.T) {
	tests := []struct {
		name         string
		frameWidth   int
		logicalWidth int
		want         float64
	}{
		{name: "doubled desktop", frameWidth: 3840, logicalWidth: 1920, want: 2},
		{name: "unscaled desktop", frameWidth: 1920, logicalWidth: 1920, want: 1},
		{name: "fractional scale recovers from a rounded logical size", frameWidth: 2560, logicalWidth: 1707, want: 1.5},
		{name: "a picture below the desktop size is clamped", frameWidth: 1000, logicalWidth: 2000, want: 1},
		{name: "no picture width", frameWidth: 0, logicalWidth: 1920, want: 1},
		{name: "no logical width", frameWidth: 1920, logicalWidth: 0, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveScale(test.frameWidth, test.logicalWidth)
			if got != test.want {
				t.Errorf("deriveScale(%d, %d) = %v, want %v",
					test.frameWidth, test.logicalWidth, got, test.want)
			}
		})
	}
}

func TestCrop_ReturnsTheRequestedPartOfTheFrame(t *testing.T) {
	tests := []struct {
		name       string
		frame      Frame
		region     image.Rectangle
		wantImg    image.Rectangle
		wantOrigin image.Point
		wantRegion image.Rectangle
	}{
		{
			name:       "a region inside a 2x frame crops at twice its size",
			frame:      testFrame(2, image.Rect(0, 0, 1920, 1080), 3840, 2160),
			region:     image.Rect(100, 100, 200, 200),
			wantImg:    image.Rect(0, 0, 200, 200),
			wantOrigin: image.Pt(100, 100),
			wantRegion: image.Rect(100, 100, 200, 200),
		},
		{
			name:       "a region hanging off the desktop keeps the part that exists",
			frame:      testFrame(2, image.Rect(0, 0, 1920, 1080), 3840, 2160),
			region:     image.Rect(1900, 1000, 2100, 1200),
			wantImg:    image.Rect(0, 0, 40, 160),
			wantOrigin: image.Pt(1900, 1000),
			wantRegion: image.Rect(1900, 1000, 1920, 1080),
		},
		{
			name:       "a frame left of the origin maps through its own origin",
			frame:      testFrame(1, image.Rect(-1920, 0, 0, 1080), 1920, 1080),
			region:     image.Rect(-1920, 0, -1820, 100),
			wantImg:    image.Rect(0, 0, 100, 100),
			wantOrigin: image.Pt(-1920, 0),
			wantRegion: image.Rect(-1920, 0, -1820, 100),
		},
		{
			name:       "a fractional scale rounds the pixel rectangle",
			frame:      testFrame(1.5, image.Rect(0, 0, 100, 100), 150, 150),
			region:     image.Rect(0, 0, 10, 10),
			wantImg:    image.Rect(0, 0, 15, 15),
			wantRegion: image.Rect(0, 0, 10, 10),
		},
		{
			name:       "one logical pixel at a fractional scale is still a pixel",
			frame:      testFrame(1.5, image.Rect(0, 0, 100, 100), 150, 150),
			region:     image.Rect(1, 1, 2, 2),
			wantImg:    image.Rect(0, 0, 1, 1),
			wantOrigin: image.Pt(1, 1),
			wantRegion: image.Rect(1, 1, 2, 2),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := crop(test.frame, test.region)
			if err != nil {
				t.Fatalf("crop: %v", err)
			}

			if got.Img.Rect != test.wantImg {
				t.Errorf("image rect = %v, want %v", got.Img.Rect, test.wantImg)
			}

			if got.Origin != test.wantOrigin {
				t.Errorf("origin = %v, want %v", got.Origin, test.wantOrigin)
			}

			if got.Region != test.wantRegion {
				t.Errorf("region = %v, want %v", got.Region, test.wantRegion)
			}

			if got.Scale != test.frame.Scale {
				t.Errorf("scale = %v, want the frame's %v", got.Scale, test.frame.Scale)
			}
		})
	}
}

func TestCrop_RejectsARegionOutsideTheFrame(t *testing.T) {
	frame := testFrame(1, image.Rect(0, 0, 800, 600), 800, 600)

	tests := []struct {
		name   string
		region image.Rectangle
	}{
		{name: "wholly outside", region: image.Rect(900, 700, 1000, 800)},
		{name: "empty", region: image.Rect(10, 10, 10, 10)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := crop(frame, test.region)
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("error = %v, want CodeInvalidInput", err)
			}
		})
	}
}

// testFrame builds a frame whose pixels are width by height and whose logical
// region is region.
func testFrame(scale float64, region image.Rectangle, width, height int) Frame {
	return Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, width, height)),
		Origin: region.Min,
		Scale:  scale,
		Region: region,
	}
}
