//go:build !windows

package localapi

import (
	"fmt"
	"os"
	"path/filepath"
)

// userRuntimeDir is a per-user folder under the temporary directory, which
// on macOS is already private to the user.
func userRuntimeDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("kyokai-%d", os.Getuid()))
}
