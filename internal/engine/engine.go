// Package engine is the host of the Rust core in its own process
// (`kyokai-svc run`): it starts the core, serves the local API and stops
// with its lifeline (doc/en-us/architecture.md). In No privileges mode it is
// a user-level child of the GUI and lives exactly as long as the GUI.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// statusPoll is how often a session's status is re-read for network
// changes, which the core does not announce.
const statusPoll = 2 * time.Second

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
	// Helper, when set, runs the engine as the privileged helper: its
	// socket is open to every local user and this authorizer decides who
	// is served (the operator and the kyokai group).
	Helper *localapi.Authorizer
	// Update installs the signed bundle in a folder into the helper and
	// restarts it (helper.update); nil outside the helper.
	Update func(from string) error
	// Remove removes the helper itself, its identity included, once the
	// store has moved out (helper.remove); nil where removing needs the
	// administrator (Windows) and outside the helper.
	Remove func() error
	// AgentSocket is where the helper's session agent connects (macOS,
	// where launchd starts the agent); empty elsewhere.
	AgentSocket string
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
	authz := localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()}
	listen := localapi.ListenOptions{Path: opts.Socket, Mode: 0o600}
	runMode := "user"
	if opts.Helper != nil {
		authz, listen.Mode, runMode = *opts.Helper, 0o666, "helper"
	}
	ln, err := localapi.ListenWith(listen)
	if err != nil {
		return err
	}
	// Closing the listener removes its own socket file, never a newer one.
	defer ln.Close()
	lib, err := core.Load(opts.CoreLib)
	if err != nil {
		ln.Close()
		return err
	}
	cfg := core.Config{Version: opts.Version, StateDir: opts.StateDir, Profile: opts.Profile}
	if opts.Helper != nil {
		// The helper starts its own copy of kyokai-svc as the session agent
		// (Windows), or its LaunchAgent connects to a socket (macOS).
		exe, err := os.Executable()
		if err != nil {
			ln.Close()
			return err
		}
		cfg.Agent = exe
		if opts.AgentSocket != "" {
			cfg.Agent = opts.AgentSocket
		}
	}
	st, err := lib.Start(cfg)
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

	h := &handler{lib: lib, stop: cancel, started: time.Now(), sessions: map[chan struct{}]bool{}, runMode: runMode, stateDir: opts.StateDir, update: opts.Update, remove: opts.Remove}
	if runMode == "helper" {
		var m struct {
			Background bool `json:"background"`
		}
		_, _ = fsutil.ReadJSON(filepath.Join(opts.StateDir, "mode.json"), &m)
		h.background = m.Background
	}
	h.updateServe()
	_, _ = h.status() // reads protected mode
	return localapi.ServeWith(ctx, ln, h, authz)
}

// protectedAllows are the requests served in protected mode: the status,
// the AI page, what keeps the host user in charge of sessions (confirm,
// disconnect, stop the AI), what the GUI sends on its own as it attaches,
// quitting, and turning the mode off. Everything else is refused, ctl
// included.
var protectedAllows = map[localapi.RequestType]bool{
	localapi.RequestStatus: true, localapi.RequestProtect: true, localapi.RequestShutdown: true,
	localapi.RequestDecide: true, localapi.RequestClose: true,
	localapi.RequestCaptureShow: true, localapi.RequestSoundSource: true, localapi.RequestLogs: true,
	localapi.RequestPermission: true, localapi.RequestAgentRestart: true, localapi.RequestHelperUpdate: true,
	localapi.RequestAISettingsGet: true, localapi.RequestAISettingsSet: true, localapi.RequestAIKeySet: true,
	localapi.RequestAIModels: true, localapi.RequestAITaskStart: true, localapi.RequestAIDraft: true,
	localapi.RequestAITaskReply: true, localapi.RequestAITaskPause: true, localapi.RequestAITaskResume: true,
	localapi.RequestAITaskCancel: true, localapi.RequestAITaskDecide: true, localapi.RequestAITasks: true,
	localapi.RequestAIHistoryClear: true, localapi.RequestAIStop: true,
}

