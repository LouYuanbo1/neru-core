// 示例：读取辅助功能树（accessibility 包）
//
// 演示如何获取当前桌面最前窗口，扫描其中的可点击节点，并把平台原生的
// 角色名翻译成中立的 element.Role。这份代码是跨平台的：New 会根据运行
// 时的系统自动挑选 macOS / Linux / Windows 后端。
//
// 运行：
//
//	go run ./examples/accessibility
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/accessibility"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "accessibility:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// New 是唯一入口，自动选择当前平台的后端。
	client, err := accessibility.New(accessibility.Options{})
	if err != nil {
		return err
	}
	defer client.Close()

	// 是否有辅助功能权限（macOS 需要系统授权）。
	if !client.CheckPermissions() {
		fmt.Fprintln(os.Stderr, "accessibility: 未授予辅助功能权限")
	}

	// 可选：启动时预热输入路径（Linux 下会提前打开 libei / uinput，
	// 可能弹出授权对话框；其他平台无副作用）。
	_ = accessibility.WarmUp(ctx, client)

	// 找到最前（当前聚焦）的窗口。Window 持有平台引用，用完需 Release。
	window, err := client.FrontmostWindow(ctx)
	if err != nil {
		return fmt.Errorf("最前窗口: %w", err)
	}
	defer window.Release()

	// roles 传 nil 表示使用当前平台的默认可点击角色；
	// 最后一个参数 true 表示读取可见性等状态标志。
	nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
	if err != nil {
		return fmt.Errorf("可点击节点: %w", err)
	}

	// 转换后再统一释放，避免逐个手动 release。
	elements := accessibility.FromNodes(nodes)
	accessibility.ReleaseNodes(nodes)

	fmt.Printf("最前窗口(role=%s)共有 %d 个可点击元素\n", window.Role(), len(elements))

	for _, el := range elements {
		fmt.Printf("  %-14s 范围=%v 可点击=%v 文本=%q\n",
			el.Role(), el.Bounds(), el.IsClickable(), el.Text())
	}

	return nil
}
