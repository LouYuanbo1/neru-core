//go:build darwin

package visionframework

import (
	"image"
	"math"
	"testing"
	"unicode/utf16"

	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

func TestPixelRect_MapsVisionRectanglesOntoImagePixels(t *testing.T) {
	// A 200x100 image is enough for every case below to land on whole pixels.
	bounds := image.Rect(0, 0, 200, 100)

	cases := []struct {
		name       string
		normalized normalizedRect
		want       image.Rectangle
	}{
		{
			name:       "the whole image maps to the whole image",
			normalized: normalizedRect{X: 0, Y: 0, Width: 1, Height: 1},
			want:       image.Rect(0, 0, 200, 100),
		},
		{
			name:       "vision's bottom-left corner is the image's bottom-left",
			normalized: normalizedRect{X: 0, Y: 0, Width: 0.5, Height: 0.5},
			want:       image.Rect(0, 50, 100, 100),
		},
		{
			name:       "vision's top-right corner is the image's top-right",
			normalized: normalizedRect{X: 0.5, Y: 0.5, Width: 0.5, Height: 0.5},
			want:       image.Rect(100, 0, 200, 50),
		},
		{
			name:       "a word box keeps its edge",
			normalized: normalizedRect{X: 0.25, Y: 0.5, Width: 0.25, Height: 0.1},
			want:       image.Rect(50, 40, 100, 50),
		},
		{
			name:       "a box on the top edge stops at zero",
			normalized: normalizedRect{X: 0, Y: 0.9, Width: 1, Height: 0.1},
			want:       image.Rect(0, 0, 200, 10),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := pixelRect(testCase.normalized, bounds); got != testCase.want {
				t.Fatalf("pixelRect(%+v) = %v, want %v", testCase.normalized, got, testCase.want)
			}
		})
	}
}

func TestPixelRect_ClipsToTheImage(t *testing.T) {
	bounds := image.Rect(0, 0, 200, 100)

	cases := []struct {
		name       string
		normalized normalizedRect
		want       image.Rectangle
	}{
		{
			name:       "a box hanging off the left and bottom is trimmed",
			normalized: normalizedRect{X: -0.1, Y: -0.1, Width: 0.5, Height: 0.5},
			want:       image.Rect(0, 60, 80, 100),
		},
		{
			name:       "a box hanging off the right and top is trimmed",
			normalized: normalizedRect{X: 0.9, Y: 0.9, Width: 0.5, Height: 0.5},
			want:       image.Rect(180, 0, 200, 10),
		},
		{
			name:       "a box wider than the image fills it horizontally",
			normalized: normalizedRect{X: -1, Y: 0.25, Width: 3, Height: 0.5},
			want:       image.Rect(0, 25, 200, 75),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := pixelRect(testCase.normalized, bounds)
			if got != testCase.want {
				t.Fatalf("pixelRect(%+v) = %v, want %v", testCase.normalized, got, testCase.want)
			}

			if !got.In(bounds) && !got.Empty() {
				t.Fatalf("pixelRect(%+v) = %v, which is outside %v", testCase.normalized, got, bounds)
			}
		})
	}
}

func TestPixelRect_DropsRectanglesWithNoArea(t *testing.T) {
	bounds := image.Rect(0, 0, 200, 100)

	cases := []struct {
		name       string
		normalized normalizedRect
		bounds     image.Rectangle
	}{
		{name: "zero width", normalized: normalizedRect{X: 0.1, Y: 0.1, Width: 0, Height: 0.5}, bounds: bounds},
		{name: "zero height", normalized: normalizedRect{X: 0.1, Y: 0.1, Width: 0.5, Height: 0}, bounds: bounds},
		{name: "negative width", normalized: normalizedRect{X: 0.5, Y: 0.1, Width: -0.5, Height: 0.5}, bounds: bounds},
		{name: "NaN origin", normalized: normalizedRect{X: math.NaN(), Y: 0.1, Width: 0.5, Height: 0.5}, bounds: bounds},
		{name: "infinite width", normalized: normalizedRect{X: 0.1, Y: 0.1, Width: math.Inf(1), Height: 0.5}, bounds: bounds},
		{name: "nothing left after clipping", normalized: normalizedRect{X: -0.9, Y: 0.1, Width: 0.1, Height: 0.5}, bounds: bounds},
		{name: "rounded away to nothing", normalized: normalizedRect{X: 0.01, Y: 0.01, Width: 0.001, Height: 0.001}, bounds: image.Rect(0, 0, 10, 10)},
		{name: "empty bounds", normalized: normalizedRect{X: 0, Y: 0, Width: 1, Height: 1}, bounds: image.Rectangle{}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := pixelRect(testCase.normalized, testCase.bounds)
			if !got.Empty() {
				t.Fatalf("pixelRect(%+v) = %v, want the empty rectangle", testCase.normalized, got)
			}
		})
	}
}

