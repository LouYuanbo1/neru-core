package winrt

import (
	"reflect"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestNormalizeTags_TrimsAndDropsBlanks pins the tidy-up every language list
// goes through, so a configuration entry with a stray space still names the
// language the user meant.
func TestNormalizeTags_TrimsAndDropsBlanks(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want []string
	}{
		{name: "nil stays nil", tags: nil, want: nil},
		{name: "empty stays nil", tags: []string{}, want: nil},
		{name: "order is kept", tags: []string{"ja", "en-US"}, want: []string{"ja", "en-US"}},
		{name: "space is trimmed", tags: []string{" en-US ", "ja"}, want: []string{"en-US", "ja"}},
		{name: "blank entries are dropped", tags: []string{"", "en-US", "   "}, want: []string{"en-US"}},
		{name: "only blanks is no preference at all", tags: []string{"", " "}, want: nil},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := normalizeTags(testCase.tags); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("normalizeTags(%q) = %q, want %q", testCase.tags, got, testCase.want)
			}
		})
	}
}

// TestNormalizeTags_Copies pins that a caller may reuse its slice: the
// recognizer must not hold the caller's backing array.
func TestNormalizeTags_Copies(t *testing.T) {
	tags := []string{"en-US"}
	normalized := normalizeTags(tags)

	tags[0] = "ja"

	if got, want := normalized[0], "en-US"; got != want {
		t.Errorf("normalized tag changed to %q with the caller's slice, want %q", got, want)
	}
}

// TestOptionsNormalized_Copies checks the same through the public Options type.
func TestOptionsNormalized_Copies(t *testing.T) {
	tags := []string{"en-US", "ja"}
	normalized := Options{Languages: tags}.normalized()

	tags[1] = "fr"

	if got, want := normalized.Languages, []string{"en-US", "ja"}; !reflect.DeepEqual(got, want) {
		t.Errorf("normalized languages = %q, want %q", got, want)
	}
}

// TestEffectiveLanguages_CallWins pins the precedence a caller relies on when it
// passes per-call languages to a recognizer built with its own.
func TestEffectiveLanguages_CallWins(t *testing.T) {
	tests := []struct {
		name       string
		recognizer []string
		call       []string
		want       []string
	}{
		{name: "call overrides", recognizer: []string{"en-US"}, call: []string{"ja"}, want: []string{"ja"}},
		{name: "recognizer fills in", recognizer: []string{"en-US"}, call: nil, want: []string{"en-US"}},
		{name: "call trims too", recognizer: []string{"en-US"}, call: []string{" ja "}, want: []string{"ja"}},
		{name: "neither means the profile", recognizer: nil, call: nil, want: nil},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := effectiveLanguages(testCase.recognizer, testCase.call)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("effectiveLanguages(%q, %q) = %q, want %q",
					testCase.recognizer, testCase.call, got, testCase.want)
			}
		})
	}
}

// TestLanguageKey_IdentifiesTheEngine pins the cache key: the same order must
// share an engine, and a different order must not.
func TestLanguageKey_IdentifiesTheEngine(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want string
	}{
		{name: "no languages is the profile", tags: nil, want: ""},
		{name: "blank languages are the profile", tags: []string{" "}, want: ""},
		{name: "one tag", tags: []string{"en-US"}, want: "en-US"},
		{name: "order is part of the key", tags: []string{"ja", "en-US"}, want: "ja\x00en-US"},
		{name: "trimming happens first", tags: []string{" ja", "en-US "}, want: "ja\x00en-US"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := languageKey(testCase.tags); got != testCase.want {
				t.Errorf("languageKey(%q) = %q, want %q", testCase.tags, got, testCase.want)
			}
		})
	}

	if languageKey([]string{"en-US", "ja"}) == languageKey([]string{"ja", "en-US"}) {
		t.Error("languageKey gave the same key to two different orders")
	}
}

// TestValidateOptionCount_RefusesTwo pins that New() and New(opts) both read
// naturally while two Options values are reported rather than merged.
func TestValidateOptionCount_RefusesTwo(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		wantErr bool
	}{
		{name: "defaults", count: 0, wantErr: false},
		{name: "one options value", count: 1, wantErr: false},
		{name: "two options values", count: 2, wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateOptionCount(testCase.count)
			if testCase.wantErr != (err != nil) {
				t.Fatalf("validateOptionCount(%d) = %v, wanted an error: %v", testCase.count, err, testCase.wantErr)
			}

			if err != nil && !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("validateOptionCount(%d) code = %s, want %s",
					testCase.count, errs.GetCode(err), errs.CodeInvalidInput)
			}
		})
	}
}
