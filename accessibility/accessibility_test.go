package accessibility_test

import (
	"context"
	"image"
	"runtime"
	"testing"

	"github.com/LouYuanbo1/neru-core/accessibility"
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/element"
	"github.com/LouYuanbo1/neru-core/errs"
)

// fakeNode is a hand-built ax.Node, so node conversion can be tested without
// any platform accessibility server.
type fakeNode struct {
	role       string
	subrole    string
	bounds     image.Rectangle
	title      string
	desc       string
	value      string
	identifier string
	search     string
	focused    bool
	clickable  bool
}

func (f *fakeNode) ID() string              { return "fake-node" }
func (f *fakeNode) Bounds() image.Rectangle { return f.bounds }
func (f *fakeNode) Role() string            { return f.role }
func (f *fakeNode) Subrole() string         { return f.subrole }
func (f *fakeNode) Title() string           { return f.title }
func (f *fakeNode) Description() string     { return f.desc }
func (f *fakeNode) Value() string           { return f.value }
func (f *fakeNode) IsFocused() bool         { return f.focused }
func (f *fakeNode) Identifier() string      { return f.identifier }
func (f *fakeNode) IsClickable() bool       { return f.clickable }
func (f *fakeNode) Release()                {}
func (f *fakeNode) SearchText() string      { return f.search }

func TestFromNode_TranslatesThePlatformRole(t *testing.T) {
	t.Parallel()

	vocab, ok := element.VocabularyForGOOS(runtime.GOOS)
	if !ok {
		t.Skip("no accessibility vocabulary on this platform")
	}

	native, ok := element.NativeRoleFor(element.RoleButton, vocab)
	if !ok {
		t.Fatalf("no native button role for %q", vocab)
	}

	node := &fakeNode{
		role:       native,
		bounds:     image.Rect(10, 20, 110, 60),
		title:      "Save",
		desc:       "Saves the document",
		value:      "",
		identifier: "save-button",
		search:     "Save document",
		focused:    true,
		clickable:  true,
	}

	converted, err := accessibility.FromNode(node)
	if err != nil {
		t.Fatalf("FromNode: %v", err)
	}

	if converted.Role() != element.RoleButton {
		t.Errorf("Role() = %q, want %q", converted.Role(), element.RoleButton)
	}

	if converted.Bounds() != node.bounds {
		t.Errorf("Bounds() = %v, want %v", converted.Bounds(), node.bounds)
	}

	if converted.Text() != "Save" {
		t.Errorf("Text() = %q, want the title", converted.Text())
	}

	if converted.SearchText() != "Save document" {
		t.Errorf("SearchText() = %q", converted.SearchText())
	}

	if !converted.IsFocused() || !converted.IsClickable() {
		t.Error("focused and clickable state did not survive the conversion")
	}

	if converted.Identifier() != "save-button" {
		t.Errorf("Identifier() = %q", converted.Identifier())
	}

	if converted.IsVisionOnly() {
		t.Error("an accessibility node must not be vision-only")
	}
}

func TestFromNodes_SkipsNodesWithoutArea(t *testing.T) {
	t.Parallel()

	vocab, ok := element.VocabularyForGOOS(runtime.GOOS)
	if !ok {
		t.Skip("no accessibility vocabulary on this platform")
	}

	native, _ := element.NativeRoleFor(element.RoleButton, vocab)

	nodes := []ax.Node{
		&fakeNode{role: native, bounds: image.Rect(0, 0, 40, 40), clickable: true},
		&fakeNode{role: native, bounds: image.Rectangle{}},
	}

	converted := accessibility.FromNodes(nodes)
	if len(converted) != 1 {
		t.Fatalf("FromNodes = %d elements, want 1 (the empty one is skipped)", len(converted))
	}
}

func TestSurfaceNodes_RefusesABackendWithoutSurfaces(t *testing.T) {
	t.Parallel()

	client, err := accessibility.New(accessibility.Options{MaxDepth: 1})
	if err != nil {
		t.Skipf("no backend on this platform: %v", err)
	}
	defer client.Close()

	nodes, err := accessibility.SurfaceNodes(context.Background(), client, ax.SurfaceDock, nil, 1)
	if runtime.GOOS == element.GOOSDarwin {
		// macOS does implement the Dock; this test only pins the contract for
		// the platforms that do not. A live macOS run may return nodes.
		if err != nil && !errs.IsNotSupported(err) {
			t.Errorf("SurfaceNodes(darwin Dock) = %v, want nodes or CodeNotSupported", err)
		}

		accessibility.ReleaseNodes(nodes)

		return
	}

	if !errs.IsNotSupported(err) {
		t.Fatalf("SurfaceNodes(%s) = %v, want CodeNotSupported", runtime.GOOS, err)
	}

	if len(nodes) != 0 {
		t.Errorf("SurfaceNodes returned %d nodes together with an error", len(nodes))
	}
}

func TestWarmUp_IsANoopWhenTheBackendHasNothingToPrepare(t *testing.T) {
	t.Parallel()

	client, err := accessibility.New(accessibility.Options{MaxDepth: 1})
	if err != nil {
		t.Skipf("no backend on this platform: %v", err)
	}
	defer client.Close()

	if runtime.GOOS != element.GOOSLinux {
		if err := accessibility.WarmUp(context.Background(), client); err != nil {
			t.Errorf("WarmUp(%s) = %v, want nil", runtime.GOOS, err)
		}
	}
}
