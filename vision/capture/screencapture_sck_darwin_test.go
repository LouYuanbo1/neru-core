//go:build darwin

package capture

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The block literal is raw memory the Objective-C runtime reads, so its layout
// is part of the contract with libSystem and is pinned here field by field.
func TestBlockLiteral_MatchesTheRuntimeLayout(t *testing.T) {
	t.Parallel()

	if got := unsafe.Offsetof(blockLiteral{}.isa); got != 0 {
		t.Errorf("isa offset = %d, want 0", got)
	}

	if got := unsafe.Offsetof(blockLiteral{}.flags); got != 8 {
		t.Errorf("flags offset = %d, want 8", got)
	}

	if got := unsafe.Offsetof(blockLiteral{}.reserved); got != 12 {
		t.Errorf("reserved offset = %d, want 12", got)
	}

	if got := unsafe.Offsetof(blockLiteral{}.invoke); got != 16 {
		t.Errorf("invoke offset = %d, want 16", got)
	}

	if got := unsafe.Offsetof(blockLiteral{}.descriptor); got != 24 {
		t.Errorf("descriptor offset = %d, want 24", got)
	}

	if size := unsafe.Sizeof(blockLiteral{}); size != 32 {
		t.Errorf("sizeof blockLiteral = %d, want 32", size)
	}

	if size := unsafe.Sizeof(blockLiteral{}.flags) + unsafe.Sizeof(blockLiteral{}.reserved); size != 8 {
		t.Errorf("flags and reserved occupy %d bytes, want 8", size)
	}
}

func TestBlockDescriptor_HoldsTheLiteralSize(t *testing.T) {
	t.Parallel()

	if got := unsafe.Offsetof(blockDescriptor{}.reserved); got != 0 {
		t.Errorf("reserved offset = %d, want 0", got)
	}

	if got := unsafe.Offsetof(blockDescriptor{}.size); got != 8 {
		t.Errorf("size offset = %d, want 8", got)
	}

	if size := unsafe.Sizeof(blockDescriptor{}); size != 16 {
		t.Errorf("sizeof blockDescriptor = %d, want 16", size)
	}

	for name, descriptor := range map[string]blockDescriptor{
		"content": contentBlockDescriptor,
		"screen":  screenBlockDescriptor,
	} {
		if want := unsafe.Sizeof(blockLiteral{}); descriptor.size != want {
			t.Errorf("%s descriptor size = %d, want %d", name, descriptor.size, want)
		}

		if descriptor.reserved != 0 {
			t.Errorf("%s descriptor reserved = %d, want 0", name, descriptor.reserved)
		}
	}
}

// TestLoadSCK_BindsOrReportsNotSupported also checks the block literals were
// installed: a failure to bind must be reported as CodeNotSupported, and a
// success must leave both handlers callable.
func TestLoadSCK_BindsOrReportsNotSupported(t *testing.T) {
	first, firstErr := loadSCK()
	second, secondErr := loadSCK()

	if first != second {
		t.Error("loadSCK returned two different bindings")
	}

	if (firstErr == nil) != (secondErr == nil) {
		t.Fatalf("loadSCK is not idempotent: %v then %v", firstErr, secondErr)
	}

	if firstErr != nil {
		if !errs.IsNotSupported(firstErr) {
			t.Fatalf("loadSCK error = %v, want CodeNotSupported", firstErr)
		}

		t.Skipf("ScreenCaptureKit is unavailable here: %v", firstErr)
	}

	if first.classScreenshotManager == 0 || first.classShareableContent == 0 {
		t.Error("ScreenCaptureKit classes were not resolved")
	}

	if first.sel.captureImage == 0 || first.sel.shareableContent == 0 {
		t.Error("ScreenCaptureKit selectors were not resolved")
	}

	if contentBlock.invoke == 0 || screenBlock.invoke == 0 {
		t.Fatal("a completion block has no invoke function")
	}

	for name, block := range map[string]blockLiteral{"content": contentBlock, "screen": screenBlock} {
		if block.isa == 0 {
			t.Errorf("%s block has no class", name)
		}

		if block.flags != sckBlockFlagIsGlobal {
			t.Errorf("%s block flags = %#x, want %#x", name, block.flags, sckBlockFlagIsGlobal)
		}

		if block.reserved != 0 {
			t.Errorf("%s block reserved = %d, want 0", name, block.reserved)
		}

		if block.descriptor == 0 {
			t.Errorf("%s block has no descriptor", name)
		}
	}
}

