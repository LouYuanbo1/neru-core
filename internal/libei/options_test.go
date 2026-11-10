package libei

import (
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestSelectDevicesOptions_RequestsKeyboardAndPointer pins the RemoteDesktop
// `types` bitmask. The keyboard bit is what makes a modified scroll possible at
// all, and a wrong bit would silently grant a pointer-only session, so the value
// is checked against the specification rather than against the constants
// themselves.
func TestSelectDevicesOptions_RequestsKeyboardAndPointer(t *testing.T) {
	options := selectDevicesOptions()

	types, ok := options["types"].Value().(uint32)
	if !ok {
		t.Fatalf("types variant holds %T, want uint32", options["types"].Value())
	}

	const wantKeyboardOrPointer = 3
	if types != wantKeyboardOrPointer {
		t.Fatalf("types = %d, want %d (keyboard bit 1 | pointer bit 2)", types, wantKeyboardOrPointer)
	}

	// These belong to other calls or are deliberately omitted; a stray key here
	// would change what the portal grants or persists.
	for _, key := range []string{
		handleTokenKey,
		sessionHandleTokenKey,
		"persist_mode",
		"restore_token",
	} {
		if _, present := options[key]; present {
			t.Errorf("SelectDevices options carry %q, which they must not", key)
		}
	}
}

// TestCreateSessionOptions_NamesTheSession pins the session_handle_token, the
// option the portal turns into the session's object path.
func TestCreateSessionOptions_NamesTheSession(t *testing.T) {
	options := createSessionOptions("neru_session")

	token, ok := options[sessionHandleTokenKey].Value().(string)
	if !ok || token != "neru_session" {
		t.Fatalf("session_handle_token = %v, want %q", options[sessionHandleTokenKey].Value(), "neru_session")
	}
}

// TestWithHandleToken_AddsTheTokenAndKeepsTheOptions pins that the handle token
// is added without dropping the call's own options: SelectDevices carries
// `types` and still needs it after the token is attached.
func TestWithHandleToken_AddsTheTokenAndKeepsTheOptions(t *testing.T) {
	options := withHandleToken(selectDevicesOptions(), "neru_handle")

	token, ok := options[handleTokenKey].Value().(string)
	if !ok || token != "neru_handle" {
		t.Fatalf("handle_token = %v, want %q", options[handleTokenKey].Value(), "neru_handle")
	}

	if _, present := options["types"]; !present {
		t.Error("withHandleToken dropped the caller's own options")
	}
}

// TestWithHandleToken_AcceptsNilOptions pins that a call with no options of its
// own still gets a dictionary rather than a nil map panic: Start passes an empty
// one.
func TestWithHandleToken_AcceptsNilOptions(t *testing.T) {
	options := withHandleToken(nil, "neru_handle")

	if len(options) != 1 {
		t.Fatalf("options = %v, want only the handle token", options)
	}

	if _, present := options[handleTokenKey]; !present {
		t.Error("withHandleToken(nil) did not add the handle token")
	}
}

// TestRequestPath_DerivesThePortalPath pins the path rule from the portal
// specification: the sender's unique name without its leading colon, dots
// replaced by underscores, then the token. A portal that returns no handle
// leaves this path as the only way to find the answer.
func TestRequestPath_DerivesThePortalPath(t *testing.T) {
	tests := []struct {
		sender string
		token  string
		want   dbus.ObjectPath
	}{
		{
			sender: ":1.42",
			token:  "neru_abc",
			want:   "/org/freedesktop/portal/desktop/request/1_42/neru_abc",
		},
		{
			sender: ":1.7",
			token:  "neru_0123456789abcdef",
			want:   "/org/freedesktop/portal/desktop/request/1_7/neru_0123456789abcdef",
		},
	}

	for _, testCase := range tests {
		if got := requestPath(testCase.sender, testCase.token); got != testCase.want {
			t.Errorf("requestPath(%q, %q) = %q, want %q",
				testCase.sender, testCase.token, got, testCase.want)
		}
	}
}

// TestDecodeResponse_ReadsTheCodeAndResults pins the (u, a{sv}) pair a Response
// signal carries, including the case where the results dictionary is empty.
func TestDecodeResponse_ReadsTheCodeAndResults(t *testing.T) {
	results := map[string]dbus.Variant{
		"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/1_42/neru_session"),
	}

	code, decoded, err := decodeResponse([]any{uint32(0), results})
	if err != nil {
		t.Fatalf("decodeResponse returned %v, want nil", err)
	}

	if code != responseSuccess {
		t.Errorf("code = %d, want %d", code, responseSuccess)
	}

	if got, _ := decoded["session_handle"].Value().(string); got !=
		"/org/freedesktop/portal/desktop/session/1_42/neru_session" {
		t.Errorf("session_handle = %q, want the handle from the signal", got)
	}
}

