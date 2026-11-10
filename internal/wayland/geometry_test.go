//go:build linux

package wayland

import (
	"image"
	"image/color"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func testLayout() []Output {
	return []Output{
		{GlobalName: 1, X: 0, Y: 0, Width: 1920, Height: 1080, Scale: 1, Name: "left"},
		{GlobalName: 2, X: 1920, Y: 0, Width: 2560, Height: 1440, Scale: 2, Name: "right"},
		{GlobalName: 3, X: -1280, Y: 0, Width: 1280, Height: 1024, Scale: 1, Name: "negative"},
	}
}

func TestOutputAt_PicksTheOutputContainingTheRegionCenter(t *testing.T) {
	outputs := testLayout()

	cases := []struct {
		name  string
		point image.Point
		want  int
	}{
		{name: "inside the first output", point: image.Pt(10, 10), want: 1},
		{name: "inside the second output", point: image.Pt(2000, 100), want: 2},
		{name: "inside the negative output", point: image.Pt(-100, 100), want: 3},
		{name: "on a shared edge belongs to the right output", point: image.Pt(1920, 0), want: 2},
		{name: "on the right output's far edge is outside it", point: image.Pt(1920+2560, 0), want: 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, ok := outputAt(outputs, testCase.point)

			if testCase.want == 0 {
				if ok {
					t.Fatalf("point %v matched %q, want no output", testCase.point, output.Name)
				}

				return
			}

			if !ok {
				t.Fatalf("point %v matched no output, want %d", testCase.point, testCase.want)
			}

			if output.GlobalName != testCase.want {
				t.Errorf("point %v matched global %d, want %d", testCase.point, output.GlobalName, testCase.want)
			}
		})
	}
}

func TestRegionCenter_UsesTheMidpoint(t *testing.T) {
	if got := regionCenter(image.Rect(10, 20, 30, 40)); got != image.Pt(20, 30) {
		t.Errorf("center = %v, want (20,30)", got)
	}
}

func TestLogicalToPixel_ScalesAroundOutputOrigin(t *testing.T) {
	cases := []struct {
		name   string
		region image.Rectangle
		origin image.Point
		scale  float64
		want   image.Rectangle
	}{
		{
			name:   "scale one is the identity",
			region: image.Rect(10, 20, 30, 40),
			origin: image.Pt(0, 0),
			scale:  1,
			want:   image.Rect(10, 20, 30, 40),
		},
		{
			name:   "scale two doubles offsets from the output origin",
			region: image.Rect(1930, 10, 1940, 20),
			origin: image.Pt(1920, 0),
			scale:  2,
			want:   image.Rect(20, 20, 40, 40),
		},
		{
			name:   "a negative origin is subtracted first",
			region: image.Rect(-1270, 5, -1260, 15),
			origin: image.Pt(-1280, 0),
			scale:  1,
			want:   image.Rect(10, 5, 20, 15),
		},
		{
			name:   "a fractional scale rounds each edge",
			region: image.Rect(1, 1, 3, 3),
			origin: image.Pt(0, 0),
			scale:  1.5,
			want:   image.Rect(2, 2, 5, 5),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := logicalToPixel(testCase.region, testCase.origin, testCase.scale)
			if got != testCase.want {
				t.Errorf("logicalToPixel = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCropImage_ReturnsTheRequestedPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, pixel(x, y))
		}
	}

	cropped, err := cropImage(img, image.Rect(1, 1, 3, 3))
	if err != nil {
		t.Fatalf("cropImage: %v", err)
	}

	if cropped.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("cropped rect = %v, want 2x2 at the origin", cropped.Rect)
	}

	if got := cropped.RGBAAt(0, 0); got != pixel(1, 1) {
		t.Errorf("cropped(0,0) = %v, want source (1,1) = %v", got, pixel(1, 1))
	}

	if got := cropped.RGBAAt(1, 1); got != pixel(2, 2) {
		t.Errorf("cropped(1,1) = %v, want source (2,2) = %v", got, pixel(2, 2))
	}
}

func TestCropImage_ClipsToTheImageBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))

	cropped, err := cropImage(img, image.Rect(2, 1, 10, 10))
	if err != nil {
		t.Fatalf("cropImage: %v", err)
	}

	if cropped.Rect != image.Rect(0, 0, 2, 2) {
		t.Errorf("cropped rect = %v, want the 2x2 overlap", cropped.Rect)
	}
}

func TestCropImage_RejectsARectOutsideTheImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))

	if _, err := cropImage(img, image.Rect(10, 10, 20, 20)); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

// pixel gives a test image a distinct colour per coordinate.
func pixel(x, y int) color.RGBA {
	return color.RGBA{
		R: uint8(10*x + 1),
		G: uint8(10*y + 2),
		B: uint8(10*x + 10*y + 3),
		A: 0xff,
	}
}
