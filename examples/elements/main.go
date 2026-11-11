// 示例：中立元素模型（element 包）
//
// 演示四件事，全部离线，不需要辅助功能权限，也不需要屏幕：
//
//  1. 直接构造 element.Element 并读取属性；
//  2. 用 Option 补充文本、焦点和来源；
//  3. 把任意 ax.Node 交给 accessibility.FromNode 转换；
//  4. 用 element.Nearest / element.Toward 在元素之间做空间吸附。
//
// 运行：
//
//	go run ./examples/elements
package main

import (
	"fmt"
	"image"
	"os"
	"runtime"

	"github.com/LouYuanbo1/neru-core/accessibility"
	"github.com/LouYuanbo1/neru-core/accessibility/ax"
	"github.com/LouYuanbo1/neru-core/element"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "elements:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 直接构造一个中立元素。ID 不能为空，Bounds 不能为空。
	save, err := element.NewElement(
		"save-button",
		image.Rect(120, 80, 220, 120),
		element.RoleButton,
		element.WithTitle("Save"),
		element.WithClickable(true),
		element.WithFocused(true),
		element.WithSource(element.SourceAccessibilityTree),
	)
	if err != nil {
		return err
	}

	fmt.Printf("构造: id=%s role=%s 范围=%v 中心=%v 文本=%q 可点击=%v 聚焦=%v\n",
		save.ID(), save.Role(), save.Bounds(), save.Center(), save.Text(),
		save.IsClickable(), save.IsFocused())

	// 2. 可见性、包含与重叠都只依赖几何，可以离线判断。
	window := image.Rect(100, 60, 900, 700)
	fmt.Printf("在窗口内=%v 包含(150,90)=%v\n",
		save.IsVisible(window), save.Contains(image.Pt(150, 90)))

	// 3. 任何实现了 ax.Node 的类型都能转换。这里用一个假节点，
	//    省掉真实辅助功能服务器；真实后端返回的节点用法完全相同。
	nativeRole := map[string]string{
		"darwin":  "AXButton",
		"windows": "Button",
		"linux":   "push button",
	}[runtime.GOOS]

	converted, err := accessibility.FromNode(fakeNode{
		id:        "fake-1",
		role:      nativeRole,
		title:     "Open",
		bounds:    image.Rect(560, 380, 640, 420),
		clickable: true,
	})
	if err != nil {
		return err
	}

	fmt.Printf("转换: 原生角色=%q → 中立角色=%s 文本=%q 来源=%s\n",
		nativeRole, converted.Role(), converted.Text(), converted.Source())

	// 4. 空间吸附：Nearest 找离原点最近的目标，Toward 只朝一个方向找。
	//    这两个函数是纯几何的，输入是元素、原点与搜索区域。
	left, err := element.NewElement(
		"cancel", image.Rect(380, 420, 460, 460), element.RoleButton,
		element.WithTitle("Cancel"), element.WithClickable(true),
	)
	if err != nil {
		return err
	}

	origin := image.Pt(500, 400)
	region := image.Rect(0, 0, 1200, 800)
	candidates := []*element.Element{save, converted, left}

	if target, ok := element.Nearest(candidates, origin, region); ok {
		fmt.Printf("Nearest(500,400): %s @ %v\n", target.Element.ID(), target.Point)
	}

	if target, ok := element.Toward(candidates, origin, region, element.DirectionRight); ok {
		fmt.Printf("Toward(right): %s @ %v\n", target.Element.ID(), target.Point)
	}

	if target, ok := element.Toward(candidates, origin, region, element.DirectionLeft); ok {
		fmt.Printf("Toward(left): %s @ %v\n", target.Element.ID(), target.Point)
	}

	// 角色映射也可以单独使用，不经过 accessibility 门面。
	if role, ok := element.MapNativeRole(element.AXRoleMapper{}, "AXLink", ""); ok {
		fmt.Printf("AXLink → %s\n", role)
	}

	if role, ok := element.MapNativeRole(element.UIARoleMapper{}, "Button", ""); ok {
		fmt.Printf("UIA Button → %s\n", role)
	}

	if role, ok := element.MapNativeRole(element.ATSPIRoleMapper{}, "push button", ""); ok {
		fmt.Printf("AT-SPI push button → %s\n", role)
	}

	return nil
}

// fakeNode is a hand-written ax.Node. Only the fields this example needs are
// filled in; the rest are zero values.
type fakeNode struct {
	id        string
	role      string
	title     string
	bounds    image.Rectangle
	clickable bool
}

var _ ax.Node = fakeNode{}

func (f fakeNode) ID() string              { return f.id }
func (f fakeNode) Bounds() image.Rectangle { return f.bounds }
func (f fakeNode) Role() string            { return f.role }
func (f fakeNode) Title() string           { return f.title }
func (f fakeNode) Description() string     { return "" }
func (f fakeNode) Value() string           { return "" }
func (f fakeNode) IsFocused() bool         { return false }
func (f fakeNode) Identifier() string      { return "" }
func (f fakeNode) IsClickable() bool       { return f.clickable }
func (f fakeNode) Release()                {}
