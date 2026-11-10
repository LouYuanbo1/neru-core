package element

import (
	"runtime"
	"strings"
)

// NativeVocabulary identifies one platform's accessibility role vocabulary. A
// configuration entry addresses a native role name through this prefix, for
// example "atspi:page tab list".
type NativeVocabulary string

// The native vocabularies this package can translate. Each maps to exactly one
// platform.
const (
	// VocabularyAX is the macOS Accessibility (AX*) role vocabulary.
	VocabularyAX NativeVocabulary = "ax"
	// VocabularyATSPI is the Linux AT-SPI role-name vocabulary, as returned by
	// Accessible.GetRoleName (lowercase, space separated).
	VocabularyATSPI NativeVocabulary = "atspi"
	// VocabularyUIA is the Windows UI Automation control-type vocabulary, using
	// the programmatic names behind the UIA_*ControlTypeId constants.
	VocabularyUIA NativeVocabulary = "uia"
)

// The Go platform identifiers of the platforms with an accessibility backend.
const (
	GOOSDarwin  = "darwin"
	GOOSLinux   = "linux"
	GOOSWindows = "windows"
)

// vocabularySeparator separates a vocabulary prefix from a native role name in
// a configuration entry. No accessibility role name on any supported platform
// contains a colon, so splitting on the first one is unambiguous.
const vocabularySeparator = ":"

// vocabularyByGOOS maps a platform to the vocabulary its backend speaks.
var vocabularyByGOOS = map[string]NativeVocabulary{
	GOOSDarwin:  VocabularyAX,
	GOOSLinux:   VocabularyATSPI,
	GOOSWindows: VocabularyUIA,
}

// VocabularyForGOOS returns the native role vocabulary used by the given
// platform. The second result is false for platforms with no accessibility
// backend, where every configured role resolves to nothing.
func VocabularyForGOOS(goos string) (NativeVocabulary, bool) {
	vocab, ok := vocabularyByGOOS[goos]

	return vocab, ok
}

// CurrentVocabulary returns the native role vocabulary for the running
// platform, and false when this platform has no accessibility backend.
func CurrentVocabulary() (NativeVocabulary, bool) {
	return VocabularyForGOOS(runtime.GOOS)
}

// RoleMapping records how one neutral role expands into native role names on
// each supported platform. An empty slice means the platform has no
// equivalent, which role resolution reports rather than silently ignores.
type RoleMapping struct {
	// Role is the platform-neutral role this mapping describes.
	Role Role
	// AX lists the macOS role names this role expands to.
	AX []string
	// AXIsSubrole marks that AppKit declares this mapping's AX names as
	// subroles (NSAccessibilitySubrole constants), not roles. An element
	// reports such a name in its AXSubrole attribute while its AXRole stays
	// more generic, so the macOS matcher compares configured names against the
	// subrole as well as the role.
	AXIsSubrole bool
	// ATSPI lists the Linux AT-SPI role names this role expands to.
	ATSPI []string
	// UIA lists the Windows UI Automation control-type programmatic names this
	// role expands to.
	UIA []string
}

// Native returns the native role names for the given vocabulary.
func (m RoleMapping) Native(vocab NativeVocabulary) []string {
	switch vocab {
	case VocabularyAX:
		return m.AX
	case VocabularyATSPI:
		return m.ATSPI
	case VocabularyUIA:
		return m.UIA
	default:
		return nil
	}
}

