//go:build darwin

package darwin

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/action"
)

func TestEventFlags_MapsEveryModifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		modifiers action.Modifiers
		want      uint64
	}{
		{name: "none", modifiers: 0, want: 0},
		{name: "command", modifiers: action.ModCmd, want: cgEventFlagCommand},
		{name: "shift", modifiers: action.ModShift, want: cgEventFlagShift},
		{name: "alt", modifiers: action.ModAlt, want: cgEventFlagAlternate},
		{name: "ctrl", modifiers: action.ModCtrl, want: cgEventFlagControl},
		{
			name:      "cmd+shift",
			modifiers: action.ModCmd | action.ModShift,
			want:      cgEventFlagCommand | cgEventFlagShift,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := eventFlags(testCase.modifiers); got != testCase.want {
				t.Errorf("eventFlags(%v) = %#x, want %#x", testCase.modifiers, got, testCase.want)
			}
		})
	}
}

func TestButtonEventKind_CoversEveryButtonAndPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		button   action.MouseButton
		up       bool
		wantKind uint32
		wantCG   uint32
	}{
		{button: action.ButtonLeft, up: false, wantKind: cgEventLeftMouseDown, wantCG: cgMouseButtonLeft},
		{button: action.ButtonLeft, up: true, wantKind: cgEventLeftMouseUp, wantCG: cgMouseButtonLeft},
		{button: action.ButtonRight, up: false, wantKind: cgEventRightMouseDown, wantCG: cgMouseButtonRight},
		{button: action.ButtonRight, up: true, wantKind: cgEventRightMouseUp, wantCG: cgMouseButtonRight},
		{button: action.ButtonMiddle, up: false, wantKind: cgEventOtherMouseDown, wantCG: cgMouseButtonCenter},
		{button: action.ButtonMiddle, up: true, wantKind: cgEventOtherMouseUp, wantCG: cgMouseButtonCenter},
	}

	for _, testCase := range tests {
		kind, cgButton, ok := buttonEventKind(testCase.button, testCase.up)
		if !ok || kind != testCase.wantKind || cgButton != testCase.wantCG {
			t.Errorf("buttonEventKind(%v, %v) = %d, %d, %v; want %d, %d",
				testCase.button, testCase.up, kind, cgButton, ok, testCase.wantKind, testCase.wantCG)
		}
	}

	if _, _, ok := buttonEventKind(action.MouseButton(99), false); ok {
		t.Error("an unknown button must not map to an event")
	}
}

func TestElementInfo_BoundsRoundsFractionalPoints(t *testing.T) {
	t.Parallel()

	info := &elementInfo{
		position: cgPoint{X: 10.4, Y: 20.6},
		size:     cgSize{Width: 100.5, Height: 40.2},
	}

	want := image.Rect(10, 21, 111, 61)
	if got := info.bounds(); got != want {
		t.Errorf("bounds() = %v, want %v", got, want)
	}

	var nilInfo *elementInfo
	if got := nilInfo.bounds(); !got.Empty() {
		t.Errorf("nil bounds() = %v, want empty", got)
	}
}

func TestElementInfo_MatchesRoleChecksTheSubroleToo(t *testing.T) {
	t.Parallel()

	info := &elementInfo{role: "AXCheckBox", subrole: "AXSwitch"}
	roles := map[string]struct{}{"AXSwitch": {}}

	if !info.matchesRole(roles) {
		t.Error("a configured subrole must match the element")
	}

	if info.matchesRole(map[string]struct{}{"AXButton": {}}) {
		t.Error("an unrelated role must not match")
	}
}

func TestRoleName_TranslatesNativeRoles(t *testing.T) {
	t.Parallel()

	if got := roleName("AXButton"); got != "button" {
		t.Errorf("roleName(AXButton) = %q, want button", got)
	}

	if got := roleName("AXNotAThing"); got != "unknown" {
		t.Errorf("roleName(unknown) = %q, want unknown", got)
	}
}

func TestGoCString_StopsAtNUL(t *testing.T) {
	t.Parallel()

	buffer := []byte("hello\x00ignored")

	if got := goCString(&buffer[0]); got != "hello" {
		t.Errorf("goCString = %q, want hello", got)
	}

	if got := goCString(nil); got != "" {
		t.Errorf("goCString(nil) = %q, want empty", got)
	}
}
