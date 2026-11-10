//go:build linux

package linux

import (
	"image"
	"testing"
)

// These tests cover the decisions policy.go makes, which is the whole of this
// backend that can be exercised without an accessibility bus and without an X
// display: role normalization and matching, depth accounting, extent placement
// and clipping, scroll step computation, focus ranking, and application identity
// matching.
//
// The file carries the linux build tag because it is compiled as part of a
// linux-only package; it needs no desktop, only a host that can build the
// package.

func TestNormalizeRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "already native", input: "push button", expected: "push button"},
		{name: "upper case", input: "PUSH BUTTON", expected: "push button"},
		{name: "mixed case", input: "Page Tab List", expected: "page tab list"},
		{name: "collapses interior whitespace", input: "push  \t button", expected: "push button"},
		{name: "trims surrounding whitespace", input: "  entry  ", expected: "entry"},
		{name: "empty stays empty", input: "", expected: ""},
		{name: "blank stays empty", input: "   ", expected: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := normalizeRole(testCase.input); got != testCase.expected {
				t.Errorf("normalizeRole(%q) = %q, want %q", testCase.input, got, testCase.expected)
			}
		})
	}
}

func TestNormalizeRoles_DropsBlankAndDuplicateEntries(t *testing.T) {
	t.Parallel()

	got := normalizeRoles([]string{"Push Button", "", "  ", "push  button", "entry", "ENTRY"})

	expected := []string{"push button", "entry"}

	if len(got) != len(expected) {
		t.Fatalf("normalizeRoles returned %v, want %v", got, expected)
	}

	for index := range expected {
		if got[index] != expected[index] {
			t.Errorf("normalizeRoles()[%d] = %q, want %q", index, got[index], expected[index])
		}
	}
}

func TestNormalizeRoles_PreservesCallerOrder(t *testing.T) {
	t.Parallel()

	got := normalizeRoles([]string{"entry", "push button"})

	if got[0] != "entry" || got[1] != "push button" {
		t.Errorf("normalizeRoles reordered the list: %v", got)
	}
}

func TestNormalizeRoles_ReturnsNilWithoutUsableEntries(t *testing.T) {
	t.Parallel()

	for _, roles := range [][]string{nil, {}, {"  "}, {""}} {
		if got := normalizeRoles(roles); got != nil {
			t.Errorf("normalizeRoles(%v) = %v, want nil", roles, got)
		}
	}
}

func TestRoleSet_FoldsCaseAndDropsBlanks(t *testing.T) {
	t.Parallel()

	set := roleSet([]string{"Push Button", "  ", "entry"})

	if _, ok := set["push button"]; !ok {
		t.Errorf("roleSet lost the normalized role: %v", set)
	}

	if _, ok := set["entry"]; !ok {
		t.Errorf("roleSet lost the entry role: %v", set)
	}

	if len(set) != 2 {
		t.Errorf("roleSet kept %d roles, want 2: %v", len(set), set)
	}
}

func TestEffectiveRole_EditableTextIsAnEntry(t *testing.T) {
	t.Parallel()

	editable := stateBits(stateEditable)

	if got := effectiveRole(roleText, editable); got != roleEntry {
		t.Errorf("editable text reported %q, want %q", got, roleEntry)
	}

	if got := effectiveRole(roleText, nil); got != roleText {
		t.Errorf("read-only text reported %q, want %q", got, roleText)
	}

	if got := effectiveRole("push button", editable); got != "push button" {
		t.Errorf("a button's role was rewritten to %q", got)
	}
}

