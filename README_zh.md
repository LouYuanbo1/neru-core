# neru-core

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./README.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./README_zh.md)

一个纯 Go 编写的库，用于读取桌面辅助功能树（Accessibility Tree）、从像素中检测 UI 目标、注入指针输入，以及枚举、切换、移动或缩放桌面窗口。

它是 [Neru](https://github.com/y3owk1n/neru) 的辅助功能（AX）与视觉（vision）部分，被抽取为公开模块，并额外提供 Neru 本身不需要的桌面窗口管理。每个平台的后端均不使用 CGo 编写：macOS 和 Linux 在运行时通过
[`purego`](https://github.com/ebitengine/purego) 绑定系统库，Windows 使用标准库的 Win32/COM syscall 接口，Linux 屏幕捕获则直接使用 X11 线协议通信。导入 `neru-core` 的程序仍然是一个纯 Go 二进制文件，可以从任意宿主机交叉编译，且始终不需要 C 工具链。

```
go get github.com/LouYuanbo1/neru-core
```

需要 Go 1.27.3 或更高版本，即 `go.mod` 中指定的版本。

## 快速开始

### 读取屏幕上的文字

```go
import (
	"context"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

ctx := context.Background()

recognizer, err := ocr.New() // 当前系统提供的引擎
if err != nil {
	return err
}

frame, err := capture.CaptureScreen(ctx)
if err != nil {
	return err
}

text, err := vision.ReadText(ctx, frame, recognizer, nil)
```

### 列出可点击元素

```go
import "github.com/LouYuanbo1/neru-core/accessibility"

client, err := accessibility.New(accessibility.Options{})
if err != nil {
	return err
}
defer client.Close()

window, err := client.FrontmostWindow(ctx)
if err != nil {
	return err
}
defer window.Release()

nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
if err != nil {
	return err
}
defer accessibility.ReleaseNodes(nodes)

for _, elem := range accessibility.FromNodes(nodes) {
	fmt.Println(elem.Role(), elem.Text(), elem.Bounds())
}
```

### 点击离光标最近的目标

```go
import (
	"github.com/LouYuanbo1/neru-core/action"
	"github.com/LouYuanbo1/neru-core/element"
)

target, ok := element.Nearest(
	accessibility.FromNodes(nodes),
	client.CursorPosition(),
	client.ActiveScreenBounds(),
)
if !ok {
	return errors.New("没有可点击的目标")
}

// 第三个参数 restoreCursor 表示点击后把光标移回原处。
err := client.PerformAction(action.TypeLeftClick, target.Point, true, 0)
```

## 项目结构

| 包 | 用途 |
| --- | --- |
| `accessibility` | 平台选择器与中立节点转换：`New`、`FromNode`、`FromNodes`、`SurfaceNodes`、`WarmUp`。 |
| `accessibility/ax` | 后端契约（`Client`、`Window`、`App`、`Node`、`Surface`、`SurfaceProvider`）。 |
| `accessibility/darwin` | macOS 后端：通过 ApplicationServices 使用 AXUIElement，CGEvent 输入，Dock / 通知中心 / 台前调度 / 画中画 / 录屏 UI，Mission Control，辅助功能缩放，bundle 类型检测——全部通过 `purego` 实现。 |
| `accessibility/windows` | Windows 后端：通过 COM 使用 UI Automation，通过 `SendInput` 实现输入。 |
| `accessibility/linux` | Linux 后端：通过 D-Bus 使用 AT-SPI，通过 X11/XTest 实现输入，通过 wlroots/libei 实现 Wayland 输入，全部通过 `purego` 实现。 |
| `accessibility/linux/compositor` | 用于焦点与窗口原点的合成器桥接：KWin、GNOME Shell、niri、sway、Hyprland、COSMIC。 |
| `window` | 桌面窗口管理：枚举顶层窗口、读取前台窗口、按 ID / PID / 应用 / 标题激活，以及移动/缩放窗口。后端分别是 Win32、macOS 辅助功能与 X11 EWMH。 |
| `element` | 中立元素模型、角色词汇表、AX / AT-SPI / UIA 的原生角色映射，以及空间吸附（`Nearest`、`Toward`）。 |
| `action` | 平台无关的鼠标动作模型与修饰键位掩码。 |
| `errs` | 带错误码的小型错误词汇表（`CodeNotSupported`、`CodeAccessibilityFailed` 等）。 |
| `vision` | 将捕获的像素转换为元素：`Pipeline`（OCR + 矩形检测 + 合并 + 分类）、轮廓与分割检测器、区域合并、`ReadText`。 |
| `vision/capture` | 各平台屏幕捕获：Windows 使用 GDI，Linux 使用 X11 root / wlr-screencopy / desktop-portal 回退，macOS 14+ 使用 ScreenCaptureKit（回退 `screencapture`）。 |
| `vision/ocr` | 门面：`New` 返回操作系统提供的 OCR 引擎；Linux 上由系统安装的 tesseract 承担这个角色。 |
| `vision/ocr/engine` | 引擎契约（`Recognizer`、`Word`、`Options`、`Health`）。 |
| `vision/ocr/winrt`、`vision/ocr/tesseract`、`vision/ocr/visionframework` | 每个包一个引擎：Windows.Media.Ocr、libtesseract（通过 `purego` 进程内调用，或使用 CLI），以及 Apple 的 Vision 框架（文本、矩形、显著图）。 |
| `vision/contour`、`vision/segment`、`vision/pixels` | 纯 Go 检测器，以及它们共享的 `Frame` 类型和像素操作。 |

## 辅助功能（accessibility）

### 创建客户端

`accessibility.New` 为当前平台挑选后端并返回 `ax.Client` 契约，因此调用方不需要提到任何平台包。

```go
client, err := accessibility.New(accessibility.Options{
	Roles:       []element.Role{element.RoleButton, element.RoleLink},
	MaxDepth:    12,
	MaxNodes:    4000,
	SkipHitTest: true, // 仅 macOS
})
defer client.Close()
```

| 选项 | 含义 | 平台 |
| --- | --- | --- |
| `Roles` | 要扫描的中立角色；后端启动前被翻译成该平台的原生名称。 | 全部 |
| `ClickableRoles` | 直接给出的原生角色名，追加在解析结果之后；没有中立等价物时的逃生口。 | 全部 |
| `MaxDepth` | 树遍历深度上限。零值表示后端默认。 | 全部 |
| `MaxNodes` | 一次遍历访问的节点数上限。零值表示后端默认。 | 全部 |
| `SkipHitTest` | 跳过逐元素可见性命中测试，用精度换每次候选一次往返。 | macOS |
| `SmoothSteps` | 光标移动时插入的中间事件数；零值表示瞬时移动。 | macOS |
| `SmoothDelay` | 这些中间事件之间的间隔。 | macOS |
| `DisableCompositor` | 不访问 KWin / GNOME Shell / 合成器 CLI，回退到 AT-SPI 几何。 | Linux |
| `BundleType` | 覆盖用于过滤网页噪声的浏览器引擎检测。 | macOS |

`accessibility.Supported()` 报告当前构建是否有对应后端，`accessibility.Mapper()` 返回它的角色映射器。

### 读取辅助功能树

```go
if !client.CheckPermissions() {
	return fmt.Errorf("未授予辅助功能权限")
}

window, err := client.FrontmostWindow(ctx)
defer window.Release()

// roles 传 nil 表示客户端配置的集合；深度 0 表示默认；
// 最后一个参数 true 表示扫描时一并读取文本与状态。
nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
defer accessibility.ReleaseNodes(nodes)

elements := accessibility.FromNodes(nodes) // 中立角色，文本已拷贝
```

| `ax.Client` 方法 | 用途 |
| --- | --- |
| `FrontmostWindow`、`AllWindows`、`FrontmostAndPopoverWindows` | 窗口发现。 |
| `FocusedApplication`、`ApplicationByBundleID` | 应用发现；`App.Info()` 返回 bundle 元数据。 |
| `ClickableNodes(ctx, root, roles, maxDepth, readState)` | 主扫描：某个根节点下的可点击节点。 |
| `MenuBarClickableElements`、`MissionControlClickableNodes` | macOS 表面。 |
| `ClickableElementsFromBundleID` | 扫描非最前应用。 |
| `ActiveScreenBounds` | 活动显示器，逻辑像素。 |
| `PerformAction`、`Scroll`、`MoveMouse`、`CursorPosition` | 输入。`MoveMouse(p, bypassSmooth)` 可跳过平滑移动。 |
| `CheckPermissions`、`SetClickableRoles`、`ClickableRoles`、`IsMissionControlActive`、`SupportsSupplementaryElements` | 状态。 |
| `Close` | 释放后端。 |

`ax.Window` 是带 `Role()` 的 `ax.Element`；`ax.App` 另外提供 `BundleIdentifier()` 与 `Info()`。`ax.Node` 报告平台自己的角色字符串、边界、标题、描述、值、焦点、标识符与可点击性，并持有一个需要用 `Release()` 释放的平台引用。

### 角色：中立与原生

`element.Role` 是中立的词汇表（`button`、`link`、`text_field` 等）。后端比较的是原生字符串（`AXButton`、`push button`、`Button`），所以门面在遍历开始前完成翻译：

```go
client, err := accessibility.New(accessibility.Options{
	Roles: []element.Role{element.RoleButton, element.RoleLink},
})

// 也可以按调用传列表，并混写中立名与带词汇前缀的原生名：
res := element.ResolveRoleEntries([]string{"button", "link", "ax:AXTextArea"}, "darwin")
nodes, err := client.ClickableNodes(ctx, window, res.Native, 0, false)
```

`ResolveRoleEntries` 会报告拼写错误的条目（致命）、属于其他平台的条目（忽略）、以及此处无对应物的条目（忽略），而不是静默地匹配不到任何结果。当你已经持有 `element.Role` 值时，`ResolveRoles` 是更短的路径；`element.DefaultClickableNativeRoles(vocab)` 返回随库发布的默认可点击集合。

### 辅助表面（macOS）

```go
for _, surface := range []ax.Surface{
	ax.SurfaceDock,
	ax.SurfaceNotificationCenter,
	ax.SurfaceStageManager,
	ax.SurfacePictureInPicture,
	ax.SurfaceScreenCaptureUI,
} {
	nodes, err := accessibility.SurfaceNodes(ctx, client, surface, nil, 0)
	if err != nil {
		continue // 其他平台返回 CodeNotSupported，未运行时返回 CodeElementNotFound
	}
	// ... 使用这些节点，然后释放它们
}
```

没有对应表面的平台会返回 `errs.CodeNotSupported`，因此跨平台调用方可以无条件遍历全部表面。

### 预热

启动时调用一次 `accessibility.WarmUp(ctx, client)`。在 Linux 上它会提前打开 Wayland 输入会话（可能弹出授权对话框）和 uinput 滚轮，避免第一次快捷键时才付出这个代价。没有可准备内容的后端返回 nil。

### 编写后端

实现 `ax.Client` 即可。`Client` 是一个接口，平台能回答的方法写真实现，不能回答的返回 `errs.CodeNotSupported`，而不是假装成功。可选能力是独立接口：`ax.SurfaceProvider` 对应辅助表面，`accessibility.Warmer` 对应输入预热。`accessibility.SurfaceNodes` 与 `accessibility.WarmUp` 通过类型断言访问它们，因此基础契约保持精简。`examples/custom-backend` 是一个完整的最小后端：一个窗口、一个节点，其余返回 `CodeNotSupported`。

## 窗口（window）

`window` 是本模块负责窗口管理的一半：枚举桌面顶层窗口、读取前台窗口、把指定窗口切到前台，以及移动/缩放窗口。所有平台机制都藏在一个契约后面，调用方不需要自己写 Win32 的 `EnumWindows` 回调、macOS 的 AX 窗口遍历或 EWMH 客户端消息。

```go
manager, err := window.New(window.Options{})
if err != nil {
	return err
}
defer manager.Close()

windows, err := manager.List(ctx) // 前台到后台，含最小化窗口
for _, w := range windows {
	fmt.Println(w.ID, w.PID, w.Application, w.Title, w.Bounds, w.Minimized)
}

// 手里有什么就用什么定位：ID、PID、应用名或标题。
activated, err := manager.Activate(ctx, window.Target{Application: "chrome.exe"})

// 或者一次调用完成"切到前台 + 摆到指定矩形"。
placed, err := manager.SetBounds(
	ctx,
	window.Target{Application: "chrome.exe"},
	image.Rect(0, 0, 960, 1440),
)
```

| `window.Manager` 方法 | 用途 |
| --- | --- |
| `List` | 所有可见顶层窗口，前台到后台，含最小化窗口。 |
| `Frontmost` | 用户正在操作的窗口；没有窗口获得焦点时返回 `CodeElementNotFound`。 |
| `Activate` | 把目标窗口切到前台，并在复核切换生效后才报告成功。 |
| `SetBounds` | 还原、移动、缩放并抬起窗口；返回平台实际给出的矩形。 |
| `Close` | 释放后端（X11 的 display 连接）；幂等。 |

`window.Info` 携带 `ID`（平台句柄，对调用方不透明）、`PID`、`Title`、`Application`（Windows/Linux 是可执行文件名，macOS 是 bundle id）、`Bounds`（Windows/X11 为物理像素，macOS 为辅助功能 API 报告的逻辑点；最小化时为空）、`Minimized` 与 `Focused`。

`window.Target` 依次按 `ID`、`PID`、`Application`、`Title` 定位，取第一个非空字段。`Application` 忽略大小写并接受完整可执行文件路径，`Title` 是忽略大小写的子串。多个窗口同时匹配时前台窗口优先，其次是列表里最靠前的那个。`window.Find` 把这条规则应用在任意窗口切片上，因此自带枚举结果的调用方也能得到与后端一致的选中结果。

`SetBounds` 返回平台实际应用的矩形，它可能与请求值不同：有最小尺寸限制的窗口会保留自己的尺寸，平铺式窗口管理器也可能按自己的布局摆放。返回实际值才是诚实的，返回请求值只是猜测。摆放时会顺带请求前台，但是否拿到由 `Info.Focused` 如实报告，而不是把前台失败当成摆放失败。X11 窗口管理器如果始终不应用这个矩形，会返回 `CodeAccessibilityFailed`。

`Options.IncludeUntitled` 保留标题栏为空的窗口；它们通常是输入法候选框、拖拽预览之类的辅助窗口，所以默认丢弃。获得焦点的窗口无论有没有标题都会保留。

后端：Win32 的 `EnumWindows`，加上绕开前台锁的 `AttachThreadInput` 序列，以及补偿 DWM 不可见缩放边框的 `SetWindowPos`；macOS 的辅助功能窗口对象、`NSRunningApplication` 激活与 `AXPosition`/`AXSize`；X11 的 EWMH（`_NET_CLIENT_LIST`、`_NET_ACTIVE_WINDOW`、`_NET_MOVERESIZE_WINDOW`）。Wayland 会话没有可移植的窗口管理协议，因此返回 `CodeNotSupported`，而不是把 XWayland 的不完整列表当成整个桌面。`Activate` 会在报告成功前复核激活结果。

## 元素（element）

`element.Element` 是两半共同产出的中立名词。它不可变、可安全地在 goroutine 间共享，并且不携带任何平台词汇。

```go
elem, err := element.NewElement(
	"save-button",
	image.Rect(120, 80, 220, 120),
	element.RoleButton,
	element.WithTitle("Save"),
	element.WithClickable(true),
	element.WithFocused(true),
	element.WithSource(element.SourceAccessibilityTree),
)
```

| 方法 | 返回 |
| --- | --- |
| `ID`、`Role`、`Bounds`、`Center` | 标识与几何。 |
| `Title`、`Description`、`Value`、`Text` | 文本；`Text` 依次优先取 value、title、description。 |
| `SearchText` | 子树里的可搜索文本，用于提示匹配。 |
| `IsClickable`、`IsFocused`、`Identifier` | 状态。 |
| `Source`、`IsVisionOnly` | 来源；仅视觉元素只能按坐标点击。 |
| `Contains`、`Overlaps`、`IsVisible(screenBounds)` | 几何判断。 |

选项：`WithClickable`、`WithTitle`、`WithDescription`、`WithValue`、`WithFocused`、`WithIdentifier`、`WithSearchText`、`WithSource`。

### 角色词汇表

`element.Role` 的取值包括：`button`、`link`、`text_field`、`static_text`、`image`、`check_box`、`radio_button`、`menu_item`、`menu_button`、`pop_up_button`、`tab_button`、`slider`、`switch`、`disclosure_triangle`、`text_area`、`combo_box`、`popover`、`sheet`、`menu`、`list`、`heading`、`menu_bar_item`、`menu_bar`、`cell`、`row`、`list_item`、`dock_item`、`incrementor`、`color_well`、`search_field`、`toolbar_button`、`toggle`、`table`、`outline`、`application`、`window`、`tab_group`、`group`、`scroll_area`、`split_group`、`generic_element`、`unknown`。

### 映射原生角色

```go
role, ok := element.MapNativeRole(element.AXRoleMapper{}, "AXButton", "")
role, ok = element.MapNativeRole(element.UIARoleMapper{}, "Button", "")
role, ok = element.MapNativeRole(element.ATSPIRoleMapper{}, "push button", "")
```

`element.MapperForGOOS(goos)` 返回某个平台的映射器。映射器是导出的，因为 `accessibility.FromNode` 用的就是它们，而且自带节点类型的程序可以不经门面直接翻译。

### 元素间吸附

```go
target, ok := element.Nearest(elements, image.Pt(500, 400), region)
target, ok = element.Toward(elements, image.Pt(500, 400), region, element.DirectionRight)
```

两者都会把候选裁剪到区域内，并返回 `element.Target{Element, Point}`，其中 `Point` 是点击点（元素中心，必要时裁剪）。`Nearest` 按覆盖度与距离排序；`Toward` 只考虑一个方向。方向为 `DirectionUp`、`DirectionDown`、`DirectionLeft`、`DirectionRight`，`ParseDirection` / `String` 负责与配置字符串互转。

## 动作（action）

```go
client.PerformAction(action.TypeLeftClick, image.Pt(420, 240), true, 0)
client.PerformAction(action.TypeLeftMouseDown, point, false, action.ModShift|action.ModAlt)
client.MoveMouse(point, false) // false 表示不跳过平滑移动
client.Scroll(0, 120, 0)       // deltaX、deltaY、修饰键
```

| 类型 | 含义 |
| --- | --- |
| `TypeLeftClick`、`TypeRightClick`、`TypeMiddleClick` | 按下并释放。 |
| `TypeLeftMouseDown` / `Up` / `Toggle`，右键与中键同理 | 按住、释放、切换。 |
| `TypeMoveMouse`、`TypeMoveMouseRelative` | 绝对与相对移动。 |
| `TypeScroll` | 在当前位置滚动滚轮。 |

`MouseButton` 为 `ButtonLeft`、`ButtonRight`、`ButtonMiddle`；`MousePhase` 为 `PhaseClick`、`PhaseDown`、`PhaseUp`；`Type.MouseButtonPhase()` 可拆解一个类型。`ParseType`、`ParsePhase`、`ParseModifiers` 接受配置里的写法（`"left_click"`、`"down"`、`"cmd+shift"`）。

`Modifiers` 是 `ModCmd`、`ModShift`、`ModAlt`、`ModCtrl` 的位掩码。`PrimaryModifier()` 返回平台的主修饰键（macOS 是 Command，其余是 Control），`UsePrimaryModifier` 可改变它的含义。`Scroll` 接收同样的掩码；所有平台上正的 `deltaY` 向上滚动、正的 `deltaX` 向左滚动。

## 错误（errs）

```go
err := errs.New(errs.CodeNotSupported, "no OCR engine is available")
err = errs.Wrapf(cause, errs.CodeTimeout, "reading %v", region)
err = errs.New(errs.CodeAccessibilityDenied, "permission denied").
	WithContext("remedy", "系统设置 → 隐私与安全性 → 辅助功能")
```

| 错误码 | 含义 |
| --- | --- |
| `CodeNotSupported` | 本机没有该能力；消息里给出补救办法。 |
| `CodeAccessibilityDenied` | 权限或授权被拒绝。 |
| `CodeAccessibilityFailed` | 平台 API 失败。 |
| `CodeElementNotFound` | 窗口、应用或表面没有运行。 |
| `CodeInvalidInput` | 调用方传入了不可用的值（空区域、nil 识别器）。 |
| `CodeContextCanceled` | 调用方的 context 被取消。 |
| `CodeTimeout` | 截止时间到期。 |
| `CodeInternal` | 不变量被破坏；调用方无法处理。 |

`errs.IsCode` 与 `errs.GetCode` 只看最外层错误；`errors.Is` 会沿链遍历并按错误码比较，因此被包装的错误仍能匹配到原因。`errs.IsNotSupported(err)` 是最常用的降级判断。`errs.Message` 去掉错误码前缀，便于日志。不是 `*errs.Error` 的普通 error 会被当作 `CodeInternal`。

## 视觉（vision）

### 帧与捕获

一次捕获就是一个 `pixels.Frame`：

| 字段 | 含义 |
| --- | --- |
| `Img` | RGBA 像素，矩形锚定在 (0,0)。 |
| `Origin` | 帧左上角在桌面上的位置，逻辑像素。 |
| `Region` | 帧覆盖的桌面逻辑区域。 |
| `Scale` | 每逻辑像素对应的帧像素数（显示器 DPI 缩放）。 |

```go
frame, err := capture.CaptureScreen(ctx)          // 活动显示器
frame, err = capture.CaptureRect(ctx, image.Rect(0, 0, 800, 600))
```

同一个值可以直接交给检测器、`ReadText` 或 `Pipeline`，中间没有转换步骤。

### 检测器

```go
buttons, err := vision.DetectContours(ctx, frame, contour.DefaultParams())
words, err := vision.DetectSegments(ctx, frame, segment.TargetsWords)
targets, err := vision.Detect(ctx, frame, vision.StrategyContour, "", contour.DefaultParams())
```

`contour.Params` 调整灰度/模糊/Sobel/Canny 阈值、目标尺寸上下限、容器高度、中心松弛与方形图标松弛。零值会过滤掉一切，请从 `contour.DefaultParams()` 起步。`segment` 的目标是 `TargetsWords`、`TargetsPhrases` 与 `TargetsControls`。

### Pipeline

`Pipeline` 就是 `vision` 策略：OCR 加矩形检测，再合并与分类。

| 字段 | 含义 |
| --- | --- |
| `Text` | 识别器；`DetectText` 为真时必填。 |
| `Boxes` | 可选的原生矩形检测器；nil 表示纯 Go 轮廓检测器。 |
| `DetectText`、`DetectRectangles` | 运行哪一半；至少一个。 |
| `WordLevel` | 每个词一个目标，而不是每行一个。 |
| `MinConfidence` | 丢弃低于该分数的观测。 |
| `MergeIOU` | 非极大值抑制阈值；零值表示 `DefaultMergeIOU`。 |
| `Timeout` | 一次 `Detect` 的总预算；零值表示 `DefaultVisionTimeout`。 |
| `Rectangle` | `RectangleOptions{MinSize, MinAspect, MaxAspect, MaxCandidates}`。 |
| `Params` | `Boxes` 为 nil 时的轮廓阈值。 |
| `Classifier` | 角色分配；nil 表示基线分类器。 |

`Pipeline.Health(ctx)` 在不截图的情况下探测识别器。`Detect` 返回带 `SourceVision` 的元素；两半都没启用时以 `CodeInvalidInput` 拒绝。

### 读取文字

```go
text, err := vision.ReadText(ctx, frame, recognizer, nil) // nil 表示引擎默认

opts := &engine.Options{
	Languages:     []string{"en-US", "ja"},
	WordLevel:     true,
	MinConfidence: 0.4,
}
text, err = vision.ReadText(ctx, frame, recognizer, opts)
```

`ReadText` 用单个空格拼接引擎返回的词，返回一个字符串。它会在读取像素之前先探测 `Health`，因此缺少引擎时不会白截一张图。

### 从词到元素

```go
regions := vision.RegionsFromWords(words, frame.Region, frame.Img.Rect, 0.4)
elements, _ := vision.ElementsFromRegions(regions, vision.NewClassifier(vision.ClassifierOptions{}))
```

`RegionsFromWords` 利用帧的区域与图像尺寸，把引擎的图像空间方框映射回桌面。`MergeRegions` 合并重叠，`SortRegionsByScore` 排序，`ElementsFromRegions` 分配中立角色。`Classifier` 是首轮的几何与文本启发式；`NewClassifier` 接受阈值覆盖，零值等价于随库发布的基线。

### 像素工具

`pixels.Frame` 还带着检测器共享的操作：`Crop(frame, region)` 取出帧内某个逻辑矩形（并调整原点），`DeriveScale(imageWidth, logicalWidth)` 从渲染结果推回 DPI 缩放，`Downsample(img, factor)` 与 `Resample(pix, stride, w, h, order, factor)` 对高密度帧做块平均，`Rows(height, work)` 把帧按核数切开。

## OCR

### 门面

```go
recognizer, err := ocr.New()
```

`vision/ocr.New` 返回操作系统提供的引擎：Windows 上是 Windows.Media.Ocr，macOS 上是 Apple 的 Vision 框架，Linux 上是 libtesseract（或 `tesseract` 可执行文件）。它返回 `engine.Recognizer`，因此可以替代任何显式引擎。

### 契约

```go
type Recognizer interface {
	Recognize(ctx context.Context, img *image.RGBA, opts Options) ([]Word, error)
}

type Word struct {
	Text       string
	Bounds     image.Rectangle
	Confidence float64
}

type Options struct {
	Languages     []string
	WordLevel     bool
	MinConfidence float64
}
```

`Word.Bounds` 位于图像自身的像素空间，左上角为 (0,0)。`engine.Health(ctx, r)` 会探测可选的 `HealthChecker`；没有实现它的识别器被视为可用。

### 引擎

| 包 | 引擎 | 选项 |
| --- | --- | --- |
| `ocr/winrt` | Windows.Media.Ocr | `Options{Languages []string}` |
| `ocr/tesseract` | 进程内 libtesseract，或 CLI | `LibraryOptions{DataPath, Language, PageSegMode}` / `Options{Binary, PageSegMode, Languages}` |
| `ocr/visionframework` | Apple 的 Vision 框架 | `Options{Languages []string, Accurate bool}` |

```go
r, err := winrt.New(winrt.Options{Languages: []string{"en-US"}})
r, err = tesseract.NewLibrary(tesseract.LibraryOptions{Language: "eng"})
r, err = tesseract.NewCLI(tesseract.Options{Binary: "tesseract"})
r, err = visionframework.New(visionframework.Options{Accurate: true})
```

winrt、visionframework 与 tesseract 的库模式识别器持有原生资源，提供 `Close`；tesseract CLI 识别器没有需要释放的东西。机器缺少引擎或语言包时，`New`、`Health` 或 `Recognize` 会返回 `CodeNotSupported`，消息里给出安装步骤。

### 自定义引擎

```go
type fixed struct{}

func (fixed) Recognize(_ context.Context, _ *image.RGBA, _ engine.Options) ([]engine.Word, error) {
	return []engine.Word{
		{Text: "Save", Bounds: image.Rect(10, 10, 60, 30), Confidence: 0.99},
	}, nil
}

text, err := vision.ReadText(ctx, frame, fixed{}, nil)
```

只需要实现 `Recognize`。`vision` 与 `Pipeline` 依赖这个接口、从不依赖引擎包，所以导入 `vision` 不会链接任何引擎。

## 配方

### 点击文本为 “Save” 的元素

```go
nodes, _ := client.ClickableNodes(ctx, window, nil, 0, true)
defer accessibility.ReleaseNodes(nodes)

for _, elem := range accessibility.FromNodes(nodes) {
	if strings.Contains(elem.Text(), "Save") {
		return client.PerformAction(action.TypeLeftClick, elem.Center(), true, 0)
	}
}
```

### 读取某个区域的文字

```go
frame, err := capture.CaptureRect(ctx, image.Rect(0, 0, 800, 600))
text, err := vision.ReadText(ctx, frame, recognizer, nil)
```

### 辅助功能树为空时找目标

```go
frame, err := capture.CaptureScreen(ctx)
targets, err := vision.DetectSegments(ctx, frame, segment.TargetsControls)

for _, el := range targets {
	client.PerformAction(action.TypeLeftClick, el.Center(), true, 0)
}
```

### 先辅助功能，失败再回退像素

```go
nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
if err != nil || len(nodes) == 0 {
	frame, _ := capture.CaptureScreen(ctx)
	els, _ := vision.DetectContours(ctx, frame, contour.DefaultParams())
	// ... 按坐标点击 els
}
```

### 有意识地降级

```go
if errs.IsNotSupported(err) {
	// 本机没有该能力；换一条路或跳过这个功能。
}
```

所有平台缺口都报 `CodeNotSupported`，没有任何一处会用空结果假装成功。

## 示例

`go run ./examples/<name>` 即可运行。离线示例（`elements`、`errors`、`pixels`、`custom-engine`、`compositor`）不需要桌面。

| 示例 | 展示 |
| --- | --- |
| `accessibility` | 客户端建立、最前窗口、可点击扫描、节点释放。 |
| `hints` | 一个 hints 模式的扫描，只打印角色与几何，不打印文本。 |
| `roles` | 中立/原生角色解析、诊断信息、词汇表。 |
| `elements` | 元素模型、选项、`FromNode`、`Nearest`/`Toward`。 |
| `actions` | 点击、移动、滚动、拖拽与修饰键掩码。 |
| `errors` | 错误的创建、包装、匹配与检查。 |
| `capture` | 整屏与区域捕获；frame 字段。 |
| `pixels` | `Frame`、`Crop`、`DeriveScale`、降采样/重采样/并行行。 |
| `vision` | 在真实屏幕上做轮廓与分割检测。 |
| `ocr` | OCR 门面、`ReadText` 与完整的 `Pipeline`。 |
| `ocr-engines` | 按平台显式选择 winrt / visionframework / tesseract。 |
| `custom-engine` | 不依赖任何原生引擎，实现 `engine.Recognizer`。 |
| `custom-backend` | 实现 `ax.Client`；门面对它照常工作。 |
| `windows` | 窗口枚举、前台窗口、按标题激活与摆放。 |
| `compositor` | Wayland 窗口原点的换算规则。 |

## 平台支持

| 能力 | macOS | Linux | Windows |
| --- | --- | --- | --- |
| 读取辅助功能树 | AX（ApplicationServices） | AT-SPI（D-Bus） | UI Automation（COM） |
| 可点击节点扫描 | 支持 | 支持 | 支持 |
| 最前窗口 / 应用 | 支持 | 支持，优先使用合成器 | 支持 |
| 窗口枚举 / 激活 | AX 窗口 + `NSRunningApplication` | X11 上的 EWMH | `EnumWindows` + `SetForegroundWindow` |
| 窗口移动 / 缩放 | `AXPosition` / `AXSize` | `_NET_MOVERESIZE_WINDOW` | `SetWindowPos`（补偿 DWM 边框） |
| 菜单栏扫描 | 支持 | — | — |
| Dock / 通知中心 / 台前调度 / 画中画 / 录屏 UI | 支持（`SurfaceNodes`） | — | — |
| Mission Control 窗口与 Spaces 条 | 支持 | — | — |
| 鼠标点击 / 移动 / 滚动 | CGEvent | XTest（X11）、libei（Wayland）、uinput 滚轮 | `SendInput` |
| 平滑光标移动 | 支持（`SmoothSteps`） | — | — |
| 屏幕捕获 | ScreenCaptureKit（macOS 14+），回退 `screencapture` | X11 root、wlr-screencopy（Wayland） | GDI `BitBlt` |
| 文本识别 | Vision 框架 | libtesseract / tesseract CLI | Windows.Media.Ocr |
| 辅助功能权限门禁 | `AXIsProcessTrusted` | 无 | 无 |

不支持的表面会返回 `errs.CodeNotSupported`；它们绝不会用空结果假装成功。

已知限制，由后端自己声明：

- **macOS。** Neru 扫描的每个表面这里都能扫：最前窗口、菜单栏、Dock、通知中心、台前调度、画中画、录屏 UI 与 Mission Control。捕获在 macOS 14 及以上使用 ScreenCaptureKit，速度快且会排除调用方自己的窗口；失败时回退到 `screencapture` 工具，较慢且会包含自己的窗口。辅助功能缩放校正使用同一套公开 API，加上 Neru 使用的私有 SkyLight 查询。窗口枚举、激活与摆放读取 AX 窗口对象，因此需要辅助功能权限，未授权时返回 `CodeAccessibilityDenied`；最小化窗口仍会被列出，其 `Bounds` 为空。激活先通过 `NSRunningApplication` 唤起应用，再用 `AXRaise` 抬起匹配到的窗口。`SetBounds` 写 `AXPosition` 与 `AXSize`；全屏窗口会拒绝这种写入，此时如实报错而不是反复重试。
- **Linux。** X11 上的注入是 XTest；Wayland 上，合成器暴露 wlroots 虚拟指针时使用它（sway、niri、Hyprland、wayfire），否则通过 RemoteDesktop portal 使用 libei（KDE、Mutter、cosmic）。两者都不可用的会话返回 `CodeNotSupported`。当 `/dev/uinput` 可写时，uinput 滚轮可复现像素级精确的平滑滚动。捕获读取 X11 root、wlroots Wayland 会话中合成器的 wlr-screencopy 协议、KDE 上 KWin 的 ScreenShot2 接口（无提示、带元数据几何），或在其发送方白名单允许时使用 GNOME Shell 的 Screenshot 接口；否则回退到桌面 portal 的 `Screenshot` 接口（较慢，可能提示一次）。焦点与窗口原点来自 KWin、GNOME Shell、niri、sway、Hyprland 以及（通过可注入的几何来源）COSMIC 的合成器桥接；X11 仍从 AT-SPI 推断焦点，因此 Chromium 和 Electron 偶尔可能解析为后台窗口。第一次树调用会启用 `org.a11y.Status`，`Close` 会恢复会话之前的值。窗口管理只在 X11 上通过 EWMH 提供：Wayland 会话返回 `CodeNotSupported`，而不是把 XWayland 的不完整客户端列表当成答案；窗口的 `Bounds` 是客户区，不含窗口管理器绘制的装饰。`SetBounds` 发送的是 `_NET_MOVERESIZE_WINDOW` 请求，平铺/吸附式窗口管理器可能按自己的矩形摆放；管理器始终不应用请求时返回 `CodeAccessibilityFailed`。
- **Windows。** 没有菜单栏或 Mission Control 的对应物。点击是坐标注入而非 UIA Invoke，因此其行为像真实鼠标。每次查询都会创建并释放自己的 UIA client 对象，因此没有 COM 对象会在 goroutine 之间共享。`capture.ExcludeOwnWindows` 使用 `WDA_EXCLUDEFROMCAPTURE`（Windows 10 2004+）标记进程的顶层窗口，使提示浮层不会出现在其检测的帧中。窗口枚举一次 `EnumWindows` 走完，跳过被 DWM cloaked 的窗口（UWP 幽灵窗口），位置取 DWM 的扩展边框而不是不可见的缩放边框。激活先还原最小化窗口，再通过 `AttachThreadInput` 加入前台线程的输入队列以绕开前台锁；位于其他虚拟桌面的窗口仍会被系统拒绝，此时如实报错而不是谎报成功。无法打开进程的窗口（如提权应用）会被列出，但 `Application` 为空。`SetBounds` 给窗口的是调用方点名的矩形，而不是往里缩几个像素：`SetWindowPos` 操作的是窗口矩形，所以后端会先把 DWM 不可见的缩放边框补偿回去。

## 设计说明

**分层。** 每条纵切都是 门面 → 后端 → 契约。`accessibility` 选择后端，`accessibility/ax` 是这些后端实现的契约；`vision/ocr` 选择引擎，`vision/ocr/engine` 是契约，引擎与它并列。领域包（`vision`、`element`）只导入契约、从不导入后端，因此导入它们不会链接任何平台代码，门面是唯一做平台选择的地方。

**无 CGo。** 每个原生能力都在运行时获取而不是链接进来：macOS 上的 CoreFoundation、ApplicationServices、CoreGraphics 与 Objective-C 运行时；Linux 上的 D-Bus 上的 AT-SPI、X11/XTest、Wayland 线协议、libei 与 `/dev/uinput`；Windows 上标准库的 `syscall`/COM 接口。文本识别遵循同样的规则：Windows.Media.Ocr 通过 WinRT，libtesseract 通过 `purego`，Apple 的 Vision 框架通过 `purego` 与 `objc_msgSend`。内部 `native` 包是唯一加载符号的地方，且它是 internal，原始函数指针不会泄漏到公开 API。

**角色在边缘是原生的，在模型里是中立的。** 后端的 `Node` 报告平台的角色名（`AXButton`、`push button`、`Button`），因为过滤比较的就是它，诊断也可能需要它。`accessibility.FromNode` 通过平台映射器把它翻译成 `element.Role`。映射器是导出的，因此自带节点类型的程序可以不经这个包完成翻译。

**所有权。** `Window`、`App` 与 `Node` 持有一个平台引用，必须恰好释放一次（`Release`）。`FromNode` 会拷贝它需要的一切，因此调用方可以在转换后立即释放节点。元素不可变，可安全地在 goroutine 间共享。

**延迟。** macOS 的遍历把元素的属性批量合并成一次辅助功能调用，在返回前读取文本，并对每个候选做窗口服务器命中测试，因此被遮挡的控件不会被提示。`SkipHitTest` 用这份精度换每个候选一次往返。`MaxDepth` 与 `MaxNodes` 约束病态的树。

**一套窗口契约，三种机制。** `window` 把平台词汇挡在 API 之外：没有句柄类型、没有 XID、没有 AXUIElement，只有不透明的 `Info.ID`。三个窗口后端的差异比辅助功能后端更大——Win32 用一次回调遍历整个桌面，还要靠线程挂接夺取前台，并在 `SetWindowPos` 前补偿 DWM 的不可见边框；macOS 逐个应用读取 AX 窗口，通过 AppKit 激活，再写 `AXPosition`/`AXSize`；X11 读一条 EWMH 属性，再发 `_NET_ACTIVE_WINDOW` 或 `_NET_MOVERESIZE_WINDOW` 客户端消息——正因如此，这些都不该成为调用方的问题。

## 开发

```bash
go test ./...
go vet ./...

# 从任意宿主机交叉编译，无需 C 工具链：
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=linux   GOARCH=arm64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

驱动真实桌面的集成测试带有 `integration && <os>` 标签，不属于 `go test ./...`。逐平台的检查清单见
[`docs/LIVE-VERIFICATION_zh.md`](./docs/LIVE-VERIFICATION_zh.md)。

## 许可证

MIT，与 Neru 相同，本模块从该项目抽取。见 `LICENSE`。
