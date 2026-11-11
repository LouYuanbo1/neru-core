// 示例：自定义 OCR 引擎（实现 engine.Recognizer 契约）
//
// 任何实现了 Recognize 的类型都能交给 vision.ReadText 或 vision.Pipeline，
// 不需要 import 任何具体引擎。这里用固定结果代替真实 OCR，离线可跑。
//
// 运行：
//
//	go run ./examples/custom-engine
package main

import (
	"context"
	"fmt"
	"image"
	"os"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// fixedEngine 是最小的 engine.Recognizer：只实现 Recognize。
// 它不实现 HealthChecker，vision 会认为它总是可用。
type fixedEngine struct{}

func (fixedEngine) Recognize(
	_ context.Context,
	_ *image.RGBA,
	_ engine.Options,
) ([]engine.Word, error) {
	return []engine.Word{
		{Text: "Save", Bounds: image.Rect(10, 10, 60, 30), Confidence: 0.99},
		{Text: "Cancel", Bounds: image.Rect(80, 10, 150, 30), Confidence: 0.97},
	}, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "custom-engine:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	f := pixels.Frame{
		Img:    image.NewRGBA(image.Rect(0, 0, 200, 100)),
		Origin: image.Pt(400, 300),
		Region: image.Rect(400, 300, 600, 400),
		Scale:  1,
	}

	// 1. 只要文字：ReadText 把词拼成一个字符串。
	text, err := vision.ReadText(ctx, f, fixedEngine{}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("文字: %q\n", text)

	// 2. 要分类后的元素：Pipeline 在文字之上跑分类器。
	pipeline := &vision.Pipeline{
		Text:       fixedEngine{},
		DetectText: true,
	}

	elements, err := pipeline.Detect(ctx, f)
	if err != nil {
		return err
	}

	fmt.Printf("元素: %d 个\n", len(elements))
	for _, el := range elements {
		fmt.Printf("  %-10s 范围=%v 可点击=%v\n", el.Role(), el.Bounds(), el.IsClickable())
	}

	return nil
}
