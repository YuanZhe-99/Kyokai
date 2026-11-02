// Package engine is the host of the Rust core in its own process
// (`kyokai-svc run`): it starts the core, serves the local API and stops
// with its lifeline (doc/en-us/architecture.md). In No privileges mode it is
// a user-level child of the GUI and lives exactly as long as the GUI.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/cores"
	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
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
	// Helper, when set, runs the engine as the privileged helper: its
	// socket is open to every local user and this authorizer decides who
	// is served (the operator and the kyokai group).
	Helper *localapi.Authorizer
	// Update installs the signed bundle of the app folder recorded at install
	// into the helper and restarts it (helper.update; a folder the request
	// names must be that one); nil outside the helper.
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var started atomic.Bool
	defer func() {
		if started.Load() {
			lib.Stop()
		}
	}()
	// start starts the core and makes the engine's handler.
	start := func() (*handler, error) {
		st, err := lib.Start(cfg)
		if err != nil {
			return nil, fmt.Errorf("start core: %w", err)
		}
		started.Store(true)
		logf("engine %s started: device %s, secrets %s", opts.Version, st.DeviceID, st.Persistence)
		h := &handler{lib: lib, stop: cancel, started: time.Now(), sessions: map[chan localapi.Response]bool{}, poke: make(chan struct{}, 1),
			runMode: runMode, stateDir: opts.StateDir, update: opts.Update, remove: opts.Remove}
		if runMode == "helper" {
			var m struct {
				Background bool `json:"background"`
			}
			_, _ = fsutil.ReadJSON(filepath.Join(opts.StateDir, "mode.json"), &m)
			h.background = m.Background
		}
		h.updateServe()
		_, _ = h.status() // reads protected mode
		go h.publish(ctx)
		return h, nil
	}
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

	// An engine whose store moved to the other run mode makes no identity
	// of its own: it waits, parked, until a store comes back or the person
	// starts over (doc/en-us/run-modes.md, "Switching").
	if to := movedTo(opts.StateDir); to != "" {
		logf("engine %s parked: its store moved to the %s engine", opts.Version, to)
		p := &parked{moved: to, runMode: runMode, stateDir: opts.StateDir, version: opts.Version, started: time.Now(), stop: cancel, start: start}
		return localapi.ServeWith(ctx, ln, p, authz)
	}
	h, err := start()
	if err != nil {
		ln.Close()
		return err
	}
	return localapi.ServeWith(ctx, ln, h, authz)
}

// protectedAllows are the requests served in protected mode: the status,
// the AI page, what keeps the host user in charge of sessions (confirm,
// disconnect, stop the AI), what the GUI sends on its own as it attaches,
// quitting, and turning the mode off. Everything else is refused, ctl
// included.
var protectedAllows = map[localapi.RequestType]bool{
	localapi.RequestStatus: true, localapi.RequestProtect: true, localapi.RequestShutdown: true, localapi.RequestCloseControllers: true,
	localapi.RequestDecide: true, localapi.RequestClose: true,
	localapi.RequestCaptureShow: true, localapi.RequestInputKyokai: true, localapi.RequestKyokaiShown: true, localapi.RequestSoundSource: true, localapi.RequestLogs: true,
	localapi.RequestPermission: true, localapi.RequestAgentRestart: true, localapi.RequestHelperUpdate: true,
	localapi.RequestAISettingsGet: true, localapi.RequestAISettingsSet: true, localapi.RequestAIKeySet: true,
	localapi.RequestAIModels: true, localapi.RequestAITaskStart: true, localapi.RequestAIDraft: true,
	localapi.RequestAIScopeSet: true, localapi.RequestAIFoldersSet: true, localapi.RequestAIBesideSet: true, localapi.RequestAIApps: true,
	localapi.RequestAITaskReply: true, localapi.RequestAITaskPause: true, localapi.RequestAITaskResume: true,
	localapi.RequestAITaskCancel: true, localapi.RequestAITaskDecide: true, localapi.RequestAITasks: true,
	localapi.RequestAIHistoryClear: true, localapi.RequestAIStop: true, localapi.RequestClipboardMine: true,
	localapi.RequestAITaskRename: true, localapi.RequestAITaskExport: true, localapi.RequestAITaskFork: true, localapi.RequestAITaskCompact: true,
}

