package tesseract

import (
	"context"
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// tsvSample is a synthetic tesseract TSV: a header, two words on one line, and
// one word on the next, plus a row with no text and a negative confidence that
// must be skipped.
const tsvSample = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
	"1\t1\t0\t0\t0\t0\t0\t0\t400\t100\t-1\t\n" +
	"4\t1\t1\t1\t1\t0\t10\t20\t120\t20\t95\t\n" +
	"5\t1\t1\t1\t1\t1\t10\t20\t50\t20\t96\tSave\n" +
	"5\t1\t1\t1\t1\t2\t70\t20\t60\t20\t90\tfile\n" +
	"5\t1\t1\t1\t2\t1\t10\t60\t200\t20\t80\tOpen recent...\n" +
	"5\t1\t1\t1\t3\t1\t10\t90\t0\t0\t-1\t\n"

func TestParseTSV_WordLevel(t *testing.T) {
	t.Parallel()

	words, err := ParseTSV([]byte(tsvSample), true)
	if err != nil {
		t.Fatalf("ParseTSV: %v", err)
	}

	if len(words) != 3 {
		t.Fatalf("len(words) = %d, want 3: %+v", len(words), words)
	}

	first := words[0]
	if first.Text != "Save" || first.Bounds != image.Rect(10, 20, 60, 40) {
		t.Errorf("first word = %+v, want Save at (10,20)-(60,40)", first)
	}

	if first.Confidence < 0.95 || first.Confidence > 0.97 {
		t.Errorf("first confidence = %v, want ~0.96", first.Confidence)
	}
}

func TestParseTSV_LineLevelUnionsWords(t *testing.T) {
	t.Parallel()

	lines, err := ParseTSV([]byte(tsvSample), false)
	if err != nil {
		t.Fatalf("ParseTSV: %v", err)
	}

	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2: %+v", len(lines), lines)
	}

	if lines[0].Text != "Save file" {
		t.Errorf("first line = %q, want %q", lines[0].Text, "Save file")
	}

	wantBounds := image.Rect(10, 20, 130, 40)
	if lines[0].Bounds != wantBounds {
		t.Errorf("first line bounds = %v, want %v", lines[0].Bounds, wantBounds)
	}

	if lines[0].Confidence != 0.9 {
		t.Errorf("line confidence = %v, want the lowest word confidence 0.9", lines[0].Confidence)
	}
}

func TestParseTSV_EmptyOutput(t *testing.T) {
	t.Parallel()

	words, err := ParseTSV([]byte("level\tpage_num\n"), true)
	if err != nil {
		t.Fatalf("ParseTSV: %v", err)
	}

	if len(words) != 0 {
		t.Errorf("len(words) = %d, want 0", len(words))
	}
}

func TestNew_HealthReportsMissingBinary(t *testing.T) {
	t.Parallel()

	recognizer := New(Options{Binary: "definitely-not-tesseract-on-this-machine"})

	if err := recognizer.Health(context.Background()); err == nil {
		t.Fatal("Health(missing binary) = nil, want an error")
	}
}

func TestLanguageOption_PerCallWins(t *testing.T) {
	t.Parallel()

	recognizer := New(Options{Languages: []string{"eng"}})

	if got := recognizer.languageOption(ocr.Options{}); got != "eng" {
		t.Errorf("languageOption() = %q, want eng", got)
	}

	if got := recognizer.languageOption(ocr.Options{Languages: []string{"chi_sim", "eng"}}); got != "chi_sim+eng" {
		t.Errorf("languageOption(per-call) = %q, want chi_sim+eng", got)
	}

	if got := (&Recognizer{}).languageOption(ocr.Options{}); got != "" {
		t.Errorf("languageOption(no languages) = %q, want empty", got)
	}
}
