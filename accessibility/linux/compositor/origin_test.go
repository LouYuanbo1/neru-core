package compositor

import (
	"context"
	"encoding/json"
	"image"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// These tests port the pure rules from the neru daemon's window_origin.go,
// kwin_origin.go, gnome_origin.go, niri_geometry.go, sway_geometry.go,
// hyprland_geometry.go and cosmic_geometry.go. None of them needs a desktop:
// they exercise parsing, identity matching, the size gate that guards every
// origin, and each compositor's arithmetic.

// TestIdentifierMatches pins the tolerant application comparison every source
// uses. A compositor's app_id is often reverse-DNS where AT-SPI and KWin's
// resourceClass report a bare name, so comparing either side's last dot segment
// is what keeps one application from looking like two.
func TestIdentifierMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		candidate string
		want      string
		match     bool
	}{
		{name: "reverse-dns app_id vs display name", candidate: "Firefox", want: "org.mozilla.firefox", match: true},
		{name: "bare app_id case-insensitive", candidate: "Helium", want: "helium", match: true},
		{name: "exact match", candidate: "konsole", want: "konsole", match: true},
		{name: "reverse-dns both sides", candidate: "org.kde.konsole", want: "konsole", match: true},
		{name: "kde reverse-dns app_id", candidate: "Konsole", want: "org.kde.konsole", match: true},
		{name: "no match", candidate: "Unnamed", want: "org.mozilla.firefox", match: false},
		{name: "empty candidate", candidate: "", want: "org.mozilla.firefox", match: false},
		{name: "empty app_id", candidate: "Firefox", want: "", match: false},
		{name: "segment does not collide", candidate: "fox", want: "org.mozilla.firefox", match: false},
		{name: "different apps", candidate: "Nautilus", want: "org.gnome.Calculator", match: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := identifierMatches(testCase.candidate, testCase.want); got != testCase.match {
				t.Errorf("identifierMatches(%q, %q) = %v, want %v",
					testCase.candidate, testCase.want, got, testCase.match)
			}
		})
	}
}

// TestAppMatches_AnAbsentIdentityIsNotAMismatch is the half that keeps the
// origin check from being worse than no check. X11 and GNOME report no
// compositor identity, and refusing there would unoffset every hint on a whole
// session to close a rare misplacement.
func TestAppMatches_AnAbsentIdentityIsNotAMismatch(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, window, frame string }{
		{name: "no frame identity", window: "konsole", frame: ""},
		{name: "no window identity", window: "", frame: "konsole"},
		{name: "neither identity", window: "", frame: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if !appMatches(testCase.window, testCase.frame) {
				t.Errorf("appMatches(%q, %q) = false, want true", testCase.window, testCase.frame)
			}
		})
	}
}

// TestTitleMatches pins where the title comparison draws its line. KWin appends
// its own suffix to a caption (a window shortcut, a hostname, "<2>" for a shared
// title), and the foreign-toplevel title may predate it, so a prefix either way
// counts. Exact matching would unoffset such a window for good.
func TestTitleMatches(t *testing.T) {
	t.Parallel()

	const bash = "~ : bash"

	cases := []struct {
		name    string
		caption string
		focused string
		match   bool
	}{
		{name: "identical", caption: bash, focused: bash, match: true},
		{name: "whitespace only", caption: "  ~ : bash ", focused: bash, match: true},
		{name: "kwin appended a shortcut suffix", caption: "Kate {Ctrl+1}", focused: "Kate", match: true},
		{name: "the compositor carried the suffix", caption: "Kate", focused: "Kate {Ctrl+1}", match: true},
		{name: "different documents", caption: "notes.md — Kate", focused: "main.go — Kate", match: false},
		{name: "different applications entirely", caption: bash, focused: "untitled — Kate", match: false},
		{name: "no caption to compare", caption: "", focused: bash, match: true},
		{name: "no focused title to compare", caption: bash, focused: "", match: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := titleMatches(testCase.caption, testCase.focused); got != testCase.match {
				t.Errorf("titleMatches(%q, %q) = %v, want %v",
					testCase.caption, testCase.focused, got, testCase.match)
			}
		})
	}
}

