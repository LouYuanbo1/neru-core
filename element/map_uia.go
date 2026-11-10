package element

// UIARoleMapper translates Windows UI Automation control-type names into
// neutral roles. The names are the programmatic names behind the
// UIA_*ControlTypeId constants, which is what a UIA client reads from
// CurrentControlType's programmatic name, e.g. "Button" or "Edit".
type UIARoleMapper struct{}

// MapRole returns the neutral role for a UIA control-type name.
func (UIARoleMapper) MapRole(native string) (Role, bool) {
	return uiaRoles.MapRole(native)
}

// uiaRoles is the UIA control-type vocabulary.
var uiaRoles = roleTable{
	"Button":      RoleButton,
	"SplitButton": RoleButton,
	"Hyperlink":   RoleLink,
	"Edit":        RoleTextField,
	"Document":    RoleTextField,
	"CheckBox":    RoleCheckBox,
	"RadioButton": RoleRadioButton,
	"ComboBox":    RoleComboBox,
	"Slider":      RoleSlider,
	"Spinner":     RoleIncrementor,
	"TabItem":     RoleTabButton,
	"MenuItem":    RoleMenuItem,
	"DataItem":    RoleCell,
	"TreeItem":    RoleRow,
	"ListItem":    RoleListItem,
	"Image":       RoleImage,
	"Text":        RoleStaticText,
	"Heading":     RoleHeading,
	"Application": RoleApplication,
	"Window":      RoleWindow,
	"Pane":        RoleGroup,
	"Group":       RoleGroup,
	"Custom":      RoleGroup,
	"Menu":        RoleMenu,
	"MenuBar":     RoleMenuBar,
	"List":        RoleList,
	"Table":       RoleTable,
	"DataGrid":    RoleTable,
	"Tree":        RoleOutline,
	"Tab":         RoleTabGroup,
	"Unknown":     RoleUnknown,
}
