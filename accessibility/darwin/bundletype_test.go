//go:build darwin

package darwin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// plistDocument is a property list document covering every type the rules read.
const plistDocument = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.example.app</string>
	<key>LSTemplateApplication</key>
	<true/>
	<key>CrAppModeShortcutID</key>
	<string>abc</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>x-webkit-app-launch</string>
				<string>HTTPS</string>
			</array>
		</dict>
	</array>
	<key>TagCount</key>
	<integer>7</integer>
	<key>PreviewRatio</key>
	<real>0.5</real>
	<key>Disabled</key>
	<false/>
</dict>
</plist>
`

func TestParseXMLPlist_ReadsEveryValueType(t *testing.T) {
	t.Parallel()

	info, err := parseXMLPlist([]byte(plistDocument))
	if err != nil {
		t.Fatalf("parseXMLPlist: %v", err)
	}

	if got, _ := info["CFBundleIdentifier"].(string); got != "com.example.app" {
		t.Errorf("CFBundleIdentifier = %#v, want com.example.app", info["CFBundleIdentifier"])
	}

	if !plistBool(info, "LSTemplateApplication") {
		t.Error("LSTemplateApplication must read as true")
	}

	if plistBool(info, "Disabled") {
		t.Error("Disabled must read as false")
	}

	if got, _ := info["TagCount"].(float64); got != 7 {
		t.Errorf("TagCount = %#v, want 7", info["TagCount"])
	}

	if got, _ := info["PreviewRatio"].(float64); got != 0.5 {
		t.Errorf("PreviewRatio = %#v, want 0.5", info["PreviewRatio"])
	}

	if !plistHasWebScheme(info) {
		t.Error("a registered https scheme must be recognised")
	}

	if plistBool(nil, "CFBundleIdentifier") {
		t.Error("a missing key must not read as true")
	}
}

func TestParseXMLPlist_RejectsADocumentWithoutADictionaryRoot(t *testing.T) {
	t.Parallel()

	if _, err := parseXMLPlist([]byte(`<plist version="1.0"><array/></plist>`)); err == nil {
		t.Error("an array root must be rejected")
	}

	if _, err := parseXMLPlist([]byte("not a property list")); err == nil {
		t.Error("a document without a root element must be rejected")
	}
}

func TestDetectBundleTypeAt_FileRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		dirs  []string
		files []string
		info  map[string]any
		want  string
	}{
		{
			name: "electron framework",
			dirs: []string{filepath.Join("Contents", "Frameworks", "Electron Framework.framework")},
			want: BundleTypeElectron,
		},
		{
			name: "electron wins over a chromium loader",
			dirs: []string{
				filepath.Join("Contents", "Frameworks", "Electron Framework.framework"),
				filepath.Join("Contents", "Frameworks", "Some.framework", "Helpers"),
			},
			files: []string{filepath.Join("Contents", "Frameworks", "Some.framework", "Helpers", "app_mode_loader")},
			want:  BundleTypeElectron,
		},
		{
			name:  "chromium app mode loader",
			dirs:  []string{filepath.Join("Contents", "Frameworks", "Chrome Framework.framework", "Helpers")},
			files: []string{filepath.Join("Contents", "Frameworks", "Chrome Framework.framework", "Helpers", "app_mode_loader")},
			want:  BundleTypeChromium,
		},
		{
			name: "chromium loader inside a nested support bundle",
			dirs: []string{filepath.Join("Contents", "Support", "Atlas.app", "Contents", "Frameworks", "Atlas.framework", "Helpers")},
			files: []string{filepath.Join(
				"Contents", "Support", "Atlas.app", "Contents", "Frameworks", "Atlas.framework", "Helpers", "app_mode_loader",
			)},
			want: BundleTypeChromium,
		},
		{
			name: "chromium shortcut key",
			info: map[string]any{"CrAppModeShortcutID": "shortcut"},
			want: BundleTypeChromium,
		},
		{
			name: "firefox engine and browser component",
			dirs: []string{filepath.Join("Contents", "Resources", "browser")},
			files: []string{
				filepath.Join("Contents", "MacOS", "XUL"),
			},
			want: BundleTypeFirefox,
		},
		{
			name:  "xul without a browser component",
			files: []string{filepath.Join("Contents", "MacOS", "XUL")},
			want:  "",
		},
		{
			name: "safari web app template",
			info: map[string]any{"LSTemplateApplication": true},
			want: BundleTypeWebKit,
		},
		{
			name: "non browser url scheme",
			info: map[string]any{
				"CFBundleURLTypes": []any{
					map[string]any{"CFBundleURLSchemes": []any{"x-webkit-app-launch"}},
				},
			},
			want: "",
		},
		{
			name: "https url scheme",
			info: map[string]any{
				"CFBundleURLTypes": []any{
					map[string]any{"CFBundleURLSchemes": []any{"HTTPS"}},
				},
			},
			want: BundleTypeWebKit,
		},
		{
			name: "nothing identifies it",
			want: "",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			bundle := t.TempDir()

			for _, dir := range testCase.dirs {
				if err := os.MkdirAll(filepath.Join(bundle, dir), 0o755); err != nil {
					t.Fatalf("create %s: %v", dir, err)
				}
			}

			for _, file := range testCase.files {
				path := filepath.Join(bundle, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create the parent of %s: %v", file, err)
				}

				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatalf("create %s: %v", file, err)
				}
			}

			if got := detectBundleTypeAt(bundle, testCase.info); got != testCase.want {
				t.Errorf("detectBundleTypeAt = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDetectBundleTypeAt_ReadsAnInfoPlistFromDisk(t *testing.T) {
	t.Parallel()

	bundle := t.TempDir()

	contents := filepath.Join(bundle, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatalf("create Contents: %v", err)
	}

	plistPath := filepath.Join(contents, bundleInfoPlistName)
	if err := os.WriteFile(plistPath, []byte(plistDocument), 0o644); err != nil {
		t.Fatalf("write Info.plist: %v", err)
	}

	// The HTTP scheme in the document is registered as an uppercase "HTTPS",
	// which the rule lowercases before comparing.
	if got := detectBundleTypeAt(bundle, map[string]any{}); got != "" {
		t.Errorf("detectBundleTypeAt without the plist = %q, want empty", got)
	}

	parsed, err := parseXMLPlist([]byte(plistDocument))
	if err != nil {
		t.Fatalf("parseXMLPlist: %v", err)
	}

	if got := detectBundleTypeAt(bundle, parsed); got != BundleTypeChromium {
		t.Errorf("detectBundleTypeAt with the plist = %q, want %q", got, BundleTypeChromium)
	}
}

func TestDetectBundleType_RejectsAnEmptyBundleID(t *testing.T) {
	t.Parallel()

	if _, err := DetectBundleType("  "); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Fatalf("DetectBundleType(\"\") error = %v, want CodeInvalidInput", err)
	}
}

func TestNew_RejectsAnUnknownBundleTypeOverride(t *testing.T) {
	t.Parallel()

	// The check runs before anything is bound, so this needs no accessibility
	// server.
	if _, err := New(Options{BundleType: "chrome"}); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Fatalf("New error = %v, want CodeInvalidInput", err)
	}
}
