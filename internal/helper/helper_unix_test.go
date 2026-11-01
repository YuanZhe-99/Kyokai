//go:build darwin || linux

package helper

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckTrusted: a folder this (unprivileged) user made, a link, or a
// folder others may change is not the helper's to trust.
func TestCheckTrusted(t *testing.T) {
	dir := t.TempDir()
	if os.Geteuid() != 0 && checkTrusted(dir) == nil {
		t.Error("a folder of this user's is trusted")
	}
	if err := checkTrusted("/"); err != nil {
		t.Errorf("root's own folder: %v", err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if checkTrusted(link) == nil {
		t.Error("a link to a trusted folder is trusted")
	}
}
