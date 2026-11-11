// 示例：屏幕捕获（vision/capture 包）
//
// 捕获整屏与指定区域，打印 frame 的四个字段。capture 返回的就是
// pixels.Frame，可以原样交给 vision 的检测器或 OCR，中间不需要转换。
//
// 运行：
//
//	go run ./examples/capture
package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/capture"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "capture:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	whole, err := capture.CaptureScreen(ctx)
	if err != nil {
		if errs.IsNotSupported(err) {
			fmt.Println("这个平台不支持屏幕捕获；库的其余部分仍可使用。")
			return nil
		}

		return fmt.Errorf("整屏捕获: %w", err)
	}

	fmt.Printf("整屏: 图像=%v 原点=%v 区域=%v 缩放=%v\n",
		whole.Img.Rect, whole.Origin, whole.Region, whole.Scale)
	fmt.Printf("      逻辑尺寸约 %dx%d\n",
		int(float64(whole.Img.Rect.Dx())/whole.Scale),
		int(float64(whole.Img.Rect.Dy())/whole.Scale))

	// 区域捕获用逻辑（桌面）坐标，返回的 Origin 就是区域左上角。
	region := image.Rect(
		whole.Region.Min.X, whole.Region.Min.Y,
		whole.Region.Min.X+200, whole.Region.Min.Y+120,
	).Intersect(whole.Region)

	if region.Empty() {
		return nil
	}

	part, err := capture.CaptureRect(ctx, region)
	if err != nil {
		return fmt.Errorf("区域捕获: %w", err)
	}

	fmt.Printf("区域: 图像=%v 原点=%v 区域=%v 缩放=%v\n",
		part.Img.Rect, part.Origin, part.Region, part.Scale)

	// Windows 上可以在捕获前排除本进程的窗口，避免浮层被拍进画面：
	//
	//	if runtime.GOOS == "windows" {
	//		_ = capture.ExcludeOwnWindows()
	//	}
	//
	// 其余平台返回 CodeNotSupported。
	return nil
}
