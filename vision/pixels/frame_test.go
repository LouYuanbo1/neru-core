package pixels_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

func TestCrop_OwnsItsPixels(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}

	f := pixels.Frame{Img: src, Region: image.Rect(0, 0, 4, 4), Scale: 1}

	got, err := pixels.Crop(f, image.Rect(1, 1, 3, 3))
	if err != nil {
		t.Fatalf("Crop: %v", err)
	}

	if got.Img.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("rect = %v, want (0,0)-(2,2)", got.Img.Rect)
	}

	// The crop must own its pixels: a frame that shared the screenshot's storage
	// would keep the whole picture alive and would change underneath a caller
	// when the source was reused.
	src.SetRGBA(1, 1, color.RGBA{R: 9, G: 9, B: 9, A: 255})

	if got.Img.RGBAAt(0, 0).R == 9 {
		t.Error("the crop shares its pixels with the frame it came from")
	}
}

func TestDeriveScale_TurnsTheImageWidthIntoTheDesktopScale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		frameWidth   int
		logicalWidth int
		want         float64
	}{
		{name: "doubled desktop", frameWidth: 3840, logicalWidth: 1920, want: 2},
		{name: "unscaled desktop", frameWidth: 1920, logicalWidth: 1920, want: 1},
		{name: "fractional scale recovers from a rounded logical size", frameWidth: 2560, logicalWidth: 1707, want: 1.5},
		{name: "a smaller image is clamped", frameWidth: 1000, logicalWidth: 2000, want: 1},
		{name: "no image width", frameWidth: 0, logicalWidth: 1920, want: 1},
		{name: "no logical width", frameWidth: 1920, logicalWidth: 0, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := pixels.DeriveScale(test.frameWidth, test.logicalWidth)
			if got != test.want {
				t.Errorf("DeriveScale(%d, %d) = %v, want %v",
					test.frameWidth, test.logicalWidth, got, test.want)
			}
		})
	}
}

func TestCrop_ReturnsTheRequestedPartOfTheFrame(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		frame      pixels.Frame
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
			wantOrigin: image.Pt(0, 0),
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
			t.Parallel()

			got, err := pixels.Crop(test.frame, test.region)
			if err != nil {
				t.Fatalf("Crop: %v", err)
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
	t.Parallel()

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
			t.Parallel()

			_, err := pixels.Crop(frame, test.region)
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("error = %v, want CodeInvalidInput", err)
			}
		})
	}
}

// testFrame builds a frame whose pixels are width by height and whose logical
// region is region.
func testFrame(scale float64, region image.Rectangle, width, height int) pixels.Frame {
	return pixels.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, width, height)),
		Origin: region.Min,
		Scale:  scale,
		Region: region,
	}
}
