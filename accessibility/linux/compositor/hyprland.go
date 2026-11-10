package compositor

import (
	"context"
	"image"
)

// Hyprland's window source. `hyprctl -j activewindow` reports the focused
// window's class and title, and its "at" and "size" in absolute screen pixels.
//
// The -j matters: JSON reports a session with no active window as an empty
// object, where the plain-text output says "Invalid", which would read as a
// failed query on an idle desktop.

// hyprlandSource answers Hyprland's questions through its CLI.
type hyprlandSource struct{}

// hyprlandWindow mirrors the fields of `hyprctl -j activewindow` this package
// uses. "at" is [x, y] and "size" is [w, h].
type hyprlandWindow struct {
	Class string `json:"class"`
	Title string `json:"title"`
	At    []int  `json:"at"`
	Size  []int  `json:"size"`
}

// focused asks Hyprland for the active window.
func (s hyprlandSource) focused(ctx context.Context) (Window, bool, error) {
	window, err := hyprlandActiveWindow(ctx)
	if err != nil {
		return Window{}, false, err
	}

	if window.Class == "" && window.Title == "" && len(window.At) == 0 &&
		len(window.Size) == 0 {
		return Window{}, false, nil
	}

	result := Window{AppID: window.Class, Title: window.Title}
	if rect, ok := hyprlandRect(window); ok {
		result.Rect = rect
	}

	return result, true, nil
}

// origin returns the active window's screen origin, accepting it only when
// Hyprland still describes a window of the frame's size.
func (s hyprlandSource) origin(ctx context.Context, frame Window) (image.Point, bool, error) {
	window, err := hyprlandActiveWindow(ctx)
	if err != nil {
		return image.Point{}, false, err
	}

	origin, ok := hyprlandOrigin(window, frame)

	return origin, ok, nil
}

// hyprlandActiveWindow reads and decodes Hyprland's active window.
func hyprlandActiveWindow(ctx context.Context) (hyprlandWindow, error) {
	var window hyprlandWindow

	err := queryJSON(ctx, &window, "hyprctl", "-j", "activewindow")

	return window, err
}
