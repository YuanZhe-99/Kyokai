package gui

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// markDownloaded quarantines a file copied from another computer
// (com.apple.quarantine), so Gatekeeper checks it before it runs, as for a
// browser's download.
func markDownloaded(path string) {
	_ = unix.Setxattr(path, "com.apple.quarantine", fmt.Appendf(nil, "0081;%08x;Kyokai;", time.Now().Unix()), 0)
}
