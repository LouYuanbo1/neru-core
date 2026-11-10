//go:build linux

package libei

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The org.freedesktop.portal.RemoteDesktop handshake over the session bus.
//
// The portal is the only way a Wayland client can ask its compositor for an
// input-injection channel on the compositors that implement no
// virtual-pointer protocol (KWin, Mutter, cosmic-comp): the portal shows a
// consent prompt, creates a session, and hands back a connected EIS socket that
// libei then speaks over (see client.go).
//
// The four calls run in order and each one's result is delivered differently:
//
//	CreateSession(options)                           -> Request, then the
//	                                                    session handle in the
//	                                                    Response results
//	SelectDevices(session, options)                  -> Request
//	Start(session, parent_window, options)           -> Request
//	ConnectToEIS(session, options)                   -> the fd in the method
//	                                                    reply, no Request
//
// The bus connection is private, never dbus.SessionBus(): the portal ends a
// session when its connection closes, and the connection has to be one nothing
// else owns.

const (
	// portalBusName is the well-known name xdg-desktop-portal owns.
	portalBusName = "org.freedesktop.portal.Desktop"
	// portalObjectPath is the object every portal interface is exported on.
	portalObjectPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	// portalRequestInterface carries the Response signal a Request answers with.
	portalRequestInterface = "org.freedesktop.portal.Request"
	// portalResponseSignal is that signal's member name.
	portalResponseSignal = "Response"
	// portalRequestNamespace is the path namespace every Request lives under.
	portalRequestNamespace = dbus.ObjectPath("/org/freedesktop/portal/desktop/request")
	// portalSessionCloseMethod ends a session explicitly.
	portalSessionCloseMethod = "org.freedesktop.portal.Session.Close"

	// The four RemoteDesktop methods one session needs, in the order they run.
	methodCreateSession = "org.freedesktop.portal.RemoteDesktop.CreateSession"
	methodSelectDevices = "org.freedesktop.portal.RemoteDesktop.SelectDevices"
	methodStart         = "org.freedesktop.portal.RemoteDesktop.Start"
	methodConnectToEIS  = "org.freedesktop.portal.RemoteDesktop.ConnectToEIS"

	// noCallFlags sends a plain method call. The reply is what carries the
	// Request handle, so NO_REPLY_EXPECTED must not be set.
	noCallFlags = 0

	// noParentWindow is Start's parent_window. This library draws no toplevel
	// of its own, so the portal places the dialog itself.
	noParentWindow = ""

	// signalBuffer sizes the Response channel, one per call in the handshake
	// with room to spare. godbus defers rather than blocks when it is full.
	signalBuffer = 4
)

// portalSession is one established RemoteDesktop grant: the EIS socket libei
// injects through, and the function that ends the session and its connection.
type portalSession struct {
	// eisFD is the file descriptor of the EIS socket. Ownership passes to the
	// caller, which hands it to libei.
	eisFD int
	// close ends the portal session and its D-Bus connection. It leaves the EIS
	// fd alone, because libei owns it.
	close func()
}

// openPortalSession runs the handshake and returns the session.
//
// Every step is bounded by ctx, including the bus dial. A failure closes what
// was opened, so a refused dialog does not leave a half-open session for the
// portal to time out. The deferred connection close is registered before the
// reply subscription is released, so the subscription goes first (defers run in
// reverse order) and a failed handshake never removes a match from an already
// closed connection.
func openPortalSession(ctx context.Context) (portalSession, error) {
	conn, err := dialSessionBus(ctx)
	if err != nil {
		return portalSession{}, err
	}

	established := false

	defer func() {
		// On success the connection belongs to the returned session, whose close
		// function ends it.
		if !established {
			_ = conn.Close()
		}
	}()

	requester, release, err := newPortalRequester(ctx, conn)
	if err != nil {
		return portalSession{}, err
	}

	defer release()

	session, err := createRemoteDesktopSession(ctx, requester)
	if err != nil {
		return portalSession{}, err
	}

	// A session exists from here on, so every failure has to close it too, or the
	// portal keeps a grant nobody will use.
	closeDetached := func(cause error) (portalSession, error) {
		closePortalSession(conn, session)

		return portalSession{}, cause
	}

	if err := selectRemoteDesktopDevices(ctx, requester, session); err != nil {
		return closeDetached(err)
	}

	if err := startRemoteDesktopSession(ctx, requester, session); err != nil {
		return closeDetached(err)
	}

	eisFD, err := connectToEIS(ctx, requester, session)
	if err != nil {
		return closeDetached(err)
	}

	established = true

	return portalSession{
		eisFD: eisFD,
		close: func() {
			closePortalSession(conn, session)
			_ = conn.Close()
		},
	}, nil
}

