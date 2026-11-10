# 与 Neru 的对齐情况（Parity with Neru）

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./PARITY.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./PARITY_zh.md)

本模块是 Neru 的辅助功能与视觉部分，被抽取出来以便其他 Go 程序使用。本文件按特性逐项跟踪：Neru 做了什么、这里由哪个包负责、以及如何验证。它是这次抽取的工作清单：只有当代码存在、测试或实机检查通过、且该包文档中声明了相应限制（若有）时，该项才算是“完成”。

状态取值：**done（已完成）**（已实现并验证）、**partial（部分完成）**（已实现但存在文档化的差距）、**planned（已计划）**（已跟踪、尚未实现）。

关于验证的说明：本次抽取是在 Windows 宿主机上构建的。Windows 行为（含 OCR）可以在这里实机运行并已验证；Linux 与 macOS 代码则进行了两种架构的交叉编译与审查，其纯逻辑部分有单元测试，但它们的实机路径需要在目标操作系统上各运行一次，才能称之为已验证。下面标记为 **done** 的条目表示“已实现且通过编译/逻辑验证”；两个例外是 Windows 运行时路径与可在宿主机上运行的视觉测试。

## 辅助功能 — macOS

| Neru 特性 | 包 | 状态 | 验证 |
| --- | --- | --- | --- |
| AX 树读取、角色/状态/文本批量获取 | `accessibility/darwin` | done | darwin 构建/vet、单元测试通过编译 |
| 带命中测试可见性的可点击扫描 | `accessibility/darwin` | done | 同上 |
| 最前窗口/应用、全部窗口 | `accessibility/darwin` | done | 同上 |
| 菜单栏扫描 | `accessibility/darwin` | done | 同上 |
| Dock 项作为提示目标 | `accessibility/darwin`（`SurfaceNodes`） | done | 同上 |
| 通知中心目标 | `accessibility/darwin` | done | 同上 |
| 台前调度（Stage Manager）目标 | `accessibility/darwin` | done | 同上 |
| 画中画（Picture-in-Picture）目标 | `accessibility/darwin` | done | 同上 |
| 录屏 UI 目标 | `accessibility/darwin` | done | 同上 |
| Mission Control 窗口 + Spaces 条 | `accessibility/darwin` | done | 同上 |
| 用于树过滤的浏览器引擎检测 | `accessibility/darwin`（`DetectBundleType`） | done | 单元测试通过编译 |
| 辅助功能缩放视口校正 | `accessibility/darwin`（`ZoomViewport`） | done | 同上 |
| 平滑光标移动、拖动事件类型 | `accessibility/darwin`（`Options.SmoothSteps`） | done | 同上 |
| 全局鼠标按键状态、确保鼠标抬起 | `accessibility/darwin`（`MouseButtonDown`、`EnsureMouseUp`） | done | 同上 |
| 安全文本字段值置空 | `accessibility/darwin` | done | 同上 |

## 辅助功能 — Linux

| Neru 特性 | 包 | 状态 | 验证 |
| --- | --- | --- | --- |
| AT-SPI 树读取、角色、状态、范围 | `accessibility/linux` | done | linux 构建/vet、单元测试 |
| 焦点 + 原点：KWin | `accessibility/linux/compositor` | done | linux 构建/vet、可移植测试 |
| 焦点 + 原点：GNOME Shell 扩展 | `accessibility/linux/compositor` | done | 同上 |
| 焦点 + 原点：niri / sway / Hyprland | `accessibility/linux/compositor` | done | 同上 |
| 焦点 + 原点：COSMIC（原点通过可注入几何来源） | `accessibility/linux/compositor` | partial | 同上；焦点会回退 |
| X11/XTest 指针与滚轮注入 | `accessibility/linux` | done | linux 构建/vet |
| libei 注入（KDE、Mutter、cosmic） | `internal/libei` | done | linux 构建/vet、可移植测试 |
| wlroots 虚拟指针注入（sway、niri、Hyprland） | `internal/wlrinject` | done | linux 构建/vet、可移植测试 |
| uinput 平滑滚动 | `internal/uinput` | done | linux 构建/vet、宿主机测试 |
| Chromium/Electron 窗口的 X11 焦点 | — | 与 Neru 相同 | Neru 也是从 AT-SPI 推断 X11 焦点；合成器桥接仅限 Wayland |

## 辅助功能 — Windows

| Neru 特性 | 包 | 状态 | 验证 |
| --- | --- | --- | --- |
| UIA 树读取、控件类型、模式 | `accessibility/windows` | done | windows 测试、实机冒烟 |
| 窗口/应用枚举、DPI、虚拟屏幕 | `accessibility/windows` | done | 同上 |
| SendInput 点击/拖动/滚动 | `accessibility/windows` | done | 单元测试 |

## 视觉（Vision）

