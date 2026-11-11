// 示例：显式选择 OCR 引擎（vision/ocr/winrt、tesseract、visionframework）
//
// 与 examples/ocr 用门面自动选择不同，这里按平台直接挑一个具体引擎：
//
//	Windows → winrt（Windows.Media.Ocr）
//	macOS   → visionframework（Apple Vision），绑定失败时回退 tesseract
//	Linux   → tesseract（进程内 libtesseract），没有库时回退 CLI
//
// 每个平台一个 engine_<os>.go，main 只依赖 engine.Recognizer 契约。
//
// 运行：
//
//	go run ./examples/ocr-engines
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

// selected 把引擎、它的名字和释放函数绑在一起。
type selected struct {
	engine.Recognizer
	name  string
	close func() error
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ocr-engines:", err)
		os.Exit(1)
	}
}

func run() error {
	recognizer, err := newRecognizer()
	if err != nil {
		return err
	}

	if recognizer.close != nil {
		defer func() { _ = recognizer.close() }()
	}

	ctx := context.Background()

	f, err := capture.CaptureScreen(ctx)
	if err != nil {
		return fmt.Errorf("截屏: %w", err)
	}

	// WordLevel 让引擎按词返回；其余选项留默认。
	opts := &engine.Options{WordLevel: true}

	text, err := vision.ReadText(ctx, f, recognizer, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", recognizer.name, err)
	}

	fmt.Printf("引擎: %s\n", recognizer.name)
	fmt.Printf("文字（前 200 字符）:\n%.200s\n", text)

	return nil
}
