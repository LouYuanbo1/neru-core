//go:build darwin

package darwin

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

// TestCFNumberSInt32_ReadsBackACoreFoundationNumber covers the number reader
// Mission Control uses for a thumbnail's window id, against a number
// CoreFoundation itself created.
func TestCFNumberSInt32_ReadsBackACoreFoundationNumber(t *testing.T) {
	api, err := loadFrameworks()
	if err != nil {
		t.Skipf("the CoreFoundation frameworks are unavailable: %v", err)
	}

	for _, want := range []int32{0, -42, 4242} {
		value := want

		number := api.cf.numberCreate(0, cfNumberSInt32Type, unsafe.Pointer(&value))
		if number == 0 {
			t.Fatal("CFNumberCreate returned nothing")
		}

		got, ok := api.cfNumberSInt32(number)
		api.cf.release(number)

		if !ok || got != want {
			t.Errorf("cfNumberSInt32 = %d, %v; want %d", got, ok, want)
		}
	}

	if _, ok := api.cfNumberSInt32(api.cfStringRef("not a number")); ok {
		t.Error("a CFString must not read as a number")
	}

	if _, ok := api.cfNumberSInt32(0); ok {
		t.Error("a null reference must not read as a number")
	}
}

func TestCFNumberFloat64_ReadsBackACoreFoundationNumber(t *testing.T) {
	api, err := loadFrameworks()
	if err != nil {
		t.Skipf("the CoreFoundation frameworks are unavailable: %v", err)
	}

	for _, want := range []float64{0, -1.5, 4224.25} {
		value := want

		number := api.cf.numberCreate(0, cfNumberFloat64Type, unsafe.Pointer(&value))
		if number == 0 {
			t.Fatal("CFNumberCreate returned nothing")
		}

		got, ok := api.cfNumberFloat64(number)
		api.cf.release(number)

		if !ok || got != want {
			t.Errorf("cfNumberFloat64 = %v, %v; want %v", got, ok, want)
		}
	}
}

// TestReadInfoPlist_DecodesAnInfoPlistFile exercises the property-list path the
// bundle rules use: CoreFoundation where it is available, and the plutil
// fallback otherwise. Both must produce the same dictionary.
func TestReadInfoPlist_DecodesAnInfoPlistFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), bundleInfoPlistName)

	if err := os.WriteFile(path, []byte(plistDocument), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	info, err := readInfoPlist(path)
	if err != nil {
		t.Fatalf("readInfoPlist: %v", err)
	}

	if got, _ := info["CFBundleIdentifier"].(string); got != "com.example.app" {
		t.Errorf("CFBundleIdentifier = %#v, want com.example.app", info["CFBundleIdentifier"])
	}

	if !plistBool(info, "LSTemplateApplication") {
		t.Error("LSTemplateApplication must read as true")
	}

	if !plistHasWebScheme(info) {
		t.Error("a registered https scheme must be recognised")
	}
}

func TestReadInfoPlist_ReportsAnUnreadableFile(t *testing.T) {
	if _, err := readInfoPlist(filepath.Join(t.TempDir(), "missing.plist")); err == nil {
		t.Error("a missing property list must be an error")
	}
}
