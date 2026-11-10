//go:build darwin

package darwin

import (
	"image"
	"math"
	"sync"

	"github.com/LouYuanbo1/neru-core/internal/native"
)

// macOS Accessibility Zoom magnifies one display at a time, and while it is
// magnified only a sub-rectangle of that display is on screen. Moving a real
// mouse into the edge of that rectangle pans it; a synthetic event never does,
// at either event tap. So a cursor this backend positions outside the viewport
// is at the right coordinate and invisible, which leaves the user with no idea
// where it went.
//
// UAZoomChangeFocus is the public API for this and is a no-op when the user's
// zoom follows the pointer (it drives the keyboard-focus and insertion-point
// paths only), so the viewport can only really be moved through SkyLight's
// private per-display entry points. Those symbols are resolved with dlopen, and
// every entry point degrades to "leave the viewport alone" when they are
// missing: a future macOS that drops them costs the follow behavior and
// nothing else.
//
// The per-display variants are the ones to use. Zoom magnifies one display at a
// time, and the display-agnostic variants clamp a requested origin against the
// bounding box of all displays, which with a second display stacked below the
// main one leaves a band at the top edge that can never be brought into view.

// skyLightPath is the private framework that owns the window server's zoom
// state.
const skyLightPath = "/System/Library/PrivateFrameworks/SkyLight.framework/SkyLight"

// zoomViewportMarginPoints is the gap kept between a point and the edge of the
// zoom viewport when panning to reveal it. Without it the point lands exactly on
// the boundary and a cursor drawn there is half clipped.
const zoomViewportMarginPoints = 8.0

// zoomFocusTypeInsertionPoint is kUAZoomFocusTypeInsertionPoint, the focus kind
// the public Universal Access API takes for a caret or insertion point.
const zoomFocusTypeInsertionPoint uint32 = 1

// skyLightZoom is the private zoom surface. The two reserved arguments of the
// setter are undocumented, and zero reproduces the window server's own
// panning. smoothing is persistent user state rather than a per-call option:
// passing anything but the value just read from the getter rewrites the user's
// preference.
type skyLightZoom struct {
	connection    int32
	getParameters func(connection int32, display uint32, origin *cgPoint, factor *float64, smoothing *uint8) int32
	setParameters func(
		connection int32,
		display uint32,
		origin *cgPoint,
		smoothing int32,
		reserved int32,
		factor float64,
		reserved2 float64,
	) int32
}

var (
	zoomOnce    sync.Once
	zoomPrivate skyLightZoom
	zoomClient  *skyLightZoom
)

// loadZoomAPI binds the private zoom surface once. A missing symbol leaves the
// zoom handling off, which is how this backend degrades on a macOS that no
// longer offers one.
func loadZoomAPI() {
	zoomOnce.Do(func() {
		skyLightLib, err := native.Open(skyLightPath)
		if err != nil {
			return
		}

		var (
			mainConnectionID func() int32
			getParameters    func(int32, uint32, *cgPoint, *float64, *uint8) int32
			setParameters    func(int32, uint32, *cgPoint, int32, int32, float64, float64) int32
		)

		if skyLightLib.Func("SLSMainConnectionID", &mainConnectionID) != nil ||
			skyLightLib.Func("SLSGetZoomParametersForDisplay", &getParameters) != nil ||
			skyLightLib.Func("SLSSetZoomParametersForDisplay", &setParameters) != nil {
			return
		}

		zoomPrivate = skyLightZoom{
			connection:    mainConnectionID(),
			getParameters: getParameters,
			setParameters: setParameters,
		}

		zoomClient = &zoomPrivate
	})
}

// zoomEnabled reports whether the user's Accessibility Zoom is on.
func zoomEnabled() bool {
	api, err := loadFrameworks()
	if err != nil || api.ax.zoomEnabled == nil {
		return false
	}

	return api.ax.zoomEnabled() != 0
}

// zoomedDisplay is one display's magnification state.
type zoomedDisplay struct {
	display   uint32
	bounds    cgRect
	origin    cgPoint
	factor    float64
	smoothing uint8
}

// currentZoom returns the zoom state of the display a point falls on, and false
// when zoom is off, the point is on no display, the display is not magnified, or
// the private surface is missing.
func currentZoom(point cgPoint) (zoomedDisplay, bool) {
	loadZoomAPI()

	if zoomClient == nil || !zoomEnabled() {
		return zoomedDisplay{}, false
	}

	display, bounds, ok := displayForPoint(point)
	if !ok {
		return zoomedDisplay{}, false
	}

	var (
		origin    cgPoint
		factor    float64
		smoothing uint8
	)

	if zoomClient.getParameters(zoomClient.connection, display, &origin, &factor, &smoothing) != cgErrorSuccess {
		return zoomedDisplay{}, false
	}

	// A factor of one means this display is not magnified: either zoom is on
	// another display or it is zoomed all the way out. Either way there is no
	// viewport to pan.
	if factor <= 1 {
		return zoomedDisplay{}, false
	}

	return zoomedDisplay{
		display:   display,
		bounds:    bounds,
		origin:    origin,
		factor:    factor,
		smoothing: smoothing,
	}, true
}

