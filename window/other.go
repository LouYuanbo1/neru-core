//go:build !windows && !darwin && !linux

package window

import "github.com/LouYuanbo1/neru-core/errs"

// New reports that this platform has no window backend rather than returning a
// manager whose every call would fail.
func New(Options) (Manager, error) {
	return nil, errs.New(
		errs.CodeNotSupported,
		"neru-core has no window backend for this platform",
	)
}