// ownerOnly are the requests served only to the engine's owner or the
// helper's operator (localapi.IsOwner), never to the kyokai group: what
// carries the identity out or in, replaces or forgets it, and what stops or
// removes the engine itself. keys.sign, credentials.get and logs.read check
// the same where they are served.
var ownerOnly = map[localapi.RequestType]bool{
	localapi.RequestIdentityExport: true, localapi.RequestIdentityImport: true, localapi.RequestIdentityReset: true, localapi.RequestStoreForgetMove: true,
	localapi.RequestStoreExport: true, localapi.RequestStoreImport: true, localapi.RequestStoreClear: true,
	localapi.RequestHelperRemove: true, localapi.RequestShutdown: true,
}

// refuseTier refuses an ownerOnly request from a caller that is not the
// owner.
// Returns: the refusal, and whether req was refused.
func refuseTier(ctx context.Context, req localapi.Request) (localapi.Response, bool) {
	if ownerOnly[req.Type] && !localapi.IsOwner(ctx) {
		return localapi.Fail(localapi.CodeInvalid, "only this computer's owner may do this"), true
	}
	return localapi.Response{}, false
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
	// streams and videos count the open streams (Media).
	streams, videos atomic.Int32

	// poke asks the status producer (publish) for a new status soon.
	poke chan struct{}

	mu sync.Mutex
	// sessions are the attached sessions' mailboxes, each holding the
	// newest status frame it has not sent yet; true while the session still
	// waits for its first frame.
	sessions map[chan localapi.Response]bool
	// last is the status frame sent last, encoded.
	last []byte
}

