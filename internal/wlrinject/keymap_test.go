package wlrinject

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// keymapModifierKeys are the pc105 modifier keycodes the keymap has to name, as
// evdev codes. The keymap is written in xkbcommon's numbering, which is each
// code plus eight.
var keymapModifierKeys = []struct {
	name  string
	evdev uint32
}{
	{name: "left ctrl", evdev: 29},
	{name: "right ctrl", evdev: 97},
	{name: "left shift", evdev: 42},
	{name: "right shift", evdev: 54},
	{name: "left alt", evdev: 56},
	{name: "right alt", evdev: 100},
	{name: "left meta", evdev: 125},
	{name: "right meta", evdev: 126},
}

func TestKeymap_IsASelfContainedXKBV1Text(t *testing.T) {
	const (
		// A serialized pc105/us keymap is tens of kilobytes. A much smaller text
		// would mean the embed silently truncated to something xkbcommon cannot
		// compile, and a much larger one would not fit the protocol's uint32 size
		// field sensibly.
		minSize = 10_000
		maxSize = 1 << 20
	)

	if len(keymapXKB) < minSize {
		t.Fatalf("keymap is %d bytes, want at least %d (a full serialized keymap)", len(keymapXKB), minSize)
	}

	if len(keymapXKB) > maxSize {
		t.Fatalf("keymap is %d bytes, want at most %d", len(keymapXKB), maxSize)
	}

	if strings.ContainsRune(keymapXKB, 0) {
		t.Error("keymap contains a NUL before the one uploadKeymap appends; xkbcommon would stop at it")
	}

	// A self-contained keymap has no include directives: the compositor must be
	// able to compile it without xkeyboard-config installed.
	if strings.Contains(keymapXKB, "include \"") {
		t.Error("keymap contains an include directive, so the compositor needs xkeyboard-config to compile it")
	}

	trimmed := strings.TrimSpace(keymapXKB)
	if !strings.HasPrefix(trimmed, "xkb_keymap {") {
		t.Errorf("keymap does not start with the xkb_keymap block: %q", firstLine(keymapXKB))
	}

	for _, section := range []string{
		"xkb_keycodes",
		"xkb_types",
		"xkb_compatibility",
		"xkb_symbols",
	} {
		if !strings.Contains(keymapXKB, section) {
			t.Errorf("keymap has no %s section", section)
		}
	}

	if !strings.HasSuffix(trimmed, "};") {
		t.Errorf("keymap does not end with the closing block: %q", lastLine(keymapXKB))
	}
}

func TestKeymap_BindsThePc105ModifierKeycodes(t *testing.T) {
	for _, key := range keymapModifierKeys {
		// The keycodes section spells each name once; tolerate the alignment
		// padding xkbcommon's serializer uses.
		pattern := fmt.Sprintf(`(?m)<%s>\s*=\s*%d\s*;`, keyName(key.evdev), xkbKeycode(key.evdev))

		if !regexp.MustCompile(pattern).MatchString(keymapXKB) {
			t.Errorf("keymap does not bind <%s> to xkb keycode %d (evdev %d)", keyName(key.evdev), xkbKeycode(key.evdev), key.evdev)
		}
	}
}

func TestKeymap_BindsTheModifierMasksAndKeysyms(t *testing.T) {
	for _, want := range []string{
		"modifier_map Shift { <LFSH>, <RTSH> };",
		"modifier_map Control { <LCTL>, <RCTL> };",
		"modifier_map Mod1 { <LALT>, <META> };",
		"modifier_map Mod4 { <LWIN>, <RWIN>, <SUPR>, <HYPR> };",
		"Control_L",
		"Shift_L",
		"Alt_L",
		"Super_L",
	} {
		if !strings.Contains(keymapXKB, want) {
			t.Errorf("keymap does not contain %q, so wlroots cannot derive that modifier", want)
		}
	}
}

func TestKeymap_SelectsOnlyTheUSGroup(t *testing.T) {
	// Group 1 is the US table; the client always reports group 0, so a keymap
	// generated on a host with extra layouts is still deterministic. Checking a
	// letter distinguishes group 1 from any secondary layout in the same file.
	if !strings.Contains(keymapXKB, "symbols[1]= [               q,               Q ]") {
		t.Error("keymap's first group is not the US qwerty table")
	}
}

// keyName maps an evdev modifier code to the pc105 keycode name the keymap uses.
func keyName(evdev uint32) string {
	switch evdev {
	case 29:
		return "LCTL"
	case 97:
		return "RCTL"
	case 42:
		return "LFSH"
	case 54:
		return "RTSH"
	case 56:
		return "LALT"
	case 100:
		return "RALT"
	case 125:
		return "LWIN"
	case 126:
		return "RWIN"
	default:
		return fmt.Sprintf("UNKNOWN%d", evdev)
	}
}

// firstLine returns the first line of s, for failure messages.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

// lastLine returns the last non-empty line of s, for failure messages.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")

	return lines[len(lines)-1]
}