// TestCaptureWithScreenCaptureKit_ExpiredDeadline proves the fast path never
// panics when it cannot work: an expired deadline is answered before any
// framework call is made.
func TestCaptureWithScreenCaptureKit_ExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	shot, err := captureWithScreenCaptureKit(ctx, image.Rectangle{})
	if err == nil {
		t.Fatal("captureWithScreenCaptureKit succeeded with an expired deadline")
	}

	if !errs.IsCode(err, errs.CodeTimeout) {
		t.Errorf("error = %v, want CodeTimeout", err)
	}

	if shot.Img != nil {
		t.Error("a failed capture returned pixels")
	}
}

func TestCaptureWithScreenCaptureKit_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := captureWithScreenCaptureKit(ctx, image.Rectangle{}); !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("error = %v, want CodeContextCanceled", err)
	}
}

// displayFixtures is a two-display desktop with a third display to the left of
// it: the main display at the origin, a secondary one to its right, and one
// with a negative origin, which is how macOS places a display above or left of
// the main one.
func displayFixtures() []displayInfo {
	return []displayInfo{
		{id: 1, bounds: image.Rect(0, 0, 1920, 1080), pixels: image.Point{X: 3840, Y: 2160}},
		{id: 2, bounds: image.Rect(1920, 0, 3840, 1080), pixels: image.Point{X: 1920, Y: 1080}},
		{id: 3, bounds: image.Rect(-1280, 0, 0, 800), pixels: image.Point{X: 1280, Y: 800}},
	}
}

func TestDisplayContaining_PicksTheLargestOverlap(t *testing.T) {
	t.Parallel()

	displays := displayFixtures()

	tests := []struct {
		name   string
		region image.Rectangle
		want   uint32
		found  bool
	}{
		{name: "inside the main display", region: image.Rect(100, 100, 200, 200), want: 1, found: true},
		{name: "inside the secondary display", region: image.Rect(2000, 100, 2100, 200), want: 2, found: true},
		{name: "negative origin display", region: image.Rect(-1000, 100, -900, 200), want: 3, found: true},
		{name: "whole main display", region: image.Rect(0, 0, 1920, 1080), want: 1, found: true},
		{
			name:   "straddling the seam, mostly on the secondary display",
			region: image.Rect(1900, 100, 2000, 200),
			want:   2,
			found:  true,
		},
		{
			name:   "straddling the seam, mostly on the main display",
			region: image.Rect(1800, 100, 1930, 200),
			want:   1,
			found:  true,
		},
		{
			name:   "an exact tie keeps the earlier display",
			region: image.Rect(1919, 0, 1921, 10),
			want:   1,
			found:  true,
		},
		{name: "no display holds it", region: image.Rect(5000, 5000, 5100, 5100)},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			display, found := displayContaining(displays, testCase.region)
			if found != testCase.found {
				t.Fatalf("displayContaining(%v) found = %v, want %v", testCase.region, found, testCase.found)
			}

			if display.id != testCase.want {
				t.Errorf("displayContaining(%v) = display %d, want %d", testCase.region, display.id, testCase.want)
			}
		})
	}
}

