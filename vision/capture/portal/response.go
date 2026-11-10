//go:build linux

package portal

import "github.com/LouYuanbo1/neru-core/errs"

// The Response signal's codes, from the portal's org.freedesktop.portal.Request
// interface. 0 is success, 1 the user cancelled the dialog or refused the
// permission, and 2 anything else the backend failed on.
const (
	responseSuccess  uint32 = 0
	responseCanceled uint32 = 1
	responseFailed   uint32 = 2
)

// responseError maps a Response code onto the shared error vocabulary.
//
// Every non-zero code is a denial from the caller's point of view: no
// screenshot was taken, and the session's consent gate is what refused it,
// whether the user dismissed the dialog, the backend failed, or the portal
// invented a code this package does not know. A caller degrades the same way in
// all three cases, so none of them is reported as a success or as an internal
// fault, and a code the interface does not define is treated as a refusal
// rather than as an answer.
func responseError(code uint32) error {
	switch code {
	case responseSuccess:
		return nil
	case responseCanceled:
		return errs.New(
			errs.CodeAccessibilityDenied,
			"the screenshot portal's request was cancelled or denied; a screenshot "+
				"on this desktop needs it approved",
		)
	case responseFailed:
		return errs.New(
			errs.CodeAccessibilityDenied,
			"the screenshot portal failed the request; this desktop's portal backend "+
				"could not take a screenshot",
		)
	default:
		return errs.Newf(
			errs.CodeAccessibilityDenied,
			"the screenshot portal answered with response code %d, which is not a success",
			code,
		)
	}
}
