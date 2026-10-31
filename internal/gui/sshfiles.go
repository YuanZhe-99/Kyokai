package gui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// remoteFiles are an SSH pane's files on the server (doc/en-us/
// remote-services.md, "Files over SSH"): browsed and changed through SFTP,
// a channel of the pane's own SSH connection, or, where the server has no
// SFTP, copied by path with SCP. Transfers run one after another. It needs
// no permission of its own: the SSH account's rights on the server decide.
// Fields after mu are guarded by it.
type remoteFiles struct {
	conn *ssh.Client
	// sftp is nil where the server offers no SFTP: SCP alone then.
	sftp *sftp.Client
	// windows: the server is a Windows computer (SCP's quoting, the
	// terminal's cd).
	windows bool
	// home is where SFTP starts (the account's home folder).
	home    string
	ctx     context.Context
	cancel  context.CancelFunc
	changed func()

	mu        sync.Mutex
	dirs      map[string]*remoteDir
	transfers []*fileXfer
	working   bool
	// ask is a name already taken that a transfer waits on, or nil.
	ask *fileQuestion
}

// remoteDir is a folder's entries as last read, or why it could not be.
type remoteDir struct {
	entries []remoteEntry
	loading bool
	err     error
}

// remoteEntry is a file or folder in a remote folder.
type remoteEntry struct {
	name string
	dir  bool
	size int64
	mod  time.Time
}

// fileXfer is a copy of a file or a folder, to the server (up) or from it.
// Its state, err and at are guarded by remoteFiles.mu; size and done count
// bytes.
type fileXfer struct {
	up   bool
	name string
	// src is the local path (up) or the remote one; dst the folder it goes
	// into, remote (up) or local.
	src, dst   string
	size, done atomic.Int64
	state      string
	err        string
	// at is where it was written, for Show in folder.
	at      string
	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	batch   *batch
}

// A transfer's states.
const (
	xferWaiting  = "waiting"
	xferRunning  = "running"
	xferDone     = "done"
	xferFailed   = "failed"
	xferCanceled = "canceled"
)

// batch is what one action started (a drop, a choice of files): what to
// do with every name already taken, once someone chose it for the rest.
type batch struct{ policy string }

// What happens to a file whose name is taken where it goes.
const (
	policyReplace = "replace"
	policyBoth    = "both"
	policySkip    = "skip"
)

// fileQuestion asks what to do with a name taken where a file goes: the
// file there and the one coming, by size and time.
type fileQuestion struct {
	name, where   string
	there, coming remoteEntry
	answer        chan fileAnswer
}

// fileAnswer is a policy, and whether it holds for the rest of the batch.
type fileAnswer struct {
	policy string
	rest   bool
}

// errSCPFolder: SCP here copies files, not folders.
var errSCPFolder = errors.New("scp copies files only")

// openRemoteFiles starts SFTP on an SSH connection, or SCP alone where the
// server has no SFTP subsystem.
// Side effects: opens a channel of conn; changed runs on any goroutine as
// listings and transfers move.
func openRemoteFiles(conn *ssh.Client, windows bool, changed func()) *remoteFiles {
	ctx, cancel := context.WithCancel(context.Background())
	r := &remoteFiles{conn: conn, windows: windows, ctx: ctx, cancel: cancel, changed: changed, dirs: map[string]*remoteDir{}}
	if c, err := sftp.NewClient(conn); err == nil {
		r.sftp = c
		if home, err := c.Getwd(); err == nil {
			r.home = home
		} else {
			r.home = "/"
		}
	}
	return r
}

// close ends the transfers and SFTP.
// Notes: SFTP's close waits for the server to close its side, which the
// SSH connection closing after it does: it is not waited for here.
func (r *remoteFiles) close() {
	r.cancel()
	if r.sftp != nil {
		go r.sftp.Close()
	}
}

// dir is a folder's entries as last read; reading starts when none were.
func (r *remoteFiles) dir(p string) *remoteDir {
	r.mu.Lock()
	d, ok := r.dirs[p]
	r.mu.Unlock()
	if !ok {
		return r.read(p)
	}
	return d
}

