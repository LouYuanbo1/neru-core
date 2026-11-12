//go:build linux

package window

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file is the X11 backend. An X11 session keeps its window list in the X
// server, and EWMH — the freedesktop window-manager specification every mainstream
// X11 window manager implements — publishes that list as properties: _NET_CLIENT_LIST
// and _NET_CLIENT_LIST_STACKING on the root window for the windows, _NET_ACTIVE_WINDOW
// for the focused one, and per-window properties for the title, the type, the state
// and the pid. Enumerating the desktop is therefore a handful of property reads, and
// activating or placing a window is one client message: _NET_ACTIVE_WINDOW, then
// _NET_MOVERESIZE_WINDOW for a frame the caller named. linux_x11.go binds the Xlib
// calls with purego, so no part of this needs CGo.
//
// Wayland is reported as unsupported rather than answered badly. A Wayland compositor
// exposes no protocol that lets one application enumerate or activate another
// application's windows, and the XWayland server that is often running beside it knows
// only the X11 clients it hosts: listing those and calling them the desktop would send
// a caller off to activate a window that is not there. An X11 session whose window
// manager does not implement EWMH is rejected the same way, because without those
// properties there is no window list to read.
//
// The manager holds one Display for its whole life and lends it to one call at a time;
// manager.mu explains why.

// activationTimeout is how long Activate waits for the window manager to make the
// requested window active. A manager handles the request in its next event pass, which
// takes milliseconds; the wait covers a manager that is busy starting an application,
// not a slow one.
const activationTimeout = 500 * time.Millisecond

// activationPoll is the interval between two _NET_ACTIVE_WINDOW reads while Activate
// waits.
const activationPoll = 10 * time.Millisecond

// deletedExecutableSuffix is what the kernel appends to /proc/<pid>/exe when the binary
// a process was started from has been replaced or removed.
const deletedExecutableSuffix = " (deleted)"

// placementSettle is how long a moved window's frame must stay unchanged before
// SetBounds accepts it. A window manager applies the request asynchronously and
// may clamp it, so the first changed frame is not necessarily the final one.
const placementSettle = 100 * time.Millisecond

// manager is the X11 window backend.
//
// It holds one connection to the X server for its lifetime: the atoms it resolves are
// per connection, and every window query is a property read on that connection, so a
// connection per call would cost an X11 handshake per window.
type manager struct {
	includeUntitled bool

	// mu serializes the calls, and with them every Xlib request this backend makes. A
	// Display is not safe for concurrent use unless the process called XInitThreads
	// before any other Xlib call, which a library cannot require of its host, so this
	// backend lends its connection to one caller at a time instead. The same lock
	// guards closed, which makes Close wait for the call in flight rather than tearing
	// the connection down underneath it.
	mu     sync.Mutex
	closed bool

	// conn is the display connection, open for the manager's whole life and closed by
	// Close.
	conn *x11Conn
}

var _ Manager = (*manager)(nil)

// New returns the X11 window backend.
//
// It fails rather than returning a manager that cannot work: a Wayland session, a
// session with no DISPLAY, a display that cannot be opened and an X server without an
// EWMH window manager are all reported here, once, instead of on every call.
func New(opts Options) (Manager, error) {
	if err := checkX11Session(); err != nil {
		return nil, err
	}

	conn, err := openX11Connection()
	if err != nil {
		return nil, err
	}

	return &manager{includeUntitled: opts.IncludeUntitled, conn: conn}, nil
}

// List returns every window the window manager lists, front to back.
func (m *manager) List(ctx context.Context) ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureOpen(); err != nil {
		return nil, err
	}

	return m.list(ctx)
}

