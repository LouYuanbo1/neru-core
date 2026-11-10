//go:build windows

package windows

import (
	"errors"
	"image"
	"math"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Mouse and keyboard injection through SendInput.
//
// This file does not read the accessibility tree. It turns a point, a button
// and a modifier set into input records, because the ax contract's actions are
// things a user would do with a mouse rather than UIA pattern calls.

var (
	procSendInput        = user32.NewProc("SendInput")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procSetCursorPos     = user32.NewProc("SetCursorPos")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000

	// absoluteCoordinateRange is the span an absolute mouse event's dx and dy
	// address across the virtual desktop. Windows maps a value back to a pixel
	// as value * width / 65536, floored.
	absoluteCoordinateRange = 65536

	keyeventfKeyUp = 0x0002

	// wheelDelta is one notch of the wheel as an application reads it, and
	// scrollPixelsPerNotch is how many screen pixels this package treats as a
	// notch. A caller that thinks in notches passes multiples of the latter.
	wheelDelta           = 120
	scrollPixelsPerNotch = 30

	// wheelUnitsPerPixel is that mapping in mouseData units. WHEEL_DELTA is
	// 120 per notch, so one pixel is four units, and an application that
	// supports a high-resolution wheel accumulates the fraction.
	wheelUnitsPerPixel = wheelDelta / scrollPixelsPerNotch

	// maxWheelRecord is the most one wheel record carries, in whole notches
	// that fit the signed 16-bit delta an application reads. A larger value
	// wraps, and the application then scrolls a fraction of the distance or
	// the opposite way.
	maxWheelRecord = math.MaxInt16 / wheelDelta * wheelDelta

	// Virtual keys for the buttons and modifiers this file injects or reads.
	vkLButton  = 0x01
	vkRButton  = 0x02
	vkMButton  = 0x04
	vkLShift   = 0xA0
	vkRShift   = 0xA1
	vkLControl = 0xA2
	vkRControl = 0xA3
	vkLMenu    = 0xA4
	vkRMenu    = 0xA5
	vkLWin     = 0x5B
	vkRWin     = 0x5C
)

// mouseInput and inputRecord mirror Win32 MOUSEINPUT/INPUT on 64-bit Windows
// (40 bytes). SendInput rejects the wrong size with ERROR_INVALID_PARAMETER,
// so the size is asserted at compile time below rather than trusted.
type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	_           uint32
	dwExtraInfo uintptr
}

type inputRecord struct {
	inputType uint32
	_         uint32
	mi        mouseInput
}

// keyboardInput and keyInputRecord mirror Win32 KEYBDINPUT/INPUT. KEYBDINPUT
// is the smaller arm of the INPUT union, so the trailing padding is what
// brings the record up to the 40 bytes SendInput's cbSize demands.
type keyboardInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	_           uint32
	dwExtraInfo uintptr
}

type keyInputRecord struct {
	inputType uint32
	_         uint32
	ki        keyboardInput
	_         [8]byte
}

// The compile-time guard: a record of the wrong size is a silent no-op at run
// time, so a layout change has to fail the build instead.
var (
	_ [40 - unsafe.Sizeof(inputRecord{})]byte
	_ [40 - unsafe.Sizeof(keyInputRecord{})]byte
)

// inputMu serializes injections that falsify keyboard state. A hold reads the
// keyboard and then changes it, so two overlapping holds would each plan
// against the other's edits and could leave the user's physically held
// modifier released. The lock is held from the read to the restore.
var inputMu sync.Mutex

// errSendInput is returned when SendInput reports that it inserted nothing.
var errSendInput = errors.New("SendInput inserted no events")

// sendRecord posts one already-filled INPUT record.
func sendRecord(record unsafe.Pointer, size uintptr) error {
	inserted, _, callErr := procSendInput.Call(1, uintptr(record), size)
	if inserted == 0 {
		if cause := win32Error(callErr); cause != nil {
			return errs.Wrapf(cause, errs.CodeInternal, "SendInput failed")
		}

		return errs.Wrap(errSendInput, errs.CodeInternal, "SendInput failed")
	}

	return nil
}

