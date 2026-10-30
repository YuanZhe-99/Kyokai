//go:build !windows

package engine

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// keepStderr returns a function that puts this process's stderr back as it
// is now: opening the log moves it there.
func keepStderr() func() {
	saved, err := syscall.Dup(2)
	if err != nil {
		return func() {}
	}
	return func() {
		_ = unix.Dup2(saved, 2)
		_ = syscall.Close(saved)
	}
}

// writeStderr writes b to this process's stderr as the core does.
func writeStderr(b []byte) { _, _ = syscall.Write(2, b) }
