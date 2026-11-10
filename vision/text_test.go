package vision_test

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

func TestReadText_JoinsWordsInOrder(t *testing.T) {
	t.Parallel()

	recognizer := &fakeRecognizer{words: []ocr.Word{
		{Text: "Save", Bounds: image.Rect(0, 0, 10, 10), Confidence: 1},
		{Text: "  ", Bounds: image.Rect(0, 0, 10, 10), Confidence: 1},
		{Text: "file", Bounds: image.Rect(20, 0, 30, 10), Confidence: 1},
	}}

	frame := vision.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 100, 50)),
		Region: image.Rect(0, 0, 100, 50),
	}

	text, err := vision.ReadText(context.Background(), frame, recognizer, ocr.Options{})
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}

	if text != "Save file" {
		t.Errorf("ReadText = %q, want %q", text, "Save file")
	}

	if !recognizer.called {
		t.Error("the recognizer was not called")
	}
}

func TestReadText_RequiresARecognizerAndRegion(t *testing.T) {
	t.Parallel()

	frame := vision.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 10, 10)),
		Region: image.Rect(0, 0, 10, 10),
	}

	if _, err := vision.ReadText(context.Background(), frame, nil, ocr.Options{}); err == nil {
		t.Error("ReadText with no recognizer should error")
	}

	empty := vision.Frame{Img: image.NewRGBA(image.Rect(0, 0, 10, 10))}
	if _, err := vision.ReadText(context.Background(), empty, &fakeRecognizer{}, ocr.Options{}); err == nil {
		t.Error("ReadText with an empty region should error")
	}
}

func TestReadText_HealthFailureStopsBeforeRecognition(t *testing.T) {
	t.Parallel()

	recognizer := &fakeRecognizer{
		healthErr: errs.New(errs.CodeNotSupported, "no engine"),
	}

	frame := vision.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 10, 10)),
		Region: image.Rect(0, 0, 10, 10),
	}

	_, err := vision.ReadText(context.Background(), frame, recognizer, ocr.Options{})
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("ReadText = %v, want CodeNotSupported", err)
	}

	if recognizer.called {
		t.Error("the recognizer must not run when Health fails")
	}
}
