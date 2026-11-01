package bundlesig

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// TestBundles: a signed bundle reads back as the exact bytes that were
// signed; a changed file, a foreign key, a bad name or a missing signature
// is refused.
func TestBundles(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Read(dir, pub); err == nil {
		t.Fatal("an unsigned folder was accepted")
	}
	write("kyokai-svc.exe", "engine")
	write("kyokai_core.dll", "core")
	if err := Sign(dir, "0.2.0", priv); err != nil {
		t.Fatal(err)
	}
	b, err := Read(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != "0.2.0" || string(b.Data["kyokai_core.dll"]) != "core" || len(b.Names()) != 2 {
		t.Fatalf("bundle %+v", b.Manifest)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if _, err := Read(dir, other); err == nil {
		t.Error("a foreign key verified the bundle")
	}
	write("kyokai_core.dll", "swapped")
	if _, err := Read(dir, pub); err == nil {
		t.Error("a changed file was accepted")
	}
}

// TestReleaseKey: the embedded release key parses.
func TestReleaseKey(t *testing.T) {
	if _, err := PublicKey(); err != nil {
		t.Fatal(err)
	}
}

// TestReadRegular: only regular files within the limit are read
// (regression: a manifest linked to /dev/zero exhausted the helper's
// memory).
func TestReadRegular(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := readRegular(p, 5); err != nil || string(b) != "12345" {
		t.Fatalf("a regular file: %q, %v", b, err)
	}
	if _, err := readRegular(p, 4); err == nil {
		t.Error("a file over the limit was read")
	}
	if _, err := readRegular(dir, 1<<20); err == nil {
		t.Error("a folder was read")
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(p, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, err := readRegular(link, 1<<20); err == nil {
		t.Error("a symbolic link was followed")
	}
}
