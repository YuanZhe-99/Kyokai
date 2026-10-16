package engine

import (
	"context"

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
