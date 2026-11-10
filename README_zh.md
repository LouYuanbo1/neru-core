# neru-core

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./README.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./README_zh.md)

一个纯 Go 编写的库，用于读取桌面辅助功能树（Accessibility Tree）、从像素中检测 UI 目标，并注入指针输入。

它是 [Neru](https://github.com/y3owk1n/neru) 的辅助功能（AX）与视觉（vision）部分，被抽取为公开模块。每个平台的后端均不使用 CGo 编写：macOS 和 Linux 在运行时通过
[`purego`](https://github.com/ebitengine/purego) 绑定系统库，Windows 使用标准库的 Win32/COM syscall 接口，Linux 屏幕捕获则直接使用 X11 线协议通信。导入 `neru-core` 的程序仍然是一个纯 Go 二进制文件，可以从任意宿主机交叉编译，且始终不需要 C 工具链。

```
go get github.com/LouYuanbo1/neru-core
```

需要 Go 1.27.3 或更高版本，即 `go.mod` 中指定的版本。

## 项目结构

| 包 | 用途 |
| --- | --- |
| `accessibility` | 平台选择器与中立节点转换：`New`、`FromNode`、`FromNodes`、`SurfaceNodes`。 |
| `accessibility/ax` | 后端契约（`Client`、`Window`、`App`、`Node`、`SurfaceProvider`）。 |
| `accessibility/darwin` | macOS 后端：通过 ApplicationServices 使用 AXUIElement，CGEvent 输入，Dock / 通知中心 / 台前调度 / 画中画 / 录屏 UI，Mission Control，辅助功能缩放，bundle 类型检测——全部通过 `purego` 实现。 |
| `accessibility/windows` | Windows 后端：通过 COM 使用 UI Automation，通过 `SendInput` 实现输入。 |
| `accessibility/linux` | Linux 后端：通过 D-Bus 使用 AT-SPI，通过 X11/XTest 实现输入，通过 wlroots/libei 实现 Wayland 输入，全部通过 `purego` 实现。 |
| `accessibility/linux/compositor` | 用于焦点与窗口原点的合成器（compositor）桥接：KWin、GNOME Shell、niri、sway、Hyprland、COSMIC。 |
| `element` | 中立元素模型、角色词汇表、AX / AT-SPI / UIA 的原生角色映射，以及空间吸附（`Nearest`、`Toward`）。 |
| `action` | 平台无关的鼠标动作模型与修饰键位掩码。 |
| `errs` | 带错误码的小型错误词汇表（`CodeNotSupported`、`CodeAccessibilityFailed` 等）。 |
| `vision` | 将捕获的像素转换为元素：`Pipeline`（OCR + 矩形检测 + 合并 + 分类）、轮廓与分割检测器、区域合并、`ReadText`。 |
| `vision/capture` | 各平台屏幕捕获：Windows 使用 GDI，Linux 使用 X11 root / wlr-screencopy / desktop-portal 回退，macOS 使用 `screencapture`。 |
| `vision/ocr` | 识别器契约（`Recognizer`、`Word`、`Health`）。 |
| `vision/ocr/winrt`、`vision/ocr/tesseract`、`vision/ocr/visionframework` | 每个包一个引擎：Windows.Media.Ocr、libtesseract（通过 `purego` 进程内调用，或使用 CLI），以及 Apple 的 Vision 框架（文本、矩形、显著图）。 |
| `vision/ocr/auto` | 当前运行平台内置的识别器。 |
| `vision/contour`、`vision/segment`、`vision/frame` | 纯 Go 检测器以及它们共享的像素管道。 |

## 读取辅助功能树

```go
import (
	"context"
	"fmt"

	"github.com/LouYuanbo1/neru-core/accessibility"
)

ctx := context.Background()

client, err := accessibility.New(accessibility.Options{})
if err != nil {
	return err
}
defer client.Close()

if !client.CheckPermissions() {
	return fmt.Errorf("accessibility permission is not granted")
}

window, err := client.FrontmostWindow(ctx)
if err != nil {
	return err
}
defer window.Release()

nodes, err := client.ClickableNodes(ctx, window, nil, 0, false)
if err != nil {
	return err
}

elements := accessibility.FromNodes(nodes) // 中立元素 element.Role，已拷贝文本
for _, node := range nodes {
	node.Release() // 每个节点都持有一个平台引用
}

for _, elem := range elements {
	fmt.Println(elem.Role(), elem.Bounds(), elem.IsClickable())
}
```

`ClickableNodes` 接受原生角色名称，因为平台后端在读取元素文本之前就会进行过滤。传入 `nil` 使用文档中默认目标，或使用中立词汇表解析你自己的角色：

```go
roles := element.ResolveRoleEntries([]string{"button", "link", "ax:AXTextArea"}, "darwin")
nodes, err := client.ClickableNodes(ctx, window, roles.Native, 0, false)
```

`element.ResolveRoleEntries` 会报告拼写错误的条目（致命错误）、属于其他平台的条目（忽略）、以及此处无对应物的条目（忽略），而不是静默地匹配不到任何结果。当你已经持有 `element.Role` 值时，`ResolveRoles` 是更短的路径。

在 macOS 上，Neru 扫描的辅助表面只需要一次调用即可获取：

```go
for _, surface := range []accessibility.Surface{
	accessibility.SurfaceDock,
	accessibility.SurfaceNotificationCenter,
	accessibility.SurfaceStageManager,
	accessibility.SurfacePictureInPicture,
	accessibility.SurfaceScreenCaptureUI,
} {
	nodes, err := accessibility.SurfaceNodes(ctx, client, surface, nil, 0)
	if err != nil {
		continue // 其他平台返回 CodeNotSupported，未运行时返回 CodeElementNotFound
	}
	// ... 转换或使用这些节点，然后释放它们
}
```

没有此类表面的平台会返回 `errs.CodeNotSupported`，因此跨平台调用方可以对它们全部进行无条件循环。在启动时调用一次 `accessibility.WarmUp(ctx, client)`：在 Linux 上它会打开 Wayland 输入会话（可能弹出授权对话框）并在首次使用快捷键之前提前打开 uinput 滚轮。

## 针对找到的目标执行操作

```go
import (
	"image"

	"github.com/LouYuanbo1/neru-core/action"
)

point := image.Pt(420, 240)
err := client.PerformAction(action.TypeLeftClick, point, true, 0)
```

动作模型涵盖左/右/中键点击、按下、释放、切换、移动光标与滚动，动作执行期间可按住 `action.ModCmd`、`ModShift`、`ModAlt` 与 `ModCtrl` 修饰键。`restoreCursor` 会在点击后将光标恢复到原来的位置，这正是提示（hint）激活所期望的行为。

## 在没有辅助功能树的情况下查找目标

视觉（Vision）针对的是辅助功能树缺失或失真（说谎）的窗口。捕获一块区域、运行检测器，即可在全局坐标中获得可点击的元素：

```go
import (
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/contour"
)

shot, err := capture.CaptureScreen(ctx)
if err != nil {
	return err
}

frame := vision.Frame{
	Img:    shot.Img,
	Origin: shot.Origin,
	Region: shot.Region,
	Scale:  shot.Scale,
}

elements, err := vision.DetectContours(ctx, frame, contour.DefaultParams())
```

`vision.DetectSegments` 是另一个检测器：它对单词和控件进行分组，而不是查找按钮形状的方框。两者都会返回设置了 `Element.IsVisionOnly()` 的 `element.Element`，因此调用方知道它必须通过坐标来操作。对于 OCR 引擎，`vision.RegionsFromWords` 将捕获像素空间中的识别结果单词映射到桌面上，`vision.ElementsFromRegions` 则使用与检测器相同的几何启发式算法对其进行分类。

### 文本识别与 `vision` 策略

完整的“vision”策略是 OCR 加矩形检测，并进行合并与分类。OCR 引擎是可选的按需导入项，因此除非程序主动请求，否则不会引入任何原生库：

```go
import (
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr/winrt"
)

recognizer, err := winrt.New() // 或 tesseract.NewLibrary / visionframework.New
if err != nil {
	return err
}
defer recognizer.Close()

pipeline := vision.Pipeline{
	Text:             recognizer,
	DetectText:       true,
	DetectRectangles: true, // 纯 Go 轮廓检测器，除非设置了 Boxes
	WordLevel:        true,
}

elements, err := pipeline.Detect(ctx, frame)
```

`vision/ocr/winrt`（Windows.Media.Ocr）、`vision/ocr/tesseract`（通过 purego 进行进程内调用 libtesseract，或用 `NewCLI` 调用本地 `tesseract` 可执行文件）以及 `vision/ocr/visionframework`（Apple 的 Vision 框架）各自都实现了 `vision/ocr.Recognizer`。调用方也可以提供自己的引擎，或提供一个 `vision.BoxFinder`（如 Vision 框架的矩形请求）；`Pipeline` 只依赖这两个接口。

## 平台支持

| 能力 | macOS | Linux | Windows |
| --- | --- | --- | --- |
| 读取辅助功能树 | AX（ApplicationServices） | AT-SPI（D-Bus） | UI Automation（COM） |
| 可点击节点扫描 | 支持 | 支持 | 支持 |
| 最前窗口 / 应用 | 支持 | 支持，优先使用合成器 | 支持 |
| 菜单栏扫描 | 支持 | — | — |
| Dock / 通知中心 / 台前调度 / 画中画 / 录屏 UI | 支持（`SurfaceNodes`） | — | — |
| Mission Control 窗口与 Spaces 条 | 支持 | — | — |
| 鼠标点击 / 移动 / 滚动 | CGEvent | XTest（X11）、libei（Wayland）、uinput 滚轮 | `SendInput` |
| 平滑光标移动 | 支持（`SmoothSteps`） | — | — |
| 屏幕捕获 | `screencapture` | X11 root、wlr-screencopy（Wayland） | GDI `BitBlt` |
| 文本识别 | Vision 框架 | libtesseract / tesseract CLI | Windows.Media.Ocr |
| 辅助功能权限门禁 | `AXIsProcessTrusted` | 无 | 无 |

不支持的表面会返回 `errs.CodeNotSupported`；它们绝不会用空结果假装成功。

已知限制，由后端自己声明：

- **macOS。** 这里扫描了 Neru 扫描的每个表面：最前窗口、菜单栏、Dock、通知中心、台前调度、画中画、录屏 UI 以及 Mission Control。在 macOS 14 及更高版本上使用 ScreenCaptureKit 进行捕获，速度快且会将调用方自己的窗口排除在外；任何失败的位置都会回退到 `screencapture` 工具，后者速度较慢但会包含这些窗口。辅助功能缩放校正使用与 Neru 相同的公共 API 加私有 SkyLight 查询。
- **Linux。** 在 X11 上注入使用 XTest；在 Wayland 上，当合成器暴露 wlroots 虚拟指针时使用它（sway、niri、Hyprland、wayfire），否则通过 RemoteDesktop portal 使用 libei（KDE、Mutter、cosmic）。若会话两者都不可用，则返回 `CodeNotSupported`。当 `/dev/uinput` 可写时，uinput 滚轮可复现像素级精确的平滑滚动。捕获读取 X11 root、wlr-screencopy 会话中合成器的 wlr-screencopy 协议、KDE 上 KWin 的 ScreenShot2 接口（无提示、带元数据几何），或在其发送方白名单允许时使用 GNOME Shell 的 Screenshot 接口；否则回退到桌面 portal 的 `Screenshot` 接口（较慢，可能提示一次）。焦点与窗口原点来自 KWin、GNOME Shell、niri、sway、Hyprland 以及（通过可注入的几何来源）COSMIC 的合成器桥接；X11 仍从 AT-SPI 推断焦点，因此 Chromium 和 Electron 偶尔可能解析为后台窗口。第一次树调用会启用 `org.a11y.Status`，`Close` 会恢复会话之前的值。
- **Windows。** 没有菜单栏或 Mission Control 的对应物。点击是坐标注入而非 UIA Invoke，因此其行为像真实鼠标。每次查询都会创建并释放自己的 UIA client 对象，因此没有 COM 对象会在 goroutine 之间共享。`capture.ExcludeOwnWindows` 使用 `WDA_EXCLUDEFROMCAPTURE`（Windows 10 2004+）标记进程的顶层窗口，使提示浮层不会出现在其检测的帧中。

## 设计说明

**无 CGo。** 所有原生能力都在运行时访问而非链接：macOS 上的 CoreFoundation、ApplicationServices、CoreGraphics 与 Objective-C 运行时；Linux 上通过 D-Bus 的 AT-SPI、X11/XTest、Wayland 线协议、libei 与 `/dev/uinput`；Windows 上标准库的 `syscall`/COM 接口。文本识别遵循同样的规则：Windows.Media.Ocr 通过 WinRT、libtesseract 通过 `purego`、Apple 的 Vision 框架通过 `purego` 与 `objc_msgSend`。内部 `native` 包是符号加载的唯一位置，且它是内部的，因此裸函数指针永远不会泄漏到公共 API 中。

**边缘使用原生角色，模型使用中立角色。** 后端的 `Node` 报告平台的角色名称（`AXButton`、`push button`、`Button`），因为这是其过滤器所比较的，也可能是调用方诊断所需的。`accessibility.FromNode` 通过平台的映射器将其翻译为 `element.Role`。映射器是导出的，因此自行维护节点类型的程序无需此包也能进行翻译。

**所有权。** `Window`、`App` 与 `Node` 值持有一个平台引用，必须恰好释放一次（`Release`）。`FromNode` 会拷贝其所需的一切，因此调用方可以在转换后立即释放节点。元素是不可变的，并且在 goroutine 之间共享是安全的。

**延迟。** macOS 遍历会将元素的属性批处理为一次辅助功能调用，在返回前读取文本，并对每个候选进行窗口服务器的命中测试，以便被覆盖的控件不会被提示。`SkipHitTest` 用每个候选的一次往返来换取这种精确性。`MaxDepth` 与 `MaxNodes` 会约束病态的树。

## 开发

```bash
go test ./...
go vet ./...

# 无需 C 工具链即可从任意宿主机交叉编译：
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=linux   GOARCH=arm64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

驱动真实桌面的集成测试带有 `integration && <os>` 标签，不属于 `go test ./...` 的一部分。

## 许可证

MIT，与 Neru 相同的许可证，本模块即从中抽取。参见 `LICENSE`。