// RoleVocabulary is the full neutral-role table, in documentation order. It is
// the single source of truth for role resolution and for the role mappers a
// backend uses to translate its native names.
//
// Overlapping expansions are intentional. Linux and Windows do not distinguish
// a text area or a search field from a plain text field, so those roles expand
// onto the same native name there. Resolution unions the results.
var RoleVocabulary = []RoleMapping{
	{
		Role:  RoleButton,
		AX:    []string{"AXButton"},
		ATSPI: []string{"push button", "button", "toggle button"},
		UIA:   []string{"Button", "SplitButton"},
	},
	{
		Role:  RoleMenuButton,
		AX:    []string{"AXMenuButton"},
		ATSPI: []string{"push button menu"},
	},
	{
		Role:  RolePopUpButton,
		AX:    []string{"AXPopUpButton"},
		ATSPI: []string{"combo box"},
		UIA:   []string{"ComboBox"},
	},
	{
		Role:  RoleComboBox,
		AX:    []string{"AXComboBox"},
		ATSPI: []string{"combo box"},
		UIA:   []string{"ComboBox"},
	},
	{
		Role:  RoleLink,
		AX:    []string{"AXLink"},
		ATSPI: []string{"link"},
		UIA:   []string{"Hyperlink"},
	},
	{
		Role:  RoleCheckBox,
		AX:    []string{"AXCheckBox"},
		ATSPI: []string{"check box", "check menu item"},
		UIA:   []string{"CheckBox"},
	},
	{
		Role:  RoleRadioButton,
		AX:    []string{"AXRadioButton"},
		ATSPI: []string{"radio button", "radio menu item"},
		UIA:   []string{"RadioButton"},
	},
	{
		Role: RoleSwitch,
		// SwiftUI toggles report AXCheckBox / AXSwitch, so AXSwitch is a
		// subrole on macOS.
		AX:          []string{"AXSwitch"},
		AXIsSubrole: true,
		ATSPI:       []string{"switch"},
	},
	{
		Role:  RoleToggle,
		ATSPI: []string{"toggle button"},
	},
	{
		Role:  RoleDisclosureTriangle,
		AX:    []string{"AXDisclosureTriangle"},
		ATSPI: []string{"disclosure triangle"},
	},
	{
		Role:  RoleTextField,
		AX:    []string{"AXTextField"},
		ATSPI: []string{"entry", "password text"},
		UIA:   []string{"Edit"},
	},
	{
		Role:  RoleTextArea,
		AX:    []string{"AXTextArea"},
		ATSPI: []string{"entry"},
		UIA:   []string{"Edit"},
	},
	{
		Role: RoleSearchField,
		// AppKit search fields report AXTextField / AXSearchField, the second
		// as a subrole.
		AX:          []string{"AXSearchField"},
		AXIsSubrole: true,
		ATSPI:       []string{"entry"},
		UIA:         []string{"Edit"},
	},
	{
		Role:  RoleSlider,
		AX:    []string{"AXSlider"},
		ATSPI: []string{"slider"},
		UIA:   []string{"Slider"},
	},
	{
		Role:  RoleIncrementor,
		AX:    []string{"AXIncrementor"},
		ATSPI: []string{"spin button"},
		UIA:   []string{"Spinner"},
	},
	{
		Role: RoleTabButton,
		// AppKit tab buttons report AXRadioButton / AXTabButton.
		AX:          []string{"AXTabButton"},
		AXIsSubrole: true,
		ATSPI:       []string{"page tab"},
		UIA:         []string{"TabItem"},
	},
	{
		Role:  RoleMenuItem,
		AX:    []string{"AXMenuItem"},
		ATSPI: []string{"menu item"},
		UIA:   []string{"MenuItem"},
	},
	{
		Role:  RoleMenuBarItem,
		AX:    []string{"AXMenuBarItem"},
		ATSPI: []string{"menu"},
	},
	{
		Role: RoleDockItem,
		AX:   []string{"AXDockItem"},
	},
	{
		Role:  RoleCell,
		AX:    []string{"AXCell"},
		ATSPI: []string{"table cell"},
		UIA:   []string{"DataItem"},
	},
	{
		Role:  RoleRow,
		AX:    []string{"AXRow"},
		ATSPI: []string{"table row"},
		UIA:   []string{"TreeItem"},
	},
	{
		Role:  RoleListItem,
		AX:    []string{"AXRow"},
		ATSPI: []string{"list item"},
		UIA:   []string{"ListItem"},
	},
	{
		Role:  RoleImage,
		AX:    []string{"AXImage"},
		ATSPI: []string{"image", "icon"},
		UIA:   []string{"Image"},
	},
	{
		Role:  RoleStaticText,
		AX:    []string{"AXStaticText"},
		ATSPI: []string{"static", "label", "text"},
		UIA:   []string{"Text"},
	},
	{
		Role:  RoleHeading,
		AX:    []string{"AXHeading"},
		ATSPI: []string{"heading"},
	},
	{
		Role:  RoleColorWell,
		AX:    []string{"AXColorWell"},
		ATSPI: []string{"color chooser"},
	},
	{
		Role: RoleToolbarButton,
		// Toolbar buttons report AXButton / AXToolbarButton.
		AX:          []string{"AXToolbarButton"},
		AXIsSubrole: true,
	},
	// Containers and surfaces. These are not clickable defaults, but a tree
	// reports them and a consumer needs a neutral name for them.
	{
		Role:  RoleApplication,
		AX:    []string{"AXApplication"},
		ATSPI: []string{"application"},
		UIA:   []string{"Application"},
	},
	{
		Role:  RoleWindow,
		AX:    []string{"AXWindow"},
		ATSPI: []string{"frame", "window", "dialog"},
		UIA:   []string{"Window"},
	},
	{
		Role: RoleSheet,
		AX:   []string{"AXSheet"},
	},
	{
		Role: RolePopover,
		AX:   []string{"AXPopover"},
	},
	{
		Role:  RoleMenu,
		AX:    []string{"AXMenu"},
		ATSPI: []string{"menu", "popup menu"},
		UIA:   []string{"Menu"},
	},
	{
		Role:  RoleMenuBar,
		AX:    []string{"AXMenuBar"},
		ATSPI: []string{"menu bar"},
		UIA:   []string{"MenuBar"},
	},
	{
		Role:  RoleList,
		AX:    []string{"AXList"},
		ATSPI: []string{"list", "list box"},
		UIA:   []string{"List"},
	},
	{
		Role:  RoleTable,
		AX:    []string{"AXTable"},
		ATSPI: []string{"table", "tree table"},
		UIA:   []string{"Table", "DataGrid"},
	},
	{
		Role:  RoleOutline,
		AX:    []string{"AXOutline"},
		ATSPI: []string{"tree"},
		UIA:   []string{"Tree"},
	},
	{
		Role:  RoleTabGroup,
		AX:    []string{"AXTabGroup"},
		ATSPI: []string{"page tab list"},
		UIA:   []string{"Tab"},
	},
	{
		Role:  RoleGroup,
		AX:    []string{"AXGroup"},
		ATSPI: []string{"panel", "grouping", "filler"},
		UIA:   []string{"Group", "Custom"},
	},
	{
		Role:  RoleScrollArea,
		AX:    []string{"AXScrollArea"},
		ATSPI: []string{"scroll pane"},
	},
	{
		Role:  RoleSplitGroup,
		AX:    []string{"AXSplitGroup"},
		ATSPI: []string{"split pane"},
	},
	{
		Role:  RoleGenericElement,
		AX:    []string{"AXGenericElement"},
		ATSPI: []string{"unknown", "redundant object"},
		UIA:   []string{"Custom"},
	},
	{
		Role:  RoleUnknown,
		AX:    []string{"AXUnknown"},
		ATSPI: []string{"invalid"},
		UIA:   []string{"Unknown"},
	},
}

