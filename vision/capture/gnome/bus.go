//go:build linux

package gnome

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The service GNOME Shell registers, and the object the screenshot interface
// lives at.
const shellBusName = "org.gnome.Shell"

// screenshotPath is the object the interface lives at.
const screenshotPath = dbus.ObjectPath(screenshotObjectPath)

// screenshotMethod is the whole-screen method this package calls.
const screenshotMethod = screenshotInterface + "." + methodScreenshot

// noCallFlags is the flags argument of every call this package makes: the
// screenshot is not a property and an asynchronous reply would arrive on a
// connection this package is about to close.
const noCallFlags dbus.Flags = 0

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
// request cannot fail this one.
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
				"screen capture through GNOME Shell's screenshot interface needs a "+
					"session D-Bus, and connecting to it failed",
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

// requireScreenshot reports whether the shell exports the whole-screen method
// this package calls, by reading the object's introspection document.
//
// Any error means the interface is not usable for this process, so the answer
// names the interface as missing rather than reporting a transport failure: a
// capability check wants a reason to fall back from, and a shell that cannot
// answer an introspection read cannot take a screenshot either.
func requireScreenshot(ctx context.Context, conn *dbus.Conn) error {
	var document string

	err := conn.Object(shellBusName, screenshotPath).
		CallWithContext(ctx, introspectInterface+".Introspect", noCallFlags).
		Store(&document)
	if err != nil {
		return shellError(err, "could not ask GNOME Shell about its screenshot interface")
	}

	iface, err := parseIntrospection([]byte(document))
	if err != nil {
		return err
	}

	if !iface.takesScreenshots() {
		return errs.Newf(
			errs.CodeNotSupported,
			"GNOME Shell's %s interface does not export %s(b, b, s), so this shell "+
				"cannot take a whole-screen screenshot for this package",
			screenshotInterface,
			methodScreenshot,
		)
	}

	return nil
}

// callScreenshot asks the shell to write the whole desktop into filename and
// returns the filename the shell says it used.
//
// The shell answers with a boolean and a name rather than an error when it
// declines, which is why the boolean is checked here rather than left to the
// caller: a false answer means no file was written, and the caller must not read
// whatever happened to be at that path.
func callScreenshot(ctx context.Context, conn *dbus.Conn, filename string) (string, error) {
	var (
		success bool
		used    string
	)

	err := conn.Object(shellBusName, screenshotPath).
		CallWithContext(ctx, screenshotMethod, noCallFlags, false, false, filename).
		Store(&success, &used)
	if err != nil {
		return "", shellError(err, "could not ask GNOME Shell for a screenshot")
	}

	if !success {
		return "", errs.New(errs.CodeAccessibilityDenied, shellDenied)
	}

	return used, nil
}

// shellError maps a D-Bus error returned by the shell onto the shared error
// vocabulary, with the mapping itself in shellCallError so it can be tested
// without a bus.
func shellError(err error, what string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, what+": GNOME Shell did not answer in time")
	}

	return shellCallError(dbusErrorName(err), err, what)
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