func TestDisplayInfo_ScaleIsPixelsPerPoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		display displayInfo
		want    float64
	}{
		{
			name:    "retina main display",
			display: displayInfo{bounds: image.Rect(0, 0, 1920, 1080), pixels: image.Point{X: 3840, Y: 2160}},
			want:    2,
		},
		{
			name:    "non-retina display",
			display: displayInfo{bounds: image.Rect(0, 0, 1920, 1080), pixels: image.Point{X: 1920, Y: 1080}},
			want:    1,
		},
		{
			name:    "unknown pixel size reports 1",
			display: displayInfo{bounds: image.Rect(0, 0, 1920, 1080)},
			want:    1,
		},
		{
			name:    "empty bounds report 1",
			display: displayInfo{pixels: image.Point{X: 3840, Y: 2160}},
			want:    1,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.display.scale(); got != testCase.want {
				t.Errorf("scale() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestClipRegion_StaysInsideTheDisplay(t *testing.T) {
	t.Parallel()

	display := image.Rect(0, 0, 1920, 1080)

	tests := []struct {
		name   string
		region image.Rectangle
		want   image.Rectangle
	}{
		{name: "inside", region: image.Rect(10, 20, 30, 40), want: image.Rect(10, 20, 30, 40)},
		{name: "past the right edge", region: image.Rect(1800, 10, 2100, 100), want: image.Rect(1800, 10, 1920, 100)},
		{name: "past the bottom edge", region: image.Rect(10, 1000, 100, 1200), want: image.Rect(10, 1000, 100, 1080)},
		{name: "past the left edge", region: image.Rect(-50, 10, 50, 100), want: image.Rect(0, 10, 50, 100)},
		{name: "outside", region: image.Rect(2000, 10, 2100, 100), want: image.Rectangle{}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := clipRegion(testCase.region, display); got != testCase.want {
				t.Errorf("clipRegion(%v) = %v, want %v", testCase.region, got, testCase.want)
			}
		})
	}
}

func TestSourceRectIn_IsDisplayLocalPoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		region image.Rectangle
		bounds image.Rectangle
		want   cgRect
	}{
		{
			name:   "main display",
			region: image.Rect(100, 50, 300, 150),
			bounds: image.Rect(0, 0, 1920, 1080),
			want:   cgRect{Origin: cgPoint{X: 100, Y: 50}, Size: cgSize{Width: 200, Height: 100}},
		},
		{
			name:   "display left of the main one",
			region: image.Rect(-1200, 100, -1000, 300),
			bounds: image.Rect(-1280, 0, 0, 800),
			want:   cgRect{Origin: cgPoint{X: 80, Y: 100}, Size: cgSize{Width: 200, Height: 200}},
		},
		{
			name:   "display above the main one",
			region: image.Rect(10, -500, 210, -400),
			bounds: image.Rect(0, -1080, 1920, 0),
			want:   cgRect{Origin: cgPoint{X: 10, Y: 580}, Size: cgSize{Width: 200, Height: 100}},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := sourceRectIn(testCase.region, testCase.bounds); got != testCase.want {
				t.Errorf("sourceRectIn(%v) = %+v, want %+v", testCase.region, got, testCase.want)
			}
		})
	}
}

func TestRegionPixelSize_RoundsToAPixelGrid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		region image.Rectangle
		scale  float64
		wantW  int
		wantH  int
	}{
		{name: "retina", region: image.Rect(0, 0, 100, 50), scale: 2, wantW: 200, wantH: 100},
		{name: "non-retina", region: image.Rect(0, 0, 100, 50), scale: 1, wantW: 100, wantH: 50},
		{name: "fractional scale rounds", region: image.Rect(0, 0, 3, 3), scale: 1.5, wantW: 5, wantH: 5},
		{name: "zero scale means 1", region: image.Rect(0, 0, 7, 9), scale: 0, wantW: 7, wantH: 9},
		{name: "one point always asks for a pixel", region: image.Rect(0, 0, 1, 1), scale: 0.1, wantW: 1, wantH: 1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			width, height := regionPixelSize(testCase.region, testCase.scale)
			if width != testCase.wantW || height != testCase.wantH {
				t.Errorf("regionPixelSize(%v, %v) = %dx%d, want %dx%d",
					testCase.region, testCase.scale, width, height, testCase.wantW, testCase.wantH)
			}
		})
	}
}

