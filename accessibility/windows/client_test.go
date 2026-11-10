//go:build windows

package windows

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
)

// Unit tests for the client shell: role resolution, the documented defaults,
// the surfaces this platform declines, and the guards that run before any COM
// call. No test here creates a UIA client.

// foreignElement is an ax.Element this backend did not produce, used to check
// the root-type guard.
type foreignElement struct{}

func (foreignElement) Release() {}

func TestClient_ResolvedRoles(t *testing.T) {
	t.Parallel()

	configured := &Client{roles: []string{"Button", "TabItem"}}
	unconfigured := &Client{}

	tests := []struct {
		name      string
		client    *Client
		requested []string
		want      []string
	}{
		{
			name:      "an explicit list wins",
			client:    configured,
			requested: []string{"Edit"},
			want:      []string{"Edit"},
		},
		{
			name:   "the client's list is the default",
			client: configured,
			want:   []string{"Button", "TabItem"},
		},
		{
			name:   "an unconfigured client falls back to the vocabulary",
			client: unconfigured,
			want:   defaultClickableRoles,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := testCase.client.resolvedRoles(testCase.requested)
			if len(got) != len(testCase.want) {
				t.Fatalf("resolvedRoles(%v) = %v, want %v", testCase.requested, got, testCase.want)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Errorf("resolvedRoles(%v) = %v, want %v", testCase.requested, got, testCase.want)
				}
			}
		})
	}
}

func TestClient_ClickableRoles_IsACopy(t *testing.T) {
	t.Parallel()

	client := &Client{roles: []string{"Button"}}

	roles := client.ClickableRoles()
	roles[0] = "Edit"

	if got := client.ClickableRoles()[0]; got != "Button" {
		t.Errorf("mutating the result changed the client's roles to %q", got)
	}
}

// TestClient_SetClickableRoles_EmptyRestoresTheDefault pins the documented
// meaning of an empty list: filtering goes back to the shipped default rather
// than widening to every control in the tree.
func TestClient_SetClickableRoles_EmptyRestoresTheDefault(t *testing.T) {
	t.Parallel()

	client := &Client{roles: []string{"Button"}}

	client.SetClickableRoles([]string{"Edit", "Button"})
	if got := client.ClickableRoles(); len(got) != 2 || got[0] != "Edit" {
		t.Fatalf("ClickableRoles() = %v after SetClickableRoles, want [Edit Button]", got)
	}

	client.SetClickableRoles(nil)

	got := client.ClickableRoles()
	if len(got) != len(defaultClickableRoles) {
		t.Fatalf("ClickableRoles() after a reset has %d entries, want %d",
			len(got), len(defaultClickableRoles))
	}
}

func TestNormalizeMaxDepth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requested  int
		configured int
		want       int
	}{
		{name: "call bound wins", requested: 5, configured: 20, want: 5},
		{name: "client bound is the default", requested: 0, configured: 20, want: 20},
		{name: "negative falls through", requested: -1, configured: 20, want: 20},
		{name: "library default", want: DefaultMaxDepth},
		{name: "library default for a negative client bound", configured: -5, want: DefaultMaxDepth},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := normalizeMaxDepth(testCase.requested, testCase.configured); got != testCase.want {
				t.Errorf("normalizeMaxDepth(%d, %d) = %d, want %d",
					testCase.requested, testCase.configured, got, testCase.want)
			}
		})
	}
}

func TestNormalizeMaxNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		maxNodes int
		want     int
	}{
		{maxNodes: 100, want: 100},
		{maxNodes: 0, want: DefaultMaxNodes},
		{maxNodes: -1, want: DefaultMaxNodes},
	}

	for _, testCase := range tests {
		if got := normalizeMaxNodes(testCase.maxNodes); got != testCase.want {
			t.Errorf("normalizeMaxNodes(%d) = %d, want %d", testCase.maxNodes, got, testCase.want)
		}
	}
}

// TestClient_DeclinesMacOnlySurfaces pins the loudness the ax contract asks
// for: the macOS-only surfaces report CodeNotSupported and the boolean answers
// report false, so an integrator can degrade instead of scanning nothing.
func TestClient_DeclinesMacOnlySurfaces(t *testing.T) {
	t.Parallel()

	client := &Client{}
	ctx := context.Background()

	if nodes, err := client.MenuBarClickableElements(ctx, 0); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("MenuBarClickableElements = (%v, %v), want a CodeNotSupported error", nodes, err)
	}

	if nodes, err := client.MissionControlClickableNodes(ctx, 0); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("MissionControlClickableNodes = (%v, %v), want a CodeNotSupported error", nodes, err)
	}

	if client.IsMissionControlActive() {
		t.Error("IsMissionControlActive = true, want false: Windows has no Mission Control")
	}

	if client.SupportsSupplementaryElements() {
		t.Error("SupportsSupplementaryElements = true, want false")
	}

	if !client.CheckPermissions() {
		t.Error("CheckPermissions = false, want true: Windows UIA needs no consent")
	}
}

// TestClient_CloseStopsQueries checks that Close is idempotent and that a query
// after it fails with an error a caller can see, rather than returning an empty
// tree that reads as "nothing clickable".
func TestClient_CloseStopsQueries(t *testing.T) {
	t.Parallel()

	client := &Client{maxDepth: DefaultMaxDepth, maxNodes: DefaultMaxNodes}

	if err := client.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close twice = %v, want nil", err)
	}

	window := &Window{hwnd: 1}

	_, err := client.ClickableNodes(context.Background(), window, nil, 0, false)
	if !errs.IsCode(err, errs.CodeAccessibilityFailed) {
		t.Errorf("ClickableNodes after Close = %v, want CodeAccessibilityFailed", err)
	}
}

