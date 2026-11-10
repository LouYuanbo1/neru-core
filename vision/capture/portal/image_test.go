//go:build linux

package portal

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestToRGBA_ReanchorsAnImageAtTheOrigin(t *testing.T) {
	src := image.NewRGBA(image.Rect(5, 5, 7, 7))
	src.SetRGBA(5, 5, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	src.SetRGBA(6, 6, color.RGBA{R: 4, G: 5, B: 6, A: 255})

	got := toRGBA(src)

	if got.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("rect = %v, want (0,0)-(2,2)", got.Rect)
	}

	if got.RGBAAt(0, 0) != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("pixel (0,0) = %v, want the source's (5,5)", got.RGBAAt(0, 0))
	}

	if got.RGBAAt(1, 1) != (color.RGBA{R: 4, G: 5, B: 6, A: 255}) {
		t.Errorf("pixel (1,1) = %v, want the source's (6,6)", got.RGBAAt(1, 1))
	}
}

func TestToRGBA_PassesThroughRGBAAlreadyAtTheOrigin(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))

	if got := toRGBA(src); got != src {
		t.Error("toRGBA copied an image that needed no conversion")
	}
}

func TestToRGBA_PremultipliesATranslucentPixel(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 128})

	got := toRGBA(src).RGBAAt(0, 0)
	if got.A != 128 {
		t.Errorf("alpha = %d, want 128", got.A)
	}

	if got.R >= 200 || got.G >= 100 || got.B >= 50 {
		t.Errorf("pixel = %v, want premultiplied components below the source's", got)
	}
}

func TestCropImage_CopiesTheRequestedPixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))

	for y := range 4 {
		for x := range 4 {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}

	got := cropImage(src, image.Rect(1, 1, 3, 3))

	if got.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("rect = %v, want (0,0)-(2,2)", got.Rect)
	}

	if got.Stride != 2*4 {
		t.Errorf("stride = %d, want a tightly packed 2-pixel row", got.Stride)
	}

	if got.RGBAAt(0, 0) != (color.RGBA{R: 1, G: 1, B: 0, A: 255}) {
		t.Errorf("pixel (0,0) = %v, want the source's (1,1)", got.RGBAAt(0, 0))
	}

	// The crop must own its pixels: a frame that shared the screenshot's
	// storage would keep the whole picture alive and would change underneath a
	// caller when the source was reused.
	src.SetRGBA(1, 1, color.RGBA{R: 9, G: 9, B: 9, A: 255})

	if got.RGBAAt(0, 0).R == 9 {
		t.Error("the crop shares its pixels with the screenshot it came from")
	}
}

func TestDecodeFile_ReadsThePNGThePortalWrote(t *testing.T) {
	path := writePNG(t, pngImage())

	got, err := decodeFile(path)
	if err != nil {
		t.Fatalf("decodeFile: %v", err)
	}

	if got.Rect != image.Rect(0, 0, 3, 2) {
		t.Fatalf("rect = %v, want (0,0)-(3,2)", got.Rect)
	}

	if got.RGBAAt(1, 1) != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("pixel (1,1) = %v, want (1,2,3,255)", got.RGBAAt(1, 1))
	}
}

func TestDecodeFile_RefusesAFileThatIsNotAPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("not a png"), 0o600); err != nil {
		t.Fatalf("write the test file: %v", err)
	}

	_, err := decodeFile(path)
	if !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

func TestDecodeFile_RefusesAnAbsurdPictureSize(t *testing.T) {
	// A header wider than any desktop, which is what a corrupt or hostile PNG
	// would carry. The bound is checked before the decoder allocates the image.
	path := writePNG(t, image.NewRGBA(image.Rect(0, 0, maxScreenshotSide+1, 1)))

	_, err := decodeFile(path)
	if !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

func TestDecodeFile_ReportsAMissingFile(t *testing.T) {
	_, err := decodeFile(filepath.Join(t.TempDir(), "gone.png"))
	if !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

// pngImage is the picture decodeFile's tests round-trip.
func pngImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.SetRGBA(1, 1, color.RGBA{R: 1, G: 2, B: 3, A: 255})

	return img
}

// writePNG encodes img into a temporary directory and returns its path.
func writePNG(t *testing.T, img image.Image) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "shot.png")

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the test file: %v", err)
	}

	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("encode the test picture: %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("close the test file: %v", err)
	}

	return path
}
