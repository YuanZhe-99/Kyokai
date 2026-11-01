package localapi

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// A connection whose peer process started after the connection was
// accepted is refused: its PID was reused after the client exited, and the
// new process is not the client (regression: it got the client's tier).
func TestPeerCredentialsRefuseReusedPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := net.Dial("unix", path); err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p, err := peerCredentials(conn, time.Now()); err != nil || p.SID != CurrentSID() {
		t.Fatalf("this process: %+v, %v", p, err)
	}
	// As if accepted before this process started.
	if _, err := peerCredentials(conn, time.Unix(1, 0)); err == nil {
		t.Fatal("a process started after the connection was taken for its client")
	}
}
