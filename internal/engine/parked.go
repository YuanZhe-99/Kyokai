package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// movedMarker is the file a moved-out store leaves in its state folder
// (the core's backup::MOVED_MARKER).
const movedMarker = "moved.json"

// movedTo reads where this state folder's store went in a run-mode switch.
// Returns: "helper" or "user", or "" when the store was never moved out.
func movedTo(stateDir string) string {
	var m struct {
		To string `json:"to"`
	}
	if ok, err := fsutil.ReadJSON(filepath.Join(stateDir, movedMarker), &m); !ok || err != nil {
		return ""
	}
	if m.To == "helper" || m.To == "user" {
		return m.To
	}
	return ""
}

// parked is the engine of a state folder whose store moved to the other
// run mode: it starts no core, so no identity of its own appears, and
// serves only what lets the GUI see that and bring a store back, until a
// store is imported or the person starts over; from then on it is the
// ordinary engine.
type parked struct {
	moved, runMode, stateDir, version string
	started                           time.Time
	stop                              context.CancelFunc
	// start starts the core and makes the ordinary handler.
	start func() (*handler, error)

	mu    sync.Mutex
	inner *handler
	woke  chan struct{}
}

// awake starts the core, once, and returns the ordinary handler.
// Side effects: removes the marker first when fresh (starting over).
func (p *parked) awake(fresh bool) (*handler, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inner != nil {
		return p.inner, nil
	}
	if fresh {
		if err := os.Remove(filepath.Join(p.stateDir, movedMarker)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	h, err := p.start()
	if err != nil {
		return nil, err
	}
	p.inner = h
	if p.woke != nil {
		close(p.woke)
	}
	return h, nil
}

// current is the ordinary handler once the core runs, else nil.
func (p *parked) current() *handler {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inner
}

// status is what a parked engine reports: no identity, and where its store
// went.
func (p *parked) status() *localapi.Status {
	return &localapi.Status{Version: p.version, Build: version.Build(), StateDir: p.stateDir, RunMode: p.runMode,
		Moved: p.moved, Persistence: "os-store", Started: p.started}
}

// Handle serves a parked engine's requests, or the ordinary handler's
// once the core runs.
func (p *parked) Handle(ctx context.Context, req localapi.Request) localapi.Response {
	if h := p.current(); h != nil {
		return h.Handle(ctx, req)
	}
	switch req.Type {
	case localapi.RequestStatus:
		return localapi.Response{OK: true, Status: p.status()}
	case localapi.RequestShutdown:
		p.stop()
		return localapi.Response{OK: true}
	case localapi.RequestStoreImport:
		// The core starts (on what is left in the store: no history), then
		// takes the store in; the marker goes only with a successful import.
		h, err := p.awake(false)
		if err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		resp := h.Handle(ctx, req)
		if !resp.OK {
			// Still moved out: serve nothing until the next start finds the
			// marker again.
			p.stop()
		}
		return resp
	case localapi.RequestStoreForgetMove:
		if _, err := p.awake(true); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true}
	case localapi.RequestCaptureShow, localapi.RequestInputKyokai, localapi.RequestKyokaiShown, localapi.RequestSoundSource, localapi.RequestLogs:
		// What a GUI sends as it attaches: kept by the GUI and sent again
		// once the core runs.
		return localapi.Response{OK: true}
	}
	return localapi.Fail(localapi.CodeInvalid, "this device's identity is with the %s engine", p.moved)
}

// Stream sends the parked status until the core runs, then serves the
// ordinary session.
func (p *parked) Stream(ctx context.Context, req localapi.Request, send func(localapi.Response) error) {
	p.mu.Lock()
	if p.woke == nil {
		p.woke = make(chan struct{})
	}
	woke := p.woke
	p.mu.Unlock()
	if p.current() == nil {
		if send(localapi.Response{OK: true, Status: p.status()}) != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-woke:
		}
	}
	p.current().Stream(ctx, req, send)
}

// Media serves media once the core runs; a parked engine has none.
func (p *parked) Media(ctx context.Context, req localapi.Request, open func() io.ReadWriter) localapi.Response {
	if h := p.current(); h != nil {
		return h.Media(ctx, req, open)
	}
	return localapi.Fail(localapi.CodeInvalid, "this device's identity is with the %s engine", p.moved)
}
