//go:build windows

package windows

import (
	"context"
	"image"
	"maps"
	"slices"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
)

// Unit tests for the parts of the backend that need no desktop: control-type
// naming, role filtering, depth accounting and bounds clipping. Anything that
// drives UI Automation belongs in a desktop test, not here.

func TestControlTypeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		controlType int32
		wantName    string
		wantKnown   bool
	}{
		{"button", 50000, controlButton, true},
		{"check box", 50002, controlCheckBox, true},
		{"combo box", 50003, "ComboBox", true},
		{"edit", 50004, controlEdit, true},
		{"hyperlink", 50005, "Hyperlink", true},
		{"list item", 50007, "ListItem", true},
		{"menu item", 50011, "MenuItem", true},
		{"radio button", 50013, "RadioButton", true},
		{"tab item", 50019, "TabItem", true},
		{"text", 50020, "Text", true},
		{"custom", 50025, controlCustom, true},
		{"document", 50030, "Document", true},
		{"split button", 50031, controlSplit, true},
		{"window", 50032, controlTypeWindow, true},
		{"pane", 50033, controlPane, true},
		{"last known control type", 50040, "AppBar", true},
		{"unknown control type", 99999, "", false},
		{"zero control type", 0, "", false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			name, known := controlTypeName(testCase.controlType)
			if name != testCase.wantName || known != testCase.wantKnown {
				t.Fatalf(
					"controlTypeName(%d) = (%q, %v), want (%q, %v)",
					testCase.controlType, name, known, testCase.wantName, testCase.wantKnown,
				)
			}
		})
	}
}

// TestControlTypeNamesAreContiguous guards the transcription of the
// UIA_*ControlTypeId range. A gap or a duplicate would mean an id was mistyped,
// which silently misnames a control type.
func TestControlTypeNamesAreContiguous(t *testing.T) {
	t.Parallel()

	const first, last = 50000, 50040

	seen := make(map[string]int32, len(controlTypeNames))

	for controlType := int32(first); controlType <= last; controlType++ {
		name, ok := controlTypeNames[controlType]
		if !ok {
			t.Errorf("controlTypeNames lacks id %d", controlType)

			continue
		}

		if previous, duplicate := seen[name]; duplicate {
			t.Errorf("controlTypeNames has %q for both %d and %d", name, previous, controlType)
		}

		seen[name] = controlType
	}

	if len(controlTypeNames) != last-first+1 {
		t.Errorf("controlTypeNames has %d entries, want %d", len(controlTypeNames), last-first+1)
	}
}

// TestControlTypeNamesCoverTheVocabulary pins enumeration against the semantic
// vocabulary. Every UIA name a neutral role expands to must be a name the
// enumerator can produce, or that role could never match anything on Windows.
//
// Two names are exempt because UI Automation has no control type for them:
// "Application" is this backend's name for a process, which is not an element
// at all, and "Unknown" never arrives as a control type id, because the range
// is fixed and every id in it is named.
func TestControlTypeNamesCoverTheVocabulary(t *testing.T) {
	t.Parallel()

	names := slices.Collect(maps.Values(controlTypeNames))

	exempt := map[string]struct{}{
		controlTypeApplication: {},
		"Unknown":              {},
	}

	for _, mapping := range element.RoleVocabulary {
		for _, native := range mapping.UIA {
			if _, ok := exempt[native]; ok {
				continue
			}

			if !slices.Contains(names, native) {
				t.Errorf(
					"neutral role %q expands to UIA control type %q, which the enumerator never produces",
					mapping.Role, native,
				)
			}
		}
	}
}

// TestDefaultClickableRoles_ResolvesToRealControlTypes checks the fallback used
// when a caller passes no role filter. It must resolve to real UIA names, or a
// query with an empty filter would silently return nothing.
func TestDefaultClickableRoles_ResolvesToRealControlTypes(t *testing.T) {
	t.Parallel()

	if len(defaultClickableRoles) == 0 {
		t.Fatal("defaultClickableRoles is empty; a query with no filter would find nothing")
	}

	names := slices.Collect(maps.Values(controlTypeNames))

	for _, role := range defaultClickableRoles {
		if !slices.Contains(names, role) {
			t.Errorf("defaultClickableRoles contains %q, which is not a UIA control type", role)
		}
	}

	for _, want := range []string{controlButton, controlEdit, controlSplit, "Hyperlink", "TabItem", "ListItem"} {
		if !slices.Contains(defaultClickableRoles, want) {
			t.Errorf("defaultClickableRoles is missing %q", want)
		}
	}
}

