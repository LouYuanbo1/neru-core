package vision_test

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// fakeRecognizer answers with fixed words, so the pipeline can be tested
// without an OCR engine.
type fakeRecognizer struct {
	words     []ocr.Word
	healthErr error
	called    bool
}

func (f *fakeRecognizer) Recognize(context.Context, *image.RGBA, ocr.Options) ([]ocr.Word, error) {
	f.called = true

	return f.words, nil
}

func (f *fakeRecognizer) Health(context.Context) error { return f.healthErr }

// fakeBoxes answers with fixed rectangles in image pixel space.
type fakeBoxes struct {
	boxes []image.Rectangle
	err   error
}

func (f fakeBoxes) Boxes(context.Context, *image.RGBA) ([]image.Rectangle, error) {
	return f.boxes, f.err
}

// testFrame is a 200x100 capture placed at (100,100) on the desktop, at scale
// one.
func testFrame() vision.Frame {
	return vision.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 200, 100)),
		Origin: image.Pt(100, 100),
		Region: image.Rect(100, 100, 300, 200),
		Scale:  1,
	}
}

func TestPipeline_TextAndRectanglesMerge(t *testing.T) {
	t.Parallel()

	recognizer := &fakeRecognizer{words: []ocr.Word{
		{Text: "Save", Bounds: image.Rect(10, 10, 60, 30), Confidence: 0.9},
	}}

	// The first box covers the word and must lose to it; the second is a
	// separate target.
	boxes := fakeBoxes{boxes: []image.Rectangle{
		image.Rect(5, 5, 70, 35),
		image.Rect(100, 60, 140, 80),
	}}

	pipeline := vision.Pipeline{
		Text:             recognizer,
		Boxes:            boxes,
		DetectText:       true,
		DetectRectangles: true,
	}

	elements, err := pipeline.Detect(context.Background(), testFrame())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(elements) != 2 {
		t.Fatalf("len(elements) = %d, want 2: %+v", len(elements), elements)
	}

	// MergeRegions orders by score, and a rectangle scores one while text
	// carries its recognizer confidence, so find by content rather than index.
	var textElement, rectangleElement *element.Element

	for _, elem := range elements {
		if elem.Title() == "Save" {
			textElement = elem
		} else {
			rectangleElement = elem
		}
	}

	if textElement == nil || textElement.Bounds() != image.Rect(110, 110, 160, 130) {
		t.Fatalf("text element = %+v, want Save at (110,110)-(160,130)", textElement)
	}

	if rectangleElement == nil || rectangleElement.Bounds() != image.Rect(200, 160, 240, 180) {
		t.Fatalf("rectangle element = %+v, want (200,160)-(240,180)", rectangleElement)
	}

	for _, elem := range elements {
		if !elem.IsVisionOnly() {
			t.Errorf("%q is not vision-only", elem.Title())
		}
	}
}

func TestPipeline_RequiresATextRecognizerOnlyWhenTextIsOn(t *testing.T) {
	t.Parallel()

	if _, err := (&vision.Pipeline{}).Detect(context.Background(), testFrame()); err == nil {
		t.Error("Detect with no halves should error")
	}

	if _, err := (&vision.Pipeline{DetectText: true}).Detect(context.Background(), testFrame()); err == nil {
		t.Error("Detect with DetectText and no recognizer should error")
	}

	// Rectangles alone need no recognizer at all.
	pipeline := vision.Pipeline{DetectRectangles: true}
	if _, err := pipeline.Detect(context.Background(), testFrame()); err != nil {
		t.Errorf("Detect(rectangles only): %v", err)
	}
}

func TestPipeline_HealthReportsTheRecognizerState(t *testing.T) {
	t.Parallel()

	unavailable := &fakeRecognizer{
		healthErr: errs.New(errs.CodeNotSupported, "no OCR language pack"),
	}

	pipeline := vision.Pipeline{Text: unavailable, DetectText: true}

	err := pipeline.Health(context.Background())
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("Health = %v, want CodeNotSupported", err)
	}

	if _, detectErr := pipeline.Detect(context.Background(), testFrame()); !errs.IsCode(detectErr, errs.CodeNotSupported) {
		t.Errorf("Detect = %v, want the health failure", detectErr)
	}
}

func TestPipeline_RectangleFailureIsToleratedWhenTextSucceeded(t *testing.T) {
	t.Parallel()

	recognizer := &fakeRecognizer{words: []ocr.Word{
		{Text: "Save", Bounds: image.Rect(10, 10, 60, 30), Confidence: 0.9},
	}}

	pipeline := vision.Pipeline{
		Text:             recognizer,
		Boxes:            fakeBoxes{err: errors.New("vision framework refused")},
		DetectText:       true,
		DetectRectangles: true,
	}

	elements, err := pipeline.Detect(context.Background(), testFrame())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(elements) != 1 || elements[0].Title() != "Save" {
		t.Errorf("elements = %+v, want just the Save text", elements)
	}
}

func TestPipeline_ScaleMapsPixelsOntoTheDesktop(t *testing.T) {
	t.Parallel()

	frame := vision.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 400, 200)),
		Origin: image.Pt(50, 60),
		Region: image.Rect(50, 60, 250, 160),
		Scale:  2,
	}

	boxes := fakeBoxes{boxes: []image.Rectangle{
		// A 2x capture: a 20x10 pixel box is a 10x5 logical box.
		image.Rect(100, 100, 140, 120),
	}}

	pipeline := vision.Pipeline{Boxes: boxes, DetectRectangles: true}

	elements, err := pipeline.Detect(context.Background(), frame)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(elements) != 1 {
		t.Fatalf("len(elements) = %d, want 1", len(elements))
	}

	want := image.Rect(100, 110, 120, 120)
	if elements[0].Bounds() != want {
		t.Errorf("Bounds = %v, want %v", elements[0].Bounds(), want)
	}
}

func TestFilterRectangles_DropsOutliersAndCapsCandidates(t *testing.T) {
	t.Parallel()

	frame := testFrame()

	tooSmall := image.Rect(110, 110, 112, 112) // 2x2, below 5% of the shorter side
	tooTall := image.Rect(120, 120, 130, 190)  // 0.14:1, below 0.3
	tooWide := image.Rect(140, 140, 300, 145)  // 32:1, above 10
	good := image.Rect(160, 160, 260, 190)     // 3.3:1, inside the region

	// filterRectangles is unexported; exercise it through the pipeline with a
	// finder that returns all four. The finder answers in image pixels, so the
	// desktop boxes lose the frame origin.
	boxes := fakeBoxes{boxes: []image.Rectangle{
		tooSmall.Sub(image.Pt(100, 100)),
		tooTall.Sub(image.Pt(100, 100)),
		tooWide.Sub(image.Pt(100, 100)),
		good.Sub(image.Pt(100, 100)),
	}}

	pipeline := vision.Pipeline{
		Boxes:            boxes,
		DetectRectangles: true,
		Rectangle:        vision.RectangleOptions{MinSize: 0.05, MinAspect: 0.3, MaxAspect: 10, MaxCandidates: 1},
	}

	elements, err := pipeline.Detect(context.Background(), frame)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(elements) != 1 {
		t.Fatalf("len(elements) = %d, want the single good box", len(elements))
	}

	if elements[0].Bounds() != good {
		t.Errorf("Bounds = %v, want %v", elements[0].Bounds(), good)
	}

	if got := element.RoleButton; elements[0].Role() != got {
		t.Errorf("Role = %q, want %q", elements[0].Role(), got)
	}
}
