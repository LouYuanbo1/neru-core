// Package action defines the neutral execution model for mouse actions.
//
// It is platform-independent: a Type says what to do, a MouseButton says on
// which button, and a MousePhase says which part of a press it is. Consumers
// that trigger the underlying input translate this model into their own native
// calls. Everything here is about *executing* input, not about config grammar,
// so no application-specific naming vocabulary is included.
package action

import "github.com/LouYuanbo1/neru-core/errs"

// unknownLabel is the String() result for values outside the defined enums.
const unknownLabel = "unknown"

// Type is the type of action to perform on a UI element.
type Type int

const (
	// TypeLeftClick performs a left mouse click.
	TypeLeftClick Type = iota
	// TypeRightClick performs a right mouse click.
	TypeRightClick
	// TypeMiddleClick performs a middle mouse click.
	TypeMiddleClick
	// TypeLeftMouseDown presses and holds the left mouse button.
	TypeLeftMouseDown
	// TypeLeftMouseUp releases the left mouse button.
	TypeLeftMouseUp
	// TypeMoveMouse moves the mouse cursor to a specific point (absolute).
	TypeMoveMouse
	// TypeMoveMouseRelative moves the mouse cursor relative to current position.
	TypeMoveMouseRelative
	// TypeScroll performs a scroll action.
	TypeScroll
	// TypeRightMouseDown presses and holds the right mouse button.
	TypeRightMouseDown
	// TypeRightMouseUp releases the right mouse button.
	TypeRightMouseUp
	// TypeMiddleMouseDown presses and holds the middle mouse button.
	TypeMiddleMouseDown
	// TypeMiddleMouseUp releases the middle mouse button.
	TypeMiddleMouseUp
	// TypeLeftMouseToggle releases the left mouse button when held, presses it otherwise.
	TypeLeftMouseToggle
	// TypeRightMouseToggle releases the right mouse button when held, presses it otherwise.
	TypeRightMouseToggle
	// TypeMiddleMouseToggle releases the middle mouse button when held, presses it otherwise.
	TypeMiddleMouseToggle
)

// MouseButton identifies a physical mouse button.
type MouseButton int

const (
	// ButtonLeft is the left mouse button.
	ButtonLeft MouseButton = iota
	// ButtonRight is the right mouse button.
	ButtonRight
	// ButtonMiddle is the middle mouse button.
	ButtonMiddle
)

// String returns the string representation of the mouse button.
func (b MouseButton) String() string {
	switch b {
	case ButtonLeft:
		return "left"
	case ButtonRight:
		return "right"
	case ButtonMiddle:
		return "middle"
	default:
		return unknownLabel
	}
}

// MouseButtons returns every mouse button in release priority order.
func MouseButtons() []MouseButton {
	return []MouseButton{ButtonLeft, ButtonRight, ButtonMiddle}
}

// MousePhase describes which part of a button press a mouse action performs.
type MousePhase int

const (
	// PhaseClick presses and releases the button in one action.
	PhaseClick MousePhase = iota
	// PhaseDown presses and holds the button.
	PhaseDown
	// PhaseUp releases a held button.
	PhaseUp
	// PhaseToggle presses the button when it is up and releases it when it is held.
	PhaseToggle
)

// String returns the string representation of the mouse phase.
func (p MousePhase) String() string {
	switch p {
	case PhaseClick:
		return "click"
	case PhaseDown:
		return "down"
	case PhaseUp:
		return "up"
	case PhaseToggle:
		return "toggle"
	default:
		return unknownLabel
	}
}

// ParsePhase parses a phase string into a mouse phase. Click and toggle are
// not expressible as single-phase values, so only down and up parse.
func ParsePhase(phaseString string) (MousePhase, error) {
	switch phaseString {
	case "down":
		return PhaseDown, nil
	case "up":
		return PhaseUp, nil
	default:
		return 0, errs.Newf(
			errs.CodeInvalidInput,
			"unknown mouse button state: %s (expected down or up)",
			phaseString,
		)
	}
}

