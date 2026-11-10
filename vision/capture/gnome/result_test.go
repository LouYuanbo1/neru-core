package gnome

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestResultPath_UsesTheFilenameTheShellReported(t *testing.T) {
	requested := "/run/user/1000/neru-core-screenshot-1/screenshot-2.png"

	tests := []struct {
		name string
		used string
		want string
	}{
		{name: "the name it was given", used: requested, want: requested},
		{name: "an empty name falls back to the request", used: "", want: requested},
		{
			name: "a file URI is decoded",
			used: "file:///run/user/1000/neru-core-screenshot-1/screenshot-2.png",
			want: requested,
		},
		{
			name: "a localhost file URI is decoded",
			used: "file://localhost/run/user/1000/neru-core-screenshot-1/screenshot-2.png",
			want: requested,
		},
		{
			name: "a percent-encoded name is decoded",
			used: "file:///tmp/with%20space/shot.png",
			want: "/tmp/with space/shot.png",
		},
		{
			name: "a query and a fragment are dropped",
			used: "file:///tmp/shot.png?size=large#top",
			want: "/tmp/shot.png",
		},
		{
			name: "a redundant path is cleaned",
			used: "/tmp/./shot.png",
			want: "/tmp/shot.png",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resultPath(test.used, requested)
			if err != nil {
				t.Fatalf("resultPath: %v", err)
			}

			if got != test.want {
				t.Errorf("resultPath(%q) = %q, want %q", test.used, got, test.want)
			}
		})
	}
}

func TestResultPath_RefusesANameThisPackageDidNotAskFor(t *testing.T) {
	requested := "/run/user/1000/neru-core-screenshot-1/screenshot-2.png"

	tests := []struct {
		name string
		used string
	}{
		{name: "a bare basename", used: "Screenshot From 2026-11-10 05-00-00.png"},
		{name: "a relative path", used: "shots/shot.png"},
		{name: "a URI naming another host", used: "file://otherhost/tmp/shot.png"},
		{name: "a URI with no path", used: "file://"},
		{name: "a URI whose path is not absolute", used: "file://localhost"},
		{name: "the root directory", used: "/"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resultPath(test.used, requested)
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("error = %v, want CodeInvalidInput", err)
			}
		})
	}
}

func TestInsideDir_RecognisesOnlyFilesBelowTheDirectory(t *testing.T) {
	dir := "/run/user/1000/neru-core-screenshot-1"

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "a file in the directory", target: dir + "/screenshot-2.png", want: true},
		{name: "a file in a subdirectory", target: dir + "/nested/shot.png", want: true},
		{name: "a cleaned path in the directory", target: dir + "/./shot.png", want: true},
		{name: "the directory itself", target: dir},
		{name: "a sibling with the same prefix", target: dir + "-other/shot.png"},
		{name: "the parent directory", target: "/run/user/1000/shot.png"},
		{name: "an unrelated absolute path", target: "/tmp/shot.png"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := insideDir(dir, test.target); got != test.want {
				t.Errorf("insideDir(%q, %q) = %v, want %v", dir, test.target, got, test.want)
			}
		})
	}
}
