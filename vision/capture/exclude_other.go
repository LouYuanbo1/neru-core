//go:build !windows

package capture

import "github.com/LouYuanbo1/neru-core/errs"

// Window capture exclusion is a Windows facility: WDA_EXCLUDEFROMCAPTURE is a
// per-window DWM attribute. macOS excludes the caller's windows inside its
// ScreenCaptureKit capture path instead, as part of the content filter, so no
// per-window call is needed there. Linux compositors expose no per-window
// capture exclusion at all. This platform therefore reports CodeNotSupported
// for the explicit call instead of pretending it worked.

// ExcludeWindow reports that per-window capture exclusion is unavailable here.
func ExcludeWindow(uintptr) error {
	return errs.New(
		errs.CodeNotSupported,
		"excluding a window from capture through an explicit call is implemented on Windows only; macOS excludes the caller's windows inside its ScreenCaptureKit path",
	)
}

// ExcludeOwnWindows reports that the explicit per-window exclusion call is
// unavailable here. On macOS the ScreenCaptureKit path already excludes this
// process's windows; the screencapture fallback cannot.
func ExcludeOwnWindows() error {
	return errs.New(
		errs.CodeNotSupported,
		"excluding this process's windows through an explicit call is implemented on Windows only; macOS does it implicitly in the ScreenCaptureKit path",
	)
}
