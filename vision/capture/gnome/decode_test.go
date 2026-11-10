package gnome

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestDecodePNG_ReturnsTheFileAsRGBAAtTheOrigin(t *testing.T) {
	dir := t.TempDir()
	name := path.Join(dir, "shot.png")

	want := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	want.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	want.SetNRGBA(1, 0, color.NRGBA{R: 40, G: 50, B: 60, A: 255})
	want.SetNRGBA(0, 1, color.NRGBA{R: 70, G: 80, B: 90, A: 128})
	want.SetNRGBA(1, 1, color.NRGBA{R: 100, G: 110, B: 120, A: 255})

	writePNG(t, name, want)

	img, err := decodePNG(name)
	if err != nil {
		t.Fatalf("decodePNG: %v", err)
	}

	if img.Rect != image.Rect(0, 0, 2, 2) {
		t.Errorf("rect = %v, want a 2x2 image at the origin", img.Rect)
	}

	assertPixel(t, img, 0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	assertPixel(t, img, 1, 0, color.RGBA{R: 40, G: 50, B: 60, A: 255})
	// image.RGBA stores premultiplied alpha, so the half-transparent pixel the
	// PNG held as (70, 80, 90) arrives as its premultiplied half. A screenshot is
	// opaque in practice, and this is the layout image.RGBAAt already assumes.
	assertPixel(t, img, 0, 1, color.RGBA{R: 35, G: 40, B: 45, A: 128})
	assertPixel(t, img, 1, 1, color.RGBA{R: 100, G: 110, B: 120, A: 255})
}

func TestDecodePNG_ConvertsAPalettedFile(t *testing.T) {
	dir := t.TempDir()
	name := path.Join(dir, "indexed.png")

	palette := color.Palette{
		color.RGBA{R: 0, G: 0, B: 0, A: 255},
		color.RGBA{R: 0, G: 255, B: 0, A: 255},
	}
	indexed := image.NewPaletted(image.Rect(0, 0, 2, 1), palette)
	indexed.SetColorIndex(0, 0, 1)
	indexed.SetColorIndex(1, 0, 0)

	writePNG(t, name, indexed)

	img, err := decodePNG(name)
	if err != nil {
		t.Fatalf("decodePNG: %v", err)
	}

	assertPixel(t, img, 0, 0, color.RGBA{G: 255, A: 255})
	assertPixel(t, img, 1, 0, color.RGBA{A: 255})
}

func TestDecodePNG_RefusesWhatIsNotAPNG(t *testing.T) {
	dir := t.TempDir()
	name := path.Join(dir, "not-a-png.txt")

	if err := os.WriteFile(name, []byte("this is not a screenshot"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := decodePNG(name); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

func TestDecodePNG_RefusesAMissingFile(t *testing.T) {
	if _, err := decodePNG(path.Join(t.TempDir(), "missing.png")); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

func TestDecodePNG_RefusesAnImageWiderThanADesktop(t *testing.T) {
	dir := t.TempDir()
	name := path.Join(dir, "wide.png")

	wide := image.NewRGBA(image.Rect(0, 0, maxScreenshotSide+1, 1))
	writePNG(t, name, wide)

	if _, err := decodePNG(name); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

// writePNG encodes img into name.
func writePNG(t *testing.T, name string, img image.Image) {
	t.Helper()

	file, err := os.Create(name)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := png.Encode(file, img); err != nil {
		_ = file.Close()

		t.Fatalf("encode PNG: %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// assertPixel checks one pixel of an RGBA image.
func assertPixel(t *testing.T, img *image.RGBA, x, y int, want color.RGBA) {
	t.Helper()

	got := img.RGBAAt(x, y)
	if got != want {
		t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
}
