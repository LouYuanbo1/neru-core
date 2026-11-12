package window

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// sampleWindows is a front-to-back desktop: the focused editor, a second
// editor window behind it, a browser, and a second browser window.
func sampleWindows() []Info {
	return []Info{
		{
			ID:          "0x1",
			PID:         100,
			Title:       "notes.txt - Notepad",
			Application: "notepad.exe",
			Bounds:      image.Rect(0, 0, 800, 600),
			Focused:     true,
		},
		{
			ID:          "0x2",
			PID:         100,
			Title:       "todo.txt - Notepad",
			Application: "notepad.exe",
			Bounds:      image.Rect(10, 10, 810, 610),
		},
		{
			ID:          "0x3",
			PID:         200,
			Title:       "Settings - Google Chrome",
			Application: "chrome.exe",
			Bounds:      image.Rect(100, 100, 1300, 900),
		},
		{
			ID:          "0x4",
			PID:         200,
			Title:       "Docs - Google Chrome",
			Application: "chrome.exe",
			Bounds:      image.Rect(120, 120, 1320, 920),
			Minimized:   true,
		},
	}
}

func TestFind_ByIDIsExact(t *testing.T) {
	got, ok := Find(sampleWindows(), Target{ID: "0x3"})
	if !ok {
		t.Fatal("Find by id: no window")
	}

	if got.Title != "Settings - Google Chrome" {
		t.Fatalf("Find by id: got %q", got.Title)
	}
}

func TestFind_UnknownIDDoesNotFallThrough(t *testing.T) {
	// A target with an id and a pid must use the id: silently falling back to
	// the pid would activate a different window than the caller named.
	if _, ok := Find(sampleWindows(), Target{ID: "0xdead", PID: 200}); ok {
		t.Fatal("Find by unknown id: matched a window through the pid")
	}
}

func TestFind_ByPIDPrefersTheFocusedWindow(t *testing.T) {
	got, ok := Find(sampleWindows(), Target{PID: 200})
	if !ok {
		t.Fatal("Find by pid: no window")
	}

	// Both chrome windows match; neither is focused, so the frontmost one wins.
	if got.ID != "0x3" {
		t.Fatalf("Find by pid: got %q, want the frontmost chrome window", got.ID)
	}

	got, ok = Find(sampleWindows(), Target{PID: 100})
	if !ok {
		t.Fatal("Find by pid: no window")
	}

	if got.ID != "0x1" {
		t.Fatalf("Find by pid: got %q, want the focused editor window", got.ID)
	}
}

func TestFind_ByApplicationIgnoresCaseAndDirectories(t *testing.T) {
	for _, application := range []string{
		"chrome.exe",
		"CHROME.EXE",
		"  chrome.exe  ",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	} {
		got, ok := Find(sampleWindows(), Target{Application: application})
		if !ok {
			t.Fatalf("Find by application %q: no window", application)
		}

		if got.ID != "0x3" {
			t.Fatalf("Find by application %q: got %q", application, got.ID)
		}
	}
}

func TestFind_ByTitleIsACaseInsensitiveSubstring(t *testing.T) {
	got, ok := Find(sampleWindows(), Target{Title: "docs"})
	if !ok {
		t.Fatal("Find by title: no window")
	}

	if got.ID != "0x4" {
		t.Fatalf("Find by title: got %q", got.ID)
	}
}

func TestFind_PrefersTheFocusedWindowAmongMatches(t *testing.T) {
	windows := sampleWindows()
	// Make the second chrome window focused: it must win over the earlier one.
	windows[3].Focused = true
	windows[0].Focused = false

	got, ok := Find(windows, Target{Application: "chrome.exe"})
	if !ok {
		t.Fatal("Find by application: no window")
	}

	if got.ID != "0x4" {
		t.Fatalf("Find by application: got %q, want the focused window", got.ID)
	}
}

func TestFind_EmptyTargetMatchesNothing(t *testing.T) {
	if _, ok := Find(sampleWindows(), Target{}); ok {
		t.Fatal("Find with an empty target matched a window")
	}

	if _, ok := Find(sampleWindows(), Target{Application: "   ", Title: "\t"}); ok {
		t.Fatal("Find with a whitespace-only target matched a window")
	}
}

func TestFind_NoMatch(t *testing.T) {
	if _, ok := Find(sampleWindows(), Target{Title: "zzz"}); ok {
		t.Fatal("Find matched a window no field names")
	}
}

func TestTargetEmpty(t *testing.T) {
	cases := map[string]struct {
		target Target
		want   bool
	}{
		"zero value":        {Target{}, true},
		"blank application": {Target{Application: "  "}, true},
		"blank title":       {Target{Title: "\n"}, true},
		"negative pid":      {Target{PID: -1}, true},
		"id":                {Target{ID: "0x1"}, false},
		"pid":               {Target{PID: 7}, false},
		"application":       {Target{Application: "code"}, false},
		"title":             {Target{Title: "x"}, false},
	}

	for name, test := range cases {
		if got := test.target.Empty(); got != test.want {
			t.Fatalf("%s: Empty() = %v, want %v", name, got, test.want)
		}
	}
}

func TestSupported(t *testing.T) {
	// The test runs on a platform this module supports; the exact backend is
	// the build's business, but the answer must agree with it.
	if !Supported() {
		t.Skip("this platform has no window backend")
	}
}

func TestValidateBounds(t *testing.T) {
	cases := map[string]struct {
		bounds image.Rectangle
		ok     bool
	}{
		"ordinary":        {bounds: image.Rect(0, 0, 800, 600), ok: true},
		"negative origin": {bounds: image.Rect(-1920, -200, -920, 400), ok: true},
		"zero area":       {bounds: image.Rect(10, 10, 10, 10)},
		"zero width":      {bounds: image.Rect(10, 10, 10, 200)},
		"zero height":     {bounds: image.Rect(10, 10, 200, 10)},
		"inverted width": {
			bounds: image.Rectangle{Min: image.Pt(200, 10), Max: image.Pt(10, 200)},
		},
	}

	for name, test := range cases {
		err := validateBounds(test.bounds)

		if test.ok && err != nil {
			t.Fatalf("%s: validateBounds(%v) = %v, want nil", name, test.bounds, err)
		}

		if !test.ok {
			if err == nil {
				t.Fatalf("%s: validateBounds(%v) = nil, want an error", name, test.bounds)
			}

			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Fatalf("%s: code = %q, want %q", name, errs.GetCode(err), errs.CodeInvalidInput)
			}
		}
	}
}