// Handle answers one request: the caller's tier (refuseTier) and protected
// mode (protectedAllows) are checked first, then the core is asked.
func (h *handler) Handle(ctx context.Context, req localapi.Request) localapi.Response {
	if resp, refused := refuseTier(ctx, req); refused {
		return resp
	}
	if h.protected.Load() && !protectedAllows[req.Type] {
		return localapi.Fail(localapi.CodeInvalid, "protected mode is on: turn it off with the permanent password first")
	}
	if resp, ok := h.services(ctx, req); ok {
		return resp
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
	case localapi.RequestNetworkProfiles:
		var nets localapi.NetworkProfiles
		if err := h.lib.Call("network.profiles", nil, &nets); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Networks: &nets}
	case localapi.RequestNetworkProfileSet:
		if req.Profile == nil {
			return localapi.Fail(localapi.CodeInvalid, "network missing")
		}
		return h.networks("network.profile.set", req.Profile)
	case localapi.RequestNetworkProfileRemove:
		if req.Profile == nil || req.Profile.ID == "" {
			return localapi.Fail(localapi.CodeInvalid, "network missing")
		}
		return h.networks("network.profile.remove", map[string]string{"id": req.Profile.ID})
	case localapi.RequestNetworkDirect:
		if req.Direct == nil {
			return localapi.Fail(localapi.CodeInvalid, "direct choice missing")
		}
		return h.networks("network.direct", map[string]bool{"direct": *req.Direct})
	case localapi.RequestNetworkLatencyFirst:
		if req.LatencyFirst == nil {
			return localapi.Fail(localapi.CodeInvalid, "latency choice missing")
		}
		return h.networks("network.latency_first", map[string]bool{"latency_first": *req.LatencyFirst})
	case localapi.RequestNetworkBackend:
		if req.EasyTier == nil {
			return localapi.Fail(localapi.CodeInvalid, "backend missing")
		}
		var tag any // null: Kyokai Network
		if *req.EasyTier != "" {
			tag = *req.EasyTier
		}
		return h.networks("network.backend", map[string]any{"easytier": tag})
	case localapi.RequestCores:
		out := localapi.Cores{Installed: cores.Installed(h.stateDir)}
		releases, err := cores.Releases(ctx)
		if err != nil {
			out.Error = err.Error()
		}
		for _, r := range releases {
			out.Releases = append(out.Releases, localapi.CoreRelease{Tag: r.Tag, Prerelease: r.Pre})
		}
		return localapi.Response{OK: true, Cores: &out}
	case localapi.RequestCoreInstall:
		releases, err := cores.Releases(ctx)
		if err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		i := slices.IndexFunc(releases, func(r cores.Release) bool { return r.Tag == req.Tag })
		if i < 0 {
			return localapi.Fail(localapi.CodeInvalid, "EasyTier %s cannot be downloaded for this computer", req.Tag)
		}
		if err := cores.Install(ctx, h.stateDir, releases[i]); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		return localapi.Response{OK: true, Cores: &localapi.Cores{Installed: cores.Installed(h.stateDir)}}
	case localapi.RequestNameSet:
		if req.Name == nil && req.PublishName == nil {
			return localapi.Fail(localapi.CodeInvalid, "name missing")
		}
		params := map[string]any{}
		if req.Name != nil {
			params["name"] = *req.Name
		}
		if req.PublishName != nil {
			params["publish"] = *req.PublishName
		}
		return h.command("device.set_name", params)
	case localapi.RequestAgentRestart:
		return h.command("agent.restart", nil)
	case localapi.RequestStartOver:
		return h.command("store.start_over", nil)
	case localapi.RequestAccessSettings:
		if req.PasswordMode == "" && req.WithoutAsking == nil && req.ByAddress == nil && req.ServicePorts == nil && req.Nanokvm == nil {
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
		if p := req.ServicePorts; p != nil {
			ports := map[string]int{}
			for name, port := range map[string]int{"ssh": p.SSH, "rdp": p.RDP, "vnc": p.VNC} {
				if port < 0 || port > 65535 {
					return localapi.Fail(localapi.CodeInvalid, "a port is between 1 and 65535")
				}
				if port != 0 {
					ports[name] = port
				}
			}
			params["service_ports"] = ports
		}
		if req.Nanokvm != nil {
			params["nanokvm"] = req.Nanokvm
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
		// The helper's session agent keeps its own log (Windows): when the
		// choice changes it starts again (its supervisor restarts it), and
		// the new one follows the choice. Every GUI that attaches sends its
		// settings again; restarting the agent then ended every stream it
		// carried.
		changed := LogsOff(h.stateDir) != *req.LogsOff
		if err := SetLogsOff(h.stateDir, *req.LogsOff); err != nil {
			return localapi.Fail(localapi.CodeInternal, "logs: %v", err)
		}
		if changed {
			_ = h.lib.Call("agent.restart", nil, nil)
		}
		return h.command("access.history", map[string]bool{"keep": !*req.LogsOff})
	case localapi.RequestLogsRead:
		// The helper's logs need administrator rights to open: its
		// operator reads them here; the kyokai group does not.
		if !localapi.IsOwner(ctx) {
			return localapi.Fail(localapi.CodeInvalid, "only this computer's owner reads its logs")
		}
		return localapi.Response{OK: true, Document: ReadLogs(h.stateDir)}
	case localapi.RequestCaptureShow:
		if req.Show == nil {
			return localapi.Fail(localapi.CodeInvalid, "show missing")
		}
		return h.command("capture.show", map[string]bool{"show": *req.Show})
	case localapi.RequestInputKyokai:
		if req.KyokaiInput == nil {
			return localapi.Fail(localapi.CodeInvalid, "allow missing")
		}
		return h.command("input.kyokai", map[string]bool{"allow": *req.KyokaiInput})
	case localapi.RequestKyokaiShown:
		if req.Shown == nil {
			return localapi.Fail(localapi.CodeInvalid, "shown missing")
		}
		return h.command("kyokai.shown", map[string]bool{"shown": *req.Shown})
	case localapi.RequestSoundSource:
		if req.SoundSource == nil {
			return localapi.Fail(localapi.CodeInvalid, "source missing")
		}
		return h.command("sound.source", map[string]string{"source": *req.SoundSource})
	case localapi.RequestNanokvmDevices:
		var devices localapi.NanokvmDevices
		if err := h.lib.Call("nanokvm.devices", nil, &devices); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, NanokvmDevices: &devices}
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
		// The helper reads the app folder it was installed from; From, when
		// given, must name that folder (helper.SelfUpdate).
		if h.update == nil {
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
	case localapi.RequestCloseControllers:
		return h.command("sessions.close_controllers", nil)
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
		return h.command("store.import", map[string]any{"document": req.Backup.Document, "passphrase": req.Backup.Passphrase, "replace": req.Replace})
	case localapi.RequestStoreClear:
		if err := h.lib.Call("store.clear", map[string]string{"to": req.MovedTo}, nil); err != nil {
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
	case localapi.RequestAIScopeSet:
		return h.aiSettings("ai.scope.set", map[string]*[]string{"apps": req.Apps})
	case localapi.RequestAIBesideSet:
		if req.On == nil {
			return localapi.Fail(localapi.CodeInvalid, "on missing")
		}
		return h.aiSettings("ai.beside.set", map[string]bool{"on": *req.On})
	case localapi.RequestAIFoldersSet:
		return h.aiSettings("ai.folders.set", map[string]*[]string{"folders": req.Folders})
	case localapi.RequestAIApps:
		var apps []string
		if err := h.lib.Call("ai.apps", nil, &apps); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, AIApps: apps}
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
		params := map[string]any{"key": req.AITask.Key, "instruction": req.AITask.Instruction, "choice": aiChoice(req.AITask)}
		if err := h.lib.Call("ai.task.start", params, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true, ID: out.ID}
	case localapi.RequestAITaskReply:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "AI task missing")
		}
		return h.command("ai.task.reply", map[string]any{"id": req.ID, "text": req.AITask.Text, "choice": aiChoice(req.AITask)})
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
	case localapi.RequestAITaskRename:
		if req.AITask == nil {
			return localapi.Fail(localapi.CodeInvalid, "name missing")
		}
		return h.command("ai.task.rename", map[string]any{"id": req.ID, "name": req.AITask.Name})
	case localapi.RequestAITaskFork:
		params := map[string]any{"id": req.ID}
		if req.AITask != nil && req.AITask.Run != nil {
			params["run"] = *req.AITask.Run
		}
		var out struct{ ID uint64 }
		if err := h.lib.Call("ai.task.fork", params, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		h.changed()
		return localapi.Response{OK: true, ID: out.ID}
	case localapi.RequestAITaskCompact:
		return h.command("ai.task.compact", map[string]uint64{"id": req.ID})
	case localapi.RequestAITaskExport:
		var out struct{ Markdown string }
		if err := h.lib.Call("ai.task.export", map[string]uint64{"id": req.ID}, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true, Document: out.Markdown}
	case localapi.RequestAIHistoryClear:
		return h.command("ai.history.clear", nil)
	case localapi.RequestAIStop:
		return h.command("ai.stop", nil)
	case localapi.RequestClipboardMine:
		if !isDigest(req.Digest) {
			return localapi.Fail(localapi.CodeInvalid, "digest is not a lowercase hex SHA-256")
		}
		// Changes nothing the status shows: no push.
		if err := h.lib.Call("clipboard.mine", map[string]string{"digest": req.Digest}, nil); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err)
		}
		return localapi.Response{OK: true}
	case localapi.RequestShutdown:
		h.stop()
		return localapi.Response{OK: true}
	}
	return localapi.Fail(localapi.CodeUnknown, "unknown request %q", req.Type)
}