func TestRootWindowHandles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		root    ax.Element
		want    []uintptr
		wantErr bool
	}{
		{name: "window", root: &Window{hwnd: 7}, want: []uintptr{7}},
		{name: "nil", root: nil, wantErr: true},
		{name: "nil window", root: (*Window)(nil), wantErr: true},
		{name: "window without a handle", root: &Window{}, wantErr: true},
		{name: "nil app", root: (*App)(nil), wantErr: true},
		{name: "app without a process", root: &App{}, wantErr: true},
		{name: "a node", root: &Node{}, wantErr: true},
		{name: "a foreign element", root: foreignElement{}, wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := rootWindowHandles(testCase.root)
			if testCase.wantErr {
				if !errs.IsCode(err, errs.CodeInvalidInput) {
					t.Fatalf("rootWindowHandles(%T) = (%v, %v), want CodeInvalidInput",
						testCase.root, got, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("rootWindowHandles(%T) = %v, want no error", testCase.root, err)
			}

			if len(got) != len(testCase.want) || got[0] != testCase.want[0] {
				t.Errorf("rootWindowHandles(%T) = %v, want %v", testCase.root, got, testCase.want)
			}
		})
	}
}

func TestClient_NewNodeAssignsUniqueIDs(t *testing.T) {
	t.Parallel()

	client := &Client{}

	first := client.newNode(control{bounds: image.Rect(0, 0, 10, 10), role: controlButton, clickable: true})
	second := client.newNode(control{bounds: image.Rect(0, 0, 10, 10), role: controlButton, clickable: true})

	if first.ID() == second.ID() {
		t.Fatalf("two nodes share the ID %q", first.ID())
	}

	if first.ID() == "" {
		t.Fatal("a node has an empty ID")
	}

	if first.Role() != controlButton || first.Bounds() != image.Rect(0, 0, 10, 10) || !first.IsClickable() {
		t.Errorf("the node does not carry the extracted control: %+v", first)
	}
}

func TestWindow_Accessors(t *testing.T) {
	t.Parallel()

	window := &Window{hwnd: 1, pid: 2, exe: "notepad.exe", title: "Untitled - Notepad"}

	if got := window.Role(); got != controlTypeWindow {
		t.Errorf("Role() = %q, want %q", got, controlTypeWindow)
	}

	if got := window.Title(); got != "Untitled - Notepad" {
		t.Errorf("Title() = %q", got)
	}

	if got := window.BundleIdentifier(); got != "notepad.exe" {
		t.Errorf("BundleIdentifier() = %q, want notepad.exe", got)
	}

	var nilWindow *Window

	if nilWindow.Role() != "" || nilWindow.Title() != "" || nilWindow.BundleIdentifier() != "" {
		t.Error("a nil window reports a role, title or bundle id")
	}

	nilWindow.Release()
}

func TestApp_Info(t *testing.T) {
	t.Parallel()

	app := &App{pid: 2, exe: "notepad.exe"}

	info, err := app.Info()
	if err != nil {
		t.Fatalf("Info() = %v", err)
	}

	if info.Role != controlTypeApplication || info.Title != "notepad.exe" {
		t.Errorf("Info() = %+v, want the application role and the executable name", info)
	}

	if got := app.BundleIdentifier(); got != "notepad.exe" {
		t.Errorf("BundleIdentifier() = %q, want notepad.exe", got)
	}

	var nilApp *App

	if _, err := nilApp.Info(); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("Info() on a nil app = %v, want CodeInvalidInput", err)
	}

	nilApp.Release()
}

// TestNode_Accessors checks the detached value a node is: every accessor reads
// the copy taken during the query, and a released node keeps answering.
func TestNode_Accessors(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(10, 20, 110, 60)
	node := &Node{
		id:          "uia_7",
		bounds:      bounds,
		role:        controlButton,
		title:       "Save",
		description: "Save the file",
		value:       "value",
		identifier:  "saveButton",
		focused:     true,
		clickable:   true,
	}

	if node.ID() != "uia_7" || node.Bounds() != bounds || node.Role() != controlButton {
		t.Errorf("node does not report its bounds, role and id: %+v", node)
	}

	if node.Title() != "Save" || node.Description() != "Save the file" ||
		node.Value() != "value" || node.Identifier() != "saveButton" {
		t.Errorf("node does not report its text: %+v", node)
	}

	if !node.IsFocused() || !node.IsClickable() {
		t.Errorf("node does not report its state: %+v", node)
	}

	node.Release()
	node.Release()

	if node.Title() != "Save" {
		t.Error("Release dropped the node's data; a node owns no COM reference")
	}

	var nilNode *Node

	if nilNode.ID() != "" || nilNode.Role() != "" || nilNode.IsClickable() || nilNode.IsFocused() {
		t.Error("a nil node reports data")
	}

	nilNode.Release()
}

// TestClient_ImplementsAXClient is a compile-time assertion made at run time so
// its failure names the contract it checks.
func TestClient_ImplementsAXClient(t *testing.T) {
	t.Parallel()

	var _ ax.Client = &Client{}
	var _ ax.Node = &Node{}
	var _ ax.Window = &Window{}
	var _ ax.App = &App{}
}

// TestPerformAction_RejectsScroll checks that the scroll action is refused at
// the action entry point with the code a caller branches on, rather than being
// interpreted as a click.
func TestPerformAction_RejectsScroll(t *testing.T) {
	t.Parallel()

	client := &Client{}

	err := client.PerformAction(action.TypeScroll, image.Point{}, false, 0)
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("PerformAction(TypeScroll) = %v, want CodeInvalidInput", err)
	}

	err = client.PerformAction(action.Type(99), image.Point{}, false, 0)
	if !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("PerformAction on an unknown type = %v, want CodeInvalidInput", err)
	}
}
