package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// EngineEnv overrides where kyokai-svc is (development).
const EngineEnv = "KYOKAI_SVC"

// engineChild runs the No privileges engine as a child tied to this process:
// by a lifeline pipe on macOS, by watching our PID on Windows (a pipe cannot
// be handed to a child there). When the GUI exits, even by a crash, the
// engine stops.
type engineChild struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	lifeline *os.File // write end, held for the GUI's lifetime
	starts   []time.Time
}

// engineBinary finds kyokai-svc: $KYOKAI_SVC, the app's resources/bin, then
// next to the GUI.
func engineBinary() (string, error) {
	name := "kyokai-svc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p := os.Getenv(EngineEnv); p != "" {
		return p, nil
	}
	var dirs []string
	if res, err := mygo.App.Path(mygo.PathResources); err == nil && res != "" {
		dirs = append(dirs, filepath.Join(res, "bin"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "bin"), filepath.Dir(exe))
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); fileExists(p) {
			return p, nil
		}
	}
	return "", errors.New("kyokai-svc not found")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ensure starts the engine unless one already answers on the socket, and
// waits until it does.
// Notes: three starts within a minute are reported instead of retried.
func (e *engineChild) ensure(ctx context.Context, socket string) error {
	client := localapi.NewClient(socket)
	if _, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	recent := e.starts[:0]
	for _, t := range e.starts {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	e.starts = recent
	if len(e.starts) >= 3 {
		return errors.New("the engine keeps stopping")
	}
	bin, err := engineBinary()
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	var r, w *os.File
	if runtime.GOOS == "windows" {
		cmd = exec.Command(bin, "run", "--host", "user", "--socket", socket, "--parent-pid", strconv.Itoa(os.Getpid()))
		hideWindow(cmd)
	} else {
		if r, w, err = os.Pipe(); err != nil {
			return err
		}
		cmd = exec.Command(bin, "run", "--host", "user", "--socket", socket, "--lifeline-fd", "3")
		cmd.ExtraFiles = []*os.File{r}
	}
	if err := cmd.Start(); err != nil {
		if r != nil {
			r.Close()
			w.Close()
		}
		return fmt.Errorf("start the engine: %w", err)
	}
	if r != nil {
		r.Close()
	}
	if e.lifeline != nil {
		e.lifeline.Close()
	}
	e.cmd, e.lifeline = cmd, w
	e.starts = append(e.starts, now)
	// An engine that ends with an error (a crash among them) is noted in
	// the GUI's crash log, where its standard logger writes.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("the engine ended: %v", err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the engine did not start")
}

// stop ends the engine and waits briefly for its socket to go: the engine is
// asked to shut down and the lifeline closes (macOS). On Windows the engine
// would also notice our exit, but an update must replace its files first.
func (e *engineChild) stop(socket string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := localapi.NewClient(socket)
	_, _ = client.Do(ctx, localapi.Request{Type: localapi.RequestShutdown})
	e.mu.Lock()
	if e.lifeline != nil {
		e.lifeline.Close()
		e.lifeline = nil
	}
	cmd := e.cmd
	e.mu.Unlock()
	if cmd == nil {
		return
	}
	for ctx.Err() == nil {
		if _, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); errors.Is(err, localapi.ErrNotRunning) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// connectLoop keeps a session attached to the engine, starting the engine
// when needed, until the GUI stops.
func (a *App) connectLoop() {
	for a.ctx.Err() == nil {
		socket := a.sock()
		client := localapi.NewClient(socket)
		// The helper is a service: only the GUI's own engine is started.
		var err error
		if !a.usesHelper() {
			err = a.engine.ensure(a.ctx, socket)
		}
		if err != nil {
			a.update(func() { a.connected, a.engineErr = false, err.Error() })
		} else {
			// The engine captures as the person chose (it starts hidden, and
			// shares what the computer plays).
			a.sendCapture()
			a.sendSoundSource()
			a.sendLogs()
			attach, stop := context.WithCancel(a.ctx)
			a.mu.Lock()
			a.reattach = stop
			a.mu.Unlock()
			err := client.Attach(attach, func(st *localapi.Status) error {
				a.statusPushed(st)
				return nil
			})
			stop()
			if a.ctx.Err() != nil {
				return
			}
			if attach.Err() != nil {
				continue // switched to another engine
			}
			a.update(func() { a.connected, a.engineErr = false, fmt.Sprint(err) })
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// statusPushed takes a status the engine pushed: the state changes under
// the lock, then what they announce outside it — a notification and the
// window for a controller waiting for this user (even with the window
// hidden), a notification for an AI step waiting for approval — and the
// connections' tabs on the main thread.
// Notes: MyGo's notifications and window calls wait for the main thread,
// which takes a.mu to draw: made under the lock they froze the app for good
// (the first connection to a Mac host, where the first notification of a
// run waits for macOS to answer whether Kyokai may show them).
func (a *App) statusPushed(st *localapi.Status) {
	var asking string
	var approval bool
	a.update(func() {
		if a.status == nil || a.status.PasswordSerial != st.PasswordSerial {
			a.password, a.showPassword = "", false
		}
		if fresh := newRequests(a.status, st); len(fresh) > 0 {
			asking = fresh[0].DeviceID
		}
		approval = a.aiStatusChanged(a.status, st)
		// The shared draft as typed elsewhere, unless this user is typing.
		if (a.status == nil || a.status.AIDraft != st.AIDraft) && time.Since(a.ai.draftAt) > draftQuiet {
			a.ai.draft = st.AIDraft
		}
		a.permissionsChanged(st)
		a.helperChanged(st)
		a.recordSamples(st, time.Now())
		a.status, a.connected, a.engineErr = st, true, ""
	})
	if asking != "" {
		go a.notify("request", L.RequestTitle(groupDigits(asking)), L.RequestNotify())
		go a.Focus()
	}
	if approval {
		go a.notify("ai-approval", L.AINotifyApprovalTitle(), L.AINotifyApprovalBody())
	}
	// The connections' tabs follow the sessions (main thread).
	a.mu.Lock()
	w := a.window
	a.mu.Unlock()
	if w != nil {
		w.Update(a.syncViewers)
	}
}

// request sends one request to the engine.
func (a *App) request(t localapi.RequestType) (*localapi.Response, error) {
	return a.requestWith(localapi.Request{Type: t})
}

// requestWith sends one request with parameters to the engine.
func (a *App) requestWith(req localapi.Request) (*localapi.Response, error) {
	return localapi.NewClient(a.sock()).Do(a.ctx, req)
}

// revealPassword fetches the temporary password for the Home page or the
// clipboard; it never reaches a log or a file.
// Notes: `then` runs after the state is updated, without a.mu: it may call
// MyGo, which waits for the main thread (regression: Copy wrote the
// clipboard under the lock and could freeze the app).
func (a *App) revealPassword(then func(pw string)) {
	go func() {
		resp, err := a.request(localapi.RequestPasswordReveal)
		ok := false
		a.update(func() {
			if err != nil {
				a.setToast(L.EngineDown(), true)
				return
			}
			a.password = resp.Password
			if a.status != nil {
				a.passwordFor = a.status.PasswordSerial
			}
			ok = true
		})
		if ok && then != nil {
			then(resp.Password)
		}
	}()
}
