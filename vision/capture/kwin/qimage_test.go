package kwin

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestRGBA_ConvertsEveryFormatThePackageClaims(t *testing.T) {
	tests := []struct {
		name   string
		format int
		data   []byte
		want   [4]byte
	}{
		{
			name:   "RGB32 is BGRX in memory and opaque",
			format: formatRGB32,
			data:   []byte{10, 20, 30, 0x00},
			want:   [4]byte{30, 20, 10, 0xff},
		},
		{
			name:   "ARGB32 is BGRA with its alpha",
			format: formatARGB32,
			data:   []byte{10, 20, 30, 128},
			want:   [4]byte{30, 20, 10, 128},
		},
		{
			name:   "ARGB32 premultiplied is divided by its alpha",
			format: formatARGB32Premultiplied,
			data:   []byte{128, 128, 128, 128},
			want:   [4]byte{255, 255, 255, 128},
		},
		{
			name:   "RGBX8888 is RGBA in byte order and opaque",
			format: formatRGBX8888,
			data:   []byte{1, 2, 3, 0x00},
			want:   [4]byte{1, 2, 3, 0xff},
		},
		{
			name:   "RGBA8888 is copied as it is",
			format: formatRGBA8888,
			data:   []byte{1, 2, 3, 4},
			want:   [4]byte{1, 2, 3, 4},
		},
		{
			name:   "RGBA8888 premultiplied is divided by its alpha",
			format: formatRGBA8888Premultiplied,
			data:   []byte{64, 64, 64, 128},
			want:   [4]byte{128, 128, 128, 128},
		},
		{
			name:   "RGB888 is RGB in byte order",
			format: formatRGB888,
			data:   []byte{1, 2, 3},
			want:   [4]byte{1, 2, 3, 0xff},
		},
		{
			name:   "BGR888 is BGR in byte order",
			format: formatBGR888,
			data:   []byte{1, 2, 3},
			want:   [4]byte{3, 2, 1, 0xff},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := metadata{
				width:  1,
				height: 1,
				stride: len(test.data),
				format: test.format,
			}

			img, err := meta.rgba(test.data)
			if err != nil {
				t.Fatalf("rgba: %v", err)
			}

			if img.Rect != image.Rect(0, 0, 1, 1) {
				t.Errorf("rect = %v, want a 1x1 image at the origin", img.Rect)
			}

			got := [4]byte{img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3]}
			if got != test.want {
				t.Errorf("pixel = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRGBA_ReadsRowsAtTheStrideKwinReported(t *testing.T) {
	data := []byte{
		10, 20, 30, 0xff, 0xaa, 0xaa, 0xaa, 0xaa,
		40, 50, 60, 0xff, 0x00, 0x00, 0x00, 0x00,
	}

	meta := metadata{width: 1, height: 2, stride: 8, format: formatRGB32}

	img, err := meta.rgba(data)
	if err != nil {
		t.Fatalf("rgba: %v", err)
	}

	if img.Rect != image.Rect(0, 0, 1, 2) {
		t.Errorf("rect = %v, want a 1x2 image at the origin", img.Rect)
	}

	first := [4]byte{img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3]}
	if first != [4]byte{30, 20, 10, 0xff} {
		t.Errorf("first row pixel = %v, want [30 20 10 255]", first)
	}

	second := [4]byte{
		img.Pix[img.Stride], img.Pix[img.Stride+1],
		img.Pix[img.Stride+2], img.Pix[img.Stride+3],
	}
	if second != [4]byte{60, 50, 40, 0xff} {
		t.Errorf("second row pixel = %v, want [60 50 40 255]", second)
	}
}

func TestRGBA_PlacesEveryPixelOfATwoByTwoImage(t *testing.T) {
	data := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
	}

	meta := metadata{width: 2, height: 2, stride: 8, format: formatRGBA8888}

	img, err := meta.rgba(data)
	if err != nil {
		t.Fatalf("rgba: %v", err)
	}

	want := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
	}

	for index := range want {
		if img.Pix[index] != want[index] {
			t.Fatalf("pixel byte %d = %d, want %d", index, img.Pix[index], want[index])
		}
	}
}

func TestRGBA_RefusesPixelsItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		meta metadata
		data []byte
		code errs.Code
	}{
		{
			name: "an unsupported QImage format",
			meta: metadata{width: 1, height: 1, stride: 4, format: 25},
			data: []byte{0, 0, 0, 0},
			code: errs.CodeNotSupported,
		},
		{
			name: "fewer bytes than the geometry needs",
			meta: metadata{width: 2, height: 2, stride: 8, format: formatRGBA8888},
			data: []byte{0, 0, 0, 0},
			code: errs.CodeInternal,
		},
		{
			name: "no bytes at all",
			meta: metadata{width: 1, height: 1, stride: 4, format: formatRGBA8888},
			code: errs.CodeInternal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.meta.rgba(test.data)
			if !errs.IsCode(err, test.code) {
				t.Errorf("error = %v, want %v", err, test.code)
			}
		})
	}
}

func TestUnpremultiply_DividesByTheAlphaChannel(t *testing.T) {
	tests := []struct {
		name   string
		colour byte
		alpha  byte
		want   byte
	}{
		{name: "transparent", colour: 0, alpha: 0, want: 0},
		{name: "opaque is the identity", colour: 10, alpha: 0xff, want: 10},
		{name: "half alpha", colour: 64, alpha: 128, want: 128},
		{name: "a premultiplied white", colour: 128, alpha: 128, want: 255},
		{name: "a channel brighter than its alpha is clamped", colour: 255, alpha: 1, want: 255},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := unpremultiply(test.colour, test.alpha); got != test.want {
				t.Errorf("unpremultiply(%d, %d) = %d, want %d",
					test.colour, test.alpha, got, test.want)
			}
		})
	}
}