// createRemoteDesktopSession performs step 1 and returns the session handle.
func createRemoteDesktopSession(ctx context.Context, requester *portalRequester) (dbus.ObjectPath, error) {
	results, err := requester.call(ctx, methodCreateSession,
		createSessionOptions(handleToken()))
	if err != nil {
		return "", err
	}

	session, ok := sessionHandle(results)
	if !ok {
		return "", errs.New(errs.CodeAccessibilityFailed,
			"the RemoteDesktop portal created a session but named no session handle")
	}

	return session, nil
}

// selectRemoteDesktopDevices performs step 2: the pointer and keyboard the
// session should carry.
func selectRemoteDesktopDevices(
	ctx context.Context,
	requester *portalRequester,
	session dbus.ObjectPath,
) error {
	_, err := requester.call(ctx, methodSelectDevices, selectDevicesOptions(), session)

	return err
}

// startRemoteDesktopSession performs step 3, the call that shows the consent
// dialog when the portal has nothing to restore.
func startRemoteDesktopSession(
	ctx context.Context,
	requester *portalRequester,
	session dbus.ObjectPath,
) error {
	_, err := requester.call(ctx, methodStart,
		map[string]dbus.Variant{}, session, noParentWindow)

	return err
}

// connectToEIS performs step 4, a plain method call that returns the socket
// directly rather than through a Request. The caller owns the returned fd.
func connectToEIS(
	ctx context.Context,
	requester *portalRequester,
	session dbus.ObjectPath,
) (int, error) {
	var socket dbus.UnixFD

	err := requester.portal.CallWithContext(
		ctx,
		methodConnectToEIS,
		noCallFlags,
		session,
		map[string]dbus.Variant{},
	).Store(&socket)
	if err != nil {
		return -1, portalFailure(err,
			"the RemoteDesktop portal granted a session but would not hand over "+
				"an EIS socket to inject through")
	}

	if socket < 0 {
		return -1, errs.New(errs.CodeAccessibilityFailed,
			"the RemoteDesktop portal returned an invalid EIS socket")
	}

	return int(socket), nil
}

// dialSessionBus connects to the session bus without letting an unresponsive
// bus hold the caller: dbus.ConnectSessionBus takes no context, so the dial runs
// on a goroutine the caller can abandon. An abandoned dial closes whatever
// connection it eventually produces, so a wedged bus costs no connection.
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
	case <-ctx.Done():
		go func() {
			if result := <-done; result.conn != nil {
				_ = result.conn.Close()
			}
		}()

		return nil, errs.Wrapf(ctx.Err(), errs.CodeTimeout,
			"the D-Bus session bus did not accept a connection in time")
	case result := <-done:
		if result.err != nil {
			return nil, errs.Wrapf(result.err, errs.CodeNotSupported,
				"no reachable D-Bus session bus, so the RemoteDesktop portal "+
					"cannot be asked for an input session")
		}

		return result.conn, nil
	}
}

// portalRequester issues portal calls whose answer arrives as a Request's
// Response signal rather than in the method reply.
type portalRequester struct {
	conn    *dbus.Conn
	portal  dbus.BusObject
	sender  string
	signals chan *dbus.Signal
}

// newPortalRequester subscribes to the Response signals this connection's
// portal requests answer on, and returns the requester plus a release function.
// Subscribing before any call goes out closes the race the portal specification
// warns of, where a Response beats the method reply.
func newPortalRequester(ctx context.Context, conn *dbus.Conn) (*portalRequester, func(), error) {
	names := conn.Names()
	if len(names) == 0 {
		return nil, nil, errs.New(errs.CodeAccessibilityFailed,
			"the D-Bus session bus assigned no unique name, so no portal "+
				"request could be listened for")
	}

	requester := &portalRequester{
		conn:    conn,
		portal:  conn.Object(portalBusName, portalObjectPath),
		sender:  names[0],
		signals: make(chan *dbus.Signal, signalBuffer),
	}

	conn.Signal(requester.signals)

	// One match rule covers every Request-answering call, because every Request
	// the portal creates for this connection lives under one path namespace.
	err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(portalRequestInterface),
		dbus.WithMatchMember(portalResponseSignal),
		dbus.WithMatchPathNamespace(portalRequestNamespace),
	)
	if err != nil {
		conn.RemoveSignal(requester.signals)

		return nil, nil, errs.Wrapf(err, errs.CodeAccessibilityFailed,
			"could not subscribe to the RemoteDesktop portal's replies")
	}

	release := func() {
		_ = conn.RemoveMatchSignal(
			dbus.WithMatchInterface(portalRequestInterface),
			dbus.WithMatchMember(portalResponseSignal),
			dbus.WithMatchPathNamespace(portalRequestNamespace),
		)

		conn.RemoveSignal(requester.signals)
	}

	return requester, release, nil
}

