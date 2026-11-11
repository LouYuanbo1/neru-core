package vision_test

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

func TestRegionsFromWords_MapsFramePixelsOntoTheDesktop(t *testing.T) {
	t.Parallel()

	region := image.Rect(100, 200, 300, 400)  // what the caller asked for
	imageBounds := image.Rect(0, 0, 400, 400) // a 2x capture of it
	words := []engine.Word{
		{Text: "Save", Bounds: image.Rect(20, 40, 60, 60), Confidence: 0.9},
		{Text: "  ", Bounds: image.Rect(0, 0, 10, 10), Confidence: 0.9},
		{Text: "Cancel", Bounds: image.Rect(200, 100, 260, 120), Confidence: 0.2},
	}

	got := vision.RegionsFromWords(words, region, imageBounds, 0.5)

	if len(got) != 1 {
		t.Fatalf("len(RegionsFromWords) = %d, want 1", len(got))
	}

	want := image.Rect(110, 220, 130, 230)
	if got[0].Bounds != want {
		t.Errorf("Bounds = %v, want %v", got[0].Bounds, want)
	}

	if got[0].Label != "Save" || !got[0].IsText || got[0].Score != 0.9 {
		t.Errorf("region = %+v, want Save at score 0.9", got[0])
	}
}

func TestRegionsFromWords_NoGeometryFindsNothing(t *testing.T) {
	t.Parallel()

	words := []engine.Word{{Text: "Save", Confidence: 1}}

	if got := vision.RegionsFromWords(words, image.Rectangle{}, image.Rect(0, 0, 10, 10), 0); got != nil {
		t.Errorf("RegionsFromWords(empty region) = %v, want nil", got)
	}

	if got := vision.RegionsFromWords(words, image.Rect(0, 0, 10, 10), image.Rectangle{}, 0); got != nil {
		t.Errorf("RegionsFromWords(empty image) = %v, want nil", got)
	}
}

func TestElementsFromRegions_ClassifiesAndLabels(t *testing.T) {
	t.Parallel()

	regions := []vision.DetectedRegion{
		{Bounds: image.Rect(0, 0, 120, 30), Label: "Sign in", Score: 0.9, IsText: true},
		{Bounds: image.Rect(0, 60, 250, 80), Label: "Read more", Score: 0.9, IsText: true},
		{Bounds: image.Rectangle{}, Label: "gone", Score: 0.9, IsText: true},
	}

	elements, skipped := vision.ElementsFromRegions(regions, nil)
	if len(elements) != 2 || skipped != 1 {
		t.Fatalf("ElementsFromRegions = %d elements, %d skipped, want 2 and 1", len(elements), skipped)
	}

	button := elements[0]
	if button.Title() != "Sign in" || button.SearchText() != "Sign in" {
		t.Errorf("label did not reach the element: title=%q search=%q",
			button.Title(), button.SearchText())
	}

	if button.Role() != element.RoleButton || !button.IsClickable() {
		t.Errorf("button region = role %q, clickable %v", button.Role(), button.IsClickable())
	}

	if button.Source() != element.SourceVision || !button.IsVisionOnly() {
		t.Errorf("element is not vision-only: source=%q", button.Source())
	}

	// Long, narrow text classifies as a link, which is clickable.
	link := elements[1]
	if link.Role() != element.RoleLink {
		t.Errorf("link Role = %q, want %q", link.Role(), element.RoleLink)
	}

	if !link.IsClickable() {
		t.Error("a link region is a clickable target")
	}
}
