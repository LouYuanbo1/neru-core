// Package compositor detects the live Linux desktop compositor and answers the
// two questions the AT-SPI tree cannot: which window the compositor has focused,
// and where that window sits on screen.
//
// AT-SPI exposes no focused-window query of its own. Its ACTIVE state is set by
// the toolkit, not the compositor, so a Chromium or Electron window can claim it
// while it is in the background. The same protocol reports element extents
// relative to the top-level window on Wayland, where a client cannot know its own
// position; a GTK 4 client reports a zero screen origin for every element. This
// package fills both gaps from the compositor's own API, which is where the
// authoritative answer lives.
//
// # Backends
//
// Detection reads the environment the way the neru daemon's backend detector
// does: WAYLAND_DISPLAY for the session family, XDG_CURRENT_DESKTOP to name the
// desktop, and the IPC sockets NIRI_SOCKET, SWAYSOCK and
// HYPRLAND_INSTANCE_SIGNATURE to pick among the wlroots compositors. It
// deliberately does not consult XDG_SESSION_TYPE or XDG_SESSION_DESKTOP: those
// describe what a session was started as, not what is running, and a second
// opinion is how a sway session launched from a systemd user unit was once
// misread as X11.
//
//   - kwin: KWin has no command line. A small KWin script, loaded over
//     org.kde.KWin's scripting D-Bus interface, pushes the focused window's
//     client geometry and identity back to an object this package exports. The
//     script is written to XDG_RUNTIME_DIR, which must be private.
//   - gnome: the Neru GNOME Shell extension answers over its own D-Bus name
//     (org.neru.Shell). Mutter exposes no window geometry and no scripting
//     interface this package may use, so without that extension GNOME reports
//     CodeNotSupported and the caller falls back to AT-SPI.
//   - niri: `niri msg -j focused-window` and `focused-output`. niri reports a
//     workspace-view position for floating windows only (niri#2381), so a tiled
//     window is the compositor answering "no position", not a failure.
//   - sway: `swaymsg -t get_tree`, whose focused node carries both the absolute
//     rectangle and the content rectangle (rect plus window_rect, which excludes
//     server-side decorations).
//   - hyprland: `hyprctl -j activewindow`, whose "at" and "size" are absolute
//     screen pixels.
//   - cosmic: cosmic-comp publishes toplevels only through the
//     zcosmic_toplevel_info_v1 Wayland protocol, which this pure-Go package
//     cannot read. Focus therefore always reports CodeNotSupported; the origin
//     rule is still implemented and used, but only from geometry an embedder
//     supplies through Options.Geometry. A geometry the caller can vouch for is
//     accepted only when its size matches the AT-SPI frame.
//   - other: X11, an unknown session, or a wlroots compositor whose IPC socket
//     is not exported. X11 needs no origin correction, because AT-SPI already
//     reports screen coordinates there, and this package has no X11
//     focused-window query; the rest have nobody to ask.
//
// # What callers get
//
// FocusedWindow and FocusedApplication report the focused window's identity and
// rectangle. FocusedApplication is derived from the same snapshot, so an
// application walk never disagrees with the window it came from. WindowOrigin
// reports the screen position of the content origin of a window whose identity
// and size the caller describes: the compositor's answer is accepted only when
// it still names that window, because a focus change between the AT-SPI read and
// the query would otherwise place every hint at another window's position.
// PlacementFor turns that origin into the SCREEN-versus-WINDOW decision and the
// offset an AT-SPI extent needs, as a pure function.
//
// A compositor with no answer to give (nothing focused, a tiled niri window)
// returns found=false and a nil error. A compositor that cannot be asked at all
// returns errs.CodeNotSupported, which callers must treat as "fall back", never
// as "nothing is focused".
//
// The package is pure Go and uses no cgo: compositor command lines are run with
// os/exec and the D-Bus bridges use godbus. It never logs and never returns a
// window title in an error message, because a title is window contents rather
// than an application identity.
package compositor
