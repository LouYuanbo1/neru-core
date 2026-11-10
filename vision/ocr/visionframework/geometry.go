//go:build darwin

package visionframework

import (
	"image"
	"math"
	"sort"
	"unicode"

	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// normalizedRect is one rectangle as Vision reports it: the origin at the
// image's bottom-left corner with y growing upward, and both axes scaled to
// 0..1 of the image's width and height.
type normalizedRect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// normalizedRectOf converts the CGRect a bounding box is read as.
func normalizedRectOf(box cgRect) normalizedRect {
	return normalizedRect{
		X:      box.Origin.X,
		Y:      box.Origin.Y,
		Width:  box.Size.Width,
		Height: box.Size.Height,
	}
}

// pixelRect maps a Vision rectangle onto pixels of the image bounds describes,
// which is what the ocr contract asks for: (0,0) at the image's top-left
// corner with y growing downward. bounds supplies the image's size only; its
// own origin is ignored, because Vision's coordinates are relative to the
// image and never to where the image sits on screen.
//
// The result is rounded to whole pixels and clipped to the image, so a box
// Vision reports partly or wholly outside the frame cannot put a caller's hint
// off the picture. A rectangle with no area, no finite coordinates, or nothing
// left after clipping is returned as the zero Rectangle, which Empty reports.
func pixelRect(n normalizedRect, bounds image.Rectangle) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return image.Rectangle{}
	}

	if n.Width <= 0 || n.Height <= 0 || !finiteRect(n) {
		return image.Rectangle{}
	}

	// Clamp in normalized space first: an out-of-range coordinate then cannot
	// overflow the int conversion, and the mapping stays monotone so the
	// rectangle is never inverted.
	wide, tall := float64(width), float64(height)
	left := clamp01(n.X) * wide
	right := clamp01(n.X+n.Width) * wide
	bottom := clamp01(n.Y) * tall
	top := clamp01(n.Y+n.Height) * tall

	rect := image.Rect(
		int(math.Round(left)),
		int(math.Round(tall-top)),
		int(math.Round(right)),
		int(math.Round(tall-bottom)),
	)

	return rect.Intersect(image.Rect(0, 0, width, height))
}

// finiteRect reports whether every coordinate of n is a real number.
func finiteRect(n normalizedRect) bool {
	for _, value := range [...]float64{n.X, n.Y, n.Width, n.Height} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}

	return true
}

// clamp01 limits value to 0..1.
func clamp01(value float64) float64 {
	return math.Min(math.Max(value, 0), 1)
}

// wordSpan is one word of a recognized line, with the range
// boundingBoxForRange: takes. Vision measures that range in UTF-16 code units,
// like NSRange, not in bytes or runes.
type wordSpan struct {
	Text     string
	Location uintptr
	Length   uintptr
}

// wordSpans splits a recognized line into whitespace-delimited words and
// reports each word's UTF-16 range. Vision boxes that exact range, so the
// split decides grouping, not where the box lands.
//
// It splits on Unicode whitespace only. NSString's by-words enumeration, which
// would need an Objective-C block, also breaks at punctuation and at script
// boundaries: "don't" is one run here and two there. Both are defensible
// groupings of on-screen text; this one keeps identifiers and numbers whole,
// which is what a hint on a button wants.
func wordSpans(text string) []wordSpan {
	var spans []wordSpan

	unit := 0

	start, startUnit := -1, 0

	for offset, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}

		if unicode.IsSpace(r) {
			if start >= 0 {
				spans = append(spans, wordSpan{
					Text:     text[start:offset],
					Location: uintptr(startUnit),
					Length:   uintptr(unit - startUnit),
				})

				start = -1
			}
		} else if start < 0 {
			start, startUnit = offset, unit
		}

		unit += width
	}

	if start >= 0 {
		spans = append(spans, wordSpan{
			Text:     text[start:],
			Location: uintptr(startUnit),
			Length:   uintptr(unit - startUnit),
		})
	}

	return spans
}

// sortReadingOrder reorders words the way a person reads them: lines from the
// top of the image down, and left to right within a line.
//
// Words are first sorted by their vertical center, then each run that shares a
// line is sorted by left edge. Two boxes share a line when their centers
// differ by no more than half the shorter box's height, the tolerance the
// original daemon used: Vision returns words of one line with different
// heights, so an exact center match would split them into separate lines.
func sortReadingOrder(words []ocr.Word) {
	if len(words) < 2 {
		return
	}

	sort.SliceStable(words, func(i, j int) bool {
		left, right := centerY(words[i].Bounds), centerY(words[j].Bounds)
		if left != right {
			return left < right
		}

		return words[i].Bounds.Min.X < words[j].Bounds.Min.X
	})

	for start := 0; start < len(words); {
		end := start + 1
		for end < len(words) && sameLine(words[start], words[end]) {
			end++
		}

		line := words[start:end]
		sort.SliceStable(line, func(i, j int) bool {
			return line[i].Bounds.Min.X < line[j].Bounds.Min.X
		})

		start = end
	}
}

// sameLine reports whether two word boxes sit on one line.
func sameLine(a, b ocr.Word) bool {
	tolerance := math.Min(
		float64(a.Bounds.Dy()),
		float64(b.Bounds.Dy()),
	) / 2

	return math.Abs(centerY(a.Bounds)-centerY(b.Bounds)) <= tolerance
}

// centerY returns a rectangle's vertical center, the point a line shares.
func centerY(rect image.Rectangle) float64 {
	return (float64(rect.Min.Y) + float64(rect.Max.Y)) / 2
}
