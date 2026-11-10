package compositor

import (
	"image"
	"testing"
)

// TestParseWindowPayload covers the wire format between the KWin script and this
// package. The caption is last because it is the one field that can contain a
// comma, and the split keeps the remainder there.
func TestParseWindowPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
		window  kwinWindow
		ok      bool
	}{
		{
			name:    "geometry and identity",
			payload: "100,50,800,600,konsole,konsole,~ : bash",
			window: kwinWindow{
				Rect:  image.Rect(100, 50, 900, 650),
				Class: "konsole",
				Name:  "konsole",
				Title: "~ : bash",
			},
			ok: true,
		},
		{
			name:    "geometry only",
			payload: "100,50,800,600",
			window:  kwinWindow{Rect: image.Rect(100, 50, 900, 650)},
			ok:      true,
		},
		{
			name:    "a caption containing commas survives",
			payload: "100,50,800,600,firefox,navigator,notes, and more, v2",
			window: kwinWindow{
				Rect:  image.Rect(100, 50, 900, 650),
				Class: "firefox",
				Name:  "navigator",
				Title: "notes, and more, v2",
			},
			ok: true,
		},
		{
			name:    "surrounding whitespace is trimmed from the geometry",
			payload: " 100 , 50 , 800 , 600 ,konsole,konsole,bash",
			window: kwinWindow{
				Rect:  image.Rect(100, 50, 900, 650),
				Class: "konsole",
				Name:  "konsole",
				Title: "bash",
			},
			ok: true,
		},
		{name: "too few fields", payload: "100,50,800", ok: false},
		{name: "non-numeric coordinate", payload: "x,50,800,600", ok: false},
		{name: "zero width", payload: "100,50,0,600", ok: false},
		{name: "negative height", payload: "100,50,800,-1", ok: false},
		{name: "empty payload", payload: "", ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			window, ok := parseWindowPayload(testCase.payload)
			if ok != testCase.ok {
				t.Fatalf("parseWindowPayload(%q) ok = %v, want %v", testCase.payload, ok, testCase.ok)
			}

			if !ok {
				return
			}

			if window != testCase.window {
				t.Errorf("parseWindowPayload(%q) = %+v, want %+v", testCase.payload, window, testCase.window)
			}
		})
	}
}

// TestKWinWindowPublic pins which of KWin's two identifiers becomes the reported
// application identity: resourceClass, because that is what a foreign-toplevel
// app_id is usually built from, with resourceName as the fallback.
func TestKWinWindowPublic(t *testing.T) {
	t.Parallel()

	both := kwinWindow{Class: "firefox", Name: "navigator", Title: "Mozilla Firefox"}
	if got := both.public().AppID; got != "firefox" {
		t.Errorf("public().AppID = %q, want %q", got, "firefox")
	}

	onlyName := kwinWindow{Name: "navigator"}
	if got := onlyName.public().AppID; got != "navigator" {
		t.Errorf("public().AppID = %q, want %q", got, "navigator")
	}
}
