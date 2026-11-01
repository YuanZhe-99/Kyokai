package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
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

// served counts the requests that reached it.
type served struct{ n *atomic.Int32 }

func (s served) Handle(context.Context, Request) Response { s.n.Add(1); return Response{OK: true} }

func (served) Stream(context.Context, Request, func(Response) error) {}

func (served) Media(context.Context, Request, func() io.ReadWriter) Response { return Response{} }

// serve runs ServeWith on a fresh socket for the test's duration.
func serve(t *testing.T, h Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.sock")
	ln, err := ListenWith(ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeWith(ctx, ln, h, Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: CurrentSID()})
	}()
	t.Cleanup(func() { cancel(); <-done; os.RemoveAll(dir) })
	return path
}

// A client of a socket RequireServer names sends nothing to a program that
// is not the expected engine (regression: the run-mode switch sent the
// whole store to whatever listened on the helper's socket).
func TestRequireServer(t *testing.T) {
	var n atomic.Int32
	path := serve(t, served{&n})
	RequireServer(path, func(Peer) bool { return false })
	if _, err := NewClient(path).Do(context.Background(), Request{Type: RequestStatus}); !errors.Is(err, ErrUntrustedServer) {
		t.Fatalf("an untrusted engine: %v", err)
	}
	if n.Load() != 0 {
		t.Fatal("a request reached the untrusted engine")
	}
	// This process is the engine here: it passes a check for itself.
	RequireServer(path, func(p Peer) bool { return p.SID == CurrentSID() && (p.SID != "" || p.UID == uint32(os.Getuid())) })
	if _, err := NewClient(path).Do(context.Background(), Request{Type: RequestStatus}); err != nil || n.Load() != 1 {
		t.Fatalf("the expected engine: %v, %d requests", err, n.Load())
	}
}

// A connection that sends no request is closed after requestWait, so idle
// connections cannot pile up.
func TestSilentConnectionCloses(t *testing.T) {
	old := requestWait
	requestWait = 200 * time.Millisecond
	// Restored after the server's own cleanup has stopped it (cleanups run
	// last first), so no connection reads it as it changes.
	t.Cleanup(func() { requestWait = old })
	path := serve(t, ownerTier{})
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a silent connection stayed open: %v", err)
	}
}

// A packet longer than its stream's limit is refused before it is read.
func TestReadPacketLimit(t *testing.T) {
	var b bytes.Buffer
	if err := WritePacket(&b, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	if _, err := ReadPacketLimit(bytes.NewReader(raw), nil, 9); err == nil {
		t.Fatal("a packet over the limit was read")
	}
	if p, err := ReadPacketLimit(bytes.NewReader(raw), nil, 10); err != nil || len(p) != 10 {
		t.Fatalf("a packet at the limit: %d, %v", len(p), err)
	}
}

// Encode's bytes are what the server writes, and they decode to the
// response.
func TestEncode(t *testing.T) {
	r, err := Encode(Response{OK: true, Status: &Status{DeviceID: "100000002"}})
	if err != nil {
		t.Fatal(err)
	}
	line, err := r.marshal()
	if err != nil || !bytes.Equal(line, r.Encoded()) {
		t.Fatalf("marshal %q, encoded %q", line, r.Encoded())
	}
	var back Response
	if err := json.Unmarshal(line, &back); err != nil || back.Version != ProtocolVersion || back.Status.DeviceID != "100000002" {
		t.Fatalf("%s: %v", line, err)
	}
}