// TestSizeMatches pins the race guard shared by every source. The compositor's
// rectangle names an origin only for a window of the size AT-SPI just measured,
// because focus can move between the two reads.
func TestSizeMatches(t *testing.T) {
	t.Parallel()

	frame := image.Rect(100, 50, 900, 650)

	cases := []struct {
		name   string
		window image.Rectangle
		match  bool
	}{
		{name: "exact size", window: image.Rect(0, 0, 800, 600), match: true},
		{name: "within tolerance", window: image.Rect(0, 0, 800+originSizeTolerance, 600), match: true},
		{name: "another window", window: image.Rect(0, 0, 400, 300), match: false},
		{name: "no window geometry", window: image.Rectangle{}, match: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := sizeMatches(frame, testCase.window); got != testCase.match {
				t.Errorf("sizeMatches(%v, %v) = %v, want %v",
					frame, testCase.window, got, testCase.match)
			}
		})
	}

	if sizeMatches(image.Rectangle{}, image.Rect(0, 0, 800, 600)) {
		t.Error("an empty AT-SPI frame matched a compositor rectangle; nobody measured this window")
	}
}

// cachedKonsole is a window KWin reported, sized to match the frame every case
// below asks about, so size is never what decides the answer.
func cachedKonsole() kwinWindow {
	return kwinWindow{
		Rect:  image.Rect(100, 50, 900, 650),
		Class: "konsole",
		Title: "~ : bash",
	}
}

// konsoleFrame is the AT-SPI frame those cases are walking: the same size, and
// the compositor identity it was selected with.
func konsoleFrame(appID, title string) Window {
	return Window{AppID: appID, Title: title, Rect: image.Rect(100, 50, 900, 650)}
}

// TestKWinOrigin_OffsetsByTheMatchingWindow is the ordinary case.
func TestKWinOrigin_OffsetsByTheMatchingWindow(t *testing.T) {
	t.Parallel()

	origin, ok := kwinOrigin(cachedKonsole(), konsoleFrame("konsole", "~ : bash"))
	if !ok || origin != image.Pt(100, 50) {
		t.Fatalf("kwinOrigin() = (%v, %v), want ((100,50), true)", origin, ok)
	}
}

// TestKWinOrigin_RejectsAnotherApplication covers what size alone catches only by
// luck: two windows of different applications can be the same size, and
// offsetting by the wrong one puts every hint at the other window's position.
func TestKWinOrigin_RejectsAnotherApplication(t *testing.T) {
	t.Parallel()

	if _, ok := kwinOrigin(cachedKonsole(), konsoleFrame("kate", "untitled — Kate")); ok {
		t.Fatal("kwinOrigin accepted a cached window of another application")
	}
}

// TestKWinOrigin_RejectsASiblingWindow is #972 exactly: two windows of the same
// application are the same size, so only the title separates them.
func TestKWinOrigin_RejectsASiblingWindow(t *testing.T) {
	t.Parallel()

	if _, ok := kwinOrigin(cachedKonsole(), konsoleFrame("konsole", "/var/log : less")); ok {
		t.Fatal("kwinOrigin accepted a same-sized sibling window")
	}
}