// read reads a folder again, keeping what was there meanwhile.
func (r *remoteFiles) read(p string) *remoteDir {
	r.mu.Lock()
	d := r.dirs[p]
	if d == nil {
		d = &remoteDir{}
		r.dirs[p] = d
	}
	d.loading = true
	r.mu.Unlock()
	go func() {
		infos, err := r.sftp.ReadDir(p)
		entries := make([]remoteEntry, 0, len(infos))
		for _, fi := range infos {
			dir := fi.IsDir()
			if fi.Mode()&fs.ModeSymlink != 0 {
				if st, err := r.sftp.Stat(path.Join(p, fi.Name())); err == nil {
					dir = st.IsDir()
				}
			}
			entries = append(entries, remoteEntry{name: fi.Name(), dir: dir, size: fi.Size(), mod: fi.ModTime()})
		}
		sortEntries(entries)
		r.mu.Lock()
		*d = remoteDir{entries: entries, err: err}
		r.mu.Unlock()
		r.changed()
	}()
	return d
}

// snapshot is a copy of a folder's state for a frame.
func (r *remoteFiles) snapshot(p string) remoteDir {
	d := r.dir(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	return *d
}

// sortEntries puts folders first, then by name, ignoring case.
func sortEntries(e []remoteEntry) {
	slices.SortFunc(e, func(a, b remoteEntry) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
}

// validName reports whether a name typed for a file or folder can be one.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// mkdir makes a new folder in parent, named name or, taken, "name 2".
// Returns: its path.
func (r *remoteFiles) mkdir(parent, name string) (string, error) {
	p := path.Join(parent, name)
	for n := 2; ; n++ {
		if _, err := r.sftp.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			break
		}
		p = path.Join(parent, name+" "+strconv.Itoa(n))
	}
	err := r.sftp.Mkdir(p)
	r.read(parent)
	return p, err
}

// rename names a file or folder anew in its folder; a name taken refuses.
func (r *remoteFiles) rename(p, name string) error {
	to := path.Join(path.Dir(p), name)
	if _, err := r.sftp.Lstat(to); err == nil {
		return fs.ErrExist
	}
	err := r.sftp.Rename(p, to)
	r.read(path.Dir(p))
	return err
}

// remove deletes a file, or a folder with all it holds.
func (r *remoteFiles) remove(p string, dir bool) error {
	var err error
	if dir {
		err = r.sftp.RemoveAll(p)
	} else {
		err = r.sftp.Remove(p)
	}
	r.mu.Lock()
	for k := range r.dirs {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(r.dirs, k)
		}
	}
	r.mu.Unlock()
	r.read(path.Dir(p))
	return err
}

// send queues transfers as one batch and starts the queue.
func (r *remoteFiles) send(xs []*fileXfer) {
	b := &batch{}
	r.mu.Lock()
	for _, x := range xs {
		x.batch, x.state = b, xferWaiting
		x.ctx, x.cancel = context.WithCancel(r.ctx)
	}
	r.transfers = append(r.transfers, xs...)
	start := !r.working
	r.working = true
	r.mu.Unlock()
	if start {
		go r.work()
	}
	r.changed()
}

// upload queues local files and folders into a remote folder.
func (r *remoteFiles) upload(local []string, to string) {
	xs := make([]*fileXfer, 0, len(local))
	for _, l := range local {
		xs = append(xs, &fileXfer{up: true, name: filepath.Base(l), src: l, dst: to})
	}
	r.send(xs)
}

// download queues remote files and folders into a local folder.
func (r *remoteFiles) download(remote []string, to string) {
	xs := make([]*fileXfer, 0, len(remote))
	for _, p := range remote {
		xs = append(xs, &fileXfer{name: path.Base(p), src: p, dst: to})
	}
	r.send(xs)
}

// list is the transfers for a frame, oldest first.
func (r *remoteFiles) list() []*fileXfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.transfers)
}

// clearFinished drops the transfers that ended.
func (r *remoteFiles) clearFinished() {
	r.mu.Lock()
	r.transfers = slices.DeleteFunc(r.transfers, func(x *fileXfer) bool {
		return x.state != xferWaiting && x.state != xferRunning
	})
	r.mu.Unlock()
}

// again queues a transfer that failed or was canceled anew, by itself.
func (r *remoteFiles) again(x *fileXfer) {
	r.send([]*fileXfer{{up: x.up, name: x.name, src: x.src, dst: x.dst}})
}

// question is the question a transfer waits on, or nil.
func (r *remoteFiles) question() *fileQuestion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ask
}

// answer answers the question a transfer waits on.
func (r *remoteFiles) answer(a fileAnswer) {
	r.mu.Lock()
	q := r.ask
	r.ask = nil
	r.mu.Unlock()
	if q != nil {
		q.answer <- a
	}
}