// TestDecodeResponse_RejectsAMalformedBody pins that a truncated or mistyped
// reply is an error rather than a zero Response, which would otherwise read as
// success and leave the caller waiting on a session that does not exist.
func TestDecodeResponse_RejectsAMalformedBody(t *testing.T) {
	tests := []struct {
		name string
		body []any
	}{
		{name: "no fields", body: nil},
		{name: "one field", body: []any{uint32(0)}},
		{name: "code is not a uint32", body: []any{"0", map[string]dbus.Variant{}}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := decodeResponse(testCase.body)
			if err == nil {
				t.Fatal("decodeResponse accepted a malformed body")
			}

			if !errs.IsCode(err, errs.CodeAccessibilityFailed) {
				t.Fatalf("error code = %v, want %v", errs.GetCode(err), errs.CodeAccessibilityFailed)
			}
		})
	}
}

// TestSessionHandle_AcceptsBothWireTypes pins the two shapes a portal has been
// seen to use: the specification's string and some backends' object path.
func TestSessionHandle_AcceptsBothWireTypes(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]dbus.Variant
		want    dbus.ObjectPath
		ok      bool
	}{
		{
			name: "string",
			results: map[string]dbus.Variant{
				"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/1_42/a"),
			},
			want: "/org/freedesktop/portal/desktop/session/1_42/a",
			ok:   true,
		},
		{
			name: "object path",
			results: map[string]dbus.Variant{
				"session_handle": dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/portal/desktop/session/1_42/b")),
			},
			want: "/org/freedesktop/portal/desktop/session/1_42/b",
			ok:   true,
		},
		{
			name:    "missing",
			results: map[string]dbus.Variant{},
			want:    "",
			ok:      false,
		},
		{
			name: "empty string",
			results: map[string]dbus.Variant{
				"session_handle": dbus.MakeVariant(""),
			},
			want: "",
			ok:   false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := sessionHandle(testCase.results)
			if ok != testCase.ok || got != testCase.want {
				t.Fatalf("sessionHandle() = (%q, %v), want (%q, %v)",
					got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

// TestEvdevButton_MapsBothNumberings pins the two selectors Button accepts. A
// wrong mapping here presses the wrong button, which is silent and destructive
// (a right-click where a left-click was asked for).
func TestEvdevButton_MapsBothNumberings(t *testing.T) {
	tests := []struct {
		button int
		want   uint32
	}{
		{button: buttonSelectorLeft, want: 0x110},
		{button: buttonSelectorMiddle, want: 0x112},
		{button: buttonSelectorRight, want: 0x111},
		{button: int(buttonCodeLeft), want: 0x110},
		{button: int(buttonCodeMiddle), want: 0x112},
		{button: int(buttonCodeRight), want: 0x111},
	}

	for _, testCase := range tests {
		got, ok := evdevButton(testCase.button)
		if !ok || got != testCase.want {
			t.Errorf("evdevButton(%d) = (%#x, %v), want (%#x, true)",
				testCase.button, got, ok, testCase.want)
		}
	}

	for _, invalid := range []int{0, 4, 9, 0x100, 0x113, 1 << 20} {
		if code, ok := evdevButton(invalid); ok {
			t.Errorf("evdevButton(%d) = (%#x, true), want a refusal", invalid, code)
		}
	}
}

// TestHandleToken_IsUniqueAndPathSafe pins the token's shape: the portal pastes
// it into a D-Bus object path, so anything outside [A-Za-z0-9_] would make the
// path invalid, and a repeated token would make two requests answer on one path.
func TestHandleToken_IsUniqueAndPathSafe(t *testing.T) {
	seen := make(map[string]struct{}, 64)

	for range 64 {
		token := handleToken()

		if !strings.HasPrefix(token, "neru_") {
			t.Fatalf("token %q does not start with neru_", token)
		}

		for _, character := range token {
			isDigit := character >= '0' && character <= '9'
			isLower := character >= 'a' && character <= 'z'
			isUpper := character >= 'A' && character <= 'Z'
			if !isDigit && !isLower && !isUpper && character != '_' {
				t.Fatalf("token %q contains %q, which is not safe in an object path", token, character)
			}
		}

		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("handleToken returned %q twice", token)
		}

		seen[token] = struct{}{}
	}
}
