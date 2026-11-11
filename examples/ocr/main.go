// 示例：文本识别与 vision 完整策略（ocr + vision 包）
//
// 两种情况：
//  1. 只想“读取屏幕上的文字”：capture 截屏 + ReadText。
//  2. 想“识别并分类可点击目标”：用 vision.Pipeline（OCR + 矩形检测，
//     再合并、去重、分类成中立的 element 角色）。
//
// capture 直接返回 pixels.Frame，也就是 vision 检测器吃的那个类型，所以
// 截屏结果可以原样传给 ReadText / Pipeline，中间不需要任何转换。
//
// OCR 引擎是可选的按需导入。这里直接用 ocr 包：它返回操作系统提供的
// 引擎（Windows.Media.Ocr / Apple Vision / libtesseract），因此跨平台。
//
// 运行：
//
//	go run ./examples/ocr
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ocr:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// 用操作系统提供的 OCR 引擎。
	recognizer, err := ocr.New()
	if err != nil {
		return err
	}

	f, err := capture.CaptureScreen(ctx)
	if err != nil {
		return fmt.Errorf("截屏: %w", err)
	}

	// 用法一：只要文字，直接 ReadText。
	text, err := vision.ReadText(ctx, f, recognizer, nil)
	if err != nil {
		return fmt.Errorf("ReadText: %w", err)
	}
	fmt.Printf("屏幕上的文字（前 200 字符）:\n%.200s\n\n", text)

	// 用法二：完整的 vision 策略 —— 文本 + 矩形合并 + 分类。
	pipeline := &vision.Pipeline{
		Text:             recognizer,
		DetectText:       true,
		DetectRectangles: true,
		WordLevel:        true,
	}

	elements, err := pipeline.Detect(ctx, f)
	if err != nil {
		return fmt.Errorf("Pipeline.Detect: %w", err)
	}

	fmt.Printf("识别出 %d 个带角色的目标:\n", len(elements))
	for _, el := range elements {
		fmt.Printf("  %-14s 范围=%v\n", el.Role(), el.Bounds())
	}

	return nil
}
