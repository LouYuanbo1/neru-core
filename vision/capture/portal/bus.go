//go:build linux

package portal

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// noCallFlags is the flags argument of every call this package makes: none of
// the portal's methods is a property, an async reply or an auto-start opt-out.
const noCallFlags dbus.Flags = 0

// The D-Bus properties interface, which is how a portal interface announces
// itself: every portal interface carries a read-only version property, and
// reading it fails when the interface does not exist.
const (
	propertiesInterface = "org.freedesktop.DBus.Properties"
	propertiesGetMethod = propertiesInterface + ".Get"
	versionProperty     = "version"
)

// portalMissing is the sentence a missing portal is reported with. It names the
// interface and the service, so a caller reading the error knows which
// component is absent instead of guessing at the compositor, and says what
// installs it because on a bare session nothing does.
const portalMissing = "the screenshot portal (org.freedesktop.portal.Screenshot " +
	"on org.freedesktop.portal.Desktop) is not available on this session's " +
	"D-Bus; this desktop needs xdg-desktop-portal installed and running"

// dialSessionBus opens a private connection to the session bus, bounded by ctx.
//
// godbus's dial is not cancellable: connecting, the auth handshake and the Hello
// call all read and write without a deadline, so the dial runs on its own
// goroutine and ctx decides how long the caller waits. An abandoned dial closes
// the connection it eventually produced, so a wedged bus costs one goroutine
// rather than a leaked connection. The result channel is buffered for the same
// reason: a dial that finishes after the caller gave up must not block.
//
// Every capture makes its own connection, so a dead connection from a previous
// request cannot fail this one, and closing it releases the portal's Request
// objects with it.
func dialSessionBus(ctx context.Context) (*dbus.Conn, error) {
	type dialResult struct {
		conn *dbus.Conn
		err  error
	}

	done := make(chan dialResult, 1)

	go func() {
		conn, err := dbus.ConnectSessionBus()
		done <- dialResult{conn: conn, err: err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			return nil, errs.Wrap(
				result.err,
				errs.CodeNotSupported,
				"screen capture through the screenshot portal needs a session D-Bus, "+
					"and connecting to it failed",
			)
		}

		return result.conn, nil
	case <-ctx.Done():
		go func() {
			if result := <-done; result.conn != nil {
				_ = result.conn.Close()
			}
		}()

		return nil, waitError(ctx)
	}
}

// screenshotsSupported reports whether the portal exports the Screenshot
// interface, by reading the version property the interface carries. The read
// also starts the portal service on a desktop that activates it on demand, so a
// successful first probe on a cold session is expected.
//
// Any error means the interface is not usable for this process, so the answer
// names the portal as missing rather than reporting a transport failure: a
// capability check wants a reason to fall back from, and a portal that cannot
// answer a property read cannot take a screenshot either.
func screenshotsSupported(ctx context.Context, conn *dbus.Conn) error {
	var version dbus.Variant

	err := conn.Object(portalBusName, portalObjectPath).
		CallWithContext(ctx, propertiesGetMethod, noCallFlags, screenshotInterface, versionProperty).
		Store(&version)
	if err != nil {
		return errs.Wrap(err, errs.CodeNotSupported, portalMissing)
	}

	if _, isVersion := version.Value().(uint32); !isVersion {
		return errs.Newf(
			errs.CodeNotSupported,
			"%s: its %s property answered with a %T",
			portalMissing,
			versionProperty,
			version.Value(),
		)
	}

	return nil
}

// portalCallError maps a D-Bus error from the portal onto the shared error
// vocabulary. A missing service, object, interface, method or property all mean
// the same thing here: this session's portal does not implement the Screenshot
// interface, so the caller must fall back instead of retrying. A portal that
// stopped answering is a timeout, and anything else is this package's own
// failure to talk to a portal that exists.
func portalCallError(err error, what string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, what+": the portal did not answer in time")
	}

	switch dbusErrorName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownProperty":
		return errs.Wrap(err, errs.CodeNotSupported, what+": "+portalMissing)
	case "org.freedesktop.DBus.Error.NoReply",
		"org.freedesktop.DBus.Error.Timeout",
		"org.freedesktop.DBus.Error.TimedOut":
		return errs.Wrap(err, errs.CodeTimeout, what+": the portal did not answer in time")
	default:
		return errs.Wrap(err, errs.CodeInternal, what)
	}
}

// dbusErrorName returns the D-Bus error name a call failed with, or an empty
// string when the failure was not a D-Bus error (a broken connection, a
// canceled context, a decoding fault).
func dbusErrorName(err error) string {
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name
	}

	var dbusErrPtr *dbus.Error
	if errors.As(err, &dbusErrPtr) && dbusErrPtr != nil {
		return dbusErrPtr.Name
	}

	return ""
}
