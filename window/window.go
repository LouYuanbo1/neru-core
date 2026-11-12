// Package window enumerates the desktop's top-level windows, reports which one
// is in front, and brings a chosen window to the foreground, moving or resizing it
// when the caller asks.
//
// It is the window half of the desktop control surface this module exposes: the
// accessibility package answers "what is inside a window", this package
// answers "which windows are there, how do I switch to one, and how do I put it
// where I want it". A caller identifies a window by whatever it happens to have —
// the ID a previous List returned, a process id, an application name or part of a
// title — and the platform backend resolves it without the caller naming a
// platform API.
//
//	manager, err := window.New(window.Options{})
//	if err != nil {
//		return err
//	}
//	defer manager.Close()
//
//	windows, err := manager.List(ctx)
//	for _, w := range windows {
//		fmt.Println(w.ID, w.PID, w.Application, w.Title, w.Bounds, w.Minimized)
//	}
//
//	activated, err := manager.Activate(ctx, window.Target{Application: "chrome.exe"})
//
// Every platform is driven through the same contract, and each backend hides
// what its platform needs: Win32 window enumeration plus the AttachThreadInput
// dance around SetForegroundWindow on Windows, the accessibility window objects
// plus NSRunningApplication activation on macOS, and the EWMH client list plus a
// _NET_ACTIVE_WINDOW client message on X11. No backend exposes an unsafe pointer
// or a native handle through this API: ID is the only platform-shaped value,
// and it is an opaque string.
//
// A platform that cannot answer at all reports errs.CodeNotSupported rather than
// an empty list, so a caller can tell "no windows" from "cannot see windows".
// Coordinates are in the same space the rest of this module uses: physical screen
// pixels in the global top-left-origin space on Windows and X11, and the logical
// points the accessibility API reports on macOS.
package window

import (
	"context"
	"image"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Options configures a Manager. A zero value asks for the backend's defaults.
type Options struct {
	// IncludeUntitled keeps windows whose title bar is empty.
	//
	// Most untitled top-level windows are helper surfaces: IME candidate
	// windows, drag previews, hidden message hosts. They are not what a caller
	// enumerating windows to show a user or a model wants, so they are dropped
	// by default. A window that has focus is kept regardless, because an
	// application that is still loading can be focused before it has a title.
	IncludeUntitled bool
}

// Info describes one top-level window.
type Info struct {
	// ID is the platform's handle for the window, rendered as a string: the
	// Win32 window handle on Windows, the accessibility window's identity on
	// macOS, the X11 window id on Linux. It is opaque to the caller and only
	// meaningful to this platform, but it stays valid across calls, so it is
	// the most exact way to address the same window later.
	ID string
	// PID is the process that owns the window.
	PID int
	// Title is the text in the window's title bar. It can be empty on a window
	// that is still loading, or on a platform that is not allowed to read
	// another application's window title.
	Title string
	// Application is how the platform identifies the application behind the
	// window: the executable's base name on Windows and Linux ("chrome.exe"),
	// the bundle identifier on macOS ("com.google.Chrome"). It is empty when
	// the platform cannot read it, which is metadata rather than a reason to
	// drop the window.
	Application string
	// Bounds is the window's visible frame in the coordinate space the rest of
	// this module uses: physical screen pixels in the global top-left-origin
	// space on Windows and X11, logical points on macOS. It is empty for a
	// minimized window, whose frame no longer describes anything a user can see.
	Bounds image.Rectangle
	// Minimized reports whether the window is minimized (iconic). A minimized
	// window is still listed: switching back to it is exactly what Activate is
	// for.
	Minimized bool
	// Focused reports whether this is the window the user is working in.
	Focused bool
}

// Target selects a window. Each field is a different handle on the same set:
// the ID a previous List returned, the owning process, the application's
// identity, or part of the title.
//
// The fields are tried in the order ID, PID, Application, Title, and the first
// non-zero one decides. That is what lets a caller ask for "this exact window"
// when it has the ID, and for "any window of that application" when it only
// knows the application.
type Target struct {
	// ID is the exact window ID a previous List returned.
	ID string
	// PID is the owning process. When the process has several windows, a
	// focused one wins, then the frontmost one.
	PID int
	// Application is the application identity as List reported it. It is
	// compared case-insensitively and accepts a full executable path, so
	// "chrome.exe" and "C:\\Program Files\\Google\\Chrome\\chrome.exe" both
	// select Chrome.
	Application string
	// Title is a case-insensitive substring of the window title. It is the
	// least precise field and is meant for a human or a model that only knows
	// what the window says.
	Title string
}

// Empty reports whether the target names nothing.
func (t Target) Empty() bool {
	return t.ID == "" && t.PID <= 0 && strings.TrimSpace(t.Application) == "" &&
		strings.TrimSpace(t.Title) == ""
}

// Manager enumerates, activates and places the desktop's windows.
//
// A Manager holds whatever its backend needs between calls (a display
// connection on Linux, nothing on Windows) and is safe for concurrent use. Its
// methods honor ctx cancellation while they wait for the platform to settle,
// and a Manager that has been closed reports an error instead of silently
// doing nothing.
type Manager interface {
	// List returns every visible top-level window, in front-to-back order:
	// the focused window first, then the others in the order the platform
	// stacks them. Minimized windows are included. An empty slice and a nil
	// error mean the desktop really has no windows to show.
	List(ctx context.Context) ([]Info, error)
	// Frontmost returns the window the user is working in, with Focused set.
	// It reports errs.CodeElementNotFound when no application window has
	// focus, such as when the desktop itself is focused.
	Frontmost(ctx context.Context) (Info, error)
	// Activate brings the window target selects to the foreground, gives it
	// focus, and returns its refreshed Info.
	//
	// It verifies the switch before reporting success: a platform that refuses
	// the activation — Windows' foreground lock, a window manager that ignores
	// the request — produces errs.CodeAccessibilityFailed rather than a false
	// positive. A target that matches no window produces
	// errs.CodeElementNotFound, and an empty target produces
	// errs.CodeInvalidInput.
	Activate(ctx context.Context, target Target) (Info, error)
	// SetBounds moves and resizes the window target selects, and returns it as
	// it ended up.
	//
	// The window is restored first when it is minimized or maximized, because a
	// maximized window cannot be given a new frame. It is then raised and asked
	// for the foreground: a window a caller is placing is almost always the one
	// it wants to work in. Whether the platform granted the foreground is
	// reported in the returned Info.Focused rather than treated as a failure of
	// the placement, since the rectangle is what the caller asked for.
	//
	// The returned Bounds are the platform's actual frame, which can differ from
	// bounds: a window with a minimum size keeps it, and a tiling window manager
	// may apply its own layout. Reporting the actual rectangle is honest; the
	// requested one would be a guess. An X11 window manager that never applies
	// the request reports errs.CodeAccessibilityFailed, because an ignored
	// message cannot be told apart from a refusal.
	//
	// bounds must not be empty. A target that matches no window produces
	// errs.CodeElementNotFound; an empty bounds produces errs.CodeInvalidInput.
	SetBounds(ctx context.Context, target Target, bounds image.Rectangle) (Info, error)
	// Close releases the backend's resources. It is idempotent.
	Close() error
}

// Supported reports whether this build has a window backend for the running
// platform.
func Supported() bool {
	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		return true
	default:
		return false
	}
}

