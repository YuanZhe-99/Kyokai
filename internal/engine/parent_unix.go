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