// TestKWinOrigin_ToleratesIdentifierSpelling keeps the identity check from
// rejecting windows that match. A false reject unoffsets hints on every
// activation of that window; a false accept costs no more than having no check.
func TestKWinOrigin_ToleratesIdentifierSpelling(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, class, name2, appID string }{
		{name: "identical", class: "konsole", name2: "konsole", appID: "konsole"},
		{name: "different case", class: "Konsole", name2: "konsole", appID: "konsole"},
		{name: "reverse-dns on the compositor side", class: "konsole", name2: "konsole", appID: "org.kde.konsole"},
		{name: "reverse-dns on the script side", class: "org.kde.konsole", name2: "konsole", appID: "konsole"},
		{name: "app_id built from resourceClass", class: "firefox", name2: "navigator", appID: "firefox"},
		{name: "app_id built from resourceName", class: "firefox", name2: "navigator", appID: "navigator"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			window := cachedKonsole()
			window.Class, window.Name = testCase.class, testCase.name2

			if _, ok := kwinOrigin(window, konsoleFrame(testCase.appID, "~ : bash")); !ok {
				t.Errorf("kwinOrigin rejected class %q / name %q against app_id %q",
					testCase.class, testCase.name2, testCase.appID)
			}
		})
	}
}

// TestKWinOrigin_AcceptsAnAbsentIdentity keeps the check from being worse than
// what it replaces.
func TestKWinOrigin_AcceptsAnAbsentIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		window kwinWindow
		frame  Window
	}{
		{
			name:   "the compositor reported no identity",
			window: cachedKonsole(),
			frame:  konsoleFrame("", ""),
		},
		{
			name:   "the script reported no identifiers or caption",
			window: kwinWindow{Rect: image.Rect(100, 50, 900, 650)},
			frame:  konsoleFrame("konsole", "~ : bash"),
		},
		{
			name:   "only the titles are missing",
			window: cachedKonsole(),
			frame:  konsoleFrame("konsole", ""),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if _, ok := kwinOrigin(testCase.window, testCase.frame); !ok {
				t.Error("kwinOrigin rejected a window whose identity nobody reported")
			}
		})
	}
}

// TestKWinOrigin_StillRejectsASizeMismatch keeps the older check in place: it is
// the only one available when there is no identity to compare.
func TestKWinOrigin_StillRejectsASizeMismatch(t *testing.T) {
	t.Parallel()

	window := cachedKonsole()
	window.Rect = image.Rect(100, 50, 1300, 950)

	if _, ok := kwinOrigin(window, konsoleFrame("konsole", "~ : bash")); ok {
		t.Fatal("kwinOrigin accepted a window of another size")
	}
}

// TestKWinSource_ReportsNothingCachedAsAnAnswer covers the state a clear leaves
// behind: nothing is focused, which is the compositor answering.
func TestKWinSource_ReportsNothingCachedAsAnAnswer(t *testing.T) {
	t.Parallel()

	source := newKWinSource()
	source.installed = true
	source.lastAttempt = time.Now()

	origin, ok, err := source.origin(t.Context(), konsoleFrame("konsole", "~ : bash"))
	if ok || err != nil {
		t.Fatalf("origin() = (%v, %v, %v), want (empty, false, nil)", origin, ok, err)
	}
}

// TestKWinSource_ReportsAFailedInstall keeps a broken bridge distinguishable from
// an idle one. Both leave hints window-relative, and only one is something a
// person can fix.
func TestKWinSource_ReportsAFailedInstall(t *testing.T) {
	t.Parallel()

	source := newKWinSource()
	source.installErr = errKWinAbsent
	source.lastAttempt = time.Now()

	_, ok, err := source.origin(t.Context(), konsoleFrame("konsole", "~ : bash"))
	if ok || err == nil {
		t.Fatalf("origin() = (_, %v, %v), want no origin and the install error", ok, err)
	}

	if !errs.IsNotSupported(err) {
		t.Errorf("origin() code = %q, want NOT_SUPPORTED: what a person has to do "+
			"about this is not check a permission", errs.GetCode(err))
	}
}

