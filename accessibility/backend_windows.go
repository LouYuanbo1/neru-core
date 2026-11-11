//go:build windows

package accessibility

import (
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/accessibility/windows"
)

// New returns the Windows UI Automation backend.
func New(opts Options) (ax.Client, error) {
	roles, err := opts.clickableRoles()
	if err != nil {
		return nil, err
	}

	client, err := windows.New(windows.Options{
		ClickableRoles: roles,
		MaxDepth:       opts.MaxDepth,
		MaxNodes:       opts.MaxNodes,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}
