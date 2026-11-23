//go:build !windows

package localapi

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// secureSocket sets the socket's group and mode.
func secureSocket(path string, mode os.FileMode, group int) error {
	if group != 0 {
		if err := os.Chown(path, -1, group); err != nil {
			return fmt.Errorf("chown socket: %w", err)
		}
	}
	return os.Chmod(path, mode)
}

// checkSocketDir refuses a socket folder this process's user does not own,
// that is a symbolic link, or that others may write to; a private socket's
// folder must be 0700.
// Notes: whoever owns or may write to the folder could put their own socket
// in its place and receive what clients send (a network's secret among it),
// as on macOS when TMPDIR is unset and the folder is under the shared /tmp.
func checkSocketDir(dir string, private bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	otherWrite := os.FileMode(0o022)
	if runtime.GOOS == "android" && !private && ok && st.Gid >= 20000 && st.Gid == st.Uid+10000 {
		// Android installd creates app cache directories with mode 0771 owned by
		// (u0_aN, u0_aN_cache), where the dedicated *_cache GID belongs solely to
		// this app's sandbox plus system storage/cache quota accounting.
		otherWrite = 0o002
	}
	switch {
	case !fi.IsDir():
		return fmt.Errorf("the socket folder %s is not a plain folder", dir)
	case !ok || st.Uid != uint32(os.Geteuid()):
		return fmt.Errorf("the socket folder %s belongs to another user", dir)
	case fi.Mode().Perm()&otherWrite != 0:
		return fmt.Errorf("the socket folder %s may be changed by other users (mode %o)", dir, fi.Mode().Perm())
	case private && fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("the socket folder %s is open to other users (mode %o, not 700)", dir, fi.Mode().Perm())
	}
	// The folders above it, up to this user's runtime folder, too: whoever
	// owns one could rename the socket's folder away and put theirs there.
	// On Android, the app data parent (/data/user/0/<pkg>) is owned by system
	// with mode 0700/0751 while cache/ and cache/kyokai/ belong to the app UID;
	// stop once the parent belongs to root/system (uid < 10000).
	if top := userRuntimeDir(); strings.HasPrefix(dir, top+string(filepath.Separator)) {
		parent := filepath.Dir(dir)
		if runtime.GOOS == "android" {
			if pfi, err := os.Lstat(parent); err == nil {
				if pst, ok := pfi.Sys().(*syscall.Stat_t); ok && pst.Uid < 10000 {
					return nil
				}
			}
		}
		return checkSocketDir(parent, false)
	}
	return nil
}
