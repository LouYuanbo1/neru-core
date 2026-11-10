package element_test

import (
	"slices"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
)

func TestRoleVocabulary_EveryNativeNameIsMapped(t *testing.T) {
	t.Parallel()

	vocabularies := []element.NativeVocabulary{
		element.VocabularyAX,
		element.VocabularyATSPI,
		element.VocabularyUIA,
	}

	for _, vocab := range vocabularies {
		mapper, ok := element.MapperFor(vocab)
		if !ok {
			t.Fatalf("MapperFor(%q) is not registered", vocab)
		}

		for _, mapping := range element.RoleVocabulary {
			for _, native := range mapping.Native(vocab) {
				if _, found := mapper.MapRole(native); !found {
					t.Errorf("%s: native role %q expands from %q but does not map back",
						vocab, native, mapping.Role)
				}
			}
		}
	}
}

func TestAXRoleMapper_SubroleWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		role    string
		subrole string
		want    element.Role
	}{
		{name: "switch is a check box subrole", role: "AXCheckBox", subrole: "AXSwitch", want: element.RoleSwitch},
		{name: "search field is a text field subrole", role: "AXTextField", subrole: "AXSearchField", want: element.RoleSearchField},
		{name: "toolbar button is a button subrole", role: "AXButton", subrole: "AXToolbarButton", want: element.RoleToolbarButton},
		{name: "a plain role still maps", role: "AXButton", want: element.RoleButton},
		{name: "an unknown pair is unknown", role: "AXWeird", subrole: "AXWeirder", want: element.RoleUnknown},
	}

	mapper := element.AXRoleMapper{}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, _ := mapper.Map(testCase.role, testCase.subrole)
			if got != testCase.want {
				t.Errorf("Map(%q, %q) = %q, want %q", testCase.role, testCase.subrole, got, testCase.want)
			}
		})
	}
}

func TestDefaultClickableNativeRoles_MatchesPlatformVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		vocab     element.NativeVocabulary
		want      string
		notWanted string
	}{
		{vocab: element.VocabularyAX, want: "AXButton", notWanted: "entry"},
		{vocab: element.VocabularyATSPI, want: "push button", notWanted: "AXButton"},
		{vocab: element.VocabularyUIA, want: "Button", notWanted: "AXButton"},
	}

	for _, testCase := range tests {
		t.Run(string(testCase.vocab), func(t *testing.T) {
			t.Parallel()

			roles := element.DefaultClickableNativeRoles(testCase.vocab)

			if !slices.Contains(roles, testCase.want) {
				t.Errorf("default roles for %q = %v, want to contain %q", testCase.vocab, roles, testCase.want)
			}

			if slices.Contains(roles, testCase.notWanted) {
				t.Errorf("default roles for %q contain foreign role %q", testCase.vocab, testCase.notWanted)
			}
		})
	}
}

func TestDefaultClickableNativeRoles_AddsGenericOnlyOnMacOS(t *testing.T) {
	t.Parallel()

	axRoles := element.DefaultClickableNativeRoles(element.VocabularyAX)
	if !slices.Contains(axRoles, "AXGenericElement") {
		t.Error("macOS defaults must include AXGenericElement for web content")
	}

	atspiRoles := element.DefaultClickableNativeRoles(element.VocabularyATSPI)
	if slices.Contains(atspiRoles, "unknown") {
		t.Error("Linux defaults must not include the AT-SPI unknown role")
	}

	uiaRoles := element.DefaultClickableNativeRoles(element.VocabularyUIA)
	if slices.Contains(uiaRoles, "Custom") {
		t.Error("Windows defaults must not include the UIA Custom control type")
	}
}

func TestResolveRoleEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entries    []string
		goos       string
		wantNative []string
		wantFatal  bool
	}{
		{
			name:       "neutral roles expand to the platform's names",
			entries:    []string{"button", "link"},
			goos:       element.GOOSDarwin,
			wantNative: []string{"AXButton", "AXLink"},
		},
		{
			name:       "a prefixed native name passes through on its platform",
			entries:    []string{"ax:AXMenuItem"},
			goos:       element.GOOSDarwin,
			wantNative: []string{"AXMenuItem"},
		},
		{
			name:       "another platform's native name is ignored, not fatal",
			entries:    []string{"atspi:entry", "button"},
			goos:       element.GOOSDarwin,
			wantNative: []string{"AXButton"},
		},
		{
			name:      "a misspelled neutral role is fatal",
			entries:   []string{"buton"},
			goos:      element.GOOSDarwin,
			wantFatal: true,
		},
		{
			name:      "an unknown vocabulary is fatal",
			entries:   []string{"windows:Button"},
			goos:      element.GOOSDarwin,
			wantFatal: true,
		},
		{
			name:    "a role with no native equivalent is reported but not fatal",
			entries: []string{"dock_item"},
			goos:    element.GOOSLinux,
		},
		{
			name:       "duplicates collapse in entry order",
			entries:    []string{"button", "ax:AXButton"},
			goos:       element.GOOSDarwin,
			wantNative: []string{"AXButton"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			resolution := element.ResolveRoleEntries(testCase.entries, testCase.goos)

			if resolution.HasFatal() != testCase.wantFatal {
				t.Fatalf("HasFatal() = %v, want %v (diagnostics: %v)",
					resolution.HasFatal(), testCase.wantFatal, resolution.Diagnostics)
			}

			if testCase.wantNative != nil && !slices.Equal(resolution.Native, testCase.wantNative) {
				t.Errorf("Native = %v, want %v", resolution.Native, testCase.wantNative)
			}
		})
	}
}

func TestResolveRoleEntries_SuggestsTheNeutralNameForANativeSpelling(t *testing.T) {
	t.Parallel()

	resolution := element.ResolveRoleEntries([]string{"push button"}, element.GOOSWindows)
	if !resolution.HasFatal() {
		t.Fatal("a bare native name is not a neutral role and must be fatal")
	}

	diagnostic := resolution.Diagnostics[0]
	if diagnostic.Suggestion != string(element.RoleButton) {
		t.Errorf("Suggestion = %q, want %q", diagnostic.Suggestion, element.RoleButton)
	}
}

func TestVocabularyForGOOS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos      string
		want      element.NativeVocabulary
		wantFound bool
	}{
		{goos: element.GOOSDarwin, want: element.VocabularyAX, wantFound: true},
		{goos: element.GOOSLinux, want: element.VocabularyATSPI, wantFound: true},
		{goos: element.GOOSWindows, want: element.VocabularyUIA, wantFound: true},
		{goos: "plan9", wantFound: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.goos, func(t *testing.T) {
			t.Parallel()

			got, found := element.VocabularyForGOOS(testCase.goos)
			if found != testCase.wantFound || got != testCase.want {
				t.Errorf("VocabularyForGOOS(%q) = %q, %v, want %q, %v",
					testCase.goos, got, found, testCase.want, testCase.wantFound)
			}
		})
	}
}

func TestParseDirection(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"up", "down", "left", "right"} {
		direction, err := element.ParseDirection(value)
		if err != nil {
			t.Fatalf("ParseDirection(%q): %v", value, err)
		}

		if direction.String() != value {
			t.Errorf("ParseDirection(%q).String() = %q", value, direction.String())
		}
	}

	if _, err := element.ParseDirection("sideways"); err == nil {
		t.Error("ParseDirection(sideways) should error")
	}
}
