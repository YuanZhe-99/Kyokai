package main

import (
	"encoding/binary"
	"encoding/json"
	"image"
	"testing"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

func TestFailureReason(t *testing.T) {
	cases := []struct {
		state, failure, want string
	}{
		{"failed", `"unreachable"`, "unreachable"},
		{"failed", `{"refused":"service-down"}`, "service-down"},
		{"closed", `null`, "closed"},
		{"connecting", `null`, "timeout"},
	}
	for _, c := range cases {
		if got := failureReason(c.state, json.RawMessage(c.failure)); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.state, c.failure, got, c.want)
		}
	}
}

func TestKept(t *testing.T) {
	if kept(signIn{Method: "password", User: "u", Password: "p"}, true, false) != nil {
		t.Error("a saved sign-in is not saved again")
	}
	k := kept(signIn{Method: "password", User: "u", Password: "p"}, true, true)
	if k.Method != "password" || *k.Password != "p" {
		t.Errorf("remembered: %+v", k)
	}
	k = kept(signIn{Method: "key", User: "u", Password: "p"}, true, true)
	if *k.Password != "" {
		t.Error("a key sign-in keeps no password")
	}
	k = kept(signIn{Method: "password", User: "u", Password: "p"}, false, true)
	if k.Method != "" || k.Password == nil || *k.Password != "" {
		t.Errorf("not remembered forgets the password and method: %+v", k)
	}
	b, _ := json.Marshal(k)
	if string(b) != `{"user":"u","password":""}` {
		t.Errorf("an empty password is sent, to remove the saved one: %s", b)
	}
}

func TestPlan(t *testing.T) {
	pw := "saved"
	saved := credentials{Method: "account", User: "su", Password: &pw}
	if p := (openParams{}).plan(saved); p.Method != "account" || p.User != "su" || p.Password != "saved" {
		t.Errorf("the saved sign-in: %+v", p)
	}
	if p := (openParams{Method: "none"}).plan(saved); p.Method != "none" || p.Password != "" {
		t.Errorf("the form's: %+v", p)
	}
}

// A picture becomes one frame of what changed, RGBA and opaque, whatever
// number of updates came; a small buffer learns the size it needs.
func TestFrames(t *testing.T) {
	c := newClient(1)
	c.resized(4, 2)
	buf := make([]byte, 1<<10)
	if n := c.read(buf, 0); n != 1+24+4*2*4 {
		t.Fatalf("the first frame is the whole picture: %d", n)
	}
	fb := bgra.New(4, 2)
	copy(fb.Pix[4*1:], []byte{10, 20, 30, 0}) // B,G,R at (1, 0)
	c.updated(fb, image.Rect(1, 0, 2, 1))
	c.updated(fb, image.Rect(2, 1, 3, 2))
	n := c.read(buf, 0)
	head := buf[1:25]
	at := func(i int) uint32 { return binary.BigEndian.Uint32(head[i*4:]) }
	if buf[0] != packetFrame || at(0) != 1 || at(1) != 0 || at(2) != 2 || at(3) != 2 || at(4) != 4 || at(5) != 2 {
		t.Fatalf("one frame of the union: %v", head)
	}
	if n != 25+2*2*4 {
		t.Fatalf("length %d", n)
	}
	if px := buf[25:29]; px[0] != 30 || px[1] != 20 || px[2] != 10 || px[3] != 255 {
		t.Errorf("RGBA, opaque: %v", px)
	}
	if c.read(buf, 0) != -1 {
		t.Error("nothing changed since")
	}
	c.resized(16, 16)
	if n := c.read(make([]byte, 8), 0); n != -(1 + 24 + 16*16*4) {
		t.Errorf("a small buffer is told the size: %d", n)
	}
	if n := c.read(make([]byte, 2000), 0); n != 1+24+16*16*4 {
		t.Errorf("then gets the packet: %d", n)
	}
}

func TestEnd(t *testing.T) {
	c := clients.add()
	c.push(packetData, []byte("hi"))
	c.end(map[string]any{"state": "closed"})
	buf := make([]byte, 256)
	if n := c.read(buf, 0); n != 3 || buf[0] != packetData {
		t.Fatalf("queued data first: %d", n)
	}
	if n := c.read(buf, 0); buf[0] != packetState || n < 2 {
		t.Fatalf("then the last state: %d", n)
	}
	if n := c.read(buf, time.Millisecond); n != -2 || clients.get(c.id) != nil {
		t.Fatalf("then the end: %d", n)
	}
}
