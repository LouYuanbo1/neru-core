// 示例：从像素检测 UI 目标（capture + vision 包）
//
// 当窗口的辅助功能树缺失或不可用时，可以直接对屏幕像素做检测：
// 先 capture 截屏，再交给 vision 的轮廓 / 分割检测器，返回可点击元素。
// 返回的元素带有 IsVisionOnly()，表示只能靠坐标来点击。
//
// 运行：
//
//	go run ./examples/vision
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/contour"
	"github.com/LouYuanbo1/neru-core/vision/segment"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vision:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// 捕获整块屏幕，得到一个 Shot（像素 + 桌面原点 + DPI 缩放）。
	shot, err := capture.CaptureScreen(ctx)
	if err != nil {
		return fmt.Errorf("截屏: %w", err)
	}

	// 用 Shot 构建 vision.Frame。
	frame := vision.Frame{
		Img:    shot.Img,
		Origin: shot.Origin,
		Region: shot.Region,
		Scale:  shot.Scale,
	}

	// 策略一：轮廓检测 —— 找“按钮形状”的方框。
	buttons, err := vision.DetectContours(ctx, frame, contour.DefaultParams())
	if err != nil {
		return fmt.Errorf("轮廓检测: %w", err)
	}

	fmt.Printf("轮廓检测到 %d 个按钮状目标:\n", len(buttons))
	for _, el := range buttons {
		fmt.Printf("  %v\n", el.Bounds())
	}

	// 策略二：分割检测 —— 把单词 / 词组 / 控件分组。
	objects, err := vision.DetectSegments(ctx, frame, segment.TargetsWords)
	if err != nil {
		return fmt.Errorf("分割检测: %w", err)
	}

	fmt.Printf("分割检测到 %d 个单词目标:\n", len(objects))
	for _, el := range objects {
		// IsVisionOnly 提示该元素来自像素而非辅助功能树。
		fmt.Printf("  %v  visionOnly=%v\n", el.Bounds(), el.IsVisionOnly())
	}

	return nil
}
