package vision

import (
	"cmp"
	"context"
	"image"
	"slices"
	"time"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/contour"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// Defaults for the text-and-rectangles pipeline. They are the values the
// original implementation shipped: keep every observation, merge overlaps
// above half, keep up to a hundred boxes, and accept boxes at least 1% of the
// region's shorter side and between 0.3:1 and 10:1.
const (
	DefaultVisionTimeout      = 5 * time.Second
	DefaultMergeIOU           = 0.5
	DefaultMaxRectangles      = 100
	DefaultRectangleMinSize   = 0.01
	DefaultRectangleMinAspect = 0.3
	DefaultRectangleMaxAspect = 10.0
)

// BoxFinder detects rectangle-shaped targets in a frame. A backend that has
// one (Apple's Vision framework is the notable case) can hand the pipeline its
// native rectangle pass instead of the pure-Go contour detector.
//
// Boxes answers in the image's pixel space, like ocr.Word.Bounds: pixels of
// img, top-left origin. The pipeline maps them onto the desktop itself.
type BoxFinder interface {
	Boxes(ctx context.Context, img *image.RGBA) ([]image.Rectangle, error)
}

// RectangleOptions tune both the contour fallback and the filtering applied to
// a BoxFinder's answers.
//
// MinSize is a fraction of the frame region's shorter side, which a box's
// shorter side must reach; the aspect bounds are width over height.
type RectangleOptions struct {
	MinSize       float64
	MinAspect     float64
	MaxAspect     float64
	MaxCandidates int
}

// normalized fills the zero values with the shipped defaults.
func (o RectangleOptions) normalized() RectangleOptions {
	if o.MinSize <= 0 {
		o.MinSize = DefaultRectangleMinSize
	}

	if o.MinAspect <= 0 {
		o.MinAspect = DefaultRectangleMinAspect
	}

	if o.MaxAspect <= 0 {
		o.MaxAspect = DefaultRectangleMaxAspect
	}

	if o.MaxCandidates <= 0 {
		o.MaxCandidates = DefaultMaxRectangles
	}

	return o
}

// Pipeline is the "vision" strategy: recognize text, detect rectangle shapes,
// merge the two, and classify the result into neutral elements.
//
// A zero Pipeline reads text only if DetectText is set and Text is provided,
// and detects rectangles with the pure-Go contour detector when no BoxFinder
// is installed. The classifier falls back to a zero-value Classifier, which
// behaves like the shipped baseline thresholds.
type Pipeline struct {
	// Text is the recognizer the text half runs. Required when DetectText is
	// true; the pipeline never guesses an engine, so an import stays opt-in.
	Text ocr.Recognizer
	// Boxes is an optional native rectangle detector. Nil means the pure-Go
	// contour detector, whose thresholds come from Params.
	Boxes BoxFinder

	// DetectText and DetectRectangles choose the halves to run. At least one
	// must be true.
	DetectText       bool
	DetectRectangles bool

	// WordLevel asks the recognizer for one target per word instead of one per
	// line.
	WordLevel bool

	// MinConfidence drops observations below it. Zero keeps everything, which
	// is the shipped default.
	MinConfidence float64
	// MergeIOU is the non-maximum-suppression threshold. Zero means
	// DefaultMergeIOU.
	MergeIOU float64
	// Timeout bounds one Detect call, OCR and rectangles together. Zero means
	// DefaultVisionTimeout.
	Timeout time.Duration
	// Rectangle filters the rectangle half.
	Rectangle RectangleOptions
	// Params are the contour detector's thresholds when Boxes is nil. The zero
	// value means contour.DefaultParams().
	Params contour.Params
	// Classifier assigns roles. Nil means the baseline classifier.
	Classifier *Classifier
}

// Detect runs the configured halves over frame and returns vision-only
// elements in global coordinates.
//
// The rectangle half runs concurrently with text recognition, as it did in the
// original: a rectangle that a text observation already covers is dropped, so
// the text target keeps its label and its confidence. A rectangle failure is
// tolerated when text succeeded; a text failure fails the call, because the
// text half is the reason the strategy exists.
func (p *Pipeline) Detect(ctx context.Context, frame Frame) ([]*element.Element, error) {
	f := frame
	if err := f.checkRegion(ctx, StrategyVision); err != nil {
		return nil, err
	}

	if !p.DetectText && !p.DetectRectangles {
		return nil, errs.New(
			errs.CodeInvalidInput,
			"the vision pipeline needs DetectText or DetectRectangles",
		)
	}

	if p.DetectText && p.Text == nil {
		return nil, errs.New(
			errs.CodeInvalidInput,
			"the vision pipeline needs a text recognizer when DetectText is set",
		)
	}

	if p.DetectText {
		if err := ocr.Health(ctx, p.Text); err != nil {
			return nil, err
		}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultVisionTimeout
	}

	pipelineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rectangles := p.startRectangles(pipelineCtx, f)

	var text []DetectedRegion

	if p.DetectText {
		words, err := p.Text.Recognize(pipelineCtx, f.Img, ocr.Options{
			WordLevel:     p.WordLevel,
			MinConfidence: p.MinConfidence,
		})
		if err != nil {
			return nil, err
		}

		text = RegionsFromWords(words, f.Region, f.Img.Rect, p.MinConfidence)
	}

	found, rectErr := rectangles()
	if rectErr != nil && !p.DetectText {
		return nil, rectErr
	}

	rects := withoutTextOverlap(found, text, p.mergeIOU())
	merged := MergeRegions(append(text, rects...), p.mergeIOU())

	classifier := p.Classifier
	if classifier == nil {
		classifier = &Classifier{}
	}

	elements, _ := ElementsFromRegions(merged, classifier)

	return elements, nil
}

// Health reports whether the pipeline can run on this machine: it probes the
// recognizer when text detection is on. Rectangle detection needs no probe.
func (p *Pipeline) Health(ctx context.Context) error {
	if !p.DetectText {
		return nil
	}

	if p.Text == nil {
		return errs.New(
			errs.CodeInvalidInput,
			"the vision pipeline needs a text recognizer when DetectText is set",
		)
	}

	return ocr.Health(ctx, p.Text)
}

// mergeIOU returns the configured threshold or the shipped default.
func (p *Pipeline) mergeIOU() float64 {
	if p.MergeIOU > 0 {
		return p.MergeIOU
	}

	return DefaultMergeIOU
}

// startRectangles launches the rectangle half and returns a function that
// waits for it. A disabled half returns no regions and no error without
// starting a goroutine.
func (p *Pipeline) startRectangles(
	ctx context.Context,
	frame Frame,
) func() ([]DetectedRegion, error) {
	if !p.DetectRectangles {
		return func() ([]DetectedRegion, error) { return nil, nil }
	}

	options := p.Rectangle.normalized()

	type result struct {
		regions []DetectedRegion
		err     error
	}

	done := make(chan result, 1)

	go func() {
		boxes, err := p.rectangleBoxes(ctx, frame)
		if err != nil {
			done <- result{nil, err}

			return
		}

		done <- result{filterRectangles(boxes, frame, options), nil}
	}()

	return func() ([]DetectedRegion, error) {
		found := <-done

		return found.regions, found.err
	}
}

// rectangleBoxes returns rectangle candidates in desktop coordinates: the
// contour fallback's frame-local boxes are offset by the frame origin, and a
// native finder's image-pixel boxes go through pixelRectToRegion.
func (p *Pipeline) rectangleBoxes(ctx context.Context, frame Frame) ([]image.Rectangle, error) {
	if p.Boxes != nil {
		boxes, err := p.Boxes.Boxes(ctx, frame.Img)
		if err != nil {
			return nil, err
		}

		converted := make([]image.Rectangle, 0, len(boxes))

		for _, box := range boxes {
			converted = append(converted, frame.pixelRectToRegion(box))
		}

		return converted, nil
	}

	params := p.Params
	if params == (contour.Params{}) {
		params = contour.DefaultParams()
	}

	boxes, err := contour.Detect(ctx, frame.Img, frame.Scale, params)
	if err != nil {
		return nil, err
	}

	for index := range boxes {
		boxes[index] = boxes[index].Add(frame.Origin)
	}

	return boxes, nil
}

// filterRectangles keeps the boxes a hint can land on and orders them largest
// first. Boxes are in desktop coordinates.
//
// minSide is a fraction of the region's shorter side, which a box's shorter
// side must reach. rectangle_max_candidates keeps the largest boxes, since the
// detector scores none; every kept box scores one for the same reason.
func filterRectangles(
	boxes []image.Rectangle,
	frame Frame,
	options RectangleOptions,
) []DetectedRegion {
	options = options.normalized()

	minSide := options.MinSize * float64(min(frame.Region.Dx(), frame.Region.Dy()))
	regions := make([]DetectedRegion, 0, len(boxes))

	for _, box := range boxes {
		bounds := box.Intersect(frame.Region)
		if bounds.Empty() {
			continue
		}

		width, height := float64(bounds.Dx()), float64(bounds.Dy())

		if width <= 0 || height <= 0 {
			continue
		}

		aspect := width / height

		if min(width, height) < minSide ||
			aspect < options.MinAspect || aspect > options.MaxAspect {
			continue
		}

		regions = append(regions, DetectedRegion{Bounds: bounds, Score: 1})
	}

	slices.SortStableFunc(regions, func(a, b DetectedRegion) int {
		return cmp.Compare(rectArea(b.Bounds), rectArea(a.Bounds))
	})

	if len(regions) > options.MaxCandidates {
		regions = regions[:options.MaxCandidates]
	}

	return regions
}

// withoutTextOverlap drops rectangles that a text region overlaps by at least
// iouThreshold. The text region already hints that target and carries its
// label for hint search, which a rectangle would take away if it won the merge.
func withoutTextOverlap(
	rects []DetectedRegion,
	text []DetectedRegion,
	iouThreshold float64,
) []DetectedRegion {
	if len(text) == 0 {
		return rects
	}

	if iouThreshold <= 0 {
		iouThreshold = DefaultMergeIOU
	}

	kept := rects[:0]

	for _, rect := range rects {
		covered := slices.ContainsFunc(text, func(word DetectedRegion) bool {
			return intersectionOverUnion(rect.Bounds, word.Bounds) >= iouThreshold
		})
		if !covered {
			kept = append(kept, rect)
		}
	}

	return kept
}

// rectArea is a rectangle's area in pixels.
func rectArea(bounds image.Rectangle) int {
	return bounds.Dx() * bounds.Dy()
}

// pixelRectToRegion converts a rectangle from the image's pixel space to the
// desktop region the frame covers.
func (f Frame) pixelRectToRegion(box image.Rectangle) image.Rectangle {
	if f.Img == nil || f.Region.Empty() {
		return image.Rectangle{}
	}

	scaleX := float64(f.Img.Rect.Dx()) / float64(f.Region.Dx())
	scaleY := float64(f.Img.Rect.Dy()) / float64(f.Region.Dy())

	if scaleX <= 0 || scaleY <= 0 {
		return image.Rectangle{}
	}

	return image.Rect(
		f.Region.Min.X+int(float64(box.Min.X)/scaleX),
		f.Region.Min.Y+int(float64(box.Min.Y)/scaleY),
		f.Region.Min.X+int(float64(box.Max.X)/scaleX),
		f.Region.Min.Y+int(float64(box.Max.Y)/scaleY),
	).Intersect(f.Region)
}
