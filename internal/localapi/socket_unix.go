//go:build !windows

package localapi

import (
	"fmt"
	"os"
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
