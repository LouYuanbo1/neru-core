//go:build darwin || linux || freebsd || netbsd || windows

package native

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// bindFunc registers a Go function value for the C function at addr. purego
// panics on a signature it cannot express (more than one result, or a type it
// does not map), which is a programming error in the caller; it is returned as
// an error here so a missing or misdeclared symbol fails the constructor that
// bound it rather than crashing on first use.
func bindFunc(fptr any, addr uintptr, symbol string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("native: bind %q: %v", symbol, recovered)
		}
	}()

	purego.RegisterFunc(fptr, addr)

	return nil
}
