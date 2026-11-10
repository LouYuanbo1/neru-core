package winrt

import (
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Options configure a Recognizer.
//
// The zero value is the useful one: it asks Windows for an engine built from the
// account's own language profile, which is the language the user reads the
// screen in.
type Options struct {
	// Languages is the preferred language order, as BCP 47 tags such as
	// "en-US" or "ja". The first tag the machine has an OCR language pack for
	// becomes the engine's language. Empty means the account's language
	// profile: OcrEngine::TryCreateFromUserProfileLanguages.
	//
	// The order is a preference, not a filter: a machine with only the second
	// tag installed recognizes in that one rather than refusing.
	Languages []string
}

// normalized returns o with the language list trimmed, stripped of empty
// entries, and copied, so a caller may reuse or mutate its slice afterwards.
func (o Options) normalized() Options {
	return Options{Languages: normalizeTags(o.Languages)}
}

// normalizeTags copies tags, trimming surrounding space and dropping the entries
// that name nothing. A tag is BCP 47 text, so it never contains an interior
// space worth keeping.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(tags))

	for _, tag := range tags {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}

	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// effectiveLanguages is the language order one call asks for: the call's own if
// it names any, and the recognizer's otherwise. Both being empty means the user
// profile, which languageKey spells as an empty string.
func effectiveLanguages(recognizer, call []string) []string {
	if len(call) > 0 {
		return normalizeTags(call)
	}

	return normalizeTags(recognizer)
}

// languageKey reduces a language order to a comparable key for the engine cache.
// An empty list and a list of nothing but blank tags both reduce to "", which is
// the user profile, so they share the engine that answers for it.
//
// The separator is a NUL, which no BCP 47 tag may contain, so two orders cannot
// run together into the same key.
func languageKey(tags []string) string {
	return strings.Join(normalizeTags(tags), "\x00")
}

// validateOptionCount refuses a New call that was handed more than one Options
// value. New is variadic so that both New() and New(Options{...}) read
// naturally; two values is a mistake, not a merge.
func validateOptionCount(count int) error {
	if count > 1 {
		return errs.New(errs.CodeInvalidInput, "winrt.New accepts at most one Options value")
	}

	return nil
}
