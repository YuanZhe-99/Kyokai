package main

// Files over SSH as the desktop's internal/gui/sshfiles.go has them,
// without its UI: SFTP on a connection signed in as ssh.open signs in,
// folders listed and changed by synchronous calls, files copied one after
// another on the client's goroutine with their progress as packets.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// packetTransfer is JSON {"transfer": its number, "done", "total": bytes,
// "state": "waiting"|"running"|"done"|"failed"|"cancelled", "error"?: why
// (fileReason)}: a transfer's progress, at most every progressEvery while
// it runs, and its end.
const packetTransfer = 5

// listTimeout is the longest a folder's listing may take.
const listTimeout = time.Minute

// progressEvery is how often a running transfer reports its progress.
const progressEvery = 200 * time.Millisecond

// partSuffix marks a download not yet whole.
const partSuffix = ".kyokai-part"

// errExists: a rename's new name is taken.
var errExists = errors.New("exists")

// sftpFiles is an sftp.open client's files: its connection, SFTP on it,
// and the transfers waiting for its goroutine.
type sftpFiles struct {
	c      *client
	conn   *sshConn
	sftp   *sftp.Client
	home   string
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan *transfer
	once   sync.Once

	mu   sync.Mutex
	next uint64
	all  map[uint64]*transfer
}

// transfer is one file copied to the server (up) or from it.
type transfer struct {
	n             uint64
	up            bool
	local, remote string
	done, total   atomic.Int64
	ctx           context.Context
	cancel        context.CancelFunc
}

// sftps is the open sftp.open clients by client id.
var sftps = struct {
	mu  sync.Mutex
	all map[uint64]*sftpFiles
}{all: map[uint64]*sftpFiles{}}

func filesOf(id uint64) *sftpFiles {
	sftps.mu.Lock()
	defer sftps.mu.Unlock()
	return sftps.all[id]
}

// runSFTP signs client c in as runSSH does, starts SFTP and serves it
// until the client is closed or the connection ends.
func runSFTP(ctx context.Context, c *client, p openParams) {
	conn := sshSignIn(ctx, c, p)
	if conn == nil {
		return
	}
	sc, err := sftp.NewClient(conn.client)
	if err != nil {
		conn.close()
		c.end(map[string]any{"state": "failed", "error": "no-sftp"})
		return
	}
	f := startFiles(ctx, c, conn, sc)
	// The connection's end ends the client too.
	go func() {
		_ = conn.client.Wait()
		f.cancel()
	}()
	f.serve()
}

// startFiles makes client c's files of SFTP client sc, reachable by the
// files calls, and reports c connected.
// Inputs: conn, the connection sc runs on, closed with it (nil in tests).
func startFiles(ctx context.Context, c *client, conn *sshConn, sc *sftp.Client) *sftpFiles {
	home, err := sc.Getwd()
	if err != nil || home == "" {
		home = "/"
	}
	fctx, cancel := context.WithCancel(ctx)
	f := &sftpFiles{c: c, conn: conn, sftp: sc, home: home, ctx: fctx, cancel: cancel, queue: make(chan *transfer, 1024), all: map[uint64]*transfer{}}
	sftps.mu.Lock()
	sftps.all[c.id] = f
	sftps.mu.Unlock()
	c.mu.Lock()
	c.closer = f.close
	c.mu.Unlock()
	c.state(map[string]any{"state": "connected"})
	return f
}

// serve runs the transfers queued, one after another, until the files end;
// then it cancels those waiting and ends the client.
func (f *sftpFiles) serve() {
	for {
		select {
		case <-f.ctx.Done():
			f.close()
			sftps.mu.Lock()
			delete(sftps.all, f.c.id)
			sftps.mu.Unlock()
			f.mu.Lock()
			for _, t := range f.all {
				t.cancel()
				f.report(t, "cancelled", nil)
			}
			f.all = map[uint64]*transfer{}
			f.mu.Unlock()
			f.c.end(map[string]any{"state": "closed"})
			return
		case t := <-f.queue:
			f.run(t)
		}
	}
}

// close ends the transfers, SFTP and the connection.
// Notes: SFTP's close waits for the server's side, which the connection
// closing ends: it is not waited for.
func (f *sftpFiles) close() {
	f.once.Do(func() {
		f.cancel()
		go f.sftp.Close()
		if f.conn != nil {
			f.conn.close()
		}
	})
}

