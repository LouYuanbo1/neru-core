package kwin

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestPlaceScreen_MatchesAnOutputByItsLogicalSize(t *testing.T) {
	tests := []struct {
		name       string
		meta       metadata
		layout     layout
		img        image.Rectangle
		wantOrigin image.Point
		wantScale  float64
		wantRegion image.Rectangle
	}{
		{
			name:   "a 2x output right of the origin is placed on it",
			meta:   metadata{hasScale: true, scale: 2},
			layout: layout{bounds: image.Rect(1920, 0, 3840, 1080), outputs: []image.Rectangle{image.Rect(1920, 0, 3840, 1080)}, known: true},
			img:    image.Rect(0, 0, 3840, 2160),

			wantOrigin: image.Pt(1920, 0),
			wantScale:  2,
			wantRegion: image.Rect(1920, 0, 3840, 1080),
		},
		{
			name:   "a whole desktop capture matches the desktop bounds",
			meta:   metadata{hasScale: true, scale: 1},
			layout: layout{bounds: image.Rect(-1920, 0, 1920, 1080), outputs: []image.Rectangle{image.Rect(-1920, 0, 0, 1080), image.Rect(0, 0, 1920, 1080)}, known: true},
			img:    image.Rect(0, 0, 3840, 1080),

			wantOrigin: image.Pt(-1920, 0),
			wantScale:  1,
			wantRegion: image.Rect(-1920, 0, 1920, 1080),
		},
		{
			name:   "a fractional scale survives the layout's rounded size",
			meta:   metadata{hasScale: true, scale: 1.5},
			layout: layout{bounds: image.Rect(0, 0, 2560, 1440), outputs: []image.Rectangle{image.Rect(0, 0, 2560, 1440)}, known: true},
			img:    image.Rect(0, 0, 3840, 2160),

			wantScale:  1.5,
			wantRegion: image.Rect(0, 0, 2560, 1440),
		},
		{
			name:   "an unmatched image covers only its own logical size",
			meta:   metadata{hasScale: true, scale: 1},
			layout: layout{bounds: image.Rect(0, 0, 2000, 1000), known: true},
			img:    image.Rect(0, 0, 1000, 1000),

			wantScale:  1,
			wantRegion: image.Rect(0, 0, 1000, 1000),
		},
		{
			name:   "no layout anchors at the origin and keeps the metadata scale",
			meta:   metadata{hasScale: true, scale: 2},
			layout: layout{},
			img:    image.Rect(0, 0, 3840, 2160),

			wantScale:  2,
			wantRegion: image.Rect(0, 0, 1920, 1080),
		},
		{
			name:   "a reply without a scale is inferred from the matching output",
			meta:   metadata{},
			layout: layout{bounds: image.Rect(0, 0, 1920, 1080), outputs: []image.Rectangle{image.Rect(0, 0, 1920, 1080)}, known: true},
			img:    image.Rect(0, 0, 3840, 2160),

			wantScale:  2,
			wantRegion: image.Rect(0, 0, 1920, 1080),
		},
		{
			name:   "a reply without a scale prefers the output it fits exactly",
			meta:   metadata{},
			layout: layout{bounds: image.Rect(0, 0, 3840, 1080), outputs: []image.Rectangle{image.Rect(1920, 0, 3840, 1080), image.Rect(0, 0, 1920, 1080)}, known: true},
			img:    image.Rect(0, 0, 1920, 1080),

			wantOrigin: image.Pt(1920, 0),
			wantScale:  1,
			wantRegion: image.Rect(1920, 0, 3840, 1080),
		},
		{
			name:   "a reply without a scale and without a match stays at scale 1",
			meta:   metadata{},
			layout: layout{bounds: image.Rect(0, 0, 3000, 2000), known: true},
			img:    image.Rect(0, 0, 1000, 700),

			wantScale:  1,
			wantRegion: image.Rect(0, 0, 1000, 700),
		},
		{
			name:   "an empty layout falls back to the origin",
			meta:   metadata{hasScale: true, scale: 1},
			layout: layout{known: true},
			img:    image.Rect(0, 0, 800, 600),

			wantScale:  1,
			wantRegion: image.Rect(0, 0, 800, 600),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			img := image.NewRGBA(test.img)

			got := placeScreen(img, test.meta, test.layout)
			if got.Img != img {
				t.Error("placeScreen replaced the captured image")
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

func TestPlaceArea_KeepsTheRequestedRegion(t *testing.T) {
	tests := []struct {
		name       string
		meta       metadata
		region     image.Rectangle
		img        image.Point
		wantOrigin image.Point
		wantScale  float64
		wantRegion image.Rectangle
	}{
		{
			name:   "a 2x region returns twice its pixels and its own region",
			meta:   metadata{hasScale: true, scale: 2},
			region: image.Rect(100, 100, 300, 300),
			img:    image.Pt(400, 400),

			wantOrigin: image.Pt(100, 100),
			wantScale:  2,
			wantRegion: image.Rect(100, 100, 300, 300),
		},
		{
			name:   "a negative origin survives",
			meta:   metadata{hasScale: true, scale: 1},
			region: image.Rect(-1920, 0, -1820, 100),
			img:    image.Pt(100, 100),

			wantOrigin: image.Pt(-1920, 0),
			wantScale:  1,
			wantRegion: image.Rect(-1920, 0, -1820, 100),
		},
		{
			name:   "a reply without a scale derives it from the pixels",
			meta:   metadata{},
			region: image.Rect(0, 0, 100, 100),
			img:    image.Pt(150, 150),

			wantScale:  1.5,
			wantRegion: image.Rect(0, 0, 100, 100),
		},
		{
			name:   "an image smaller than the request narrows the region",
			meta:   metadata{hasScale: true, scale: 1},
			region: image.Rect(10, 10, 210, 210),
			img:    image.Pt(150, 150),

			wantOrigin: image.Pt(10, 10),
			wantScale:  1,
			wantRegion: image.Rect(10, 10, 160, 160),
		},
		{
			name:   "an image larger than the request is clipped to it",
			meta:   metadata{hasScale: true, scale: 2},
			region: image.Rect(0, 0, 100, 100),
			img:    image.Pt(300, 300),

			wantScale:  2,
			wantRegion: image.Rect(0, 0, 100, 100),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, test.img.X, test.img.Y))

			got := placeArea(img, test.meta, test.region)

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

func TestFitScale_OnlyAcceptsAnExactMultiple(t *testing.T) {
	tests := []struct {
		name   string
		size   image.Point
		rect   image.Rectangle
		want   float64
		wantOK bool
	}{
		{name: "unscaled", size: image.Pt(1920, 1080), rect: image.Rect(0, 0, 1920, 1080), want: 1, wantOK: true},
		{name: "doubled", size: image.Pt(3840, 2160), rect: image.Rect(0, 0, 1920, 1080), want: 2, wantOK: true},
		{name: "one and a half", size: image.Pt(3840, 2160), rect: image.Rect(0, 0, 2560, 1440), want: 1.5, wantOK: true},
		{name: "a smaller image is not a scale", size: image.Pt(960, 540), rect: image.Rect(0, 0, 1920, 1080)},
		{name: "a mismatched aspect ratio is not a scale", size: image.Pt(3840, 1080), rect: image.Rect(0, 0, 1920, 1080)},
		{name: "an empty rectangle", size: image.Pt(1920, 1080), rect: image.Rectangle{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := fitScale(test.size, test.rect)
			if ok != test.wantOK {
				t.Fatalf("fitScale(%v, %v) ok = %v, want %v", test.size, test.rect, ok, test.wantOK)
			}

			if got != test.want {
				t.Errorf("fitScale(%v, %v) = %v, want %v", test.size, test.rect, got, test.want)
			}
		})
	}
}

func TestDeriveScale_TurnsTheImageWidthIntoTheDesktopScale(t *testing.T) {
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