// sendMouseEvent posts one MOUSEEVENTF_* event with mouseData set.
func sendMouseEvent(flags, data uint32) error {
	var record inputRecord

	record.inputType = inputMouse
	record.mi.dwFlags = flags
	record.mi.mouseData = data

	return sendRecord(unsafe.Pointer(&record), unsafe.Sizeof(record))
}

// sendMouseMove posts one MOUSEEVENTF_MOVE. The flags decide what the deltas
// mean: absolute virtual-desktop coordinates, or a relative move.
func sendMouseMove(flags uint32, deltaX, deltaY int32) error {
	var record inputRecord

	record.inputType = inputMouse
	record.mi.dwFlags = flags
	record.mi.dx = deltaX
	record.mi.dy = deltaY

	return sendRecord(unsafe.Pointer(&record), unsafe.Sizeof(record))
}

// sendKeyEvent presses or releases one virtual key.
func sendKeyEvent(virtualKey uint16, isUp bool) error {
	var record keyInputRecord

	record.inputType = inputKeyboard
	record.ki.wVk = virtualKey
	if isUp {
		record.ki.dwFlags = keyeventfKeyUp
	}

	return sendRecord(unsafe.Pointer(&record), unsafe.Sizeof(record))
}

// keyDown reports whether a virtual key is held right now. GetAsyncKeyState
// answers both sides of the keyboard for a mouse button and for the left/right
// key codes this file uses, and reads the state the input queue sees, so an
// injection is visible to it.
func keyDown(virtualKey uint16) bool {
	state, _, _ := procGetAsyncKeyState.Call(uintptr(virtualKey))

	return state&0x8000 != 0
}

// moveCursorTo moves the pointer to point with one absolute SendInput move.
//
// An absolute event rather than SetCursorPos is what the contract asks for,
// and it is also what makes the move visible to an application that is
// watching pointer input rather than the cursor position.
func moveCursorTo(point image.Point) error {
	desktop := virtualScreenBounds()

	return sendMouseMove(
		mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk,
		absoluteCoordinate(point.X, desktop.Min.X, desktop.Dx()),
		absoluteCoordinate(point.Y, desktop.Min.Y, desktop.Dy()),
	)
}

// absoluteCoordinate maps a pixel on one axis of the virtual desktop onto the
// absolute range so that Windows' floor(value * size / 65536) lands on that
// pixel again: the smallest value whose product reaches the pixel.
func absoluteCoordinate(pixel, origin, size int) int32 {
	if size <= 0 {
		return 0
	}

	offset := min(max(pixel-origin, 0), size-1)
	value := (offset*absoluteCoordinateRange + size - 1) / size

	return int32(min(value, absoluteCoordinateRange-1))
}

// cursorPosition returns where the pointer is, in physical screen pixels.
func cursorPosition() (image.Point, error) {
	var position win32Point

	ok, _, callErr := procGetCursorPos.Call(uintptr(unsafe.Pointer(&position)))
	if ok == 0 {
		return image.Point{}, win32Failure("GetCursorPos", callErr)
	}

	return image.Point{X: int(position.x), Y: int(position.y)}, nil
}

// setCursorPosition warps the pointer to point. It is used to undo a move, not
// to make one, because a warp generates no pointer motion for an application
// to read.
func setCursorPosition(point image.Point) error {
	ok, _, callErr := procSetCursorPos.Call(uintptr(point.X), uintptr(point.Y))
	if ok == 0 {
		return win32Failure("SetCursorPos", callErr)
	}

	return nil
}

// buttonFlags holds the SendInput flags that press and release one button.
type buttonFlags struct {
	down uint32
	up   uint32
}