// list enumerates the desktop's windows. The caller holds m.mu and has checked that
// the manager is open.
//
// The order is the contract's front-to-back one: the focused window first, then the
// others in the window manager's stacking order. That order comes from
// _NET_CLIENT_LIST_STACKING, which EWMH defines bottom to top and which is therefore
// reversed; a manager that does not publish it falls back to _NET_CLIENT_LIST, whose
// order is the initial mapping order. Mapping order is a weaker answer than stacking
// order — older windows come first regardless of where they are now — and putting the
// focused window in front of it is the part of the order a caller can rely on either
// way.
func (m *manager) list(ctx context.Context) ([]Info, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	active, err := m.conn.activeWindow()
	if err != nil {
		return nil, err
	}

	clients, err := m.conn.clientList()
	if err != nil {
		return nil, err
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	// A desktop with no windows answers with an empty slice rather than nil, so a
	// caller can range over the result without a nil check.
	windows := make([]Info, 0, len(clients))

	var (
		focused  Info
		hasFocus bool
	)

	for _, client := range clients {
		if err := contextError(ctx); err != nil {
			return nil, err
		}

		info, ok, err := m.describeWindow(client, active)
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		if info.Focused {
			focused, hasFocus = info, true

			continue
		}

		windows = append(windows, info)
	}

	if err := contextError(ctx); err != nil {
		return nil, err
	}

	if hasFocus {
		windows = append([]Info{focused}, windows...)
	}

	return windows, nil
}

// Frontmost returns the window the user is working in.
func (m *manager) Frontmost(ctx context.Context) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	// _NET_ACTIVE_WINDOW is the window manager's answer, not the X input focus: the
	// input focus can be on a menu or on the window manager's own frame, while this
	// property names the client the user is working in. A session where nothing is
	// focused — the desktop itself is, or the window manager has no client — carries
	// no property value, which the contract reports as "not found".
	active, err := m.conn.activeWindow()
	if err != nil {
		return Info{}, err
	}

	if active == 0 {
		return Info{}, errs.New(errs.CodeElementNotFound, "no application window has focus")
	}

	info, ok, err := m.describeWindow(active, active)
	if err != nil {
		return Info{}, err
	}

	if !ok {
		return Info{}, errs.Newf(
			errs.CodeElementNotFound,
			"the active window %s is not one this backend can describe",
			formatWindowID(active),
		)
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	info.Focused = true

	return info, nil
}

// Activate brings the window target selects to the foreground and gives it focus.
func (m *manager) Activate(ctx context.Context, target Target) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := validateTarget(target); err != nil {
		return Info{}, err
	}

	// A fresh list is what makes a target a description of a window rather than a
	// handle that has to still be alive: a window that was recreated since the
	// caller's last List is still found by application or by title, and the id the
	// window manager currently has is the one the request is sent for.
	windows, err := m.list(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := Find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	win, ok := parseWindowID(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not an X11 window id",
			selected.ID,
		)
	}

	if err := m.conn.activateWindow(win); err != nil {
		return Info{}, err
	}

	// The request is only a request, so the switch is verified by reading the
	// property back until the window manager agrees. A manager that keeps another
	// window active is reported as a failure instead of as the success the sent
	// message would suggest.
	deadline := time.Now().Add(activationTimeout)

	for {
		if err := contextError(ctx); err != nil {
			return Info{}, err
		}

		active, err := m.conn.activeWindow()
		if err != nil {
			return Info{}, err
		}

		if active == win {
			break
		}

		if time.Now().After(deadline) {
			return Info{}, errs.Newf(
				errs.CodeAccessibilityFailed,
				"could not activate %q (%s): %s is still the active window",
				selected.Title,
				selected.Application,
				m.activeDescription(active),
			)
		}

		time.Sleep(activationPoll)
	}

	activated, ok, err := m.describeWindow(win, win)
	if err != nil {
		return Info{}, err
	}

	if !ok {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared while it was being activated",
		)
	}

	activated.Focused = true

	return activated, nil
}

