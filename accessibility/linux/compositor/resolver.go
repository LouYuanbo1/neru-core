package compositor

import (
	"context"
	"image"
	"sync"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Window describes one top-level window as far as the compositor and AT-SPI
// together describe it.
//
// The two roles are the same shape on purpose. FocusedWindow fills Rect with
// where the compositor says the window is. WindowOrigin takes a Window whose
// Rect is the AT-SPI frame's own extents, of which only the size is compared:
// the compositor's rectangle is accepted as the frame's origin only when the two
// describe a window of the same size.
type Window struct {
	// AppID is the application identity the compositor reports: a Wayland
	// app_id, KWin's resourceClass, Hyprland's class, or Mutter's WM_CLASS. It
	// may be empty when the compositor is not asked, and may share no spelling
	// with an AT-SPI application name; comparisons are tolerant.
	AppID string

	// Title is the window's title, used to tell two windows of one application
	// apart and to identify the focused window when the compositor's app id and
	// an AT-SPI application name share no spelling. It is never logged and never
	// put in an error.
	Title string

	// Rect is the window's rectangle. For FocusedWindow it is the compositor's
	// on-screen rectangle, empty when the compositor has no geometry to give (a
	// tiled niri window). For WindowOrigin it is the AT-SPI frame's extents.
	Rect image.Rectangle
}

// Application identifies the focused application. Linux has no bundle
// identifiers, so AppID is whatever the compositor calls the application, which
// is the closest thing a caller can key on.
type Application struct {
	// AppID is the focused application's compositor-reported identity.
	AppID string
}

// GeometrySource supplies the focused window's on-screen rectangle for a
// compositor whose geometry this package cannot read itself.
//
// COSMIC is the case: cosmic-comp publishes window geometry only through the
// zcosmic_toplevel_info_v1 Wayland protocol. An embedder that already runs a
// Wayland client may hand that geometry in; without one, COSMIC reports
// CodeNotSupported and the caller keeps AT-SPI's coordinates.
type GeometrySource interface {
	// FocusedGeometry returns the focused window's on-screen rectangle.
	//
	// found=false with a nil error means the compositor answered and has no
	// focused window to report. A non-nil error means it could not be asked.
	FocusedGeometry(ctx context.Context) (bounds image.Rectangle, found bool, err error)
}

// Options configures a Resolver.
type Options struct {
	// Geometry supplies COSMIC's focused-window geometry. It is ignored on every
	// other backend, which reads its own source.
	Geometry GeometrySource
}

// source is one compositor's window source: the focus snapshot and the origin
// correction, both read from the same compositor-specific API.
type source interface {
	// focused reports the focused window. found=false with a nil error means the
	// compositor answered and nothing is focused. A non-nil error means it could
	// not be asked.
	focused(ctx context.Context) (Window, bool, error)

	// origin reports the screen position of the content origin of the window
	// frame describes. found=false with a nil error is the compositor answering
	// that it has no position for that window (nothing focused, a tiled niri
	// window). A non-nil error means the source failed to answer, which callers
	// must not read as the former.
	origin(ctx context.Context, frame Window) (image.Point, bool, error)
}

// Resolver detects the live compositor and answers the focus and origin
// questions AT-SPI cannot.
//
// It is safe for concurrent use. Sources that hold a D-Bus connection guard it
// with their own mutex, and a compositor CLI is a separate process per query, so
// two scans do not serialize on each other beyond that.
type Resolver struct {
	// backend is what Backend reports.
	backend Backend
	// source answers this backend's questions; never nil.
	source source
	// closer releases a source that holds a connection. It is nil for CLI
	// sources, which own nothing between queries.
	closer func() error

	// closeOnce makes Close idempotent.
	closeOnce sync.Once
	// closeErr is what the source's closer returned, reported again by Close.
	closeErr error
}

// New returns a resolver for an explicitly named backend.
//
// It performs no I/O: a source that needs a connection opens it on its first
// query, so constructing a resolver for a compositor this session does not run
// is not an error. opts is used only by BackendCOSMIC.
func New(backend Backend, opts Options) *Resolver {
	resolver := &Resolver{backend: backend}

	switch backend {
	case BackendKWin:
		source, release := acquireKWinSource()
		resolver.source = source
		resolver.closer = release
	case BackendGNOME:
		gnome := newGNOMESource()
		resolver.source = gnome
		resolver.closer = gnome.close
	case BackendNiri:
		resolver.source = niriSource{}
	case BackendSway:
		resolver.source = swaySource{}
	case BackendHyprland:
		resolver.source = hyprlandSource{}
	case BackendCOSMIC:
		resolver.source = cosmicSource{geometry: opts.Geometry}
	default:
		resolver.source = noSource{backend: backend}
	}

	return resolver
}

// Detect returns the resolver for the running session, reading only the
// environment (see detectBackend for the precedence). It never fails: a session
// it cannot identify gets BackendOther, whose queries report
// errs.CodeNotSupported so the caller keeps AT-SPI's behavior.
//
// The context is accepted for symmetry with the query methods and for a future
// probe; detection itself touches no compositor.
func Detect(ctx context.Context) *Resolver {
	return DetectWithOptions(ctx, Options{})
}

// DetectWithOptions is Detect with source options, for an embedder that can
// supply COSMIC's geometry.
func DetectWithOptions(ctx context.Context, opts Options) *Resolver {
	return New(detectBackend(envGetenv), opts)
}

// Backend reports the detected compositor: "kwin", "gnome", "niri", "sway",
// "hyprland", "cosmic" or "other". It is a plain string because that is the
// vocabulary integrators log and configure.
func (r *Resolver) Backend() string {
	if r == nil {
		return BackendOther.String()
	}

	return r.backend.String()
}

// FocusedApplication returns the focused application, where the compositor can
// name one. found=false with a nil error is "nothing is focused"; a
// CodeNotSupported error is "this session cannot be asked", which a caller must
// handle by ranking AT-SPI's own ACTIVE and SHOWING states.
func (r *Resolver) FocusedApplication(ctx context.Context) (Application, bool, error) {
	window, found, err := r.FocusedWindow(ctx)
	if err != nil || !found || window.AppID == "" {
		return Application{}, false, err
	}

	return Application{AppID: window.AppID}, true, nil
}

// FocusedWindow returns the focused window with the compositor's rectangle and
// identity.
//
// A Wayland client cannot query another client's position, so Rect is empty for
// a compositor that reports identity but no geometry (a tiled niri window).
// Callers that need an origin ask WindowOrigin, which reports the same
// distinction separately.
func (r *Resolver) FocusedWindow(ctx context.Context) (Window, bool, error) {
	if r == nil {
		return Window{}, false, errNoSource(BackendOther)
	}

	return r.source.focused(ctx)
}

// WindowOrigin returns the screen position of the content origin of frame,
// which is the anchor a WINDOW-relative AT-SPI extent must be offset by:
// origin minus the window's own WINDOW-relative position.
//
// frame.Rect is the AT-SPI frame's screen extents; only its size and the
// identity fields are used, because the compositor's rectangle is accepted only
// when it still describes this window. found=false with a nil error is the
// compositor having no position to give (nothing focused, a tiled window, a
// window that changed size since the AT-SPI read). A CodeNotSupported error
// means this session has no origin source at all.
func (r *Resolver) WindowOrigin(ctx context.Context, frame Window) (image.Point, bool, error) {
	if r == nil {
		return image.Point{}, false, errNoSource(BackendOther)
	}

	return r.source.origin(ctx, frame)
}

// Close releases any connection a source keeps open. It is idempotent; a
// resolver with a CLI source owns nothing and closes nothing.
func (r *Resolver) Close() error {
	if r == nil {
		return nil
	}

	r.closeOnce.Do(func() {
		if r.closer != nil {
			r.closeErr = r.closer()
		}
	})

	return r.closeErr
}

// noSource is the resolver for a session with no compositor to ask: X11, an
// unidentified session, or a wlroots compositor whose IPC socket is not
// exported.
type noSource struct {
	backend Backend
}

func (s noSource) focused(_ context.Context) (Window, bool, error) {
	return Window{}, false, errNoSource(s.backend)
}

func (s noSource) origin(_ context.Context, _ Window) (image.Point, bool, error) {
	return image.Point{}, false, errNoSource(s.backend)
}

// errNoSource is the "nobody to ask" error. It is CodeNotSupported rather than a
// failure because nothing is broken: the compositor exposes no query this
// package can make, and the caller should use AT-SPI's own coordinates.
func errNoSource(backend Backend) error {
	return errs.Newf(
		errs.CodeNotSupported,
		"no compositor window source on linux backend %q: this session exposes "+
			"neither an IPC command this library can run nor a D-Bus bridge it can read",
		backend.String(),
	)
}