// abs is a path the app gave as an absolute clean one: "" and "~" are the
// home folder, "~/x" and relative paths are below it.
func (f *sftpFiles) abs(p string) string {
	switch {
	case p == "" || p == "~":
		return f.home
	case strings.HasPrefix(p, "~/"):
		p = path.Join(f.home, p[2:])
	case !path.IsAbs(p):
		p = path.Join(f.home, p)
	}
	return path.Clean(p)
}

// fileEntry is a file or folder in a listing.
type fileEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified int64  `json:"modified"`
	Link     bool   `json:"link"`
}

// entryOf is a listed file; a link to a folder is a folder.
func entryOf(fi fs.FileInfo, target func() (fs.FileInfo, error)) fileEntry {
	e := fileEntry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mode: fi.Mode().String(), Modified: fi.ModTime().Unix()}
	if fi.Mode()&fs.ModeSymlink != 0 {
		e.Link = true
		if st, err := target(); err == nil {
			e.Dir = st.IsDir()
		}
	}
	return e
}

// sortEntries puts folders first, then by name, ignoring case.
func sortEntries(e []fileEntry) {
	slices.SortFunc(e, func(a, b fileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// list reads a folder.
// Returns: {"path": its absolute path, "entries": folders first}.
func (f *sftpFiles) list(p string) (any, error) {
	p = f.abs(p)
	ctx, cancel := context.WithTimeout(f.ctx, listTimeout)
	infos, err := f.sftp.ReadDirContext(ctx, p)
	cancel()
	if err != nil {
		return nil, err
	}
	entries := make([]fileEntry, 0, len(infos))
	for _, fi := range infos {
		entries = append(entries, entryOf(fi, func() (fs.FileInfo, error) { return f.sftp.Stat(path.Join(p, fi.Name())) }))
	}
	sortEntries(entries)
	return map[string]any{"path": p, "entries": entries}, nil
}

// rename moves a file or folder; a name taken refuses with "exists".
func (f *sftpFiles) rename(from, to string) error {
	from, to = f.abs(from), f.abs(to)
	if _, err := f.sftp.Lstat(to); err == nil {
		return errExists
	}
	return f.sftp.Rename(from, to)
}

// start queues a transfer and reports it waiting.
// Returns: its number.
func (f *sftpFiles) start(up bool, local, remote string) (uint64, error) {
	if !filepath.IsAbs(local) {
		return 0, fmt.Errorf("local is not an absolute path")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	f.mu.Lock()
	f.next++
	t := &transfer{n: f.next, up: up, local: local, remote: f.abs(remote), ctx: ctx, cancel: cancel}
	f.all[t.n] = t
	f.mu.Unlock()
	f.report(t, "waiting", nil)
	select {
	case f.queue <- t:
		return t.n, nil
	default:
		f.finish(t, "failed", errors.New("too many transfers"))
		return 0, fmt.Errorf("too many transfers")
	}
}

// stop cancels a transfer, waiting or running; one ended is left.
func (f *sftpFiles) stop(n uint64) error {
	f.mu.Lock()
	t := f.all[n]
	f.mu.Unlock()
	if t == nil {
		return fmt.Errorf("no such transfer")
	}
	t.cancel()
	return nil
}

// run copies one transfer and reports how it ended.
func (f *sftpFiles) run(t *transfer) {
	if t.ctx.Err() != nil {
		f.finish(t, "cancelled", nil)
		return
	}
	f.report(t, "running", nil)
	// Progress while it runs.
	tick := time.NewTicker(progressEvery)
	ended, quiet := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(quiet)
		for {
			select {
			case <-tick.C:
				f.report(t, "running", nil)
			case <-ended:
				return
			}
		}
	}()
	var err error
	if t.up {
		err = f.put(t)
	} else {
		err = f.get(t)
	}
	tick.Stop()
	close(ended)
	<-quiet // no progress after the end
	switch {
	case t.ctx.Err() != nil:
		f.finish(t, "cancelled", nil)
	case err != nil:
		f.finish(t, "failed", err)
	default:
		f.finish(t, "done", nil)
	}
}

// get copies a remote file to its local path: into a part file, renamed
// into place once whole, so a copy that ends early leaves nothing.
func (f *sftpFiles) get(t *transfer) error {
	src, err := f.sftp.Open(t.remote)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errFolder
	}
	t.total.Store(st.Size())
	if err := os.MkdirAll(filepath.Dir(t.local), 0o755); err != nil {
		return err
	}
	part := t.local + partSuffix
	dst, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(&progress{w: dst, t: t}, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, t.local)
	}
	if err != nil {
		_ = os.Remove(part)
	}
	return err
}

// put copies a local file to its remote path; one that ends early is
// removed there.
func (f *sftpFiles) put(t *transfer) error {
	src, err := os.Open(t.local)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errFolder
	}
	t.total.Store(st.Size())
	dst, err := f.sftp.OpenFile(t.remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, &progressReader{r: src, t: t})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = f.sftp.Remove(t.remote)
	}
	return err
}

