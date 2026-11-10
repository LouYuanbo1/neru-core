package compositor

import (
	"context"
	"image"
)

// niri's window source. `niri msg -j focused-window` reports the focused
// window's identity, size and workspace-view position; `niri msg -j
// focused-output` reports the focused output's logical origin. Adding the two
// gives the window's screen rectangle.
//
// niri sets tile_pos_in_workspace_view for floating windows only (niri#2381), so
// a tiled window has no position to give. That is the compositor answering, not
// a failed query: hints fall back to AT-SPI's coordinates without a warning, and
// the output query is skipped rather than being allowed to fail.

// niriSource answers niri's questions through its CLI.
type niriSource struct{}

// niriWindow mirrors the fields of `niri msg -j focused-window` this package
// uses.
type niriWindow struct {
	AppID  string `json:"app_id"`
	Title  string `json:"title"`
	Layout struct {
		WindowSize             []int     `json:"window_size"`
		TilePosInWorkspaceView []float64 `json:"tile_pos_in_workspace_view"`
	} `json:"layout"`
}

// niriOutput mirrors the fields of `niri msg -j focused-output` this package
// uses.
type niriOutput struct {
	Logical struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"logical"`
}

// focused asks niri for the focused window. Rect is filled only for a window
// niri gave a workspace-view position, which is a floating one.
func (s niriSource) focused(ctx context.Context) (Window, bool, error) {
	window, found, err := niriFocusedWindow(ctx)
	if err != nil || !found {
		return Window{}, false, err
	}

	rect, ok, err := niriFocusedRect(ctx, window)
	if err != nil {
		// The window's identity is already an answer, so a failed output query
		// must not throw it away. Rect stays empty, and the origin path reports
		// the failure if the caller asks for one.
		return Window{AppID: window.AppID, Title: window.Title}, true, nil
	}

	result := Window{AppID: window.AppID, Title: window.Title}
	if ok {
		result.Rect = rect
	}

	return result, true, nil
}

// origin returns the focused window's content origin, accepting it only when
// niri still reports a window of the frame's size.
func (s niriSource) origin(ctx context.Context, frame Window) (image.Point, bool, error) {
	window, found, err := niriFocusedWindow(ctx)
	if err != nil || !found {
		return image.Point{}, false, err
	}

	if _, _, ok := niriTile(window); !ok {
		return image.Point{}, false, nil
	}

	output, err := niriFocusedOutput(ctx)
	if err != nil {
		return image.Point{}, false, err
	}

	origin, ok := niriOrigin(window, output, frame)

	return origin, ok, nil
}

// niriFocusedWindow reads the focused window, and reports false when niri says
// there is none. A window with neither identity nor layout is niri's empty
// answer, not a window.
func niriFocusedWindow(ctx context.Context) (niriWindow, bool, error) {
	var window niriWindow

	err := queryJSON(ctx, &window, "niri", "msg", "-j", "focused-window")
	if err != nil {
		return niriWindow{}, false, err
	}

	if window.AppID == "" && window.Title == "" && len(window.Layout.WindowSize) == 0 {
		return niriWindow{}, false, nil
	}

	return window, true, nil
}

// niriFocusedOutput reads the focused output's logical origin.
func niriFocusedOutput(ctx context.Context) (niriOutput, error) {
	var output niriOutput

	err := queryJSON(ctx, &output, "niri", "msg", "-j", "focused-output")

	return output, err
}

// niriFocusedRect computes the window's screen rectangle, and reports false when
// niri gave it no position (a tiled window) or no size. A failed output query is
// returned as an error, because it is a query that never happened.
func niriFocusedRect(ctx context.Context, window niriWindow) (image.Rectangle, bool, error) {
	if _, _, ok := niriTile(window); !ok {
		return image.Rectangle{}, false, nil
	}

	output, err := niriFocusedOutput(ctx)
	if err != nil {
		return image.Rectangle{}, false, err
	}

	rect, ok := niriScreenRect(window, output)

	return rect, ok, nil
}