// DefaultClickableRoles is the neutral role list this library documents as the
// default hint targets: the roles a click can land on across all three
// platforms. A caller that passes no role filter to a backend should resolve
// this list instead, so the default is the same whether or not a
// configuration is present.
var DefaultClickableRoles = []Role{
	RoleButton,
	RoleMenuButton,
	RolePopUpButton,
	RoleComboBox,
	RoleCheckBox,
	RoleRadioButton,
	RoleSwitch,
	RoleDisclosureTriangle,
	RoleLink,
	RoleTextField,
	RoleTextArea,
	RoleSlider,
	RoleTabButton,
	RoleMenuItem,
	RoleCell,
	RoleRow,
	RoleListItem,
}

// DefaultClickableNativeRoles resolves DefaultClickableRoles for one
// vocabulary, dropping roles that platform has no name for.
//
// macOS additionally gets AXGenericElement. Web content and several AppKit
// containers surface only as generic elements there, and without it a hints
// session would find nothing on a page rendered by Safari or Electron. The
// other two platforms have real roles for the same widgets, so their generic
// name ("unknown", "Custom") would only add noise.
func DefaultClickableNativeRoles(vocab NativeVocabulary) []string {
	resolution := ResolveRoles(DefaultClickableRoles, vocab)
	native := resolution.Native

	if vocab == VocabularyAX {
		for _, name := range native {
			if name == "AXGenericElement" {
				return native
			}
		}

		native = append(native, "AXGenericElement")
	}

	return native
}

// semanticIndex indexes RoleVocabulary by neutral role.
var semanticIndex = func() map[Role]RoleMapping {
	index := make(map[Role]RoleMapping, len(RoleVocabulary))

	for _, mapping := range RoleVocabulary {
		index[mapping.Role] = mapping
	}

	return index
}()

// LookupRole returns the mapping for a neutral role.
func LookupRole(role Role) (RoleMapping, bool) {
	mapping, ok := semanticIndex[role]

	return mapping, ok
}

// NativeRoleFor returns the first native name a neutral role expands to in the
// given vocabulary, and whether the vocabulary has an equivalent at all. It is
// the reverse of a mapper's MapRole and is what makes `element.Role` printable
// as the platform's own name.
func NativeRoleFor(role Role, vocab NativeVocabulary) (string, bool) {
	mapping, ok := LookupRole(role)
	if !ok {
		return "", false
	}

	names := mapping.Native(vocab)
	if len(names) == 0 {
		return "", false
	}

	return names[0], true
}

// SplitVocabularyEntry splits "vocab:native name" into its halves. The third
// result reports whether the entry carried a separator at all. An unknown
// prefix still reports true, so a caller can diagnose it rather than treat the
// whole string as a neutral role name.
func SplitVocabularyEntry(entry string) (NativeVocabulary, string, bool) {
	prefix, native, found := strings.Cut(entry, vocabularySeparator)
	if !found {
		return "", "", false
	}

	return NativeVocabulary(strings.TrimSpace(prefix)), strings.TrimSpace(native), true
}

// KnownVocabulary reports whether vocab is one of the native vocabularies.
func KnownVocabulary(vocab NativeVocabulary) bool {
	switch vocab {
	case VocabularyAX, VocabularyATSPI, VocabularyUIA:
		return true
	default:
		return false
	}
}