// displayForPoint returns the display a point falls on and that display's
// bounds, and false when the point is on no display.
func displayForPoint(point cgPoint) (uint32, cgRect, bool) {
	api, err := loadFrameworks()
	if err != nil {
		return 0, cgRect{}, false
	}

	var (
		display uint32
		matched uint32
	)

	if api.cg.displaysWithPoint(point, 1, &display, &matched) != cgErrorSuccess || matched == 0 {
		return 0, cgRect{}, false
	}

	return display, api.cg.displayBounds(display), true
}

// ensureZoomViewportContainsPoint pans the zoom viewport so target is on screen,
// which every positioned mouse event must do before it is posted. It returns at
// once when no display at the point is magnified, and degrades to the public
// focus API when the private surface is missing.
func ensureZoomViewportContainsPoint(target cgPoint) {
	if !zoomEnabled() {
		return
	}

	loadZoomAPI()

	if zoomClient == nil {
		zoomFocusFallback(target)

		return
	}

	state, ok := currentZoom(target)
	if !ok {
		return
	}

	wanted, pan := zoomPanTarget(state.bounds, state.origin, state.factor, target)
	if !pan {
		return
	}

	// The window server keeps the cursor at a fixed position within the
	// viewport, so panning drags the cursor with it. Callers pan first and
	// position the cursor afterwards, never the other way round.
	zoomClient.setParameters(
		zoomClient.connection,
		state.display,
		&wanted,
		int32(state.smoothing),
		0,
		state.factor,
		0,
	)
}

// zoomFocusFallback asks the documented Universal Access API to bring target
// into view, which is the one path left on a macOS whose private surface has
// gone. Under follow-the-pointer zoom it does nothing, so this is best-effort by
// nature.
func zoomFocusFallback(target cgPoint) {
	api, err := loadFrameworks()
	if err != nil || api.ax.zoomChangeFocus == nil {
		return
	}

	rect := cgRect{Origin: target, Size: cgSize{Width: 1, Height: 1}}

	api.ax.zoomChangeFocus(&rect, nil, zoomFocusTypeInsertionPoint)
}

// zoomPanTarget returns the viewport center that brings target inside the
// current viewport by the smallest pan, clamped so the viewport cannot leave its
// display. pan is false when the target is already inside the margin or the
// clamp pins the viewport exactly where it already is, which is what makes an
// unreachable target cost nothing instead of re-pinning the viewport to the same
// edge on every move.
//
// The margin is clamped to half the viewport, so it can never exceed the
// viewport itself at extreme magnification.
func zoomPanTarget(bounds cgRect, origin cgPoint, factor float64, target cgPoint) (cgPoint, bool) {
	if factor <= 1 || bounds.Size.Width <= 0 || bounds.Size.Height <= 0 {
		return origin, false
	}

	halfWidth := bounds.Size.Width / (2 * factor)
	halfHeight := bounds.Size.Height / (2 * factor)

	marginX := math.Min(zoomViewportMarginPoints, halfWidth/2)
	marginY := math.Min(zoomViewportMarginPoints, halfHeight/2)

	insetWidth := halfWidth - marginX
	insetHeight := halfHeight - marginY

	wanted := origin

	if target.X < origin.X-insetWidth {
		wanted.X = target.X + insetWidth
	} else if target.X > origin.X+insetWidth {
		wanted.X = target.X - insetWidth
	}

	if target.Y < origin.Y-insetHeight {
		wanted.Y = target.Y + insetHeight
	} else if target.Y > origin.Y+insetHeight {
		wanted.Y = target.Y - insetHeight
	}

	wanted.X = math.Max(
		bounds.Origin.X+halfWidth,
		math.Min(bounds.Origin.X+bounds.Size.Width-halfWidth, wanted.X),
	)
	wanted.Y = math.Max(
		bounds.Origin.Y+halfHeight,
		math.Min(bounds.Origin.Y+bounds.Size.Height-halfHeight, wanted.Y),
	)

	return wanted, wanted != origin
}

// zoomViewportRectangle returns the rectangle of a display that is visible at
// the given viewport center and magnification.
func zoomViewportRectangle(bounds cgRect, origin cgPoint, factor float64) cgRect {
	if factor <= 0 {
		return cgRect{}
	}

	halfWidth := bounds.Size.Width / (2 * factor)
	halfHeight := bounds.Size.Height / (2 * factor)

	return cgRect{
		Origin: cgPoint{X: origin.X - halfWidth, Y: origin.Y - halfHeight},
		Size:   cgSize{Width: halfWidth * 2, Height: halfHeight * 2},
	}
}

// ZoomViewport returns the part of the display that Accessibility Zoom is
// currently showing at point, in global top-left-origin points, and false when
// zoom is off, the point is on no display, or that display is not magnified.
//
// An overlay that must not draw outside the magnified region asks this; the
// backend itself already pans the viewport before every positioned event it
// posts.
func (c *Client) ZoomViewport(point image.Point) (image.Rectangle, bool) {
	state, ok := currentZoom(cgPoint{X: float64(point.X), Y: float64(point.Y)})
	if !ok {
		return image.Rectangle{}, false
	}

	return rectFromCG(zoomViewportRectangle(state.bounds, state.origin, state.factor)), true
}