| Neru 特性 | 包 | 状态 | 验证 |
| --- | --- | --- | --- |
| 轮廓检测器 | `vision/contour` | done | 宿主机测试 |
| 分割检测器 | `vision/segment` | done | 宿主机测试 |
| 几何分类器、区域合并（NMS） | `vision` | done | 宿主机测试 |
| OCR 单词到桌面区域 | `vision.RegionsFromWords` | done | 宿主机测试 |
| Windows.Media.Ocr 识别器 | `vision/ocr/winrt` | done | **在 Windows 上实机验证** |
| tesseract 识别器（通过 purego 进程内调用 libtesseract） | `vision/ocr/tesseract` | done | 宿主机测试（此处未安装该库） |
| tesseract CLI 识别器 | `vision/ocr/tesseract`（`NewCLI`） | done | 宿主机测试 |
| Vision 框架 OCR | `vision/ocr/visionframework` | done | darwin 构建/vet、单元测试通过编译 |
| Vision 框架矩形检测 | `vision/ocr/visionframework` | done | 同上 |
| Vision 框架显著图 | `vision/ocr/visionframework` | done | 同上 |
| 矩形过滤（尺寸/宽高比/数量） | `vision` | done | 宿主机测试 |
| `vision` 策略管道（文本 + 矩形 + 合并） | `vision.Pipeline` | done | 宿主机测试 |
| 拆分单词的文本目标 | `vision.Pipeline.WordLevel` | done | 宿主机测试 |
| 从帧中读取文本 | `vision.ReadText` | done | 宿主机测试 |
| 屏幕捕获：macOS ScreenCaptureKit（14+）并排除自身窗口、`screencapture` 回退 | `vision/capture` | done | darwin 构建/vet、单元测试通过编译 |
| 屏幕捕获：GDI / X11 / screencapture | `vision/capture` | done | 宿主机测试、交叉编译 |
| 屏幕捕获：wlr-screencopy（Wayland） | `vision/capture/wayland` | done | linux 构建/vet、可移植测试 |
| 屏幕捕获：KDE/GNOME portal 回退 | `vision/capture/portal` | done | linux 构建/vet、可移植测试；需要一个实机会话 |
| 屏幕捕获：KDE KWin ScreenShot2（原生、无提示） | `vision/capture/kwin` | done | linux 构建/vet、可移植测试 |
| 屏幕捕获：GNOME Shell（原生；通常被拒绝，回退到 portal） | `vision/capture/gnome` | partial | linux 构建/vet、可移植测试；GNOME 实机必须使用 portal |
| 屏幕捕获：KDE portal + PipeWire（原生流） | — | 不需要 | KWin ScreenShot2 无需 PipeWire 即可返回相同帧 |
| 捕获时排除自身窗口 | `vision/capture` | done | Windows 的 `ExcludeOwnWindows` 已宿主机测试；macOS 隐含在 ScreenCaptureKit 路径中；Linux 无对应物 |

## 剩余工作

1. **在目标操作系统上的实机运行。** 每个特性均已实现、针对所有目标平台进行了编译与审查，逻辑也有单元测试；macOS 专用路径与 Linux 专用路径仍需按照 `docs/LIVE-VERIFICATION.md` 各进行一次手动运行，才能称得上端到端验证通过。
2. **Chromium/Electron 的 X11 焦点**与 Neru 的行为一致：其合成器桥接仅限 Wayland，X11 焦点来自 AT-SPI 的 ACTIVE/SHOWING 排名。这一点已在 `accessibility/linux` 中说明，并非抽取差距。

## 库的形态

每个能力都放在能够承载它的最小包中，因此导入方只为自己用到的部分付费：

| 包 | 导入成本 |
| --- | --- |
| `accessibility` | 平台选择器；会拉取该平台的后端 |
| `accessibility/ax` | 仅契约 |
| `accessibility/{darwin,linux,windows}` | 单平台的原生绑定 |
| `accessibility/linux/compositor` | D-Bus/CLI 合成器桥接（仅 Linux） |
| `element`、`action`、`errs` | 纯 Go，无原生绑定 |
| `vision`、`vision/contour`、`vision/segment`、`vision/frame` | 纯 Go 检测器与管道 |
| `vision/capture` | 平台捕获；`vision/capture/wayland` 仅限 Linux |
| `vision/ocr` | 仅契约 |
| `vision/ocr/{winrt,tesseract,visionframework}` | 各一个识别器引擎 |
| `vision/ocr/auto` | 在运行时选择平台的引擎 |
| `internal/*` | 实现细节，模块外不可导入 |

## 验证命令

```bash
GOCACHE=<writable> go test ./...
GOCACHE=<writable> go vet ./...
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  GOOS=... GOARCH=... go build ./... && go vet ./...
done
```

涉及读取或操作真实桌面的集成测试带有 `integration && <os>` 标签。