// handler answers the local API from the core.
type handler struct {
	lib     *core.Lib
	stop    context.CancelFunc
	started time.Time
	// runMode is "user" or "helper", for the status.
	runMode string
	// stateDir keeps the helper's mode (mode.json).
	stateDir string
	// background: the helper serves from boot, not only with a GUI.
	background bool
	// served is what the core was last told to serve.
	served string
	// update is Options.Update.
	update func(from string) error
	// remove removes the helper itself (macOS); nil elsewhere.
	remove func() error

	// protected: protected mode is on (protectedAllows), as the core's
	// status said last.
	protected atomic.Bool

	mu       sync.Mutex
	sessions map[chan struct{}]bool // each is poked when the status changes
}

func (h *handler) Handle(_ context.Context, req localapi.Request) localapi.Response {
	if h.protected.Load() && !protectedAllows[req.Type] {
		return localapi.Fail(localapi.CodeInvalid, "protected mode is on: turn it off with the permanent password first")
	}
	switch req.Type {
	case localapi.RequestProtect:
		if req.Protect == nil {
			return localapi.Fail(localapi.CodeInvalid, "protected-mode choice missing")
		}
		resp := h.command("protect.set", map[string]any{"on": req.Protect.On, "password": req.Protect.Password})
		if resp.OK {
			h.protected.Store(req.Protect.On)
		}
		return resp
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
	case localapi.RequestNetworkGet:
		var ns localapi.NetworkSettings
		if err := h.lib.Call("network.get", nil, &ns); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Network: &ns}
	case localapi.RequestNetworkSet:
		if req.Network == nil {
			return localapi.Fail(localapi.CodeInvalid, "network settings missing")
		}
		var ns localapi.NetworkSettings
		if err := h.lib.Call("network.set", req.Network, &ns); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true, Network: &ns}
	case localapi.RequestNameSet:
		if req.Name == nil {
			return localapi.Fail(localapi.CodeInvalid, "name missing")
		}
		return h.command("device.set_name", map[string]string{"name": *req.Name})
	case localapi.RequestAgentRestart:
		return h.command("agent.restart", nil)
	case localapi.RequestStartOver:
		return h.command("store.start_over", nil)
	case localapi.RequestAccessSettings:
		if req.PasswordMode == "" && req.WithoutAsking == nil && req.ByAddress == nil {
			return localapi.Fail(localapi.CodeInvalid, "nothing to change")
		}
		params := map[string]any{}
		if req.PasswordMode != "" {
			params["password_mode"] = req.PasswordMode
		}
		if req.WithoutAsking != nil {
			params["without_asking"] = *req.WithoutAsking
		}
		if req.ByAddress != nil {
			ranges := req.ByAddress.Ranges
			if ranges == nil {
				ranges = []string{}
			}
			params["by_address"] = map[string]any{"on": req.ByAddress.On, "ranges": ranges}
		}
		return h.command("access.settings", params)
	case localapi.RequestPermission:
		if req.Permission == "" {
			return localapi.Fail(localapi.CodeInvalid, "permission missing")
		}
		return h.command("permission.request", map[string]string{"name": req.Permission})
	case localapi.RequestLogs:
		if req.LogsOff == nil {
			return localapi.Fail(localapi.CodeInvalid, "logs choice missing")
		}
		if err := SetLogsOff(h.stateDir, *req.LogsOff); err != nil {
			return localapi.Fail(localapi.CodeInternal, "logs: %v", err)
		}
		return h.command("access.history", map[string]bool{"keep": !*req.LogsOff})
	case localapi.RequestCaptureShow:
		if req.Show == nil {
			return localapi.Fail(localapi.CodeInvalid, "show missing")
		}
		return h.command("capture.show", map[string]bool{"show": *req.Show})
	case localapi.RequestSoundSource:
		if req.SoundSource == nil {
			return localapi.Fail(localapi.CodeInvalid, "source missing")
		}
		return h.command("sound.source", map[string]string{"source": *req.SoundSource})
	case localapi.RequestSoundDevices:
		var devices []localapi.SoundDevice
		if err := h.lib.Call("sound.devices", nil, &devices); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, SoundDevices: devices}
	case localapi.RequestPasswordGrants:
		if req.Grants == nil {
			return localapi.Fail(localapi.CodeInvalid, "permissions missing")
		}
		return h.command("access.password_grants", map[string][]string{"grants": append([]string{}, *req.Grants...)})
	case localapi.RequestBackground:
		if h.runMode != "helper" || req.Background == nil {
			return localapi.Fail(localapi.CodeInvalid, "only the helper has a background mode")
		}
		if err := fsutil.WriteJSON(filepath.Join(h.stateDir, "mode.json"), map[string]bool{"background": *req.Background}); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		h.mu.Lock()
		h.background = *req.Background
		h.mu.Unlock()
		h.updateServe()
		h.changed()
		return localapi.Response{OK: true}
	case localapi.RequestPermanentSet:
		if req.Permanent == nil {
			return localapi.Fail(localapi.CodeInvalid, "permanent password missing")
		}
		return h.command("password.set_permanent", req.Permanent)
	case localapi.RequestConnect:
		if req.Connect == nil {
			return localapi.Fail(localapi.CodeInvalid, "connect request missing")
		}
		var out struct{ ID uint64 }
		if err := h.lib.Call("session.connect", req.Connect, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true, ID: out.ID}
	case localapi.RequestIdentify:
		if req.Address == "" {
			return localapi.Fail(localapi.CodeInvalid, "address missing")
		}
		var out struct {
			DeviceID string `json:"device_id"`
			Failure  string `json:"failure"`
		}
		if err := h.lib.Call("session.identify", map[string]string{"address": req.Address}, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, DeviceID: out.DeviceID, Failure: out.Failure}
	case localapi.RequestDeviceRemove:
		if req.DeviceID == "" {
			return localapi.Fail(localapi.CodeInvalid, "device ID missing")
		}
		resp := h.command("device.remove", map[string]string{"device_id": req.DeviceID})
		h.changed()
		return resp
	case localapi.RequestClose:
		return h.command("session.close", map[string]uint64{"id": req.ID})
	case localapi.RequestForgetPassword:
		if req.DeviceID == "" {
			return localapi.Fail(localapi.CodeInvalid, "device ID missing")
		}
		return h.command("password.forget_saved", map[string]string{"device_id": req.DeviceID})
	case localapi.RequestDecide:
		if req.Decide == nil {
			return localapi.Fail(localapi.CodeInvalid, "decision missing")
		}
		return h.command("request.decide", req.Decide)
	case localapi.RequestTrustedList:
		var list []localapi.TrustedDevice
		if err := h.lib.Call("trusted.list", nil, &list); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Trusted: list}
	case localapi.RequestTrustedRemove:
		return h.command("trusted.remove", map[string]string{"key": req.Key})
	case localapi.RequestIdentityExport:
		if req.Backup == nil {
			return localapi.Fail(localapi.CodeInvalid, "passphrase missing")
		}
		var out struct{ Document string }
		if err := h.lib.Call("identity.export", map[string]string{"passphrase": req.Backup.Passphrase}, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, Document: out.Document}
	case localapi.RequestIdentityImport:
		if req.Backup == nil {
			return localapi.Fail(localapi.CodeInvalid, "backup missing")
		}
		return h.command("identity.import", req.Backup)
	case localapi.RequestIdentityReset:
		return h.command("identity.reset", nil)
	case localapi.RequestHelperUpdate:
		if h.update == nil || req.From == "" {
			return localapi.Fail(localapi.CodeInvalid, "only the helper updates itself")
		}
		if err := h.update(req.From); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true}
	case localapi.RequestHelperRemove:
		if h.remove == nil {
			return localapi.Fail(localapi.CodeInvalid, "this helper cannot remove itself")
		}
		if err := h.remove(); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true}
	case localapi.RequestStoreExport:
		if req.Backup == nil {
			return localapi.Fail(localapi.CodeInvalid, "passphrase missing")
		}
		var out struct{ Document string }
		if err := h.lib.Call("store.export", map[string]string{"passphrase": req.Backup.Passphrase}, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, Document: out.Document}
	case localapi.RequestStoreImport:
		if req.Backup == nil {
			return localapi.Fail(localapi.CodeInvalid, "store missing")
		}
		return h.command("store.import", req.Backup)
	case localapi.RequestStoreClear:
		if err := h.lib.Call("store.clear", nil, nil); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		// This engine no longer holds the identity: it stops.
		h.stop()
		return localapi.Response{OK: true}
	case localapi.RequestHostForget:
		return h.command("host.forget", map[string]string{"device_id": req.DeviceID})
	case localapi.RequestAISettingsGet:
		return h.aiSettings("ai.settings.get", nil)
	case localapi.RequestAISettingsSet:
		if req.AISettings == nil {
			return localapi.Fail(localapi.CodeInvalid, "AI settings missing")
		}
		change := *req.AISettings
		change.Keys = nil
		return h.aiSettings("ai.settings.set", change)
	case localapi.RequestAIKeySet:
		if req.AIKey == nil {
			return localapi.Fail(localapi.CodeInvalid, "AI key missing")
		}
		return h.aiSettings("ai.key.set", req.AIKey)
	case localapi.RequestAIModels:
		var out struct {
			Models []localapi.AIModel `json:"models"`
		}
		if err := h.lib.Call("ai.models.list", map[string]string{"source": req.Source}, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, AIModels: out.Models}
	case localapi.RequestAIDraft:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "draft missing")
		}
		return h.command("ai.draft.set", map[string]string{"text": req.AITask.Text})
	case localapi.RequestAITaskStart:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "AI task missing")
		}
		var out struct{ ID uint64 }
		params := map[string]string{"key": req.AITask.Key, "instruction": req.AITask.Instruction}
		if err := h.lib.Call("ai.task.start", params, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true, ID: out.ID}
	case localapi.RequestAITaskReply:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "AI task missing")
		}
		return h.command("ai.task.reply", map[string]any{"id": req.ID, "text": req.AITask.Text})
	case localapi.RequestAITaskPause, localapi.RequestAITaskResume, localapi.RequestAITaskCancel:
		return h.command(string(req.Type), map[string]uint64{"id": req.ID})
	case localapi.RequestAITaskDecide:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "decision missing")
		}
		return h.command("ai.task.decide", map[string]any{"id": req.ID, "approve": req.AITask.Approve})
	case localapi.RequestAITasks:
		var tasks []localapi.AITask
		if err := h.lib.Call("ai.tasks", nil, &tasks); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, AITasks: tasks}
	case localapi.RequestAIHistoryClear:
		return h.command("ai.history.clear", nil)
	case localapi.RequestAIStop:
		return h.command("ai.stop", nil)
	case localapi.RequestShutdown:
		h.stop()
		return localapi.Response{OK: true}
	}
	return localapi.Fail(localapi.CodeUnknown, "unknown request %q", req.Type)
}

