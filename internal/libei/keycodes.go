package libei

// Linux evdev mouse button codes, from <linux/input-event-codes.h>. libei's
// button call carries one of these, exactly like the wlroots virtual pointer
// and /dev/uinput do, and the compositor routes it to the focused client.
const (
	buttonCodeLeft   uint32 = 0x110 // BTN_LEFT, 272
	buttonCodeRight  uint32 = 0x111 // BTN_RIGHT, 273
	buttonCodeMiddle uint32 = 0x112 // BTN_MIDDLE, 274
)

// buttonSelectorLeft, buttonSelectorMiddle and buttonSelectorRight are the
// small numbering Button documents (1 left, 2 middle, 3 right), the same
// numbering X11 uses for its button events.
const (
	buttonSelectorLeft   = 1
	buttonSelectorMiddle = 2
	buttonSelectorRight  = 3
)

// evdevButton maps a caller's button selector to the evdev code libei carries.
//
// Both numberings are accepted and cannot collide: the selectors are 1..3 and
// every BTN_* code is far above them, so a caller holding either convention
// gets the button it named. Anything else is refused rather than passed through
// as an arbitrary key code, which the compositor would interpret as a keyboard
// event on a device that has no keyboard capability.
func evdevButton(button int) (uint32, bool) {
	switch button {
	case buttonSelectorLeft, int(buttonCodeLeft):
		return buttonCodeLeft, true
	case buttonSelectorMiddle, int(buttonCodeMiddle):
		return buttonCodeMiddle, true
	case buttonSelectorRight, int(buttonCodeRight):
		return buttonCodeRight, true
	default:
		return 0, false
	}
}