// mediaPoll is how long one wait for a media packet lasts, so the pump
// notices a closed GUI connection soon.
const mediaPoll = 500 * time.Millisecond

// Limits of the streams one engine carries at once (an authorized client
// could otherwise open them until memory runs out), and of their packets.
const (
	// maxStreams bounds the media streams and tunnels open together;
	// maxVideoStreams the media streams among them, each with a buffer of
	// localapi.MaxPacket for the core's video.
	maxStreams      = 64
	maxVideoStreams = 16
	// videoInMax is the longest packet a client sends on a media stream: a
	// control message, which the session carries up to 4 MiB.
	videoInMax = 8 << 20
	// tunnelMax is the longest tunnel packet either way: the core gives a
	// session frame's bytes (under 64 KiB), the client pieces of 64 KiB.
	tunnelMax = 1 + 128<<10
)

// Media pumps a controller session's media between the core and the GUI:
// packets from the core (video) go to the GUI, packets from the GUI
// (control messages) go to the core, until either side ends. A tunnel's
// bytes go the same way (tunnel.open).
// Notes: refused in protected mode like any request protectedAllows does
// not name, and beyond maxStreams (maxVideoStreams for media).
func (h *handler) Media(ctx context.Context, req localapi.Request, open func() io.ReadWriter) localapi.Response {
	if h.protected.Load() && !protectedAllows[req.Type] {
		return localapi.Fail(localapi.CodeInvalid, "protected mode is on: turn it off with the permanent password first")
	}
	if h.streams.Add(1) > maxStreams {
		h.streams.Add(-1)
		return localapi.Fail(localapi.CodeInvalid, "too many streams are open")
	}
	defer h.streams.Add(-1)
	if req.Type == localapi.RequestTunnelOpen {
		return h.tunnel(ctx, req, open)
	}
	if req.Type == localapi.RequestAIPaneServe {
		return h.servePane(ctx, req, open)
	}
	if h.videos.Add(1) > maxVideoStreams {
		h.videos.Add(-1)
		return localapi.Fail(localapi.CodeInvalid, "too many media streams are open")
	}
	defer h.videos.Add(-1)
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
	h.pump(ctx, req.ID, open(), localapi.MaxPacket, videoInMax)
	return localapi.Response{OK: true}
}

