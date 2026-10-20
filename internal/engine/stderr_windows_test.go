package engine

import "golang.org/x/sys/windows"

// keepStderr returns a function that puts this process's stderr back as it
// is now: opening the log moves it there.
func keepStderr() func() {
	h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return func() {}
	}
	return func() { _ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, h) }
}

// writeStderr writes b to this process's stderr as the core does: through
// the handle looked up as it writes.
func writeStderr(b []byte) {
	if h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil {
		var n uint32
		_ = windows.WriteFile(h, b, &n, nil)
	}
}
