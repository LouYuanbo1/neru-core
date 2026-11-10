package vision

import (
	"fmt"
	"image"
	"strings"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// RegionsFromWords maps recognized words onto the desktop's global top-left
// origin coordinate space and drops the findings not worth a hint.
//
// The words carry bounds in the captured image's own pixel space, with the
// origin at the top-left of the captured region rather than of the screen,
// because that is the only space an engine handed a buffer of pixels can answer
// in. This function puts them back on the desktop.
//
// region is what the caller asked to have captured, in global unscaled pixels.
// imageBounds is what the capture backend actually returned, which is not
// always the same size: a Wayland compositor answers a region request in
// physical pixels, so a 2x output hands back a frame twice the requested size.
// The ratio between the two is the output scale, and dividing by it is what
// puts a hint on its text rather than a quarter of the way up the window.
//
// minConfidence is inclusive: a word scoring exactly the floor is kept. A
// region or image with no area has no mapping, so nothing is returned rather
// than boxes in the wrong space.
func RegionsFromWords(
	words []ocr.Word,
	region image.Rectangle,
	imageBounds image.Rectangle,
	minConfidence float64,
) []DetectedRegion {
	if region.Empty() || imageBounds.Empty() {
		return nil
	}

	scaleX := float64(imageBounds.Dx()) / float64(region.Dx())
	scaleY := float64(imageBounds.Dy()) / float64(region.Dy())

	if scaleX <= 0 || scaleY <= 0 {
		return nil
	}

	regions := make([]DetectedRegion, 0, len(words))

	for _, word := range words {
		if strings.TrimSpace(word.Text) == "" {
			continue
		}

		if word.Confidence < minConfidence {
			continue
		}

		bounds := image.Rect(
			region.Min.X+int(float64(word.Bounds.Min.X)/scaleX),
			region.Min.Y+int(float64(word.Bounds.Min.Y)/scaleY),
			region.Min.X+int(float64(word.Bounds.Max.X)/scaleX),
			region.Min.Y+int(float64(word.Bounds.Max.Y)/scaleY),
		).Intersect(region)

		if bounds.Empty() {
			continue
		}

		regions = append(regions, DetectedRegion{
			Bounds: bounds,
			Label:  word.Text,
			Score:  word.Confidence,
			IsText: true,
		})
	}

	return regions
}

// ElementsFromRegions classifies regions into clickable vision-only elements.
// The second result counts the regions that produced none, so a caller can
// report how many were dropped without reporting anything about them: a
// region's label is text off the user's screen.
//
// A nil classifier uses a zero-value one, which behaves like the shipped
// baseline thresholds.
func ElementsFromRegions(
	regions []DetectedRegion,
	classifier *Classifier,
) ([]*element.Element, int) {
	if classifier == nil {
		classifier = &Classifier{}
	}

	elements := make([]*element.Element, 0, len(regions))
	skipped := 0

	for _, region := range regions {
		if region.Bounds.Empty() {
			skipped++

			continue
		}

		role, clickable := classifier.Classify(region)

		options := []element.Option{
			element.WithSource(element.SourceVision),
		}

		if clickable {
			options = append(options, element.WithClickable(true))
		}

		if region.Label != "" {
			options = append(options,
				element.WithTitle(region.Label),
				element.WithSearchText(region.Label),
			)
		}

		elem, err := element.NewElement(
			element.ID("vision-"+regionKey(region.Bounds)),
			region.Bounds,
			role,
			options...,
		)
		if err != nil {
			skipped++

			continue
		}

		elements = append(elements, elem)
	}

	return elements, skipped
}

// regionKey returns a deterministic key for a rectangle, used as the element
// ID for vision-detected elements. The same control found again on the next
// refresh keeps the same hint identity.
func regionKey(bounds image.Rectangle) string {
	return fmt.Sprintf("%d-%d-%d-%d", bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y)
}

// SortRegionsByScore sorts regions by descending score, in place. MergeRegions
// does this internally; it is exported for callers that keep their own list in
// priority order.
func SortRegionsByScore(regions []DetectedRegion) {
	sortRegionsByScore(regions)
}
