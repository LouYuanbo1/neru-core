//go:build darwin

package accessibility

import (
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/darwin"
)

// New returns the macOS accessibility backend.
func New(opts Options) (ax.Client, error) {
	client, err := darwin.New(darwin.Options{
		ClickableRoles: opts.ClickableRoles,
		MaxDepth:       opts.MaxDepth,
		MaxNodes:       opts.MaxNodes,
		SkipHitTest:    opts.SkipHitTest,
		SmoothSteps:    opts.SmoothSteps,
		SmoothDelay:    opts.SmoothDelay,
		BundleType:     opts.BundleType,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}
