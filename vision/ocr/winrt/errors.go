package winrt

import (
	"context"
	"errors"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// languagePackRemedy is the part of every "no engine" message that tells the
// reader what to install. Windows ships the OCR model separately from the
// language, as that language's Basic typing feature, so a machine that runs
// Windows fine can still have none.
const languagePackRemedy = "in Settings > Time & language > Language & region, open a " +
	"language's options and install Basic typing, or run " +
	`Add-WindowsCapability -Online -Name "Language.OCR~~~en-US~0.0.1.0"`

// hresultFailed reports whether hresult is a failure HRESULT, the high bit set.
func hresultFailed(hresult uintptr) bool {
	return int32(uint32(hresult)) < 0
}

// hresultError names the call that failed and the HRESULT it returned. The code
// is the one thing a reader can look up, so it is spelled the way the SDK spells
// it; the message carries nothing that came off the screen.
func hresultError(operation string, hresult uintptr) error {
	return errs.Newf(
		errs.CodeAccessibilityFailed,
		"%s failed with HRESULT 0x%08X",
		operation,
		uint32(hresult),
	)
}

// contextError turns a finished context into the contract's error: a deadline
// that expired is CodeTimeout, an explicit cancellation is
// CodeContextCanceled, and both keep the context's error as their cause so
// errors.Is reaches it.
func contextError(ctx context.Context, operation string) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errs.Wrap(
			ctx.Err(),
			errs.CodeTimeout,
			operation+" ran past its deadline; raise the caller's budget, or read a smaller region",
		)
	case errors.Is(ctx.Err(), context.Canceled):
		return errs.Wrap(ctx.Err(), errs.CodeContextCanceled, operation+" canceled")
	default:
		return errs.New(errs.CodeTimeout, operation+" stopped without a deadline")
	}
}

// errNoLanguagePack is the answer for a machine whose language profile has no
// OCR pack at all. It is not a failure of the caller's request, so it carries
// CodeNotSupported and the install steps.
func errNoLanguagePack() error {
	return errs.New(
		errs.CodeNotSupported,
		"no OCR language pack is installed for any of this account's languages; "+languagePackRemedy,
	)
}

// errNoLanguagePackFor is the same answer for an explicit language order: every
// tag the caller named resolved to no installed OCR pack.
func errNoLanguagePackFor(tags []string) error {
	return errs.Newf(
		errs.CodeNotSupported,
		"no OCR language pack is installed for %s; %s",
		strings.Join(tags, ", "),
		languagePackRemedy,
	)
}

// errEmptyImage refuses a frame with no pixels. Reading "no text" out of an
// image the engine never looked at would be a lie a caller cannot tell from a
// blank screen, so it is an input error instead.
func errEmptyImage() error {
	return errs.New(errs.CodeInvalidInput, "text recognition needs an image with pixels in it")
}

// errClosed refuses use after Close.
func errClosed() error {
	return errs.New(errs.CodeInvalidInput, "the text recognizer is closed")
}

// errTooThinToShrink refuses a frame whose short side disappears when the frame
// is shrunk to fit the engine's image limit. No screen produces one, so it is
// reported as bad input rather than as a recognition the engine refused.
func errTooThinToShrink() error {
	return errs.New(errs.CodeInvalidInput, "the frame is too thin to shrink for the OCR engine")
}

// errNotWindows is what a non-Windows build answers.
func errNotWindows() error {
	return errs.New(
		errs.CodeNotSupported,
		"text recognition through Windows.Media.Ocr needs Windows 10 or 11; use another ocr backend on this platform",
	)
}