// SetBounds places the window target selects and returns it as it ended up.
func (m *manager) SetBounds(
	ctx context.Context,
	target Target,
	bounds image.Rectangle,
) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureOpen(); err != nil {
		return Info{}, err
	}

	if err := validateTarget(target); err != nil {
		return Info{}, err
	}

	if err := validateBounds(bounds); err != nil {
		return Info{}, err
	}

	if err := contextError(ctx); err != nil {
		return Info{}, err
	}

	windows, err := m.list(ctx)
	if err != nil {
		return Info{}, err
	}

	selected, ok := Find(windows, target)
	if !ok {
		return Info{}, notFound(target)
	}

	win, ok := parseWindowID(selected.ID)
	if !ok {
		return Info{}, errs.Newf(
			errs.CodeInternal,
			"window id %q is not an X11 window id",
			selected.ID,
		)
	}

	before, alive := m.conn.windowGeometry(win)
	if !alive {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared before it could be placed",
		)
	}

	// Bring the window up first: a minimized window is unmapped, and a manager
	// applies a geometry request to the frame it is showing. The activation is
	// best effort beyond this point; the frame is what the caller asked for.
	if err := m.conn.activateWindow(win); err != nil {
		return Info{}, err
	}

	if err := m.conn.moveResizeWindow(win, bounds); err != nil {
		return Info{}, err
	}

	if _, err := m.waitForGeometry(ctx, win, before, bounds); err != nil {
		return Info{}, err
	}

	active, err := m.conn.activeWindow()
	if err != nil {
		return Info{}, err
	}

	placed, ok, err := m.describeWindow(win, active)
	if err != nil {
		return Info{}, err
	}

	if !ok {
		return Info{}, errs.New(
			errs.CodeElementNotFound,
			"the window disappeared while it was being placed",
		)
	}

	return placed, nil
}

// waitForGeometry polls until the window's frame matches want, or until a frame
// the window manager clamped has settled, and returns what it saw.
//
// A frame that never leaves the position it had before the request is the
// manager ignoring or refusing the message, which is reported as a failure. A
// frame that changed but is not exactly want is accepted: tiling and snapping
// managers routinely apply their own rectangle, and reporting the actual one is
// more useful than insisting on the requested one.
func (m *manager) waitForGeometry(
	ctx context.Context,
	win uintptr,
	before, want image.Rectangle,
) (image.Rectangle, error) {
	if before == want {
		return want, nil
	}

	deadline := time.Now().Add(activationTimeout)

	var (
		last       image.Rectangle
		lastChange time.Time
	)

	for {
		geometry, alive := m.conn.windowGeometry(win)
		if !alive {
			return image.Rectangle{}, errs.New(
				errs.CodeElementNotFound,
				"the window disappeared while it was being placed",
			)
		}

		if geometry == want {
			return geometry, nil
		}

		if geometry != last {
			last = geometry
			lastChange = time.Now()
		} else if geometry != before && time.Since(lastChange) >= placementSettle {
			return geometry, nil
		}

		if err := contextError(ctx); err != nil {
			return image.Rectangle{}, err
		}

		if time.Now().After(deadline) {
			if last != before {
				return last, nil
			}

			return image.Rectangle{}, errs.Newf(
				errs.CodeAccessibilityFailed,
				"the window manager did not apply the requested frame %v; the window is still at %v",
				want,
				last,
			)
		}

		time.Sleep(activationPoll)
	}
}

// Close closes the connection to the X server. It is idempotent.
func (m *manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}

	m.closed = true

	return m.conn.close()
}

// ensureOpen reports the error a closed manager answers with. The caller holds m.mu,
// which is what makes a closed manager fail every call rather than acting on a closed
// Display.
func (m *manager) ensureOpen() error {
	if m.closed {
		return errs.New(errs.CodeInternal, "window manager is closed")
	}

	return nil
}

// describeWindow reads one window the window manager listed, and reports whether it is
// a window a caller should see. The caller holds m.mu.
func (m *manager) describeWindow(win, active uintptr) (Info, bool, error) {
	// The geometry read doubles as the existence check: a window the window manager
	// still lists but that has already been destroyed is dropped, because an id nobody
	// can activate is not a window a caller wants to be offered.
	bounds, alive := m.conn.windowGeometry(win)
	if !alive {
		return Info{}, false, nil
	}

	types, err := m.conn.windowTypes(win)
	if err != nil {
		return Info{}, false, err
	}

	if !m.conn.showsWindow(types) {
		return Info{}, false, nil
	}

	states, err := m.conn.windowStates(win)
	if err != nil {
		return Info{}, false, err
	}

	title, err := m.conn.windowTitle(win)
	if err != nil {
		return Info{}, false, err
	}

	pid, err := m.conn.windowPID(win)
	if err != nil {
		return Info{}, false, err
	}

	focused := win == active

	// An empty title is a helper surface most of the time, but not when it is the
	// window the user is in: an application that is still loading has focus before it
	// has a title.
	if title == "" && !m.includeUntitled && !focused {
		return Info{}, false, nil
	}

	info := Info{
		ID:          formatWindowID(win),
		PID:         pid,
		Title:       title,
		Application: processApplication(pid),
		Minimized:   m.conn.isMinimized(states),
		Focused:     focused,
	}

	// A minimized window is unmapped, so its frame describes nothing a user can see.
	// The bounds the geometry read returned are its pre-minimize rectangle, which is
	// where it will come back rather than where it is.
	if !info.Minimized {
		info.Bounds = bounds
	}

	return info, true, nil
}

