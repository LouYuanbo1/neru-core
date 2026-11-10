//go:build windows

package winrt

import (
	"strings"
	"testing"
)

// TestPackageGUIDs_AreDistinct catches a copy-paste that pointed two interfaces
// at the same ID, which would make one of them unreachable.
func TestPackageGUIDs_AreDistinct(t *testing.T) {
	ids := map[string]guid{
		"OcrEngineStatics":      iidOcrEngineStatics,
		"LanguageFactory":       iidLanguageFactory,
		"SoftwareBitmapFactory": iidSoftwareBitmapFactory,
		"BufferFactory":         iidBufferFactory,
		"BufferByteAccess":      iidBufferByteAccess,
		"AsyncInfo":             iidAsyncInfo,
	}

	seen := make(map[guid]string, len(ids))

	for name, id := range ids {
		if first, ok := seen[id]; ok {
			t.Errorf("%s and %s share an interface ID", name, first)
		}

		seen[id] = name
	}

	if len(seen) != len(ids) {
		t.Errorf("got %d distinct IDs for %d interfaces", len(seen), len(ids))
	}
}

// TestHString_RoundTrips pins the lifetime helpers the engine's text reads go
// through: the handle must survive the call, and String must copy before the
// handle dies. This touches combase but no engine, so it needs no language pack.
func TestHString_RoundTrips(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "ascii", text: "hello"},
		{name: "empty", text: ""},
		{name: "a word off a screen", text: "HELLO world"},
		{name: "non-latin", text: "héllo 世界"},
		{name: "long", text: strings.Repeat("neru", 512)},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handle, err := newHString(testCase.text)
			if err != nil {
				t.Fatalf("newHString(%q): %v", testCase.text, err)
			}
			defer handle.delete()

			if got := handle.String(); got != testCase.text {
				t.Errorf("String() = %q, want %q", got, testCase.text)
			}
		})
	}
}

// TestHString_RejectsAnEmbeddedNUL pins that an impossible string is refused
// before it becomes an HSTRING that silently ends at the NUL.
func TestHString_RejectsAnEmbeddedNUL(t *testing.T) {
	handle, err := newHString("a\x00b")
	if err == nil {
		handle.delete()

		t.Fatal("newHString accepted a string with an embedded NUL")
	}
}

// TestHString_ZeroValueIsTheEmptyString pins the zero handle, which WinRT reads
// as an empty string and which every delete path must tolerate.
func TestHString_ZeroValueIsTheEmptyString(t *testing.T) {
	var handle hstring

	if got := handle.String(); got != "" {
		t.Errorf("zero HSTRING String() = %q, want an empty string", got)
	}

	handle.delete()
}

// TestInspectableText_RefusesANilObject pins that a failed property read is an
// empty run rather than a crash: the caller drops it.
func TestInspectableText_RefusesANilObject(t *testing.T) {
	if got := inspectableText(nil, ocrWordText); got != "" {
		t.Errorf("inspectableText(nil) = %q, want an empty string", got)
	}
}
