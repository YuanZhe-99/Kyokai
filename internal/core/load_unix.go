//go:build darwin || linux

package core

import "github.com/ebitengine/purego"

// open loads the library by absolute path (purego: no cgo on macOS or Linux).
func open(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

// symbol resolves an exported function of the loaded library.
func symbol(h uintptr, name string) (uintptr, error) {
	return purego.Dlsym(h, name)
}

// syscallN calls a C function and returns its first result register.
func syscallN(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}
