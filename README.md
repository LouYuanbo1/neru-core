# neru-core

[![English](https://img.shields.io/badge/Language-English-blue.svg)](./README.md)
[![中文](https://img.shields.io/badge/Language-%E4%B8%AD%E6%96%87-red.svg)](./README_zh.md)

A pure-Go library for reading the desktop's accessibility tree, detecting UI
targets from pixels, injecting pointer input, and enumerating, switching, moving
or resizing desktop windows.

It is the accessibility (AX) and vision half of [Neru](https://github.com/y3owk1n/neru),
extracted into a public module, plus desktop window management, which Neru
itself does not need. Every platform backend is written without CGo:
macOS and Linux bind the system libraries at run time with
[`purego`](https://github.com/ebitengine/purego), Windows uses the standard
library's Win32/COM syscall surface, and Linux screen capture speaks the X11
wire protocol directly. A program that imports `neru-core` stays a plain Go
binary, cross-compiles from any host, and never needs a C toolchain.

```
go get github.com/LouYuanbo1/neru-core
```

Requires Go 1.27.3 or newer, the version in `go.mod`.

## Quick start

### Read the text on screen

```go
import (
	"context"

	"github.com/LouYuanbo1/neru-core/vision"
	"github.com/LouYuanbo1/neru-core/vision/capture"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

ctx := context.Background()

recognizer, err := ocr.New() // the engine this system provides
if err != nil {
	return err
}

frame, err := capture.CaptureScreen(ctx)
if err != nil {
	return err
}

text, err := vision.ReadText(ctx, frame, recognizer, nil)
```

### List what is clickable

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

### Click the element nearest the cursor

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
	return errors.New("nothing to click")
}

// The third argument, restoreCursor, puts the pointer back afterwards.
err := client.PerformAction(action.TypeLeftClick, target.Point, true, 0)
```

## What is here

| Package | What it does |
| --- | --- |
| `accessibility` | Platform selector and the neutral-node conversion: `New`, `FromNode`, `FromNodes`, `SurfaceNodes`, `WarmUp`. |
| `accessibility/ax` | The backend contract (`Client`, `Window`, `App`, `Node`, `Surface`, `SurfaceProvider`). |
| `accessibility/darwin` | macOS backend: AXUIElement through ApplicationServices, CGEvent input, Dock/Notification Center/Stage Manager/PiP/screen-capture UI, Mission Control, Accessibility Zoom, bundle-type detection — all via `purego`. |
| `accessibility/windows` | Windows backend: UI Automation through COM, input through `SendInput`. |
| `accessibility/linux` | Linux backend: AT-SPI over D-Bus, input through X11/XTest, Wayland input through wlroots/libei, all via `purego`. |
| `accessibility/linux/compositor` | Compositor bridges for focus and window origin: KWin, GNOME Shell, niri, sway, Hyprland, COSMIC. |
| `window` | Desktop window management: enumerate top-level windows, read the frontmost one, activate one by ID / PID / application / title, and move or resize it. Win32, macOS accessibility and X11 EWMH backends. |
| `element` | Neutral element model, the role vocabulary, native role mappers for AX / AT-SPI / UIA, and spatial snapping (`Nearest`, `Toward`). |
| `action` | Platform-neutral mouse action model and modifier bitmask. |
| `errs` | Small error vocabulary with codes (`CodeNotSupported`, `CodeAccessibilityFailed`, …). |
| `vision` | Turns captured pixels into elements: `Pipeline` (OCR + rectangles + merge + classify), contour and segment detectors, region merging, `ReadText`. |
| `vision/capture` | Screen capture per platform: GDI on Windows, X11 root / wlr-screencopy / desktop-portal fallback on Linux, ScreenCaptureKit on macOS 14+ with a `screencapture` fallback. |
| `vision/ocr` | The facade: `New` returns the OCR engine the operating system provides, or the system-wide tesseract installation that stands in for it on Linux. |
| `vision/ocr/engine` | The engine contract (`Recognizer`, `Word`, `Options`, `Health`). |
| `vision/ocr/winrt`, `vision/ocr/tesseract`, `vision/ocr/visionframework` | One engine per package: Windows.Media.Ocr, libtesseract (in-process via `purego`, or the CLI), Apple's Vision framework (text, rectangles, saliency). |
| `vision/contour`, `vision/segment`, `vision/pixels` | The pure-Go detectors, and the `Frame` type plus the pixel operations they share. |

## Accessibility

### Creating a client

`accessibility.New` picks the backend for the running platform and returns the
`ax.Client` contract, so call sites never mention a platform package.

```go
client, err := accessibility.New(accessibility.Options{
	Roles:      []element.Role{element.RoleButton, element.RoleLink},
	MaxDepth:   12,
	MaxNodes:   4000,
	SkipHitTest: true, // macOS only
})
defer client.Close()
```

| Option | Meaning | Platform |
| --- | --- | --- |
| `Roles` | Neutral roles to scan for; translated to native names before the backend starts. | all |
| `ClickableRoles` | Native role names, appended verbatim; the escape hatch for names with no neutral equivalent. | all |
| `MaxDepth` | Bound on the tree walk below the root. Zero means the backend default. | all |
| `MaxNodes` | Bound on how many nodes one walk visits. Zero means the backend default. | all |
| `SkipHitTest` | Skip the per-element visibility hit test, trading precision for one round trip per candidate. | macOS |
| `SmoothSteps` | Intermediate move events a cursor move posts; zero moves instantly. | macOS |
| `SmoothDelay` | Pause between those move events. | macOS |
| `DisableCompositor` | Do not talk to KWin/GNOME Shell/compositor CLIs; fall back to AT-SPI geometry. | Linux |
| `BundleType` | Override the browser-engine detection used to filter noisy web content. | macOS |

`accessibility.Supported()` reports whether this build has a backend for the
running platform, and `accessibility.Mapper()` returns its role mapper.

### Reading the tree

```go
if !client.CheckPermissions() {
	return fmt.Errorf("accessibility permission is not granted")
}

window, err := client.FrontmostWindow(ctx)
defer window.Release()

// nil roles means the client's configured set; 0 depth means the default;
// true reads text and state while scanning.
nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
defer accessibility.ReleaseNodes(nodes)

elements := accessibility.FromNodes(nodes) // neutral roles, copied text
```

| `ax.Client` method | Purpose |
| --- | --- |
| `FrontmostWindow`, `AllWindows`, `FrontmostAndPopoverWindows` | Window discovery. |
| `FocusedApplication`, `ApplicationByBundleID` | Application discovery; `App.Info()` reports bundle metadata. |
| `ClickableNodes(ctx, root, roles, maxDepth, readState)` | The main scan: clickable nodes under one root. |
| `MenuBarClickableElements`, `MissionControlClickableNodes` | macOS surfaces. |
| `ClickableElementsFromBundleID` | Scan an application that is not frontmost. |
| `ActiveScreenBounds` | The active display, in logical pixels. |
| `PerformAction`, `Scroll`, `MoveMouse`, `CursorPosition` | Input. `MoveMouse(p, bypassSmooth)` skips smooth motion. |
| `CheckPermissions`, `SetClickableRoles`, `ClickableRoles`, `IsMissionControlActive`, `SupportsSupplementaryElements` | State. |
| `Close` | Releases the backend. |

`ax.Window` is an `ax.Element` with a `Role()`; `ax.App` adds
`BundleIdentifier()` and `Info()`. `ax.Node` reports the platform's own role
string, bounds, title, description, value, focus, identifier and clickability,
and owns a platform reference that `Release()` frees.

### Roles: neutral and native

`element.Role` is the neutral vocabulary (`button`, `link`, `text_field`, …).
A backend compares native strings (`AXButton`, `push button`, `Button`), so the
facade translates before the walk starts:

```go
client, err := accessibility.New(accessibility.Options{
	Roles: []element.Role{element.RoleButton, element.RoleLink},
})

// A per-call list is also possible, and can mix neutral and native names:
res := element.ResolveRoleEntries([]string{"button", "link", "ax:AXTextArea"}, "darwin")
nodes, err := client.ClickableNodes(ctx, window, res.Native, 0, false)
```

`ResolveRoleEntries` reports misspelled entries (fatal), entries from another
platform (ignored), and entries with no equivalent here (ignored) instead of
silently matching nothing. `ResolveRoles` is the shorter path when you already
hold `element.Role` values. `element.DefaultClickableNativeRoles(vocab)` returns
the shipped default set.

### Auxiliary surfaces (macOS)

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
		continue // CodeNotSupported elsewhere, CodeElementNotFound if not running
	}
	// ... use the nodes, then release them
}
```

A platform without a surface reports `errs.CodeNotSupported`, so a
cross-platform caller can loop over all of them unconditionally.

### Warming up

Call `accessibility.WarmUp(ctx, client)` once at startup. On Linux it opens the
Wayland input session (which can show a consent dialog) and the uinput wheel
ahead of the first hotkey. Backends with nothing to prepare return nil.

### Writing a backend

Implement `ax.Client`. `Client` is one interface, so only the methods a platform
can answer need real code; the rest report `errs.CodeNotSupported` rather than
pretending. Optional capabilities are separate interfaces: `ax.SurfaceProvider`
for the auxiliary surfaces, and `accessibility.Warmer` for input warm-up.
`accessibility.SurfaceNodes` and `accessibility.WarmUp` type-assert to them, so
the base contract stays small. `examples/custom-backend` is a complete minimal
backend: a window, a node, and `CodeNotSupported` for everything else.

## Desktop windows (window)

`window` is the window-management half of the module: it enumerates the
desktop's top-level windows, reports the frontmost one, brings a chosen window to
the foreground, and moves or resizes it. The platform work sits behind one
contract, so a caller never writes a Win32 `EnumWindows` callback, an
accessibility window walk or an EWMH client message itself.

```go
manager, err := window.New(window.Options{})
if err != nil {
	return err
}
defer manager.Close()

windows, err := manager.List(ctx) // front to back, minimized windows included
for _, w := range windows {
	fmt.Println(w.ID, w.PID, w.Application, w.Title, w.Bounds, w.Minimized)
}

// Address a window by exactly what you have: ID, PID, application or title.
activated, err := manager.Activate(ctx, window.Target{Application: "chrome.exe"})

// Or bring it forward and give it a new frame in one call.
placed, err := manager.SetBounds(
	ctx,
	window.Target{Application: "chrome.exe"},
	image.Rect(0, 0, 960, 1440),
)
```

| `window.Manager` method | Purpose |
| --- | --- |
| `List` | Every visible top-level window, front to back; minimized windows included. |
| `Frontmost` | The window the user is working in; `CodeElementNotFound` when none has focus. |
| `Activate` | Bring a target to the foreground, verified before it reports success. |
| `SetBounds` | Restore, move, resize and raise a window; returns the frame the platform actually gave it. |
| `Close` | Release the backend (the X11 display connection); idempotent. |

`window.Info` carries `ID` (an opaque platform handle), `PID`, `Title`,
`Application` (executable base name on Windows and Linux, bundle identifier on
macOS), `Bounds` (physical screen pixels on Windows and X11, logical points on
macOS; empty when minimized), `Minimized` and `Focused`.

A `window.Target` selects by `ID`, `PID`, `Application` or `Title`, in that
priority order; the first non-empty field decides. `Application` is compared
case-insensitively and accepts a full executable path, and `Title` is a
case-insensitive substring. Among several matches a focused window wins, then
the frontmost one. `window.Find` applies that rule to any window slice, so a
caller holding its own enumeration gets the same selection the backends use.

`SetBounds` returns the frame the platform actually applied, which can differ
from the rectangle it was asked for: a window with a minimum size keeps it, and a
tiling window manager may lay it out its own way. Reporting the actual frame is
honest; the requested one would be a guess. The foreground is requested as part
of placing a window, but whether the platform granted it is reported in
`Info.Focused` rather than treated as a failure of the placement. An X11 window
manager that never applies the frame reports `CodeAccessibilityFailed`.

`Options.IncludeUntitled` keeps windows whose title bar is empty; those are
usually helper surfaces, so they are dropped by default. A window that has focus
is always kept.

Backends: Win32 `EnumWindows`, plus the `AttachThreadInput` sequence that gets
`SetForegroundWindow` past Windows' foreground lock and a `SetWindowPos` that
compensates for the invisible DWM resize borders; macOS accessibility window
objects plus `NSRunningApplication` activation and `AXPosition`/`AXSize`; X11
EWMH (`_NET_CLIENT_LIST`, `_NET_ACTIVE_WINDOW`, `_NET_MOVERESIZE_WINDOW`). A
Wayland session exposes no portable window-management protocol, so it reports
`CodeNotSupported` rather than presenting a partial XWayland list. `Activate`
verifies the switch before reporting success.

## Elements

`element.Element` is the neutral noun both halves produce. It is immutable, safe
to share between goroutines, and carries no platform vocabulary.

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

| Method | Returns |
| --- | --- |
| `ID`, `Role`, `Bounds`, `Center` | Identity and geometry. |
| `Title`, `Description`, `Value`, `Text` | Text; `Text` prefers value, then title, then description. |
| `SearchText` | Text of the node's subtree, for hint search. |
| `IsClickable`, `IsFocused`, `Identifier` | State. |
| `Source`, `IsVisionOnly` | What found it; vision-only elements must be clicked by coordinate. |
| `Contains`, `Overlaps`, `IsVisible(screenBounds)` | Geometry predicates. |

Options: `WithClickable`, `WithTitle`, `WithDescription`, `WithValue`,
`WithFocused`, `WithIdentifier`, `WithSearchText`, `WithSource`.

### Role vocabulary

`element.Role` values include: `button`, `link`, `text_field`, `static_text`,
`image`, `check_box`, `radio_button`, `menu_item`, `menu_button`,
`pop_up_button`, `tab_button`, `slider`, `switch`, `disclosure_triangle`,
`text_area`, `combo_box`, `popover`, `sheet`, `menu`, `list`, `heading`,
`menu_bar_item`, `menu_bar`, `cell`, `row`, `list_item`, `dock_item`,
`incrementor`, `color_well`, `search_field`, `toolbar_button`, `toggle`, `table`,
`outline`, `application`, `window`, `tab_group`, `group`, `scroll_area`,
`split_group`, `generic_element`, `unknown`.

### Mapping native roles

```go
role, ok := element.MapNativeRole(element.AXRoleMapper{}, "AXButton", "")
role, ok = element.MapNativeRole(element.UIARoleMapper{}, "Button", "")
role, ok = element.MapNativeRole(element.ATSPIRoleMapper{}, "push button", "")
```

`element.MapperForGOOS(goos)` returns the mapper for a platform. The mappers are
exported because `accessibility.FromNode` uses them, and a program with its own
node type can translate without the facade.

### Snapping between elements

```go
target, ok := element.Nearest(elements, image.Pt(500, 400), region)
target, ok = element.Toward(elements, image.Pt(500, 400), region, element.DirectionRight)
```

Both clip candidates to the region and return `element.Target{Element, Point}`,
where `Point` is the click point (the element's center, clipped). `Nearest`
ranks by coverage and distance; `Toward` only considers one direction. Directions
are `DirectionUp`, `DirectionDown`, `DirectionLeft`, `DirectionRight`, and
`ParseDirection`/`String` convert to and from config strings.

## Actions

```go
client.PerformAction(action.TypeLeftClick, image.Pt(420, 240), true, 0)
client.PerformAction(action.TypeLeftMouseDown, point, false, action.ModShift|action.ModAlt)
client.MoveMouse(point, false) // false means "do not bypass smooth motion"
client.Scroll(0, 120, 0)       // deltaX, deltaY, modifiers
```

| Type | Meaning |
| --- | --- |
| `TypeLeftClick`, `TypeRightClick`, `TypeMiddleClick` | Press and release. |
| `TypeLeftMouseDown` / `Up` / `Toggle`, same for right and middle | Hold, release, toggle. |
| `TypeMoveMouse`, `TypeMoveMouseRelative` | Absolute and relative motion. |
| `TypeScroll` | Wheel at the current position. |

`MouseButton` is `ButtonLeft`, `ButtonRight`, `ButtonMiddle`; `MousePhase` is
`PhaseClick`, `PhaseDown`, `PhaseUp`; `Type.MouseButtonPhase()` decomposes a
type. `ParseType`, `ParsePhase` and `ParseModifiers` accept config spellings
(`"left_click"`, `"down"`, `"cmd+shift"`).

`Modifiers` is a bitmask of `ModCmd`, `ModShift`, `ModAlt`, `ModCtrl`.
`PrimaryModifier()` is the platform accelerator (Command on macOS, Control
elsewhere); `UsePrimaryModifier` changes what it means. `Scroll` takes the same
mask; positive `deltaY` scrolls up and positive `deltaX` scrolls left on every
platform.

## Errors

```go
err := errs.New(errs.CodeNotSupported, "no OCR engine is available")
err = errs.Wrapf(cause, errs.CodeTimeout, "reading %v", region)
err = errs.New(errs.CodeAccessibilityDenied, "permission denied").
	WithContext("remedy", "System Settings → Privacy & Security → Accessibility")
```

| Code | Meaning |
| --- | --- |
| `CodeNotSupported` | The facility is absent here; the message names the remedy. |
| `CodeAccessibilityDenied` | A permission or consent was refused. |
| `CodeAccessibilityFailed` | The platform API failed. |
| `CodeElementNotFound` | The window, app or surface is not running. |
| `CodeInvalidInput` | The caller passed something unusable (empty region, nil recognizer). |
| `CodeContextCanceled` | The caller's context was canceled. |
| `CodeTimeout` | A deadline expired. |
| `CodeInternal` | An invariant broke; not actionable by the caller. |

`errs.IsCode` and `errs.GetCode` look at the outermost error; `errors.Is` walks
the chain and compares codes, so a wrapped error still matches its cause.
`errs.IsNotSupported(err)` is the usual degradation check. `errs.Message`
strips the code prefix for logging. A plain `error` that is not an `*errs.Error`
reads as `CodeInternal`.

## Vision

### Frames and capture

A capture is a `pixels.Frame`:

| Field | Meaning |
| --- | --- |
| `Img` | The RGBA pixels, rectangle anchored at (0,0). |
| `Origin` | Where the frame's top-left sits on the desktop, in logical pixels. |
| `Region` | The logical desktop area the frame covers. |
| `Scale` | Frame pixels per logical pixel (the monitor's DPI scale). |

```go
frame, err := capture.CaptureScreen(ctx)          // the active display
frame, err = capture.CaptureRect(ctx, image.Rect(0, 0, 800, 600))
```

The same value goes straight into a detector, `ReadText` or a `Pipeline`; there
is no conversion step.

### Detectors

```go
buttons, err := vision.DetectContours(ctx, frame, contour.DefaultParams())
words, err := vision.DetectSegments(ctx, frame, segment.TargetsWords)
targets, err := vision.Detect(ctx, frame, vision.StrategyContour, "", contour.DefaultParams())
```

`contour.Params` tunes grayscale/blur/Sobel/Canny thresholds, target size
bounds, container height, center slack and square-icon slack. The zero value
filters everything out; start from `contour.DefaultParams()`. `segment` targets
are `TargetsWords`, `TargetsPhrases` and `TargetsControls`.

### The pipeline

`Pipeline` is the `vision` strategy: OCR plus rectangle detection, merged and
classified.

| Field | Meaning |
| --- | --- |
| `Text` | The recognizer. Required when `DetectText` is set. |
| `Boxes` | Optional native rectangle finder; nil means the pure-Go contour detector. |
| `DetectText`, `DetectRectangles` | Which halves to run; at least one. |
| `WordLevel` | One target per word instead of per line. |
| `MinConfidence` | Drop observations below this score. |
| `MergeIOU` | Non-maximum-suppression threshold; zero means `DefaultMergeIOU`. |
| `Timeout` | Bound for one `Detect` call; zero means `DefaultVisionTimeout`. |
| `Rectangle` | `RectangleOptions{MinSize, MinAspect, MaxAspect, MaxCandidates}`. |
| `Params` | Contour thresholds when `Boxes` is nil. |
| `Classifier` | Role assignment; nil means the baseline classifier. |

`Pipeline.Health(ctx)` probes the recognizer without capturing. `Detect` returns
elements with `SourceVision`; a call with neither half set is refused with
`CodeInvalidInput`.

### Reading text

```go
text, err := vision.ReadText(ctx, frame, recognizer, nil) // nil = engine defaults

opts := &engine.Options{
	Languages:     []string{"en-US", "ja"},
	WordLevel:     true,
	MinConfidence: 0.4,
}
text, err = vision.ReadText(ctx, frame, recognizer, opts)
```

`ReadText` joins the engine's words with single spaces and returns one string.
It probes `Health` before reading pixels, so a missing engine fails before a
screenshot is taken.

### From words to elements

```go
regions := vision.RegionsFromWords(words, frame.Region, frame.Img.Rect, 0.4)
elements, _ := vision.ElementsFromRegions(regions, vision.NewClassifier(vision.ClassifierOptions{}))
```

`RegionsFromWords` maps the engine's image-space boxes onto the desktop using the
frame's region and image size. `MergeRegions` fuses overlaps, `SortRegionsByScore`
orders them, and `ElementsFromRegions` assigns neutral roles. A `Classifier` is a
first-pass geometry-and-text heuristic; `NewClassifier` accepts threshold
overrides, and the zero value behaves like the shipped baseline.

### Pixel helpers

`pixels.Frame` also carries the operations the detectors share:
`Crop(frame, region)` returns the part of a frame inside a logical rectangle
(with its origin adjusted), `DeriveScale(imageWidth, logicalWidth)` recovers the
DPI scale from a render, `Downsample(img, factor)` and
`Resample(pix, stride, w, h, order, factor)` box-filter dense frames, and
`Rows(height, work)` splits a frame across cores.

## OCR

### The facade

```go
recognizer, err := ocr.New()
```

`vision/ocr.New` returns the engine the operating system provides:
Windows.Media.Ocr on Windows, Apple's Vision framework on macOS, libtesseract
(or a `tesseract` executable) on Linux. It returns `engine.Recognizer`, so it
substitutes for any explicit engine.

### The contract

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

`Word.Bounds` is in the image's own pixel space, with (0,0) at its top-left.
`engine.Health(ctx, r)` probes an optional `HealthChecker`; a recognizer without
one is assumed available.

### Engines

| Package | Engine | Options |
| --- | --- | --- |
| `ocr/winrt` | Windows.Media.Ocr | `Options{Languages []string}` |
| `ocr/tesseract` | libtesseract in process, or the CLI | `LibraryOptions{DataPath, Language, PageSegMode}` / `Options{Binary, PageSegMode, Languages}` |
| `ocr/visionframework` | Apple's Vision framework | `Options{Languages []string, Accurate bool}` |

```go
r, err := winrt.New(winrt.Options{Languages: []string{"en-US"}})
r, err = tesseract.NewLibrary(tesseract.LibraryOptions{Language: "eng"})
r, err = tesseract.NewCLI(tesseract.Options{Binary: "tesseract"})
r, err = visionframework.New(visionframework.Options{Accurate: true})
```

The winrt, visionframework and tesseract-library recognizers hold native
resources and have `Close`; the tesseract CLI recognizer has nothing to release.
A machine without the engine or its language pack reports `CodeNotSupported`
from `New`, `Health` or `Recognize`, with the install step in the message.

### A custom engine

```go
type fixed struct{}

func (fixed) Recognize(_ context.Context, _ *image.RGBA, _ engine.Options) ([]engine.Word, error) {
	return []engine.Word{
		{Text: "Save", Bounds: image.Rect(10, 10, 60, 30), Confidence: 0.99},
	}, nil
}

text, err := vision.ReadText(ctx, frame, fixed{}, nil)
```

Only `Recognize` is required. `vision` and `Pipeline` depend on this interface,
never on an engine package, so importing `vision` does not link one.

## Recipes

### Click the element whose text says "Save"

```go
nodes, _ := client.ClickableNodes(ctx, window, nil, 0, true)
defer accessibility.ReleaseNodes(nodes)

for _, elem := range accessibility.FromNodes(nodes) {
	if strings.Contains(elem.Text(), "Save") {
		return client.PerformAction(action.TypeLeftClick, elem.Center(), true, 0)
	}
}
```

### Read the text in one region

```go
frame, err := capture.CaptureRect(ctx, image.Rect(0, 0, 800, 600))
text, err := vision.ReadText(ctx, frame, recognizer, nil)
```

### Find targets when the tree is empty

```go
frame, err := capture.CaptureScreen(ctx)
targets, err := vision.DetectSegments(ctx, frame, segment.TargetsControls)

for _, el := range targets {
	client.PerformAction(action.TypeLeftClick, el.Center(), true, 0)
}
```

### Accessibility first, pixels as the fallback

```go
nodes, err := client.ClickableNodes(ctx, window, nil, 0, true)
if err != nil || len(nodes) == 0 {
	frame, _ := capture.CaptureScreen(ctx)
	els, _ := vision.DetectContours(ctx, frame, contour.DefaultParams())
	// ... click els by coordinate
}
```

### Degrade deliberately

```go
if errs.IsNotSupported(err) {
	// The facility is absent on this machine; fall back or skip the feature.
}
```

Every platform gap reports `CodeNotSupported`; none pretends to succeed with an
empty result.

## Examples

`go run ./examples/<name>` runs each one. The offline examples
(`elements`, `errors`, `pixels`, `custom-engine`, `compositor`) need no desktop.

| Example | Shows |
| --- | --- |
| `accessibility` | Client setup, frontmost window, clickable scan, node release. |
| `hints` | A hints-mode scan that prints roles and geometry, not text. |
| `roles` | Neutral/native role resolution, diagnostics, vocabulary tables. |
| `elements` | Element model, options, `FromNode`, `Nearest`/`Toward`. |
| `actions` | Clicks, moves, scrolls, drags and modifier masks. |
| `errors` | Creating, wrapping, matching and inspecting errors. |
| `capture` | Whole-screen and region capture; frame fields. |
| `pixels` | `Frame`, `Crop`, `DeriveScale`, downsample/resample/rows. |
| `vision` | Contour and segment detection over a real screen. |
| `ocr` | The OCR facade, `ReadText` and the full `Pipeline`. |
| `ocr-engines` | Explicit winrt / visionframework / tesseract selection per OS. |
| `custom-engine` | Implementing `engine.Recognizer` without any native engine. |
| `custom-backend` | Implementing `ax.Client`; the facade works against it unchanged. |
| `windows` | Window enumeration, frontmost window, activation by title, and placement. |
| `compositor` | Wayland window-origin placement math. |

## Platform support

| Capability | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Read accessibility tree | AX (ApplicationServices) | AT-SPI (D-Bus) | UI Automation (COM) |
| Clickable-node scan | yes | yes | yes |
| Frontmost window / app | yes | yes, compositor-first | yes |
| Window enumeration / activation | AX windows + `NSRunningApplication` | EWMH over X11 | `EnumWindows` + `SetForegroundWindow` |
| Window move / resize | `AXPosition` / `AXSize` | `_NET_MOVERESIZE_WINDOW` | `SetWindowPos` (DWM border compensated) |
| Menu bar scan | yes | — | — |
| Dock / Notification Center / Stage Manager / PiP / screen-capture UI | yes (`SurfaceNodes`) | — | — |
| Mission Control windows + Spaces bar | yes | — | — |
| Mouse click / move / scroll | CGEvent | XTest (X11), libei (Wayland), uinput wheel | `SendInput` |
| Smooth cursor motion | yes (`SmoothSteps`) | — | — |
| Screen capture | ScreenCaptureKit (macOS 14+), `screencapture` fallback | X11 root, wlr-screencopy (Wayland) | GDI `BitBlt` |
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
  API plus the private SkyLight query Neru uses. Window enumeration and
  activation read AX window objects, so they need the Accessibility permission
  and report `CodeAccessibilityDenied` without it; a minimized window is still
  listed, and its bounds are empty. Activation uses `NSRunningApplication`, then
  raises the matched window with `AXRaise`. `SetBounds` writes `AXPosition` and
  `AXSize`; a full-screen window refuses the write, and that refusal is reported
  rather than retried.
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
  previous values. Window management is EWMH over X11 only: a Wayland session
  reports `CodeNotSupported` instead of a partial XWayland client list, and a
  window's bounds are its client area, not the decorations the window manager
  draws around it. `SetBounds` is a `_NET_MOVERESIZE_WINDOW` request, so a tiling
  or snapping manager may apply its own rectangle; a request the manager never
  applies is reported as `CodeAccessibilityFailed`.
- **Windows.** There is no menu-bar or Mission Control analogue. Clicking is
  coordinate injection, not a UIA Invoke, so it behaves like a real mouse. Each
  query creates and releases its own UIA client object, so no COM object is
  shared across goroutines. `capture.ExcludeOwnWindows` marks the process's
  top-level windows with `WDA_EXCLUDEFROMCAPTURE` (Windows 10 2004+), so a hint
  overlay does not appear in the frame it detects over. Window enumeration walks
  `EnumWindows` once, skips DWM-cloaked windows (UWP ghosts), and reports DWM's
  extended frame bounds rather than the invisible resize borders. Activation
  un-minimizes first, then defeats the foreground lock by attaching to the
  foreground thread's input queue; a window on another virtual desktop is still
  refused, and the call reports that rather than claiming success. A window
  whose process cannot be opened (an elevated application) is listed with an
  empty `Application`. `SetBounds` gives the window the frame the caller names,
  not a few pixels inside it: `SetWindowPos` addresses the window rectangle, so
  the backend adds the invisible DWM resize borders back before moving it.

## Design notes

**Layering.** Each vertical is facade → backends → contract. `accessibility`
selects a backend; `accessibility/ax` is the contract those backends implement;
`vision/ocr` selects an engine, `vision/ocr/engine` is the contract, and the
engines sit beside it. The domain packages (`vision`, `element`) import the
contract, never a backend, so importing them links no platform code and the
facade stays the only place a platform is chosen.

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

**One window contract, three mechanisms.** `window` keeps the platform's
vocabulary out of the API: no handle type, no XID, no AXUIElement crosses it,
only an opaque `Info.ID`. The backends differ far more than the accessibility
ones — Win32 enumerates the whole desktop in one callback pass, needs a
thread-attachment dance to take the foreground and compensates DWM's invisible
borders before `SetWindowPos`; macOS reads AX windows per application, activates
through AppKit and writes `AXPosition`/`AXSize`; X11 reads an EWMH property and
sends `_NET_ACTIVE_WINDOW` or `_NET_MOVERESIZE_WINDOW` client messages — which is
exactly why none of that is the caller's problem.

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
`integration && <os>` and are not part of `go test ./...`. See
[`docs/LIVE-VERIFICATION.md`](./docs/LIVE-VERIFICATION.md) for the per-platform
checklist.

## License

MIT, the same license as Neru, from which this module is extracted. See
`LICENSE`.
