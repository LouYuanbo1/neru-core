//go:build linux

package accessibility

import (
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/linux"
)

// New returns the Linux AT-SPI backend.
func New(opts Options) (ax.Client, error) {
	client, err := linux.New(linux.Options{
		ClickableRoles:    opts.ClickableRoles,
		MaxDepth:          opts.MaxDepth,
		MaxNodes:          opts.MaxNodes,
		DisableCompositor: opts.DisableCompositor,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}
