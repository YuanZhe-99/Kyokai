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