// mediaPoll is how long one wait for a media packet lasts, so the pump
// notices a closed GUI connection soon.
const mediaPoll = 500 * time.Millisecond

// Media pumps a controller session's media between the core and the GUI:
// packets from the core (video) go to the GUI, packets from the GUI
// (control messages) go to the core, until either side ends.
func (h *handler) Media(ctx context.Context, req localapi.Request, open func() io.ReadWriter) localapi.Response {
	st, err := h.status()
	if err != nil {
		return localapi.Fail(localapi.CodeInternal, "%v", err)
	}
	live := slices.ContainsFunc(st.Sessions, func(s localapi.SessionInfo) bool {
		return s.ID == req.ID && s.Role == "controller" && (s.State == "connected" || s.State == "reconnecting")
	})
	if !live {
		return localapi.Fail(localapi.CodeInvalid, "no live session %d", req.ID)
	}
	rw := open()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		var buf []byte
		for {
			p, err := localapi.ReadPacket(rw, buf)
			if err != nil {
				return
			}
			buf = p
			if errors.Is(h.lib.MediaWrite(req.ID, p), core.ErrMediaEnded) {
				return
			}
		}
	}()
	buf := make([]byte, localapi.MaxPacket)
	for ctx.Err() == nil {
		n, err := h.lib.MediaRead(req.ID, buf, mediaPoll)
		switch {
		case errors.Is(err, core.ErrMediaTimeout), errors.Is(err, core.ErrMediaTooBig):
			continue
		case err != nil:
			return localapi.Response{OK: true}
		}
		if localapi.WritePacket(rw, buf[:n]) != nil {
			return localapi.Response{OK: true}
		}
	}
	return localapi.Response{OK: true}
}

