package vision

import (
	"image"
	"math"

	"github.com/LouYuanbo1/neru-core/element"
)

// DetectedRegion is a region of interest identified by a detector. Contour
// and segment find box shapes; a text-recognition backend may add labeled and
// scored text regions. Multiple requests that overlap are merged into a single
// element.
type DetectedRegion struct {
	Bounds image.Rectangle
	Label  string
	Score  float64
	IsText bool
}

// ClassifierOptions are the geometric and saliency thresholds the classifier
// applies. Every field's zero value means the shipped baseline, so an options
// struct built with only the fields a caller wants to tune stays small. These
// replace the application config the original classifier read.
type ClassifierOptions struct {
	GenericClickableMinConfidence float64
	ButtonMinAspect               float64
	ButtonMaxAspect               float64
	ButtonMinConfidence           float64
	ButtonIconMaxSize             float64
	LinkMinAspect                 float64
	LinkMaxHeight                 float64
	LinkMinWidth                  float64
	ImageMinSize                  float64
	CheckboxMaxSize               float64
}

// classifierDefaults are the shipped baselines for every tunable the original
// config carried as defaults, so a zero-value ClassifierOptions behaves like
// the configured default.
var classifierDefaults = [10]float64{
	0.5, // generic-clickable min confidence
	0.8, // button min aspect
	8.0, // button max aspect
	0.3, // button min confidence
	48,  // button/icon max size
	5.0, // link min aspect
	40,  // link max height
	50,  // link min width
	48,  // image min size
	32,  // checkbox max size
}

// Classifier assigns a role to a detected region from geometry and saliency.
// It is the first-pass classifier usable on every platform, answering in the
// neutral element.Role vocabulary.
type Classifier struct {
	opts ClassifierOptions
}

// NewClassifier builds a Classifier. A zero-value Classifier can also be used
// directly; it behaves like NewClassifier(ClassifierOptions{}).
func NewClassifier(opts ClassifierOptions) *Classifier {
	return &Classifier{opts: opts}
}

// Classify assigns a neutral role to a region and whether it is clickable.
func (c *Classifier) Classify(region DetectedRegion) (element.Role, bool) {
	bounds := region.Bounds
	width := float64(bounds.Dx())
	height := float64(bounds.Dy())

	if width <= 0 || height <= 0 {
		return element.RoleUnknown, false
	}

	aspectRatio := width / height

	switch {
	case region.IsText && c.isLikelyButton(aspectRatio, region.Score, width, height):
		return element.RoleButton, true
	case region.IsText && c.isLikelyLink(aspectRatio, width, height):
		return element.RoleLink, true
	case region.IsText:
		return element.RoleStaticText, false
	case c.isLikelyCheckBox(aspectRatio, width, height):
		return element.RoleCheckBox, true
	case c.isLikelyButton(aspectRatio, region.Score, width, height):
		return element.RoleButton, true
	case c.isLikelyImage(aspectRatio, width, height):
		return element.RoleImage, false
	default:
		if region.Score > c.defaulted(0) {
			return element.RoleButton, true
		}

		return element.RoleGenericElement, false
	}
}

// isLikelyButton checks aspect ratio and saliency to guess "button".
// Buttons and toolbar icons are typically 0.8:1 to 8:1 width:height (square-ish
// to wide) with decent saliency. For near-square regions, size is constrained
// to avoid catching large images.
func (c *Classifier) isLikelyButton(aspectRatio, score, width, height float64) bool {
	minAspect := c.defaulted(1)
	maxAspect := c.defaulted(2)
	minConf := c.defaulted(3)
	maxIconSize := c.defaulted(4)

	if aspectRatio < minAspect || aspectRatio > maxAspect || score < minConf {
		return false
	}

	// Near-square regions: limit size to avoid classifying images as buttons.
	if aspectRatio >= minAspect && aspectRatio <= 1.5 {
		return max(width, height) <= maxIconSize
	}

	return true
}

// isLikelyLink checks for long, narrow text (links).
func (c *Classifier) isLikelyLink(aspectRatio, width, height float64) bool {
	minAspect := c.defaulted(5)
	maxHeight := c.defaulted(6)
	minWidth := c.defaulted(7)

	return aspectRatio > minAspect && height < maxHeight && width > minWidth
}