// work runs the waiting transfers one after another until none waits.
func (r *remoteFiles) work() {
	for {
		r.mu.Lock()
		i := slices.IndexFunc(r.transfers, func(x *fileXfer) bool { return x.state == xferWaiting })
		if i < 0 {
			r.working = false
			r.mu.Unlock()
			return
		}
		x := r.transfers[i]
		x.state, x.started = xferRunning, time.Now()
		r.mu.Unlock()
		r.changed()
		err := r.run(x)
		r.mu.Lock()
		switch {
		case x.ctx.Err() != nil:
			x.state = xferCanceled
		case err != nil:
			x.state, x.err = xferFailed, fileError(err)
		default:
			x.state = xferDone
		}
		r.mu.Unlock()
		x.cancel()
		r.changed()
	}
}

// run copies one transfer.
func (r *remoteFiles) run(x *fileXfer) error {
	switch {
	case r.sftp == nil && x.up:
		return r.scpUp(x)
	case r.sftp == nil:
		return r.scpDown(x)
	case x.up:
		return r.sftpUp(x)
	}
	return r.sftpDown(x)
}

// sftpDown copies a remote file or folder into a local folder.
func (r *remoteFiles) sftpDown(x *fileXfer) error {
	st, err := r.sftp.Stat(x.src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		x.size.Store(st.Size())
		dst, ok, err := r.place(x, filepath.Join(x.dst, x.name), localInfo, remoteEntry{name: x.name, size: st.Size(), mod: st.ModTime()}, x.dst)
		if err != nil || !ok {
			return err
		}
		return r.sftpGetFile(x, x.src, dst)
	}
	type file struct {
		from, to string
		size     int64
	}
	top, ok, err := r.place(x, filepath.Join(x.dst, x.name), localInfo, remoteEntry{name: x.name, dir: true, mod: st.ModTime()}, x.dst)
	if err != nil || !ok {
		return err
	}
	var files []file
	var dirs []string
	w := r.sftp.Walk(x.src)
	for w.Step() {
		if err := w.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(w.Path(), x.src), "/")
		to := filepath.Join(top, filepath.FromSlash(rel))
		if w.Stat().IsDir() {
			dirs = append(dirs, to)
		} else if w.Stat().Mode().IsRegular() {
			files = append(files, file{w.Path(), to, w.Stat().Size()})
			x.size.Add(w.Stat().Size())
		}
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := r.sftpGetFile(x, f.from, f.to); err != nil {
			return err
		}
	}
	x.at = top
	return nil
}

// sftpGetFile copies one remote file to a local path: into a part file,
// renamed into place once whole, so a copy that ends early leaves nothing.
func (r *remoteFiles) sftpGetFile(x *fileXfer, from, to string) error {
	src, err := r.sftp.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	part := to + partSuffix
	dst, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(&progress{w: dst, x: x}, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, to)
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	if x.at == "" {
		x.at = to
	}
	return nil
}

// partSuffix marks a download not yet whole.
const partSuffix = ".kyokai-part"

// sftpUp copies a local file or folder into a remote folder.
func (r *remoteFiles) sftpUp(x *fileXfer) error {
	st, err := os.Stat(x.src)
	if err != nil {
		return err
	}
	defer r.read(x.dst)
	if !st.IsDir() {
		x.size.Store(st.Size())
		dst, ok, err := r.place(x, path.Join(x.dst, x.name), r.remoteInfo, remoteEntry{name: x.name, size: st.Size(), mod: st.ModTime()}, x.dst)
		if err != nil || !ok {
			return err
		}
		return r.sftpPutFile(x, x.src, dst)
	}
	top, ok, err := r.place(x, path.Join(x.dst, x.name), r.remoteInfo, remoteEntry{name: x.name, dir: true, mod: st.ModTime()}, x.dst)
	if err != nil || !ok {
		return err
	}
	type file struct{ from, to string }
	var files []file
	var dirs []string
	err = filepath.WalkDir(x.src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(x.src, p)
		to := path.Join(top, filepath.ToSlash(rel))
		switch {
		case d.IsDir():
			dirs = append(dirs, to)
		case d.Type().IsRegular():
			if fi, err := d.Info(); err == nil {
				x.size.Add(fi.Size())
			}
			files = append(files, file{p, to})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := r.sftp.MkdirAll(d); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := r.sftpPutFile(x, f.from, f.to); err != nil {
			return err
		}
	}
	x.at = top
	return nil
}

// sftpPutFile copies one local file to a remote path; one that ends early
// is removed there.
func (r *remoteFiles) sftpPutFile(x *fileXfer, from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := r.sftp.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, &progressReader{r: src, x: x})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = r.sftp.Remove(to)
		return err
	}
	if x.at == "" {
		x.at = to
	}
	return nil
}

