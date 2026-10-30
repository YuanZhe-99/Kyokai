package localapi

import (
	"os"
	"path/filepath"
)

// userRuntimeDir is $XDG_RUNTIME_DIR (logind's private /run/user/<uid>),
// else the user's cache folder.
// Notes: never the shared /tmp, where another user could create the folder
// first and receive the GUI's requests (a network's secret among them).
func userRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".cache")
}