func TestWantsRole(t *testing.T) {
	t.Parallel()

	buttons := roleSet([]string{"push button"})
	entries := roleSet([]string{"entry"})
	texts := roleSet([]string{"text"})

	cases := []struct {
		name     string
		role     string
		set      map[string]struct{}
		expected bool
	}{
		{name: "match", role: "push button", set: buttons, expected: true},
		{name: "no match", role: "slider", set: buttons, expected: false},
		{
			name: "entry also accepts text", role: roleText, set: entries,
			expected: true,
		},
		{
			name: "text accepts text", role: roleText, set: texts,
			expected: true,
		},
		{
			name: "a button is not an entry candidate", role: "push button", set: entries,
			expected: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := wantsRole(testCase.role, testCase.set); got != testCase.expected {
				t.Errorf("wantsRole(%q) = %v, want %v", testCase.role, got, testCase.expected)
			}
		})
	}
}

func TestIsValueRole(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		roleEntry:       true,
		roleSpinButton:  true,
		"push button":   false,
		roleText:        false,
		"password text": false,
	}

	for role, expected := range cases {
		if got := isValueRole(role); got != expected {
			t.Errorf("isValueRole(%q) = %v, want %v", role, got, expected)
		}
	}
}

func TestIsWindowRole(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		roleFrame:     true,
		roleWindow:    true,
		roleDialog:    true,
		"panel":       false,
		"application": false,
	}

	for role, expected := range cases {
		if got := isWindowRole(role); got != expected {
			t.Errorf("isWindowRole(%q) = %v, want %v", role, got, expected)
		}
	}
}

func TestAtspiStateBit(t *testing.T) {
	t.Parallel()

	// The first word carries every state below 32; the second carries the rest,
	// so bit 1 of word 1 is state index 33.
	states := []uint32{1 << stateShowing, 1 << 1}

	if !atspiStateBit(states, stateShowing) {
		t.Errorf("stateShowing was not seen in %v", states)
	}

	if atspiStateBit(states, stateActive) {
		t.Errorf("stateActive was seen in %v", states)
	}

	if !atspiStateBit(states, 33) {
		t.Errorf("a bit in the second word was not seen in %v", states)
	}

	if atspiStateBit(states, 64) {
		t.Errorf("a bit outside the bitfield was reported set")
	}

	if atspiStateBit(nil, stateShowing) {
		t.Errorf("an empty bitfield reported a state")
	}
}

func TestAtspiEnabled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		states   []uint32
		expected bool
	}{
		{name: "enabled", states: stateBits(stateEnabled), expected: true},
		{name: "sensitive only, as GTK 4 reports", states: stateBits(stateSensitive), expected: true},
		{name: "neither", states: stateBits(stateShowing), expected: false},
		{name: "no states at all", states: nil, expected: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := atspiEnabled(testCase.states); got != testCase.expected {
				t.Errorf("atspiEnabled(%v) = %v, want %v", testCase.states, got, testCase.expected)
			}
		})
	}
}

func TestWalkDepth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		maxDepth int
		expected int
	}{
		{name: "zero means the default", maxDepth: 0, expected: defaultMaxDepth},
		{name: "negative means the default", maxDepth: -3, expected: defaultMaxDepth},
		{name: "a caller's value is kept", maxDepth: 5, expected: 5},
		{name: "the ceiling clamps", maxDepth: maxWalkDepth + 1, expected: maxWalkDepth},
		{name: "exactly the ceiling is kept", maxDepth: maxWalkDepth, expected: maxWalkDepth},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := walkDepth(testCase.maxDepth); got != testCase.expected {
				t.Errorf("walkDepth(%d) = %d, want %d", testCase.maxDepth, got, testCase.expected)
			}
		})
	}
}

func TestDepthAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		depth    int
		limit    int
		expected bool
	}{
		{name: "the root is always read", depth: 0, limit: 0, expected: true},
		{name: "one past the limit is not", depth: 1, limit: 0, expected: false},
		{name: "exactly the limit is read", depth: 3, limit: 3, expected: true},
		{name: "beyond the limit is not", depth: 4, limit: 3, expected: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := depthAllowed(testCase.depth, testCase.limit)
			if got != testCase.expected {
				t.Errorf(
					"depthAllowed(%d, %d) = %v, want %v",
					testCase.depth, testCase.limit, got, testCase.expected,
				)
			}
		})
	}
}

func TestDepthLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		maxDepth   int
		configured int
		expected   int
	}{
		{
			name: "nothing configured", maxDepth: 0, configured: 0,
			expected: defaultMaxDepth,
		},
		{
			name: "the client's default applies", maxDepth: 0, configured: 12,
			expected: 12,
		},
		{
			name: "the call overrides the client", maxDepth: 4, configured: 12,
			expected: 4,
		},
		{
			name: "a negative call value falls back", maxDepth: -1, configured: 12,
			expected: 12,
		},
		{
			name:     "both are clamped by the ceiling",
			maxDepth: maxWalkDepth + 5, configured: maxWalkDepth + 5,
			expected: maxWalkDepth,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := depthLimit(testCase.maxDepth, testCase.configured)
			if got != testCase.expected {
				t.Errorf(
					"depthLimit(%d, %d) = %d, want %d",
					testCase.maxDepth, testCase.configured, got, testCase.expected,
				)
			}
		})
	}
}

func TestNodeLimit(t *testing.T) {
	t.Parallel()

	if got := nodeLimit(0); got != maxNodesPerScan {
		t.Errorf("nodeLimit(0) = %d, want the package default %d", got, maxNodesPerScan)
	}

	if got := nodeLimit(-5); got != maxNodesPerScan {
		t.Errorf("nodeLimit(-5) = %d, want the package default", got)
	}

	if got := nodeLimit(10); got != 10 {
		t.Errorf("nodeLimit(10) = %d, want 10", got)
	}
}

func TestPlacementFor(t *testing.T) {
	t.Parallel()

	screen := image.Rect(0, 0, 1920, 1080)
	atOrigin := image.Rect(0, 0, 800, 600)

	cases := []struct {
		name        string
		frameScreen image.Rectangle
		frameWindow image.Rectangle
		screen      image.Rectangle
		wantCoord   uint32
		wantOffset  image.Point
	}{
		{
			name:        "window coordinates need the frame's screen origin",
			frameScreen: image.Rect(800, 300, 1600, 900),
			frameWindow: image.Rect(0, 0, 800, 600),
			screen:      screen,
			wantCoord:   coordWindow,
			wantOffset:  image.Pt(800, 300),
		},
		{
			name:        "a client-side decoration margin is subtracted",
			frameScreen: image.Rect(800, 300, 1610, 910),
			frameWindow: image.Rect(10, 10, 820, 620),
			screen:      screen,
			wantCoord:   coordWindow,
			wantOffset:  image.Pt(790, 290),
		},
		{
			name:        "equal origins mean screen coordinates are already right",
			frameScreen: image.Rect(800, 300, 1600, 900),
			frameWindow: image.Rect(800, 300, 1600, 900),
			screen:      screen,
			wantCoord:   coordScreen,
		},
		{
			name:        "a window at the screen origin needs no translation",
			frameScreen: atOrigin,
			frameWindow: atOrigin,
			screen:      screen,
			wantCoord:   coordScreen,
		},
		{
			name:        "a frame off screen keeps plain screen coordinates",
			frameScreen: image.Rect(4000, 300, 4800, 900),
			frameWindow: image.Rect(0, 0, 800, 600),
			screen:      screen,
			wantCoord:   coordScreen,
		},
		{
			name:        "an unknown screen cannot invalidate the frame",
			frameScreen: image.Rect(4000, 300, 4800, 900),
			frameWindow: image.Rect(0, 0, 800, 600),
			screen:      image.Rectangle{},
			wantCoord:   coordWindow,
			wantOffset:  image.Pt(4000, 300),
		},
		{
			name:        "no screen extents",
			frameScreen: image.Rectangle{},
			frameWindow: image.Rect(0, 0, 800, 600),
			screen:      screen,
			wantCoord:   coordScreen,
		},
		{
			name:        "no window extents",
			frameScreen: image.Rect(800, 300, 1600, 900),
			frameWindow: image.Rectangle{},
			screen:      screen,
			wantCoord:   coordScreen,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := placementFor(testCase.frameScreen, testCase.frameWindow, testCase.screen)
			if got.coordType != testCase.wantCoord {
				t.Errorf(
					"placementFor(%v, %v).coordType = %d, want %d",
					testCase.frameScreen, testCase.frameWindow, got.coordType, testCase.wantCoord,
				)
			}

			if got.offset != testCase.wantOffset {
				t.Errorf(
					"placementFor(%v, %v).offset = %v, want %v",
					testCase.frameScreen, testCase.frameWindow, got.offset, testCase.wantOffset,
				)
			}
		})
	}
}

