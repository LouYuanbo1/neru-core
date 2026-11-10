// Package vision ties a captured frame to a list of uncertain elements by
// running the pure-Go contour or segment detector over it and turning the
// boxes they find into vision-only elements. It is the recognizer half of the
// library: platform backends capture pixels and feed them here as *image.RGBA,
// and get back element.Elements they can hand to the ax layer for clicking.
//
// The classifier lives here too. It is a first-pass, geometry-and-text
// heuristic that assigns a role to a detected region. It answers in the
// neutral element.Role vocabulary, never a platform's native role string: a
// platform backend maps those neutral roles to its own tree through
// element.RoleMapper, so this package never has to know a platform spelling.
package vision

import (
	"context"
	"fmt"
	"image"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/contour"
	"github.com/LouYuanbo1/neru-core/vision/segment"
)

// Strategy names a detection approach.
type Strategy string

// The strategies a caller can name.
const (
	// StrategyVision recognizes text with an OCR engine, optionally merges
	// rectangle detections, and classifies the result. It is the "vision"
	// strategy of the original application; run it through Pipeline.
	StrategyVision Strategy = "vision"
	// StrategyContour runs the pure-Go contour detector.
	StrategyContour Strategy = "contour"
	// StrategySegment runs the pure-Go segment detector.
	StrategySegment Strategy = "segment"
)

// Frame is one capture handed to a pixel detector: the pixels, where its
// top-left corner sits on the global desktop, the region in global
// coordinates the caller asked for, and how many frame pixels make one
// logical pixel. The frame may be wider than the region (a capture backend
// may win back a whole display) but never narrower.
type Frame struct {
	// Img is the captured pixels, in RGBA.
	Img *image.RGBA
	// Origin is where the frame's top-left corner sits on the desktop.
	Origin image.Point
	// Region is the part of the frame, in global coordinates, to detect in.
	Region image.Rectangle
	// Scale is frame pixels per logical pixel.
	Scale float64
}

// checkRegion honors a canceled context and refuses an empty region, the
// shared preamble of every detector. It marks the frame's region canonical so
// the detector clips against it consistently.
func (f *Frame) checkRegion(ctx context.Context, detector Strategy) error {
	select {
	case <-ctx.Done():
		return errs.Wrap(
			ctx.Err(),
			errs.CodeContextCanceled,
			"operation canceled",
		)
	default:
	}

	f.Region = f.Region.Canon()
	if f.Region.Empty() {
		return errs.Newf(
			errs.CodeInvalidInput,
			"%s detection needs a region to read; %v is empty",
			detector,
			f.Region,
		)
	}

	return nil
}

// DetectContours finds button-shaped boxes in the frame with the contour
// detector and returns them as vision-only button elements in global
// coordinates.
func DetectContours(
	ctx context.Context,
	frame Frame,
	params contour.Params,
) ([]*element.Element, error) {
	f := frame
	if err := f.checkRegion(ctx, StrategyContour); err != nil {
		return nil, err
	}

	rects, err := contour.Detect(ctx, f.Img, f.Scale, params)
	if err != nil {
		return nil, err
	}

	return targetElements(f, rects, StrategyContour), nil
}

// DetectSegments finds likely hint targets in the frame with the segment
// detector (every word, phrases, or controls) and returns them as vision-only
// button elements in global coordinates.
func DetectSegments(
	ctx context.Context,
	frame Frame,
	targets string,
) ([]*element.Element, error) {
	f := frame
	if err := f.checkRegion(ctx, StrategySegment); err != nil {
		return nil, err
	}

	rects, err := segment.Detect(ctx, f.Img, f.Scale, targets)
	if err != nil {
		return nil, err
	}

	return targetElements(f, rects, StrategySegment), nil
}

// Detect runs the strategy's detector over the frame and returns the boxes it
// finds as vision-only elements in global coordinates. See detectContours and
// detectSegments for the two strategies' shape biases.
func Detect(
	ctx context.Context,
	frame Frame,
	strategy Strategy,
	targets string,
	params contour.Params,
) ([]*element.Element, error) {
	switch strategy {
	case StrategyContour:
		return DetectContours(ctx, frame, params)
	case StrategySegment:
		return DetectSegments(ctx, frame, targets)
	default:
		return nil, errs.Newf(
			errs.CodeInvalidInput,
			"unknown vision strategy %q", strategy,
		)
	}
}

// targetElements turns detector rectangles, which are logical pixels relative
// to the frame's top-left corner, into clickable vision-only buttons in global
// coordinates. Rectangles are clipped to the frame's region, so a frame wider
// than the region asked for yields nothing outside it, and a target straddling
// the edge cannot put its hint, and its click, outside.
func targetElements(
	f Frame,
	rects []image.Rectangle,
	strategy Strategy,
) []*element.Element {
	elements := make([]*element.Element, 0, len(rects))

	for _, rect := range rects {
		bounds := rect.Add(f.Origin).Intersect(f.Region)
		if bounds.Empty() {
			continue
		}

		elementID := element.ID(fmt.Sprintf(
			"%s-%s-%d-%d-%d-%d",
			strategy,
			element.SourceVision,
			bounds.Min.X,
			bounds.Min.Y,
			bounds.Dx(),
			bounds.Dy(),
		))

		elem, err := element.NewElement(
			elementID,
			bounds,
			element.RoleButton,
			element.WithClickable(true),
			element.WithSource(element.SourceVision),
		)
		if err != nil {
			continue
		}

		elements = append(elements, elem)
	}

	return elements
}
