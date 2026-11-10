//go:build linux

// Package linux implements the ax.Client contract for Linux desktops.
//
// Three independent mechanisms split the work:
//
//   - The accessibility tree is read over AT-SPI, the cross-toolkit Linux
//     accessibility protocol, which is a D-Bus interface. Each application that
//     enables accessibility exports its whole widget tree on a private bus; this
//     backend connects to that bus lazily and walks the tree from the focused
//     application's top-level window.
//   - Pointer, wheel and modifier input is injected through the X server's
//     XTEST extension, reached with purego bindings to libX11 and libXtst, so
//     the module still builds and runs without a C toolchain and without cgo.
//   - The compositor, through the sibling compositor package, answers the two
//     questions AT-SPI cannot: which window the desktop actually has focused, and
//     where that window sits on screen.
//
// # Focus and window origin
//
// AT-SPI has no focused-window query. Its ACTIVE state is set by the toolkit
// rather than the compositor, so a Chromium or Electron window can claim it from
// the background, and on Wayland a client cannot know its own position at all.
// The compositor package asks the live desktop instead:
//
//   - KWin, through a small script this library loads over org.kde.KWin's
//     scripting D-Bus interface. The script is written to XDG_RUNTIME_DIR, which
//     must be private, and reports the focused window's client rectangle and
//     identity.
//   - GNOME, through the Neru GNOME Shell extension's own D-Bus name when that
//     extension is installed. Mutter exposes no other window query; without the
//     extension GNOME keeps the AT-SPI behavior.
//   - niri, sway and Hyprland, through `niri msg`, `swaymsg` and `hyprctl`, each
//     of which reports the focused window's geometry and identity.
//   - COSMIC, for the origin only, from a compositor.GeometrySource the embedder
//     supplies. cosmic-comp publishes window geometry only through the
//     zcosmic_toplevel_info_v1 Wayland protocol, which a pure-Go library cannot
//     read, so focus there falls back to AT-SPI.
//   - X11 needs no origin bridge, because AT-SPI already reports screen
//     coordinates there. This package has no X11 focused-window query, so X11
//     focus stays with the AT-SPI ranking, and the Chromium/Electron
//     background-window case that ranking has remains on X11.
//   - An unidentified session, or a wlroots compositor whose IPC socket is not
//     exported, has nobody to ask and keeps the AT-SPI behavior for both
//     questions.
//
// Focus selection asks the compositor first. When it cannot answer — X11, GNOME
// without the extension, a wlroots compositor whose IPC socket is not exported,
// or a desktop where nothing is focused — the ranking in selectFocusedFrame
// decides, which is the behavior this backend had before the compositor existed.
// When the compositor does answer but no AT-SPI frame can be singled out, nothing
// is reported as focused: guessing another window would put hints in a background
// one, which is worse than none. An embedder that does not want this library
// talking to the desktop can set Options.DisableCompositor.
//
// Placement uses the compositor's origin when it has one. Element extents are
// read in WINDOW coordinates and offset by the origin less the window's own
// window-relative position, which is exact and also corrects a toolkit that
// reports a zero screen origin for every element (GTK 4). A toolkit that cannot
// answer in window coordinates gets its SCREEN extents offset by the origin less
// the window's own screen origin, which is zero on a toolkit that reported them
// correctly. Without a compositor origin, placement falls back to the AT-SPI
// reconstruction in placementFor, which is exact for toolkits that report extents
// relative to the top-level window and degrades to plain screen coordinates
// otherwise.
//
// Everything the contract asks for that Linux genuinely cannot do — a global
// menu bar, Mission Control, supplementary surfaces — returns
// errs.CodeNotSupported with a message naming the missing facility, never a
// silent empty result.
//
// # Activation
//
// Toolkits build their accessibility tree only while assistive technology is
// enabled. The first tree call turns on org.a11y.Status.IsEnabled and
// ScreenReaderEnabled, remembering the previous values and restoring them in
// Close, exactly as at-spi2-core's own clients do. That is session-wide state,
// which is why a client that never asks for a tree never touches it.
package linux
