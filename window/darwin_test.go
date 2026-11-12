//go:build darwin

package window

import (
	"image"
	"testing"
)

func TestOrderApplications_PutsTheFrontmostFirst(t *testing.T) {
	applications := []runningApplication{
		{pid: 1, bundleID: "a"},
		{pid: 2, bundleID: "b"},
		{pid: 3, bundleID: "c"},
	}

	ordered := orderApplications(applications, 2)

	want := []int32{2, 1, 3}
	for index, pid := range want {
		if ordered[index].pid != pid {
			t.Fatalf("position %d is pid %d, want %d", index, ordered[index].pid, pid)
		}
	}

	if len(ordered) != len(applications) {
		t.Fatalf("orderApplications changed the count: %d -> %d", len(applications), len(ordered))
	}
}

func TestOrderApplications_WithoutAKnownFrontmost(t *testing.T) {
	applications := []runningApplication{{pid: 1}, {pid: 2}}

	for _, frontmost := range []int32{0, -1, 99} {
		ordered := orderApplications(applications, frontmost)

		if ordered[0].pid != 1 || ordered[1].pid != 2 {
			t.Fatalf("frontmost %d reordered the list: %+v", frontmost, ordered)
		}
	}
}

func TestFocusedWindowIndex(t *testing.T) {
	cases := map[string]struct {
		states []windowState
		want   int
	}{
		"no windows":       {states: nil, want: -1},
		"main window wins": {states: []windowState{{}, {main: true}, {focused: true}}, want: 1},
		"focused is the fallback": {
			states: []windowState{{}, {focused: true}},
			want:   1,
		},
		"neither: the front one": {states: []windowState{{}, {}}, want: 0},
	}

	for name, test := range cases {
		if got := focusedWindowIndex(test.states); got != test.want {
			t.Fatalf("%s: focusedWindowIndex() = %d, want %d", name, got, test.want)
		}
	}
}

func TestWindowState_Untitled(t *testing.T) {
	cases := map[string]struct {
		state windowState
		want  bool
	}{
		"property absent":  {windowState{}, false},
		"empty title":      {windowState{title: "", hasTitle: true}, true},
		"whitespace title": {windowState{title: "  \t", hasTitle: true}, true},
		"real title":       {windowState{title: "Settings", hasTitle: true}, false},
	}

	for name, test := range cases {
		if got := test.state.untitled(); got != test.want {
			t.Fatalf("%s: untitled() = %v, want %v", name, got, test.want)
		}
	}
}

func TestWindowState_Bounds(t *testing.T) {
	state := windowState{
		position:    cgPoint{X: 1.4, Y: 2.6},
		size:        cgSize{Width: 10.4, Height: 20.6},
		hasPosition: true,
		hasSize:     true,
	}

	if got, want := state.bounds(), image.Rect(1, 3, 11, 24); got != want {
		t.Fatalf("bounds() = %v, want %v", got, want)
	}

	if got := (windowState{}).bounds(); !got.Empty() {
		t.Fatalf("bounds() without position or size = %v, want empty", got)
	}

	// A window at the origin is positioned, not absent.
	atOrigin := windowState{hasPosition: true, hasSize: true}
	if got := atOrigin.bounds(); got != image.Rect(0, 0, 0, 0) {
		t.Fatalf("bounds() at the origin with no size = %v", got)
	}
}

func TestFormatWindowID_ParseRoundTrip(t *testing.T) {
	cases := []struct {
		pid   int32
		index int
	}{
		{pid: 1, index: 0},
		{pid: 1234, index: 7},
		{pid: 99999, index: 0},
	}

	for _, test := range cases {
		formatted := formatWindowID(test.pid, test.index)

		pid, index, ok := parseWindowID(formatted)
		if !ok {
			t.Fatalf("parseWindowID(%q) failed", formatted)
		}

		if pid != test.pid || index != test.index {
			t.Fatalf("parseWindowID(%q) = %d:%d, want %d:%d", formatted, pid, index, test.pid, test.index)
		}
	}
}

func TestParseWindowID_RejectsMalformedIDs(t *testing.T) {
	for _, value := range []string{"", "1", "1:", ":2", "abc:1", "0:1", "1:-1", "1:2:3"} {
		if pid, index, ok := parseWindowID(value); ok {
			t.Fatalf("parseWindowID(%q) = %d:%d, want a rejection", value, pid, index)
		}
	}
}