func TestPixelRect_IgnoresTheBoundsOrigin(t *testing.T) {
	// A subimage starts partway across its parent. Vision answers relative to
	// the image it was given, so the parent's offset must not move a result.
	offset := image.Rect(400, 300, 600, 400)
	origin := image.Rect(0, 0, 200, 100)

	normalized := normalizedRect{X: 0.25, Y: 0.5, Width: 0.25, Height: 0.1}

	if got, want := pixelRect(normalized, offset), pixelRect(normalized, origin); got != want {
		t.Fatalf("pixelRect with bounds at %v = %v, want %v like an image at the origin", offset.Min, got, want)
	}
}

func TestWordSpans_SplitsOnWhitespaceWithUTF16Ranges(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []wordSpan
	}{
		{
			name: "one word",
			text: "hello",
			want: []wordSpan{{Text: "hello", Location: 0, Length: 5}},
		},
		{
			name: "surrounding and repeated whitespace is skipped",
			text: "  hi  there\n",
			want: []wordSpan{
				{Text: "hi", Location: 2, Length: 2},
				{Text: "there", Location: 6, Length: 5},
			},
		},
		{
			name: "punctuation stays attached to its word",
			text: "don't stop",
			want: []wordSpan{
				{Text: "don't", Location: 0, Length: 5},
				{Text: "stop", Location: 6, Length: 4},
			},
		},
		{
			name: "identifiers and numbers stay whole",
			text: "a-b 1.2",
			want: []wordSpan{
				{Text: "a-b", Location: 0, Length: 3},
				{Text: "1.2", Location: 4, Length: 3},
			},
		},
		{
			name: "a BMP rune is one UTF-16 unit and an astral one is two",
			text: "é 𝄞",
			want: []wordSpan{
				{Text: "é", Location: 0, Length: 1},
				{Text: "𝄞", Location: 2, Length: 2},
			},
		},
		{
			name: "an emoji inside a word counts as two units",
			text: "you😀 now",
			want: []wordSpan{
				{Text: "you😀", Location: 0, Length: 5},
				{Text: "now", Location: 6, Length: 3},
			},
		},
		{name: "empty text", text: "", want: nil},
		{name: "only whitespace", text: " \t\n ", want: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := wordSpans(testCase.text)
			if len(got) != len(testCase.want) {
				t.Fatalf("wordSpans(%q) = %v, want %v", testCase.text, got, testCase.want)
			}

			units := utf16.Encode([]rune(testCase.text))

			for index, span := range got {
				want := testCase.want[index]
				if span != want {
					t.Fatalf("wordSpans(%q)[%d] = %+v, want %+v", testCase.text, index, span, want)
				}

				start, end := int(span.Location), int(span.Location+span.Length)
				if end > len(units) {
					t.Fatalf("wordSpans(%q)[%d] ends at UTF-16 unit %d, past the string's %d", testCase.text, index, end, len(units))
				}

				if decoded := string(utf16.Decode(units[start:end])); decoded != span.Text {
					t.Fatalf("wordSpans(%q)[%d] range decodes to %q, want %q", testCase.text, index, decoded, span.Text)
				}
			}
		})
	}
}

func TestSortReadingOrder_ReadsLinesTopToBottomThenLeftToRight(t *testing.T) {
	words := []engine.Word{
		{Text: "second", Bounds: image.Rect(60, 10, 120, 30)},
		{Text: "below", Bounds: image.Rect(0, 40, 50, 60)},
		{Text: "first", Bounds: image.Rect(0, 12, 50, 28)},
		{Text: "third", Bounds: image.Rect(10, 41, 40, 59)},
	}

	sortReadingOrder(words)

	want := []string{"first", "second", "below", "third"}
	for index, word := range words {
		if word.Text != want[index] {
			t.Fatalf("word %d is %q, want %q (order %v)", index, word.Text, want[index], textOf(words))
		}
	}
}

func TestSortReadingOrder_KeepsAFlushLineOrderWithinItsTolerance(t *testing.T) {
	// The two boxes' centers are four pixels apart, inside half the shorter
	// box's height, so Vision-style words of one line stay together.
	words := []engine.Word{
		{Text: "wide", Bounds: image.Rect(50, 4, 90, 24)},
		{Text: "narrow", Bounds: image.Rect(0, 5, 40, 15)},
		{Text: "next", Bounds: image.Rect(0, 20, 40, 30)},
	}

	sortReadingOrder(words)

	want := []string{"narrow", "wide", "next"}
	for index, word := range words {
		if word.Text != want[index] {
			t.Fatalf("word %d is %q, want %q (order %v)", index, word.Text, want[index], textOf(words))
		}
	}
}

func TestSortReadingOrder_LeavesShortListsAlone(t *testing.T) {
	var nothing []engine.Word

	sortReadingOrder(nothing)

	if nothing != nil {
		t.Fatalf("sortReadingOrder(nil) = %v, want nil", nothing)
	}

	one := []engine.Word{{Text: "only", Bounds: image.Rect(10, 10, 40, 20)}}

	sortReadingOrder(one)

	if len(one) != 1 || one[0].Text != "only" {
		t.Fatalf("sortReadingOrder(one) = %v, want the same single word", one)
	}
}

// textOf lists the words in order, so a failure message shows what came back.
func textOf(words []engine.Word) []string {
	texts := make([]string, len(words))
	for index, word := range words {
		texts[index] = word.Text
	}

	return texts
}
