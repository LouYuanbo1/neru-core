// Package element provides the platform-neutral domain model for UI elements.
//
// An Element is found either in an accessibility tree (via an ax adapter) or
// on screen (via a vision adapter). Elements are immutable after creation, so
// goroutines can share them.
//
// The model deliberately carries no platform vocabulary: Role uses neutral
// logical names, not macOS "AX*" strings. Platform backends translate their
// native role strings into these names through RoleMapper, so the roles stay
// consistent across macOS, Linux, and Windows.
package element

import (
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// ID uniquely identifies an element.
type ID string

// Role is the platform-neutral logical role of an element.
type Role string

// Common logical roles.
const (
	RoleButton             Role = "button"
	RoleLink               Role = "link"
	RoleTextField          Role = "text_field"
	RoleStaticText         Role = "static_text"
	RoleImage              Role = "image"
	RoleCheckBox           Role = "check_box"
	RoleRadioButton        Role = "radio_button"
	RoleMenuItem           Role = "menu_item"
	RoleMenuButton         Role = "menu_button"
	RolePopUpButton        Role = "pop_up_button"
	RoleTabButton          Role = "tab_button"
	RoleSlider             Role = "slider"
	RoleSwitch             Role = "switch"
	RoleDisclosureTriangle Role = "disclosure_triangle"
	RoleTextArea           Role = "text_area"
	RoleComboBox           Role = "combo_box"
	RolePopover            Role = "popover"
	RoleSheet              Role = "sheet"
	RoleMenu               Role = "menu"
	RoleList               Role = "list"
	RoleHeading            Role = "heading"
	RoleMenuBarItem        Role = "menu_bar_item"
	RoleMenuBar            Role = "menu_bar"
	RoleCell               Role = "cell"
	RoleRow                Role = "row"
	RoleListItem           Role = "list_item"
	RoleDockItem           Role = "dock_item"
	RoleIncrementor        Role = "incrementor"
	RoleColorWell          Role = "color_well"
	RoleSearchField        Role = "search_field"
	RoleToolbarButton      Role = "toolbar_button"
	RoleToggle             Role = "toggle"
	RoleTable              Role = "table"
	RoleOutline            Role = "outline"
	RoleApplication        Role = "application"
	RoleWindow             Role = "window"
	RoleTabGroup           Role = "tab_group"
	RoleGroup              Role = "group"
	RoleScrollArea         Role = "scroll_area"
	RoleSplitGroup         Role = "split_group"
	RoleGenericElement     Role = "generic_element"
	RoleUnknown            Role = "unknown"
)

// RoleMapper translates a backend's native role strings into neutral Roles.
// Each platform backend provides its own implementation (macOS AX strings,
// Windows UIA patterns, Linux AT-SPI roles), so the domain model never has to
// know a platform spelling.
type RoleMapper interface {
	// MapRole returns the neutral Role for a backend-native role string, and
	// whether the backend recognizes it.
	MapRole(native string) (Role, bool)
}

// Source names what found an element.
type Source string

// The sources an element can come from.
const (
	SourceAccessibilityTree Source = "accessibility_tree"
	SourceVision            Source = "vision"
	SourceManual            Source = "manual"
)

// Element is a UI element in the accessibility tree (or detected via vision).
// It is immutable after creation, so goroutines can share it.
type Element struct {
	id          ID
	bounds      image.Rectangle
	role        Role
	isClickable bool
	title       string
	description string
	value       string
	searchText  string
	source      Source
	focused     bool
	identifier  string
}

// NewElement creates a new element with validation.
func NewElement(elementID ID, bounds image.Rectangle, role Role, opts ...Option) (*Element, error) {
	if elementID == "" {
		return nil, errs.New(errs.CodeInvalidInput, "element ID cannot be empty")
	}

	if bounds.Empty() {
		return nil, errs.New(errs.CodeInvalidInput, "element bounds cannot be empty")
	}

	element := &Element{
		id:     elementID,
		bounds: bounds,
		role:   role,
		source: SourceAccessibilityTree,
	}

	for _, opt := range opts {
		opt(element)
	}

	return element, nil
}

// Option configures an Element.
type Option func(*Element)

// WithClickable sets whether the element is clickable.
func WithClickable(clickable bool) Option {
	return func(e *Element) {
		e.isClickable = clickable
	}
}

// WithTitle sets the element title.
func WithTitle(title string) Option {
	return func(e *Element) {
		e.title = title
	}
}

// WithDescription sets the element description.
func WithDescription(desc string) Option {
	return func(e *Element) {
		e.description = desc
	}
}

// WithValue sets the element value.
func WithValue(val string) Option {
	return func(e *Element) {
		e.value = val
	}
}

// WithSearchText sets additional searchable text associated with the element,
// such as the text its accessibility subtree carries.
func WithSearchText(text string) Option {
	return func(e *Element) {
		e.searchText = text
	}
}

// WithSource sets what found the element.
func WithSource(source Source) Option {
	return func(e *Element) {
		e.source = source
	}
}

// WithFocused sets whether the element has keyboard focus.
func WithFocused(focused bool) Option {
	return func(e *Element) {
		e.focused = focused
	}
}

// WithIdentifier sets the identifier the app's developer gave the element.
func WithIdentifier(identifier string) Option {
	return func(e *Element) {
		e.identifier = identifier
	}
}

// ID returns the element ID.
func (e *Element) ID() ID {
	return e.id
}

// Bounds returns the element bounds.
func (e *Element) Bounds() image.Rectangle {
	return e.bounds
}

// Role returns the element role.
func (e *Element) Role() Role {
	return e.role
}

// IsClickable returns whether the element is clickable.
func (e *Element) IsClickable() bool {
	return e.isClickable
}

// Title returns the element title.
func (e *Element) Title() string {
	return e.title
}

// Description returns the element description.
func (e *Element) Description() string {
	return e.description
}

// Value returns the element value.
func (e *Element) Value() string {
	return e.value
}

// Text returns the element's representative text: its value, else its title,
// else its description. A field's value is what it holds and static text keeps
// its words there, so the value comes first, and a button or link is named by
// its title.
func (e *Element) Text() string {
	for _, text := range []string{e.value, e.title, e.description} {
		if text != "" {
			return text
		}
	}

	return ""
}

// IsVisionOnly returns true if the element was found by vision rather than the
// accessibility tree. Vision-only elements have no AX reference, so actions
// must use coordinate-based clicks.
func (e *Element) IsVisionOnly() bool {
	return e.source == SourceVision
}

// SearchText returns additional searchable text associated with the element,
// such as the text its accessibility subtree carries.
func (e *Element) SearchText() string {
	return e.searchText
}

// Source returns what found the element.
func (e *Element) Source() Source {
	return e.source
}

// IsFocused returns whether the element has keyboard focus.
func (e *Element) IsFocused() bool {
	return e.focused
}

// Identifier returns the identifier the app's developer gave the element.
func (e *Element) Identifier() string {
	return e.identifier
}

// Center returns the center point of the element.
func (e *Element) Center() image.Point {
	return image.Point{
		X: e.bounds.Min.X + e.bounds.Dx()/2,
		Y: e.bounds.Min.Y + e.bounds.Dy()/2,
	}
}

// Contains checks if a point is within the element bounds.
func (e *Element) Contains(pt image.Point) bool {
	return pt.In(e.bounds)
}

// Overlaps checks if this element overlaps with another.
func (e *Element) Overlaps(other *Element) bool {
	return e.bounds.Overlaps(other.bounds)
}

// IsVisible checks if the element is visible within the given screen bounds.
func (e *Element) IsVisible(screenBounds image.Rectangle) bool {
	return e.bounds.Overlaps(screenBounds) && !e.bounds.Empty()
}
