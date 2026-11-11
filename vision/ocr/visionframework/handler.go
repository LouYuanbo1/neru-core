//go:build darwin

package visionframework

import (
	"context"
	"errors"
	"runtime"

	"github.com/LouYuanbo1/neru-core/errs"
)

// withPool runs fn inside an Objective-C autorelease pool. A goroutine has no
// pool of its own, so every message that autoreleases anything (an NSString
// built by a class method, an NSArray returned by Vision) would otherwise keep
// its objects alive until the process exits. A pool belongs to the thread that
// pushed it, so the goroutine is pinned to its thread for the duration.
func (api *frameworks) withPool(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pool := api.objc.poolPush()
	defer api.objc.poolPop(pool)

	return fn()
}

// perform runs one request against cgImage and returns the request's results
// array, which is owned by request: the caller reads it before releasing
// request, inside the same pool.
//
// performRequests:error: is synchronous and Vision cannot be interrupted, so
// ctx is checked before the handler exists and again once Vision is done. A
// call therefore overshoots a deadline by at most one request rather than
// running without a bound.
func (api *frameworks) perform(
	ctx context.Context,
	cgImage, request uintptr,
	name string,
) (uintptr, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}

	options := api.objc.msgSendID(api.class("NSDictionary"), api.selector("dictionary"))
	if options == 0 {
		return 0, errs.New(errs.CodeInternal, "Vision could not create the request handler options")
	}

	allocated := api.objc.msgSendID(api.class("VNImageRequestHandler"), api.selector("alloc"))
	handler := api.objc.msgSendIDWithTwoIDs(
		allocated,
		api.selector("initWithCGImage:options:"),
		cgImage,
		options,
	)
	if handler == 0 {
		return 0, errs.New(errs.CodeInternal, "Vision could not create a VNImageRequestHandler")
	}

	defer api.objc.msgSendVoid(handler, api.selector("release"))

	requests := api.objc.msgSendArrayWithObjects(
		api.class("NSArray"),
		api.selector("arrayWithObjects:count:"),
		[]uintptr{request},
		1,
	)
	if requests == 0 {
		return 0, errs.New(errs.CodeInternal, "Vision could not create the request array")
	}

	var requestError uintptr

	if ok := api.objc.msgSendPerformRequests(
		handler,
		api.selector("performRequests:error:"),
		requests,
		&requestError,
	); !ok {
		return 0, api.visionError(name, requestError)
	}

	if err := contextError(ctx); err != nil {
		return 0, err
	}

	return api.objc.msgSendID(request, api.selector("results")), nil
}

// newRequest allocates and initialises one Vision request. The caller owns the
// result and releases it, inside the pool.
func (api *frameworks) newRequest(name string) (uintptr, error) {
	class := api.class(name)
	if class == 0 {
		return 0, errs.Newf(
			errs.CodeNotSupported,
			"the macOS Vision framework has no %s; macOS 10.15 or later is required",
			name,
		)
	}

	allocated := api.objc.msgSendID(class, api.selector("alloc"))
	request := api.objc.msgSendID(allocated, api.selector("init"))
	if request == 0 {
		return 0, errs.Newf(errs.CodeInternal, "Vision could not create a %s", name)
	}

	return request, nil
}

// contextError converts a finished context into the error the engine contract
// names for it: a deadline is CodeTimeout, a cancellation
// CodeContextCanceled. A live context yields nil.
func contextError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, "Vision ran out of the caller's time budget")
	}

	return errs.Wrap(err, errs.CodeContextCanceled, "Vision was canceled by the caller")
}
