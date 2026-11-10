//go:build !darwin && !linux && !windows

package accessibility

import (
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/errs"
)

// New reports that this platform has no accessibility backend rather than
// returning a client whose every call would fail.
func New(Options) (ax.Client, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"neru-core has no accessibility backend for this platform",
	)
}
