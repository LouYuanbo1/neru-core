// Package native loads dynamic libraries and binds their exported C symbols
// without CGo.
//
// It is the single seam between the platform backends and the operating
// system's own libraries. On macOS and Linux a library is opened with dlopen
// and a symbol is resolved with dlsym; on Windows it is opened with
// LoadLibrary and resolved with GetProcAddress. Everything is done through
// github.com/ebitengine/purego, which performs the actual calls with assembly
// generated function pointers, so a caller can build for every platform
// without a C toolchain.
//
// The package is internal on purpose: it hands out raw function pointers and
// addresses of exported data symbols, which a public API must not.
package native

import (
	"errors"
	"fmt"
	"sync"
)

// Library is one loaded dynamic library. Open returns a handle whose symbols
// stay valid until Close, exactly like a dlopen handle.
type Library struct {
	// Name is the path (or, on Windows, the name) the library was opened with.
	Name string

	handle uintptr

	once   sync.Once
	closed bool
}

// Open loads the first of names that the operating system accepts. A name may
// be an absolute path, a framework path, or a bare soname the loader searches
// for. The returned error names every attempt so a missing system framework
// is diagnosable.
func Open(names ...string) (*Library, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("native: no library name given")
	}

	var failures []error

	for _, name := range names {
		handle, err := openLibrary(name)
		if err == nil {
			return &Library{Name: name, handle: handle}, nil
		}

		failures = append(failures, fmt.Errorf("%s: %w", name, err))
	}

	return nil, fmt.Errorf("native: could not load any of the libraries: %w", errors.Join(failures...))
}

// Close releases the library. It is safe to call more than once, and no
// symbol bound from it may be called afterwards.
func (l *Library) Close() error {
	if l == nil {
		return nil
	}

	var err error

	l.once.Do(func() {
		l.closed = true
		err = closeLibrary(l.handle)
	})

	return err
}

// Address resolves one exported symbol to its address. It returns an error
// rather than panicking so a backend can degrade when a symbol is missing on
// an older operating system.
func (l *Library) Address(symbol string) (uintptr, error) {
	if l == nil || l.handle == 0 {
		return 0, fmt.Errorf("native: library is not open")
	}

	addr, err := lookupSymbol(l.handle, symbol)
	if err != nil {
		return 0, fmt.Errorf("native: %s: symbol %q: %w", l.Name, symbol, err)
	}

	if addr == 0 {
		return 0, fmt.Errorf("native: %s: symbol %q resolved to a null address", l.Name, symbol)
	}

	return addr, nil
}

// Func binds one C function to fptr, which must be a pointer to a Go function
// whose signature matches the C function. The same conversions purego
// documents apply: uintptr for pointers, int32/uint32 for fixed-width
// integers, float64 for double, and an explicitly sized struct for a C struct
// passed by value.
func (l *Library) Func(symbol string, fptr any) error {
	addr, err := l.Address(symbol)
	if err != nil {
		return err
	}

	return bindFunc(fptr, addr, symbol)
}

// Data returns the address of an exported data symbol, such as
// CoreFoundation's kCFTypeArrayCallBacks or a kAX*Attribute CFStringRef
// variable. The caller reads what the address holds.
func (l *Library) Data(symbol string) (uintptr, error) {
	return l.Address(symbol)
}

// Must lazily opens one library and panics when it cannot be loaded. A
// backend uses it for the frameworks every supported macOS offers, where a
// failure means the process cannot work at all.
func Must(name string, aliases ...string) *Library {
	lib, err := Open(append([]string{name}, aliases...)...)
	if err != nil {
		panic(err)
	}

	return lib
}
