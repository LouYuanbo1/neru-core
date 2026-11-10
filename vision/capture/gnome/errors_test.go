package gnome

import (
	"errors"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestShellCallError_MapsTheErrorsTheShellReturns(t *testing.T) {
	cause := errors.New("the shell said no")

	tests := []struct {
		name string
		want errs.Code
	}{
		{name: errAccessDenied, want: errs.CodeAccessibilityDenied},
		{name: errServiceUnknown, want: errs.CodeNotSupported},
		{name: errNameHasNoOwner, want: errs.CodeNotSupported},
		{name: errUnknownObject, want: errs.CodeNotSupported},
		{name: errUnknownIface, want: errs.CodeNotSupported},
		{name: errUnknownMethod, want: errs.CodeNotSupported},
		{name: errUnknownProperty, want: errs.CodeNotSupported},
		{name: errNoReply, want: errs.CodeTimeout},
		{name: errTimeout, want: errs.CodeTimeout},
		{name: errTimedOut, want: errs.CodeTimeout},
		{name: "org.freedesktop.DBus.Error.NotAuthorized", want: errs.CodeAccessibilityDenied},
		{name: "org.example.Error.SomethingElse", want: errs.CodeInternal},
		{name: "", want: errs.CodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := shellCallError(test.name, cause, "could not capture")
			if !errs.IsCode(err, test.want) {
				t.Errorf("error = %v, want %v", err, test.want)
			}

			if !errors.Is(err, cause) {
				t.Errorf("error %v does not carry its cause", err)
			}
		})
	}
}

func TestDenialError_MatchesOnlyDenialNames(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "org.freedesktop.DBus.Error.AccessDenied", want: true},
		{name: "org.gnome.Shell.Screenshot.Error.NotAllowed", want: true},
		{name: "org.example.Error.PermissionFailure", want: true},
		{name: "org.example.Error.NotAuthorized", want: true},
		{name: "org.example.Error.Failure", want: false},
		{name: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := denialError(test.name); got != test.want {
				t.Errorf("denialError(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}
