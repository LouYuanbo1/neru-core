//go:build darwin

package darwin

import (
	"image"
	"math"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The attributes one read of an element fetches. They are requested in one
// AXUIElementCopyMultipleAttributeValues round trip, which the accessibility
// server answers far faster than the same number of single reads.
var infoAttributes = [...]string{
	"AXRole",
	"AXSubrole",
	"AXTitle",
	"AXDescription",
	"AXValue",
	"AXIdentifier",
	"AXRoleDescription",
	"AXPosition",
	"AXSize",
	"AXEnabled",
	"AXFocused",
	"AXHidden",
	"AXVisible",
}

// elementInfo is what one element says about itself.
//
// visible defaults to true because an application that does not implement
// AXVisible is visible; hidden defaults to false. Both mirror the values the
// AX API documents for an absent attribute.
type elementInfo struct {
	role            string
	subrole         string
	title           string
	description     string
	value           string
	identifier      string
	roleDescription string

	position cgPoint
	size     cgSize

	enabled    bool
	hasEnabled bool
	focused    bool
	hidden     bool
	visible    bool

	pid int32
}

// bounds converts the element's AXPosition and AXSize into a global
// top-left-origin rectangle. A fractional point is rounded to the nearest
// pixel, so adjacent elements do not develop one-pixel gaps.
func (i *elementInfo) bounds() image.Rectangle {
	if i == nil {
		return image.Rectangle{}
	}

	minX := int(math.Round(i.position.X))
	minY := int(math.Round(i.position.Y))
	width := int(math.Round(i.size.Width))
	height := int(math.Round(i.size.Height))

	return image.Rect(minX, minY, minX+width, minY+height)
}

// matchesRole reports whether the element's native role or subrole is in the
// set. AppKit reports a search field as AXTextField + AXSearchField and a
// switch as AXCheckBox + AXSwitch, so both attributes are compared.
func (i *elementInfo) matchesRole(roles map[string]struct{}) bool {
	if i == nil || len(roles) == 0 {
		return false
	}

	if _, ok := roles[i.role]; ok {
		return true
	}

	_, ok := roles[i.subrole]

	return ok
}

// searchTextCandidate reports whether the element contributes text of its own
// to a search of what its subtree says.
func (i *elementInfo) searchTextCandidate() bool {
	if i == nil {
		return false
	}

	return strings.TrimSpace(i.title) != "" ||
		strings.TrimSpace(i.description) != "" ||
		strings.TrimSpace(i.value) != ""
}

// subroleSecureTextField is the AXSubrole of a password field.
const subroleSecureTextField = "AXSecureTextField"

// secureValue blanks the value of a secure text field. Its value is the
// password the user typed: nothing this library does needs it, and it would
// otherwise reach a node's search text and through it any hint search or log a
// caller keeps. Blanking it leaves the element itself clickable, which is what
// a hint needs.
func secureValue(subrole, value string) string {
	if subrole == subroleSecureTextField {
		return ""
	}

	return value
}

// readInfo fetches the element's attributes in one batch, falling back to one
// call per attribute when the element rejects the batch. Some Stage Manager
// strip buttons answer AXIdentifier with kAXErrorIllegalArgument, which fails
// the whole batch; reading one at a time keeps the rest of the element usable.
//
// The returned info is never nil: even when every read fails it carries the
// pid, which callers use to identify the owning application.
func (c *Client) readInfo(ref uintptr) (*elementInfo, error) {
	info := &elementInfo{visible: true}

	if ref == 0 {
		return info, errs.New(errs.CodeInvalidInput, "accessibility element is nil")
	}

	defer c.f.ax.getPid(ref, &info.pid)

	attributes := make([]uintptr, 0, len(infoAttributes))
	for _, name := range infoAttributes {
		attributes = append(attributes, c.f.cfStringRef(name))
	}

	array := c.f.cf.arrayCreate(0, attributes, int64(len(attributes)), c.f.cf.typeArrayCallbacks)
	if array == 0 {
		return info, errs.New(errs.CodeAccessibilityFailed, "could not build the attribute list")
	}

	defer c.f.cf.release(array)

	var values uintptr

	status := c.f.ax.copyMultipleValues(ref, array, 0, &values)
	if status != kAXErrorSuccess {
		if values != 0 {
			c.f.cf.release(values)
		}

		if status == kAXErrorIllegalArgument {
			c.applyAttributesOneByOne(ref, info)

			return info, nil
		}

		return info, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the element would not report its attributes (AXError %d)",
			status,
		)
	}

	defer c.f.cf.release(values)

	reported := c.f.arrayValues(values)
	if len(reported) < len(infoAttributes) {
		return info, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the element reported %d of %d attributes",
			len(reported),
			len(infoAttributes),
		)
	}

	c.applyAttributes(info, reported)

	return info, nil
}

// applyAttributesOneByOne refills info with one AX call per attribute. A read
// that fails leaves that attribute at its default, which is the same state as
// a batch that reported kCFNull for it.
func (c *Client) applyAttributesOneByOne(ref uintptr, info *elementInfo) {
	values := make([]uintptr, len(infoAttributes))

	for index, name := range infoAttributes {
		value, status := c.f.copyAttribute(ref, name)
		if status != kAXErrorSuccess || value == 0 {
			continue
		}

		values[index] = value

		defer c.f.cf.release(value)
	}

	c.applyAttributes(info, values)
}

// applyAttributes turns the batch's values into info fields. A value of an
// unexpected type is skipped: AX answers an unsupported attribute with an
// error placeholder, and reading it as text or a boolean would invent data.
func (c *Client) applyAttributes(info *elementInfo, values []uintptr) {
	text := func(index int) string {
		if index >= len(values) {
			return ""
		}

		if !c.f.isCFString(values[index]) {
			return ""
		}

		return c.f.goString(values[index])
	}

	info.role = text(0)
	info.subrole = text(1)
	info.title = text(2)
	info.description = text(3)
	info.value = text(4)
	info.identifier = text(5)
	info.roleDescription = text(6)

	if len(values) > 7 {
		if point, ok := c.f.pointValue(values[7]); ok {
			info.position = point
		}
	}

	if len(values) > 8 {
		if size, ok := c.f.sizeValue(values[8]); ok {
			info.size = size
		}
	}

	if len(values) > 9 {
		if enabled, ok := c.f.cfBool(values[9]); ok {
			info.enabled = enabled
			info.hasEnabled = true
		}
	}

	if len(values) > 10 {
		if focused, ok := c.f.cfBool(values[10]); ok {
			info.focused = focused
		}
	}

	if len(values) > 11 {
		if hidden, ok := c.f.cfBool(values[11]); ok {
			info.hidden = hidden
		}
	}

	if len(values) > 12 {
		if visible, ok := c.f.cfBool(values[12]); ok {
			info.visible = visible
		}
	}

	// The subrole is known by now, so a password field's value is dropped only
	// after it has been read: a secure field still answers AXValue.
	info.value = secureValue(info.subrole, info.value)
}
