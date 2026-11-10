package kwin

import (
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The error names ScreenShot2 documents, plus the D-Bus errors that mean the
// service, the object or the method is not there.
const (
	// errNotAuthorized is KWin refusing this process. Releases up to Plasma 5.27
	// checked the caller's desktop file for the interface before serving it.
	errNotAuthorized = "org.kde.KWin.ScreenShot2.Error.NoAuthorized"
	// errCancelled is KWin's sink reporting that no image could be produced.
	errCancelled      = "org.kde.KWin.ScreenShot2.Error.Cancelled"
	errInvalidArea    = "org.kde.KWin.ScreenShot2.Error.InvalidArea"
	errInvalidScreen  = "org.kde.KWin.ScreenShot2.Error.InvalidScreen"
	errInvalidWindow  = "org.kde.KWin.ScreenShot2.Error.InvalidWindow"
	errNoActiveWindow = "org.kde.KWin.ScreenShot2.Error.NoActiveWindow"
	errFileDescriptor = "org.kde.KWin.ScreenShot2.Error.FileDescriptor"

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

// kwinMissing is the sentence a missing interface is reported with. It names the
// interface, the object and the bus names, so a caller reading the error knows
// which component is absent instead of guessing at the compositor.
const kwinMissing = "KWin's screenshot interface (org.kde.KWin.ScreenShot2 at " +
	"/org/kde/KWin/ScreenShot2 on org.kde.KWin.ScreenShot2 or org.kde.KWin) is " +
	"not available on this session's D-Bus; this desktop is not running KWin"

// kwinCallError maps a D-Bus error name from KWin onto the shared error
// vocabulary. The name is passed separately from the cause so the mapping is
// pure logic and can be tested without a bus.
//
// A missing service, object, interface or method all mean the same thing here:
// this session's KWin does not offer the ScreenShot2 method the caller needs, so
// the caller must fall back instead of retrying. KWin's refusal to produce the
// frame, and its refusal to serve this caller at all, are denials: a caller
// degrades from them exactly like a refused permission prompt. Anything else is
// this package's own failure to talk to a KWin that exists.
func kwinCallError(name string, cause error, what string) error {
	switch name {
	case errServiceUnknown, errNameHasNoOwner, errUnknownObject,
		errUnknownIface, errUnknownMethod, errUnknownProperty:
		return errs.Wrap(cause, errs.CodeNotSupported, what+": "+kwinMissing)
	case errNotAuthorized:
		return errs.Wrap(
			cause,
			errs.CodeAccessibilityDenied,
			what+": KWin does not authorize this process to take a screenshot; "+
				"releases that check authorization require the caller's desktop file "+
				"to declare X-KDE-DBUS-Restricted-Interfaces with the ScreenShot2 "+
				"interface, so the screenshot portal is the fallback there",
		)
	case errCancelled:
		return errs.Wrap(
			cause,
			errs.CodeAccessibilityDenied,
			what+": KWin cancelled the capture, so there is no frame",
		)
	case errInvalidArea:
		return errs.Wrap(cause, errs.CodeInvalidInput, what+": KWin refused the area as empty")
	case errInvalidScreen, errNoActiveWindow, errInvalidWindow:
		return errs.Wrap(
			cause,
			errs.CodeNotSupported,
			what+": KWin could not identify the output or window to capture on this session",
		)
	case errFileDescriptor:
		return errs.Wrap(cause, errs.CodeInternal, what+": KWin refused the pipe descriptor")
	case errNoReply, errTimeout, errTimedOut:
		return errs.Wrap(cause, errs.CodeTimeout, what+": KWin did not answer in time")
	}

	if denialError(name) {
		return errs.Wrap(cause, errs.CodeAccessibilityDenied, what+": KWin denied the request")
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
