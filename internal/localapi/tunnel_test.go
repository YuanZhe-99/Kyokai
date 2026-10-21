package localapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// echoTunnels is a handler whose tunnels echo every packet, and that
// refuses a tunnel to "vnc" as the engine refuses a service that is down.
type echoTunnels struct{}

func (echoTunnels) Handle(context.Context, Request) Response { return Response{OK: true} }

func (echoTunnels) Stream(context.Context, Request, func(Response) error) {}

func (echoTunnels) Media(_ context.Context, req Request, open func() io.ReadWriter) Response {
	if req.Type != RequestTunnelOpen || req.Tunnel == nil || req.Tunnel.Service == "vnc" {
		return Fail(CodeInvalid, "service-down")
	}
	rw := open()
	var buf []byte
	for {
		p, err := ReadPacket(rw, buf)
		if err != nil {
			return Response{OK: true}
		}
		buf = p
		if WritePacket(rw, p) != nil {
			return Response{OK: true}
		}
	}
}

// A tunnel's bytes cross the local API both ways as packets 14, a long
// write in pieces; a refusal names its reason.
func TestTunnelCarriesBytes(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "t.sock")
	ln, err := ListenWith(ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ServeWith(ctx, ln, echoTunnels{}, Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: CurrentSID()})
	c := NewClient(path)
	conn, err := c.Tunnel(ctx, TunnelRequest{DeviceID: "100000002", Service: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	long := bytes.Repeat([]byte("kyokai "), 30000) // more than one packet
	go func() { _, _ = conn.Write(long) }()
	got := make([]byte, len(long))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, long) {
		t.Fatal("the bytes came back changed")
	}
	_, err = c.Tunnel(ctx, TunnelRequest{DeviceID: "100000002", Service: "vnc"})
	var refused *RequestError
	if !errors.As(err, &refused) || refused.Message != "service-down" {
		t.Fatalf("a refused tunnel: %v", err)
	}
}