// pump carries a controller session's packets between the core and a
// client until either ends: the core's (video, a tunnel's bytes) to the
// client, the client's (control messages, a tunnel's bytes) to the core.
// Inputs: out, the longest packet taken from the core (a longer one is
// dropped); in, the longest the client may send (a longer one ends it).
func (h *handler) pump(ctx context.Context, session uint64, rw io.ReadWriter, out, in int) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		var buf []byte
		for {
			p, err := localapi.ReadPacketLimit(rw, buf, in)
			if err != nil {
				return
			}
			buf = p
			if errors.Is(h.lib.MediaWrite(session, p), core.ErrMediaEnded) {
				return
			}
		}
	}()
	buf := make([]byte, out)
	for ctx.Err() == nil {
		n, err := h.lib.MediaRead(session, buf, mediaPoll)
		switch {
		case errors.Is(err, core.ErrMediaTimeout), errors.Is(err, core.ErrMediaTooBig):
			continue
		case err != nil:
			return
		}
		if localapi.WritePacket(rw, buf[:n]) != nil {
			return
		}
	}
}

// aiSettings runs a core method that answers the AI settings.
func (h *handler) aiSettings(method string, params any) localapi.Response {
	var s localapi.AISettings
	if err := h.lib.Call(method, params, &s); err != nil {
		return localapi.Fail(localapi.CodeInvalid, "%v", err)
	}
	return localapi.Response{OK: true, AISettings: &s}
}

// networks runs a core method that changes the networks kept and answers
// them; a refusal says why.
// Side effects: the attached sessions get the new status.
func (h *handler) networks(method string, params any) localapi.Response {
	var nets localapi.NetworkProfiles
	if err := h.lib.Call(method, params, &nets); err != nil {
		return localapi.Fail(localapi.CodeInvalid, "%v", err)
	}
	h.changed()
	return localapi.Response{OK: true, Networks: &nets}
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

// Stream sends the session the status as the producer (publish) makes it:
// at once, after every command that changes it, and whenever it moves,
// until the client leaves.
// Notes: a slow client falls behind on its own: its mailbox keeps only the
// newest frame, and nobody waits for it.
func (h *handler) Stream(ctx context.Context, _ localapi.Request, send func(localapi.Response) error) {
	box := make(chan localapi.Response, 1)
	h.mu.Lock()
	h.sessions[box] = true
	h.mu.Unlock()
	h.updateServe()
	h.changed()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, box)
		h.mu.Unlock()
		h.updateServe()
		// The helper's controller sessions belong to a GUI: one that went
		// away (it quit, crashed, or Windows logged off) and did not come
		// back within a moment leaves nobody to look at them.
		if h.runMode == "helper" {
			time.AfterFunc(controllersGrace, h.closeOrphans)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-box:
			// A status that could not be read ends the session, so the GUI
			// attaches anew and says why instead of showing the last one.
			if send(frame) != nil || !frame.OK {
				return
			}
		}
	}
}

