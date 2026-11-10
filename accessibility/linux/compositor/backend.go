package compositor

import (
	"os"
	"strings"
)

// Backend names the compositor a session runs. It is the vocabulary
// LinuxBackend in the neru daemon uses, narrowed to what has a window source
// here: the wlroots family is split by the IPC socket each compositor exports,
// and every session this package cannot ask is BackendOther.
type Backend string

const (
	// BackendKWin is KDE Plasma's KWin on Wayland, where AT-SPI reports
	// window-relative coordinates and the compositor bridge is the only source of
	// a window's position. A KDE X11 session is BackendOther: AT-SPI already
	// reports screen coordinates there.
	BackendKWin Backend = "kwin"
	// BackendGNOME is GNOME Shell on Wayland. Its window source is the Neru
	// GNOME Shell extension.
	BackendGNOME Backend = "gnome"
	// BackendNiri is the niri scrollable-tiling compositor.
	BackendNiri Backend = "niri"
	// BackendSway is the sway i3-compatible compositor.
	BackendSway Backend = "sway"
	// BackendHyprland is the Hyprland compositor.
	BackendHyprland Backend = "hyprland"
	// BackendCOSMIC is System76's COSMIC desktop.
	BackendCOSMIC Backend = "cosmic"
	// BackendOther is X11, an unidentified session, or a wlroots compositor
	// whose IPC socket is not exported. It has no focus or geometry source, so
	// callers keep the AT-SPI behavior.
	BackendOther Backend = "other"
)

// String returns the backend's stable label, which is also the value
// Resolver.Backend reports and every origin source selects on.
func (b Backend) String() string { return string(b) }

// The environment variables compositor detection reads. They are named once so
// a test drives the same variables the detector does.
const (
	envCurrentDesktop = "XDG_CURRENT_DESKTOP"
	envWaylandDisplay = "WAYLAND_DISPLAY"
	envNiriSocket     = "NIRI_SOCKET"
	envSwaySocket     = "SWAYSOCK"
	envHyprlandSig    = "HYPRLAND_INSTANCE_SIGNATURE"
)

// envGetenv is the environment lookup detection uses. It is a variable so a
// test can drive detection without mutating the process environment.
var envGetenv = os.Getenv

// wlrootsDesktops are the XDG_CURRENT_DESKTOP values that name a compositor of
// the wlroots family. The "<name>:wlroots" tag is how a wlroots compositor tells
// xdg-desktop-portal-wlr to serve it, so it answers the same question for
// compositors not named here.
var wlrootsDesktops = []string{
	"SWAY", "HYPRLAND", "NIRI", "RIVER", "WAYFIRE", "LABWC", "WLROOTS",
}

// detectBackend resolves the session's compositor from the environment, reading
// the variables through getenv so a test can supply them. It follows the neru
// backend detector's precedence exactly:
//
//  1. A session with no WAYLAND_DISPLAY is not a Wayland session, whatever
//     XDG_CURRENT_DESKTOP says, and is reported as other (X11 or unknown).
//  2. On Wayland, GNOME, KDE and COSMIC are named by XDG_CURRENT_DESKTOP.
//  3. A wlroots session, named or not, is narrowed to niri, sway or Hyprland by
//     the IPC socket its compositor exports. A wlroots compositor with no
//     exported socket is other, because there is nobody to ask.
//
// XDG_SESSION_TYPE is not consulted: it records how the session was started,
// not what is running, and reading it as a second opinion has misclassified a
// sway session launched from a systemd user unit.
func detectBackend(getenv func(string) string) Backend {
	if getenv(envWaylandDisplay) == "" {
		return BackendOther
	}

	desktop := strings.ToUpper(getenv(envCurrentDesktop))

	switch {
	case strings.Contains(desktop, "GNOME"):
		return BackendGNOME
	case strings.Contains(desktop, "KDE"):
		return BackendKWin
	case strings.Contains(desktop, "COSMIC"):
		return BackendCOSMIC
	}

	if desktop != "" && !namesWlroots(desktop) {
		return BackendOther
	}

	if backend, ok := wlrootsBackend(getenv); ok {
		return backend
	}

	return BackendOther
}

// namesWlroots reports whether a desktop name belongs to the wlroots family.
func namesWlroots(desktop string) bool {
	for _, name := range wlrootsDesktops {
		if strings.Contains(desktop, name) {
			return true
		}
	}

	return false
}

// wlrootsBackend picks the wlroots compositor by the IPC socket it exports. Each
// variable is set only under its own compositor, so the order only matters for a
// session that inherited a stale one; niri is checked first because its socket
// is the least likely to be inherited from an outer session.
func wlrootsBackend(getenv func(string) string) (Backend, bool) {
	switch {
	case getenv(envNiriSocket) != "":
		return BackendNiri, true
	case getenv(envSwaySocket) != "":
		return BackendSway, true
	case getenv(envHyprlandSig) != "":
		return BackendHyprland, true
	default:
		return BackendOther, false
	}
}
