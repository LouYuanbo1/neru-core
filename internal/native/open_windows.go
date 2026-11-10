//go:build windows

package native

import "syscall"

// openLibrary loads a DLL by name or path. LoadLibrary searches the standard
// DLL search order for a bare name, so system DLLs ("user32.dll") need no path.
func openLibrary(name string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(name)
	if err != nil {
		return 0, err
	}

	return uintptr(handle), nil
}

func closeLibrary(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}

func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return syscall.GetProcAddress(syscall.Handle(handle), name)
}
