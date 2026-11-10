package gnome

import (
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The D-Bus error names this package maps specially.
const (
	// errAccessDenied is the shell refusing this process. Current GNOME releases
	// only serve an allowlist of senders.
	errAccessDenied = "org.freedesktop.DBus.Error.AccessDenied"

	errServiceUnknown  = "org.freedesktop.DBus.Error.ServiceUnknown"
	errNameHasNoOwner  = "org.freedesktop.DBus.Error.NameHasNoOwner"
	errUnknownObject   = "org.freedesktop.DBus.Error.UnknownObject"
	errUnknownIface    = "org.freedesktop.DBus.Error.UnknownInterface"
	errUnknownMethod   = "org.freedesktop.DBus.Error.UnknownMethod"
	errUnknownProperty = "org.freedesktop.DBus.Error.UnknownProperty"

	errNoReply  = "org.freedesktop.DBus.Error.NoReply"
	errTimeout  = "org.freedesktop.DBus.Error.Timeout"
	errTimedOut = "org.freedesktop.DBus.Error.TimedOut"
)

// shellMissing is the sentence a missing interface is reported with. It names the
// interface, the object and the service, so a caller reading the error knows
// which component is absent instead of guessing at the compositor.
const shellMissing = "GNOME Shell's screenshot interface (org.gnome.Shell.Screenshot " +
	"at /org/gnome/Shell/Screenshot on org.gnome.Shell) is not available on this " +
	"session's D-Bus; this desktop is not running GNOME Shell or does not export it"

// shellDenied is the sentence a refused screenshot is reported with. The shell
// gives no reason, so the sentence says what the interface's refusal means on
// this desktop rather than inventing one.
const shellDenied = "GNOME Shell refused the screenshot; recent releases only take one " +
	"for an allowlisted caller, such as the desktop portal or the shell's own " +
	"screenshot tool, so this desktop has to be captured through the portal"

// shellCallError maps a D-Bus error name from the shell onto the shared error
// vocabulary. The name is passed separately from the cause so the mapping is pure
// logic and can be tested without a bus.
//
// A missing service, object, interface or method all mean the same thing here:
// this session's shell does not offer the screenshot method, so the caller must
// fall back instead of retrying. A denial is the shell refusing this process,
// which on current releases is the ordinary answer outside its allowlist.
// Anything else is this package's own failure to talk to a shell that exists.
func shellCallError(name string, cause error, what string) error {
	switch name {
	case errServiceUnknown, errNameHasNoOwner, errUnknownObject,
		errUnknownIface, errUnknownMethod, errUnknownProperty:
		return errs.Wrap(cause, errs.CodeNotSupported, what+": "+shellMissing)
	case errAccessDenied:
		return errs.Wrap(cause, errs.CodeAccessibilityDenied, what+": "+shellDenied)
	case errNoReply, errTimeout, errTimedOut:
		return errs.Wrap(cause, errs.CodeTimeout, what+": GNOME Shell did not answer in time")
	}

	if denialError(name) {
		return errs.Wrap(cause, errs.CodeAccessibilityDenied, what+": GNOME Shell denied the request")
	}

	return errs.Wrap(cause, errs.CodeInternal, what)
}

// denialError reports whether a D-Bus error name is one of the spellings a
// denial arrives under. Only the name is inspected, never the message: a message
// may mention a denial while reporting something else.
func denialError(name string) bool {
	lower := strings.ToLower(name)

	for _, marker := range []string{"denied", "authoriz", "notallowed", "permission", "authfailed"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	return false
}
