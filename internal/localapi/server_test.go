package localapi

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An engine that stops late must not remove the socket of the engine
// started after it (which left that one unreachable and restarted in a
// loop), while its own socket file goes when it closes.
func TestClosingRemovesOnlyItsOwnSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	old, err := ListenWith(ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	// The old engine is still stopping when its file is taken over: the
	// stale file goes and a new engine listens on the path.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	next, err := ListenWith(ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	// Now the old one closes.
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("the newer engine's socket is gone: %v", err)
	}
	conn.Close()
	// Its own socket file goes when it closes.
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closing left its own socket file: %v", err)
	}
}
