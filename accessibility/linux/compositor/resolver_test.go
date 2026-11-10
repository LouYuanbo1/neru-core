package compositor

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// fakeGeometry is a GeometrySource a test drives.
type fakeGeometry struct {
	bounds image.Rectangle
	found  bool
	err    error
}

func (f fakeGeometry) FocusedGeometry(_ context.Context) (image.Rectangle, bool, error) {
	return f.bounds, f.found, f.err
}

// fakeSource is a compositor source a test drives, so the resolver's own
// behavior can be checked without a desktop.
type fakeSource struct {
	window      Window
	found       bool
	err         error
	originPoint image.Point
	known       bool
}

func (f fakeSource) focused(_ context.Context) (Window, bool, error) {
	return f.window, f.found, f.err
}

func (f fakeSource) origin(_ context.Context, _ Window) (image.Point, bool, error) {
	return f.originPoint, f.known, f.err
}

// TestResolver_FocusedApplicationDerivesFromTheSnapshot pins that an application
// walk and the window it came from cannot disagree: both come from one
// FocusedWindow read.
func TestResolver_FocusedApplicationDerivesFromTheSnapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source fakeSource
		want   string
		found  bool
	}{
		{
			name:   "the identity is the window's application",
			source: fakeSource{window: Window{AppID: "firefox", Title: "Mozilla Firefox"}, found: true},
			want:   "firefox",
			found:  true,
		},
		{
			name:   "a window with no application identity reports no application",
			source: fakeSource{window: Window{Title: "Mozilla Firefox"}, found: true},
			found:  false,
		},
		{
			name:   "nothing focused reports nothing",
			source: fakeSource{},
			found:  false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			resolver := &Resolver{source: testCase.source}

			app, found, err := resolver.FocusedApplication(t.Context())
			if err != nil {
				t.Fatalf("FocusedApplication() error = %v", err)
			}

			if found != testCase.found || app.AppID != testCase.want {
				t.Errorf("FocusedApplication() = (%q, %v), want (%q, %v)",
					app.AppID, found, testCase.want, testCase.found)
			}
		})
	}
}

// TestResolver_WindowOriginDelegatesToItsSource pins that the resolver reports
// the source's three answers unchanged.
func TestResolver_WindowOriginDelegatesToItsSource(t *testing.T) {
	t.Parallel()

	resolver := &Resolver{source: fakeSource{originPoint: image.Pt(100, 50), known: true}}

	origin, known, err := resolver.WindowOrigin(t.Context(), Window{Rect: image.Rect(0, 0, 800, 600)})
	if err != nil || !known || origin != image.Pt(100, 50) {
		t.Errorf("WindowOrigin() = (%v, %v, %v), want ((100,50), true, nil)", origin, known, err)
	}
}

// TestResolver_NoSourceReportsNotSupported pins the contract a caller relies on
// to fall back: a session with nobody to ask is CodeNotSupported, never
// "nothing is focused".
func TestResolver_NoSourceReportsNotSupported(t *testing.T) {
	t.Parallel()

	resolver := New(BackendOther, Options{})

	if got := resolver.Backend(); got != BackendOther.String() {
		t.Errorf("Backend() = %q, want %q", got, BackendOther.String())
	}

	if _, found, err := resolver.FocusedWindow(t.Context()); found || !errs.IsNotSupported(err) {
		t.Errorf("FocusedWindow() = (_, %v, %v), want (false, NOT_SUPPORTED)", found, err)
	}

	if _, found, err := resolver.WindowOrigin(t.Context(), Window{}); found || !errs.IsNotSupported(err) {
		t.Errorf("WindowOrigin() = (_, %v, %v), want (false, NOT_SUPPORTED)", found, err)
	}
}

// TestResolver_NilIsSafe keeps a nil resolver, which a caller gets when it turns
// the compositor off, from panicking in a scan.
func TestResolver_NilIsSafe(t *testing.T) {
	t.Parallel()

	var resolver *Resolver

	if got := resolver.Backend(); got != BackendOther.String() {
		t.Errorf("nil Backend() = %q, want %q", got, BackendOther.String())
	}

	if _, found, err := resolver.FocusedApplication(t.Context()); found || !errs.IsNotSupported(err) {
		t.Errorf("nil FocusedApplication() = (_, %v, %v), want (false, NOT_SUPPORTED)", found, err)
	}

	if err := resolver.Close(); err != nil {
		t.Errorf("nil Close() = %v, want nil", err)
	}
}

// TestResolver_CosmicFocusFallsBack pins that COSMIC never answers focus, even
// with geometry: a rectangle says where a window is, not which application owns
// it, so there is no frame to select from it.
func TestResolver_CosmicFocusFallsBack(t *testing.T) {
	t.Parallel()

	resolver := New(BackendCOSMIC, Options{
		Geometry: fakeGeometry{bounds: image.Rect(120, 40, 920, 640), found: true},
	})

	if _, found, err := resolver.FocusedWindow(t.Context()); found || !errs.IsNotSupported(err) {
		t.Errorf("FocusedWindow() = (_, %v, %v), want (false, NOT_SUPPORTED)", found, err)
	}
}

// TestResolver_CosmicOriginUsesSuppliedGeometry covers the one COSMIC path this
// pure-Go package can take: an embedder's rectangle, accepted only for a frame
// of the same size.
func TestResolver_CosmicOriginUsesSuppliedGeometry(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(120, 40, 920, 640)
	resolver := New(BackendCOSMIC, Options{Geometry: fakeGeometry{bounds: bounds, found: true}})

	frame := Window{AppID: "cosmic-term", Rect: image.Rect(0, 0, 800, 600)}

	origin, found, err := resolver.WindowOrigin(t.Context(), frame)
	if err != nil || !found || origin != bounds.Min {
		t.Fatalf("WindowOrigin() = (%v, %v, %v), want (%v, true, nil)",
			origin, found, err, bounds.Min)
	}

	if _, found, err := resolver.WindowOrigin(t.Context(), Window{Rect: image.Rect(0, 0, 400, 300)}); found || err != nil {
		t.Errorf("WindowOrigin() for another size = (_, %v, %v), want (false, nil)", found, err)
	}
}

// TestResolver_CosmicWithoutGeometry pins the documented fallback: no geometry
// source means no origin, with the reason a caller can act on.
func TestResolver_CosmicWithoutGeometry(t *testing.T) {
	t.Parallel()

	resolver := New(BackendCOSMIC, Options{})

	if _, found, err := resolver.WindowOrigin(t.Context(), Window{Rect: image.Rect(0, 0, 800, 600)}); found || !errs.IsNotSupported(err) {
		t.Fatalf("WindowOrigin() = (_, %v, %v), want (false, NOT_SUPPORTED)", found, err)
	}
}

// TestResolver_CosmicPropagatesAGeometryFailure keeps the caller's own failure
// from being mistaken for "nothing focused".
func TestResolver_CosmicPropagatesAGeometryFailure(t *testing.T) {
	t.Parallel()

	boom := errors.New("wayland connection lost")
	resolver := New(BackendCOSMIC, Options{Geometry: fakeGeometry{err: boom}})

	if _, _, err := resolver.WindowOrigin(t.Context(), Window{Rect: image.Rect(0, 0, 800, 600)}); !errors.Is(err, boom) {
		t.Errorf("WindowOrigin() error = %v, want it to carry %v", err, boom)
	}
}

// TestResolver_CloseIsIdempotent pins that a client can close a resolver on
// shutdown without owing a second release to anyone.
func TestResolver_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	resolver := New(BackendKWin, Options{})

	if err := resolver.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}

	if err := resolver.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
}
