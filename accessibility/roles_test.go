package accessibility

import (
	"slices"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

func TestClickableRoles_EmptyOptionsAskForTheDefault(t *testing.T) {
	t.Parallel()

	roles, err := Options{}.clickableRoles()
	if err != nil {
		t.Fatalf("clickableRoles: %v", err)
	}

	if roles != nil {
		t.Errorf("roles = %v, want nil so the backend keeps its default set", roles)
	}
}

func TestClickableRoles_NativeNamesPassThrough(t *testing.T) {
	t.Parallel()

	roles, err := Options{ClickableRoles: []string{"One", "Two"}}.clickableRoles()
	if err != nil {
		t.Fatalf("clickableRoles: %v", err)
	}

	if want := []string{"One", "Two"}; !slices.Equal(roles, want) {
		t.Errorf("roles = %v, want %v", roles, want)
	}
}

func TestClickableRoles_NeutralRolesResolveForThisPlatform(t *testing.T) {
	t.Parallel()

	vocabulary, ok := element.CurrentVocabulary()
	if !ok {
		t.Skip("this platform has no accessibility role vocabulary")
	}

	neutral := []element.Role{element.RoleButton, element.RoleLink}

	want := element.ResolveRoles(neutral, vocabulary).Native
	if len(want) == 0 {
		t.Fatalf("the test's neutral roles resolve to nothing on %s", vocabulary)
	}

	roles, err := Options{Roles: neutral}.clickableRoles()
	if err != nil {
		t.Fatalf("clickableRoles: %v", err)
	}

	if !slices.Equal(roles, want) {
		t.Errorf("roles = %v, want %v", roles, want)
	}
}

func TestClickableRoles_AppendsNativeNamesAfterNeutralOnes(t *testing.T) {
	t.Parallel()

	vocabulary, ok := element.CurrentVocabulary()
	if !ok {
		t.Skip("this platform has no accessibility role vocabulary")
	}

	neutral := element.ResolveRoles([]element.Role{element.RoleButton}, vocabulary).Native

	roles, err := Options{
		Roles:          []element.Role{element.RoleButton},
		ClickableRoles: []string{"Custom"},
	}.clickableRoles()
	if err != nil {
		t.Fatalf("clickableRoles: %v", err)
	}

	if want := append(slices.Clone(neutral), "Custom"); !slices.Equal(roles, want) {
		t.Errorf("roles = %v, want %v", roles, want)
	}
}

func TestClickableRoles_RejectsAnUnknownRole(t *testing.T) {
	t.Parallel()

	_, err := Options{Roles: []element.Role{"not-a-role"}}.clickableRoles()
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("error = %v, want CodeInvalidInput", err)
	}
}
