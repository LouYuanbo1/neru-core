package element

// AXRoleMapper translates macOS Accessibility role and subrole names into
// neutral roles.
//
// AppKit splits a widget's identity over two attributes: AXRole names the
// family ("AXButton", "AXRow") and AXSubrole refines it ("AXSwitch" on an
// AXCheckBox, "AXSearchField" on an AXTextField). MapRole reads the role alone
// and Map reads both, preferring a recognized subrole. Use Map when the tree
// walker has both; use MapRole when only a role string is at hand.
type AXRoleMapper struct{}

// MapRole returns the neutral role for a macOS AX role name.
func (AXRoleMapper) MapRole(native string) (Role, bool) {
	if role, ok := axRoles.MapRole(native); ok {
		return role, true
	}

	return axSubroles.MapRole(native)
}

// Map returns the neutral role for a macOS role/subrole pair. A recognized
// subrole wins, because it is the more specific name; otherwise the role
// decides. An unrecognized role reports RoleUnknown with false, so a caller
// can tell "not clickable" from "unknown".
func (AXRoleMapper) Map(role, subrole string) (Role, bool) {
	if mapped, ok := axSubroles.MapRole(subrole); ok {
		return mapped, true
	}

	if mapped, ok := axRoles.MapRole(role); ok {
		return mapped, true
	}

	if role == "" {
		return RoleUnknown, false
	}

	return RoleUnknown, false
}

// axRoles is the AXRole vocabulary.
var axRoles = roleTable{
	"AXButton":             RoleButton,
	"AXMenuButton":         RoleMenuButton,
	"AXPopUpButton":        RolePopUpButton,
	"AXComboBox":           RoleComboBox,
	"AXLink":               RoleLink,
	"AXCheckBox":           RoleCheckBox,
	"AXRadioButton":        RoleRadioButton,
	"AXDisclosureTriangle": RoleDisclosureTriangle,
	"AXTextField":          RoleTextField,
	"AXTextArea":           RoleTextArea,
	"AXSlider":             RoleSlider,
	"AXIncrementor":        RoleIncrementor,
	"AXMenuItem":           RoleMenuItem,
	"AXMenuBarItem":        RoleMenuBarItem,
	"AXDockItem":           RoleDockItem,
	"AXCell":               RoleCell,
	"AXRow":                RoleRow,
	"AXImage":              RoleImage,
	"AXStaticText":         RoleStaticText,
	"AXHeading":            RoleHeading,
	"AXColorWell":          RoleColorWell,
	"AXApplication":        RoleApplication,
	"AXWindow":             RoleWindow,
	"AXSheet":              RoleSheet,
	"AXPopover":            RolePopover,
	"AXMenu":               RoleMenu,
	"AXMenuBar":            RoleMenuBar,
	"AXList":               RoleList,
	"AXTable":              RoleTable,
	"AXOutline":            RoleOutline,
	"AXTabGroup":           RoleTabGroup,
	"AXGroup":              RoleGroup,
	"AXScrollArea":         RoleScrollArea,
	"AXSplitGroup":         RoleSplitGroup,
	"AXGenericElement":     RoleGenericElement,
	"AXToggle":             RoleToggle,
	"AXUnknown":            RoleUnknown,
}

// axSubroles is the AXSubrole vocabulary, which AppKit reports separately from
// AXRole.
var axSubroles = roleTable{
	"AXSwitch":           RoleSwitch,
	"AXSearchField":      RoleSearchField,
	"AXTabButton":        RoleTabButton,
	"AXToolbarButton":    RoleToolbarButton,
	"AXCloseButton":      RoleButton,
	"AXMinimizeButton":   RoleButton,
	"AXZoomButton":       RoleButton,
	"AXFullScreenButton": RoleButton,
}
