package libei

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The RemoteDesktop portal's option dictionaries and Request bookkeeping, kept
// free of build tags so their shape can be tested without a session bus.
//
// A portal call that may show a dialog does not answer with its result in the
// method reply. The reply carries the object path of a Request, and the result
// arrives later as that Request's Response signal. Two option keys take part in
// that exchange, and both are built here:
//
//   - handle_token names the Request, so its path is known before the signal
//     can arrive and a listener can be in place first.
//   - session_handle_token names the session CreateSession creates, so the
//     handle in the reply can be checked against the one we asked for.
//
// The options deliberately carry no restore token and no persist_mode. A token
// is a credential that has to be stored and re-presented to avoid the consent
// dialog, and this package keeps no state on disk, so it asks for a session
// that ends with the process rather than for a grant the user would have to
// revoke by hand later. That costs one consent dialog per daemon start, which
// is the same trade the liboeffis fallback in the reference implementation
// makes.

const (
	// remoteDesktopDeviceKeyboard and remoteDesktopDevicePointer are the
	// RemoteDesktop `types` bitmask. The keyboard is asked for because a
	// modified scroll and a held modifier are key events; a portal is free to
	// grant only the pointer, and the client then reports keyboard injection as
	// unsupported rather than pretending.
	remoteDesktopDeviceKeyboard uint32 = 1
	remoteDesktopDevicePointer  uint32 = 2

	// responseSuccess, responseCanceled and responseEnded are the codes a
	// Request's Response signal carries. Cancel is the user's own answer and
	// stays a separate error, because a second prompt would only repeat it.
	responseSuccess  uint32 = 0
	responseCanceled uint32 = 1
	responseEnded    uint32 = 2

	// handleTokenKey is the option that names one Request.
	handleTokenKey = "handle_token"
	// sessionHandleTokenKey is the CreateSession option that names the session.
	sessionHandleTokenKey = "session_handle_token"

	// portalRequestPathPrefix opens a Request's object path. The rest is our
	// unique bus name and the handle token.
	portalRequestPathPrefix = "/org/freedesktop/portal/desktop/request/"

	// handleTokenBytes is the entropy behind one token. It only has to be
	// unique among this connection's in-flight requests.
	handleTokenBytes = 16
)

// selectDevicesOptions builds the SelectDevices options.
//
// Only `types` is set. `persist_mode` is omitted on purpose: it would ask the
// portal to keep the grant after this process exits, and without a stored
// restore token the next start would neither reuse nor revoke it cleanly.
func selectDevicesOptions() map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"types": dbus.MakeVariant(remoteDesktopDeviceKeyboard | remoteDesktopDevicePointer),
	}
}

// createSessionOptions builds the CreateSession options.
func createSessionOptions(sessionToken string) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		sessionHandleTokenKey: dbus.MakeVariant(sessionToken),
	}
}

// withHandleToken adds the handle_token that ties a call to its Request path.
func withHandleToken(options map[string]dbus.Variant, token string) map[string]dbus.Variant {
	if options == nil {
		options = make(map[string]dbus.Variant)
	}

	options[handleTokenKey] = dbus.MakeVariant(token)

	return options
}

// handleToken returns a token unique among this connection's requests. The
// portal pastes it into an object path, so it stays within [A-Za-z0-9_].
func handleToken() string {
	buffer := make([]byte, handleTokenBytes)

	// crypto/rand.Read is documented never to fail on any supported platform.
	_, _ = rand.Read(buffer)

	return "neru_" + hex.EncodeToString(buffer)
}

// requestPath derives the object path a Request answers on, per the portal
// specification: the sender's unique name with its leading colon removed and
// its dots replaced by underscores, then the handle token.
func requestPath(sender, token string) dbus.ObjectPath {
	element := strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")

	return dbus.ObjectPath(portalRequestPathPrefix + element + "/" + token)
}

// decodeResponse reads the (response code, results) pair a Response signal
// carries.
func decodeResponse(body []any) (uint32, map[string]dbus.Variant, error) {
	const responseFields = 2

	if len(body) < responseFields {
		return 0, nil, errs.New(errs.CodeAccessibilityFailed,
			"the RemoteDesktop portal sent a malformed reply")
	}

	code, ok := body[0].(uint32)
	if !ok {
		return 0, nil, errs.New(errs.CodeAccessibilityFailed,
			"the RemoteDesktop portal sent a reply with no response code")
	}

	results, _ := body[1].(map[string]dbus.Variant)

	return code, results, nil
}

// sessionHandle reads a CreateSession reply's session handle. The specification
// types it as a string but some portals send an object path, so both pass.
func sessionHandle(results map[string]dbus.Variant) (dbus.ObjectPath, bool) {
	value, ok := results["session_handle"]
	if !ok {
		return "", false
	}

	switch handle := value.Value().(type) {
	case string:
		return dbus.ObjectPath(handle), handle != ""
	case dbus.ObjectPath:
		return handle, handle != ""
	default:
		return "", false
	}
}
