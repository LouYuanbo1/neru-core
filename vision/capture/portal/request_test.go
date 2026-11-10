//go:build linux

package portal

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestScreenshotOptions_AskForNoAreaPicker(t *testing.T) {
	options := screenshotOptions("neru_core_7_1")

	if len(options) != 3 {
		t.Fatalf("options = %v, want the three this package sets", options)
	}

	token, isString := options[optionHandleToken].Value().(string)
	if !isString || token != "neru_core_7_1" {
		t.Errorf("handle_token = %v, want the token that was passed in", options[optionHandleToken].Value())
	}

	interactive, isBool := options[optionInteractive].Value().(bool)
	if !isBool || interactive {
		t.Errorf("interactive = %v, want a false bool", options[optionInteractive].Value())
	}

	modal, isBool := options[optionModal].Value().(bool)
	if !isBool || modal {
		t.Errorf("modal = %v, want a false bool", options[optionModal].Value())
	}
}

func TestRequestHandle_DerivesThePathTheInterfaceDocuments(t *testing.T) {
	tests := []struct {
		name   string
		sender string
		token  string
		want   dbus.ObjectPath
	}{
		{
			name:   "a unique name's colon is dropped and its dots become underscores",
			sender: ":1.42",
			token:  "neru_core_7_1",
			want:   "/org/freedesktop/portal/desktop/request/1_42/neru_core_7_1",
		},
		{
			name:   "every dot is replaced",
			sender: ":1.42.7",
			token:  "t",
			want:   "/org/freedesktop/portal/desktop/request/1_42_7/t",
		},
		{
			name:   "a name already without its colon is accepted",
			sender: "1.42",
			token:  "t",
			want:   "/org/freedesktop/portal/desktop/request/1_42/t",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestHandle(test.sender, test.token); got != test.want {
				t.Errorf("requestHandle(%q, %q) = %q, want %q",
					test.sender, test.token, got, test.want)
			}
		})
	}
}

func TestNextRequestToken_IsPortalSafeAndUnique(t *testing.T) {
	first := nextRequestToken()
	second := nextRequestToken()

	if first == second {
		t.Fatalf("two requests derived the same token %q", first)
	}

	pid := strconv.Itoa(os.Getpid())

	for _, token := range []string{first, second} {
		if !strings.HasPrefix(token, requestTokenPrefix) {
			t.Errorf("token %q does not carry the %q prefix", token, requestTokenPrefix)
		}

		if !strings.Contains(token, pid) {
			t.Errorf("token %q does not name this process %s", token, pid)
		}

		for _, char := range token {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_", char) {
				t.Fatalf("token %q contains %q, which the portal's handle_token forbids", token, char)
			}
		}
	}
}

func TestDecodeResponse_SuccessCarriesThePicturesURI(t *testing.T) {
	results, err := decodeResponse([]any{
		responseSuccess,
		map[string]dbus.Variant{resultURIKey: dbus.MakeVariant("file:///tmp/shot.png")},
	})
	if err != nil {
		t.Fatalf("decodeResponse: %v", err)
	}

	path, err := resultFilePath(results)
	if err != nil {
		t.Fatalf("resultFilePath: %v", err)
	}

	if path != "/tmp/shot.png" {
		t.Errorf("path = %q, want %q", path, "/tmp/shot.png")
	}
}

func TestDecodeResponse_RefusesADenial(t *testing.T) {
	_, err := decodeResponse([]any{responseCanceled, map[string]dbus.Variant{}})
	if !errs.IsCode(err, errs.CodeAccessibilityDenied) {
		t.Errorf("error = %v, want CodeAccessibilityDenied", err)
	}
}

func TestDecodeResponse_RefusesABodyItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		body []any
	}{
		{name: "too few values", body: []any{responseSuccess}},
		{name: "a code that is not a uint32", body: []any{"0", map[string]dbus.Variant{}}},
		{name: "a result that is not a dictionary", body: []any{responseSuccess, "nope"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeResponse(test.body); !errs.IsCode(err, errs.CodeInternal) {
				t.Errorf("decodeResponse(%v) error = %v, want CodeInternal", test.body, err)
			}
		})
	}
}

func TestResultFilePath_RefusesAResultWithoutAUsableURI(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]dbus.Variant
	}{
		{name: "no uri at all", results: map[string]dbus.Variant{}},
		{name: "an empty uri", results: map[string]dbus.Variant{resultURIKey: dbus.MakeVariant("")}},
		{name: "a uri that is not a string", results: map[string]dbus.Variant{resultURIKey: dbus.MakeVariant(7)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resultFilePath(test.results); !errs.IsCode(err, errs.CodeInternal) {
				t.Errorf("resultFilePath(%v) error = %v, want CodeInternal", test.results, err)
			}
		})
	}
}

func TestIsResponseSignal_OnlyAcceptsThisRequestsAnswer(t *testing.T) {
	namespace := requestNamespace(":1.42")
	derived := requestHandle(":1.42", "neru_core_7_1")

	tests := []struct {
		name    string
		signal  *dbus.Signal
		handles []dbus.ObjectPath
		want    bool
	}{
		{
			name: "the predicted path under this connection's namespace",
			signal: &dbus.Signal{
				Name: responseSignalName,
				Path: dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_42/other"),
			},
			handles: []dbus.ObjectPath{derived},
			want:    true,
		},
		{
			name:    "a handle the portal returned that the token did not predict",
			signal:  &dbus.Signal{Name: responseSignalName, Path: "/org/freedesktop/portal/desktop/request/9_9/odd"},
			handles: []dbus.ObjectPath{derived, "/org/freedesktop/portal/desktop/request/9_9/odd"},
			want:    true,
		},
		{
			name:    "a signal that is not a Response",
			signal:  &dbus.Signal{Name: "org.freedesktop.portal.Request.Closed", Path: derived},
			handles: []dbus.ObjectPath{derived},
		},
		{
			name:    "another connection's request",
			signal:  &dbus.Signal{Name: responseSignalName, Path: "/org/freedesktop/portal/desktop/request/9_9/odd"},
			handles: []dbus.ObjectPath{derived},
		},
		{
			name:    "no signal at all",
			signal:  nil,
			handles: []dbus.ObjectPath{derived},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isResponseSignal(test.signal, namespace, test.handles); got != test.want {
				t.Errorf("isResponseSignal = %t, want %t", got, test.want)
			}
		})
	}
}