func TestPlacementPlace_AddsTheOffset(t *testing.T) {
	t.Parallel()

	place := placement{coordType: coordWindow, offset: image.Pt(800, 300)}

	got := place.place(image.Rect(10, 20, 110, 70))

	want := image.Rect(810, 320, 910, 370)

	if got != want {
		t.Errorf("place gave %v, want %v", got, want)
	}
}

func TestScreenPlacement_AddsNothing(t *testing.T) {
	t.Parallel()

	rect := image.Rect(10, 20, 110, 70)

	if got := screenPlacement().place(rect); got != rect {
		t.Errorf("screenPlacement moved %v to %v", rect, got)
	}
}

func TestClipToScreen(t *testing.T) {
	t.Parallel()

	screen := image.Rect(0, 0, 1920, 1080)

	cases := []struct {
		name      string
		rect      image.Rectangle
		screen    image.Rectangle
		wantRect  image.Rectangle
		wantShown bool
	}{
		{
			name:      "inside the screen is unchanged",
			rect:      image.Rect(100, 100, 300, 200),
			screen:    screen,
			wantRect:  image.Rect(100, 100, 300, 200),
			wantShown: true,
		},
		{
			name:      "hanging off the right edge is clipped",
			rect:      image.Rect(1800, 100, 2000, 200),
			screen:    screen,
			wantRect:  image.Rect(1800, 100, 1920, 200),
			wantShown: true,
		},
		{
			name:      "entirely off screen is dropped",
			rect:      image.Rect(2000, 100, 2100, 200),
			screen:    screen,
			wantShown: false,
		},
		{
			name:      "zero area is dropped",
			rect:      image.Rectangle{},
			screen:    screen,
			wantShown: false,
		},
		{
			name:      "an unknown screen clips nothing",
			rect:      image.Rect(2000, 100, 2100, 200),
			screen:    image.Rectangle{},
			wantRect:  image.Rect(2000, 100, 2100, 200),
			wantShown: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, shown := clipToScreen(testCase.rect, testCase.screen)
			if shown != testCase.wantShown {
				t.Fatalf("clipToScreen(%v) shown = %v, want %v", testCase.rect, shown, testCase.wantShown)
			}

			if shown && got != testCase.wantRect {
				t.Errorf("clipToScreen(%v) = %v, want %v", testCase.rect, got, testCase.wantRect)
			}
		})
	}
}

func TestScrollNotches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		delta    int
		expected int
	}{
		{name: "no delta", delta: 0, expected: 0},
		{name: "a tiny delta still scrolls once", delta: 1, expected: 1},
		{name: "half a notch rounds down to the floor of one", delta: 15, expected: 1},
		{name: "just over half a notch rounds up", delta: 16, expected: 1},
		{name: "one full notch", delta: scrollPixelsPerNotch, expected: 1},
		{name: "one and a half notches round up", delta: 45, expected: 2},
		{name: "negative deltas use the magnitude", delta: -100, expected: 3},
		{name: "a huge delta is capped", delta: 1_000_000, expected: maxScrollNotchesPerAxis},
		{name: "a huge negative delta is capped", delta: -1_000_000, expected: maxScrollNotchesPerAxis},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := scrollNotches(testCase.delta); got != testCase.expected {
				t.Errorf("scrollNotches(%d) = %d, want %d", testCase.delta, got, testCase.expected)
			}
		})
	}
}

