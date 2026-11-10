//go:build !windows && !darwin && !linux

package capture

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// unsupported answers every non-implementing platform: the capture contract
// is satisfied by refusing with CodeNotSupported rather than fabricating a
// frame. Implementations that can read the screen never reach these.
func unsupported(what string) error {
	return errs.New(errs.CodeNotSupported, what+" is not supported on this platform")
}

// captureScreen is the non-implementing answer on this platform: refusing with
// CodeNotSupported is more honest than reading an empty screen.
func captureScreen(context.Context) (Shot, error) {
	return Shot{}, unsupported("screen capture")
}

// captureRect has the same contract as captureScreen: this platform cannot
// read the screen, so a region capture is refused too.
func captureRect(context.Context, image.Rectangle) (Shot, error) {
	return Shot{}, unsupported("region capture")
}
