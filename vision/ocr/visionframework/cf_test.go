//go:build darwin

package visionframework

import "testing"

func TestResolveLanguage_MatchesVisionSpelling(t *testing.T) {
	supported := map[string]string{
		"en-us":   "en-US",
		"en-gb":   "en-GB",
		"fr-fr":   "fr-FR",
		"zh-hans": "zh-Hans",
	}

	cases := []struct {
		name string
		tag  string
		want string
		ok   bool
	}{
		{name: "an exact tag keeps Vision's spelling", tag: "fr-FR", want: "fr-FR", ok: true},
		{name: "case does not matter", tag: "FR-fr", want: "fr-FR", ok: true},
		{name: "a base tag picks the first regional tag alphabetically", tag: "en", want: "en-GB", ok: true},
		{name: "a base tag with different case still matches", tag: "EN", want: "en-GB", ok: true},
		{name: "a script subtag matches exactly", tag: "zh-Hans", want: "zh-Hans", ok: true},
		{name: "an unknown language is refused", tag: "xx-YY", ok: false},
		{name: "a tag is not matched by prefix alone", tag: "e", ok: false},
		{name: "a region Vision does not list is refused", tag: "en-AU", ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := resolveLanguage(testCase.tag, supported)
			if ok != testCase.ok || got != testCase.want {
				t.Fatalf("resolveLanguage(%q) = %q, %v; want %q, %v", testCase.tag, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestResolveLanguage_EmptySupportListRefusesEverything(t *testing.T) {
	if spelled, ok := resolveLanguage("en-US", nil); ok || spelled != "" {
		t.Fatalf("resolveLanguage against no support list = %q, %v; want refusal", spelled, ok)
	}
}
