// Package engine is the host of the Rust core in its own process
// (`kyokai-svc run`): it starts the core, serves the local API and stops
// with its lifeline (doc/en-us/architecture.md). In No privileges mode it is
// a user-level child of the GUI and lives exactly as long as the GUI.
package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// Options configure Run.
type Options struct {
	// Version is the product version passed to the core.
	Version string
	// Socket is the local API socket path.
	Socket string
	// StateDir holds the core's secrets and identity (DefaultStateDir).
	StateDir string
	// Profile separates a development instance (KYOKAI_PROFILE).
	Profile string
	// CoreLib is the absolute path of the core library.
	CoreLib string
	// ParentPID, when non-zero, stops the engine once that process is gone
	// (Windows, where a pipe cannot be handed to a child).
	ParentPID int
	// Lifeline, when set, stops the engine at its EOF (macOS): the GUI holds
	// the write end and it closes when the GUI exits, even by a crash.
	Lifeline *os.File
	// Logf receives diagnostics; nil discards them. Never a secret.
	Logf func(format string, args ...any)
}

// DefaultStateDir is %APPDATA%\Kyokai on Windows and
// ~/Library/Application Support/Kyokai on macOS; a profile gets its own
// "Kyokai-<profile>" folder.
func DefaultStateDir(profile string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	name := "Kyokai"
	if profile != "" {
		name += "-" + profile
	}
	return filepath.Join(base, name), nil
}

// Run starts the core and serves the local API until ctx ends, the
// lifeline or parent goes away, or a client asks for shutdown.
// Side effects: loads the core library, creates the state directory and
// the socket; stops the core (dropping its in-memory secrets) on return.
func Run(ctx context.Context, opts Options) error {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// The socket first: a second engine stops here without touching the
	// identity.
	ln, err := localapi.Listen(opts.Socket)
	if err != nil {
		return err
	}
	defer os.Remove(opts.Socket)
	lib, err := core.Load(opts.CoreLib)
	if err != nil {
		ln.Close()
		return err
	}
	st, err := lib.Start(core.Config{Version: opts.Version, StateDir: opts.StateDir, Profile: opts.Profile})
	if err != nil {
		ln.Close()
		return fmt.Errorf("start core: %w", err)
	}
	defer lib.Stop()
	logf("engine %s started: device %s, secrets %s", opts.Version, st.DeviceID, st.Persistence)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	switch {
	case opts.Lifeline != nil:
		go func() {
			_, _ = io.Copy(io.Discard, opts.Lifeline)
			logf("lifeline closed: stopping")
			cancel()
		}()
	case opts.ParentPID != 0:
		go func() {
			watchParent(ctx, opts.ParentPID)
			if ctx.Err() == nil {
				logf("parent %d is gone: stopping", opts.ParentPID)
				cancel()
			}
		}()
	}

	h := &handler{lib: lib, stop: cancel, started: time.Now(), sessions: map[chan struct{}]bool{}}
	return localapi.Serve(ctx, ln, h)
}

// handler answers the local API from the core.
type handler struct {
	lib     *core.Lib
	stop    context.CancelFunc
	started time.Time

	mu       sync.Mutex
	sessions map[chan struct{}]bool // each is poked when the status changes
}

func (h *handler) Handle(_ context.Context, req localapi.Request) localapi.Response {
	switch req.Type {
	case localapi.RequestStatus:
		st, err := h.status()
		if err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Status: st}
	case localapi.RequestPasswordReveal:
		var pw struct{ Temporary string }
		if err := h.lib.Call("password.reveal", nil, &pw); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Password: pw.Temporary}
	case localapi.RequestPasswordRegenerate:
		if err := h.lib.Call("password.regenerate", nil, nil); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true}
	case localapi.RequestShutdown:
		h.stop()
		return localapi.Response{OK: true}
	}
	return localapi.Fail(localapi.CodeUnknown, "unknown request %q", req.Type)
}

// Stream pushes the status now and after every change until the client
// leaves.
func (h *handler) Stream(ctx context.Context, _ localapi.Request, send func(localapi.Response) error) {
	poke := make(chan struct{}, 1)
	h.mu.Lock()
	h.sessions[poke] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, poke)
		h.mu.Unlock()
	}()
	for {
		st, err := h.status()
		if err != nil || send(localapi.Response{OK: true, Status: st}) != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-poke:
		}
	}
}

func (h *handler) changed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for poke := range h.sessions {
		select {
		case poke <- struct{}{}:
		default:
		}
	}
}

func (h *handler) status() (*localapi.Status, error) {
	var st core.Status
	if err := h.lib.Call("status", nil, &st); err != nil {
		return nil, err
	}
	return &localapi.Status{
		Version: st.Version, ABI: st.ABI, DeviceID: st.DeviceID, Fingerprint: st.Fingerprint,
		StateDir: st.StateDir, RunMode: "user", Persistence: st.Persistence,
		PasswordSerial: st.PasswordSerial, Started: h.started,
	}, nil
}

// Logger returns a Logf writing to path (appending), or to stderr when the
// file cannot be opened.
func Logger(path string) func(string, ...any) {
	var w io.Writer = os.Stderr
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			w = f
		}
	}
	l := log.New(w, "", log.LstdFlags)
	return l.Printf
}
