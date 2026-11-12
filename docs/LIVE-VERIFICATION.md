# Live verification on a real desktop

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./LIVE-VERIFICATION.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./LIVE-VERIFICATION_zh.md)

The Windows, Linux and macOS backends cannot all be exercised on one machine,
and CI here only cross-compiles the non-host platforms. This is the checklist to
run on each target. Every step is a `go test` command or a short program; none
needs the `neru` daemon.

Integration tests are tagged `integration && <os>` and skip themselves when the
desktop lacks the prerequisite, so a full run is safe and self-describing.

## macOS

```bash
# AX tree, supplementary surfaces, Mission Control, zoom, mouse state
go test -tags=integration ./accessibility/darwin/
# Vision framework OCR, rectangles, saliency
go test -tags=integration ./vision/ocr/visionframework/
# ScreenCaptureKit capture (falls back to screencapture)
go test -tags=integration ./vision/capture/
```

Manual checks:

1. Grant Accessibility and Screen Recording to the test binary (the terminal
   running `go test`).
2. `go run ./examples/hints` — the frontmost window should list clickable
   elements; role names should be `button`, `link`, `text_field`, …
3. Open Mission Control: `MissionControlClickableNodes(ctx, 0)` should return
   the current desktop's windows plus, when expanded, the Spaces bar.
4. Open the Dock and Notification Center: `SurfaceNodes` for each should return
   items.
5. Turn on Accessibility Zoom: a click after `WarmUp` should still land on the
   requested point (the viewport is panned first).
6. Vision OCR: run `go test -tags=integration ./vision/ocr/visionframework/`
   with a screenshot containing known text.
7. ScreenCaptureKit: run `go test -tags=integration -run SCK -v ./vision/capture/`
   on macOS 14+; expect a Retina scale of 2, `Region` equal to the logical
   display bounds, and the test process's own windows absent from the image.
   On macOS 13 or older the fast path reports `CodeNotSupported` and the
   `screencapture` fallback must still return a frame.
8. Windows: `go run ./examples/windows` should list every application's windows
   (minimized ones included, with empty bounds),
   `go run ./examples/windows -title Settings` should bring the matching window
   forward, and adding `-rect 0,0,960,900` should move and resize it. A
   full-screen window refuses the AX writes, and that refusal is reported rather
   than hidden. Without Accessibility permission, expect
   `CodeAccessibilityDenied` rather than an empty list.

Known non-goals: `screencapture` includes the caller's own windows (no
ScreenCaptureKit content filter yet), and the private SkyLight zoom symbols may
change between macOS releases (the backend degrades to plain placement).

## Linux — wlroots session (sway, niri, Hyprland, wayfire)

```bash
go test -tags=integration ./accessibility/linux/ ./accessibility/linux/compositor/
go test -tags=integration ./internal/wayland/ ./vision/capture/wayland/
go test -tags=integration ./internal/wlrinject/
go test -tags=integration ./internal/uinput/
```

Manual checks:

1. `go run ./examples/hints` in a wlroots session: `WAYLAND_DISPLAY` set,
   frontmost window should list nodes, focus should follow the compositor
   (KWin/GNOME bridges are not involved here).
2. `PerformAction(TypeLeftClick, …)` should click a native Wayland client (not
   only XWayland). This is the wlrinject path.
3. `Scroll(0, 120, 0)` should move a scrollable view; with `/dev/uinput`
   writable it uses the hi-res wheel.
4. `capture.CaptureScreen` should return a frame covering the output union,
   with `Scale` matching the output scale.
5. On an X11 session, `go run ./examples/windows` should list every top-level
   window (title, PID, application, client-area bounds),
   `go run ./examples/windows -title <window title>` should raise and focus the
   match, and `-rect 0,0,960,1440` should move and resize it to that client area.
   On Wayland the same commands must report `CodeNotSupported` for window
   management rather than a partial XWayland list. A tiling manager may apply its
   own rectangle; the printed bounds are what it actually applied.

## Linux — KDE or GNOME Wayland session

```bash
go test -tags=integration ./accessibility/linux/ ./accessibility/linux/compositor/
go test -tags=integration ./vision/capture/kwin/     # KDE
go test -tags=integration ./vision/capture/gnome/    # GNOME (may skip on AccessDenied)
go test -tags=integration ./vision/capture/portal/   # fallback
```

Manual checks:

1. Focus and window origin: a GTK 4 window's hints should sit on the controls
   rather than at the screen origin (`compositor.PlacementFor`).
2. Injection: clicking a native Wayland window should work through libei
   (KDE/Mutter), which shows the RemoteDesktop consent prompt on first use.
3. Capture on KDE: `capture.CaptureScreen` should use KWin's ScreenShot2 with
   no permission prompt and report the metadata scale; if KWin 5.27 refuses the
   caller (desktop-file authorization), the portal fallback should prompt once.
4. Capture on GNOME: the native Screenshot interface is usually denied to an
   ordinary application; capture must fall through to the portal rather than
   fail.
5. `org.a11y.Status` restore: after `Close`, the assistive-technology flags
   should be what they were before the first tree read.

## Windows

```bash
go test -tags=integration ./accessibility/windows/
go test -tags=integration ./vision/ocr/winrt/
NERU_WINDOW_LIVE=1 go test -tags=integration -run 'TestManager_(Activate|SetBounds)' ./window/   # PowerShell: $env:NERU_WINDOW_LIVE='1'
```

Manual checks:

1. Run at medium integrity (a normal interactive desktop, not the sandbox here).
   At low integrity, UIPI blocks cross-process UIA and SendInput; the tests will
   look empty rather than fail, which is the environment, not the backend. The
   window activation test skips itself unless `NERU_WINDOW_LIVE` is set because
   a low-integrity process also cannot take the foreground: `AttachThreadInput`
   is refused across integrity levels.
2. `go run ./examples/hints` on Notepad, Settings (WinUI) and Chrome should
   list buttons/edit fields/list items.
3. `capture.ExcludeOwnWindows()` followed by a capture with a visible overlay
   should omit the overlay (Windows 10 2004+).
4. OCR: `winrt.New().Health(ctx)` should be nil with an English language pack;
   a frame with known text should return it with box coordinates in image space.
5. Windows: `go run ./examples/windows` should list every visible top-level
   window, including minimized ones with empty bounds,
   `go run ./examples/windows -title 关于` (with `winver.exe` open) should
   restore, raise and focus that window, and adding `-rect 120,90,500,400` should
   move it to exactly that visible frame. With another application in front and
   the user idle, activation should still succeed; with the foreground locked by
   recent input it may report `CodeAccessibilityFailed`, which is the honest
   answer rather than a false success.

## What a failure means

- `CodeNotSupported`: the facility is absent on this machine (compositor
  protocol, portal, OCR language pack, `/dev/uinput`, libei). The message names
  the remedy.
- `CodeAccessibilityDenied`: a consent or permission was refused (TCC, portal
  dialog, XDG session). It is not retried automatically where retrying would
  re-prompt.
- Anything else: a real failure; the package documents which code it returns.