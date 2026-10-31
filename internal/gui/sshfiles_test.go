package gui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// memFiles is remoteFiles over an in-memory SFTP server whose home is "/".
func memFiles(t *testing.T) *remoteFiles {
	t.Helper()
	a, b := net.Pipe()
	srv := sftp.NewRequestServer(a, sftp.InMemHandler())
	go func() { _ = srv.Serve() }()
	c, err := sftp.NewClientPipe(b, b)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &remoteFiles{sftp: c, home: "/", ctx: ctx, cancel: cancel, changed: func() {}, dirs: map[string]*remoteDir{}}
	t.Cleanup(func() {
		r.close()
		_ = srv.Close()
	})
	return r
}

// settle waits until no transfer waits or runs, answering each name taken
// with answer (nil: a test that expects none).
func settle(t *testing.T, r *remoteFiles, answer *fileAnswer) []*fileXfer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if q := r.question(); q != nil {
			if answer == nil {
				t.Fatalf("asked about %s", q.name)
			}
			r.answer(*answer)
		}
		xs := r.list()
		r.mu.Lock()
		busy := slices.ContainsFunc(xs, func(x *fileXfer) bool { return x.state == xferWaiting || x.state == xferRunning })
		r.mu.Unlock()
		if !busy {
			return xs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the transfers did not end")
	return nil
}

// wantFile checks a local file's contents.
func wantFile(t *testing.T, p, want string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil || string(got) != want {
		t.Fatalf("%s: %q %v, want %q", p, got, err, want)
	}
}

