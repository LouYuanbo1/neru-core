//go:build linux

package portal

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestFilePath_TurnsThePortalURIIntoAPath(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "plain path",
			uri:  "file:///run/user/1000/doc/1a2b3c/shot.png",
			want: "/run/user/1000/doc/1a2b3c/shot.png",
		},
		{
			name: "a space the portal percent-encoded",
			uri:  "file:///tmp/Screenshot%20at%2010.00.png",
			want: "/tmp/Screenshot at 10.00.png",
		},
		{
			name: "a non-ASCII name the portal percent-encoded",
			uri:  "file:///tmp/scr%C3%A9enshot.png",
			want: "/tmp/scréenshot.png",
		},
		{
			name: "the localhost authority means this machine",
			uri:  "file://localhost/tmp/shot.png",
			want: "/tmp/shot.png",
		},
		{
			name: "a query or fragment names the same file",
			uri:  "file:///tmp/shot.png?v=1#top",
			want: "/tmp/shot.png",
		},
		{
			name: "dot segments are resolved",
			uri:  "file:///tmp/../tmp/shot.png",
			want: "/tmp/shot.png",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := FilePath(test.uri)
			if err != nil {
				t.Fatalf("FilePath(%q): %v", test.uri, err)
			}

			if got != test.want {
				t.Errorf("FilePath(%q) = %q, want %q", test.uri, got, test.want)
			}
		})
	}
}

func TestFilePath_RefusesAURIThatNamesNoLocalFile(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{name: "another scheme", uri: "https://example.com/shot.png"},
		{name: "another host", uri: "file://elsewhere/tmp/shot.png"},
		{name: "empty", uri: ""},
		{name: "the root", uri: "file:///"},
		{name: "a broken escape", uri: "file:///tmp/%zz.png"},
		{name: "a NUL byte", uri: "file:///tmp/a%00b.png"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FilePath(test.uri)
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("FilePath(%q) error = %v, want CodeInvalidInput", test.uri, err)
			}
		})
	}
}
