package localapi

import (
	"context"
	"encoding/json"
	"io"
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

// ownerTier is a handler that answers whether a request came from the
// owner tier.
type ownerTier struct{}

func (ownerTier) Handle(ctx context.Context, _ Request) Response { return Response{OK: IsOwner(ctx)} }

func (ownerTier) Stream(context.Context, Request, func(Response) error) {}

func (ownerTier) Media(context.Context, Request, func() io.ReadWriter) Response { return Response{} }

// Stopping the server ends its idle connections at once, each served as
// its owner until then (regression: the watcher that ends them read ctx
// while serveConn wrapped it with the tier, and the torn context crashed a
// test run; `go test -race` reports the old code here).
func TestStopEndsIdleConnections(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := ListenWith(ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- ServeWith(ctx, ln, ownerTier{}, Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: CurrentSID()})
	}()
	req, _ := json.Marshal(Request{Version: ProtocolVersion, Type: RequestStatus})
	for range 8 {
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		var resp Response
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if !resp.OK {
			t.Fatal("a connection was not served as its owner")
		}
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle connections kept the server from stopping")
	}
}
