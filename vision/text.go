package vision

import (
	"context"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// ReadText recognizes the text in a frame and returns it joined into one
// string, in the engine's reading order.
//
// It is the "read what is on screen" operation a copy-text or search feature
// needs, without the element classification the Pipeline adds. A recognizer
// that cannot run reports errs.CodeNotSupported through Health before any
// pixels are read.
//
// opts may be nil, which asks for the engine's defaults and keeps the common
// call free of an import of the engine package. Pass a value to choose the
// languages, word level, or confidence floor.
//
// The result is screen content. The caller owns it; nothing here logs it.
func ReadText(
	ctx context.Context,
	f pixels.Frame,
	recognizer engine.Recognizer,
	opts *engine.Options,
) (string, error) {
	if recognizer == nil {
		return "", errs.New(errs.CodeInvalidInput, "ReadText needs a text recognizer")
	}

	if err := checkRegion(ctx, &f, StrategyVision); err != nil {
		return "", err
	}

	if err := engine.Health(ctx, recognizer); err != nil {
		return "", err
	}

	callOptions := engine.Options{}
	if opts != nil {
		callOptions = *opts
	}

	words, err := recognizer.Recognize(ctx, f.Img, callOptions)
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