// call makes one portal call and waits for its Request to answer. It mints the
// handle token itself, because the answer's path derives from it and a
// mismatched token would wait on a path nothing answers.
func (r *portalRequester) call(
	ctx context.Context,
	method string,
	options map[string]dbus.Variant,
	args ...any,
) (map[string]dbus.Variant, error) {
	token := handleToken()
	options = withHandleToken(options, token)

	var handle dbus.ObjectPath

	err := r.portal.CallWithContext(
		ctx,
		method,
		noCallFlags,
		append(append([]any{}, args...), options)...,
	).Store(&handle)
	if err != nil {
		return nil, portalFailure(err, "the RemoteDesktop portal refused "+method)
	}

	// The returned handle is authoritative; the derived path is only a fallback
	// for a portal that returns none.
	if handle == "" {
		handle = requestPath(r.sender, token)
	}

	return r.await(ctx, handle, method)
}

// await blocks until the Request at handle answers, or ctx runs out.
func (r *portalRequester) await(
	ctx context.Context,
	handle dbus.ObjectPath,
	method string,
) (map[string]dbus.Variant, error) {
	responseName := portalRequestInterface + "." + portalResponseSignal

	for {
		select {
		case <-ctx.Done():
			return nil, errs.Wrapf(ctx.Err(), errs.CodeTimeout,
				"the RemoteDesktop portal did not answer %s in time", method)
		case signal, open := <-r.signals:
			if !open {
				return nil, errs.Newf(errs.CodeAccessibilityFailed,
					"the D-Bus session bus closed while waiting for %s", method)
			}

			if signal.Path != handle || signal.Name != responseName {
				continue
			}

			code, results, err := decodeResponse(signal.Body)
			if err != nil {
				return nil, err
			}

			switch code {
			case responseSuccess:
				return results, nil
			case responseCanceled:
				return nil, errs.New(errs.CodeAccessibilityDenied,
					"the RemoteDesktop consent prompt was canceled, so Wayland "+
						"input injection has no session; approve the prompt to "+
						"allow it")
			case responseEnded:
				return nil, errs.New(errs.CodeAccessibilityFailed,
					"the RemoteDesktop portal ended the request before it was answered")
			default:
				return nil, errs.Newf(errs.CodeAccessibilityFailed,
					"the RemoteDesktop portal answered with unknown response code %d", code)
			}
		}
	}
}

// portalFailure phrases a failed portal call: a timeout is not a refusal, a
// missing method means the installed portal is too old, and NotAllowed is the
// user or the desktop policy saying no.
func portalFailure(err error, message string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errs.Wrap(err, errs.CodeTimeout,
			message+", and the request ran out of time")
	}

	switch busErrorName(err) {
	case "org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.InvalidArgs":
		return errs.Wrapf(err, errs.CodeNotSupported,
			"%s: the installed xdg-desktop-portal does not implement it "+
				"(ConnectToEIS needs xdg-desktop-portal 1.18 or later)", message)
	case "org.freedesktop.portal.Error.NotAllowed":
		return errs.Wrap(err, errs.CodeAccessibilityDenied, message)
	default:
		return errs.Wrap(err, errs.CodeAccessibilityFailed, message)
	}
}

// busErrorName reduces a D-Bus error to its name, and reports "" for anything
// that is not one. godbus yields dbus.Error by value on a client call and by
// pointer from NewError, so both are matched.
func busErrorName(err error) string {
	var busError dbus.Error

	var busErrorPtr *dbus.Error

	switch {
	case errors.As(err, &busError):
		return busError.Name
	case errors.As(err, &busErrorPtr):
		return busErrorPtr.Name
	default:
		return ""
	}
}

// closePortalSession ends a session explicitly rather than waiting for the
// portal to notice the connection close, until which the user would see a held
// grant. It expects no reply, because teardown can run on a goroutine holding a
// keyboard grab where waiting on a wedged portal would freeze hotkeys.
func closePortalSession(conn *dbus.Conn, session dbus.ObjectPath) {
	_ = conn.Object(portalBusName, session).
		Call(portalSessionCloseMethod, dbus.FlagNoReplyExpected).Err
}
