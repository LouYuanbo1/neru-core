package uinput

// modifierKeycodes are the evdev key codes Open advertises and Key accepts.
//
// The device this package creates is a scroll wheel that can also hold a
// modifier, which is what a modified scroll needs: pressing ctrl on the same
// device that scrolls means the compositor sees one device's state instead of
// two it has to merge. Holding anything else through this device is not a case
// this backend was built for, so the key set stays at the eight modifiers and
// Key refuses the rest rather than silently dropping the event.
//
// Values are KEY_* from <linux/input-event-codes.h>.
var modifierKeycodes = [...]int{
	29,  // KEY_LEFTCTRL
	97,  // KEY_RIGHTCTRL
	42,  // KEY_LEFTSHIFT
	54,  // KEY_RIGHTSHIFT
	56,  // KEY_LEFTALT
	100, // KEY_RIGHTALT
	125, // KEY_LEFTMETA
	126, // KEY_RIGHTMETA
}

// isModifierKeycode reports whether code is one of the advertised keys.
func isModifierKeycode(code int) bool {
	for _, keycode := range modifierKeycodes {
		if keycode == code {
			return true
		}
	}

	return false
}
