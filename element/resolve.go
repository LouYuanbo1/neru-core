package element

import (
	"fmt"
	"runtime"
	"strings"
)

// RoleDiagnosticKind classifies a problem found while resolving configured
// roles into native role names.
type RoleDiagnosticKind int

const (
	// RoleDiagnosticUnknownName marks a bare entry that is not a neutral role.
	// It is fatal. The neutral vocabulary is closed, so this is a typo or a
	// native role name written without its vocabulary prefix.
	RoleDiagnosticUnknownName RoleDiagnosticKind = iota
	// RoleDiagnosticUnknownVocabulary marks an entry whose prefix is not one of
	// the known native vocabularies. It is fatal.
	RoleDiagnosticUnknownVocabulary
	// RoleDiagnosticForeignVocabulary marks a well-formed native entry that
	// belongs to a different platform. It is informational, because a
	// cross-platform configuration carries entries for every machine.
	RoleDiagnosticForeignVocabulary
	// RoleDiagnosticNoNativeEquivalent marks a valid neutral role that has no
	// counterpart on the running platform. It is informational.
	RoleDiagnosticNoNativeEquivalent
)

// RoleDiagnostic describes one configuration entry that could not be resolved
// into a usable native role, together with a human-readable explanation.
type RoleDiagnostic struct {
	// Entry is the configuration entry verbatim.
	Entry string
	// Kind classifies the problem.
	Kind RoleDiagnosticKind
	// Suggestion is a replacement entry, when one can be derived.
	Suggestion string
	// GOOS is the platform the entry was resolved against.
	GOOS string
}

// IsFatal reports whether the diagnostic should reject the configuration
// rather than only be reported. Only malformed or unrecognized entries are
// fatal; entries that do not apply to this platform are not.
func (d RoleDiagnostic) IsFatal() bool {
	return d.Kind == RoleDiagnosticUnknownName || d.Kind == RoleDiagnosticUnknownVocabulary
}

// Message returns a human-readable explanation of the diagnostic.
func (d RoleDiagnostic) Message() string {
	switch d.Kind {
	case RoleDiagnosticUnknownName:
		if d.Suggestion != "" {
			return fmt.Sprintf("unknown role %q: use %q", d.Entry, d.Suggestion)
		}

		return fmt.Sprintf("unknown role %q", d.Entry)
	case RoleDiagnosticUnknownVocabulary:
		return fmt.Sprintf(
			"unknown role vocabulary in %q: expected a neutral role or one of %q, %q, %q",
			d.Entry, VocabularyAX, VocabularyATSPI, VocabularyUIA,
		)
	case RoleDiagnosticForeignVocabulary:
		return fmt.Sprintf("%q does not apply on %s and is ignored", d.Entry, d.GOOS)
	case RoleDiagnosticNoNativeEquivalent:
		return fmt.Sprintf("%q has no equivalent on %s and is ignored", d.Entry, d.GOOS)
	default:
		return fmt.Sprintf("invalid role %q", d.Entry)
	}
}

// ResolvedRoleEntry records how a single configuration entry resolved.
type ResolvedRoleEntry struct {
	// Entry is the configuration entry verbatim.
	Entry string
	// Role is set when the entry named a neutral role.
	Role Role
	// Vocabulary is set when the entry carried a native vocabulary prefix.
	Vocabulary NativeVocabulary
	// Native lists the native role names the entry contributes on this
	// platform. It is empty for entries that do not apply here.
	Native []string
	// Diagnostic is set when the entry could not be fully resolved.
	Diagnostic *RoleDiagnostic
}

// RoleResolution is the outcome of resolving a configured role list.
type RoleResolution struct {
	// GOOS is the platform the entries were resolved against.
	GOOS string
	// Native is the deduplicated union of native role names, in entry order.
	Native []string
	// Entries records the per-entry outcome, in configuration order.
	Entries []ResolvedRoleEntry
	// Diagnostics lists every problem found, in configuration order.
	Diagnostics []RoleDiagnostic
}

// HasFatal reports whether any diagnostic should reject the configuration.
func (r RoleResolution) HasFatal() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.IsFatal() {
			return true
		}
	}

	return false
}

// FatalMessages returns the messages of every fatal diagnostic.
func (r RoleResolution) FatalMessages() []string {
	var messages []string

	for _, diagnostic := range r.Diagnostics {
		if diagnostic.IsFatal() {
			messages = append(messages, diagnostic.Message())
		}
	}

	return messages
}

// IgnoredMessages returns the messages of every non-fatal diagnostic, i.e. the
// entries that are valid but do not apply on this platform.
func (r RoleResolution) IgnoredMessages() []string {
	var messages []string

	for _, diagnostic := range r.Diagnostics {
		if !diagnostic.IsFatal() {
			messages = append(messages, diagnostic.Message())
		}
	}

	return messages
}

