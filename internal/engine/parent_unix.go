//go:build !windows

package engine

import (
	"context"
	"os"
	"syscall"
	"time"
)

// watchParent returns when process pid is gone (or we were re-parented).
func watchParent(ctx context.Context, pid int) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		if os.Getppid() != pid || syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
	}
}

// quietStderr sends this process's stderr nowhere (No logs): a launchd
// job's StandardErrorPath (the macOS helper's helper.log) would keep it.
func quietStderr() {
	if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		_ = syscall.Dup2(int(null.Fd()), 2)
		_ = null.Close()
	}
}