// aiSettings runs a core method that answers the AI settings.
func (h *handler) aiSettings(method string, params any) localapi.Response {
	var s localapi.AISettings
	if err := h.lib.Call(method, params, &s); err != nil {
		return localapi.Fail(localapi.CodeInvalid, "%v", err)
	}
	return localapi.Response{OK: true, AISettings: &s}
}

// command runs a core method that changes state and answers ok or the
// core's reason; attached sessions see the change at once.
func (h *handler) command(method string, params any) localapi.Response {
	if err := h.lib.Call(method, params, nil); err != nil {
		return localapi.Fail(localapi.CodeInvalid, "%v", err)
	}
	h.changed()
	return localapi.Response{OK: true}
}

// Stream pushes the status now, after every command that changes it, and
// whenever the network's state moves (re-read every statusPoll), until the
// client leaves.
func (h *handler) Stream(ctx context.Context, _ localapi.Request, send func(localapi.Response) error) {
	poke := make(chan struct{}, 1)
	h.mu.Lock()
	h.sessions[poke] = true
	h.mu.Unlock()
	h.updateServe()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, poke)
		h.mu.Unlock()
		h.updateServe()
	}()
	tick := time.NewTicker(statusPoll)
	defer tick.Stop()
	var last *localapi.Status
	for {
		st, err := h.status()
		if err != nil {
			return
		}
		if last == nil || !reflect.DeepEqual(*st, *last) {
			if send(localapi.Response{OK: true, Status: st}) != nil {
				return
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return
		case <-poke:
		case <-tick.C:
		}
	}
}

