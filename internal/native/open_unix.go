//go:build darwin || linux || freebsd || netbsd

package native

import "github.com/ebitengine/purego"

// openLibrary loads a shared object the way the C library's dlopen does. The
// RTLD_GLOBAL flag matches a framework's own semantics: symbols it exports
// stay visible to libraries opened later.
func openLibrary(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

func closeLibrary(handle uintptr) error {
	return purego.Dlclose(handle)
}

func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}