// TestGNOMEOrigin pins the extension's accept-or-reject rules, including the
// size check that catches a transient the shell reported and AT-SPI did not.
func TestGNOMEOrigin(t *testing.T) {
	t.Parallel()

	const (
		appID = "org.gnome.Nautilus"
		title = "Home"
	)

	window := Window{
		AppID: appID,
		Title: title,
		Rect:  image.Rect(100, 50, 900, 650),
	}
	frame := Window{AppID: appID, Title: title, Rect: image.Rect(100, 50, 900, 650)}

	cases := []struct {
		name   string
		window Window
		frame  Window
		want   image.Point
		ok     bool
	}{
		{name: "matching window offsets", window: window, frame: frame, want: image.Pt(100, 50), ok: true},
		{
			name:   "another application is rejected",
			window: window,
			frame:  Window{AppID: "firefox", Title: title, Rect: frame.Rect},
		},
		{
			name:   "another document is rejected",
			window: window,
			frame:  Window{AppID: appID, Title: "Downloads", Rect: frame.Rect},
		},
		{
			name:   "a different size is rejected",
			window: window,
			frame:  Window{AppID: appID, Title: title, Rect: image.Rect(100, 50, 500, 650)},
		},
		{
			name:   "an identity nobody reported is not a mismatch",
			window: Window{Rect: window.Rect},
			frame:  Window{Rect: frame.Rect},
			want:   image.Pt(100, 50),
			ok:     true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, ok := gnomeOrigin(testCase.window, testCase.frame)
			if ok != testCase.ok || got != testCase.want {
				t.Errorf("gnomeOrigin() = (%v, %v), want (%v, %v)", got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

// TestGNOMESource_OriginFor reports the extension's refusal and "nothing
// focused" separately, which is what keeps a missing extension fixable.
func TestGNOMESource_OriginFor(t *testing.T) {
	t.Parallel()

	frame := Window{Rect: image.Rect(100, 50, 900, 650)}

	absent := newGNOMESource()
	absent.query = func(_ context.Context) (Window, bool, error) {
		return Window{}, false, errGNOMEAbsent(errKWinAbsent)
	}

	if _, ok, err := absent.origin(t.Context(), frame); ok || err == nil {
		t.Fatal("origin() reported no error for an extension that could not be asked")
	}

	empty := newGNOMESource()
	empty.query = func(_ context.Context) (Window, bool, error) {
		return Window{}, false, nil
	}

	if _, ok, err := empty.origin(t.Context(), frame); ok || err != nil {
		t.Fatal("origin() turned the shell's empty answer into a failure")
	}
}

// TestCosmicOrigin_RejectsAFrameOfAnotherSize pins the race guard the embedder's
// geometry gets: a rectangle names the origin only for a window of the size
// AT-SPI just measured.
func TestCosmicOrigin_RejectsAFrameOfAnotherSize(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(120, 40, 920, 640)

	cases := []struct {
		name  string
		frame Window
		ok    bool
	}{
		{name: "exact size", frame: Window{Rect: image.Rect(0, 0, 800, 600)}, ok: true},
		{name: "within tolerance", frame: Window{Rect: image.Rect(0, 0, 800+originSizeTolerance, 600)}, ok: true},
		{name: "another window", frame: Window{Rect: image.Rect(0, 0, 400, 300)}, ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			origin, ok := cosmicOrigin(bounds, testCase.frame)
			if ok != testCase.ok {
				t.Fatalf("cosmicOrigin() accepted = %v, want %v", ok, testCase.ok)
			}

			if ok && origin != bounds.Min {
				t.Errorf("cosmicOrigin() = %v, want %v", origin, bounds.Min)
			}
		})
	}
}

// TestPlacementFor pins the SCREEN-vs-WINDOW choice a scan makes once the
// compositor's origin is known. Window extents are exact and are preferred;
// screen extents are the fallback for a toolkit that cannot answer in window
// coordinates, and the offset then corrects a zero screen origin.
func TestPlacementFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		origin     image.Point
		screen     image.Rectangle
		window     image.Rectangle
		haveWindow bool
		relative   bool
		wantOffset image.Point
	}{
		{
			name:       "window extents are read relative to the window",
			origin:     image.Pt(100, 50),
			screen:     image.Rect(0, 0, 800, 600),
			window:     image.Rect(5, 20, 805, 620),
			haveWindow: true,
			relative:   true,
			wantOffset: image.Pt(95, 30),
		},
		{
			name:       "a zero screen origin is corrected without window extents",
			origin:     image.Pt(100, 50),
			screen:     image.Rect(0, 0, 800, 600),
			haveWindow: false,
			relative:   false,
			wantOffset: image.Pt(100, 50),
		},
		{
			name:       "correct screen coordinates need no offset",
			origin:     image.Pt(100, 50),
			screen:     image.Rect(100, 50, 900, 650),
			haveWindow: false,
			relative:   false,
			wantOffset: image.Pt(0, 0),
		},
		{
			name:       "no frame extents means no placement at all",
			origin:     image.Pt(100, 50),
			haveWindow: true,
			window:     image.Rect(5, 20, 805, 620),
			relative:   false,
			wantOffset: image.Pt(0, 0),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := PlacementFor(testCase.origin, testCase.screen, testCase.window, testCase.haveWindow)
			if got.WindowRelative != testCase.relative || got.Offset != testCase.wantOffset {
				t.Errorf("PlacementFor() = (%v, %v), want (%v, %v)",
					got.WindowRelative, got.Offset, testCase.relative, testCase.wantOffset)
			}
		})
	}
}

// TestNiriOrigin covers the gate every niri case turns on, whether niri gave this
// window a position at all, and the arithmetic that places the position it did
// give.
func TestNiriOrigin(t *testing.T) {
	t.Parallel()

	output := niriOutput{}
	output.Logical.X = 100
	output.Logical.Y = 10

	makeWindow := func(size []int, tile []float64) niriWindow {
		var window niriWindow

		window.Layout.WindowSize = size
		window.Layout.TilePosInWorkspaceView = tile

		return window
	}

	cases := []struct {
		name           string
		window         niriWindow
		frameW, frameH int
		wantX, wantY   int
		ok             bool
	}{
		{
			name:   "floating window offset by output+tile",
			window: makeWindow([]int{946, 942}, []float64{958, 52}),
			frameW: 946, frameH: 942,
			wantX: 1058, wantY: 62,
			ok: true,
		},
		{
			name:   "tiled window has no tile_pos",
			window: makeWindow([]int{946, 942}, nil),
			frameW: 946, frameH: 942,
			ok: false,
		},
		{
			name:   "size mismatch rejected",
			window: makeWindow([]int{946, 942}, []float64{958, 52}),
			frameW: 500, frameH: 942,
			ok: false,
		},
		{
			name:   "size within tolerance accepted",
			window: makeWindow([]int{946, 942}, []float64{958, 52}),
			frameW: 950, frameH: 940,
			wantX: 1058, wantY: 62,
			ok: true,
		},
		{
			name:   "missing window_size rejected",
			window: makeWindow(nil, []float64{958, 52}),
			frameW: 946, frameH: 942,
			ok: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			frame := Window{Rect: image.Rect(0, 0, testCase.frameW, testCase.frameH)}
			origin, ok := niriOrigin(testCase.window, output, frame)

			if ok != testCase.ok {
				t.Fatalf("niriOrigin() ok = %v, want %v", ok, testCase.ok)
			}

			if ok && origin != image.Pt(testCase.wantX, testCase.wantY) {
				t.Errorf("niriOrigin() = %v, want (%d,%d)", origin, testCase.wantX, testCase.wantY)
			}
		})
	}
}

