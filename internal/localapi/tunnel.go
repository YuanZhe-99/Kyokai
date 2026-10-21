package localapi

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// PacketTunnel is the packet kind of a tunnel's bytes, both ways
// (doc/en-us/remote-services.md).
const PacketTunnel byte = 14

// TunnelWait is how long the engine waits for the host to admit a tunnel
// (its user may be asked) before it answers tunnel.open.
const TunnelWait = 90 * time.Second

// tunnelPiece is the most bytes one tunnel packet carries.
const tunnelPiece = 64 << 10

// Tunnel opens a tunnel to a device's own service (RequestTunnelOpen) and
// returns its bytes as a connection.
// Returns: a *RequestError whose Message names why there is none
// ("unsupported", "service-down", "denied", "unreachable", …) when the
// host did not admit it.
// Notes: the engine answers once the host admitted the tunnel or refused
// it, after up to TunnelWait; closing the connection ends the tunnel.
func (c *Client) Tunnel(ctx context.Context, t TunnelRequest) (net.Conn, error) {
	conn, err := c.openStream(ctx, Request{Type: RequestTunnelOpen, Tunnel: &t}, TunnelWait+c.Timeout)
	if err != nil {
		return nil, err
	}
	return &tunnelConn{Conn: conn}, nil
}

// openStream sends a streaming request and reads its answer, waiting up to
// wait; the connection then carries packets.
func (c *Client) openStream(ctx context.Context, req Request, wait time.Duration) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeRequest(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	// Read the answer byte by byte: what follows it is packets, which a
	// buffered reader would swallow.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var line []byte
	one := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	for len(line) < maxResponseSize {
		if _, err := conn.Read(one); err != nil {
			conn.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if one[0] == '\n' {
			break
		}
		line = append(line, one[0])
	}
	_ = conn.SetReadDeadline(time.Time{})
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, err
	}
	if !resp.OK {
		conn.Close()
		return nil, &RequestError{Code: resp.Code, Message: resp.Error}
	}
	return conn, nil
}

// tunnelConn is a tunnel's bytes over a packet stream: reads take the
// bytes of tunnel packets, writes send them in packets.
type tunnelConn struct {
	net.Conn
	rmu     sync.Mutex
	buf     []byte
	pending []byte
	wmu     sync.Mutex
}

// Read returns the tunnel's next bytes.
func (t *tunnelConn) Read(p []byte) (int, error) {
	t.rmu.Lock()
	defer t.rmu.Unlock()
	for len(t.pending) == 0 {
		pkt, err := ReadPacket(t.Conn, t.buf)
		if err != nil {
			return 0, err
		}
		t.buf = pkt
		if len(pkt) > 0 && pkt[0] == PacketTunnel {
			t.pending = pkt[1:]
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

// Write sends p through the tunnel; it waits while the tunnel is behind.
func (t *tunnelConn) Write(p []byte) (int, error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	sent := 0
	for sent < len(p) {
		n := min(len(p)-sent, tunnelPiece)
		pkt := make([]byte, 1+n)
		pkt[0] = PacketTunnel
		copy(pkt[1:], p[sent:sent+n])
		if err := WritePacket(t.Conn, pkt); err != nil {
			return sent, err
		}
		sent += n
	}
	return sent, nil
}