// place decides where a file or folder goes when its name is taken there:
// replaced, beside it as "name (2)", or skipped, as its batch says or the
// person answers.
// Inputs: at, the path it would take; info, what is there (nil, nil: free).
// Returns: the path to write, false to skip it.
// Side effects: a replaced folder or file is removed first.
func (r *remoteFiles) place(x *fileXfer, at string, info func(string) (*remoteEntry, error), coming remoteEntry, where string) (string, bool, error) {
	there, err := info(at)
	if err != nil || there == nil {
		return at, true, err
	}
	r.mu.Lock()
	policy := x.batch.policy
	r.mu.Unlock()
	if policy == "" {
		q := &fileQuestion{name: x.name, where: where, there: *there, coming: coming, answer: make(chan fileAnswer, 1)}
		r.mu.Lock()
		r.ask = q
		r.mu.Unlock()
		r.changed()
		select {
		case a := <-q.answer:
			policy = a.policy
			if a.rest {
				r.mu.Lock()
				x.batch.policy = a.policy
				r.mu.Unlock()
			}
		case <-x.ctx.Done():
			r.mu.Lock()
			if r.ask == q {
				r.ask = nil
			}
			r.mu.Unlock()
			return "", false, x.ctx.Err()
		}
	}
	switch policy {
	case policySkip:
		x.size.Store(0)
		return "", false, nil
	case policyBoth:
		join, dir := filepath.Join, filepath.Dir
		if x.up {
			join, dir = path.Join, path.Dir
		}
		for n := 2; ; n++ {
			p := join(dir(at), numbered(path.Base(filepath.ToSlash(at)), n, coming.dir))
			if e, err := info(p); err != nil {
				return "", false, err
			} else if e == nil {
				return p, true, nil
			}
		}
	}
	// Replace: a folder there is not merged into.
	if there.dir != coming.dir || there.dir {
		if x.up {
			err = r.sftp.RemoveAll(at)
		} else {
			err = os.RemoveAll(at)
		}
	}
	return at, true, err
}

// numbered is "name (n).ext" ("name (n)" for a folder).
func numbered(name string, n int, dir bool) string {
	ext := ""
	if !dir {
		ext = path.Ext(name)
		if ext == name {
			ext = ""
		}
	}
	return strings.TrimSuffix(name, ext) + " (" + strconv.Itoa(n) + ")" + ext
}

// localInfo is a local file or folder, nil when there is none.
func localInfo(p string) (*remoteEntry, error) {
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &remoteEntry{name: st.Name(), dir: st.IsDir(), size: st.Size(), mod: st.ModTime()}, nil
}

// remoteInfo is a remote file or folder, nil when there is none.
func (r *remoteFiles) remoteInfo(p string) (*remoteEntry, error) {
	st, err := r.sftp.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &remoteEntry{name: st.Name(), dir: st.IsDir(), size: st.Size(), mod: st.ModTime()}, nil
}

// progress counts what a transfer wrote, and stops it once canceled.
type progress struct {
	w io.Writer
	x *fileXfer
}

func (p *progress) Write(b []byte) (int, error) {
	if err := p.x.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.x.done.Add(int64(n))
	return n, err
}

// progressReader counts what a transfer read, and stops it once canceled.
type progressReader struct {
	r io.Reader
	x *fileXfer
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.x.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.x.done.Add(int64(n))
	return n, err
}

// scpDown copies a remote file into a local folder with SCP.
func (r *remoteFiles) scpDown(x *fileXfer) error {
	return r.scp(x, "-f", x.src, func(in io.Writer, out *bufio.Reader) error {
		return scpReceive(in, out, func(name string, size int64, body io.Reader) error {
			x.size.Store(size)
			at, ok, err := r.place(x, filepath.Join(x.dst, filepath.Base(name)), localInfo, remoteEntry{name: name, size: size}, x.dst)
			if err != nil || !ok {
				if err == nil {
					_, err = io.Copy(io.Discard, body)
				}
				return err
			}
			part := at + partSuffix
			f, err := os.Create(part)
			if err != nil {
				return err
			}
			_, err = io.Copy(&progress{w: f, x: x}, body)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Rename(part, at)
			}
			if err != nil {
				_ = os.Remove(part)
				return err
			}
			x.at = at
			return nil
		})
	})
}

// scpUp copies a local file into a remote folder with SCP; a folder is
// refused. A name taken there is replaced: SCP cannot look first.
func (r *remoteFiles) scpUp(x *fileXfer) error {
	f, err := os.Open(x.src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errSCPFolder
	}
	x.size.Store(st.Size())
	return r.scp(x, "-t", x.dst, func(in io.Writer, out *bufio.Reader) error {
		return scpSend(in, out, x.name, st.Size(), &progressReader{r: f, x: x})
	})
}