// TestNiriScreenRect_SkipsTheOutputQueryForATiledWindow pins niri's exception on
// the focus path: a tiled window has no workspace-view position, so no
// rectangle, and the caller does not ask niri's output for one.
func TestNiriScreenRect_SkipsTheOutputQueryForATiledWindow(t *testing.T) {
	t.Parallel()

	var window niriWindow
	window.Layout.WindowSize = []int{946, 942}

	if _, ok := niriScreenRect(window, niriOutput{}); ok {
		t.Fatal("a tiled niri window reported a screen rectangle")
	}
}

// swayTreeJSON is a trimmed `swaymsg -t get_tree` with a focused window nested
// under an output, a workspace and a container, plus a decoration offset in
// window_rect.
const swayTreeJSON = `{
  "focused": false, "rect": {"x":0,"y":0,"width":1920,"height":1080},
  "window_rect": {"x":0,"y":0,"width":0,"height":0},
  "nodes": [
    {"focused": false, "rect": {"x":960,"y":0,"width":960,"height":1080},
     "window_rect": {"x":0,"y":0,"width":0,"height":0},
     "nodes": [
       {"focused": true, "rect": {"x":960,"y":0,"width":960,"height":1080},
        "window_rect": {"x":2,"y":24,"width":956,"height":1052}, "nodes": [], "floating_nodes": []}
     ], "floating_nodes": []}
  ], "floating_nodes": []
}`

