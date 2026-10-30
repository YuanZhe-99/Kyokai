package localapi

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPeerCredentialsLinux checks that a connection reports this process's
// uid, gid, pid and every one of its groups.
func TestPeerCredentialsLinux(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := net.Dial("unix", path); err == nil {
			defer c.Close()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p, err := peerCredentials(conn)
	if err != nil {
		t.Fatal(err)
	}
	if p.UID != uint32(os.Getuid()) || p.GID != uint32(os.Getgid()) || p.PID != uint32(os.Getpid()) {
		t.Fatalf("peer %d/%d/%d, want %d/%d/%d", p.UID, p.GID, p.PID, os.Getuid(), os.Getgid(), os.Getpid())
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range append(groups, os.Getgid()) {
		if !slices.Contains(p.Groups, uint32(g)) {
			t.Fatalf("group %d missing from %v", g, p.Groups)
		}
	}
}
