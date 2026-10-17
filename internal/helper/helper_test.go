package helper

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveWithParent checks that uninstalling removes the product folder
// only once nothing else is left in it, and that a repeated uninstall
// succeeds.
func TestRemoveWithParent(t *testing.T) {
	cases := []struct {
		name          string
		dir, sibling  bool // what exists under the parent beforehand
		parentRemains bool
	}{
		{"only dir", true, false, false},
		{"dir and sibling", true, true, true},
		{"dir already gone", false, false, false},
		{"dir gone, sibling stays", false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "Kyokai")
			dir := filepath.Join(parent, "helper")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.dir {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "kyokai-svc.exe"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.sibling {
				if err := os.WriteFile(filepath.Join(parent, "other"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeWithParent(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("dir still there: %v", err)
			}
			_, err := os.Stat(parent)
			if remains := err == nil; remains != c.parentRemains {
				t.Errorf("parent remains = %v, want %v", remains, c.parentRemains)
			}
		})
	}
}
