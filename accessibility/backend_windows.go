//go:build windows

package accessibility

import (
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/windows"
)

// New returns the Windows UI Automation backend.
func New(opts Options) (ax.Client, error) {
	client, err := windows.New(windows.Options{
		ClickableRoles: opts.ClickableRoles,
		MaxDepth:       opts.MaxDepth,
		MaxNodes:       opts.MaxNodes,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}