// errFolder: transfers copy files, not folders.
var errFolder = errors.New("is-folder")

// report queues a transfer packet.
func (f *sftpFiles) report(t *transfer, state string, err error) {
	fields := map[string]any{"transfer": t.n, "done": t.done.Load(), "total": t.total.Load(), "state": state}
	if err != nil {
		fields["error"] = fileReason(err)
	}
	body, _ := json.Marshal(fields)
	f.c.push(packetTransfer, body)
}

// finish reports a transfer's end and forgets it.
func (f *sftpFiles) finish(t *transfer, state string, err error) {
	t.cancel()
	f.mu.Lock()
	_, known := f.all[t.n]
	delete(f.all, t.n)
	f.mu.Unlock()
	if known {
		f.report(t, state, err)
	}
}

// progress counts what a download wrote, and stops it once cancelled.
type progress struct {
	w io.Writer
	t *transfer
}

func (p *progress) Write(b []byte) (int, error) {
	if err := p.t.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.t.done.Add(int64(n))
	return n, err
}

// progressReader counts what an upload read, and stops it once cancelled.
type progressReader struct {
	r io.Reader
	t *transfer
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.t.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.t.done.Add(int64(n))
	return n, err
}

// fileReason is a file call's or transfer's error as the app names it:
// permission-denied, not-found, exists, is-folder, connection-lost; else
// the error's text.
func fileReason(err error) string {
	var se *sftp.StatusError
	switch {
	case errors.Is(err, errExists), errors.Is(err, fs.ErrExist):
		return "exists"
	case errors.Is(err, errFolder):
		return "is-folder"
	case errors.Is(err, fs.ErrPermission), errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxPermissionDenied:
		return "permission-denied"
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile:
		return "not-found"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, os.ErrClosed), errors.Is(err, sftp.ErrSSHFxConnectionLost), errors.Is(err, context.Canceled):
		return "connection-lost"
	}
	return err.Error()
}

// filesCall runs one of the sftp.* calls on an open sftp client.
//
//	sftp.list {id, path}           {"path", "entries": [{name, dir, size,
//	                               mode, modified (unix s), link}]}
//	sftp.mkdir {id, path}          {}
//	sftp.remove {id, path}         {}: a file or an empty folder
//	sftp.rename {id, from, to}     {}; a name taken: "exists"
//	sftp.download {id, remote, local}, sftp.upload {id, local, remote}
//	                               {"transfer": n}; local is absolute, and
//	                               what is at the destination is replaced
//	sftp.cancel {id, transfer}     {}
//
// Paths on the server are absolute, or below the home folder ("" or "~"
// itself). Errors are fileReason's names.
func filesCall(method string, raw json.RawMessage) (any, error) {
	var p struct {
		ID       uint64 `json:"id"`
		Path     string `json:"path"`
		From     string `json:"from"`
		To       string `json:"to"`
		Remote   string `json:"remote"`
		Local    string `json:"local"`
		Transfer uint64 `json:"transfer"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad params")
	}
	f := filesOf(p.ID)
	if f == nil {
		if clients.get(p.ID) != nil {
			return nil, fmt.Errorf("not connected")
		}
		return nil, fmt.Errorf("no such client")
	}
	var err error
	switch method {
	case "sftp.list":
		r, err := f.list(p.Path)
		if err != nil {
			return nil, errors.New(fileReason(err))
		}
		return r, nil
	case "sftp.mkdir":
		err = f.sftp.Mkdir(f.abs(p.Path))
	case "sftp.remove":
		err = f.sftp.Remove(f.abs(p.Path))
	case "sftp.rename":
		err = f.rename(p.From, p.To)
	case "sftp.download", "sftp.upload":
		n, err := f.start(method == "sftp.upload", p.Local, p.Remote)
		if err != nil {
			return nil, err
		}
		return map[string]any{"transfer": n}, nil
	case "sftp.cancel":
		err = f.stop(p.Transfer)
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
	if err != nil {
		return nil, errors.New(fileReason(err))
	}
	return map[string]any{}, nil
}