// ResolveRoles expands neutral roles into the native role names a backend
// speaking vocab filters on. It is the direct path for a caller that already
// holds element.Role values rather than configuration strings.
func ResolveRoles(roles []Role, vocab NativeVocabulary) RoleResolution {
	resolution := RoleResolution{}
	seen := make(map[string]struct{}, len(roles))

	for _, role := range roles {
		mapping, ok := LookupRole(role)
		if !ok {
			continue
		}

		for _, native := range mapping.Native(vocab) {
			if _, duplicate := seen[native]; duplicate {
				continue
			}

			seen[native] = struct{}{}

			resolution.Native = append(resolution.Native, native)
		}
	}

	return resolution
}

// ResolveRoleEntries expands configured role entries into the native role names
// used by the accessibility backend of the given platform (a Go GOOS value).
//
// An entry is either a neutral role from the closed vocabulary ("button"), or a
// native role name addressed through its vocabulary prefix ("atspi:page tab
// list"). Prefixed entries for other platforms resolve to nothing and are
// reported, not rejected, so one configuration can serve several machines.
// Empty and whitespace-only entries are skipped silently.
func ResolveRoleEntries(entries []string, goos string) RoleResolution {
	current, hasVocabulary := VocabularyForGOOS(goos)

	resolution := RoleResolution{GOOS: goos}
	seen := make(map[string]struct{}, len(entries))

	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}

		resolved := resolveRoleEntry(entry, current, hasVocabulary, goos)

		for _, native := range resolved.Native {
			if _, duplicate := seen[native]; duplicate {
				continue
			}

			seen[native] = struct{}{}

			resolution.Native = append(resolution.Native, native)
		}

		if resolved.Diagnostic != nil {
			resolution.Diagnostics = append(resolution.Diagnostics, *resolved.Diagnostic)
		}

		resolution.Entries = append(resolution.Entries, resolved)
	}

	return resolution
}

// ResolveRoleEntriesForCurrentPlatform resolves entries against the running
// platform.
func ResolveRoleEntriesForCurrentPlatform(entries []string) RoleResolution {
	return ResolveRoleEntries(entries, runtime.GOOS)
}

// resolveRoleEntry resolves a single non-empty configuration entry.
func resolveRoleEntry(
	entry string,
	current NativeVocabulary,
	hasVocabulary bool,
	goos string,
) ResolvedRoleEntry {
	if vocab, native, prefixed := SplitVocabularyEntry(entry); prefixed {
		return resolveNativeEntry(entry, vocab, native, current, hasVocabulary, goos)
	}

	return resolveNeutralEntry(entry, current, hasVocabulary, goos)
}

// resolveNativeEntry resolves an entry that carried a vocabulary prefix.
func resolveNativeEntry(
	entry string,
	vocab NativeVocabulary,
	native string,
	current NativeVocabulary,
	hasVocabulary bool,
	goos string,
) ResolvedRoleEntry {
	resolved := ResolvedRoleEntry{Entry: entry, Vocabulary: vocab}

	if !KnownVocabulary(vocab) || native == "" {
		resolved.Diagnostic = &RoleDiagnostic{
			Entry: entry,
			Kind:  RoleDiagnosticUnknownVocabulary,
			GOOS:  goos,
		}

		return resolved
	}

	if !hasVocabulary || vocab != current {
		resolved.Diagnostic = &RoleDiagnostic{
			Entry: entry,
			Kind:  RoleDiagnosticForeignVocabulary,
			GOOS:  goos,
		}

		return resolved
	}

	resolved.Native = []string{native}

	return resolved
}

// resolveNeutralEntry resolves a bare entry, which must name a neutral role.
func resolveNeutralEntry(
	entry string,
	current NativeVocabulary,
	hasVocabulary bool,
	goos string,
) ResolvedRoleEntry {
	role := Role(entry)

	mapping, ok := LookupRole(role)
	if !ok {
		return ResolvedRoleEntry{
			Entry: entry,
			Diagnostic: &RoleDiagnostic{
				Entry:      entry,
				Kind:       RoleDiagnosticUnknownName,
				Suggestion: suggestionForBareEntry(entry),
				GOOS:       goos,
			},
		}
	}

	resolved := ResolvedRoleEntry{Entry: entry, Role: role}

	if hasVocabulary {
		resolved.Native = mapping.Native(current)
	}

	if len(resolved.Native) == 0 {
		resolved.Diagnostic = &RoleDiagnostic{
			Entry: entry,
			Kind:  RoleDiagnosticNoNativeEquivalent,
			GOOS:  goos,
		}
	}

	return resolved
}

// suggestionForBareEntry derives a replacement for a bare entry that is not a
// neutral role. A native role name written without its prefix, as older
// configurations write every role, resolves to the neutral role that covers
// it, which is the migration a caller needs.
func suggestionForBareEntry(entry string) string {
	for _, vocab := range []NativeVocabulary{VocabularyAX, VocabularyATSPI, VocabularyUIA} {
		if role, ok := (nativeRoleMappers[vocab]).MapRole(entry); ok {
			return string(role)
		}
	}

	return ""
}
