package winrt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestHresultFailed_ReadsTheHighBit pins the one test every HRESULT check in
// this package is built on.
func TestHresultFailed_ReadsTheHighBit(t *testing.T) {
	tests := []struct {
		name     string
		hresult  uintptr
		wantFail bool
	}{
		{name: "S_OK", hresult: 0x00000000, wantFail: false},
		{name: "S_FALSE", hresult: 0x00000001, wantFail: false},
		{name: "E_ACCESSDENIED", hresult: 0x80070005, wantFail: true},
		{name: "E_FAIL", hresult: 0x80004005, wantFail: true},
		{name: "E_POINTER", hresult: 0x80004003, wantFail: true},
		{name: "RPC_E_CHANGED_MODE", hresult: 0x80010106, wantFail: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := hresultFailed(testCase.hresult); got != testCase.wantFail {
				t.Errorf("hresultFailed(0x%08X) = %v, want %v",
					uint32(testCase.hresult), got, testCase.wantFail)
			}
		})
	}
}

// TestHresultError_NamesTheCallAndCode pins that a failure is diagnosable: the
// HRESULT is the only thing a reader can look up, so it must survive.
func TestHresultError_NamesTheCallAndCode(t *testing.T) {
	err := hresultError("OcrEngine.RecognizeAsync", 0x80004005)

	if !errs.IsCode(err, errs.CodeAccessibilityFailed) {
		t.Errorf("code = %s, want %s", errs.GetCode(err), errs.CodeAccessibilityFailed)
	}

	message := err.Error()

	for _, want := range []string{"OcrEngine.RecognizeAsync", "0x80004005"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not contain %q", message, want)
		}
	}
}

// TestErrNoLanguagePack_NamesTheRemedy pins the contract's requirement that an
// unsupported engine says what to install. It is the message a user acts on.
func TestErrNoLanguagePack_NamesTheRemedy(t *testing.T) {
	err := errNoLanguagePack()

	if !errs.IsNotSupported(err) {
		t.Errorf("code = %s, want %s", errs.GetCode(err), errs.CodeNotSupported)
	}

	for _, want := range []string{"Basic typing", "Add-WindowsCapability", "Settings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
}

// TestErrNoLanguagePackFor_NamesTheLanguages pins the explicit-language answer:
// a caller that asked for a language it cannot have is told which ones.
func TestErrNoLanguagePackFor_NamesTheLanguages(t *testing.T) {
	err := errNoLanguagePackFor([]string{"en-US", "ja"})

	if !errs.IsNotSupported(err) {
		t.Errorf("code = %s, want %s", errs.GetCode(err), errs.CodeNotSupported)
	}

	for _, want := range []string{"en-US", "ja", "Basic typing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
}

// TestContextError_MapsDeadlinesAndCancellation pins the two codes a caller
// branches on to tell "too slow" from "no longer wanted", including the cause
// errors.Is needs to reach.
func TestContextError_MapsDeadlinesAndCancellation(t *testing.T) {
	tests := []struct {
		name    string
		ctx     func() context.Context
		code    errs.Code
		cause   error
		wantMsg string
	}{
		{
			name: "an expired deadline is a timeout",
			ctx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 0)
				cancel()

				return ctx
			},
			code:    errs.CodeTimeout,
			cause:   context.DeadlineExceeded,
			wantMsg: "deadline",
		},
		{
			name: "a cancellation is a cancellation",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				return ctx
			},
			code:    errs.CodeContextCanceled,
			cause:   context.Canceled,
			wantMsg: "canceled",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := contextError(testCase.ctx(), "text recognition")

			if !errs.IsCode(err, testCase.code) {
				t.Errorf("code = %s, want %s", errs.GetCode(err), testCase.code)
			}

			if !errors.Is(err, testCase.cause) {
				t.Errorf("errors.Is(%v, %v) = false, want true", err, testCase.cause)
			}

			if !strings.Contains(err.Error(), testCase.wantMsg) {
				t.Errorf("message %q does not mention %q", err.Error(), testCase.wantMsg)
			}
		})
	}
}

// TestContextError_UndeadlinedContextIsStillATimeout pins the fallback: this is
// only reached if a context reports done with no error, and reporting a timeout
// beats reporting nothing.
func TestContextError_UndeadlinedContextIsStillATimeout(t *testing.T) {
	err := contextError(context.Background(), "text recognition")

	if !errs.IsCode(err, errs.CodeTimeout) {
		t.Errorf("code = %s, want %s", errs.GetCode(err), errs.CodeTimeout)
	}
}

// TestInputErrors_AreInvalidInput pins the refusals a caller can hit without any
// engine being involved.
func TestInputErrors_AreInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "an image with no pixels", err: errEmptyImage()},
		{name: "a frame too thin to shrink", err: errTooThinToShrink()},
		{name: "a closed recognizer", err: errClosed()},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if !errs.IsCode(testCase.err, errs.CodeInvalidInput) {
				t.Errorf("code = %s, want %s", errs.GetCode(testCase.err), errs.CodeInvalidInput)
			}
		})
	}
}

// TestErrNotWindows_IsNotSupported pins the non-Windows answer. It is built on
// every platform so the stub's message cannot drift with the build tag.
func TestErrNotWindows_IsNotSupported(t *testing.T) {
	if err := errNotWindows(); !errs.IsNotSupported(err) {
		t.Errorf("code = %s, want %s", errs.GetCode(err), errs.CodeNotSupported)
	}
}
