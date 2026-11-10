//go:build !darwin && !linux && !freebsd && !netbsd && !windows

package native

import "errors"

var errUnsupported = errors.New("native: dynamic libraries are not supported on this platform")

func openLibrary(string) (uintptr, error)           { return 0, errUnsupported }
func closeLibrary(uintptr) error                    { return errUnsupported }
func lookupSymbol(uintptr, string) (uintptr, error) { return 0, errUnsupported }
func bindFunc(any, uintptr, string) error           { return errUnsupported }
