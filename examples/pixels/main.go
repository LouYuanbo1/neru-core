// 示例：像素层（vision/pixels 包）
//
// 演示 Frame 的字段、Crop 的裁剪语义、DeriveScale 的缩放推导，以及
// 检测器共享的像素操作（Downsample / Resample / Rows）。
// 图像在代码里画出来，全部离线。
//
// 运行：
//
//	go run ./examples/pixels
package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pixels:", err)
		os.Exit(1)
	}
}

func run() error {
	// 画一张 8x4 的图：左半边红，右半边蓝。
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))

	for y := range 4 {
		for x := range 8 {
			c := color.RGBA{R: 255, A: 255}
			if x >= 4 {
				c = color.RGBA{B: 255, A: 255}
			}

			img.SetRGBA(x, y, c)
		}
	}

	// 一个 Frame 描述“像素 + 它们放在桌面的哪里、以什么比例”。
	f := pixels.Frame{
		Img:    img,
		Origin: image.Pt(100, 100),
		Region: image.Rect(100, 100, 108, 104),
		Scale:  1,
	}
	fmt.Printf("Frame: 图像=%v 原点=%v 区域=%v 缩放=%v\n",
		f.Img.Rect, f.Origin, f.Region, f.Scale)

	// Crop 取出区域内的一块，返回的图像锚定在 (0,0)，
	// Origin 是裁剪后那块在桌面上的位置。
	part, err := pixels.Crop(f, image.Rect(102, 101, 106, 103))
	if err != nil {
		return err
	}
	fmt.Printf("Crop: 图像=%v 原点=%v 区域=%v 缩放=%v\n",
		part.Img.Rect, part.Origin, part.Region, part.Scale)

	// DeriveScale 从“图像宽度 / 逻辑宽度”推缩放，
	// 顺手把 1.5 这种被取整过的比例还原出来。
	fmt.Printf("DeriveScale(2560, 1707)=%v\n", pixels.DeriveScale(2560, 1707))

	// Downsample 做块平均，用于把高 DPI 截图降到逻辑像素。
	small := pixels.Downsample(img, 2)
	fmt.Printf("Downsample(2): 图像=%v 左上像素=%v\n", small.Rect, small.RGBAAt(0, 0))

	// Resample 直接处理打包像素（例如 BGRA 的截图），
	// 可以同时完成通道换序与降采样。
	raw := []byte{10, 20, 30, 255, 50, 60, 70, 255} // 两个 BGRA 像素
	resampled := pixels.Resample(raw, len(raw), 2, 1, pixels.BGRA, 1)
	fmt.Printf("Resample(BGRA, 1): 图像=%v 像素=%v\n",
		resampled.Rect, resampled.RGBAAt(0, 0))

	// Rows 把一段行区间按核数切开，检测器用它并行扫描。
	covered := 0
	pixels.Rows(4, func(y0, y1 int) { covered += y1 - y0 })
	fmt.Printf("Rows(4) 覆盖行数=%d\n", covered)

	return nil
}
