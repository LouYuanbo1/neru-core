// 示例：错误词汇表（errs 包）
//
// 演示一个错误从创建、附加上下文、包装，到调用方判定的完整路径。
// 全部离线。
//
// 运行：
//
//	go run ./examples/errors
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/LouYuanbo1/neru-core/errs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "errors:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 新建：一个错误码 + 一句给人看的话。
	noEngine := errs.New(errs.CodeNotSupported, "no OCR engine is available")
	fmt.Printf("新建: code=%s message=%q\n", errs.GetCode(noEngine), errs.Message(noEngine))

	// 2. 附加上下文：诊断信息不改变错误码。
	denied := errs.New(errs.CodeAccessibilityDenied, "accessibility permission is not granted").
		WithContext("remedy", "System Settings → Privacy & Security → Accessibility")
	fmt.Printf("上下文: %v\n", denied.Context())

	// 3. 包装：外层错误码取代内层，内层仍挂在链上。
	scan := errs.Wrap(denied, errs.CodeAccessibilityFailed, "scan the frontmost window")
	fmt.Printf("包装: code=%s message=%q\n", errs.GetCode(scan), errs.Message(scan))

	// 4. 判定：IsCode 只看最外层；沿链匹配用 errors.Is（按错误码比较）。
	fmt.Printf("IsCode(scan, FAILED)=%v IsCode(scan, DENIED)=%v IsNotSupported(scan)=%v\n",
		errs.IsCode(scan, errs.CodeAccessibilityFailed),
		errs.IsCode(scan, errs.CodeAccessibilityDenied),
		errs.IsNotSupported(scan))
	fmt.Printf("errors.Is(scan, denied)=%v\n", errors.Is(scan, denied))

	// 5. 取回具体类型，读取 Cause 与上下文。
	//    errors.As 拿到的是最外层的 *errs.Error；上下文挂在被包装的那层。
	var domainErr *errs.Error
	if errors.As(scan, &domainErr) {
		fmt.Printf("As: code=%s cause=%v\n", domainErr.Code(), domainErr.Cause())
	}

	if cause, ok := domainErr.Cause().(*errs.Error); ok {
		fmt.Printf("被包装那层的上下文: %v\n", cause.Context())
	}

	// 6. 不在词汇表里的普通 error 会被归为 CodeInternal。
	fmt.Printf("普通 error → %s\n", errs.GetCode(errors.New("boom")))

	return nil
}
