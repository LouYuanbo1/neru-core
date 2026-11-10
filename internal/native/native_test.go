//go:build darwin || linux || freebsd || netbsd || windows

package native

import (
	"runtime"
	"testing"
)

func TestOpen_MissingLibraryIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := Open("definitely-not-a-library-neru-core"); err == nil {
		t.Fatal("Open(missing library) succeeded, want an error")
	}
}

func TestOpen_LoadsSystemLibraryAndCallsAFunction(t *testing.T) {
	t.Parallel()

	libraryName, functionName := systemProcessLibrary()

	lib, err := Open(libraryName)
	if err != nil {
		t.Fatalf("Open(%q): %v", libraryName, err)
	}
	defer lib.Close()

	var processID func() uint32

	if err := lib.Func(functionName, &processID); err != nil {
		t.Fatalf("Func(%q): %v", functionName, err)
	}

	if got := processID(); got == 0 {
		t.Errorf("%s() = 0, want the current process id", functionName)
	}

	if runtime.GOOS == "windows" {
		// A Windows handle is not an error value; Close must still succeed and
		// be safe to call twice.
		if err := lib.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	}
}

func TestAddress_UnknownSymbolIsAnError(t *testing.T) {
	t.Parallel()

	libraryName, _ := systemProcessLibrary()

	lib, err := Open(libraryName)
	if err != nil {
		t.Fatalf("Open(%q): %v", libraryName, err)
	}
	defer lib.Close()

	if _, err := lib.Address("NeruCoreSymbolThatDoesNotExist"); err == nil {
		t.Fatal("Address(unknown symbol) succeeded, want an error")
	}
}

// systemProcessLibrary names a library every platform ships and one function in
// it that returns the current process id, so the loader is exercised for real
// on each host rather than only compiled.
func systemProcessLibrary() (library, function string) {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/lib/libSystem.B.dylib", "getpid"
	case "linux":
		return "libc.so.6", "getpid"
	case "windows":
		return "kernel32.dll", "GetCurrentProcessId"
	default:
		return "libc.so.6", "getpid"
	}
}
