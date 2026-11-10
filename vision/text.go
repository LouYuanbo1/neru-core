package vision

import (
	"context"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// ReadText recognizes the text in a frame and returns it joined into one
// string, in the engine's reading order.
//
// It is the "read what is on screen" operation a copy-text or search feature
// needs, without the element classification the Pipeline adds. A recognizer
// that cannot run reports errs.CodeNotSupported through Health before any
// pixels are read.
//
// The result is screen content. The caller owns it; nothing here logs it.
func ReadText(
	ctx context.Context,
	frame Frame,
	recognizer ocr.Recognizer,
	opts ocr.Options,
) (string, error) {
	if recognizer == nil {
		return "", errs.New(errs.CodeInvalidInput, "ReadText needs a text recognizer")
	}

	f := frame

	if err := f.checkRegion(ctx, StrategyVision); err != nil {
		return "", err
	}

	if err := ocr.Health(ctx, recognizer); err != nil {
		return "", err
	}

	words, err := recognizer.Recognize(ctx, f.Img, opts)
	if err != nil {
		return "", err
	}

	var builder strings.Builder

	for _, word := range words {
		text := strings.TrimSpace(word.Text)
		if text == "" {
			continue
		}

		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}

		builder.WriteString(text)
	}

	return builder.String(), nil
}
