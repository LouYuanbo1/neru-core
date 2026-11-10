// Package errs provides a small error domain for the neru-core library.
//
// An Error carries a Code that callers branch on, a message, an optional
// cause, and optional context fields. errors.Is matches two Errors by code.
// The code set is deliberately small: it covers only what accessibility and
// vision work needs, with no application-subsystem codes leaking in.
package errs

import (
	"errors"
	"fmt"
)

// Code is a self-describing error code.
type Code string

// Error codes for the accessibility and vision domains.
const (
	// CodeAccessibilityDenied indicates accessibility permissions are not granted.
	CodeAccessibilityDenied Code = "ACCESSIBILITY_DENIED"

	// CodeAccessibilityFailed indicates an accessibility API call failed.
	CodeAccessibilityFailed Code = "ACCESSIBILITY_FAILED"

	// CodeElementNotFound indicates a UI element could not be found.
	CodeElementNotFound Code = "ELEMENT_NOT_FOUND"

	// CodeInvalidInput indicates invalid input parameters.
	CodeInvalidInput Code = "INVALID_INPUT"

	// CodeNotSupported indicates the operation is not supported on the current
	// platform. Stub backends return this so callers can distinguish "not
	// implemented yet" from "actually failed".
	CodeNotSupported Code = "NOT_SUPPORTED"

	// CodeContextCanceled indicates the operation was canceled.
	CodeContextCanceled Code = "CONTEXT_CANCELED"

	// CodeTimeout indicates the operation timed out.
	CodeTimeout Code = "TIMEOUT"

	// CodeInternal indicates an internal error occurred.
	CodeInternal Code = "INTERNAL"
)

// Error is an error with a code, a message, and an optional cause.
type Error struct {
	code    Code
	message string
	cause   error
	context map[string]any
}

// New creates a new error with the given code and message.
func New(code Code, message string) *Error {
	return &Error{
		code:    code,
		message: message,
	}
}

// Newf creates a new error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{
		code:    code,
		message: fmt.Sprintf(format, args...),
	}
}

// Wrap wraps an existing error with a code and message.
func Wrap(err error, code Code, message string) *Error {
	if err == nil {
		return nil
	}

	return &Error{
		code:    code,
		message: message,
		cause:   err,
	}
}

// Wrapf wraps an existing error with a formatted message.
func Wrapf(err error, code Code, format string, args ...any) *Error {
	if err == nil {
		return nil
	}

	return &Error{
		code:    code,
		message: fmt.Sprintf(format, args...),
		cause:   err,
	}
}

// Code returns the error code.
func (e *Error) Code() Code {
	return e.code
}

// Message returns the error message.
func (e *Error) Message() string {
	return e.message
}

// Cause returns the underlying cause error.
func (e *Error) Cause() error {
	return e.cause
}

// Context returns the error context.
func (e *Error) Context() map[string]any {
	return e.context
}

// WithContext adds context information to the error.
func (e *Error) WithContext(key string, value any) *Error {
	if e.context == nil {
		e.context = make(map[string]any)
	}

	e.context[key] = value

	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.code, e.message, e.cause)
	}

	return fmt.Sprintf("[%s] %s", e.code, e.message)
}

// Unwrap returns the underlying cause error.
func (e *Error) Unwrap() error {
	return e.cause
}

// Is implements error matching for errors.Is.
func (e *Error) Is(target error) bool {
	targetError, ok := target.(*Error)
	if !ok {
		return false
	}

	return e.code == targetError.code
}

// IsCode checks if an error has the specified error code.
func IsCode(err error, code Code) bool {
	if domainErr, ok := errors.AsType[*Error](err); ok {
		return domainErr.code == code
	}

	return false
}

// GetCode extracts the error code from an error, or returns CodeInternal if it
// is not an Error.
func GetCode(err error) Code {
	if domainErr, ok := errors.AsType[*Error](err); ok {
		return domainErr.code
	}

	return CodeInternal
}

// Message returns an error's message without the code an Error carries.
func Message(err error) string {
	if err == nil {
		return ""
	}

	if domainErr, ok := errors.AsType[*Error](err); ok {
		return domainErr.Message()
	}

	return err.Error()
}

// IsNotSupported checks if an error indicates an unimplemented platform
// feature. Callers use it to fall back when a feature is unavailable.
func IsNotSupported(err error) bool {
	return IsCode(err, CodeNotSupported)
}