// TestSwayOrigin checks the content origin, rect plus window_rect, and that a
// size mismatch is rejected.
func TestSwayOrigin(t *testing.T) {
	t.Parallel()

	var tree swayNode

	err := json.Unmarshal([]byte(swayTreeJSON), &tree)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Content origin = rect(960,0) + window_rect(2,24) = (962,24). The content
	// size is window_rect, 956x1052.
	origin, ok := swayOrigin(&tree, Window{Rect: image.Rect(0, 0, 956, 1052)})
	if !ok || origin != image.Pt(962, 24) {
		t.Fatalf("focused content origin: got (%v, %v), want ((962,24), true)", origin, ok)
	}

	if _, ok := swayOrigin(&tree, Window{Rect: image.Rect(0, 0, 500, 500)}); ok {
		t.Fatal("swayOrigin accepted a frame of another size")
	}
}

// TestSwayFindFocusedNoneFocused pins that "no focused node" is found rather than
// defaulting to the tree root.
func TestSwayFindFocusedNoneFocused(t *testing.T) {
	t.Parallel()

	tree := swayNode{Focused: false, Nodes: []swayNode{{Focused: false}}}
	if findFocused(&tree) != nil {
		t.Fatal("findFocused returned a node on a tree with no focused node")
	}
}

// TestSwayAppID covers sway's two identity vocabularies: app_id for a native
// Wayland client and window_properties for an XWayland one.
func TestSwayAppID(t *testing.T) {
	t.Parallel()

	wayland := swayNode{AppID: "kitty"}
	if got := swayAppID(&wayland); got != "kitty" {
		t.Errorf("swayAppID(wayland) = %q, want %q", got, "kitty")
	}

	xwayland := swayNode{}
	xwayland.WindowProperties.Class = "Firefox"
	xwayland.WindowProperties.Instance = "Navigator"

	if got := swayAppID(&xwayland); got != "Firefox" {
		t.Errorf("swayAppID(xwayland) = %q, want %q", got, "Firefox")
	}
}

// TestHyprlandOrigin covers the active window's absolute position and the size
// gate.
func TestHyprlandOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		window hyprlandWindow
		frame  Window
		want   image.Point
		ok     bool
	}{
		{
			name:   "active window at/size",
			window: hyprlandWindow{At: []int{958, 52}, Size: []int{946, 942}},
			frame:  Window{Rect: image.Rect(0, 0, 946, 942)},
			want:   image.Pt(958, 52),
			ok:     true,
		},
		{
			name:   "size mismatch rejected",
			window: hyprlandWindow{At: []int{958, 52}, Size: []int{946, 942}},
			frame:  Window{Rect: image.Rect(0, 0, 500, 500)},
			ok:     false,
		},
		{
			name:   "missing at rejected",
			window: hyprlandWindow{Size: []int{946, 942}},
			frame:  Window{Rect: image.Rect(0, 0, 946, 942)},
			ok:     false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			origin, ok := hyprlandOrigin(testCase.window, testCase.frame)
			if ok != testCase.ok || (ok && origin != testCase.want) {
				t.Errorf("hyprlandOrigin() = (%v, %v), want (%v, %v)",
					origin, ok, testCase.want, testCase.ok)
			}
		})
	}
}
