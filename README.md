# neru-core

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./README.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./README_zh.md)

A pure-Go library for reading the desktop's accessibility tree, detecting UI
targets from pixels, and injecting pointer input.

It is the accessibility (AX) and vision half of [Neru](https://github.com/y3owk1n/neru),
extracted into a public module. Every platform backend is written without CGo:
macOS and Linux bind the system libraries at run time with
[`purego`](https://github.com/ebitengine/purego), Windows uses the standard
library's Win32/COM syscall surface, and Linux screen capture speaks the X11
wire protocol directly. A program that imports `neru-core` stays a plain Go
binary, cross-compiles from any host, and never needs a C toolchain.

```
go get github.com/LouYuanbo1/neru-core
```

Requires Go 1.27.3 or newer, the version in `go.mod`.

## What is here

| Package | What it does |
| --- | --- |
| `accessibility` | Platform selector and the neutral-node conversion: `New`, `FromNode`, `FromNodes`, `SurfaceNodes`. |
| `accessibility/ax` | The backend contract (`Client`, `Window`, `App`, `Node`, `SurfaceProvider`). |
| `accessibility/darwin` | macOS backend: AXUIElement through ApplicationServices, CGEvent input, Dock/Notification Center/Stage Manager/PiP/screen-capture UI, Mission Control, Accessibility Zoom, bundle-type detection — all via `purego`. |
| `accessibility/windows` | Windows backend: UI Automation through COM, input through `SendInput`. |
| `accessibility/linux` | Linux backend: AT-SPI over D-Bus, input through X11/XTest, Wayland input through wlroots/libei, all via `purego`. |
| `accessibility/linux/compositor` | Compositor bridges for focus and window origin: KWin, GNOME Shell, niri, sway, Hyprland, COSMIC. |
| `element` | Neutral element model, the role vocabulary, native role mappers for AX / AT-SPI / UIA, and spatial snapping (`Nearest`, `Toward`). |
| `action` | Platform-neutral mouse action model and modifier bitmask. |
| `errs` | Small error vocabulary with codes (`CodeNotSupported`, `CodeAccessibilityFailed`, …). |
| `vision` | Turns captured pixels into elements: `Pipeline` (OCR + rectangles + merge + classify), contour and segment detectors, region merging, `ReadText`. |
| `vision/capture` | Screen capture per platform: GDI on Windows, X11 root / wlr-screencopy / desktop-portal fallback on Linux, `screencapture` on macOS. |
| `vision/ocr` | The recognizer contract (`Recognizer`, `Word`, `Health`). |
| `vision/ocr/winrt`, `vision/ocr/tesseract`, `vision/ocr/visionframework` | One engine per package: Windows.Media.Ocr, libtesseract (in-process via `purego`, or the CLI), Apple's Vision framework (text, rectangles, saliency). |
| `vision/ocr/auto` | The running platform's built-in recognizer. |
| `vision/contour`, `vision/segment`, `vision/frame` | The pure-Go detectors and the pixel plumbing they share. |

## Reading the accessibility tree

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

elements := accessibility.FromNodes(nodes) // neutral element.Role, copied text
for _, node := range nodes {
	node.Release() // every node owns a platform reference
}

for _, elem := range elements {
	fmt.Println(elem.Role(), elem.Bounds(), elem.IsClickable())
}
```

`ClickableNodes` takes native role names, because the platform backends filter
before they read an element's text. Pass `nil` for the documented default
targets, or resolve your own with the neutral vocabulary:

```go
roles := element.ResolveRoleEntries([]string{"button", "link", "ax:AXTextArea"}, "darwin")
nodes, err := client.ClickableNodes(ctx, window, roles.Native, 0, false)
```

`element.ResolveRoleEntries` reports entries that are misspelled (fatal), that
belong to another platform (ignored), or that have no equivalent here (ignored)
instead of silently matching nothing. `ResolveRoles` is the shorter path when
you already hold `element.Role` values.

On macOS, the auxiliary surfaces Neru scans are one call away:

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
		continue // CodeNotSupported elsewhere, CodeElementNotFound if not running
	}
	// ... convert or use the nodes, then release them
}
```

A platform without a surface reports `errs.CodeNotSupported`, so a
cross-platform caller can loop over all of them unconditionally. Call
`accessibility.WarmUp(ctx, client)` once at startup: on Linux it opens the
Wayland input session (which can show a consent dialog) and the uinput wheel
ahead of the first hotkey.

## Acting on what you found

```go
import (
	"image"

	"github.com/LouYuanbo1/neru-core/action"
)

point := image.Pt(420, 240)
err := client.PerformAction(action.TypeLeftClick, point, true, 0)
```

The action model covers left/right/middle click, press, release, toggle, moving
the cursor, and scrolling, with `action.ModCmd`, `ModShift`, `ModAlt` and
`ModCtrl` held during the action. `restoreCursor` puts the pointer back where it
was after a click, which is what a hint activation wants.

## Finding targets without an accessibility tree

Vision is for the windows whose accessibility tree lies or is empty. Capture a
region, run a detector, and get clickable elements back in global coordinates:

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

`vision.DetectSegments` is the other detector: it groups words and controls
rather than looking for button-shaped boxes. Both return `element.Element`s
with `Element.IsVisionOnly()` set, so a caller knows it must act by
coordinates. For an OCR engine, `vision.RegionsFromWords` maps recognized words
from the capture's pixel space onto the desktop, and
`vision.ElementsFromRegions` classifies them with the same geometry heuristics
the detectors use.

### Text recognition and the `vision` strategy

The full "vision" strategy is OCR plus rectangle detection, merged and
classified. The OCR engine is an opt-in import, so nothing pulls in a native
library unless the program asks for one:

```go
import (
	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/ocr/winrt"
)

recognizer, err := winrt.New() // or tesseract.NewLibrary / visionframework.New
if err != nil {
	return err
}
defer recognizer.Close()

pipeline := vision.Pipeline{
	Text:             recognizer,
	DetectText:       true,
	DetectRectangles: true, // pure-Go contour detector unless Boxes is set
	WordLevel:        true,
}

elements, err := pipeline.Detect(ctx, frame)
```

`vision/ocr/winrt` (Windows.Media.Ocr), `vision/ocr/tesseract` (an in-process
libtesseract via purego, or `NewCLI` for a local `tesseract` executable) and
`vision/ocr/visionframework` (Apple's Vision framework) each implement
`vision/ocr.Recognizer`. A caller can also supply its own engine or a
`vision.BoxFinder` such as the Vision framework's rectangle request; the
`Pipeline` only depends on those two interfaces.

## Platform support

| Capability | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Read accessibility tree | AX (ApplicationServices) | AT-SPI (D-Bus) | UI Automation (COM) |
| Clickable-node scan | yes | yes | yes |
| Frontmost window / app | yes | yes, compositor-first | yes |
| Menu bar scan | yes | — | — |
| Dock / Notification Center / Stage Manager / PiP / screen-capture UI | yes (`SurfaceNodes`) | — | — |
| Mission Control windows + Spaces bar | yes | — | — |
| Mouse click / move / scroll | CGEvent | XTest (X11), libei (Wayland), uinput wheel | `SendInput` |
| Smooth cursor motion | yes (`SmoothSteps`) | — | — |
| Screen capture | `screencapture` | X11 root, wlr-screencopy (Wayland) | GDI `BitBlt` |
| Text recognition | Vision framework | libtesseract / tesseract CLI | Windows.Media.Ocr |
| Accessibility permission gate | `AXIsProcessTrusted` | none | none |

Unsupported surfaces return `errs.CodeNotSupported`; they never pretend to
succeed with an empty result.

Known limits, stated by the backends themselves:

- **macOS.** Every surface Neru scans is scanned here: the frontmost window, the
  menu bar, the Dock, Notification Center, Stage Manager, Picture-in-Picture,
  the screen-capture UI and Mission Control. Capture uses ScreenCaptureKit on
  macOS 14 and newer, which is fast and leaves the caller's own windows out;
  anywhere that fails it falls back to the `screencapture` utility, which is
  slower and includes them. Accessibility Zoom correction uses the same public
  API plus the private SkyLight query Neru uses.
- **Linux.** Injection on X11 is XTest; on Wayland it is the wlroots virtual
  pointer where the compositor exposes it (sway, niri, Hyprland, wayfire) and
  libei through the RemoteDesktop portal otherwise (KDE, Mutter, cosmic). A
  session with neither reports `CodeNotSupported`. The uinput wheel reproduces
  pixel-accurate smooth scrolling wherever `/dev/uinput` is writable. Capture
  reads the X11 root, the compositor's wlr-screencopy protocol on a wlroots
  Wayland session, KWin's ScreenShot2 interface on KDE (no prompt, metadata
  geometry), or GNOME Shell's Screenshot interface where its sender allowlist
  permits it; otherwise it falls back to the desktop portal's `Screenshot`
  interface (slower, may prompt once). Focus and window origin come from the
  compositor bridge for KWin, GNOME Shell, niri, sway, Hyprland and (through an
  injectable geometry source) COSMIC; X11 still infers focus from AT-SPI, so
  Chromium and Electron can occasionally resolve to a background window. The
  first tree call enables `org.a11y.Status`, and `Close` restores the session's
  previous values.
- **Windows.** There is no menu-bar or Mission Control analogue. Clicking is
  coordinate injection, not a UIA Invoke, so it behaves like a real mouse. Each
  query creates and releases its own UIA client object, so no COM object is
  shared across goroutines. `capture.ExcludeOwnWindows` marks the process's
  top-level windows with `WDA_EXCLUDEFROMCAPTURE` (Windows 10 2004+), so a hint
  overlay does not appear in the frame it detects over.

## Design notes

**No CGo.** Every native capability is reached at run time instead of linked:
CoreFoundation, ApplicationServices, CoreGraphics and the Objective-C runtime on
macOS; AT-SPI over D-Bus, X11/XTest, the Wayland wire protocol, libei and
`/dev/uinput` on Linux; the standard library's `syscall`/COM surface on
Windows. Text recognition follows the same rule: Windows.Media.Ocr through
WinRT, libtesseract through `purego`, Apple's Vision framework through
`purego` and `objc_msgSend`. The internal `native` package is the only place a
symbol is loaded, and it is internal so the raw function pointers never leak
into the public API.

**Native roles at the edge, neutral roles in the model.** A backend's `Node`
reports the platform's role name (`AXButton`, `push button`, `Button`) because
that is what its filter compares and what a caller may need for diagnostics.
`accessibility.FromNode` translates it into `element.Role` through the
platform's mapper. The mappers are exported, so a program that keeps its own
node type can translate without this package.

**Ownership.** `Window`, `App` and `Node` values own a platform reference and
must be released exactly once (`Release`). `FromNode` copies everything it
needs, so a caller can release nodes immediately after converting them.
Elements are immutable and safe to share between goroutines.

**Latency.** The macOS walk batches an element's attributes into one
accessibility call, reads text before returning, and hit-tests each candidate
against the window server so covered controls are not hinted. `SkipHitTest`
trades that precision for one round trip per candidate. `MaxDepth` and
`MaxNodes` bound a pathological tree.

## Development

```bash
go test ./...
go vet ./...

# Cross-compile without a C toolchain, from any host:
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=linux   GOARCH=arm64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Integration tests that drive a real desktop are tagged
`integration && <os>` and are not part of `go test ./...`.

## License

MIT, the same license as Neru, from which this module is extracted. See
`LICENSE`.