//go:build integration && windows

package winrt

import (
	"context"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// These are the only tests that run Windows.Media.Ocr. Everything else about the
// backend, such as how a word's box lands in the frame and which failures a
// caller should stop retrying, is decided in Go and tested without an engine.
// Whether the bridge actually reads text cannot be.
//
// A machine with no OCR language pack skips them rather than failing: the engine
// is an optional Windows component, and its absence is what Health reports.
//
// Run with: go test -tags=integration ./vision/ocr/winrt/

// requireRecognizer opens a recognizer and checks that this machine can
// recognize, skipping the test when no OCR language pack is installed.
func requireRecognizer(t *testing.T, options ...Options) *Recognizer {
	t.Helper()

	recognizer, err := New(options...)
	if err != nil {
		skipOrFail(t, "New", err)
	}

	t.Cleanup(func() { _ = recognizer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if healthErr := recognizer.Health(ctx); healthErr != nil {
		skipOrFail(t, "Health", healthErr)
	}

	return recognizer
}

// skipOrFail turns "this machine has no engine" into a skip and every other
// failure into a test failure.
func skipOrFail(t *testing.T, what string, err error) {
	t.Helper()

	if errs.IsNotSupported(err) {
		t.Skipf("no usable OCR engine (%s): %v", what, err)
	}

	t.Fatalf("%s: %v", what, err)
}

// TestIntegration_Health_ReportsAnEngine pins that the probe the vision pipeline
// runs before capturing answers on a real machine, and that a second probe finds
// the engine already made.
func TestIntegration_Health_ReportsAnEngine(t *testing.T) {
	recognizer := requireRecognizer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := recognizer.Health(ctx); err != nil {
		t.Fatalf("second Health: %v", err)
	}
}

// TestIntegration_Recognize_BlankFrameIsNotAnError pins the difference between
// "there is no text on screen" and "recognition failed". A caller that could not
// tell them apart would report an error every time a user pointed hints at an
// image viewer.
func TestIntegration_Recognize_BlankFrameIsNotAnError(t *testing.T) {
	recognizer := requireRecognizer(t)

	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, wordLevel := range []bool{true, false} {
		words, err := recognizer.Recognize(ctx, img, ocr.Options{WordLevel: wordLevel})
		if err != nil {
			t.Fatalf("Recognize(WordLevel=%v) on a blank frame: %v", wordLevel, err)
		}

		if len(words) != 0 {
			t.Errorf("found %d runs of text in a blank frame (WordLevel=%v)", len(words), wordLevel)
		}
	}
}

// TestIntegration_Recognize_ReadsTheFixture is the acceptance check: a word
// rendered into a frame at a known place comes back with that word and a box
// over it, in the frame's own pixel space. The fixture is drawn with Segoe UI at
// 72pt into an 800x200 frame, with the text starting at (40, 40).
func TestIntegration_Recognize_ReadsTheFixture(t *testing.T) {
	recognizer := requireRecognizer(t)

	img := loadFixture(t, filepath.Join("testdata", "hello.png"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	words, err := recognizer.Recognize(ctx, img, ocr.Options{WordLevel: true})
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}

	if len(words) == 0 {
		t.Fatal("recognized nothing in the fixture")
	}

	drawn := image.Rect(40, 40, 400, 180)

	found := false

	for _, word := range words {
		if !word.Bounds.In(img.Bounds()) {
			t.Errorf("word box %v is outside the %v frame", word.Bounds, img.Bounds())
		}

		if !word.Bounds.Overlaps(drawn) {
			t.Errorf("word box %v does not overlap the %v the text was drawn in", word.Bounds, drawn)
		}

		if word.Confidence != wordConfidence {
			t.Errorf("word confidence = %v, want %v", word.Confidence, wordConfidence)
		}

		if strings.TrimSpace(word.Text) != word.Text || word.Text == "" {
			t.Errorf("word text is empty or untrimmed (%d characters)", len(word.Text))
		}

		if strings.EqualFold(word.Text, "HELLO") {
			found = true
		}
	}

	if !found {
		// The frame is a rendered fixture, not a screen, so its text is not
		// anybody's content; only the count is logged either way.
		t.Errorf("recognized %d runs, none of them the fixture's word", len(words))
	}

	lines, err := recognizer.Recognize(ctx, img, ocr.Options{})
	if err != nil {
		t.Fatalf("Recognize(line level): %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("line level returned %d runs, want 1", len(lines))
	}

	if !strings.Contains(strings.ToUpper(lines[0].Text), "HELLO") {
		t.Errorf("line text does not contain the fixture's word (%d characters)", len(lines[0].Text))
	}
}

// TestIntegration_Recognize_AcceptsAFrameWiderThanTheEngineLimit pins the
// resample. A frame past MaxImageDimension, which any 4K monitor is, is read
// rather than refused, and comes back in its own coordinate space.
func TestIntegration_Recognize_AcceptsAFrameWiderThanTheEngineLimit(t *testing.T) {
	recognizer := requireRecognizer(t)

	img := image.NewRGBA(image.Rect(0, 0, 3840, 200))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	words, err := recognizer.Recognize(ctx, img, ocr.Options{WordLevel: true})
	if err != nil {
		t.Fatalf("Recognize on a 3840-wide frame: %v", err)
	}

	for _, word := range words {
		if !word.Bounds.In(img.Bounds()) {
			t.Errorf("word box %v is outside the %v frame it was read from", word.Bounds, img.Bounds())
		}
	}
}

// TestIntegration_Recognize_HonorsTheBudget pins the two answers a caller
// branches on: an expired deadline is a timeout and a canceled context is a
// cancellation, even before the engine is asked to work.
func TestIntegration_Recognize_HonorsTheBudget(t *testing.T) {
	recognizer := requireRecognizer(t)

	img := loadFixture(t, filepath.Join("testdata", "hello.png"))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := recognizer.Recognize(canceled, img, ocr.Options{}); !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("canceled context code = %s, want %s", errs.GetCode(err), errs.CodeContextCanceled)
	}

	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()

	if _, err := recognizer.Recognize(expired, img, ocr.Options{}); !errs.IsCode(err, errs.CodeTimeout) {
		t.Errorf("expired deadline code = %s, want %s", errs.GetCode(err), errs.CodeTimeout)
	}
}

// TestIntegration_Health_LanguageWithoutAPack pins the per-language path: a
// caller that names one language gets either that engine or a refusal naming
// what to install, never a silent fallback to a different language.
func TestIntegration_Health_LanguageWithoutAPack(t *testing.T) {
	recognizer, err := New(Options{Languages: []string{"zz-ZZ"}})
	if err != nil {
		skipOrFail(t, "New", err)
	}
	t.Cleanup(func() { _ = recognizer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	healthErr := recognizer.Health(ctx)
	if healthErr == nil {
		t.Skip("this machine has an OCR pack for a language it should not")
	}

	if !errs.IsNotSupported(healthErr) {
		t.Fatalf("Health with an uninstalled language code = %s, want %s",
			errs.GetCode(healthErr), errs.CodeNotSupported)
	}

	if !strings.Contains(healthErr.Error(), "Basic typing") {
		t.Errorf("refusal %q does not name the remedy", healthErr.Error())
	}

	// Recognize must answer the same way, so a caller that skips Health is not
	// told something different.
	if _, err := recognizer.Recognize(ctx, loadFixture(t, filepath.Join("testdata", "hello.png")), ocr.Options{}); !errs.IsNotSupported(err) {
		t.Errorf("Recognize with an uninstalled language code = %s, want %s",
			errs.GetCode(err), errs.CodeNotSupported)
	}
}

// TestIntegration_Recognize_PerCallLanguageOverrides pins that the per-call
// language list is what builds the engine, which is how a caller reads a screen
// in one language out of a process configured for another.
func TestIntegration_Recognize_PerCallLanguageOverrides(t *testing.T) {
	recognizer := requireRecognizer(t)

	img := loadFixture(t, filepath.Join("testdata", "hello.png"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	words, err := recognizer.Recognize(ctx, img, ocr.Options{
		Languages: []string{"en-US"},
		WordLevel: true,
	})
	if err != nil {
		// A machine whose language packs do not include English is not a
		// failure of this code path: the refusal itself is asserted in
		// TestIntegration_Health_LanguageWithoutAPack.
		if errs.IsNotSupported(err) {
			t.Skipf("no English OCR pack on this machine: %v", err)
		}

		t.Fatalf("Recognize with a per-call language: %v", err)
	}

	if len(words) == 0 {
		t.Error("recognized nothing with an explicit language")
	}
}

// loadFixture decodes a fixture frame into the RGBA a capture produces.
func loadFixture(t *testing.T, path string) *image.RGBA {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	decoded, err := png.Decode(file)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	frame := image.NewRGBA(decoded.Bounds())
	draw.Draw(frame, frame.Rect, decoded, decoded.Bounds().Min, draw.Src)

	return frame
}