// flagsForButton returns the SendInput flags addressing the given button. An
// unknown button falls back to the left one, which is the only sensible
// default for a click.
func flagsForButton(button action.MouseButton) buttonFlags {
	switch button {
	case action.ButtonRight:
		return buttonFlags{down: mouseeventfRightDown, up: mouseeventfRightUp}
	case action.ButtonMiddle:
		return buttonFlags{down: mouseeventfMiddleDown, up: mouseeventfMiddleUp}
	case action.ButtonLeft:
		return buttonFlags{down: mouseeventfLeftDown, up: mouseeventfLeftUp}
	default:
		return buttonFlags{down: mouseeventfLeftDown, up: mouseeventfLeftUp}
	}
}

// virtualKeyForButton returns the virtual key whose state reports whether the
// given button is held.
func virtualKeyForButton(button action.MouseButton) uint16 {
	switch button {
	case action.ButtonRight:
		return vkRButton
	case action.ButtonMiddle:
		return vkMButton
	case action.ButtonLeft:
		return vkLButton
	default:
		return vkLButton
	}
}

// buttonDown reports whether a mouse button is held, whoever pressed it.
func buttonDown(button action.MouseButton) bool {
	return keyDown(virtualKeyForButton(button))
}

// sendButtonPhase presses or releases one button at the cursor.
func sendButtonPhase(button action.MouseButton, isUp bool) error {
	flags := flagsForButton(button)
	if isUp {
		return sendMouseEvent(flags.up, 0)
	}

	return sendMouseEvent(flags.down, 0)
}

// clickButton presses and releases one button at the cursor.
func clickButton(button action.MouseButton) error {
	if err := sendButtonPhase(button, false); err != nil {
		return err
	}

	return sendButtonPhase(button, true)
}

// modifierFamily is one physical modifier a chord can hold: the bit that names
// it, and the two virtual keys that can present it.
//
// Both sides of the keyboard are listed because either of them presents the
// modifier, so suppressing only the left key suppresses nothing when the user
// holds the right one. The left key is canonical: it is what this file presses
// when the caller asks for a modifier that is not held.
type modifierFamily struct {
	modifier action.Modifiers
	left     uint16
	right    uint16
}

// modifierFamilies is every modifier in the order a chord presses them. ModCmd
// maps to the Windows key, which is the closest thing Windows has to the
// platform's command modifier.
var modifierFamilies = [...]modifierFamily{
	{modifier: action.ModCmd, left: vkLWin, right: vkRWin},
	{modifier: action.ModCtrl, left: vkLControl, right: vkRControl},
	{modifier: action.ModAlt, left: vkLMenu, right: vkRMenu},
	{modifier: action.ModShift, left: vkLShift, right: vkRShift},
}

// modifierHold records what one injection falsified so release can put the
// keyboard back as it found it.
type modifierHold struct {
	// pressed are the keys this hold pressed, which release must let go of.
	pressed []uint16
	// suppressed are the keys this hold released, which release must press
	// back if they are still up.
	suppressed []uint16
}

// holdModifiers makes the keyboard present exactly modifiers, and returns the
// hold that undoes it. The caller must release the hold on every path,
// including failure.
//
// A SendInput mouse event carries no modifier field, unlike a CGEvent, which
// takes flags. Whatever the keyboard holds is part of every injected event, so
// a plain scroll bound to Ctrl+J would arrive as ctrl+scroll and most
// applications read that as zoom. The only way to present a set is to make the
// keyboard actually hold it: release what is held and was not asked for, press
// what was asked for and is not held, and undo both afterwards.
//
// A key already held that was asked for is left exactly as it is, because a
// key is one bit of OS state rather than a count. Pressing it a second time
// would make the release that follows tell every application the user let go
// while they are still holding it.
//
// The hold owns inputMu until release, so two injections cannot each plan
// against the other's edits.
func holdModifiers(modifiers action.Modifiers) (*modifierHold, error) {
	inputMu.Lock()

	hold := &modifierHold{}

	for _, family := range modifierFamilies {
		if modifiers.Has(family.modifier) {
			if keyDown(family.left) || keyDown(family.right) {
				continue
			}

			if err := sendKeyEvent(family.left, false); err != nil {
				hold.releaseLocked()
				inputMu.Unlock()

				return nil, err
			}

			hold.pressed = append(hold.pressed, family.left)

			continue
		}

		for _, key := range [...]uint16{family.left, family.right} {
			if !keyDown(key) {
				continue
			}

			if err := sendKeyEvent(key, true); err != nil {
				hold.releaseLocked()
				inputMu.Unlock()

				return nil, err
			}

			hold.suppressed = append(hold.suppressed, key)
		}
	}

	return hold, nil
}

