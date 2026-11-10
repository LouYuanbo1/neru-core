package winrt

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// TestWordBounds_ConvertsAndClips pins the conversion from a WinRT box to the
// space the caller asked in, which is the one piece of this backend a caller
// places hints with: a box a pixel off puts a hint beside its word.
func TestWordBounds_ConvertsAndClips(t *testing.T) {
	tests := []struct {
		name   string
		rect   winrtRect
		factor int
		img    image.Rectangle
		want   image.Rectangle
	}{
		{
			name:   "whole pixels come back unchanged",
			rect:   winrtRect{X: 10, Y: 20, Width: 100, Height: 30},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rect(10, 20, 110, 50),
		},
		{
			name:   "fractional edges round to the nearest pixel",
			rect:   winrtRect{X: 10.4, Y: 20.6, Width: 100.2, Height: 30},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rect(10, 21, 111, 51),
		},
		{
			name:   "a downsampled frame scales its boxes back up",
			rect:   winrtRect{X: 10, Y: 20, Width: 100, Height: 30},
			factor: 2,
			img:    image.Rect(0, 0, 800, 600),
			want:   image.Rect(20, 40, 220, 100),
		},
		{
			name:   "a box past the edge is clipped to the frame",
			rect:   winrtRect{X: 380, Y: 280, Width: 100, Height: 100},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rect(380, 280, 400, 300),
		},
		{
			name:   "a box entirely outside the frame is empty",
			rect:   winrtRect{X: 500, Y: 500, Width: 10, Height: 10},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rectangle{},
		},
		{
			name:   "a negative box is empty",
			rect:   winrtRect{X: -20, Y: -20, Width: 10, Height: 10},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rectangle{},
		},
		{
			name:   "boxes are anchored at the image's own origin",
			rect:   winrtRect{X: 10, Y: 20, Width: 100, Height: 30},
			factor: 1,
			img:    image.Rect(100, 50, 500, 350),
			want:   image.Rect(110, 70, 210, 100),
		},
		{
			name:   "a zero-width box describes nothing",
			rect:   winrtRect{X: 10, Y: 20, Width: 0, Height: 30},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rectangle{},
		},
		{
			name:   "a not-a-number edge describes nothing",
			rect:   winrtRect{X: float32(math.NaN()), Y: 20, Width: 10, Height: 10},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rectangle{},
		},
		{
			name:   "an infinite edge describes nothing",
			rect:   winrtRect{X: 10, Y: 20, Width: float32(math.Inf(1)), Height: 10},
			factor: 1,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rectangle{},
		},
		{
			name:   "the factor is applied before the clip",
			rect:   winrtRect{X: 190, Y: 10, Width: 20, Height: 20},
			factor: 2,
			img:    image.Rect(0, 0, 400, 300),
			want:   image.Rect(380, 20, 400, 60),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := wordBounds(testCase.rect, testCase.factor, testCase.img)
			if got != testCase.want {
				t.Errorf("wordBounds(%+v, %d, %v) = %v, want %v",
					testCase.rect, testCase.factor, testCase.img, got, testCase.want)
			}
		})
	}
}

// TestUnionBounds_DoesNotStartAtTheOrigin pins the line-box union against the
// mistake that would put a line's box at (0, 0): a plain union with the zero
// rectangle does exactly that.
func TestUnionBounds_DoesNotStartAtTheOrigin(t *testing.T) {
	var line image.Rectangle

	line = unionBounds(line, image.Rect(100, 40, 150, 60))
	if want := image.Rect(100, 40, 150, 60); line != want {
		t.Fatalf("first box: unionBounds = %v, want %v", line, want)
	}

	line = unionBounds(line, image.Rectangle{})
	if want := image.Rect(100, 40, 150, 60); line != want {
		t.Fatalf("empty add: unionBounds = %v, want %v", line, want)
	}

	line = unionBounds(line, image.Rect(120, 10, 200, 70))
	if want := image.Rect(100, 10, 200, 70); line != want {
		t.Fatalf("overlapping add: unionBounds = %v, want %v", line, want)
	}

	line = unionBounds(line, image.Rect(250, 80, 260, 90))
	if want := image.Rect(100, 10, 260, 90); line != want {
		t.Fatalf("disjoint add: unionBounds = %v, want %v", line, want)
	}
}

