package compositor

import (
	"context"
	"testing"
)

// fakeEnv turns a map into the getenv function detectBackend takes, so detection
// is driven through its real inputs without mutating the process environment.
func fakeEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// TestDetectBackend pins the precedence neru's backend detector uses: the
// session family first, the desktop next, and the wlroots IPC socket last.
//
// A stale socket must never pick a source on a session the desktop named, and a
// desktop name on a non-Wayland session is not a Wayland session.
func TestDetectBackend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		want Backend
	}{
		{
			name: "nothing set is other",
			env:  nil,
			want: BackendOther,
		},
		{
			name: "x11 is other",
			env:  map[string]string{"DISPLAY": ":0"},
			want: BackendOther,
		},
		{
			name: "x11 named KDE is still other",
			env: map[string]string{
				"DISPLAY": ":0", "XDG_CURRENT_DESKTOP": "KDE",
			},
			want: BackendOther,
		},
		{
			name: "wayland KDE is kwin",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "KDE",
			},
			want: BackendKWin,
		},
		{
			name: "wayland GNOME is gnome",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "GNOME",
			},
			want: BackendGNOME,
		},
		{
			name: "wayland GNOME with xwayland is still gnome",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME",
				"DISPLAY": ":0",
			},
			want: BackendGNOME,
		},
		{
			name: "wayland COSMIC is cosmic",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "COSMIC",
			},
			want: BackendCOSMIC,
		},
		{
			name: "an unnamed wlroots session picks niri by its socket",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "NIRI_SOCKET": "/run/niri.sock",
			},
			want: BackendNiri,
		},
		{
			name: "an unnamed wlroots session picks sway by its socket",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "SWAYSOCK": "/run/sway.sock",
			},
			want: BackendSway,
		},
		{
			name: "an unnamed wlroots session picks Hyprland by its signature",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "HYPRLAND_INSTANCE_SIGNATURE": "abc",
			},
			want: BackendHyprland,
		},
		{
			name: "a desktop name picks its compositor",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "sway",
				"SWAYSOCK": "/run/sway.sock",
			},
			want: BackendSway,
		},
		{
			name: "the xdg-desktop-portal wlroots tag counts",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "labwc:wlroots",
				"SWAYSOCK": "/run/sway.sock",
			},
			want: BackendSway,
		},
		{
			name: "a stale wlroots socket does not override KDE",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "KDE",
				"SWAYSOCK": "/run/sway.sock",
			},
			want: BackendKWin,
		},
		{
			name: "a wlroots compositor with no IPC socket is other",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "river",
			},
			want: BackendOther,
		},
		{
			name: "a compositor outside the family does not borrow a wlroots socket",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "XFCE",
				"SWAYSOCK": "/run/sway.sock",
			},
			want: BackendOther,
		},
		{
			name: "XDG_SESSION_TYPE is not a second opinion",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "KDE",
				"XDG_SESSION_TYPE": "x11",
			},
			want: BackendKWin,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := detectBackend(fakeEnv(testCase.env)); got != testCase.want {
				t.Errorf("detectBackend(%v) = %q, want %q", testCase.env, got, testCase.want)
			}
		})
	}
}

// TestDetect_ReportsTheSessionThroughTheRealEnvironment exercises the exported
// entry point once, with the variables it reads pinned so the answer does not
// depend on the host.
func TestDetect_ReportsTheSessionThroughTheRealEnvironment(t *testing.T) {
	// These are the variables detection reads. Clearing them makes the expected
	// answer the same on every host.
	for _, name := range []string{
		envWaylandDisplay, "DISPLAY", envCurrentDesktop,
		envNiriSocket, envSwaySocket, envHyprlandSig,
	} {
		t.Setenv(name, "")
	}

	resolver := Detect(context.Background())
	if resolver == nil {
		t.Fatal("Detect returned nil")
	}

	if got := resolver.Backend(); got != BackendOther.String() {
		t.Errorf("Detect().Backend() = %q, want %q", got, BackendOther.String())
	}
}
