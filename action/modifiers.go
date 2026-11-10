package action

import (
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Modifiers is a bitmask of keyboard modifier keys held during an action.
type Modifiers uint64

const (
	// ModCmd is the Command (⌘) modifier.
	ModCmd Modifiers = 1 << iota
	// ModShift is the Shift (⇧) modifier.
	ModShift
	// ModAlt is the Option/Alt (⌥) modifier.
	ModAlt
	// ModCtrl is the Control (⌃) modifier.
	ModCtrl
)

// modifierNames maps lowercase modifier names to their bitmask values.
var modifierNames = map[string]Modifiers{
	"cmd":     ModCmd,
	"command": ModCmd,
	"super":   ModCmd,
	"meta":    ModCmd,
	"shift":   ModShift,
	"alt":     ModAlt,
	"option":  ModAlt,
	"ctrl":    ModCtrl,
	"control": ModCtrl,
}

// primaryModifier is the modifier Primary stands for in this process, and
// zero until UsePrimaryModifier sets it.
var primaryModifier atomic.Uint64

// UsePrimaryModifier makes mod what Primary stands for in this process.
func UsePrimaryModifier(mod Modifiers) {
	primaryModifier.Store(uint64(mod))
}

// PrimaryModifier returns the modifier Primary stands for. Until
// UsePrimaryModifier sets one, that is the platform's default accelerator,
// Command on macOS and Control on Linux and Windows.
func PrimaryModifier() Modifiers {
	if mod := Modifiers(primaryModifier.Load()); mod != 0 {
		return mod
	}

	if runtime.GOOS == "darwin" {
		return ModCmd
	}

	return ModCtrl
}

// ParseModifiers parses a comma-separated modifier string (e.g. "cmd,shift")
// into a Modifiers bitmask. An empty string returns 0 (no modifiers).
func ParseModifiers(input string) (Modifiers, error) {
	if input == "" {
		return 0, nil
	}

	var mods Modifiers
	for part := range strings.SplitSeq(input, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if name == "" {
			continue
		}

		if name == "primary" {
			mods |= PrimaryModifier()

			continue
		}

		mod, ok := modifierNames[name]
		if !ok {
			return 0, errs.Newf(errs.CodeInvalidInput, "unknown modifier: %s", name)
		}

		mods |= mod
	}

	return mods, nil
}

// Has reports whether all the given modifiers are set.
func (m Modifiers) Has(mods Modifiers) bool {
	return m&mods == mods
}

// String returns a human-readable, comma-separated form of the modifiers.
func (m Modifiers) String() string {
	if m == 0 {
		return ""
	}

	parts := make([]string, 0, 4)
	if m&ModCmd != 0 {
		parts = append(parts, "cmd")
	}
	if m&ModShift != 0 {
		parts = append(parts, "shift")
	}
	if m&ModAlt != 0 {
		parts = append(parts, "alt")
	}
	if m&ModCtrl != 0 {
		parts = append(parts, "ctrl")
	}

	return strings.Join(parts, ",")
}
