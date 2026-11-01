//go:build !windows

package localapi

import (
	"fmt"
	"os"
	"path/filepath"
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
	switch {
	case !fi.IsDir():
		return fmt.Errorf("the socket folder %s is not a plain folder", dir)
	case !ok || st.Uid != uint32(os.Geteuid()):
		return fmt.Errorf("the socket folder %s belongs to another user", dir)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("the socket folder %s may be changed by other users (mode %o)", dir, fi.Mode().Perm())
	case private && fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("the socket folder %s is open to other users (mode %o, not 700)", dir, fi.Mode().Perm())
	}
	// The folders above it, up to this user's runtime folder, too: whoever
	// owns one could rename the socket's folder away and put theirs there.
	if top := userRuntimeDir(); strings.HasPrefix(dir, top+string(filepath.Separator)) {
		return checkSocketDir(filepath.Dir(dir), false)
	}
	return nil
}
