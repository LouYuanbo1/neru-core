//go:build darwin

package visionframework

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// RectanglesOptions are the VNDetectRectanglesRequest knobs this package
// exposes. A zero value leaves every field at Vision's own default, which is
// tuned for document scanning rather than for a screen: a caller looking for
// UI controls wants Recognizer.Boxes or explicit values.
//
// Vision stores each of these as a 32-bit float, so a value is rounded to that
// precision when it is handed over.
type RectanglesOptions struct {
	// MinimumSize is the smallest side of a rectangle worth reporting, as a
	// fraction of the image's smaller dimension. Zero leaves Vision's default;
	// otherwise it must be between 0 and 1.
	MinimumSize float64

	// MinimumAspectRatio is the narrowest width-to-height ratio to report.
	// Zero leaves Vision's default; a negative value is refused.
	MinimumAspectRatio float64

	// MaximumAspectRatio is the widest width-to-height ratio to report. Zero
	// leaves Vision's default; a negative value, or one below
	// MinimumAspectRatio, is refused.
	MaximumAspectRatio float64

	// MaximumObservations caps how many rectangles come back. Zero leaves
	// Vision's default; a negative value is refused.
	MaximumObservations int

	// QuadratureTolerance is how far, in degrees, a reported corner may
	// deviate from a right angle. Zero leaves Vision's default; otherwise it
	// must be between 0 and 45.
	QuadratureTolerance float64
}

// validate refuses values Vision would not accept, so a bad option comes back
// as errs.CodeInvalidInput instead of an exception raised inside the framework.
func (o RectanglesOptions) validate() error {
	switch {
	case o.MinimumSize < 0 || o.MinimumSize > 1:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle minimum size %g must be between 0 and 1, as a fraction of the image's smaller side",
			o.MinimumSize,
		)
	case o.MinimumAspectRatio < 0:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle minimum aspect ratio %g must not be negative",
			o.MinimumAspectRatio,
		)
	case o.MaximumAspectRatio < 0:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle maximum aspect ratio %g must not be negative",
			o.MaximumAspectRatio,
		)
	case o.MaximumAspectRatio > 0 && o.MinimumAspectRatio > o.MaximumAspectRatio:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle minimum aspect ratio %g is above the maximum %g",
			o.MinimumAspectRatio,
			o.MaximumAspectRatio,
		)
	case o.MaximumObservations < 0:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle maximum observations %d must not be negative",
			o.MaximumObservations,
		)
	case o.QuadratureTolerance < 0 || o.QuadratureTolerance > 45:
		return errs.Newf(
			errs.CodeInvalidInput,
			"rectangle quadrature tolerance %g must be between 0 and 45 degrees",
			o.QuadratureTolerance,
		)
	default:
		return nil
	}
}

// Rectangles detects box-shaped regions in img with VNDetectRectanglesRequest.
//
// Rectangles are in the image's own pixel space, clipped to it, in Vision's
// order (most confident first). A rectangle Vision reports partly outside the
// frame is trimmed to what is inside; one entirely outside is dropped. No
// rectangle found is not an error, and neither is an image with nothing
// box-shaped in it.
//
// The error vocabulary follows the package's: errs.CodeNotSupported without
// Vision, errs.CodeInvalidInput for a bad image or bad options,
// errs.CodeTimeout and errs.CodeContextCanceled for the caller's deadline, and
// errs.CodeAccessibilityFailed for a request Vision itself failed.
func Rectangles(
	ctx context.Context,
	img *image.RGBA,
	opts RectanglesOptions,
) ([]image.Rectangle, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	api, err := load()
	if err != nil {
		return nil, err
	}

	bounds, err := imageBounds(img)
	if err != nil {
		return nil, err
	}

	var rects []image.Rectangle

	err = api.withPool(func() error {
		return api.withCGImage(img, func(cgImage uintptr) error {
			var detectErr error

			rects, detectErr = api.detectRectangles(ctx, cgImage, bounds, opts)

			return detectErr
		})
	})
	if err != nil {
		return nil, err
	}

	return rects, nil
}

// detectRectangles runs one VNDetectRectanglesRequest. It must be called
// inside a pool, with a request-free cgImage.
func (api *frameworks) detectRectangles(
	ctx context.Context,
	cgImage uintptr,
	bounds image.Rectangle,
	opts RectanglesOptions,
) ([]image.Rectangle, error) {
	request, err := api.newRequest("VNDetectRectanglesRequest")
	if err != nil {
		return nil, err
	}

	defer api.objc.msgSendVoid(request, api.selector("release"))

	if opts.MaximumObservations > 0 {
		api.objc.msgSendVoidWithUint(
			request,
			api.selector("setMaximumObservations:"),
			uintptr(opts.MaximumObservations),
		)
	}

	setFloat := func(selector string, value float64) {
		if value > 0 {
			api.objc.msgSendVoidWithFloat(request, api.selector(selector), float32(value))
		}
	}

	setFloat("setMinimumSize:", opts.MinimumSize)
	setFloat("setMinimumAspectRatio:", opts.MinimumAspectRatio)
	setFloat("setMaximumAspectRatio:", opts.MaximumAspectRatio)
	setFloat("setQuadratureTolerance:", opts.QuadratureTolerance)

	results, err := api.perform(ctx, cgImage, request, "VNDetectRectanglesRequest")
	if err != nil {
		return nil, err
	}

	observations := api.arrayItems(results)
	rects := make([]image.Rectangle, 0, len(observations))

	for _, observation := range observations {
		box := api.objc.msgSendRect(observation, api.selector("boundingBox"))

		if rect := pixelRect(normalizedRectOf(box), bounds); !rect.Empty() {
			rects = append(rects, rect)
		}
	}

	return rects, nil
}
