//go:build linux

package kwin

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The bus names KWin answers on. The interface is registered under KWin's own
// object path, and the compositor owns both names on one connection, so either
// reaches the same object: current releases export it under the dedicated
// org.kde.KWin.ScreenShot2 name, and KWin's main org.kde.KWin name is probed as
// well because a release or a packaging that does not register the dedicated
// service still answers there. The dedicated name is probed first because it is
// the one the interface documents.
var kwinServices = []string{
	"org.kde.KWin.ScreenShot2",
	"org.kde.KWin",
}

// screenShotPath is the object the interface lives at.
const screenShotPath = dbus.ObjectPath(screenShotObjectPath)

// noCallFlags is the flags argument of every call this package makes: none of
// ScreenShot2's methods is a property, an async reply or an auto-start opt-out.
const noCallFlags dbus.Flags = 0

// The options this package sets, out of ScreenShot2's vocabulary. Every one of
// them is optional, and a KWin that predates an option ignores it rather than
// failing the call, which is why they can be sent unconditionally.
const (
	// optionIncludeCursor keeps the pointer out of the frame: a cursor drawn
	// over a window is not part of what the window shows, and a detector
	// reading the pixels would see it as content.
	optionIncludeCursor = "include-cursor"
	// optionNativeResolution asks for physical pixels instead of KWin's
	// logical-size render, so a scaled output returns the detail the screen
	// actually has and the metadata's scale describes the ratio.
	optionNativeResolution = "native-resolution"
	// optionHideCallerWindows keeps KWin from hiding windows that belong to the
	// calling process. KWin 6 hides them by default so a screenshot tool does
	// not photograph its own dialog; this package is not a screenshot tool, and
	// silently removing a caller's windows from the frame would make the pixels
	// disagree with what is on the screen. KWin 5 ignores the option.
	optionHideCallerWindows = "hide-caller-windows"
)

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
// request cannot fail this one. godbus negotiates Unix file descriptor passing
// during the auth handshake, which is what lets the pipe travel over this
// connection.
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
				"screen capture through KWin's ScreenShot2 needs a session D-Bus, "+
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

// introspect reads one object's introspection document.
func introspect(ctx context.Context, conn *dbus.Conn, service string) ([]byte, error) {
	var document string

	err := conn.Object(service, screenShotPath).
		CallWithContext(ctx, introspectInterface+".Introspect", noCallFlags).
		Store(&document)
	if err != nil {
		return nil, kwinError(err, "could not ask KWin about its ScreenShot2 interface")
	}

	return []byte(document), nil
}

// screenShotService finds the bus name KWin exports ScreenShot2 on and reads the
// interface it offers.
//
// The dedicated name is probed first; a missing service there is not an error, it
// is the answer of a KWin that only owns its main name. A transport failure on
// the first name is not retried on the second, because a bus that failed to
// deliver a call will fail the next one too and the caller should see the
// transport fault rather than an invented "not supported".
func screenShotService(ctx context.Context, conn *dbus.Conn) (string, screenShot2, error) {
	var missing error

	for _, service := range kwinServices {
		document, err := introspect(ctx, conn, service)
		if err != nil {
			if errs.IsCode(err, errs.CodeNotSupported) {
				missing = err

				continue
			}

			return "", screenShot2{}, err
		}

		interfaceDescription, err := parseIntrospection(document)
		if err != nil {
			missing = err

			continue
		}

		return service, interfaceDescription, nil
	}

	if missing == nil {
		missing = errs.New(errs.CodeNotSupported, kwinMissing)
	}

	return "", screenShot2{}, missing
}

// screenshotOptions builds the options dictionary every capture sends.
func screenshotOptions() map[string]dbus.Variant {
	return map[string]dbus.Variant{
		optionIncludeCursor:     dbus.MakeVariant(false),
		optionNativeResolution:  dbus.MakeVariant(true),
		optionHideCallerWindows: dbus.MakeVariant(false),
	}
}

// kwinError maps a D-Bus error returned by KWin onto the shared error vocabulary,
// with the mapping itself in kwinCallError so it can be tested without a bus.
func kwinError(err error, what string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, what+": KWin did not answer in time")
	}

	return kwinCallError(dbusErrorName(err), err, what)
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
