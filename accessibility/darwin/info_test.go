//go:build darwin

package darwin

import (
	"testing"
)

func TestSecureValue_BlanksAPasswordField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subrole string
		value   string
		want    string
	}{
		{name: "secure text field", subrole: subroleSecureTextField, value: "hunter2", want: ""},
		{name: "search field", subrole: "AXSearchField", value: "query", want: "query"},
		{name: "no subrole", subrole: "", value: "text", want: "text"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := secureValue(testCase.subrole, testCase.value); got != testCase.want {
				t.Fatalf("secureValue(%q, %q) = %q, want %q",
					testCase.subrole, testCase.value, got, testCase.want)
			}
		})
	}
}

// TestSecureValue_KeepsTheFieldClickable pins the other half of the rule: the
// element still answers the role filter, so a hint can be placed on a password
// field even though its value never leaves the accessibility server.
func TestSecureValue_KeepsTheFieldClickable(t *testing.T) {
	t.Parallel()

	info := &elementInfo{role: "AXTextField", subrole: subroleSecureTextField, value: "hunter2"}
	info.value = secureValue(info.subrole, info.value)

	if info.value != "" {
		t.Fatalf("value = %q, want blank", info.value)
	}

	if !info.matchesRole(map[string]struct{}{"AXTextField": {}}) {
		t.Error("a secure text field must still match its role")
	}

	if info.searchTextCandidate() {
		t.Error("a blanked password field must not contribute search text")
	}
}
