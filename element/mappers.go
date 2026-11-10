package element

// roleTable is a lookup from a backend's native role names to neutral roles.
// It is built once per vocabulary from the platform's own name list, not from
// RoleVocabulary, because the reverse direction needs one deliberate choice
// where several neutral roles share a native name (UIA "Edit" is a
// text_field before it is a text_area).
type roleTable map[string]Role

func (t roleTable) MapRole(native string) (Role, bool) {
	role, ok := t[native]

	return role, ok
}

// nativeRoleMappers maps each vocabulary to its mapper.
var nativeRoleMappers = map[NativeVocabulary]RoleMapper{
	VocabularyAX:    AXRoleMapper{},
	VocabularyATSPI: ATSPIRoleMapper{},
	VocabularyUIA:   UIARoleMapper{},
}

// MapperFor returns the mapper that translates native role names of the given
// vocabulary into neutral roles. The second result is false for a vocabulary
// this package does not know.
func MapperFor(vocab NativeVocabulary) (RoleMapper, bool) {
	mapper, ok := nativeRoleMappers[vocab]

	return mapper, ok
}

// MapperForGOOS returns the mapper for the running platform, and false when
// the platform has no accessibility backend.
func MapperForGOOS(goos string) (RoleMapper, bool) {
	vocab, ok := VocabularyForGOOS(goos)
	if !ok {
		return nil, false
	}

	return MapperFor(vocab)
}

// subroleMapper is a mapper whose platform reports a role and a separate
// subrole (macOS does). A backend that has both passes them together so the
// more specific name wins.
type subroleMapper interface {
	Map(role, subrole string) (Role, bool)
}

// MapNativeRole translates a backend's role and, when the platform has one,
// subrole into a neutral role. A mapper that does not know the pair reports
// false, so a caller can keep the native name instead of guessing.
func MapNativeRole(mapper RoleMapper, role, subrole string) (Role, bool) {
	if mapper == nil {
		return RoleUnknown, false
	}

	if withSubrole, ok := mapper.(subroleMapper); ok && subrole != "" {
		if mapped, found := withSubrole.Map(role, subrole); found {
			return mapped, true
		}
	}

	return mapper.MapRole(role)
}