func TestRoleFilter_Matches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		roles []string
		role  string
		want  bool
	}{
		{name: "exact", roles: []string{"Button"}, role: "Button", want: true},
		{name: "case insensitive", roles: []string{"button"}, role: "Button", want: true},
		{name: "upper case filter", roles: []string{"TABITEM"}, role: "TabItem", want: true},
		{name: "padded filter", roles: []string{"  Button  "}, role: "Button", want: true},
		{name: "not listed", roles: []string{"Button"}, role: "Edit", want: false},
		{name: "empty filter keeps everything", role: "Pane", want: true},
		{name: "blank entries are ignored", roles: []string{"", " "}, role: "Pane", want: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := newRoleFilter(testCase.roles).matches(testCase.role); got != testCase.want {
				t.Errorf("matches(%q) with %v = %v, want %v",
					testCase.role, testCase.roles, got, testCase.want)
			}
		})
	}
}

// TestWalkBounded_VisitsExactlyTheDepthBudget pins the depth accounting: the
// window's children are depth one, as on macOS, and nothing below maxDepth is
// visited.
func TestWalkBounded_VisitsExactlyTheDepthBudget(t *testing.T) {
	t.Parallel()

	tree := map[int][]int{
		0: {1, 2},
		1: {3},
		2: {4, 5},
		3: {6},
	}

	tests := []struct {
		name     string
		maxDepth int
		want     map[int]int
	}{
		{name: "one level", maxDepth: 1, want: map[int]int{1: 1, 2: 1}},
		{name: "two levels", maxDepth: 2, want: map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 2}},
		{name: "three levels", maxDepth: 3, want: map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 2, 6: 3}},
		{name: "depth zero visits nothing", maxDepth: 0, want: map[int]int{}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			visited := make(map[int]int)

			walkBounded[int](
				0,
				testCase.maxDepth,
				firstChildOf(tree),
				nextSiblingOf(tree),
				func(int) {},
				func() bool { return false },
				func(node, depth int) { visited[node] = depth },
			)

			if len(visited) != len(testCase.want) {
				t.Fatalf("visited %v, want %v", visited, testCase.want)
			}

			for node, depth := range testCase.want {
				if visited[node] != depth {
					t.Errorf("node %d visited at depth %d, want %d", node, visited[node], depth)
				}
			}
		})
	}
}

// TestWalkBounded_ReleasesEveryVisitedNodeOnce checks the ownership rule: the
// walker releases each node it obtained, exactly once, so a COM reference
// cannot leak or be freed twice.
func TestWalkBounded_ReleasesEveryVisitedNodeOnce(t *testing.T) {
	t.Parallel()

	tree := map[int][]int{
		0: {1},
		1: {2, 3},
		2: {4},
	}

	released := make(map[int]int)

	walkBounded[int](
		0,
		10,
		firstChildOf(tree),
		nextSiblingOf(tree),
		func(node int) { released[node]++ },
		func() bool { return false },
		func(int, int) {},
	)

	for _, node := range []int{1, 2, 3, 4} {
		if released[node] != 1 {
			t.Errorf("node %d released %d times, want 1", node, released[node])
		}
	}

	if released[0] != 0 {
		t.Errorf("the root was released %d times, want 0: the caller owns it", released[0])
	}
}

// TestWalkBounded_StopsWhenTold checks that a canceled query (or a spent node
// budget) ends the walk promptly instead of finishing the tree.
func TestWalkBounded_StopsWhenTold(t *testing.T) {
	t.Parallel()

	tree := map[int][]int{0: {1, 2, 3, 4, 5}}

	visited := 0

	walkBounded[int](
		0,
		10,
		firstChildOf(tree),
		nextSiblingOf(tree),
		func(int) {},
		func() bool { return visited >= 2 },
		func(int, int) { visited++ },
	)

	if visited != 2 {
		t.Errorf("visited %d nodes after the stop signal, want 2", visited)
	}
}

