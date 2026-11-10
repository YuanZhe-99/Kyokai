package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/pkg/sftp"
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

// sftpTest is an sftp.open client against pkg/sftp's in-memory server,
// with its packets.
func sftpTest(t *testing.T) (*client, *sftpFiles) {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	srv := sftp.NewRequestServer(struct {
		io.Reader
		io.WriteCloser
	}{sr, sw}, sftp.InMemHandler())
	go srv.Serve()
	sc, err := sftp.NewClientPipe(cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	c := clients.add()
	f := startFiles(context.Background(), c, nil, sc)
	go f.serve()
	t.Cleanup(func() { c.close(); _ = srv.Close() })
	return c, f
}

// filesTest runs a files call on c.
func filesTest(t *testing.T, c *client, method string, params map[string]any) (map[string]any, error) {
	t.Helper()
	params["id"] = c.id
	raw, _ := json.Marshal(params)
	r, err := run(method, raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(r)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}

// packets reads c's packets of kind until one passes done.
func packetsUntil(t *testing.T, c *client, kind byte, done func(map[string]any) bool) map[string]any {
	t.Helper()
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := c.read(buf, 100*time.Millisecond)
		if n <= 0 || buf[0] != kind {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(buf[1:n], &m)
		if done(m) {
			return m
		}
	}
	t.Fatal("no such packet")
	return nil
}

func TestSFTP(t *testing.T) {
	c, f := sftpTest(t)
	packetsUntil(t, c, packetState, func(m map[string]any) bool { return m["state"] == "connected" })
	if got := f.abs("~"); got != f.home {
		t.Errorf("~ is home: %q", got)
	}
	if got := f.abs("a/../b/"); got != path.Join(f.home, "b") {
		t.Errorf("relative below home, cleaned: %q", got)
	}
	if _, err := filesTest(t, c, "sftp.mkdir", map[string]any{"path": "/Docs"}); err != nil {
		t.Fatal(err)
	}
	// Up, then down again.
	dir := t.TempDir()
	local := filepath.Join(dir, "up.txt")
	body := bytes.Repeat([]byte("kyokai "), 10000)
	_ = os.WriteFile(local, body, 0o644)
	r, err := filesTest(t, c, "sftp.upload", map[string]any{"local": local, "remote": "/Docs/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	n := r["transfer"]
	end := transferEnd(t, c, n)
	if end["state"] != "done" || end["done"] != float64(len(body)) || end["total"] != float64(len(body)) {
		t.Fatalf("upload: %v", end)
	}
	r, _ = filesTest(t, c, "sftp.list", map[string]any{"path": "/Docs"})
	entries, _ := r["entries"].([]any)
	if r["path"] != "/Docs" || len(entries) != 1 || entries[0].(map[string]any)["name"] != "a.txt" || entries[0].(map[string]any)["size"] != float64(len(body)) {
		t.Fatalf("list: %v", r)
	}
	if _, err := filesTest(t, c, "sftp.rename", map[string]any{"from": "/Docs/a.txt", "to": "/Docs/b.txt"}); err != nil {
		t.Fatal(err)
	}
	_, _ = filesTest(t, c, "sftp.mkdir", map[string]any{"path": "/Docs/c"})
	if _, err := filesTest(t, c, "sftp.rename", map[string]any{"from": "/Docs/b.txt", "to": "/Docs/c"}); err == nil || err.Error() != "exists" {
		t.Errorf("a name taken refuses: %v", err)
	}
	down := filepath.Join(dir, "sub", "down.txt")
	r, _ = filesTest(t, c, "sftp.download", map[string]any{"remote": "/Docs/b.txt", "local": down})
	n = r["transfer"]
	end = transferEnd(t, c, n)
	if got, _ := os.ReadFile(down); end["state"] != "done" || !bytes.Equal(got, body) {
		t.Fatalf("download: %v, %d bytes", end, len(got))
	}
	if _, err := os.Stat(down + partSuffix); err == nil {
		t.Error("no part file left")
	}
	r, _ = filesTest(t, c, "sftp.download", map[string]any{"remote": "/Docs/none", "local": filepath.Join(dir, "x")})
	n = r["transfer"]
	end = transferEnd(t, c, n)
	if end["state"] != "failed" || end["error"] != "not-found" {
		t.Errorf("a missing file fails: %v", end)
	}
	if _, err := filesTest(t, c, "sftp.remove", map[string]any{"path": "/Docs/b.txt"}); err != nil {
		t.Fatal(err)
	}
	r, _ = filesTest(t, c, "sftp.list", map[string]any{"path": "/Docs"})
	if entries, _ := r["entries"].([]any); len(entries) != 1 || entries[0].(map[string]any)["dir"] != true {
		t.Errorf("removed: %v", r)
	}
	// close ends it.
	c.close()
	packetsUntil(t, c, packetState, func(m map[string]any) bool { return m["state"] == "closed" })
	if _, err := filesTest(t, c, "sftp.list", map[string]any{"path": "/"}); err == nil {
		t.Error("closed: no more calls")
	}
}

func TestSortEntries(t *testing.T) {
	e := []fileEntry{{Name: "b"}, {Name: "Z", Dir: true}, {Name: "A"}, {Name: "a", Dir: true}}
	sortEntries(e)
	var got []string
	for _, x := range e {
		got = append(got, x.Name)
	}
	if strings.Join(got, ",") != "a,Z,A,b" {
		t.Errorf("folders first, then by name: %v", got)
	}
}

// transferEnd is transfer n's last packet.
func transferEnd(t *testing.T, c *client, n any) map[string]any {
	t.Helper()
	return packetsUntil(t, c, packetTransfer, func(m map[string]any) bool {
		return m["transfer"] == n && m["state"] != "waiting" && m["state"] != "running"
	})
}
