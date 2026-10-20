package engine

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

// watchParent returns when process pid exits.
func watchParent(ctx context.Context, pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	done := make(chan struct{})
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

// stderrFile is the file this process's stderr is, kept open (and out of
// the collector's reach) while it is.
var stderrFile *os.File

// quietStderr sends this process's stderr nowhere (No logs): the core
// writes its panics and log lines there.
func quietStderr() {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	if windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(null.Fd())) != nil {
		_ = null.Close()
		return
	}
	stderrFile = null
}

// logStderr points this process's stderr at f, the log, so that the core's
// panics and log lines and a Go crash trace (both look the handle up as
// they write) are kept there.
// Returns: whether it could.
func logStderr(f *os.File) bool {
	if windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())) != nil {
		return false
	}
	stderrFile = f
	return true
}
