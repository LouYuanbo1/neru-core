//go:build linux

package wayland

import (
	"image"
	"image/color"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestConvertShmBuffer_ConvertsXRGB8888ToOpaqueRGBA(t *testing.T) {
	// Two rows of two pixels, laid out B, G, R, pad.
	source := []byte{
		30, 20, 10, 0, 60, 50, 40, 0,
		90, 80, 70, 0, 120, 110, 100, 0,
	}

	img, err := convertShmBuffer(source, 2, 2, 8, shmFormatXRGB8888, false)
	if err != nil {
		t.Fatalf("convertShmBuffer: %v", err)
	}

	if img.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("rect = %v, want 2x2 at the origin", img.Rect)
	}

	want := []color.RGBA{
		{R: 10, G: 20, B: 30, A: 0xff},
		{R: 40, G: 50, B: 60, A: 0xff},
		{R: 70, G: 80, B: 90, A: 0xff},
		{R: 100, G: 110, B: 120, A: 0xff},
	}

	for i, expected := range want {
		got := img.RGBAAt(i%2, i/2)
		if got != expected {
			t.Errorf("pixel %d = %v, want %v", i, got, expected)
		}
	}
}

func TestConvertShmBuffer_ConvertsARGB8888KeepingAlpha(t *testing.T) {
	source := []byte{
		30, 20, 10, 128, 60, 50, 40, 255,
	}

	img, err := convertShmBuffer(source, 2, 1, 8, shmFormatARGB8888, false)
	if err != nil {
		t.Fatalf("convertShmBuffer: %v", err)
	}

	if got, want := img.RGBAAt(0, 0), (color.RGBA{R: 10, G: 20, B: 30, A: 128}); got != want {
		t.Errorf("pixel 0 = %v, want %v", got, want)
	}

	if got, want := img.RGBAAt(1, 0), (color.RGBA{R: 40, G: 50, B: 60, A: 255}); got != want {
		t.Errorf("pixel 1 = %v, want %v", got, want)
	}
}

func TestConvertShmBuffer_InvertsRowsWhenFlagged(t *testing.T) {
	// Row 0 is red, row 1 is blue.
	source := []byte{
		0, 0, 255, 0,
		255, 0, 0, 0,
	}

	straight, err := convertShmBuffer(source, 1, 2, 4, shmFormatXRGB8888, false)
	if err != nil {
		t.Fatalf("convertShmBuffer: %v", err)
	}

	inverted, err := convertShmBuffer(source, 1, 2, 4, shmFormatXRGB8888, true)
	if err != nil {
		t.Fatalf("convertShmBuffer with y-invert: %v", err)
	}

	if got, want := straight.RGBAAt(0, 0), (color.RGBA{R: 255, A: 0xff}); got != want {
		t.Errorf("straight row 0 = %v, want %v", got, want)
	}

	if got, want := inverted.RGBAAt(0, 0), (color.RGBA{B: 255, A: 0xff}); got != want {
		t.Errorf("inverted row 0 = %v, want %v", got, want)
	}

	if got, want := inverted.RGBAAt(0, 1), (color.RGBA{R: 255, A: 0xff}); got != want {
		t.Errorf("inverted row 1 = %v, want %v", got, want)
	}
}

func TestConvertShmBuffer_RejectsUnknownFormat(t *testing.T) {
	source := make([]byte, 16)

	_, err := convertShmBuffer(source, 2, 2, 8, 0x34325241, false)
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Fatalf("error = %v, want CodeNotSupported", err)
	}
}

func TestConvertShmBuffer_RejectsStrideNarrowerThanTheRow(t *testing.T) {
	source := make([]byte, 16)

	_, err := convertShmBuffer(source, 2, 2, 4, shmFormatXRGB8888, false)
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Fatalf("error = %v, want CodeNotSupported", err)
	}
}

func TestConvertShmBuffer_RejectsAShortBuffer(t *testing.T) {
	_, err := convertShmBuffer(make([]byte, 4), 2, 2, 8, shmFormatXRGB8888, false)
	if !errs.IsCode(err, errs.CodeInternal) {
		t.Fatalf("error = %v, want CodeInternal", err)
	}
}

func TestShmBufferInfo_Validate_AcceptsBothSupportedFormats(t *testing.T) {
	for _, format := range []uint32{shmFormatXRGB8888, shmFormatARGB8888} {
		info := shmBufferInfo{format: format, width: 1920, height: 1080, stride: 1920 * 4}

		if err := info.validate(); err != nil {
			t.Errorf("format %d refused: %v", format, err)
		}
	}
}

func TestShmBufferInfo_Validate_RejectsUnknownFormat(t *testing.T) {
	info := shmBufferInfo{format: 0x34325241, width: 2, height: 2, stride: 8}

	if err := info.validate(); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestShmBufferInfo_Validate_RejectsShortStride(t *testing.T) {
	info := shmBufferInfo{format: shmFormatXRGB8888, width: 2, height: 2, stride: 4}

	if err := info.validate(); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestShmBufferInfo_Validate_RejectsEmptyFrame(t *testing.T) {
	info := shmBufferInfo{format: shmFormatXRGB8888}

	if err := info.validate(); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}