// MouseButtonPhase decomposes a mouse button action type into the button it
// acts on and the phase it performs. The second return value is false for
// action types that are not mouse button actions.
func (t Type) MouseButtonPhase() (MouseButton, MousePhase, bool) {
	switch t {
	case TypeLeftClick:
		return ButtonLeft, PhaseClick, true
	case TypeRightClick:
		return ButtonRight, PhaseClick, true
	case TypeMiddleClick:
		return ButtonMiddle, PhaseClick, true
	case TypeLeftMouseDown:
		return ButtonLeft, PhaseDown, true
	case TypeLeftMouseUp:
		return ButtonLeft, PhaseUp, true
	case TypeRightMouseDown:
		return ButtonRight, PhaseDown, true
	case TypeRightMouseUp:
		return ButtonRight, PhaseUp, true
	case TypeMiddleMouseDown:
		return ButtonMiddle, PhaseDown, true
	case TypeMiddleMouseUp:
		return ButtonMiddle, PhaseUp, true
	case TypeLeftMouseToggle:
		return ButtonLeft, PhaseToggle, true
	case TypeRightMouseToggle:
		return ButtonRight, PhaseToggle, true
	case TypeMiddleMouseToggle:
		return ButtonMiddle, PhaseToggle, true
	case TypeMoveMouse, TypeMoveMouseRelative, TypeScroll:
		return 0, 0, false
	default:
		return 0, 0, false
	}
}

// String returns the string representation of the action type.
func (t Type) String() string {
	switch t {
	case TypeLeftClick:
		return "left_click"
	case TypeRightClick:
		return "right_click"
	case TypeMiddleClick:
		return "middle_click"
	case TypeLeftMouseDown:
		return "left_mouse_down"
	case TypeLeftMouseUp:
		return "left_mouse_up"
	case TypeRightMouseDown:
		return "right_mouse_down"
	case TypeRightMouseUp:
		return "right_mouse_up"
	case TypeMiddleMouseDown:
		return "middle_mouse_down"
	case TypeMiddleMouseUp:
		return "middle_mouse_up"
	case TypeLeftMouseToggle:
		return "left_mouse_toggle"
	case TypeRightMouseToggle:
		return "right_mouse_toggle"
	case TypeMiddleMouseToggle:
		return "middle_mouse_toggle"
	case TypeMoveMouse:
		return "move_mouse"
	case TypeMoveMouseRelative:
		return "move_mouse_relative"
	case TypeScroll:
		return "scroll"
	default:
		return unknownLabel
	}
}

// ParseType parses a string into an action type.
func ParseType(actionString string) (Type, error) {
	switch actionString {
	case "left_click":
		return TypeLeftClick, nil
	case "right_click":
		return TypeRightClick, nil
	case "middle_click":
		return TypeMiddleClick, nil
	// mouse_down / mouse_up are the original left-button spellings. They stay
	// accepted so existing callers keep working.
	case "left_mouse_down", "mouse_down":
		return TypeLeftMouseDown, nil
	case "left_mouse_up", "mouse_up":
		return TypeLeftMouseUp, nil
	case "right_mouse_down":
		return TypeRightMouseDown, nil
	case "right_mouse_up":
		return TypeRightMouseUp, nil
	case "middle_mouse_down":
		return TypeMiddleMouseDown, nil
	case "middle_mouse_up":
		return TypeMiddleMouseUp, nil
	case "left_mouse_toggle":
		return TypeLeftMouseToggle, nil
	case "right_mouse_toggle":
		return TypeRightMouseToggle, nil
	case "middle_mouse_toggle":
		return TypeMiddleMouseToggle, nil
	case "move_mouse":
		return TypeMoveMouse, nil
	case "move_mouse_relative":
		return TypeMoveMouseRelative, nil
	case "scroll":
		return TypeScroll, nil
	default:
		return 0, errs.Newf(errs.CodeInvalidInput, "unknown action type: %s", actionString)
	}
}

// IsClick returns true if the action is a click type.
func (t Type) IsClick() bool {
	return t == TypeLeftClick || t == TypeRightClick || t == TypeMiddleClick
}

// IsMouseButton returns true if the action involves a mouse button.
func (t Type) IsMouseButton() bool {
	_, _, ok := t.MouseButtonPhase()

	return ok
}

// IsMoveMouse returns true if the action moves the mouse cursor.
func (t Type) IsMoveMouse() bool {
	return t == TypeMoveMouse || t == TypeMoveMouseRelative
}