// TestDownsampleFactor_KeepsFramesUnderTheLimit pins the smallest whole factor
// that fits the engine's cap, which is what keeps a 4K frame readable instead of
// refused.
func TestDownsampleFactor_KeepsFramesUnderTheLimit(t *testing.T) {
	tests := []struct {
		name         string
		img          image.Rectangle
		maxDimension int
		want         int
	}{
		{"a frame that fits keeps its pixels", image.Rect(0, 0, 800, 600), 2600, 1},
		{"exactly at the limit", image.Rect(0, 0, 2600, 200), 2600, 1},
		{"one pixel past the limit", image.Rect(0, 0, 2601, 200), 2600, 2},
		{"a 4K frame halves", image.Rect(0, 0, 3840, 2160), 2600, 2},
		{"a very wide frame thirds", image.Rect(0, 0, 6000, 1000), 2600, 3},
		{"the longest side decides", image.Rect(0, 0, 400, 5200), 2600, 2},
		{"an unknown limit downsamples nothing", image.Rect(0, 0, 4000, 4000), 0, 1},
		{"an empty frame downsamples nothing", image.Rect(0, 0, 0, 0), 2600, 1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := downsampleFactor(testCase.img, testCase.maxDimension); got != testCase.want {
				t.Errorf("downsampleFactor(%v, %d) = %d, want %d",
					testCase.img, testCase.maxDimension, got, testCase.want)
			}
		})
	}
}

// TestDownsample_AveragesWholeBlocks pins that shrinking a frame averages it
// rather than picking one pixel out of each block, which is what keeps small
// text legible enough for the engine to read after the resample.
func TestDownsample_AveragesWholeBlocks(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	values := [4]byte{10, 20, 30, 40}

	for y := range 4 {
		for x := range 4 {
			quadrant := (y/2)*2 + x/2
			img.SetRGBA(x, y, rgbaWithRed(values[quadrant]))
		}
	}

	out := downsample(img, 2)

	if got, want := out.Bounds(), image.Rect(0, 0, 2, 2); got != want {
		t.Fatalf("downsample produced %v, want %v", got, want)
	}

	for y := range 2 {
		for x := range 2 {
			want := values[y*2+x]
			if got := out.RGBAAt(x, y).R; got != want {
				t.Errorf("downsample at (%d,%d) red = %d, want %d", x, y, got, want)
			}
		}
	}
}

// TestDownsample_AveragesTheRightPixels is the same check with four distinct
// values in one block, so an implementation that samples a corner instead of
// averaging fails it.
func TestDownsample_AveragesTheRightPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, rgbaWithRed(0))
	img.SetRGBA(1, 0, rgbaWithRed(100))
	img.SetRGBA(0, 1, rgbaWithRed(200))
	img.SetRGBA(1, 1, rgbaWithRed(255))

	out := downsample(img, 2)

	if got, want := out.RGBAAt(0, 0).R, byte((0+100+200+255)/4); got != want {
		t.Errorf("averaged red = %d, want %d", got, want)
	}

	if got := out.RGBAAt(0, 0).A; got != 255 {
		t.Errorf("averaged alpha = %d, want 255", got)
	}
}

// TestDownsample_HonorsTheImageOrigin pins that a frame whose bounds do not
// start at (0,0) is read from the right pixels. A capture is normally anchored,
// but a cropped sub-image is not, and reading the wrong rows would return a box
// that names the wrong text.
func TestDownsample_HonorsTheImageOrigin(t *testing.T) {
	img := image.NewRGBA(image.Rect(5, 7, 9, 11))

	for y := 7; y < 11; y++ {
		for x := 5; x < 9; x++ {
			img.SetRGBA(x, y, rgbaWithRed(255))
		}
	}

	out := downsample(img, 2)

	if got, want := out.Bounds(), image.Rect(0, 0, 2, 2); got != want {
		t.Fatalf("downsample produced %v, want %v", got, want)
	}

	for y := range 2 {
		for x := range 2 {
			if got := out.RGBAAt(x, y).R; got != 255 {
				t.Errorf("downsample at (%d,%d) red = %d, want 255", x, y, got)
			}
		}
	}
}

// rgbaWithRed builds an opaque pixel with the given red channel.
func rgbaWithRed(red byte) color.RGBA {
	return color.RGBA{R: red, A: 0xFF}
}
