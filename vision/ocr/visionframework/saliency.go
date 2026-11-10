//go:build darwin

package visionframework

import (
	"context"
	"image"
)

// Salient returns the regions the attention-based saliency model considers
// worth looking at, with VNGenerateAttentionBasedSaliencyImageRequest and the
// salientObjects of its VNSaliencyImageObservation.
//
// Regions are in the image's own pixel space, clipped to it, in the order
// Vision reports them. They are what draws the eye, not what is clickable: a
// caller that wants controls wants Rectangles or Recognizer.Boxes. An image the
// model finds nothing salient in returns no regions and no error.
func Salient(ctx context.Context, img *image.RGBA) ([]image.Rectangle, error) {
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

			rects, detectErr = api.detectSalient(ctx, cgImage, bounds)

			return detectErr
		})
	})
	if err != nil {
		return nil, err
	}

	return rects, nil
}

// detectSalient runs one VNGenerateAttentionBasedSaliencyImageRequest and
// copies its observation's salient objects into rectangles. It must be called
// inside a pool, with a request-free cgImage.
func (api *frameworks) detectSalient(
	ctx context.Context,
	cgImage uintptr,
	bounds image.Rectangle,
) ([]image.Rectangle, error) {
	request, err := api.newRequest("VNGenerateAttentionBasedSaliencyImageRequest")
	if err != nil {
		return nil, err
	}

	defer api.objc.msgSendVoid(request, api.selector("release"))

	results, err := api.perform(
		ctx,
		cgImage,
		request,
		"VNGenerateAttentionBasedSaliencyImageRequest",
	)
	if err != nil {
		return nil, err
	}

	observations := api.arrayItems(results)
	if len(observations) == 0 {
		return nil, nil
	}

	// A saliency observation answers a list of salient objects, which is nil
	// when the model found nothing rather than a list of regions.
	objects := api.arrayItems(api.objc.msgSendID(observations[0], api.selector("salientObjects")))
	rects := make([]image.Rectangle, 0, len(objects))

	for _, object := range objects {
		box := api.objc.msgSendRect(object, api.selector("boundingBox"))

		if rect := pixelRect(normalizedRectOf(box), bounds); !rect.Empty() {
			rects = append(rects, rect)
		}
	}

	return rects, nil
}