// updateServe tells the core whom it serves: everyone in No privileges
// mode and while a GUI is attached to the helper; without one, the helper
// serves only unattended grants (Background service) or no one (Follow
// GUI), which ends its host sessions (doc/en-us/run-modes.md).
func (h *handler) updateServe() {
	h.mu.Lock()
	serve := "all"
	if h.runMode == "helper" && len(h.sessions) == 0 {
		serve = "none"
		if h.background {
			serve = "unattended"
		}
	}
	same := serve == h.served
	h.served = serve
	h.mu.Unlock()
	if !same {
		_ = h.lib.Call("host.serve", map[string]string{"serve": serve}, nil)
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
	h.protected.Store(st.Protected)
	var network localapi.NetworkStatus
	var requests []localapi.HostRequest
	var sessions []localapi.SessionInfo
	var recent []localapi.RecentDevice
	var ai *localapi.AIActive
	var about localapi.About
	var permissions *localapi.Permissions
	var byAddress localapi.ByAddress
	for _, part := range []struct {
		name string
		raw  json.RawMessage
		dst  any
	}{{"network", st.Network, &network}, {"requests", st.Requests, &requests}, {"sessions", st.Sessions, &sessions}, {"recent", st.Recent, &recent}, {"ai", st.AI, &ai}, {"about", st.About, &about}, {"permissions", st.Permissions, &permissions}, {"by_address", st.ByAddress, &byAddress}} {
		if err := json.Unmarshal(part.raw, part.dst); err != nil {
			return nil, fmt.Errorf("%s status: %w", part.name, err)
		}
	}
	return &localapi.Status{
		Version: st.Version, Build: version.Build(), ABI: st.ABI, DeviceID: st.DeviceID, Fingerprint: st.Fingerprint,
		StateDir: st.StateDir, RunMode: h.runMode, Background: h.isBackground(), Persistence: st.Persistence,
		PasswordSerial: st.PasswordSerial, Network: network, PermanentPassword: st.PermanentPassword,
		PasswordGrants: st.PasswordGrants, PasswordMode: st.PasswordMode, WithoutAsking: st.WithoutAsking, ByAddress: byAddress, LogsOff: LogsOff(h.stateDir) || !st.KeepsHistory, Protected: st.Protected, AIDraft: st.AIDraft, Permissions: permissions,
		Requests: requests, Sessions: sessions, Recent: recent, AI: ai, Started: h.started,
		Name: st.Name, About: about,
	}, nil
}

// noLogs is the marker in a state directory that keeps every process
// using it from writing logs or crash traces (Settings, No logs).
const noLogs = "no-logs"

// logFile is this process's log (Logger, CrashLog): the path it uses, and
// the open file, nil while logs are off.
var logFile struct {
	sync.Mutex
	path string
	f    *os.File
	off  bool
}

// LogsOff reports whether a state directory says to keep no logs.
func LogsOff(stateDir string) bool {
	_, err := os.Stat(filepath.Join(stateDir, noLogs))
	return err == nil
}

// Logger returns a Logf writing to path (appending), or to stderr when the
// file cannot be opened; nowhere while the state directory (path's
// grandparent) says no logs.
// Side effects: a fatal Go error's trace goes to the same file (CrashLog).
func Logger(path string) func(string, ...any) {
	CrashLog(path)
	return func(format string, args ...any) {
		logFile.Lock()
		defer logFile.Unlock()
		var w io.Writer = os.Stderr
		switch {
		case logFile.f != nil:
			w = logFile.f
		case logFile.off:
			return
		}
		log.New(w, "", log.LstdFlags).Printf(format, args...)
	}
}

// CrashLog opens path for appending and sends this process's crash trace
// (an unrecovered panic or a fatal runtime error) there as well as to
// stderr, unless the state directory (path's grandparent) says no logs.
// Returns: the file, or nil when logs are off or it cannot be opened.
func CrashLog(path string) *os.File {
	logFile.Lock()
	defer logFile.Unlock()
	logFile.path = path
	stateDir := filepath.Dir(filepath.Dir(path))
	logFile.off = LogsOff(stateDir)
	if logFile.off {
		// launchd opens the macOS helper's helper.log anew at each start.
		quietStderr()
		_ = os.Remove(filepath.Join(stateDir, "helper.log"))
		return nil
	}
	return openLog()
}

// openLog opens logFile.path (caller holds logFile).
func openLog() *os.File {
	if err := os.MkdirAll(filepath.Dir(logFile.path), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(logFile.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	logFile.f = f
	return f
}

// SetLogsOff turns logs off for a state directory and this process, and
// deletes the ones written (the logs folder, and the macOS helper's
// helper.log), or turns them on again.
// Side effects: writes or removes the marker; closes or reopens this
// process's log; while off, a crash trace goes to stderr only, and stderr
// itself nowhere (quietStderr).
func SetLogsOff(stateDir string, off bool) error {
	logFile.Lock()
	defer logFile.Unlock()
	marker := filepath.Join(stateDir, noLogs)
	logFile.off = off
	if !off {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if logFile.f == nil && logFile.path != "" {
			openLog()
		}
		return nil
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if logFile.f != nil {
		_ = logFile.f.Close()
		logFile.f = nil
	}
	quietStderr()
	if err := os.RemoveAll(filepath.Join(stateDir, "logs")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stateDir, "helper.log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// isBackground reports the helper's mode for the status.
func (h *handler) isBackground() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.background
}
