//go:build linux

// Package libei injects pointer, wheel and keyboard input through libei, the
// Emulated Input client library, without cgo.
//
// It is the Linux answer for Wayland compositors that implement no
// virtual-pointer protocol of their own — KWin, Mutter and cosmic-comp among
// them. Those compositors do expose the xdg-desktop-portal RemoteDesktop
// interface, which shows a consent prompt, creates an input session and hands
// back a connected EIS socket; libei then speaks the EIS protocol over that
// socket. The library is bound with purego, so nothing links against libei at
// build time and the module still builds without a C toolchain.
//
// # What a client does
//
// Dial runs the portal handshake and attaches libei to the socket, then pumps
// the event loop until the granted devices are usable. The returned Client
// carries MoveTo, Button, Scroll and Key, all of which take the library's shared
// conventions: global logical pixels, +y up and +x left for scroll, evdev key
// codes for keys. Close releases the libei context, the EIS socket it owns and
// the portal session.
//
// A Client is safe for concurrent use. Its methods serialize on one lock,
// because libei is not thread-safe and because two interleaved frames would
// merge into one event.
//
// # The lifecycle in one paragraph
//
// The portal session is created with CreateSession, SelectDevices (pointer and
// keyboard), Start (the consent dialog) and ConnectToEIS (the socket). libei
// creates a sender with ei_new_sender, names it with ei_configure_name, and
// attaches it with ei_setup_backend_fd; from that call on libei owns the
// descriptor, and ei_unref closes it. The seat's capabilities are bound in two
// steps because KWin drops the keyboard when keyboard and pointer are bound
// together (libei_client.c in the reference implementation documents the same
// workaround). Every emit is wrapped in ei_device_start_emulating and a
// ei_device_frame carrying ei_now, and the socket is pumped before and after so
// a pause, resume or removal is seen between actions.
//
// # What is degraded, and why
//
// A missing libei, a missing session bus or a portal without ConnectToEIS
// returns errs.CodeNotSupported with the remedy in the message; a canceled
// consent prompt or a desktop policy that refuses returns
// errs.CodeAccessibilityDenied; a session the compositor never brings up
// returns errs.CodeTimeout. A granted session without a keyboard is not a
// failure: the pointer still works, Key reports the missing grant, and
// HasKeyboard answers the question for the caller.
//
// The portal is asked for no restore token and no persist_mode, so every Dial
// shows one consent dialog. Persisting a grant would mean storing a credential,
// and a library that keeps no state on disk cannot revoke one; a caller that
// wants a prompt-free start has to own that decision.
//
// Nothing here logs input. The package has no logger and never formats a
// coordinate, a key code or a scroll delta into an error or a message.
//
// # System dependencies
//
// libei.so.1 at run time (Debian/Ubuntu libei1, Fedora libei), a D-Bus session
// bus, and an xdg-desktop-portal with the RemoteDesktop interface at version 2
// or later for ConnectToEIS (xdg-desktop-portal 1.18+). No cgo, and no
// compile-time linkage to any of them.
package libei
