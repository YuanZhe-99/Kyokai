package helper

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/bundlesig"
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

// TestInstallBundleWritesTheVerifiedBytes guards the update against a
// swap in the user-writable app folder after verification: the helper
// installs what it checked, never what is there later.
func TestInstallBundleWritesTheVerifiedBytes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	app, bin := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{"kyokai-svc.exe": "new engine", "kyokai_core.dll": "new core"} {
		if err := os.WriteFile(filepath.Join(app, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "kyokai-svc.exe"), []byte("old engine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bundlesig.Sign(app, "0.2.0", priv); err != nil {
		t.Fatal(err)
	}
	b, err := bundlesig.Read(app, pub)
	if err != nil {
		t.Fatal(err)
	}
	// Swapped after the check.
	if err := os.WriteFile(filepath.Join(app, "kyokai_core.dll"), []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installBundle(bin, b); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(bin, "kyokai_core.dll"))
	if string(got) != "new core" {
		t.Fatalf("installed %q, not the verified bytes", got)
	}
	if old, _ := os.ReadFile(filepath.Join(bin, "kyokai-svc.exe.old")); string(old) != "old engine" {
		t.Errorf("the running engine was not set aside: %q", old)
	}
}

// TestAppGone: the helper removes itself only when the app it was
// installed from lost its engine; a helper without a recorded app stays.
func TestAppGone(t *testing.T) {
	dir := t.TempDir()
	if (Settings{}).AppGone() {
		t.Error("no recorded app reads as gone")
	}
	s := Settings{AppDir: dir}
	if !s.AppGone() {
		t.Error("an empty folder reads as the app")
	}
	if err := os.WriteFile(filepath.Join(dir, exeName("kyokai-svc")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.AppGone() {
		t.Error("the app's engine is there")
	}
}

// TestDeepestFirst: a removal scheduled for the next restart deletes
// files before their folders (regression: the helper could not delete its
// state while its own log was open, and stayed running).
func TestDeepestFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Kyokai")
	if got := deepestFirst(dir); len(got) != 0 {
		t.Fatalf("missing dir: %v", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "logs", "engine.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := deepestFirst(dir)
	pos := map[string]int{}
	for i, p := range got {
		pos[p] = i
	}
	log, logs, state := filepath.Join(dir, "state", "logs", "engine.log"), filepath.Join(dir, "state", "logs"), filepath.Join(dir, "state")
	if !(pos[log] < pos[logs] && pos[logs] < pos[state] && pos[state] < pos[dir]) || len(got) != 4 {
		t.Fatalf("order %v", got)
	}
}
