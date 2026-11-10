package compositor

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// COSMIC's window source. cosmic-comp publishes its toplevels through the
// zcosmic_toplevel_info_v1 Wayland protocol, which a pure-Go package cannot
// speak: reading it needs a Wayland client, and this library has no cgo. The
// daemon this package was extracted from reads that protocol through its native
// wlroots client.
//
// The origin rule is still ported and used, because an embedder that already
// runs a Wayland client can supply the focused window's rectangle through
// Options.Geometry. Focus is never answered from geometry alone: a rectangle
// says where a window is, not which application owns it, so focus stays with
// AT-SPI's ranking even then.

// cosmicSource answers COSMIC's origin question from an optional geometry
// source.
type cosmicSource struct {
	// geometry is the embedder's focused-window geometry, when one was supplied.
	geometry GeometrySource
}

// focused always reports CodeNotSupported. COSMIC exposes no application
// identity over a transport this package can read, and a rectangle without an
// identity cannot select an AT-SPI frame.
func (s cosmicSource) focused(_ context.Context) (Window, bool, error) {
	return Window{}, false, errs.New(
		errs.CodeNotSupported,
		"COSMIC publishes its toplevels only over the zcosmic_toplevel_info_v1 "+
			"Wayland protocol, which this pure-Go library cannot read; "+
			"focus stays with AT-SPI's own ranking",
	)
}

// origin accepts the embedder-supplied focused window rectangle, and only when
// its size matches the AT-SPI frame, so the same race guard niri and sway apply
// also covers a geometry the caller obtained separately.
func (s cosmicSource) origin(
	ctx context.Context,
	frame Window,
) (image.Point, bool, error) {
	if s.geometry == nil {
		return image.Point{}, false, errs.New(
			errs.CodeNotSupported,
			"COSMIC exposes no window geometry this library can read; "+
				"supply compositor.Options.Geometry from a Wayland client to "+
				"correct hint coordinates",
		)
	}

	bounds, found, err := s.geometry.FocusedGeometry(ctx)
	if err != nil {
		return image.Point{}, false, err
	}

	if !found {
		return image.Point{}, false, nil
	}

	origin, ok := cosmicOrigin(bounds, frame)

	return origin, ok, nil
}
