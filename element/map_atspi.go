package element

// ATSPIRoleMapper translates Linux AT-SPI role names into neutral roles. The
// names are the GEnum nicks libatspi returns from Accessible.GetRoleName:
// lowercase and space separated ("push button", "page tab list").
type ATSPIRoleMapper struct{}

// MapRole returns the neutral role for an AT-SPI role name.
func (ATSPIRoleMapper) MapRole(native string) (Role, bool) {
	return atspiRoles.MapRole(native)
}

// atspiRoles is the AT-SPI role vocabulary. Where a toolkit reports several
// names for one widget family, every spelling is listed.
var atspiRoles = roleTable{
	"push button":         RoleButton,
	"button":              RoleButton,
	"toggle button":       RoleToggle,
	"push button menu":    RoleMenuButton,
	"menu button":         RoleMenuButton,
	"combo box":           RoleComboBox,
	"link":                RoleLink,
	"check box":           RoleCheckBox,
	"check menu item":     RoleCheckBox,
	"radio button":        RoleRadioButton,
	"radio menu item":     RoleRadioButton,
	"switch":              RoleSwitch,
	"disclosure triangle": RoleDisclosureTriangle,
	"entry":               RoleTextField,
	"password text":       RoleTextField,
	"slider":              RoleSlider,
	"spin button":         RoleIncrementor,
	"page tab":            RoleTabButton,
	"menu item":           RoleMenuItem,
	"menu":                RoleMenu,
	"popup menu":          RoleMenu,
	"menu bar":            RoleMenuBar,
	"table cell":          RoleCell,
	"table row":           RoleRow,
	"list item":           RoleListItem,
	"image":               RoleImage,
	"icon":                RoleImage,
	"static":              RoleStaticText,
	"label":               RoleStaticText,
	"text":                RoleStaticText,
	"heading":             RoleHeading,
	"color chooser":       RoleColorWell,
	"application":         RoleApplication,
	"frame":               RoleWindow,
	"window":              RoleWindow,
	"dialog":              RoleWindow,
	"page tab list":       RoleTabGroup,
	"panel":               RoleGroup,
	"grouping":            RoleGroup,
	"filler":              RoleGroup,
	"scroll pane":         RoleScrollArea,
	"split pane":          RoleSplitGroup,
	"list":                RoleList,
	"list box":            RoleList,
	"table":               RoleTable,
	"tree table":          RoleTable,
	"tree":                RoleOutline,
	"unknown":             RoleGenericElement,
	"redundant object":    RoleGenericElement,
	"invalid":             RoleUnknown,
}
