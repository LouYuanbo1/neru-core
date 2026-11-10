//go:build darwin

package darwin

import (
	"context"
	"testing"
)

func TestInSpacesBar_FindsTheBarThroughAncestors(t *testing.T) {
	t.Parallel()

	root := &treeNode{info: &elementInfo{role: "AXApplication"}}
	bar := &treeNode{info: &elementInfo{role: "AXGroup", identifier: spacesBarIdentifier}, parent: root}
	desktop := &treeNode{info: &elementInfo{role: axRoleButton}, parent: bar}
	thumbnail := &treeNode{info: &elementInfo{role: axRoleButton}, parent: root}

	if !inSpacesBar(desktop) {
		t.Error("a button below the Spaces bar group must be recognised as in the bar")
	}

	if inSpacesBar(thumbnail) {
		t.Error("a desktop thumbnail must not be treated as part of the Spaces bar")
	}

	// The bar itself is an ancestor, not a member: its own frame is not moved
	// and it is not dropped with the buttons when the bar collapses.
	if inSpacesBar(bar) {
		t.Error("the Spaces bar group itself is not in the bar")
	}

	if inSpacesBar(&treeNode{}) {
		t.Error("a node without ancestors is not in the bar")
	}
}

func TestSpacesBarFrame_ShiftsTheReportedCenterBackByHalfTheSize(t *testing.T) {
	t.Parallel()

	got := spacesBarFrame(cgPoint{X: 500, Y: 300}, cgSize{Width: 120, Height: 80})
	want := cgPoint{X: 440, Y: 260}

	if got != want {
		t.Fatalf("spacesBarFrame = %+v, want %+v", got, want)
	}
}

func TestSpacesBarFrame_HalvesAnOddSizeExactly(t *testing.T) {
	t.Parallel()

	// Sizes and positions are CoreGraphics points, so the halving is exact
	// rather than truncated. The rounding happens later, when the frame becomes
	// an integer rectangle.
	got := spacesBarFrame(cgPoint{X: 101, Y: 51}, cgSize{Width: 5, Height: 3})
	want := cgPoint{X: 98.5, Y: 49.5}

	if got != want {
		t.Fatalf("spacesBarFrame = %+v, want %+v", got, want)
	}
}

// TestMissionControlClickableNodes_SurvivesAMissingWindowManager calls the walk
// without asserting a count: whether WindowManager and Mission Control are up
// depends on the machine. It pins that the call answers rather than failing
// when there is nothing to read.
func TestMissionControlClickableNodes_SurvivesAMissingWindowManager(t *testing.T) {
	client, err := New(Options{})
	if err != nil {
		t.Skipf("the accessibility frameworks are unavailable: %v", err)
	}
	defer client.Close()

	if _, err := client.MissionControlClickableNodes(context.Background(), 0); err != nil {
		t.Fatalf("MissionControlClickableNodes: %v", err)
	}
}
