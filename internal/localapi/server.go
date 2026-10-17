package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// maxRequestSize bounds one request line; snapshots are responses, so
// requests stay small.
const maxRequestSize = 1 << 20

// Handler answers requests. Handle runs on the connection's goroutine and
// may block; implementations must be safe for concurrent calls.
type Handler interface {
	Handle(ctx context.Context, req Request) Response
	// Stream serves a session (RequestSessionAttach): it sends frames until
	// ctx ends (the client closed the connection or the server stops) and
	// then returns. send fails once the client is gone.
	Stream(ctx context.Context, req Request, send func(Response) error)
	// Media serves a media stream (RequestMedia). It checks the request and
	// returns a refusal, or calls open, which answers OK and hands over the
	// connection for packets, and returns when the stream ends.
	Media(ctx context.Context, req Request, open func() io.ReadWriter) Response
}

// maxSocketPath is the longest socket path the kernel accepts: sun_path
// less its terminating NUL, 104 bytes on macOS and 108 elsewhere.
var maxSocketPath = func() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}()

// ListenOptions configures Listen.
type ListenOptions struct {
	// Path is the socket path.
	Path string
	// Mode is the socket's file mode. A per-user socket is 0600; a system
	// socket is 0660 so its group can connect, or 0666 when other local
	// users may read.
	Mode os.FileMode
	// DirMode is the socket directory's mode when Group is set: 0750 for a
	// group-only socket, 0755 when other users may reach it.
	DirMode os.FileMode
	// Group, when non-zero, chowns the socket directory and socket to
	// that gid (a system socket's authorized group). Requires privilege.
	Group int
	// Authorizer decides each connection's tier.
	Authorizer Authorizer
}

// ListenWith creates the daemon socket with explicit ownership and mode,
// replacing a stale one but refusing to take over from a live daemon.
// A zero Mode means 0600 (the per-user default).
// Notes: a path longer than the kernel's limit is refused with a readable
// error instead of bind's EINVAL.
func ListenWith(o ListenOptions) (net.Listener, error) {
	path := o.Path
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("socket path is %d bytes, longer than the %d this system allows: %s", len(path), maxSocketPath, path)
	}
	mode := o.Mode
	if mode == 0 {
		mode = 0o600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// A system socket lets its group traverse the directory; a per-user
	// socket stays owner-only. DirMode widens it to others when they may
	// read.
	// A socket other users may reach needs a directory they can enter.
	if mode&0o006 != 0 && o.Group == 0 && runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o755); err != nil {
			return nil, fmt.Errorf("chmod socket directory: %w", err)
		}
	}
	if o.Group != 0 && runtime.GOOS != "windows" {
		if err := os.Chown(dir, -1, o.Group); err != nil {
			return nil, fmt.Errorf("chown socket directory: %w", err)
		}
		dirMode := o.DirMode
		if dirMode == 0 {
			dirMode = 0o750
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			return nil, fmt.Errorf("chmod socket directory: %w", err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		// A socket file exists: is something listening?
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("the engine is already running on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := secureSocket(path, mode, o.Group); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// ServeWith accepts connections until ctx is cancelled, answering each
// request on its own line. Access is decided once per connection from the
// kernel-reported peer identity (see Authorizer); connections whose
// identity cannot be read, or that belong to another user, are closed
// without a reply.
//
// authz decides who is served: the per-user engine's own user, or the
// helper's operator and group.
func ServeWith(ctx context.Context, ln net.Listener, h Handler, authz Authorizer) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		connections.Add(1)
		go func() { defer connections.Done(); serveConn(ctx, conn, h, authz) }()
	}
}

func serveConn(ctx context.Context, conn net.Conn, h Handler, authz Authorizer) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			// Interrupt idle reads without racing the final response from an
			// active shutdown handler. The handler receives ctx cancellation;
			// its response is written before serveConn closes the connection.
			_ = conn.SetReadDeadline(time.Now())
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		case <-done:
		}
	}()
	peer, err := peerCredentials(conn)
	access := err == nil && authz.Allows(peer)
	if !access {
		// No identity, or not authorized: answer nothing at all.
		return
	}
	reader := bufio.NewReaderSize(conn, maxRequestSize)
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// ReadSlice never allocates beyond the request limit. Do not echo
			// attacker-controlled input (it can also contain config secrets).
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = conn.Write([]byte(fmt.Sprintf("{\"version\":%d,\"ok\":false,\"error\":\"request exceeds one MiB limit\"}\n", ProtocolVersion)))
			return
		}
		if err != nil {
			if err != io.EOF {
				// The peer went away mid-line; nothing to answer.
				_ = err
			}
			return
		}
		var req Request
		var resp Response
		if err := json.Unmarshal(line, &req); err != nil {
			resp = Response{Version: ProtocolVersion, OK: false, Error: "bad request: " + err.Error()}
		} else if req.Version != ProtocolVersion {
			resp = Response{Version: ProtocolVersion, OK: false,
				Error: fmt.Sprintf("protocol version mismatch: request %d, daemon %d", req.Version, ProtocolVersion)}
		} else if req.Type == RequestSessionAttach {
			stream(ctx, conn, reader, h, req)
			return
		} else if req.Type == RequestMedia {
			media(ctx, conn, reader, h, req)
			return
		} else {
			resp = h.Handle(ctx, req)
		}
		if resp.Version == 0 {
			resp.Version = ProtocolVersion
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(append(payload, '\n')); err != nil {
			return
		}
	}
}

// stream runs a session on conn: the handler's frames are written as
// lines, and the session ends when the client closes its side (detected by
// a read returning) or the server stops.
func stream(ctx context.Context, conn net.Conn, reader *bufio.Reader, h Handler, req Request) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// A session client sends nothing more; any read result (EOF, an
		// error, stray data) ends the session.
		_ = conn.SetReadDeadline(time.Time{})
		_, _ = reader.ReadByte()
		cancel()
	}()
	var mu sync.Mutex
	send := func(resp Response) error {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp.Version = ProtocolVersion
		payload, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(append(payload, '\n')); err != nil {
			cancel()
			return err
		}
		return nil
	}
	h.Stream(ctx, req, send)
}

// media runs a media stream on conn: the handler either refuses (the
// answer is written as usual) or opens it, after which the connection
// carries packets in both directions until either side closes it.
func media(ctx context.Context, conn net.Conn, reader *bufio.Reader, h Handler, req Request) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	write := func(resp Response) bool {
		resp.Version = ProtocolVersion
		payload, err := json.Marshal(resp)
		if err != nil {
			return false
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write(append(payload, '\n'))
		return err == nil
	}
	opened := false
	resp := h.Media(ctx, req, func() io.ReadWriter {
		opened = true
		write(Response{OK: true})
		_ = conn.SetDeadline(time.Time{})
		// Packets the client sent are read through the request reader,
		// which may already hold some of them.
		return struct {
			io.Reader
			io.Writer
		}{reader, conn}
	})
	if !opened {
		write(resp)
	}
}