// release presses back what the hold suppressed, lets go of what it pressed,
// and frees the injection lock.
func (h *modifierHold) release() {
	h.releaseLocked()
	inputMu.Unlock()
}

// releaseLocked is release without the unlock, so a failed hold can undo
// itself before it hands the lock back.
//
// A suppressed key that reads down again is left alone: something pressed it
// after this hold released it, and pressing it once more would leave a key
// held that nothing ever lets go of. Errors are dropped because a restore that
// fails has nothing better to try, and reporting it would mask the outcome of
// the action it wraps.
func (h *modifierHold) releaseLocked() {
	for _, key := range h.pressed {
		_ = sendKeyEvent(key, true)
	}

	h.pressed = nil

	for _, key := range h.suppressed {
		if keyDown(key) {
			continue
		}

		_ = sendKeyEvent(key, false)
	}

	h.suppressed = nil
}

// wheelEvent is one MOUSEEVENTF_WHEEL or MOUSEEVENTF_HWHEEL record, before it
// is posted.
type wheelEvent struct {
	flags uint32
	data  uint32
}

// wheelEvents turns a pixel scroll delta into the wheel records SendInput
// needs, one per axis that moves.
//
// The deltas follow the library's shared convention: positive deltaY scrolls
// up and positive deltaX scrolls left. MOUSEEVENTF_WHEEL agrees on the
// vertical sign, but MOUSEEVENTF_HWHEEL reads positive as right, so the
// horizontal component is negated.
func wheelEvents(deltaX, deltaY int) []wheelEvent {
	return wheelRecords(int32(deltaY)*wheelUnitsPerPixel, int32(-deltaX)*wheelUnitsPerPixel)
}

// wheelRecords is wheelEvents below the sign convention. Vertical and
// horizontal are already in WHEEL_DELTA units with Win32's signs, and a zero
// axis sends nothing.
func wheelRecords(vertical, horizontal int32) []wheelEvent {
	var events []wheelEvent

	events = appendWheelRecords(events, mouseeventfWheel, vertical)
	events = appendWheelRecords(events, mouseeventfHWheel, horizontal)

	return events
}

// appendWheelRecords splits one axis into records of at most maxWheelRecord
// units each, so a long scroll travels its whole distance in the direction
// asked instead of wrapping to a fraction of it.
func appendWheelRecords(events []wheelEvent, flags uint32, units int32) []wheelEvent {
	for units != 0 {
		step := min(max(units, -maxWheelRecord), maxWheelRecord)
		events = append(events, wheelEvent{flags: flags, data: uint32(step)})
		units -= step
	}

	return events
}

// scrollWheel posts a pixel scroll at the cursor, presenting exactly
// modifiers as held for the duration.
func scrollWheel(deltaX, deltaY int, modifiers action.Modifiers) error {
	if deltaX == 0 && deltaY == 0 {
		return nil
	}

	hold, err := holdModifiers(modifiers)
	if err != nil {
		return err
	}

	defer hold.release()

	for _, event := range wheelEvents(deltaX, deltaY) {
		if err := sendMouseEvent(event.flags, event.data); err != nil {
			return err
		}
	}

	return nil
}
