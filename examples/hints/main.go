// Command hints lists the clickable elements of the frontmost window, the way
// a hints mode starts: find the window, walk it for clickable roles, then
// translate each node into the neutral element model.
//
// It prints roles and geometry, not titles: an element's text is screen
// content, and an example should model the privacy the library expects of its
// callers.
//
// Run it on macOS, Linux or Windows:
//
//	go run ./examples/hints
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/accessibility"
	"github.com/LouYuanbo1/neru-core/element"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hints:", err)
		os.Exit(1)
	}
}

func run() error {
	client, err := accessibility.New(accessibility.Options{})
	if err != nil {
		return err
	}
	defer client.Close()

	if !client.CheckPermissions() {
		fmt.Fprintln(os.Stderr, "hints: accessibility permission is not granted")
	}

	ctx := context.Background()

	window, err := client.FrontmostWindow(ctx)
	if err != nil {
		return fmt.Errorf("frontmost window: %w", err)
	}
	defer window.Release()

	nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
	if err != nil {
		return fmt.Errorf("clickable nodes: %w", err)
	}

	elements := accessibility.FromNodes(nodes)

	// The neutral elements hold copies, so the platform nodes can be released
	// before anything is printed.
	accessibility.ReleaseNodes(nodes)

	fmt.Printf("window role %s, %d clickable elements\n", window.Role(), len(nodes))

	for _, elem := range elements {
		fmt.Printf("- %-16s %v\n", describeRole(elem.Role()), elem.Bounds())
	}

	return nil
}

// describeRole names a neutral role, falling back to the raw value so an
// unmapped role still prints.
func describeRole(role element.Role) string {
	if role == "" {
		return string(element.RoleUnknown)
	}

	return string(role)
}