// Find returns the window target selects.
//
// The selection rule is the one Target documents: the first non-zero of ID,
// PID, Application, Title decides, and among several matches a focused window
// wins, then the earliest in the slice. List returns windows front to back, so
// the earliest match is the frontmost one.
//
// Find is exported because a caller that already has a window list — from List,
// from a test fixture, or from its own enumeration — can apply the same rule
// the backends apply instead of reimplementing it.
func Find(windows []Info, target Target) (Info, bool) {
	if target.Empty() {
		return Info{}, false
	}

	application := normalizeApplication(target.Application)
	title := strings.ToLower(strings.TrimSpace(target.Title))

	matches := func(w Info) bool {
		switch {
		case target.ID != "":
			return w.ID == target.ID
		case target.PID > 0:
			return w.PID == target.PID
		case application != "":
			return normalizeApplication(w.Application) == application
		default:
			return title != "" && strings.Contains(strings.ToLower(w.Title), title)
		}
	}

	var first Info

	found := false

	for _, w := range windows {
		if !matches(w) {
			continue
		}

		if w.Focused {
			return w, true
		}

		if !found {
			first = w
			found = true
		}
	}

	return first, found
}

// normalizeApplication reduces an application identity to the form matching
// compares: trimmed, lower case, and with any directory stripped, so a full
// executable path and a bare executable name select the same application.
func normalizeApplication(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	return strings.ToLower(filepath.Base(trimmed))
}

// notFound builds the error Activate reports for a target no window matched.
// It names the field that was used, so a caller reading the message knows
// whether it asked for the wrong process, the wrong application or the wrong
// title.
func notFound(target Target) error {
	switch {
	case target.ID != "":
		return errs.Newf(errs.CodeElementNotFound, "no window with id %q", target.ID)
	case target.PID > 0:
		return errs.Newf(errs.CodeElementNotFound, "process %d has no visible window", target.PID)
	case strings.TrimSpace(target.Application) != "":
		return errs.Newf(
			errs.CodeElementNotFound,
			"no window whose application is %q",
			target.Application,
		)
	default:
		return errs.Newf(
			errs.CodeElementNotFound,
			"no window whose title contains %q",
			target.Title,
		)
	}
}

// validateTarget reports the error a backend returns before it reads anything
// when the target names nothing.
func validateTarget(target Target) error {
	if target.Empty() {
		return errs.New(errs.CodeInvalidInput, "window target is empty")
	}

	return nil
}

// validateBounds reports the error a backend returns before it touches a window
// when the requested rectangle has no area.
func validateBounds(bounds image.Rectangle) error {
	if bounds.Empty() {
		return errs.New(
			errs.CodeInvalidInput,
			"window bounds must have a positive width and height",
		)
	}

	return nil
}

// contextError reports a canceled or expired context in this module's error
// vocabulary, or nil when ctx is still live.
func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.CodeContextCanceled, "window query canceled")
	}

	return nil
}