// scp runs "scp -f path" or "scp -t path" on the server and speaks its
// protocol with fn.
func (r *remoteFiles) scp(x *fileXfer, mode, p string, fn func(in io.Writer, out *bufio.Reader) error) error {
	sess, err := r.conn.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	stop := context.AfterFunc(x.ctx, func() { _ = sess.Close() })
	defer stop()
	in, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	if err := sess.Start("scp " + mode + " " + scpQuote(p, r.windows)); err != nil {
		return err
	}
	err = fn(in, bufio.NewReader(out))
	_ = in.Close()
	if werr := sess.Wait(); err == nil && werr != nil {
		var exit *ssh.ExitError
		if !errors.As(werr, &exit) || exit.ExitStatus() != 0 {
			err = werr
		}
	}
	return err
}

// scpQuote quotes a path for the server's shell: double quotes for
// Windows', single quotes elsewhere, a leading ~/ left outside for the
// shell to expand.
func scpQuote(p string, windows bool) string {
	if windows {
		return `"` + p + `"`
	}
	lead := ""
	if strings.HasPrefix(p, "~/") {
		lead, p = "~/", p[2:]
	} else if p == "~" {
		return p
	}
	return lead + "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// scpReceive is the receiving side of SCP's protocol for one file (the
// server ran "scp -f"): got takes its name, size and contents.
func scpReceive(in io.Writer, out *bufio.Reader, got func(name string, size int64, body io.Reader) error) error {
	if _, err := in.Write([]byte{0}); err != nil {
		return err
	}
	for {
		line, err := out.ReadString('\n')
		if err != nil {
			return err
		}
		switch line[0] {
		case 1, 2:
			return scpRefused(line[1:])
		case 'T': // times, which "scp -f" without -p does not send
			if _, err := in.Write([]byte{0}); err != nil {
				return err
			}
			continue
		case 'D':
			return errSCPFolder
		case 'C':
		default:
			return fmt.Errorf("scp: unexpected %q", line)
		}
		f := strings.SplitN(strings.TrimSuffix(line[1:], "\n"), " ", 3)
		if len(f) != 3 {
			return fmt.Errorf("scp: unexpected %q", line)
		}
		size, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("scp: unexpected %q", line)
		}
		if _, err := in.Write([]byte{0}); err != nil {
			return err
		}
		body := io.LimitReader(out, size)
		if err := got(f[2], size, body); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return err
		}
		if err := scpAck(out); err != nil {
			return err
		}
		_, err = in.Write([]byte{0})
		return err
	}
}

// scpSend is the sending side of SCP's protocol for one file (the server
// ran "scp -t").
func scpSend(in io.Writer, out *bufio.Reader, name string, size int64, body io.Reader) error {
	if err := scpAck(out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(in, "C0644 %d %s\n", size, name); err != nil {
		return err
	}
	if err := scpAck(out); err != nil {
		return err
	}
	if n, err := io.CopyN(in, body, size); err != nil {
		if n < size && errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if _, err := in.Write([]byte{0}); err != nil {
		return err
	}
	return scpAck(out)
}

// scpAck reads SCP's answer: 0, or a refusal with its reason.
func scpAck(out *bufio.Reader) error {
	b, err := out.ReadByte()
	if err != nil {
		return err
	}
	if b == 0 {
		return nil
	}
	line, _ := out.ReadString('\n')
	return scpRefused(line)
}

// scpRefused is a refusal SCP sent, with its reason.
func scpRefused(why string) error {
	return &scpError{strings.TrimSpace(why)}
}

// scpError is a refusal from the server's scp.
type scpError struct{ why string }

func (e *scpError) Error() string { return "scp: " + e.why }

// fileError words why a file operation or a transfer failed.
func fileError(err error) string {
	var se *sftp.StatusError
	var scp *scpError
	switch {
	case errors.Is(err, fs.ErrPermission), errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxPermissionDenied:
		return L.FilesErrDenied()
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile:
		return L.FilesErrGone()
	case errors.Is(err, fs.ErrExist):
		return L.FilesErrTaken()
	case errors.Is(err, errSCPFolder):
		return L.FilesErrSCPFolder()
	case errors.As(err, &scp):
		return L.FilesErrServer(scp.why)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, os.ErrClosed), errors.Is(err, sftp.ErrSSHFxConnectionLost):
		return L.FilesErrEnded()
	}
	return L.FilesErrFailed()
}

// downloadsDir is where downloads go: the Downloads folder in the home
// folder, made when missing.
func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, "Downloads")
	return d, os.MkdirAll(d, 0o755)
}
