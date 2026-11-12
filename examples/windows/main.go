// 示例：枚举、激活与摆放桌面窗口（window 包）
//
// List 返回所有可见顶层窗口（含最小化窗口）的标题、PID、应用标识与屏幕矩形；
// Frontmost 返回当前前台窗口；Activate 按 ID / PID / 应用名 / 标题把窗口切到前台；
// SetBounds 在激活的同时把窗口移动/缩放到指定矩形（最小化/最大化会先还原）。
//
// 这份代码是跨平台的：New 会根据运行时的系统自动挑选 macOS / Linux / Windows
// 后端，调用方不需要知道底层是 Win32 的 EnumWindows/SetWindowPos、macOS 的 AX
// 窗口对象，还是 X11 的 EWMH 客户端列表。
//
// 运行：
//
//	go run ./examples/windows                              # 只读取窗口，不改动桌面
//	go run ./examples/windows -title 记事本                 # 激活标题含“记事本”的窗口
//	go run ./examples/windows -title 记事本 -rect 0,0,960,1440  # 激活并摆到左半屏
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"

	"github.com/LouYuanbo1/neru-core/window"
)

func main() {
	title := flag.String("title", "", "激活标题包含该子串的窗口；为空时只读取")
	rect := flag.String("rect", "", "可选：把窗口摆放到的矩形 x,y,width,height；需同时给 -title")
	flag.Parse()

	if err := run(*title, *rect); err != nil {
		fmt.Fprintln(os.Stderr, "window:", err)
		os.Exit(1)
	}
}

func run(title, rect string) error {
	ctx := context.Background()

	// New 是唯一入口，自动选择当前平台的后端。
	manager, err := window.New(window.Options{})
	if err != nil {
		return err
	}
	defer manager.Close()

	// 枚举所有可见顶层窗口，前台窗口在最前。
	windows, err := manager.List(ctx)
	if err != nil {
		return fmt.Errorf("枚举窗口: %w", err)
	}

	fmt.Printf("共 %d 个可见顶层窗口\n", len(windows))

	for _, w := range windows {
		state := ""
		if w.Focused {
			state += " 前台"
		}
		if w.Minimized {
			state += " 最小化"
		}

		fmt.Printf("  %-10s pid=%-6d %-24s %-32q %v%s\n",
			w.ID, w.PID, w.Application, w.Title, w.Bounds, state)
	}

	// 前台窗口同样走统一接口；没有窗口获得焦点时返回明确错误。
	frontmost, err := manager.Frontmost(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "前台窗口:", err)
	} else {
		fmt.Printf("前台窗口: %q (%s, pid=%d)\n",
			frontmost.Title, frontmost.Application, frontmost.PID)
	}

	if title == "" {
		if rect != "" {
			return fmt.Errorf("-rect 需要同时给 -title，否则不知道要摆放哪个窗口")
		}

		return nil
	}

	target := window.Target{Title: title}

	if rect != "" {
		bounds, err := parseRect(rect)
		if err != nil {
			return err
		}

		// 激活 + 摆放一次完成；返回的是平台实际接受的矩形。
		placed, err := manager.SetBounds(ctx, target, bounds)
		if err != nil {
			return fmt.Errorf("摆放窗口: %w", err)
		}

		fmt.Printf("已摆放: %q (%s, pid=%d) -> %v\n",
			placed.Title, placed.Application, placed.PID, placed.Bounds)

		return nil
	}

	// 按标题子串激活；返回的是复核过的窗口信息，而不是"调用过 API"。
	activated, err := manager.Activate(ctx, target)
	if err != nil {
		return fmt.Errorf("激活窗口: %w", err)
	}

	fmt.Printf("已激活: %q (%s, pid=%d)\n",
		activated.Title, activated.Application, activated.PID)

	return nil
}

// parseRect 解析 x,y,width,height 形式的矩形。
func parseRect(value string) (image.Rectangle, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("-rect 需要 4 个整数 x,y,width,height，实际为 %q", value)
	}

	numbers := make([]int, 4)

	for index, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("-rect 的第 %d 个值 %q 不是整数", index+1, part)
		}

		numbers[index] = number
	}

	bounds := image.Rect(numbers[0], numbers[1], numbers[0]+numbers[2], numbers[1]+numbers[3])
	if bounds.Empty() {
		return image.Rectangle{}, fmt.Errorf("-rect 的宽高必须为正数")
	}

	return bounds, nil
}
