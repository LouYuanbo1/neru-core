# 在真实桌面上的实测（Live verification on a real desktop）

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./LIVE-VERIFICATION.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./LIVE-VERIFICATION_zh.md)

Windows、Linux 与 macOS 后端无法在单台机器上全部跑通，而这里的 CI 仅对非宿主平台做交叉编译。本文件是在每个目标平台上运行的检查清单。每一步都是一条 `go test` 命令或一个简短程序；都不需要 `neru` 守护进程。

集成测试带有 `integration && <os>` 标签，当桌面缺少前置条件时会自行跳过，因此完整运行是安全且自描述的。

## macOS

```bash
# AX 树、补充表面、Mission Control、缩放、鼠标状态
go test -tags=integration ./accessibility/darwin/
# Vision 框架 OCR、矩形检测、显著图
go test -tags=integration ./vision/ocr/visionframework/
# ScreenCaptureKit 捕获（失败时回退到 screencapture）
go test -tags=integration ./vision/capture/
```

手动检查：

1. 为测试二进制文件（运行 `go test` 的终端）授予辅助功能与屏幕录制权限。
2. `go run ./examples/hints` —— 最前窗口应列出可点击元素；角色名应为 `button`、`link`、`text_field` 等。
3. 打开 Mission Control：`MissionControlClickableNodes(ctx, 0)` 应返回当前桌面的窗口，并在展开时返回 Spaces 条。
4. 打开 Dock 与通知中心：它们各自的 `SurfaceNodes` 都应返回相应项。
5. 开启辅助功能缩放：`WarmUp` 之后的点击仍应落在请求的点上（视口会先进行平移）。
6. Vision OCR：用一张包含已知文本的截图运行 `go test -tags=integration ./vision/ocr/visionframework/`。
7. ScreenCaptureKit：在 macOS 14+ 上运行 `go test -tags=integration -run SCK -v ./vision/capture/`；预期 Retina 缩放为 2、`Region` 等于逻辑显示边界、且测试进程自身的窗口不出现在图像中。在 macOS 13 或更旧版本上，快速路径会报告 `CodeNotSupported`，`screencapture` 回退仍必须返回一张截图。

已知的非目标：`screencapture` 会包含调用方自身的窗口（尚无 ScreenCaptureKit 内容过滤器），且私有 SkyLight 缩放符号可能随 macOS 版本变化（后端会退化为普通定位）。

## Linux — wlroots 会话（sway、niri、Hyprland、wayfire）

```bash
go test -tags=integration ./accessibility/linux/ ./accessibility/linux/compositor/
go test -tags=integration ./internal/wayland/ ./vision/capture/wayland/
go test -tags=integration ./internal/wlrinject/
go test -tags=integration ./internal/uinput/
```

手动检查：

1. 在 wlroots 会话中运行 `go run ./examples/hints`：设置了 `WAYLAND_DISPLAY`，最前窗口应列出节点，焦点应跟随合成器（此处不涉及 KWin/GNOME 桥接）。
2. `PerformAction(TypeLeftClick, …)` 应能点击原生 Wayland 客户端（而非仅 XWayland）。这是 wlrinject 路径。
3. `Scroll(0, 120, 0)` 应能移动可滚动视图；当 `/dev/uinput` 可写时使用高分辨率滚轮。
4. `capture.CaptureScreen` 应返回覆盖输出联合区域的帧，且 `Scale` 与输出缩放一致。

## Linux — KDE 或 GNOME Wayland 会话

```bash
go test -tags=integration ./accessibility/linux/ ./accessibility/linux/compositor/
go test -tags=integration ./vision/capture/kwin/     # KDE
go test -tags=integration ./vision/capture/gnome/    # GNOME（AccessDenied 时可能跳过）
go test -tags=integration ./vision/capture/portal/   # 回退
```

手动检查：

1. 焦点与窗口原点：GTK 4 窗口的提示应落在控件上，而非屏幕原点（`compositor.PlacementFor`）。
2. 注入：点击原生 Wayland 窗口应通过 libei（KDE/Mutter）生效，首次使用时 libei 会显示 RemoteDesktop 授权提示。
3. 在 KDE 上捕获：`capture.CaptureScreen` 应使用 KWin 的 ScreenShot2、无权限提示，并报告元数据缩放；若 KWin 5.27 拒绝调用方（桌面文件授权），portal 回退应提示一次。
4. 在 GNOME 上捕获：原生 Screenshot 接口通常拒绝普通应用；捕获必须回退到 portal 而非失败。
5. `org.a11y.Status` 恢复：`Close` 之后，辅助技术标志应恢复为第一次读取树之前的值。

## Windows

```bash
go test -tags=integration ./accessibility/windows/
go test -tags=integration ./vision/ocr/winrt/
```

手动检查：

1. 以中等完整性级别运行（正常的交互式桌面，而非此处的沙箱）。在低完整性下，UIPI 会阻止跨进程 UIA 与 SendInput；测试会表现为“空”而非失败，这是环境所致，而非后端问题。
2. 在记事本、设置（WinUI）与 Chrome 上运行 `go run ./examples/hints`，应列出按钮/编辑框/列表项。
3. `capture.ExcludeOwnWindows()` 之后，在存在可见浮层的情况下捕获，应省略该浮层（Windows 10 2004+）。
4. OCR：在装有英文语言包时，`winrt.New().Health(ctx)` 应为 nil；包含已知文本的帧应返回该文本，并附带图像空间中的框坐标。

## 失败意味着什么

- `CodeNotSupported`：本机缺少该设施（合成器协议、portal、OCR 语言包、`/dev/uinput`、libei）。错误信息会说明解决办法。
- `CodeAccessibilityDenied`：某个授权或许可被拒绝（TCC、portal 对话框、XDG 会话）。在重试会再次弹窗的地方不会自动重试。
- 其他任何情况：真实失败；对应包会说明其返回的错误码。