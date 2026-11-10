//go:build linux

package portal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The one portal object this package speaks to, and the two interfaces it
// needs: Screenshot for the request and Request for the answer. The Request
// interface has no methods; its Response signal is the whole of it.
const (
	portalBusName       = "org.freedesktop.portal.Desktop"
	portalObjectPath    = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	screenshotInterface = "org.freedesktop.portal.Screenshot"
	screenshotMethod    = screenshotInterface + ".Screenshot"
	requestInterface    = "org.freedesktop.portal.Request"
	responseMember      = "Response"
	responseSignalName  = requestInterface + "." + responseMember
)

// The Request objects a portal exports all live under this prefix, followed by
// the caller's bus name and the handle token. Answering by prefix rather than
// by one exact path is what lets this package accept a handle that differs from
// the one its token predicts, as old backends did.
const requestPathPrefix = "/org/freedesktop/portal/desktop/request/"

// The three Screenshot options this package sets, out of the interface's
// vocabulary.
const (
	// optionHandleToken carries the token the Request object's path is derived
	// from. The interface requires it to match [A-Za-z0-9_].
	optionHandleToken = "handle_token"
	// optionInteractive asks for the desktop's own area picker. It is false
	// here: a picker returns a picture of whatever the user dragged out, with
	// nothing saying which part of the desktop that was, and the caller's
	// region is cropped locally from the whole desktop instead.
	optionInteractive = "interactive"
	// optionModal makes the dialog modal to the parent window. It is false
	// because this package passes no parent window, so a modal dialog would
	// block dialogs the session itself is showing.
	optionModal = "modal"
)

// resultURIKey is the key a Screenshot result carries the picture's location
// under. The location is a file URI; FilePath turns it into a path.
const resultURIKey = "uri"

// requestTokenPrefix tags every handle token this package generates, so a
// request visible in a portal's logs can be traced to this library rather than
// to the desktop's own tools.
const requestTokenPrefix = "neru_core_"

// noParentWindow is the empty parent_window the interface accepts from a caller
// that has no window to parent a dialog to. A real one would be an X11 window
// id or a Wayland surface handle, neither of which this library has.
const noParentWindow = ""

// signalBuffer is how many signals a capture's channel holds. One Response per
// request is expected, and the connection's own NameAcquired arrives on the
// same channel because godbus delivers every signal to every registered
// channel, so a small reserve keeps godbus from spawning a goroutine per
// delivery.
const signalBuffer = 8

// requestCounter numbers this process's requests, so two captures on two
// connections cannot derive the same Request path.
var requestCounter atomic.Uint64

// nextRequestToken builds a handle token unique to this process and request.
func nextRequestToken() string {
	return fmt.Sprintf("%s%d_%d", requestTokenPrefix, os.Getpid(), requestCounter.Add(1))
}

// screenshotOptions builds the options dictionary for Screenshot. There is no
// area option in the interface, which is why a region capture runs the same
// whole-desktop request and crops.
func screenshotOptions(token string) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		optionHandleToken: dbus.MakeVariant(token),
		optionInteractive: dbus.MakeVariant(false),
		optionModal:       dbus.MakeVariant(false),
	}
}

// requestHandle derives the object path the portal will export for a request,
// as the interface documents it: the sender's unique bus name with its leading
// colon removed and its dots replaced by underscores, then the token.
func requestHandle(sender, token string) dbus.ObjectPath {
	return dbus.ObjectPath(requestNamespace(sender) + token)
}

// requestNamespace is the Request path prefix this connection's requests live
// under.
func requestNamespace(sender string) string {
	return requestPathPrefix + senderToken(sender) + "/"
}

// senderToken is the bus-name fragment a request path carries: the unique name
// ":1.42" becomes "1_42".
func senderToken(sender string) string {
	return strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
}

// uniqueName returns this connection's own unique bus name, which a portal
// request path is derived from. godbus lists it among the connection's names
// and only the unique one starts with a colon.
func uniqueName(conn *dbus.Conn) (string, error) {
	for _, name := range conn.Names() {
		if strings.HasPrefix(name, ":") {
			return name, nil
		}
	}

	return "", errs.New(
		errs.CodeInternal,
		"the session bus did not name this connection, so a portal request handle "+
			"could not be derived",
	)
}

// callScreenshot starts the request and returns the Request object's path. The
// portal may answer immediately, which is why the caller subscribes before this
// runs and holds the answer on a channel until it does.
func callScreenshot(ctx context.Context, conn *dbus.Conn, token string) (dbus.ObjectPath, error) {
	var handle dbus.ObjectPath

	err := conn.Object(portalBusName, portalObjectPath).
		CallWithContext(ctx, screenshotMethod, noCallFlags, noParentWindow, screenshotOptions(token)).
		Store(&handle)
	if err != nil {
		return "", portalCallError(
			err,
			"could not ask the screenshot portal for a screenshot",
		)
	}

	return handle, nil
}

// awaitResponse waits for the Response signal of this connection's request and
// returns the results it carries.
//
// A signal counts when its path is the handle the portal returned, the handle
// the token predicted, or any other request path under this connection's
// namespace: backends have answered on either of the two, and the namespace
// matches both without knowing which one this portal chose. Only one request
// exists per capture, because every capture opens its own connection, so no
// other request can arrive under that namespace while this one waits.
//
// The wait ends with ctx, which grab has already bounded, and reports the
// caller's cancellation or the budget running out in the shared vocabulary.
func awaitResponse(
	ctx context.Context,
	signals <-chan *dbus.Signal,
	namespace string,
	handles []dbus.ObjectPath,
) (map[string]dbus.Variant, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, waitError(ctx)
		case signal, open := <-signals:
			if !open {
				return nil, errs.New(
					errs.CodeInternal,
					"the D-Bus connection closed before the screenshot portal answered",
				)
			}

			if !isResponseSignal(signal, namespace, handles) {
				continue
			}

			return decodeResponse(signal.Body)
		}
	}
}

// isResponseSignal reports whether a signal is this request's answer.
func isResponseSignal(signal *dbus.Signal, namespace string, handles []dbus.ObjectPath) bool {
	if signal == nil || signal.Name != responseSignalName {
		return false
	}

	if strings.HasPrefix(string(signal.Path), namespace) {
		return true
	}

	for _, handle := range handles {
		if signal.Path == handle {
			return true
		}
	}

	return false
}

// decodeResponse reads the (u response, a{sv} results) body of a Response
// signal, refusing a code that means no screenshot was taken.
func decodeResponse(body []any) (map[string]dbus.Variant, error) {
	var (
		code    uint32
		results map[string]dbus.Variant
	)

	err := dbus.Store(body, &code, &results)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeInternal,
			"the screenshot portal answered with a Response this package could not read",
		)
	}

	if err := responseError(code); err != nil {
		return nil, err
	}

	return results, nil
}

// resultFilePath reads the uri out of a Screenshot result and turns it into the
// local path to decode.
func resultFilePath(results map[string]dbus.Variant) (string, error) {
	value, present := results[resultURIKey]
	if !present {
		return "", errs.New(
			errs.CodeInternal,
			"the screenshot portal answered without a uri for the picture it took",
		)
	}

	uri, isString := value.Value().(string)
	if !isString || uri == "" {
		return "", errs.Newf(
			errs.CodeInternal,
			"the screenshot portal answered with a %T where a uri belongs",
			value.Value(),
		)
	}

	return FilePath(uri)
}