// TestClipToVisible checks the clip that keeps a control laid out past the
// window edge, or past the screen, from placing a hint where a click cannot
// land.
func TestClipToVisible(t *testing.T) {
	t.Parallel()

	visible := image.Rect(0, 0, 1400, 660)

	tests := []struct {
		name    string
		bounds  image.Rectangle
		visible image.Rectangle
		want    image.Rectangle
		wantOK  bool
	}{
		{
			name:    "inside",
			bounds:  image.Rect(10, 10, 50, 40),
			visible: visible,
			want:    image.Rect(10, 10, 50, 40),
			wantOK:  true,
		},
		{
			name:    "straddling the bottom edge",
			bounds:  image.Rect(10, 640, 50, 700),
			visible: visible,
			want:    image.Rect(10, 640, 50, 660),
			wantOK:  true,
		},
		{
			name:    "below the window",
			bounds:  image.Rect(10, 700, 50, 740),
			visible: visible,
			wantOK:  false,
		},
		{
			name:    "zero sized",
			bounds:  image.Rect(10, 10, 10, 40),
			visible: visible,
			wantOK:  false,
		},
		{
			name:    "negative origin kept",
			bounds:  image.Rect(-100, -50, -20, -10),
			visible: image.Rect(-200, -200, 0, 0),
			want:    image.Rect(-100, -50, -20, -10),
			wantOK:  true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, ok := clipToVisible(testCase.bounds, testCase.visible)
			if ok != testCase.wantOK {
				t.Fatalf("clipToVisible(%v, %v) ok = %v, want %v",
					testCase.bounds, testCase.visible, ok, testCase.wantOK)
			}

			if got != testCase.want {
				t.Errorf("clipToVisible(%v, %v) = %v, want %v",
					testCase.bounds, testCase.visible, got, testCase.want)
			}
		})
	}
}

// TestUTF16PtrToString reads a NUL-terminated UTF-16 string the way a BSTR
// getter hands one back, including the empty string.
func TestUTF16PtrToString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		words []uint16
		want  string
	}{
		{name: "empty", words: []uint16{0}, want: ""},
		{name: "ascii", words: []uint16{'S', 'a', 'v', 'e', 0}, want: "Save"},
		{name: "unicode", words: []uint16{'保', '存', 0}, want: "保存"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := utf16PtrToString(&testCase.words[0]); got != testCase.want {
				t.Errorf("utf16PtrToString(%v) = %q, want %q", testCase.words, got, testCase.want)
			}
		})
	}

	if got := utf16PtrToString(nil); got != "" {
		t.Errorf("utf16PtrToString(nil) = %q, want an empty string", got)
	}
}

// TestHrFailed checks the sign convention every COM result is read through.
func TestHrFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		hresult uintptr
		want    bool
	}{
		{hresult: 0, want: false},
		{hresult: 1, want: false},
		{hresult: 0x80004003, want: true},
		{hresult: 0x80010106, want: true},
	}

	for _, testCase := range tests {
		if got := hrFailed(testCase.hresult); got != testCase.want {
			t.Errorf("hrFailed(%#x) = %v, want %v", testCase.hresult, got, testCase.want)
		}
	}
}

// TestCtxErr checks that an already-canceled context is reported with the
// library's cancellation code rather than as a query that found nothing.
func TestCtxErr(t *testing.T) {
	t.Parallel()

	if err := ctxErr(context.Background()); err != nil {
		t.Fatalf("ctxErr on a live context = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ctxErr(ctx); err == nil {
		t.Fatal("ctxErr on a canceled context = nil, want an error")
	}
}

// firstChildOf and nextSiblingOf turn an adjacency map into the callbacks
// walkBounded expects, so the depth accounting can be tested on ints.
func firstChildOf(tree map[int][]int) func(int) (int, bool) {
	return func(node int) (int, bool) {
		children := tree[node]
		if len(children) == 0 {
			return 0, false
		}

		return children[0], true
	}
}

func nextSiblingOf(tree map[int][]int) func(int) (int, bool) {
	return func(node int) (int, bool) {
		for _, siblings := range tree {
			for index, sibling := range siblings {
				if sibling == node && index+1 < len(siblings) {
					return siblings[index+1], true
				}
			}
		}

		return 0, false
	}
}
