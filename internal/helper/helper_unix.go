//go:build darwin || linux

package helper

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// prepareDir makes dir a folder of root's that only root may change, with
// mode: one that someone else made, or that others may change, or a link,
// is moved aside to dir.untrusted-<time> first and never adopted.
// Side effects: may rename dir; creates it; sets its owner and mode.
func prepareDir(dir string, mode os.FileMode) error {
	if _, err := os.Lstat(dir); err == nil && checkTrusted(dir) != nil {
		aside := fmt.Sprintf("%s.untrusted-%d", dir, time.Now().UnixNano())
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("move aside %s, which someone else made: %w", dir, err)
		}
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if err := os.Lchown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil {
		return err
	}
	return checkTrusted(dir)
}

// checkTrusted reports why dir is not a folder only root controls: it is a
// symbolic link or not a folder, it belongs to another user, or its group
// or others may write to it.
func checkTrusted(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%s is a link or not a folder", dir)
	case !ok || st.Uid != 0:
		return fmt.Errorf("%s does not belong to root", dir)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s may be changed by others (mode %o)", dir, fi.Mode().Perm())
	}
	return nil
}

// localPath reports true: a path here cannot lead the helper to sign in to
// another computer (Windows' shares).
func localPath(string) bool { return true }

// IsHelper reports whether p, the process listening on SocketPath as a
// client sees it, is the helper itself: root. The GUI checks it before
// sending anything to the helper (localapi.RequireServer).
func IsHelper(p localapi.Peer) bool { return p.Known && p.UID == 0 }
