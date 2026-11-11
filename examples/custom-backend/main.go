// 示例：自己实现一个辅助功能后端（ax.Client 契约）
//
// ax.Client 是一个接口，平台后端只是它的实现之一。任何实现了它的类型都能
// 配合 accessibility.FromNode / SurfaceNodes / WarmUp 使用；这个例子用一个
// 只读的假后端演示最小实现：能回答的方法给出结果，不能回答的返回
// errs.CodeNotSupported，而不是假装成功。
//
// 全部离线，不需要辅助功能权限。
//
// 运行：
//
//	go run ./examples/custom-backend
package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"runtime"

	"github.com/LouYuanbo1/neru-core/accessibility"
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/errs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "custom-backend:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	var client ax.Client = &backend{}

	window, err := client.FrontmostWindow(ctx)
	if err != nil {
		return err
	}
	defer window.Release()

	nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
	if err != nil {
		return err
	}
	defer accessibility.ReleaseNodes(nodes)

	// 门面照常工作：它只认 ax.Client，不关心背后是谁。
	for _, elem := range accessibility.FromNodes(nodes) {
		fmt.Printf("元素: %-8s %-10q %v\n", elem.Role(), elem.Text(), elem.Bounds())
	}

	// 没有实现的能力返回 CodeNotSupported，调用方据此降级。
	if _, err := client.AllWindows(ctx); errs.IsNotSupported(err) {
		fmt.Printf("AllWindows: %s\n", errs.Message(err))
	}

	return nil
}

// backend 是 ax.Client 的最小实现。
type backend struct {
	roles []string
}

var _ ax.Client = (*backend)(nil)

func unsupported(what string) error {
	return errs.Newf(errs.CodeNotSupported, "%s is not implemented by this example", what)
}

func (*backend) FrontmostWindow(context.Context) (ax.Window, error) { return window{}, nil }

func (b *backend) ClickableNodes(
	context.Context,
	ax.Element,
	[]string,
	int,
	bool,
) ([]ax.Node, error) {
	// 后端报告的是平台原生角色名；这里按运行平台挑一个，好让
	// accessibility.FromNode 能把它翻译成 button。
	nativeRole := map[string]string{
		"darwin":  "AXButton",
		"windows": "Button",
		"linux":   "push button",
	}[runtime.GOOS]

	return []ax.Node{
		node{id: "fake-save", role: nativeRole, title: "Save", bounds: image.Rect(100, 80, 200, 120)},
	}, nil
}

func (*backend) AllWindows(context.Context) ([]ax.Window, error) {
	return nil, unsupported("AllWindows")
}

func (*backend) FrontmostAndPopoverWindows(context.Context) ([]ax.Window, error) {
	return nil, unsupported("FrontmostAndPopoverWindows")
}

func (*backend) FocusedApplication(context.Context) (ax.App, error) {
	return nil, unsupported("FocusedApplication")
}

func (*backend) ApplicationByBundleID(context.Context, string) (ax.App, error) {
	return nil, unsupported("ApplicationByBundleID")
}

func (*backend) MenuBarClickableElements(context.Context, int) ([]ax.Node, error) {
	return nil, unsupported("MenuBarClickableElements")
}

func (*backend) MissionControlClickableNodes(context.Context, int) ([]ax.Node, error) {
	return nil, unsupported("MissionControlClickableNodes")
}

func (*backend) ClickableElementsFromBundleID(context.Context, string, []string, int) ([]ax.Node, error) {
	return nil, unsupported("ClickableElementsFromBundleID")
}

func (*backend) ActiveScreenBounds() image.Rectangle { return image.Rect(0, 0, 1920, 1080) }

func (*backend) PerformAction(action.Type, image.Point, bool, action.Modifiers) error {
	return unsupported("PerformAction")
}

func (*backend) Scroll(int, int, action.Modifiers) error { return unsupported("Scroll") }

func (*backend) MoveMouse(image.Point, bool) {}

func (*backend) CursorPosition() image.Point { return image.Point{} }

func (*backend) CheckPermissions() bool { return true }

func (b *backend) SetClickableRoles(roles []string) { b.roles = roles }

func (b *backend) ClickableRoles() []string { return b.roles }

func (*backend) IsMissionControlActive() bool { return false }

func (*backend) SupportsSupplementaryElements() bool { return false }

func (*backend) Close() error { return nil }

// window 是 ax.Window：ax.Element（只有 Release）加 Role。
type window struct{}

func (window) Release()     {}
func (window) Role() string { return "AXWindow" }

// node 是 ax.Node：平台原生角色、边界与文本，外加一个平台引用。
type node struct {
	id     string
	role   string
	title  string
	bounds image.Rectangle
}

func (n node) ID() string              { return n.id }
func (n node) Bounds() image.Rectangle { return n.bounds }
func (n node) Role() string            { return n.role }
func (n node) Title() string           { return n.title }
func (n node) Description() string     { return "" }
func (n node) Value() string           { return "" }
func (n node) IsFocused() bool         { return false }
func (n node) Identifier() string      { return "" }
func (n node) IsClickable() bool       { return true }
func (n node) Release()                {}