// isLikelyImage checks for near-square regions that are large enough to be
// actual images rather than buttons or icons.
func (c *Classifier) isLikelyImage(aspectRatio, width, height float64) bool {
	minSize := c.defaulted(8)

	return aspectRatio >= 0.8 && aspectRatio <= 1.5 && width >= minSize && height >= minSize
}

// isLikelyCheckBox checks for small square-ish regions (< 32px in both dims).
func (c *Classifier) isLikelyCheckBox(aspectRatio, width, height float64) bool {
	maxSize := c.defaulted(9)

	return aspectRatio >= 0.8 && aspectRatio <= 1.5 && width < maxSize && height < maxSize
}

// defaulted returns the options field if set, else a shipped baseline. The
// field indexes map to classifierDefaults in the order the ClassifierOptions
// fields are declared.
func (c *Classifier) defaulted(index int) float64 {
	switch index {
	case 0:
		if c.opts.GenericClickableMinConfidence > 0 {
			return c.opts.GenericClickableMinConfidence
		}
	case 1:
		if c.opts.ButtonMinAspect > 0 {
			return c.opts.ButtonMinAspect
		}
	case 2:
		if c.opts.ButtonMaxAspect > 0 {
			return c.opts.ButtonMaxAspect
		}
	case 3:
		if c.opts.ButtonMinConfidence > 0 {
			return c.opts.ButtonMinConfidence
		}
	case 4:
		if c.opts.ButtonIconMaxSize > 0 {
			return c.opts.ButtonIconMaxSize
		}
	case 5:
		if c.opts.LinkMinAspect > 0 {
			return c.opts.LinkMinAspect
		}
	case 6:
		if c.opts.LinkMaxHeight > 0 {
			return c.opts.LinkMaxHeight
		}
	case 7:
		if c.opts.LinkMinWidth > 0 {
			return c.opts.LinkMinWidth
		}
	case 8:
		if c.opts.ImageMinSize > 0 {
			return c.opts.ImageMinSize
		}
	case 9:
		if c.opts.CheckboxMaxSize > 0 {
			return c.opts.CheckboxMaxSize
		}
	}

	return classifierDefaults[index]
}

// MergeRegions merges overlapping regions using non-maximum suppression.
// Regions with higher scores suppress lower-scoring overlaps (IoU >
// iouThreshold). A zero or negative threshold uses 0.5.
func MergeRegions(regions []DetectedRegion, iouThreshold float64) []DetectedRegion {
	if len(regions) == 0 {
		return nil
	}

	if iouThreshold <= 0 {
		iouThreshold = 0.5
	}

	sorted := make([]DetectedRegion, len(regions))
	copy(sorted, regions)
	sortRegionsByScore(sorted)

	var result []DetectedRegion
	for len(sorted) > 0 {
		best := sorted[0]
		sorted = sorted[1:]

		var remaining []DetectedRegion
		for _, r := range sorted {
			if intersectionOverUnion(best.Bounds, r.Bounds) < iouThreshold {
				remaining = append(remaining, r)
			}
		}

		sorted = remaining

		result = append(result, best)
	}

	return result
}

// intersectionOverUnion computes the IoU of two rectangles.
func intersectionOverUnion(a, b image.Rectangle) float64 {
	intersection := a.Intersect(b)
	if intersection.Empty() {
		return 0
	}

	intersectArea := float64(intersection.Dx() * intersection.Dy())

	unionArea := float64(a.Dx()*a.Dy()) + float64(b.Dx()*b.Dy()) - intersectArea
	if unionArea <= 0 {
		return 0
	}

	return math.Round(intersectArea/unionArea*100) / 100
}

// sortRegionsByScore sorts regions descending by Score using a simple
// insertion sort (n is typically very small, < 100).
func sortRegionsByScore(regions []DetectedRegion) {
	for i := 1; i < len(regions); i++ {
		key := regions[i]

		insertPos := i - 1
		for insertPos >= 0 && regions[insertPos].Score < key.Score {
			regions[insertPos+1] = regions[insertPos]
			insertPos--
		}

		regions[insertPos+1] = key
	}
}
