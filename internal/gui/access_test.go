package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/rfb"
)

// okEngine answers every request with OK.
type okEngine struct{}

func (okEngine) Handle(context.Context, localapi.Request) localapi.Response {
	return localapi.Response{Version: localapi.ProtocolVersion, OK: true}
}
func (okEngine) Stream(context.Context, localapi.Request, func(localapi.Response) error) {}
func (okEngine) Media(context.Context, localapi.Request, func() io.ReadWriter) localapi.Response {
	return localapi.Response{}
}

// A saved access setting with no follow-up must not crash the GUI
// (choosing "Permanent" under Accepted passwords once did).
func TestAccessCommandWithoutFollowUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go localapi.ServeWith(ctx, ln, okEngine{}, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})

	a := New("", t.TempDir(), path)
	a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, PasswordMode: "permanent"}, nil)
	for deadline := time.Now().Add(5 * time.Second); ; {
		a.mu.Lock()
		busy, toast := a.busy["access"], a.toast
		a.mu.Unlock()
		if !busy {
			if toast != "" {
				t.Fatalf("the request failed: %s", toast)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// revealEngine answers every request with OK and a temporary password.
type revealEngine struct{ okEngine }

func (revealEngine) Handle(context.Context, localapi.Request) localapi.Response {
	return localapi.Response{Version: localapi.ProtocolVersion, OK: true, Password: "k7mq2xpa"}
}

// TestNothingWaitsForTheMainThreadUnderTheLock: notifications and the
// clipboard, which wait for the main thread, are never reached holding
// a.mu, which the main thread takes to draw (regression: a connection
// request and an AI step waiting for approval were announced under the
// lock, and Copy wrote the clipboard under it; the app froze, as on the
// first connection to a Mac).
func TestNothingWaitsForTheMainThreadUnderTheLock(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go localapi.ServeWith(ctx, ln, revealEngine{}, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})

	a := New("", t.TempDir(), path)
	called := make(chan string, 4)
	// Stands in for the main thread, which the real call waits for: it
	// takes the lock to draw.
	main := func(what string) {
		took := make(chan struct{})
		go func() {
			a.mu.Lock()
			a.mu.Unlock()
			close(took)
		}()
		select {
		case <-took:
		case <-time.After(2 * time.Second):
			t.Errorf("%s waited for the main thread holding the app lock", what)
		}
		called <- what
	}
	notify, write := notifier, clipboardWrite
	t.Cleanup(func() { notifier, clipboardWrite = notify, write })
	notifier = func(id, _, _ string) { main(id) }
	clipboardWrite = func(string) { main("clipboard") }

	a.statusPushed(&localapi.Status{
		Requests: []localapi.HostRequest{{ID: 1, DeviceID: "482916735"}},
		AI:       &localapi.AIActive{ID: 3, State: "approval"},
	})
	a.revealPassword(func(p string) { clipboardWrite(p) })
	want := map[string]bool{"request": true, "ai-approval": true, "clipboard": true}
	for len(want) > 0 {
		select {
		case w := <-called:
			delete(want, w)
		case <-time.After(5 * time.Second):
			t.Fatalf("never called: %v", want)
		}
	}
}

// recordEngine answers every request with OK and keeps the requests.
type recordEngine struct {
	okEngine
	mu   sync.Mutex
	seen []localapi.Request
}

func (e *recordEngine) Handle(_ context.Context, req localapi.Request) localapi.Response {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, req)
	return localapi.Response{Version: localapi.ProtocolVersion, OK: true}
}

// last is the last request of a type the engine was sent.
func (e *recordEngine) last(typ localapi.RequestType) (localapi.Request, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.seen) - 1; i >= 0; i-- {
		if e.seen[i].Type == typ {
			return e.seen[i], true
		}
	}
	return localapi.Request{}, false
}

// TestKyokaiInputFollowsHideFromCapture: a person's remote input may reach
// Kyokai's windows exactly when they show in capture (Hide Kyokai from
// capture off), and the GUI tells the engine both together.
func TestKyokaiInputFollowsHideFromCapture(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &recordEngine{}
	go localapi.ServeWith(ctx, ln, engine, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})

	a := New("", t.TempDir(), path)
	for _, show := range []bool{true, false} {
		a.mu.Lock()
		a.prefs.ShowInCapture = show
		a.mu.Unlock()
		a.sendCapture()
		input, ok := engine.last(localapi.RequestInputKyokai)
		if !ok || input.KyokaiInput == nil || *input.KyokaiInput != show {
			t.Errorf("shown %v: input.kyokai %+v", show, input.KyokaiInput)
		}
		capture, ok := engine.last(localapi.RequestCaptureShow)
		if !ok || capture.Show == nil || *capture.Show != show {
			t.Errorf("shown %v: capture.show %+v", show, capture.Show)
		}
	}
}