// statusPoll is how often the producer re-reads the status for what the
// core does not announce (the network's state, a new connection request);
// statusSettle is how long it lets a burst of commands settle first.
const (
	statusPoll   = 500 * time.Millisecond
	statusSettle = 100 * time.Millisecond
)

// publish is the engine's one status producer: on a poke (changed), after
// statusSettle, and every statusPoll it reads the status once and hands the
// same encoded frame to every attached session (deliver), until ctx ends.
// Notes: reads nothing while no session is attached.
func (h *handler) publish(ctx context.Context) {
	tick := time.NewTicker(statusPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-h.poke:
			select {
			case <-ctx.Done():
				return
			case <-time.After(statusSettle):
			}
			// A poke that came meanwhile is answered by this read.
			select {
			case <-h.poke:
			default:
			}
		}
		h.mu.Lock()
		attached := len(h.sessions) > 0
		h.mu.Unlock()
		if !attached {
			continue
		}
		st, err := h.status()
		resp := localapi.Response{OK: true, Status: st}
		if err != nil {
			// Never stale data as current: the sessions end with the error.
			resp = localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		frame, err := localapi.Encode(resp)
		if err != nil {
			continue
		}
		h.deliver(frame)
	}
}

// deliver hands frame to every session that has not had it: all of them
// when it differs from the last one, else those waiting for their first.
// Side effects: replaces a frame a session has not taken yet; never waits.
func (h *handler) deliver(frame localapi.Response) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := !bytes.Equal(frame.Encoded(), h.last)
	h.last = frame.Encoded()
	for box, first := range h.sessions {
		if !changed && !first {
			continue
		}
		h.sessions[box] = false
		// deliver is the only sender, under h.mu: after the stale frame is
		// taken out there is room.
		select {
		case <-box:
		default:
		}
		box <- frame
	}
}

// controllersGrace is how long the helper keeps its controller sessions
// after its last GUI detached: a GUI restarting (an update, Reopen) comes
// back in time.
const controllersGrace = 15 * time.Second

