package compositor

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// GNOME's window source. Mutter exposes no window geometry to a client and no
// scripting interface a library may use, so the answer comes from the Neru GNOME
// Shell extension, which reports the focused window over its own D-Bus name.
//
// The extension is installed and enabled by the neru daemon, not by this
// library: writing an extension into a user's data directory is a daemon's
// decision, not a library's. A session without it reports CodeNotSupported, and
// the caller keeps AT-SPI's coordinates.
//
// The protocol is one method, FocusedWindow, whose reply is (found, app id,
// title, x, y, width, height). The app id is Mutter's WM_CLASS, the same
// vocabulary foreign-toplevel speaks elsewhere.

const (
	// gnomeBusName is the Neru GNOME Shell extension's well-known name.
	gnomeBusName = "org.neru.Shell"
	// gnomeObjectPath is the object carrying FocusedWindow.
	gnomeObjectPath dbus.ObjectPath = "/org/neru/Shell"
	// gnomeInterface is the extension's D-Bus interface.
	gnomeInterface = "org.neru.Shell"
	// gnomeFocusedMethod answers with the focused window's state.
	gnomeFocusedMethod = gnomeInterface + ".FocusedWindow"

	// gnomeQueryTimeout bounds the bridge's one synchronous round trip. It is
	// longer than the CLI timeout because the shell is a resident process, not a
	// freshly spawned one.
	gnomeQueryTimeout = 2 * time.Second
)

// errGNOMEAbsent is reported when the extension is not on the bus. It is
// CodeNotSupported because nothing is broken: the session simply has no window
// source, and the caller should fall back.
func errGNOMEAbsent(err error) error {
	return errs.Wrap(
		err,
		errs.CodeNotSupported,
		"the Neru GNOME Shell extension is not running (org.neru.Shell is not on "+
			"the session bus), so GNOME cannot report the focused window",
	)
}

// gnomeSource answers GNOME's questions through the extension.
type gnomeSource struct {
	// query is the bridge query. It is a field so the accept-or-reject rules can
	// be tested without GNOME Shell.
	query func(ctx context.Context) (Window, bool, error)

	// mu guards conn.
	mu   sync.Mutex
	conn *dbus.Conn
}

// newGNOMESource returns a source that queries the extension.
func newGNOMESource() *gnomeSource {
	source := &gnomeSource{}
	source.query = source.focusedFromExtension

	return source
}

// focused reports the extension's focused window.
func (s *gnomeSource) focused(ctx context.Context) (Window, bool, error) {
	return s.queryWindow(ctx)
}

// origin returns the extension's rectangle as the frame's origin, accepting it
// only when it still describes the same window.
func (s *gnomeSource) origin(ctx context.Context, frame Window) (image.Point, bool, error) {
	window, found, err := s.queryWindow(ctx)
	if err != nil || !found {
		return image.Point{}, false, err
	}

	origin, ok := gnomeOrigin(window, frame)

	return origin, ok, nil
}

// queryWindow runs the configured query. It falls back to the extension query
// when a source was built without one, so a zero value cannot panic.
func (s *gnomeSource) queryWindow(ctx context.Context) (Window, bool, error) {
	if s.query == nil {
		return s.focusedFromExtension(ctx)
	}

	return s.query(ctx)
}

// close releases the source's session-bus connection.
func (s *gnomeSource) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return nil
	}

	conn := s.conn
	s.conn = nil

	return conn.Close()
}

// focusedFromExtension asks the extension for the focused window.
func (s *gnomeSource) focusedFromExtension(ctx context.Context) (Window, bool, error) {
	conn, err := s.connection()
	if err != nil {
		return Window{}, false, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the Neru GNOME Shell extension needs a D-Bus session bus",
		)
	}

	callCtx, cancel := context.WithTimeout(ctx, gnomeQueryTimeout)
	defer cancel()

	var (
		found                  bool
		appID, title           string
		posX, posY, width, hgt int32
	)

	err = conn.Object(gnomeBusName, gnomeObjectPath).
		CallWithContext(callCtx, gnomeFocusedMethod, 0).
		Store(&found, &appID, &title, &posX, &posY, &width, &hgt)
	if err != nil {
		if isServiceUnknown(err) {
			return Window{}, false, errGNOMEAbsent(err)
		}

		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Window{}, false, errs.Wrapf(
				err,
				errs.CodeTimeout,
				"the Neru GNOME Shell extension did not answer %s before the query deadline",
				gnomeFocusedMethod,
			)
		}

		return Window{}, false, errs.Wrapf(
			err,
			errs.CodeAccessibilityFailed,
			"asking the Neru GNOME Shell extension (%s) failed",
			gnomeFocusedMethod,
		)
	}

	if !found {
		return Window{}, false, nil
	}

	return gnomeWindow(appID, title, posX, posY, width, hgt), true, nil
}

// connection returns the source's session-bus connection, dialing one when there
// is none or the last was closed under it.
func (s *gnomeSource) connection() (*dbus.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil && s.conn.Connected() {
		return s.conn, nil
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}

	s.conn = conn

	return conn, nil
}

// gnomeWindow builds a window from the extension's reply. A reply with no area
// is still an answer about a window, so the identity survives and Rect is empty.
func gnomeWindow(appID, title string, posX, posY, width, height int32) Window {
	window := Window{AppID: appID, Title: title}

	if width > 0 && height > 0 {
		window.Rect = image.Rect(
			int(posX), int(posY), int(posX)+int(width), int(posY)+int(height),
		)
	}

	return window
}

// isServiceUnknown reports whether a failed D-Bus call means the well-known name
// has no owner, which is the extension not running rather than a broken call.
func isServiceUnknown(err error) bool {
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		return false
	}

	switch dbusErr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner":
		return true
	default:
		return false
	}
}
