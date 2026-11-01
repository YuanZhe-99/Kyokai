//go:build !windows

package localapi

import (
	"os"
	"path/filepath"
	"testing"
)

// A socket folder others may change, or a private socket's folder others
// may read, or a link to one, is refused (regression: on macOS without
// TMPDIR the folder was under the shared /tmp, and an existing one was
// taken whoever made it).
func TestCheckSocketDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketDir(dir, true); err != nil {
		t.Fatalf("this user's 0700 folder: %v", err)
	}
	link := filepath.Join(base, "l")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if checkSocketDir(link, true) == nil {
		t.Error("a link to the folder was taken")
	}
	for _, c := range []struct {
		mode    os.FileMode
		private bool
		ok      bool
	}{{0o755, true, false}, {0o755, false, true}, {0o775, false, false}, {0o777, false, false}, {0o700, false, true}} {
		if err := os.Chmod(dir, c.mode); err != nil {
			t.Fatal(err)
		}
		if err := checkSocketDir(dir, c.private); (err == nil) != c.ok {
			t.Errorf("mode %o private %v: %v", c.mode, c.private, err)
		}
	}
	if os.Geteuid() != 0 {
		// A folder of another user's: root's own.
		if checkSocketDir("/", false) == nil {
			t.Error("a folder of another user's was taken")
		}
	}
}
