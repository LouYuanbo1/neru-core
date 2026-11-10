package compositor

import (
	"context"
	"image"
)

// sway's window source. `swaymsg -t get_tree` returns the whole node tree; the
// focused node carries its absolute rectangle, its content rectangle (rect plus
// window_rect, which excludes server-side decorations and so aligns with the
// AT-SPI content origin), and its identity (app_id for a native Wayland client,
// window_properties for an XWayland one).

// swaySource answers sway's questions through its CLI.
type swaySource struct{}

// swayRect is one rectangle in sway's tree.
type swayRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// swayNode mirrors the fields of `swaymsg -t get_tree` this package uses.
type swayNode struct {
	AppID   string `json:"app_id"`
	Name    string `json:"name"`
	Focused bool   `json:"focused"`

	Rect       swayRect `json:"rect"`
	WindowRect swayRect `json:"window_rect"`

	WindowProperties struct {
		Class    string `json:"class"`
		Instance string `json:"instance"`
		Title    string `json:"title"`
	} `json:"window_properties"`

	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

// focused asks sway for the focused node.
//
// sway reports no identity on the tree's own root, so the app id comes from the
// node's app_id when it has one and from its XWayland window_properties
// otherwise. The title is the node's name, which is the window title for a view.
func (s swaySource) focused(ctx context.Context) (Window, bool, error) {
	tree, err := swayTree(ctx)
	if err != nil {
		return Window{}, false, err
	}

	node := findFocused(tree)
	if node == nil {
		return Window{}, false, nil
	}

	window := Window{AppID: swayAppID(node), Title: swayTitle(node)}

	if rect, ok := swayFrameRect(node); ok {
		window.Rect = rect
	}

	return window, true, nil
}

// origin returns the focused node's content origin, accepting it only when the
// node still describes a window of the frame's size.
func (s swaySource) origin(ctx context.Context, frame Window) (image.Point, bool, error) {
	tree, err := swayTree(ctx)
	if err != nil {
		return image.Point{}, false, err
	}

	origin, ok := swayOrigin(tree, frame)

	return origin, ok, nil
}

// swayTree reads and decodes sway's node tree.
func swayTree(ctx context.Context) (*swayNode, error) {
	tree := &swayNode{}

	err := queryJSON(ctx, tree, "swaymsg", "-t", "get_tree")
	if err != nil {
		return nil, err
	}

	return tree, nil
}

// findFocused returns the focused node in the tree, if any.
func findFocused(node *swayNode) *swayNode {
	if node == nil {
		return nil
	}

	if node.Focused {
		return node
	}

	for index := range node.Nodes {
		if hit := findFocused(&node.Nodes[index]); hit != nil {
			return hit
		}
	}

	for index := range node.FloatingNodes {
		if hit := findFocused(&node.FloatingNodes[index]); hit != nil {
			return hit
		}
	}

	return nil
}

// swayAppID returns the application identity sway reports for a node. The two
// vocabularies are mutually exclusive: a native Wayland client has app_id and no
// window_properties, an XWayland one the reverse.
func swayAppID(node *swayNode) string {
	if node.AppID != "" {
		return node.AppID
	}

	if node.WindowProperties.Class != "" {
		return node.WindowProperties.Class
	}

	return node.WindowProperties.Instance
}

// swayTitle returns the window title sway reports for a node.
func swayTitle(node *swayNode) string {
	if node.Name != "" {
		return node.Name
	}

	return node.WindowProperties.Title
}

// swayFrameRect returns a node's absolute rectangle, and reports false when it
// has no area (a container, not a view).
func swayFrameRect(node *swayNode) (image.Rectangle, bool) {
	if node.Rect.Width <= 0 || node.Rect.Height <= 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(
		node.Rect.X,
		node.Rect.Y,
		node.Rect.X+node.Rect.Width,
		node.Rect.Y+node.Rect.Height,
	), true
}
