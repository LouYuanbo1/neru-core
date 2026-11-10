# Parity with Neru

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./PARITY.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./PARITY_zh.md)

This module is the accessibility and vision half of Neru, extracted so another
Go program can use it. This file tracks, feature by feature, what Neru does,
which package here owns it, and how it is verified. It is the working checklist
for the extraction: an item is only "done" when the code exists, the tests or a
live check passed, and the limitation (if any) is stated in that package's
documentation.

Status values: **done** (implemented and verified), **partial** (implemented
with a documented gap), **planned** (tracked, not implemented).

A note on verification: this extraction was built on a Windows host. Windows
behavior can be run here (and OCR was verified live); Linux and macOS code is
cross-compiled and vetted for both architectures, and their pure logic is
unit-tested, but their live paths need one run on the target OS before being
called verified. The entries marked **done** below mean "implemented and
compilation/logic-verified"; the two exceptions are Windows runtime paths and
the host-runnable vision tests.

## Accessibility — macOS

| Neru feature | Package | Status | Verification |
| --- | --- | --- | --- |
| AX tree read, role/state/text batch | `accessibility/darwin` | done | darwin build/vet, unit tests compile |
| Clickable scan with hit-test visibility | `accessibility/darwin` | done | same |
| Frontmost window/app, all windows | `accessibility/darwin` | done | same |
| Menu bar scan | `accessibility/darwin` | done | same |
| Dock items as hint targets | `accessibility/darwin` (`SurfaceNodes`) | done | same |
| Notification Center targets | `accessibility/darwin` | done | same |
| Stage Manager targets | `accessibility/darwin` | done | same |
| Picture-in-Picture targets | `accessibility/darwin` | done | same |
| Screen-capture UI targets | `accessibility/darwin` | done | same |
| Mission Control windows + Spaces bar | `accessibility/darwin` | done | same |
| Browser engine detection for tree filtering | `accessibility/darwin` (`DetectBundleType`) | done | unit tests compile |
| Accessibility Zoom viewport correction | `accessibility/darwin` (`ZoomViewport`) | done | same |
| Smooth cursor motion, drag event types | `accessibility/darwin` (`Options.SmoothSteps`) | done | same |
| Global mouse-button state, ensure mouse up | `accessibility/darwin` (`MouseButtonDown`, `EnsureMouseUp`) | done | same |
| Secure text field value blanking | `accessibility/darwin` | done | same |

## Accessibility — Linux

| Neru feature | Package | Status | Verification |
| --- | --- | --- | --- |
| AT-SPI tree read, roles, states, extents | `accessibility/linux` | done | linux build/vet, unit tests |
| Focus + origin: KWin | `accessibility/linux/compositor` | done | linux build/vet, portable tests |
| Focus + origin: GNOME Shell extension | `accessibility/linux/compositor` | done | same |
| Focus + origin: niri / sway / Hyprland | `accessibility/linux/compositor` | done | same |
| Focus + origin: COSMIC (origin via injectable geometry) | `accessibility/linux/compositor` | partial | same; focus falls back |
| X11/XTest pointer and wheel injection | `accessibility/linux` | done | linux build/vet |
| libei injection (KDE, Mutter, cosmic) | `internal/libei` | done | linux build/vet, portable tests |
| wlroots virtual-pointer injection (sway, niri, Hyprland) | `internal/wlrinject` | done | linux build/vet, portable tests |
| uinput smooth scrolling | `internal/uinput` | done | linux build/vet, host tests |
| X11 focus for Chromium/Electron windows | — | same as Neru | Neru also infers X11 focus from AT-SPI; the compositor bridge is Wayland-only |

## Accessibility — Windows

| Neru feature | Package | Status | Verification |
| --- | --- | --- | --- |
| UIA tree read, control types, patterns | `accessibility/windows` | done | windows tests, live smoke |
| Window/app enumeration, DPI, virtual screen | `accessibility/windows` | done | same |
| SendInput click/drag/scroll | `accessibility/windows` | done | unit tests |

## Vision