func TestSelectFocusedFrame_RanksActiveAndShowingFirst(t *testing.T) {
	t.Parallel()

	frames := []frameCandidate{
		{showing: true},
		{active: true, showing: true},
		{active: true},
	}

	index, ok := selectFocusedFrame(frames)
	if !ok {
		t.Fatalf("selectFocusedFrame found nothing in %v", frames)
	}

	if index != 1 {
		t.Errorf("selectFocusedFrame picked %d, want the active and showing window at 1", index)
	}
}

func TestSelectFocusedFrame_FallsBackToActive(t *testing.T) {
	t.Parallel()

	frames := []frameCandidate{
		{showing: true},
		{active: true},
	}

	index, ok := selectFocusedFrame(frames)
	if !ok || index != 1 {
		t.Errorf("selectFocusedFrame picked (%d, %v), want the active window at 1", index, ok)
	}
}

func TestSelectFocusedFrame_FallsBackToShowing(t *testing.T) {
	t.Parallel()

	frames := []frameCandidate{
		{},
		{showing: true},
	}

	index, ok := selectFocusedFrame(frames)
	if !ok || index != 1 {
		t.Errorf("selectFocusedFrame picked (%d, %v), want the showing window at 1", index, ok)
	}
}

func TestSelectFocusedFrame_KeepsRegistryOrderOnATie(t *testing.T) {
	t.Parallel()

	frames := []frameCandidate{
		{active: true, showing: true},
		{active: true, showing: true},
	}

	index, ok := selectFocusedFrame(frames)
	if !ok || index != 0 {
		t.Errorf("selectFocusedFrame picked (%d, %v), want the first window at 0", index, ok)
	}
}

func TestSelectFocusedFrame_ReportsNothing(t *testing.T) {
	t.Parallel()

	for _, frames := range [][]frameCandidate{nil, {}, {{}}} {
		if index, ok := selectFocusedFrame(frames); ok {
			t.Errorf("selectFocusedFrame(%v) picked %d, want nothing", frames, index)
		}
	}
}

func TestIdentifierMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		candidate string
		want      string
		expected  bool
	}{
		{name: "exact", candidate: "Firefox", want: "firefox", expected: true},
		{name: "desktop id against a display name", candidate: "firefox", want: "org.mozilla.firefox", expected: true},
		{name: "display name against a desktop id", candidate: "org.mozilla.firefox", want: "firefox", expected: true},
		{name: "process name against a desktop id", candidate: "nautilus", want: "org.gnome.nautilus", expected: true},
		{name: "different applications", candidate: "code", want: "org.gnome.nautilus", expected: false},
		{name: "empty candidate", candidate: "", want: "firefox", expected: false},
		{name: "a bare suffix is not a segment", candidate: "com", want: "com.example", expected: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := identifierMatches(testCase.candidate, testCase.want)
			if got != testCase.expected {
				t.Errorf(
					"identifierMatches(%q, %q) = %v, want %v",
					testCase.candidate, testCase.want, got, testCase.expected,
				)
			}
		})
	}
}

func TestLastDotSegment(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"org.mozilla.firefox": "firefox",
		"firefox":             "",
		"trailing.":           "",
		"a.b":                 "b",
		"":                    "",
	}

	for value, expected := range cases {
		if got := lastDotSegment(value); got != expected {
			t.Errorf("lastDotSegment(%q) = %q, want %q", value, got, expected)
		}
	}
}

func TestNormalizeIdentifier(t *testing.T) {
	t.Parallel()

	if got := normalizeIdentifier("  Org.Mozilla.Firefox "); got != "org.mozilla.firefox" {
		t.Errorf("normalizeIdentifier did not fold case and whitespace: %q", got)
	}
}