// parkedEngine streams a parked status until wake closes, then an ordinary
// one, as a helper whose core starts only later does.
type parkedEngine struct {
	*recordEngine
	wake chan struct{}
}

func (e parkedEngine) Stream(ctx context.Context, _ localapi.Request, send func(localapi.Response) error) {
	_ = send(localapi.Response{Version: localapi.ProtocolVersion, OK: true, Status: &localapi.Status{Moved: "user"}})
	select {
	case <-e.wake:
	case <-ctx.Done():
		return
	}
	_ = send(localapi.Response{Version: localapi.ProtocolVersion, OK: true, Status: &localapi.Status{}})
	<-ctx.Done()
}

// TestSettingsReachAnEngineOnceItRuns: the GUI's settings go to the engine
// once its core runs, not before (regression: sent before attaching, they
// were lost by a helper that was starting or parked, and Kyokai's windows
// took no remote input with Hide Kyokai from capture off).
func TestSettingsReachAnEngineOnceItRuns(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := parkedEngine{recordEngine: &recordEngine{}, wake: make(chan struct{})}
	go localapi.ServeWith(ctx, ln, engine, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})

	a := New("", t.TempDir(), path)
	a.mu.Lock()
	a.prefs.ShowInCapture = true
	a.mu.Unlock()
	go func() { _ = localapi.NewClient(path).Attach(ctx, a.attachedStatus(ctx)) }()
	time.Sleep(300 * time.Millisecond)
	if _, ok := engine.last(localapi.RequestInputKyokai); ok {
		t.Fatal("settings sent to a parked engine")
	}
	close(engine.wake)
	for deadline := time.Now().Add(5 * time.Second); ; {
		input, ok := engine.last(localapi.RequestInputKyokai)
		if ok && input.KyokaiInput != nil && *input.KyokaiInput {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input.kyokai never reached the engine once it ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, typ := range []localapi.RequestType{localapi.RequestCaptureShow, localapi.RequestSoundSource, localapi.RequestLogs} {
		if _, ok := engine.last(typ); !ok {
			t.Errorf("%s never sent", typ)
		}
	}
}

// TestReached: a refusal came from the engine; a lost request did not.
func TestReached(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{nil, true},
		{&localapi.RequestError{Message: "unknown request"}, true},
		{fmt.Errorf("wrapped: %w", &localapi.RequestError{}), true},
		{localapi.ErrNotRunning, false},
		{context.DeadlineExceeded, false},
	} {
		if got := reached(c.err); got != c.want {
			t.Errorf("reached(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestVNCSignInError: a VNC server that needs a sign-in the tab did not
// have asks for one; a refused or dropped sign-in is a wrong one
// (regression: a Mac that dropped the blank attempt showed "could not be
// opened" and never the form).
func TestVNCSignInError(t *testing.T) {
	other := errors.New("broken pipe")
	for _, c := range []struct {
		err   error
		blank bool
		want  error
	}{
		{fmt.Errorf("x: %w", rfb.ErrUnsupportedServer), true, errNeedSignIn},
		{fmt.Errorf("x: %w", rfb.ErrAuthenticationFailed), true, errNeedSignIn},
		{fmt.Errorf("x: %w", rfb.ErrUnsupportedServer), false, rfb.ErrUnsupportedServer},
		{fmt.Errorf("x: %w", rfb.ErrAuthenticationFailed), false, errSignInFailed},
		{fmt.Errorf("x: %w", rfb.ErrPermissionDenied), false, errSignInFailed},
		{fmt.Errorf("x: %w", rfb.ErrSignInDropped), false, errSignInFailed},
		{other, false, other},
	} {
		if got := vncSignInError(c.err, c.blank); !errors.Is(got, c.want) {
			t.Errorf("vncSignInError(%v, %v) = %v, want %v", c.err, c.blank, got, c.want)
		}
	}
}