// closeOrphans ends the helper's controller sessions when no GUI is
// attached.
func (h *handler) closeOrphans() {
	h.mu.Lock()
	attached := len(h.sessions) > 0
	h.mu.Unlock()
	if !attached {
		_ = h.lib.Call("sessions.close_controllers", nil, nil)
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

// changed asks the producer (publish) for a new status: the attached
// sessions receive it after every command that changes it.
// Notes: never waits; pokes that come together make one status.
func (h *handler) changed() {
	select {
	case h.poke <- struct{}{}:
	default:
	}
}

// coreStatus is the core's status decoded once: core.Status with the parts
// it leaves raw decoded straight into the local API's types (these
// shallower fields take the same JSON names).
type coreStatus struct {
	core.Status
	Network     localapi.NetworkStatus  `json:"network"`
	Requests    []localapi.HostRequest  `json:"requests"`
	Sessions    []localapi.SessionInfo  `json:"sessions"`
	Recent      []localapi.RecentDevice `json:"recent"`
	AI          *localapi.AIActive      `json:"ai"`
	About       localapi.About          `json:"about"`
	Permissions *localapi.Permissions   `json:"permissions"`
	ByAddress   localapi.ByAddress      `json:"by_address"`
	Watching    []localapi.Watched      `json:"watching"`
	Watchers    []localapi.Watcher      `json:"watchers"`
}

// status reads the core's status as the local API's.
// Side effects: notes whether protected mode is on.
func (h *handler) status() (*localapi.Status, error) {
	var st coreStatus
	if err := h.lib.Call("status", nil, &st); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	h.protected.Store(st.Protected)
	return &localapi.Status{
		Version: st.Version, Build: version.Build(), ABI: st.ABI, DeviceID: st.DeviceID, Fingerprint: st.Fingerprint,
		StateDir: st.StateDir, RunMode: h.runMode, Background: h.isBackground(), Persistence: st.Persistence,
		PasswordSerial: st.PasswordSerial, Network: st.Network, PermanentPassword: st.PermanentPassword,
		PasswordGrants: st.PasswordGrants, PasswordMode: st.PasswordMode, WithoutAsking: st.WithoutAsking, ByAddress: st.ByAddress, ServicePorts: localapi.ServicePorts(st.ServicePorts), Nanokvm: (*localapi.Nanokvm)(st.Nanokvm), LogsOff: LogsOff(h.stateDir) || !st.KeepsHistory, Protected: st.Protected, AIDraft: st.AIDraft, Permissions: st.Permissions,
		Requests: st.Requests, Sessions: st.Sessions, Recent: st.Recent, AI: st.AI, Started: h.started,
		Name: st.Name, PublishName: st.PublishName, About: st.About, Watching: st.Watching, Watchers: st.Watchers,
	}, nil
}

// isDigest reports whether s is a SHA-256 in lowercase hex.
func isDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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

// logTail is how much of each log ReadLogs returns: its newest part.
const logTail = 1 << 20

// ReadLogs returns the logs a state directory keeps (logs/*.log and the
// macOS helper's helper.log), each under its name and cut to its newest
// logTail bytes.
// Returns: "" while the state directory says no logs, or none exist.
func ReadLogs(stateDir string) string {
	if LogsOff(stateDir) {
		return ""
	}
	paths, _ := filepath.Glob(filepath.Join(stateDir, "logs", "*.log"))
	paths = append(paths, filepath.Join(stateDir, "helper.log"))
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if len(data) > logTail {
			data = data[len(data)-logTail:]
		}
		fmt.Fprintf(&b, "==== %s ====\n%s\n", filepath.Base(p), data)
	}
	return b.String()
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

// CrashLog opens path for appending and keeps there what this process
// writes to stderr — a crash trace (an unrecovered panic or a fatal runtime
// error) and what the core writes (its panics and log lines) — unless the
// state directory (path's grandparent) says no logs; stderr then goes
// nowhere.
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

// openLog opens logFile.path and points this process's stderr, and the
// standard logger, at it; where stderr cannot be pointed there, the crash
// trace alone goes there (caller holds logFile).
// Notes: with stderr in the file, the crash trace is not sent there a
// second time (SetCrashOutput writes in addition to stderr).
func openLog() *os.File {
	if err := os.MkdirAll(filepath.Dir(logFile.path), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(logFile.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if logStderr(f) {
		// The standard logger kept the stderr it started with (Windows).
		log.SetOutput(f)
	} else {
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
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
	// Stderr leaves the log before the log closes.
	quietStderr()
	log.SetOutput(io.Discard)
	if logFile.f != nil {
		_ = logFile.f.Close()
		logFile.f = nil
	}
	// The session agent's log is open until the agent that wrote it exits
	// (Windows refuses to delete it meanwhile): a few seconds at most.
	logs := filepath.Join(stateDir, "logs")
	err := os.RemoveAll(logs)
	for wait := 0; err != nil && wait < 30; wait++ {
		time.Sleep(100 * time.Millisecond)
		err = os.RemoveAll(logs)
	}
	if err != nil {
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

// aiChoice is a task's model and thinking as the core takes them (`choice`
// of ai.task.start and ai.task.reply); absent ones keep the defaults.
func aiChoice(t *localapi.AITaskRequest) map[string]any {
	c := map[string]any{}
	if t.Model != nil {
		c["model"] = t.Model
	}
	if t.Thinking != "" {
		c["thinking"] = t.Thinking
	}
	if t.On != "" {
		c["on"] = t.On
	}
	return c
}