// TestRemoteFilesOverSFTP: a file and a folder go up and come back down,
// whole; a name taken asks, and Keep both, Skip and Replace each do what
// they say, for the rest of the batch when chosen so; a folder is renamed,
// a taken name refused, and deleted with what it holds; nothing half-copied
// stays behind.
func TestRemoteFilesOverSFTP(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	r := memFiles(t)
	local := t.TempDir()
	note := filepath.Join(local, "note.txt")
	_ = os.WriteFile(note, []byte("hello"), 0o644)
	tree := filepath.Join(local, "proj")
	_ = os.MkdirAll(filepath.Join(tree, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(tree, "src", "main.go"), []byte("package main"), 0o644)

	r.upload([]string{note, tree}, "/")
	for _, x := range settle(t, r, nil) {
		if x.state != xferDone || x.done.Load() != x.size.Load() {
			t.Fatalf("%s: %s %q, %d of %d", x.name, x.state, x.err, x.done.Load(), x.size.Load())
		}
	}
	d := r.snapshot("/")
	for !slices.ContainsFunc(d.entries, func(e remoteEntry) bool { return e.name == "proj" }) {
		time.Sleep(5 * time.Millisecond)
		d = r.snapshot("/")
	}
	if names := entryNames(d.entries); !slices.Equal(names, []string{"proj/", "note.txt"}) {
		t.Fatalf("listed %v", names)
	}

	down := t.TempDir()
	r.download([]string{"/note.txt", "/proj"}, down)
	settle(t, r, nil)
	wantFile(t, filepath.Join(down, "note.txt"), "hello")
	wantFile(t, filepath.Join(down, "proj", "src", "main.go"), "package main")

	// Again: both names are taken. Keep both, for the rest.
	r.clearFinished()
	r.download([]string{"/note.txt", "/proj"}, down)
	settle(t, r, &fileAnswer{policy: policyBoth, rest: true})
	wantFile(t, filepath.Join(down, "note (2).txt"), "hello")
	wantFile(t, filepath.Join(down, "proj (2)", "src", "main.go"), "package main")

	// Skip leaves what is there; Replace writes over it.
	_ = os.WriteFile(filepath.Join(down, "note.txt"), []byte("mine"), 0o644)
	r.download([]string{"/note.txt"}, down)
	settle(t, r, &fileAnswer{policy: policySkip})
	wantFile(t, filepath.Join(down, "note.txt"), "mine")
	r.download([]string{"/note.txt"}, down)
	settle(t, r, &fileAnswer{policy: policyReplace})
	wantFile(t, filepath.Join(down, "note.txt"), "hello")
	if parts, _ := filepath.Glob(filepath.Join(down, "*"+partSuffix)); len(parts) != 0 {
		t.Fatalf("part files left: %v", parts)
	}

	at, err := r.mkdir("/", "New folder")
	if err != nil || at != "/New folder" {
		t.Fatalf("mkdir: %q %v", at, err)
	}
	if at, err := r.mkdir("/", "New folder"); err != nil || at != "/New folder 2" {
		t.Fatalf("a second new folder: %q %v", at, err)
	}
	if err := r.rename("/New folder 2", "proj"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a rename onto a name taken: %v", err)
	}
	if err := r.rename("/proj", "app"); err != nil {
		t.Fatal(err)
	}
	if err := r.remove("/app", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sftp.Stat("/app/src/main.go"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a folder deleted kept its files: %v", err)
	}
}

// TestCancelEndsATransfer: a transfer canceled ends as canceled, and a
// download canceled leaves no file.
func TestCancelEndsATransfer(t *testing.T) {
	r := memFiles(t)
	f, _ := r.sftp.Create("/big")
	_, _ = f.Write(bytes.Repeat([]byte("x"), 1<<20))
	_ = f.Close()
	down := t.TempDir()
	r.download([]string{"/big"}, down)
	x := r.list()[0]
	x.cancel()
	settle(t, r, nil)
	r.mu.Lock()
	state := x.state
	r.mu.Unlock()
	if state != xferCanceled {
		t.Fatalf("state %s", state)
	}
	if files, _ := os.ReadDir(down); len(files) != 0 {
		t.Fatalf("left %v", files)
	}
}

func entryNames(es []remoteEntry) []string {
	var names []string
	for _, e := range es {
		n := e.name
		if e.dir {
			n += "/"
		}
		names = append(names, n)
	}
	return names
}

// TestSCPProtocol: a file received and sent as scp -f and scp -t speak, and
// a refusal from the server read as its reason.
func TestSCPProtocol(t *testing.T) {
	// Receiving: the server offers a file once told to start.
	srvIn, in := io.Pipe()
	out, srvOut := io.Pipe()
	go func() {
		b := make([]byte, 1)
		_, _ = srvIn.Read(b)
		_, _ = srvOut.Write([]byte("C0644 5 a.txt\n"))
		_, _ = srvIn.Read(b)
		_, _ = srvOut.Write([]byte("hello\x00"))
		_, _ = srvIn.Read(b)
	}()
	var got bytes.Buffer
	err := scpReceive(in, bufio.NewReader(out), func(name string, size int64, body io.Reader) error {
		if name != "a.txt" || size != 5 {
			t.Errorf("offered %q %d", name, size)
		}
		_, err := io.Copy(&got, body)
		return err
	})
	if err != nil || got.String() != "hello" {
		t.Fatalf("received %q %v", got.String(), err)
	}

	// A refusal.
	srvIn, in = io.Pipe()
	out, srvOut = io.Pipe()
	go func() {
		b := make([]byte, 1)
		_, _ = srvIn.Read(b)
		_, _ = srvOut.Write([]byte("\x01scp: /nope: No such file or directory\n"))
	}()
	var se *scpError
	if err := scpReceive(in, bufio.NewReader(out), nil); !errors.As(err, &se) || se.why != "scp: /nope: No such file or directory" {
		t.Fatalf("refusal: %v", err)
	}

	// Sending: the server acknowledges each step.
	srvIn, in = io.Pipe()
	out, srvOut = io.Pipe()
	sent := make(chan string, 1)
	go func() {
		rd := bufio.NewReader(srvIn)
		_, _ = srvOut.Write([]byte{0})
		head, _ := rd.ReadString('\n')
		_, _ = srvOut.Write([]byte{0})
		body := make([]byte, 6)
		_, _ = io.ReadFull(rd, body)
		_, _ = srvOut.Write([]byte{0})
		sent <- head + string(body)
	}()
	if err := scpSend(in, bufio.NewReader(out), "b.txt", 5, bytes.NewReader([]byte("world"))); err != nil {
		t.Fatal(err)
	}
	if s := <-sent; s != "C0644 5 b.txt\nworld\x00" {
		t.Fatalf("sent %q", s)
	}
}

// TestPathWords: SCP's quoting for each shell, the terminal's cd, the
// names a copy kept beside another takes, the names refused, and the
// tree's marks from the transfers.
func TestPathWords(t *testing.T) {
	for _, c := range []struct {
		p       string
		windows bool
		want    string
	}{
		{"/srv/a b", false, `'/srv/a b'`},
		{"~/it's", false, `~/'it'\''s'`},
		{"~", false, "~"},
		{`C:\Users\me\a.txt`, true, `"C:\Users\me\a.txt"`},
	} {
		if got := scpQuote(c.p, c.windows); got != c.want {
			t.Errorf("scpQuote(%q, %v) = %s, want %s", c.p, c.windows, got, c.want)
		}
	}
	if got := cdCommand("/C:/Users/me", true); got != `cd "C:\Users\me"` {
		t.Errorf("cd on Windows: %s", got)
	}
	if got := cdCommand("/home/me/a b", false); got != `cd '/home/me/a b'` {
		t.Errorf("cd: %s", got)
	}
	for _, c := range []struct {
		name string
		dir  bool
		want string
	}{{"report.pdf", false, "report (2).pdf"}, {"archive.tar.gz", false, "archive.tar (2).gz"}, {".bashrc", false, ".bashrc (2)"}, {"v1.2", true, "v1.2 (2)"}} {
		if got := numbered(c.name, 2, c.dir); got != c.want {
			t.Errorf("numbered(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	for name, ok := range map[string]bool{"a": true, "a b.txt": true, "": false, ".": false, "..": false, "a/b": false, `a\b`: false} {
		if validName(name) != ok {
			t.Errorf("validName(%q) = %v", name, !ok)
		}
	}
	marks := transferMarks([]*fileXfer{
		{up: true, name: "a", dst: "/x", state: xferRunning},
		{name: "b", src: "/x/b", state: xferDone},
		{name: "c", src: "/x/c", state: xferFailed},
		{name: "c", src: "/x/c", state: xferDone},
		{up: true, name: "d", dst: "/x", state: xferDone},
	})
	want := map[string]string{"/x/a": "↑", "/x/b": "↓", "/x/c": "↓"}
	if len(marks) != len(want) {
		t.Fatalf("marks %v", marks)
	}
	for k, v := range want {
		if marks[k] != v {
			t.Errorf("marks %v, want %v", marks, want)
		}
	}
}

// TestFilesPanel: the panel lists the home folder, hidden files only once
// asked; files dropped on a folder go into it; Delete asks first, then
// deletes; a name taken asks over the pane and Keep both answers it.
func TestFilesPanel(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	r := memFiles(t)
	_ = r.sftp.Mkdir("/docs")
	for _, name := range []string{"/a.txt", "/.profile"} {
		f, _ := r.sftp.Create(name)
		_, _ = f.Write([]byte("x"))
		_ = f.Close()
	}
	c := &sshClient{s: &serviceTab{app: a, device: "657365333", name: "Studio Mac · 657 365 333"}}
	c.opening.Do(func() {})
	c.files.Store(r)
	c.panel.open = true
	tt := ui.NewTester(func(ctx *ui.Context) {
		ui.Row(ctx).Fill().Children(func() { c.panelView(ctx, "k", false) })
	}, 360, 640)
	frames := func(until func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			tt.Frame()
			if until() {
				return
			}
		}
		t.Fatalf("never: %q", tt.Texts())
	}
	frames(func() bool { return tt.HasText("a.txt") && tt.HasText("docs") })
	shot := func(name string) {
		dir := os.Getenv("SCREENSHOT_DIR")
		if dir == "" {
			return
		}
		for _, dark := range []bool{false, true} {
			tt.SetDark(dark)
			tt.Frame()
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("files-%s-%v.png", name, dark)))
			if err != nil {
				t.Fatal(err)
			}
			_ = png.Encode(f, tt.Image())
			_ = f.Close()
		}
		tt.SetDark(false)
	}
	shot("tree")
	c.panel.tab = tabSettings
	frames(func() bool { return tt.HasText(L.TermLookFor("Studio Mac")) && tt.HasText(L.TermPreview()) })
	shot("settings")
	c.panel.tab = tabFiles
	frames(func() bool { return tt.HasText("a.txt") })
	if tt.HasText(".profile") {
		t.Fatal("a hidden file shows")
	}
	if err := tt.Click(L.FilesShowHidden()); err != nil {
		t.Fatal(err)
	}
	frames(func() bool { return tt.HasText(".profile") })

	local := filepath.Join(t.TempDir(), "drop.txt")
	_ = os.WriteFile(local, []byte("dropped"), 0o644)
	docs, _ := tt.Find("docs")
	if !tt.DropFiles(docs.X+4, docs.Y+4, local) {
		t.Fatal("the folder took no files")
	}
	tt.Frame()
	settle(t, r, nil)
	if st, err := r.sftp.Stat("/docs/drop.txt"); err != nil || st.Size() != 7 {
		t.Fatalf("dropped on docs: %v", err)
	}
	if c.panel.tab != tabTransfers {
		t.Fatal("a drop does not show the transfers")
	}
	shot("transfers")

	c.panel.tab = tabFiles
	frames(func() bool { return tt.HasText("a.txt") })
	if err := tt.RightClick("a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(L.FilesDeleteAsk()); err != nil {
		t.Fatalf("%v in %q", err, tt.Menu())
	}
	frames(func() bool { return tt.HasText(L.FilesDeleteHint()) })
	shot("delete")
	if _, err := r.sftp.Stat("/a.txt"); err != nil {
		t.Fatal("deleted before the answer")
	}
	if err := tt.Click(L.FilesDelete()); err != nil {
		t.Fatal(err)
	}
	frames(func() bool { _, err := r.sftp.Stat("/a.txt"); return errors.Is(err, os.ErrNotExist) })

	q := &fileQuestion{name: "a.txt", where: "/home/me/Downloads", answer: make(chan fileAnswer, 1)}
	r.mu.Lock()
	r.ask = q
	r.mu.Unlock()
	frames(func() bool { return tt.HasText(L.FilesTaken("a.txt", "Downloads")) })
	shot("taken")
	if err := tt.Click(L.FilesKeepBoth()); err != nil {
		t.Fatal(err)
	}
	if got := <-q.answer; got.policy != policyBoth || got.rest {
		t.Fatalf("answer %+v", got)
	}
}

// TestFilesOpenOverSSH: the panel starts SFTP on a channel of the pane's
// own SSH connection, at the server's working folder; a server that
// refuses the subsystem leaves SCP alone.
func TestFilesOpenOverSSH(t *testing.T) {
	for _, offer := range []bool{true, false} {
		client, server := connPair(t)
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		host, _ := ssh.NewSignerFromKey(priv)
		cfg := &ssh.ServerConfig{NoClientAuth: true}
		cfg.AddHostKey(host)
		go func() {
			_, chans, reqs, err := ssh.NewServerConn(server, cfg)
			if err != nil {
				return
			}
			go ssh.DiscardRequests(reqs)
			for nc := range chans {
				ch, reqs, err := nc.Accept()
				if err != nil {
					return
				}
				go func() {
					for r := range reqs {
						if r.Type == "subsystem" && offer && string(r.Payload[4:]) == "sftp" {
							_ = r.Reply(true, nil)
							go func() { _ = sftp.NewRequestServer(ch, sftp.InMemHandler()).Serve() }()
							continue
						}
						_ = r.Reply(false, nil)
					}
				}()
			}
		}()
		conn, chans, reqs, err := ssh.NewClientConn(client, "localhost:22", &ssh.ClientConfig{User: "me", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
		if err != nil {
			t.Fatal(err)
		}
		r := openRemoteFiles(ssh.NewClient(conn, chans, reqs), false, func() {})
		if (r.sftp != nil) != offer {
			t.Fatalf("offered SFTP %v, got %v", offer, r.sftp != nil)
		}
		if offer && r.home != "/" {
			t.Fatalf("home %q", r.home)
		}
		r.close()
	}
}
