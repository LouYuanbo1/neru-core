//go:build darwin

package darwin

import (
	"errors"
	"image"
	"math"
	"time"

	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Timing that makes a synthetic click look like a human one. The window server
// can attribute an up event to a control only after the matching down event
// has been delivered, and restoring the cursor too early can move it before
// the click lands.
const (
	mouseClickDownUpDelay  = 8 * time.Millisecond
	mouseClickSettleDelay  = 50 * time.Millisecond
	doubleClickInterval    = 500 * time.Millisecond
	doubleClickMaxDistance = 5
)

// PerformAction posts the input an action type describes. A mouse button
// action moves the cursor, presses the button at point, and (for a click)
// releases it; TypeMoveMouse moves without clicking; TypeScroll is refused
// because the contract gives it its own Scroll method with deltas.
func (c *Client) PerformAction(
	actionType action.Type,
	point image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	switch actionType {
	case action.TypeMoveMouse:
		c.MoveMouse(point, false)

		return nil
	case action.TypeMoveMouseRelative:
		current := c.CursorPosition()
		c.MoveMouse(image.Pt(current.X+point.X, current.Y+point.Y), false)

		return nil
	case action.TypeScroll:
		return errs.New(
			errs.CodeInvalidInput,
			"scroll needs Scroll(deltaX, deltaY, modifiers) rather than PerformAction",
		)
	}

	button, phase, ok := actionType.MouseButtonPhase()
	if !ok {
		return errs.Newf(errs.CodeInvalidInput, "unsupported action type %s", actionType)
	}

	return c.performMouseButton(button, phase, point, restoreCursor, modifiers)
}

// performMouseButton applies one phase of a button action.
func (c *Client) performMouseButton(
	button action.MouseButton,
	phase action.MousePhase,
	point image.Point,
	restoreCursor bool,
	modifiers action.Modifiers,
) error {
	flags := eventFlags(modifiers)

	switch phase {
	case action.PhaseClick:
		return c.clickAt(button, point, restoreCursor, flags)
	case action.PhaseDown:
		if err := c.postButtonEvent(button, false, point, flags, 1); err != nil {
			return err
		}

		c.setHeld(button, true)

		return nil
	case action.PhaseUp:
		if err := c.postButtonEvent(button, true, point, flags, 1); err != nil {
			return err
		}

		c.setHeld(button, false)

		return nil
	case action.PhaseToggle:
		if c.isHeld(button) {
			if err := c.postButtonEvent(button, true, point, flags, 1); err != nil {
				return err
			}

			c.setHeld(button, false)

			return nil
		}

		if err := c.postButtonEvent(button, false, point, flags, 1); err != nil {
			return err
		}

		c.setHeld(button, true)

		return nil
	default:
		return errs.Newf(errs.CodeInvalidInput, "unsupported mouse phase %s", phase)
	}
}

// clickAt moves, presses and releases the button at point, then optionally
// puts the cursor back where it was. The click count is tracked so two clicks
// in the same place inside the double-click interval become a double click.
func (c *Client) clickAt(
	button action.MouseButton,
	point image.Point,
	restoreCursor bool,
	flags uint64,
) error {
	var original image.Point

	if restoreCursor {
		original = c.CursorPosition()
	}

	count := c.nextClickCount(point)

	down, err := c.newButtonEvent(button, false, point, flags, count)
	if err != nil {
		return err
	}

	up, err := c.newButtonEvent(button, true, point, flags, count)
	if err != nil {
		c.f.cf.release(down)

		return err
	}

	location := cgPoint{X: float64(point.X), Y: float64(point.Y)}

	c.moveEvent(point, flags)

	c.postPositioned(down, location)

	time.Sleep(mouseClickDownUpDelay)

	c.postPositioned(up, location)

	c.f.cf.release(down)
	c.f.cf.release(up)

	time.Sleep(mouseClickSettleDelay)

	if restoreCursor {
		c.MoveMouse(original, true)
	}

	return nil
}

// postButtonEvent posts a single mouse down or up event at point. It moves the
// cursor first so applications that track the pointer agree on where the
// button event happened.
func (c *Client) postButtonEvent(
	button action.MouseButton,
	up bool,
	point image.Point,
	flags uint64,
	clickCount int,
) error {
	c.moveEvent(point, flags)

	event, err := c.newButtonEvent(button, up, point, flags, clickCount)
	if err != nil {
		return err
	}

	c.postPositioned(event, cgPoint{X: float64(point.X), Y: float64(point.Y)})
	c.f.cf.release(event)

	time.Sleep(mouseClickSettleDelay)

	return nil
}

// newButtonEvent creates a mouse down or up event for the button, with the
// modifiers and click count stamped on it.
func (c *Client) newButtonEvent(
	button action.MouseButton,
	up bool,
	point image.Point,
	flags uint64,
	clickCount int,
) (uintptr, error) {
	kind, cgButton, ok := buttonEventKind(button, up)
	if !ok {
		return 0, errs.Newf(errs.CodeInvalidInput, "unknown mouse button %v", button)
	}

	event := c.f.cg.eventCreateMouseEvent(0, kind, cgPoint{
		X: float64(point.X),
		Y: float64(point.Y),
	}, cgButton)
	if event == 0 {
		return 0, errs.New(errs.CodeInternal, "could not create a mouse event")
	}

	c.f.cg.eventSetFlags(event, flags)
	c.f.cg.eventSetIntegerField(event, cgMouseEventClickState, int64(clickCount))

	return event, nil
}

// postPositioned posts one positioned event, after panning the Accessibility
// Zoom viewport to the point so the event lands where it is visible.
//
// The lock keeps the pan and the post indivisible: cursor operations arrive on
// unrelated goroutines, and two interleaved ones could otherwise pan for one
// target and post the other. The autorelease pool matters because a Go thread
// has none of its own: posting autoreleases a CFData per event, and without a
// pool the process keeps every one until it exits.
func (c *Client) postPositioned(event uintptr, point cgPoint) {
	c.postMu.Lock()
	defer c.postMu.Unlock()

	c.f.withPool(func() {
		ensureZoomViewportContainsPoint(point)
		c.f.cg.eventPost(cgEventTapSession, event)
	})
}

// moveEvent posts the cursor at point: kCGEventMouseMoved normally, and the
// matching *MouseDragged type while this client holds a button, which is what
// makes the target application see a drag rather than a hovering pointer.
func (c *Client) moveEvent(point image.Point, flags uint64) {
	kind, button := c.dragEvent()

	location := cgPoint{X: float64(point.X), Y: float64(point.Y)}

	event := c.f.cg.eventCreateMouseEvent(0, kind, location, button)
	if event == 0 {
		return
	}

	c.f.cg.eventSetFlags(event, flags)
	c.postPositioned(event, location)
	c.f.cf.release(event)
}

// dragEvent returns the event type a cursor move must be posted with, and the
// button it belongs to. Moving while a button is held has to be posted as a drag
// of that button or the application never sees the drag. When several buttons
// are held the left-most wins, because a single move event cannot describe more
// than one.
func (c *Client) dragEvent() (uint32, uint32) {
	for _, button := range action.MouseButtons() {
		if !c.isHeld(button) {
			continue
		}

		if kind, cgButton, ok := draggedEventKind(button); ok {
			return kind, cgButton
		}
	}

	return cgEventMouseMoved, cgMouseButtonLeft
}

// draggedEventKind maps a button to the dragged event type and CGMouseButton
// that describe a drag of it.
func draggedEventKind(button action.MouseButton) (uint32, uint32, bool) {
	switch button {
	case action.ButtonLeft:
		return cgEventLeftMouseDragged, cgMouseButtonLeft, true
	case action.ButtonRight:
		return cgEventRightMouseDragged, cgMouseButtonRight, true
	case action.ButtonMiddle:
		return cgEventOtherMouseDragged, cgMouseButtonCenter, true
	default:
		return 0, 0, false
	}
}

// MoveMouse places the cursor at point and posts the matching move event, so
// accessibility hit tests and hover states see it, and pans the Accessibility
// Zoom viewport when the point would otherwise be off screen.
//
// With Options.SmoothSteps positive and bypassSmooth false the move is posted as
// that many events spaced by Options.SmoothDelay, which makes the pointer glide
// instead of jumping. A true bypassSmooth always moves instantly.
func (c *Client) MoveMouse(point image.Point, bypassSmooth bool) {
	// One move at a time: two smooth moves interleaving their steps would fight
	// over the cursor and land nowhere in particular.
	c.motionMu.Lock()
	defer c.motionMu.Unlock()

	if c.smoothSteps > 0 && !bypassSmooth {
		c.moveSmooth(point)

		return
	}

	c.moveInstant(point)
}

// moveInstant warps the cursor and posts one move event at the same point.
func (c *Client) moveInstant(point image.Point) {
	location := cgPoint{X: float64(point.X), Y: float64(point.Y)}

	c.f.cg.warpCursor(location)
	c.moveEvent(image.Pt(int(math.Round(location.X)), int(math.Round(location.Y))), 0)
}

// moveSmooth posts the steps of a smooth move, ending exactly on the target.
func (c *Client) moveSmooth(end image.Point) {
	start := c.CursorPosition()

	for index, point := range smoothPoints(start, end, c.smoothSteps) {
		if index > 0 && c.smoothDelay > 0 {
			time.Sleep(c.smoothDelay)
		}

		c.moveInstant(point)
	}
}

// smoothPoints returns the cursor positions a smooth move posts, the last of
// them exactly end. The count equals steps when that is positive, and one point
// otherwise. An intermediate point is truncated toward zero rather than
// rounded, which is what the daemon this was ported from does, so both draw the
// same path.
func smoothPoints(start, end image.Point, steps int) []image.Point {
	if steps <= 0 {
		return []image.Point{end}
	}

	points := make([]image.Point, 0, steps)

	for step := 1; step <= steps; step++ {
		progress := float64(step) / float64(steps)
		points = append(points, image.Point{
			X: int(float64(start.X) + float64(end.X-start.X)*progress),
			Y: int(float64(start.Y) + float64(end.Y-start.Y)*progress),
		})
	}

	// progress is exactly one on the last step, but the endpoint is the one
	// coordinate a caller can observe afterwards.
	points[len(points)-1] = end

	return points
}

// MouseButtonDown reports whether the system currently considers the given mouse
// button pressed. It asks the window server's combined session state, so a
// button the user or another process holds counts too, not only one this client
// pressed.
func (c *Client) MouseButtonDown(button action.MouseButton) bool {
	_, cgButton, ok := buttonEventKind(button, false)
	if !ok {
		return false
	}

	return c.f.cg.eventSourceButtonState(cgEventSourceStateCombined, cgButton) != 0
}

// EnsureMouseUp releases every mouse button that is down, so an action that
// cannot run while a button is held starts from a clean state. A button the
// window server reports down but this client never pressed is released too:
// leaving it held would make the next drag start from an unknown position.
//
// Every button is attempted and the failures are joined, because one failed
// release must not leave the rest held.
func (c *Client) EnsureMouseUp() error {
	var failures []error

	for _, button := range action.MouseButtons() {
		if !c.MouseButtonDown(button) && !c.isHeld(button) {
			continue
		}

		if err := c.releaseAtCursor(button); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}

// releaseAtCursor posts a button-up where the cursor is, which is where a held
// drag ended, and clears this client's own record of the button.
func (c *Client) releaseAtCursor(button action.MouseButton) error {
	kind, cgButton, ok := buttonEventKind(button, true)
	if !ok {
		return errs.Newf(errs.CodeInvalidInput, "unknown mouse button %v", button)
	}

	position := c.CursorPosition()
	point := cgPoint{X: float64(position.X), Y: float64(position.Y)}

	event := c.f.cg.eventCreateMouseEvent(0, kind, point, cgButton)
	if event == 0 {
		return errs.New(errs.CodeInternal, "could not create a mouse event")
	}

	// Clear the modifiers: a release carrying whatever the session currently
	// believes is held would read as a different gesture.
	c.f.cg.eventSetFlags(event, 0)
	c.postPositioned(event, point)
	c.f.cf.release(event)

	c.setHeld(button, false)

	time.Sleep(mouseClickSettleDelay)

	return nil
}

// CursorPosition returns the pointer's global position.
func (c *Client) CursorPosition() image.Point {
	event := c.f.cg.eventCreate(0)
	if event == 0 {
		return image.Point{}
	}

	defer c.f.cf.release(event)

	location := c.f.cg.eventGetLocation(event)

	return image.Pt(int(math.Round(location.X)), int(math.Round(location.Y)))
}

// Scroll posts a pixel-unit wheel event at the cursor. Positive deltaY scrolls
// up and positive deltaX scrolls left, which is the sign convention the ax
// contract documents.
func (c *Client) Scroll(deltaX, deltaY int, modifiers action.Modifiers) error {
	if deltaX == 0 && deltaY == 0 {
		return nil
	}

	event := c.f.cg.eventCreateScrollWheelTwo(
		0,
		cgScrollUnitPixel,
		2,
		clampInt32(deltaY),
		clampInt32(deltaX),
		0,
	)
	if event == 0 {
		return errs.New(errs.CodeInternal, "could not create a scroll event")
	}

	defer c.f.cf.release(event)

	position := c.CursorPosition()
	point := cgPoint{X: float64(position.X), Y: float64(position.Y)}

	c.f.cg.eventSetLocation(event, point)

	// Stamp the flags even when none are set. A nil-source event is born with
	// whatever modifiers the session currently believes are held, so leaving
	// the field alone turns a plain scroll into a zoom while a modifier is
	// down.
	c.f.cg.eventSetFlags(event, eventFlags(modifiers))
	c.postPositioned(event, point)

	return nil
}

// eventFlags translates action modifiers into CGEvent flags.
func eventFlags(modifiers action.Modifiers) uint64 {
	var flags uint64

	if modifiers.Has(action.ModShift) {
		flags |= cgEventFlagShift
	}

	if modifiers.Has(action.ModCtrl) {
		flags |= cgEventFlagControl
	}

	if modifiers.Has(action.ModAlt) {
		flags |= cgEventFlagAlternate
	}

	if modifiers.Has(action.ModCmd) {
		flags |= cgEventFlagCommand
	}

	return flags
}

// buttonEventKind maps a button and phase to the event type and CGMouseButton
// the event carries.
func buttonEventKind(button action.MouseButton, up bool) (uint32, uint32, bool) {
	switch button {
	case action.ButtonLeft:
		if up {
			return cgEventLeftMouseUp, cgMouseButtonLeft, true
		}

		return cgEventLeftMouseDown, cgMouseButtonLeft, true
	case action.ButtonRight:
		if up {
			return cgEventRightMouseUp, cgMouseButtonRight, true
		}

		return cgEventRightMouseDown, cgMouseButtonRight, true
	case action.ButtonMiddle:
		if up {
			return cgEventOtherMouseUp, cgMouseButtonCenter, true
		}

		return cgEventOtherMouseDown, cgMouseButtonCenter, true
	default:
		return 0, 0, false
	}
}

// setHeld records whether a button is currently pressed, for the toggle
// phases and for posting a drag event while a button is down.
func (c *Client) setHeld(button action.MouseButton, held bool) {
	c.heldMu.Lock()
	defer c.heldMu.Unlock()

	c.held[button] = held
}

func (c *Client) isHeld(button action.MouseButton) bool {
	c.heldMu.Lock()
	defer c.heldMu.Unlock()

	return c.held[button]
}

// nextClickCount returns the click number the next click should carry: one for
// a fresh click, two when the previous one was close in time and place, and so
// on for triple clicks.
func (c *Client) nextClickCount(point image.Point) int {
	c.clickMu.Lock()
	defer c.clickMu.Unlock()

	now := time.Now()

	nearInTime := now.Sub(c.lastClickAt) < doubleClickInterval
	nearInPlace := absInt(point.X-c.lastClickPoint.X) <= doubleClickMaxDistance &&
		absInt(point.Y-c.lastClickPoint.Y) <= doubleClickMaxDistance

	if nearInTime && nearInPlace {
		c.clickCount++
	} else {
		c.clickCount = 1
	}

	c.lastClickAt = now
	c.lastClickPoint = point

	return c.clickCount
}

func clampInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}

	if value < math.MinInt32 {
		return math.MinInt32
	}

	return int32(value)
}

func absInt(value int) int {
	return max(value, -value)
}
