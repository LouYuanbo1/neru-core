// 示例：合成器几何（accessibility/linux/compositor 包）
//
// Wayland 上 AT-SPI 报的屏幕坐标并不可靠（GTK 4 客户端常常报 0），而合成器
// 知道窗口真正在哪。PlacementFor 把两者合起来，决定元素矩形如何换算到屏幕。
//
// 这里用构造的矩形演示规则，不连接任何合成器，任何平台都能跑。
//
// 运行：
//
//	go run ./examples/compositor
package main

import (
	"fmt"
	"image"
	"os"

	"github.com/LouYuanbo1/neru-core/accessibility/linux/compositor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compositor:", err)
		os.Exit(1)
	}
}

func run() error {
	// AT-SPI 报的窗口屏幕矩形。Wayland 上它可能整体偏移，甚至从 (0,0) 开始。
	frameScreen := image.Rect(500, 400, 1300, 1060)

	// 窗口相对矩形：元素坐标相对于窗口左上角。
	frameWindow := image.Rect(0, 0, 800, 660)

	// 合成器报的窗口原点，这才是窗口真正在屏幕上的位置。
	origin := image.Pt(240, 160)

	// 有窗口相对矩形：精确，按窗口原点平移。
	exact := compositor.PlacementFor(origin, frameScreen, frameWindow, true)
	fmt.Printf("窗口相对=%v 偏移=%v\n", exact.WindowRelative, exact.Offset)

	// 没有窗口相对矩形：只能按 AT-SPI 报的屏幕坐标平移，偏移量会带上
	// 它自己的误差。
	approx := compositor.PlacementFor(origin, frameScreen, frameWindow, false)
	fmt.Printf("仅屏幕坐标=%v 偏移=%v\n", approx.WindowRelative, approx.Offset)

	// 把窗口内的一个元素矩形挪到屏幕坐标。
	elem := image.Rect(20, 30, 120, 60)
	fmt.Printf("元素 %v → 屏幕 %v\n", elem, elem.Add(exact.Offset))

	return nil
}