func TestCropRGBA_TrimOnlyTrims(t *testing.T) {
	t.Parallel()

	source := image.NewRGBA(image.Rect(0, 0, 10, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 10; x++ {
			source.SetRGBA(x, y, rgbaFixture(x, y))
		}
	}

	t.Run("larger than asked for", func(t *testing.T) {
		t.Parallel()

		cropped := cropRGBA(source, 5, 4)
		if got := cropped.Bounds(); got != image.Rect(0, 0, 5, 4) {
			t.Fatalf("bounds = %v, want 5x4 at the origin", got)
		}

		if cropped == source {
			t.Fatal("a larger image was not trimmed")
		}

		if cropped.Stride < 5*4 {
			t.Errorf("stride = %d, too small for 5 pixels", cropped.Stride)
		}

		for _, point := range []image.Point{{X: 0, Y: 0}, {X: 4, Y: 3}} {
			if want := source.RGBAAt(point.X, point.Y); cropped.RGBAAt(point.X, point.Y) != want {
				t.Errorf("pixel %v changed: got %v, want %v", point, cropped.RGBAAt(point.X, point.Y), want)
			}
		}
	})

	t.Run("already the right size", func(t *testing.T) {
		t.Parallel()

		if cropped := cropRGBA(source, 10, 8); cropped != source {
			t.Error("an image of the requested size was copied")
		}
	})

	t.Run("smaller than asked for", func(t *testing.T) {
		t.Parallel()

		if cropped := cropRGBA(source, 20, 20); cropped != source {
			t.Error("an image smaller than the request was trimmed")
		}
	})

	t.Run("mixed, wider but shorter", func(t *testing.T) {
		t.Parallel()

		cropped := cropRGBA(source, 4, 20)
		if got := cropped.Bounds(); got != image.Rect(0, 0, 4, 8) {
			t.Errorf("bounds = %v, want 4x8", got)
		}
	})

	t.Run("no size", func(t *testing.T) {
		t.Parallel()

		if cropped := cropRGBA(source, 0, 0); cropped != source {
			t.Error("a zero request trimmed the image")
		}
	})
}

// rgbaFixture gives every pixel a distinct colour so a mis-copied row or column
// is visible in the comparison.
func rgbaFixture(x, y int) color.RGBA {
	return color.RGBA{
		R: uint8(x * 7),
		G: uint8(y * 11),
		B: uint8(x + y),
		A: 255,
	}
}

func TestOwnWindowIndexes_SelectsThisProcessesWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pids []int32
		self int32
		want []int
	}{
		{name: "two of four", pids: []int32{100, 42, 42, 7}, self: 42, want: []int{1, 2}},
		{name: "none", pids: []int32{100, 7}, self: 42},
		{name: "no windows", pids: nil, self: 42},
		{name: "every window", pids: []int32{42, 42}, self: 42, want: []int{0, 1}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := ownWindowIndexes(testCase.pids, testCase.self)
			if len(got) != len(testCase.want) {
				t.Fatalf("ownWindowIndexes(%v) = %v, want %v", testCase.pids, got, testCase.want)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("ownWindowIndexes(%v) = %v, want %v", testCase.pids, got, testCase.want)
				}
			}
		})
	}
}

func TestRectFromCG_RoundsDisplayBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rect cgRect
		want image.Rectangle
	}{
		{
			name: "fractional origin and size",
			rect: cgRect{Origin: cgPoint{X: 10.4, Y: 20.6}, Size: cgSize{Width: 100.5, Height: 40.2}},
			want: image.Rect(10, 21, 111, 61),
		},
		{
			name: "display left of the main one",
			rect: cgRect{Origin: cgPoint{X: -1280}, Size: cgSize{Width: 1280, Height: 800}},
			want: image.Rect(-1280, 0, 0, 800),
		},
		{
			name: "empty",
			rect: cgRect{},
			want: image.Rect(0, 0, 0, 0),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := rectFromCG(testCase.rect); got != testCase.want {
				t.Errorf("rectFromCG(%+v) = %v, want %v", testCase.rect, got, testCase.want)
			}
		})
	}
}

