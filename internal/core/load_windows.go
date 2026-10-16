package core

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// open loads the library by absolute path, resolving its own dependencies
// only from its folder and System32, never from the search path.
func open(path string) (uintptr, error) {
	if err := windows.SetDefaultDllDirectories(windows.LOAD_LIBRARY_SEARCH_SYSTEM32); err != nil {
		return 0, err
	}
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	return uintptr(h), err
}

func symbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}

func syscallN(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}