// activeDescription names the window the window manager keeps active, for an error
// message. The title is read best effort: a window whose title cannot be read is still
// named by its id, which is what a caller needs in order to see what is in front.
func (m *manager) activeDescription(active uintptr) string {
	if active == 0 {
		return "no window"
	}

	title, err := m.conn.windowTitle(active)
	if err != nil || title == "" {
		return formatWindowID(active)
	}

	return strconv.Quote(title) + " (" + formatWindowID(active) + ")"
}

// checkX11Session reports why this session cannot be managed through X11, or nil when it
// can.
//
// The two failures are different problems and get different messages: a Wayland session
// has no window-management protocol to speak, while an X11 session without DISPLAY is a
// session this process is not attached to.
func checkX11Session() error {
	if evidence, wayland := waylandSession(); wayland {
		return errs.Newf(
			errs.CodeNotSupported,
			"window management needs an X11 session: this is a Wayland session (%s), and "+
				"Wayland has no protocol that lets one application enumerate, activate or "+
				"place another application's windows",
			evidence,
		)
	}

	if strings.TrimSpace(os.Getenv("DISPLAY")) == "" {
		return errs.New(
			errs.CodeNotSupported,
			"window management needs an X11 display and DISPLAY is not set",
		)
	}

	return nil
}

// waylandSession reports whether this session is a Wayland one, and names the variable
// that says so for the error message.
//
// XDG_SESSION_TYPE is what the display manager sets. WAYLAND_DISPLAY is checked as
// well, because a session started from a nested compositor often has the former unset
// while the latter proves the compositor is Wayland.
func waylandSession() (string, bool) {
	if sessionType := strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")); strings.EqualFold(sessionType, "wayland") {
		return "XDG_SESSION_TYPE=wayland", true
	}

	if display := os.Getenv("WAYLAND_DISPLAY"); display != "" {
		return "WAYLAND_DISPLAY=" + display, true
	}

	return "", false
}

// processApplication returns the application behind a window as List reports it: the
// base name of the executable the process runs.
//
// /proc/<pid>/exe is a magic link to the binary, which is the program the user
// started, and it is the answer that matches the Windows backend's "chrome.exe".
// Reading it needs permission to follow another user's link, so a window owned by a
// different user falls back to /proc/<pid>/comm, the name the kernel holds for the
// process: the executable's name truncated to 15 bytes, which is still enough to tell
// nm-applet from firefox. A binary replaced by a package upgrade is reported as
// "name (deleted)", and that suffix is the kernel's rather than part of the name.
//
// An empty result is metadata rather than a failure, so the window is kept: a window
// whose process cannot be read is still a usable window.
func processApplication(pid int) string {
	if pid <= 0 {
		return ""
	}

	directory := "/proc/" + strconv.Itoa(pid)

	if target, err := os.Readlink(directory + "/exe"); err == nil {
		name := filepath.Base(strings.TrimSuffix(target, deletedExecutableSuffix))
		if name != "" && name != "." && name != "/" {
			return name
		}
	}

	comm, err := os.ReadFile(directory + "/comm")
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(comm))
}

// formatWindowID renders an X11 window id as the opaque ID Info carries.
func formatWindowID(win uintptr) string {
	return "0x" + strconv.FormatUint(uint64(win), 16)
}

// parseWindowID reads back an ID formatWindowID produced.
func parseWindowID(id string) (uintptr, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	trimmed = strings.TrimPrefix(trimmed, "0x")

	value, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil || value == 0 {
		return 0, false
	}

	return uintptr(value), true
}
