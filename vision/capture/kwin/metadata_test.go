package kwin

import (
	"math"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestMetadataFrom_ReadsKwinScreenShotReply(t *testing.T) {
	values := map[string]any{
		resultType:   "raw",
		resultWidth:  uint32(3840),
		resultHeight: uint32(2160),
		resultStride: uint32(3840 * 4),
		resultFormat: uint32(formatRGBX8888),
		resultScale:  2.0,
		// The reply also carries these; they describe no geometry and are not
		// read, so they must not disturb the parse.
		"screen":   "DP-1",
		"windowId": "00000000-0000-0000-0000-000000000000",
	}

	meta, err := metadataFrom(values)
	if err != nil {
		t.Fatalf("metadataFrom: %v", err)
	}

	if meta.imageType != rawImageType {
		t.Errorf("image type = %q, want %q", meta.imageType, rawImageType)
	}

	if meta.width != 3840 || meta.height != 2160 || meta.stride != 3840*4 {
		t.Errorf("geometry = %dx%d at %d, want 3840x2160 at %d",
			meta.width, meta.height, meta.stride, 3840*4)
	}

	if meta.format != formatRGBX8888 {
		t.Errorf("format = %d, want %d", meta.format, formatRGBX8888)
	}

	if !meta.hasScale || meta.scale != 2 {
		t.Errorf("scale = %v (present %v), want 2", meta.scale, meta.hasScale)
	}
}

func TestMetadataFrom_AcceptsTheIntegerSpellingsKwinMightUse(t *testing.T) {
	tests := []struct {
		name  string
		width any
	}{
		{name: "uint32", width: uint32(800)},
		{name: "int32", width: int32(800)},
		{name: "int", width: 800},
		{name: "int64", width: int64(800)},
		{name: "uint64", width: uint64(800)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta, err := metadataFrom(map[string]any{
				resultType:   "raw",
				resultWidth:  test.width,
				resultHeight: uint32(600),
				resultStride: uint32(3200),
				resultFormat: uint32(formatRGB32),
			})
			if err != nil {
				t.Fatalf("metadataFrom: %v", err)
			}

			if meta.width != 800 || meta.height != 600 {
				t.Errorf("geometry = %dx%d, want 800x600", meta.width, meta.height)
			}
		})
	}
}

func TestMetadataFrom_RejectsAReplyItCannotRead(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			resultType:   "raw",
			resultWidth:  uint32(800),
			resultHeight: uint32(600),
			resultStride: uint32(3200),
			resultFormat: uint32(formatRGB32),
		}
	}

	tests := []struct {
		name string
		edit func(map[string]any)
		want errs.Code
	}{
		{name: "no dictionary", edit: func(map[string]any) {}, want: errs.CodeInternal},
		{name: "no type", edit: func(v map[string]any) { delete(v, resultType) }, want: errs.CodeInternal},
		{name: "a type this package cannot read", edit: func(v map[string]any) { v[resultType] = "png" }, want: errs.CodeNotSupported},
		{name: "no width", edit: func(v map[string]any) { delete(v, resultWidth) }, want: errs.CodeInternal},
		{name: "a width of the wrong type", edit: func(v map[string]any) { v[resultWidth] = "wide" }, want: errs.CodeInternal},
		{name: "no height", edit: func(v map[string]any) { delete(v, resultHeight) }, want: errs.CodeInternal},
		{name: "no stride", edit: func(v map[string]any) { delete(v, resultStride) }, want: errs.CodeInternal},
		{name: "no format", edit: func(v map[string]any) { delete(v, resultFormat) }, want: errs.CodeInternal},
		{name: "no pixels", edit: func(v map[string]any) { v[resultWidth] = uint32(0) }, want: errs.CodeInternal},
		{name: "a dimension beyond a desktop", edit: func(v map[string]any) { v[resultHeight] = uint32(maxScreenshotSide + 1) }, want: errs.CodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := valid()
			test.edit(values)

			if test.name == "no dictionary" {
				values = nil
			}

			_, err := metadataFrom(values)
			if !errs.IsCode(err, test.want) {
				t.Errorf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMetadataFrom_TreatsAnUnusableScaleAsAbsent(t *testing.T) {
	tests := []struct {
		name  string
		scale any
		want  bool
	}{
		{name: "a double", scale: 1.5, want: true},
		{name: "an integer", scale: uint32(2), want: true},
		{name: "negative", scale: -1.0, want: false},
		{name: "zero", scale: 0.0, want: false},
		{name: "not a number", scale: math.NaN(), want: false},
		{name: "infinite", scale: math.Inf(1), want: false},
		{name: "missing", scale: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]any{
				resultType:   "raw",
				resultWidth:  uint32(800),
				resultHeight: uint32(600),
				resultStride: uint32(3200),
				resultFormat: uint32(formatRGB32),
			}

			if test.scale != nil {
				values[resultScale] = test.scale
			}

			meta, err := metadataFrom(values)
			if err != nil {
				t.Fatalf("metadataFrom: %v", err)
			}

			if meta.hasScale != test.want {
				t.Errorf("hasScale = %v, want %v (scale %v)", meta.hasScale, test.want, meta.scale)
			}
		})
	}
}

func TestByteLen_DescribesTheWholeBufferKwinWrites(t *testing.T) {
	tests := []struct {
		name string
		meta metadata
		want int
		code errs.Code
	}{
		{
			name: "a four byte pixel",
			meta: metadata{width: 800, height: 600, stride: 3200, format: formatRGBX8888},
			want: 3200 * 600,
		},
		{
			name: "a three byte pixel",
			meta: metadata{width: 800, height: 600, stride: 2400, format: formatBGR888},
			want: 2400 * 600,
		},
		{
			name: "a padded row",
			meta: metadata{width: 801, height: 600, stride: 3204, format: formatRGBX8888},
			want: 3204 * 600,
		},
		{
			name: "a stride too short for the width",
			meta: metadata{width: 800, height: 600, stride: 1600, format: formatRGBX8888},
			code: errs.CodeInternal,
		},
		{
			name: "a zero stride",
			meta: metadata{width: 800, height: 600, stride: 0, format: formatRGBX8888},
			code: errs.CodeInternal,
		},
		{
			name: "an unsupported format",
			meta: metadata{width: 800, height: 600, stride: 3200, format: 25},
			code: errs.CodeNotSupported,
		},
		{
			name: "more bytes than this package reads",
			meta: metadata{width: 64000, height: 64000, stride: 64000 * 4, format: formatRGBX8888},
			code: errs.CodeInternal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.meta.byteLen()
			if test.code != "" {
				if !errs.IsCode(err, test.code) {
					t.Fatalf("error = %v, want %v", err, test.code)
				}

				return
			}

			if err != nil {
				t.Fatalf("byteLen: %v", err)
			}

			if got != test.want {
				t.Errorf("byteLen = %d, want %d", got, test.want)
			}
		})
	}
}