// stateBits builds an AT-SPI state bitfield with the given state indices set, so
// a test can name states instead of computing shifts.
func stateBits(states ...uint) []uint32 {
	words := make([]uint32, 2)

	for _, state := range states {
		word := state / bitsPerStateWord
		if int(word) < len(words) {
			words[word] |= 1 << (state % bitsPerStateWord)
		}
	}

	return words
}

// TestSelectAppFrame_RanksTitleThenActiveThenShowing pins the ranking used once
// the compositor has named the focused application. Two windows of one
// application share an app_id, so the title is what separates them; ACTIVE and
// SHOWING are the fallbacks for a compositor that reports the application but
// not the title. ACTIVE is only trusted where the compositor maintains it.
func TestSelectAppFrame_RanksTitleThenActiveThenShowing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		frames       []frameCandidate
		ignoreActive bool
		want         int
		ok           bool
	}{
		{
			name: "a unique title match wins over ACTIVE siblings",
			frames: []frameCandidate{
				{active: true, showing: true, appMatch: true},
				{active: false, showing: true, appMatch: true, titleMatch: true},
			},
			want: 1,
			ok:   true,
		},
		{
			name: "a unique ACTIVE+SHOWING frame wins without a title",
			frames: []frameCandidate{
				{active: true, showing: true, appMatch: true},
				{active: false, showing: true, appMatch: true},
			},
			want: 0,
			ok:   true,
		},
		{
			name: "a unique SHOWING frame is the next fallback",
			frames: []frameCandidate{
				{showing: true, appMatch: true},
				{showing: false, appMatch: true},
			},
			want: 0,
			ok:   true,
		},
		{
			name: "two equally plausible siblings report nothing",
			frames: []frameCandidate{
				{active: true, showing: true, appMatch: true},
				{active: true, showing: true, appMatch: true},
			},
			ok: false,
		},
		{
			name: "an ambiguous title does not select",
			frames: []frameCandidate{
				{showing: true, appMatch: true, titleMatch: true},
				{showing: true, appMatch: true, titleMatch: true},
			},
			ok: false,
		},
		{
			name: "a title match of another application is not this application's",
			frames: []frameCandidate{
				{showing: true, titleMatch: true},
				{showing: true, appMatch: true},
			},
			want: 1,
			ok:   true,
		},
		{
			name: "an unreliable ACTIVE state leaves showing siblings unresolved",
			frames: []frameCandidate{
				{active: true, showing: true, appMatch: true},
				{active: false, showing: true, appMatch: true},
			},
			ignoreActive: true,
			ok:           false,
		},
		{
			name:   "no application match reports nothing",
			frames: []frameCandidate{{active: true, showing: true}},
			ok:     false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			index, ok := selectAppFrame(testCase.frames, !testCase.ignoreActive)
			if ok != testCase.ok {
				t.Fatalf("selectAppFrame() ok = %v, want %v", ok, testCase.ok)
			}

			if ok && index != testCase.want {
				t.Errorf("selectAppFrame() = %d, want %d", index, testCase.want)
			}
		})
	}
}

// TestSelectTitleFrame_RequiresAUniqueTitle pins the last identity signal for a
// compositor app_id that no AT-SPI application name answers to.
func TestSelectTitleFrame_RequiresAUniqueTitle(t *testing.T) {
	t.Parallel()

	unique := []frameCandidate{
		{showing: true, appMatch: true},
		{showing: true, appMatch: true, titleMatch: true},
	}

	if index, ok := selectTitleFrame(unique); !ok || index != 1 {
		t.Fatalf("selectTitleFrame(unique) = (%d, %v), want (1, true)", index, ok)
	}

	ambiguous := []frameCandidate{{titleMatch: true}, {titleMatch: true}}
	if _, ok := selectTitleFrame(ambiguous); ok {
		t.Error("selectTitleFrame picked one of two frames sharing the focused title")
	}

	if _, ok := selectTitleFrame([]frameCandidate{{showing: true}}); ok {
		t.Error("selectTitleFrame selected a frame with no title match at all")
	}
}
