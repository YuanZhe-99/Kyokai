package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// tunnelPacket is the media packet kind of tunnel bytes, both ways.
const tunnelPacket = 14

// tunnelWait bounds a tunnel's opening: dialing, signing in and the host's
// user answering (localapi.TunnelWait on the desktop).
const tunnelWait = 90 * time.Second

// tunnelError is why a tunnel did not open: a failure the core names
// (unreachable, wrong-device, key-changed, incompatible, broken,
// unsupported), the host's refusal (password, denied, timeout, busy,
// rate-limited, absent, service-down), "closed" or "timeout".
type tunnelError struct{ reason string }

func (e tunnelError) Error() string { return e.reason }

// openTunnel opens a tunnel to a device's service through the core and
// waits for it, as the desktop engine does (internal/engine/services.go).
// Inputs: service is ssh, rdp or vnc; address, where to dial the device
// ("" through the network).
// Returns: the tunnel as a connection, or a tunnelError.
// Side effects: a session in the core, closed with the connection.
func openTunnel(ctx context.Context, device, address, service string) (net.Conn, error) {
	params := map[string]any{"device_id": device, "service": service}
	if address != "" {
		params["address"] = address
	}
	var opened struct {
		ID uint64 `json:"id"`
	}
	if err := coreCall("tunnel.open", params, &opened); err != nil {
		return nil, err
	}
	id := opened.ID
	deadline := time.Now().Add(tunnelWait)
	var w struct {
		State   string          `json:"state"`
		Failure json.RawMessage `json:"failure"`
	}
	for {
		if err := coreCall("tunnel.wait", map[string]any{"id": id, "timeout_ms": 1000}, &w); err != nil {
			closeSession(id)
			return nil, err
		}
		if w.State != "connecting" && w.State != "confirming" {
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
	}
	if w.State != "connected" {
		closeSession(id)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, tunnelError{failureReason(w.State, w.Failure)}
	}
	return newTunnelConn(id), nil
}

// failureReason names why a tunnel did not connect
// (internal/engine/services.go tunnelFailure).
func failureReason(state string, failure json.RawMessage) string {
	var s string
	if json.Unmarshal(failure, &s) == nil && s != "" {
		return s
	}
	var refused struct {
		Refused string `json:"refused"`
	}
	if json.Unmarshal(failure, &refused) == nil && refused.Refused != "" {
		return refused.Refused
	}
	if state == "closed" {
		return "closed"
	}
	return "timeout"
}

func closeSession(id uint64) { _ = coreCall("session.close", map[string]any{"id": id}, nil) }

// tunnelConn is a tunnel as a net.Conn: one end of a pipe whose other end
// two goroutines pump to and from the core, so deadlines work as on any
// connection.
type tunnelConn struct {
	net.Conn
	id   uint64
	once sync.Once
}

func newTunnelConn(id uint64) *tunnelConn {
	mine, theirs := net.Pipe()
	t := &tunnelConn{Conn: mine, id: id}
	// The core to the clients: a whole tunnel piece always fits (core
	// tunnel.rs PIECE is 65 518 bytes), else it would be lost.
	go func() {
		buf := make([]byte, 1+128<<10)
		defer theirs.Close()
		for {
			n := mediaRead(id, buf, 500*time.Millisecond)
			switch {
			case n == -2:
				return
			case n <= 0:
				continue
			case buf[0] != tunnelPacket:
				continue
			}
			if _, err := theirs.Write(buf[1:n]); err != nil {
				return
			}
		}
	}()
	// The clients to the core; a write blocks until the core has room.
	go func() {
		buf := make([]byte, 1+64<<10)
		buf[0] = tunnelPacket
		for {
			n, err := theirs.Read(buf[1:])
			if n > 0 && mediaWrite(id, buf[:1+n]) == -2 {
				_ = theirs.Close()
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return t
}

// Close ends the tunnel's session in the core and the pipe.
func (t *tunnelConn) Close() error {
	t.once.Do(func() { closeSession(t.id) })
	return t.Conn.Close()
}

// RemoteAddr names the tunnel (rdp reads it).
func (t *tunnelConn) RemoteAddr() net.Addr { return tunnelAddr{} }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "kyokai" }
func (tunnelAddr) String() string  { return "localhost:0" }

// errClosed is a client closed by the app.
var errClosed = errors.New("closed")