| Neru feature | Package | Status | Verification |
| --- | --- | --- | --- |
| Contour detector | `vision/contour` | done | host tests |
| Segment detector | `vision/segment` | done | host tests |
| Geometry classifier, region merge (NMS) | `vision` | done | host tests |
| OCR words to desktop regions | `vision.RegionsFromWords` | done | host tests |
| Windows.Media.Ocr recognizer | `vision/ocr/winrt` | done | **live-verified on Windows** |
| tesseract recognizer (in-process libtesseract via purego) | `vision/ocr/tesseract` | done | host tests (no library installed here) |
| tesseract CLI recognizer | `vision/ocr/tesseract` (`NewCLI`) | done | host tests |
| Vision framework OCR | `vision/ocr/visionframework` | done | darwin build/vet, unit tests compile |
| Vision framework rectangles | `vision/ocr/visionframework` | done | same |
| Vision framework saliency | `vision/ocr/visionframework` | done | same |
| Rectangle filtering (size/aspect/count) | `vision` | done | host tests |
| `vision` strategy pipeline (text + rectangles + merge) | `vision.Pipeline` | done | host tests |
| Split-word text targets | `vision.Pipeline.WordLevel` | done | host tests |
| Read text from a frame | `vision.ReadText` | done | host tests |
| Screen capture: macOS ScreenCaptureKit (14+) with own-window exclusion, `screencapture` fallback | `vision/capture` | done | darwin build/vet, unit tests compile |
| Screen capture: GDI / X11 / screencapture | `vision/capture` | done | host tests, cross-build |
| Screen capture: wlr-screencopy (Wayland) | `vision/capture/wayland` | done | linux build/vet, portable tests |
| Screen capture: KDE/GNOME portal fallback | `vision/capture/portal` | done | linux build/vet, portable tests; needs a live session |
| Screen capture: KDE KWin ScreenShot2 (native, no prompt) | `vision/capture/kwin` | done | linux build/vet, portable tests |
| Screen capture: GNOME Shell (native; usually denied, falls back to portal) | `vision/capture/gnome` | partial | linux build/vet, portable tests; live GNOME must use the portal |
| Screen capture: KDE portal + PipeWire (native stream) | — | not needed | KWin ScreenShot2 returns the same frame without PipeWire |
| Capture excluding own windows | `vision/capture` | done | Windows `ExcludeOwnWindows` host-tested; macOS implicit in the ScreenCaptureKit path; Linux has no equivalent |

## Remaining work

1. **Live runs on the target OSes.** Every feature is implemented, compiled and
   vetted for all target platforms, and the logic is unit-tested; the
   macOS-only and Linux-only paths still need one hands-on run with
   `docs/LIVE-VERIFICATION.md` before being called verified end to end.
2. **X11 focus for Chromium/Electron** is the same behavior Neru has: its
   compositor bridge is Wayland-only, and X11 focus comes from AT-SPI's
   ACTIVE/SHOWING ranking. This is documented in `accessibility/linux` and is
   not an extraction gap.

## Library shape

A capability lives in the smallest package that can own it, so an importer pays
only for what it uses:

| Package | Import cost |
| --- | --- |
| `accessibility` | platform selector; pulls that platform's backend |
| `accessibility/ax` | contract only |
| `accessibility/{darwin,linux,windows}` | one platform's native bindings |
| `accessibility/linux/compositor` | D-Bus/CLI compositor bridge (Linux only) |
| `element`, `action`, `errs` | pure Go, no native bindings |
| `vision`, `vision/contour`, `vision/segment`, `vision/frame` | pure Go detectors and pipeline |
| `vision/capture` | platform capture; `vision/capture/wayland` only on Linux |
| `vision/ocr` | contract only |
| `vision/ocr/{winrt,tesseract,visionframework}` | one recognizer engine each |
| `vision/ocr/auto` | picks the platform's engine at run time |
| `internal/*` | implementation details, not importable outside the module |

## Verification commands

```bash
GOCACHE=<writable> go test ./...
GOCACHE=<writable> go vet ./...
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  GOOS=... GOARCH=... go build ./... && go vet ./...
done
```

Integration tests that read or drive a real desktop are tagged
`integration && <os>`.