// 示例：鼠标动作（action 包 + Client 输入）
//
// 演示如何使用同一份平台无关的动作模型去点击、移动、拖动和滚动鼠标。
// 坐标一律是全局桌面坐标（逻辑像素）。
//
// 运行：
//
//	go run ./examples/actions
package main

import (
	"fmt"
	"image"
	"os"

	"github.com/LouYuanbo1/neru-core/accessibility"
	"github.com/LouYuanbo1/neru-core/action"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "actions:", err)
		os.Exit(1)
	}
}

func run() error {
	client, err := accessibility.New(accessibility.Options{})
	if err != nil {
		return err
	}
	defer client.Close()

	// 1. 在某个坐标上左键单击。restoreCursor=true 表示点击后把光标移回原处。
	point := image.Pt(420, 240)
	if err := client.PerformAction(action.TypeLeftClick, point, true, 0); err != nil {
		return err
	}

	// 2. 只移动光标（不点击）。第二个参数 bypassSmooth 表示是否跳过平滑插值。
	client.MoveMouse(image.Pt(500, 300), false)

	// 3. 在当前光标位置滚动。正 deltaY 向上，正 deltaX 向左。
	if err := client.Scroll(0, 120, 0); err != nil {
		return err
	}

	// 4. 带修饰键的点击：按住主修饰键 + Shift（macOS 是 Command，其他是 Control）。
	mods := action.PrimaryModifier() | action.ModShift
	if err := client.PerformAction(action.TypeLeftClick, point, false, mods); err != nil {
		return err
	}

	// 5. 右键单击（例如呼出上下文菜单）。
	if err := client.PerformAction(action.TypeRightClick, point, true, 0); err != nil {
		return err
	}

	fmt.Printf("当前光标位置: %v\n", client.CursorPosition())
	return nil
}