// resetPendingSlot puts the package-level slot back to its initial state so one
// test's timeout cannot leak into another's expectation.
func resetPendingSlot(t *testing.T) {
	t.Helper()

	sckPendingMu.Lock()
	sckPendingCh = nil
	sckPendingStale = false
	sckPendingMu.Unlock()
}

// TestPendingSlot_RoutesOneCallAtATime covers the hand-off between a completion
// handler running on ScreenCaptureKit's queue and the goroutine waiting for it.
// The events carry no objects, so no Objective-C message is sent.
func TestPendingSlot_RoutesOneCallAtATime(t *testing.T) {
	resetPendingSlot(t)
	t.Cleanup(func() { resetPendingSlot(t) })

	api := &sck{}

	events, err := sckInstallPending()
	if err != nil {
		t.Fatalf("sckInstallPending: %v", err)
	}

	if _, err := sckInstallPending(); err == nil {
		t.Fatal("a second call claimed the slot while the first was in flight")
	}

	api.deliver(sckStageImage, 0, 0)

	select {
	case event := <-events:
		if event.stage != sckStageImage {
			t.Errorf("stage = %d, want the screenshot stage", event.stage)
		}
	default:
		t.Fatal("the completion was not delivered to the waiting call")
	}

	// The slot is free again, so the next call can claim it.
	next, err := sckInstallPending()
	if err != nil {
		t.Fatalf("sckInstallPending after a delivery: %v", err)
	}

	// A completion nobody waits for must be dropped rather than queued.
	sckAbandonPending(next)
	api.deliver(sckStageContent, 0, 0)
}

// TestPendingSlot_StaysClosedUntilAnAbandonedCompletionArrives is the guard
// that keeps a late completion from being mistaken for the next call's result.
func TestPendingSlot_StaysClosedUntilAnAbandonedCompletionArrives(t *testing.T) {
	resetPendingSlot(t)
	t.Cleanup(func() { resetPendingSlot(t) })

	api := &sck{}

	events, err := sckInstallPending()
	if err != nil {
		t.Fatalf("sckInstallPending: %v", err)
	}

	sckAbandonPending(events)

	if _, err := sckInstallPending(); err == nil {
		t.Fatal("a call started while an abandoned completion was still outstanding")
	} else if !errs.IsCode(err, errs.CodeTimeout) {
		t.Errorf("error = %v, want CodeTimeout", err)
	}

	api.deliver(sckStageContent, 0, 0)

	if _, err := sckInstallPending(); err != nil {
		t.Errorf("the slot stayed closed after the abandoned completion arrived: %v", err)
	}
}

// TestAbandon_ReleasesAResultThatArrivedAsTheDeadlineFired covers a delivery
// and a timeout crossing: the delivery clears the slot, so giving up must still
// drain the channel, or the retained object would never be released.
func TestAbandon_ReleasesAResultThatArrivedAsTheDeadlineFired(t *testing.T) {
	resetPendingSlot(t)
	t.Cleanup(func() { resetPendingSlot(t) })

	api := &sck{}

	events, err := sckInstallPending()
	if err != nil {
		t.Fatalf("sckInstallPending: %v", err)
	}

	api.deliver(sckStageImage, 0, 0)
	api.abandon(events)

	select {
	case <-events:
		t.Error("an abandoned result was left in the channel")
	default:
	}

	if _, err := sckInstallPending(); err != nil {
		t.Errorf("the slot did not reopen after an abandoned delivery: %v", err)
	}
}
