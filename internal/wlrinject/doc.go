// Package wlrinject injects pointer and keyboard events into a wlroots
// compositor through the two unstable protocols that compositor family
// implements, in pure Go.
//
// # Why a second Wayland input path
//
// The RemoteDesktop portal plus libei is the portable answer on a native
// Wayland session (see the libei package), but it is only *available* where the
// compositor implements the portal's libei path: KDE/KWin, GNOME/Mutter and
// cosmic. The wlroots family — sway, niri, Hyprland and wayfire — does not, and
// instead exposes two unstable protocols to any client on the session:
//
//   - zwlr_virtual_pointer_manager_v1 creates a zwlr_virtual_pointer_v1, which
//     accepts absolute motion, buttons and wheel/touchpad axes.
//   - zwp_virtual_keyboard_manager_v1 creates a zwp_virtual_keyboard_v1, which
//     accepts an XKB keymap, raw key events and an explicit modifier state.
//
// Both are unauthenticated: there is no consent dialog and no portal session,
// which is what makes them the low-latency path on those compositors.
//
// # Protocol flow
//
// One Client owns one Wayland connection. Dial talks the core protocol:
//
//	wl_display.get_registry  -> wl_registry.global events
//	wl_registry.bind         -> wl_seat, wl_output, both managers
//	wl_seat.capabilities     -> pick the first seat that has a pointer
//	wl_output.geometry/mode/scale -> the desktop layout and its extents
//	manager.create_virtual_pointer / create_virtual_keyboard
//	zwp_virtual_keyboard_v1.keymap (see below)
//
// Nothing creates a wl_surface and the client never takes pointer or keyboard
// focus, so connecting does not disturb the session. Each method then writes
// its requests followed by wl_display.sync and reads until that callback's done
// event, so a method returns only once the compositor has processed the action.
// That roundtrip is the whole synchronization discipline; there is no
// background reader goroutine and no queue of pending events. One mutex
// serializes the methods.
//
// Every wait is bounded by a two-second socket deadline and reports
// errs.CodeTimeout. A connection that times out, breaks, or receives a
// wl_display.error is poisoned: the socket is closed and every later call fails
// with the same error instead of reading replies that belong to an abandoned
// request. A protocol failure is errs.CodeAccessibilityFailed, and a compositor
// that exposes neither protocol (or no Wayland socket) is
// errs.CodeNotSupported.
//
// # Frames and commits
//
// zwlr_virtual_pointer_v1 has a frame request and deliberately no commit
// request: in version 1 and 2 the frame *is* the end of a gesture. Each method
// groups its requests — motion_absolute; or button; or axis/axis_discrete plus
// axis_source — then sends one frame, and the sync roundtrip flushes the whole
// group. There is no separate commit to send, and inventing one would be an
// unknown opcode the compositor rejects.
//
// zwp_virtual_keyboard_v1 has neither frame nor commit: key and modifiers are
// applied in request order, and the protocol's own XML documents no grouping.
// The keymap request must precede both, which is why Dial uploads it and does
// not create the keyboard until the keymap roundtrip has completed.
//
// # The keymap and key codes
//
// A virtual keyboard must supply its own XKB keymap before it can send a key.
// The compositor compiles it with xkbcommon and resolves the injected keycodes
// through it, and wlroots additionally derives its modifier indexes from it.
// This package embeds a serialized XKB_KEYMAP_FORMAT_TEXT_V1 pc105/us keymap
// (keymap.xkb) instead of asking the caller for one: the library is pure Go and
// cannot link libxkbcommon, and a self-contained keymap means the compositor
// needs no xkeyboard-config files to compile it.
//
// The keymap is written to an unlinked shared-memory file and its descriptor is
// passed with SCM_RIGHTS on the keymap request. The descriptor is closed as soon
// as the request is on the wire; the compositor keeps its own reference.
//
// Key events carry Linux evdev codes, not XKB keycodes: the wire's key argument
// is the same raw code wl_keyboard carries, and xkbcommon's keycode is that
// code plus 8. The embedded keymap is written in the xkbcommon numbering
// (<LCTL> = 37, evdev 29), so the two never need converting on the way out.
//
// zwp_virtual_keyboard_v1 does not derive modifier state from key events:
// wlroots forwards virtual keys with update_state disabled, so the compositor's
// xkb state changes only when the client sends zwp_virtual_keyboard_v1.modifiers.
// Key therefore tracks the four modifiers the library uses and reports the new
// mask before the key event of a modifier press or release. The masks are XKB
// real-modifier bits: Shift is 1<<0, Control 1<<2, Mod1 (alt) 1<<3 and Mod4
// (meta/super) 1<<6. The evdev codes are the ones the rest of the library
// already uses: ctrl 29, shift 42, alt 56, meta 125, plus their right-hand
// twins 97, 54, 100 and 126.
//
// # Coordinates and scrolling
//
// Wayland has no global pointer coordinate. motion_absolute takes a fraction of
// an extent, so MoveTo subtracts the top-left corner of the union of the
// wl_output rectangles (the layout can start at a negative coordinate) and
// passes the union's width and height as the extents. The layout is read once,
// at Dial; a session whose output layout changes needs a new Client.
//
// Scroll uses the library's shared convention (dx positive left, dy positive
// up) and converts pixels to notches at 30 px per notch. wl_pointer.axis is
// positive right and positive down, so both axes flip, and one notch is 10
// units. When the seat advertises version 8 or later, the client has wl_pointer
// v8 (axis_value120) available, and a whole-notch scroll is sent as
// axis_discrete, whose notch count wlroots turns into axis_value120 at 120 units
// per notch; a fractional scroll stays a plain axis event on every compositor.
// The axis source is wheel for a whole notch and continuous for a fraction, so a
// toolkit does not snap a fractional delta back to a detent.
//
// # Degradations
//
// Pointer and keyboard are independent: a compositor that exposes only one
// manager yields a Client with HasPointer or HasKeyboard false, and the other
// device's methods keep working. Key reports CodeNotSupported rather than
// silently dropping a modifier hold when no virtual keyboard exists. Fractional
// output scales are rounded to logical pixels when the layout extents are
// computed; a click can be off by less than one logical pixel. Pointer
// coordinates and key codes are never logged.
package wlrinject
