//go:build windows

package winrt

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// TestRecognizer_Recognize_RefusesAnImageWithNoPixels pins that a frame the
// engine never looked at is an input error rather than an empty result a caller
// would read as "there is no text on this screen".
func TestRecognizer_Recognize_RefusesAnImageWithNoPixels(t *testing.T) {
	tests := []struct {
		name string
		img  *image.RGBA
	}{
		{name: "nil", img: nil},
		{name: "empty bounds", img: image.NewRGBA(image.Rectangle{})},
		{name: "zero height", img: image.NewRGBA(image.Rect(0, 0, 40, 0))},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// No worker and no engine: the refusal must happen before either
			// is reached, or this test would touch the WinRT runtime.
			recognizer := &Recognizer{}

			words, err := recognizer.Recognize(context.Background(), testCase.img, ocr.Options{})
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Fatalf("error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
			}

			if words != nil {
				t.Errorf("refusal returned %d words, want none", len(words))
			}
		})
	}
}

// TestRecognizer_AfterClose_Refuses pins that a closed recognizer cannot be used
// by accident, on either entry point.
func TestRecognizer_AfterClose_Refuses(t *testing.T) {
	recognizer := &Recognizer{}
	recognizer.closed.Store(true)

	if _, err := recognizer.Recognize(
		context.Background(),
		image.NewRGBA(image.Rect(0, 0, 10, 10)),
		ocr.Options{},
	); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("Recognize error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}

	if err := recognizer.Health(context.Background()); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("Health error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}
}

// TestRecognizer_NilHandle_IsHarmless pins the zero pointer: an unset field in a
// caller's struct refuses instead of panicking.
func TestRecognizer_NilHandle_IsHarmless(t *testing.T) {
	var recognizer *Recognizer

	if err := recognizer.Close(); err != nil {
		t.Errorf("Close on a nil recognizer = %v, want nil", err)
	}

	if _, err := recognizer.Recognize(
		context.Background(),
		image.NewRGBA(image.Rect(0, 0, 10, 10)),
		ocr.Options{},
	); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("Recognize error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}

	if err := recognizer.Health(context.Background()); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("Health error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}
}

// TestNew_RefusesTwoOptions pins the variadic shape: New() and New(Options{})
// are both valid, and two values are a mistake rather than a merge.
func TestNew_RefusesTwoOptions(t *testing.T) {
	recognizer, err := New(Options{}, Options{Languages: []string{"en-US"}})
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Fatalf("error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}

	if recognizer != nil {
		t.Error("New returned a recognizer beside an error")
	}
}

// TestRecognizer_HealthIsTheOptionalContract pins that the vision pipeline's
// probe reaches this recognizer and its answer, not a default of "available".
func TestRecognizer_HealthIsTheOptionalContract(t *testing.T) {
	recognizer := &Recognizer{}
	recognizer.closed.Store(true)

	var contract ocr.Recognizer = recognizer
	if err := ocr.Health(context.Background(), contract); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("ocr.Health error code = %s, want %s", errs.GetCode(err), errs.CodeInvalidInput)
	}
}
