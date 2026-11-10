package wlrinject

import _ "embed"

// keymapXKB is the XKB keymap the virtual keyboard uploads before it may send
// a key. It is a serialized XKB_KEYMAP_FORMAT_TEXT_V1 keymap: the exact text
// xkb_keymap_get_as_string produces for a standard evdev/pc105/us
// configuration, the same RMLVO the C client in neru's Linux adapter compiles
// with xkb_keymap_new_from_names.
//
// It is embedded instead of generated because this library is pure Go and
// cannot link libxkbcommon. Keeping the serialized form rather than a five-line
// include-based keymap also means the compositor needs no xkeyboard-config
// files to compile it: the text is self-contained, with every keycode name,
// type, compatibility rule and symbol already resolved. The compositor compiles
// it with xkbcommon and derives its wl_keyboard modifier indexes from it; the
// modifier names it looks up (Shift, Caps, Control, Mod1, Mod2, Mod4) are all
// bound here.
//
// A serialized host keymap keeps every layout group it was generated with. Only
// group 1 (the US table) is ever selected, because this client always reports
// group 0 in zwp_virtual_keyboard_v1.modifiers. That is intentional: the keymap
// serves injection, not the user's own typing, and modifier masks are
// layout-independent.
//
// The file is 54 KB, well below the protocol's uint32 size field and far below
// the message size ceiling, because its contents travel in a file descriptor
// rather than in the request body.
//
// It is libxkbcommon's own serialized host keymap fixture
// (test/data/keymaps/host.xkb in the libxkbcommon project, MIT licensed),
// generated from a US pc105 layout. The fixture also carries secondary layout
// groups because its host had more than one layout configured; this client
// always reports group 0, so only the US group is ever active. The text is
// round-tripped through xkb_keymap_new_from_string by xkbcommon's own test
// suite, so it is known to compile.
//
//go:embed keymap.xkb
var keymapXKB string

// keymapFormatXKBV1 is wl_keyboard.keymap_format's XKB v1 text value. The only
// format the virtual keyboard protocol accepts in practice.
const keymapFormatXKBV1 = keyboardKeymapFormatXKBV1
