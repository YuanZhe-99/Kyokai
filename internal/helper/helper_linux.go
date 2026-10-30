package helper

// The Linux helper (doc/en-us/run-modes.md): a systemd system service
// running the engine as root, and a systemd user service running the
// session agent in every graphical session, which captures and injects
// there. Both run the helper's own root-owned copy of kyokai-svc and the
// core library.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
)

// Names of the units: the engine (a system service) and the session agent
// (a user service in every graphical session).
const (
	ServiceUnit = "kyokai-helper.service"
	AgentUnit   = "kyokai-agent.service"
)

var (
	// BinDir holds the helper's kyokai-svc and core library; root only may
	// change them.
	BinDir = "/usr/local/lib/kyokai/helper"
	// StateDir is the helper's state: identity, secrets (their key sealed
	// by systemd-creds), logs; root only.
	StateDir = "/var/lib/kyokai"
	// SocketPath is the helper's local API socket. Every local user can
	// connect; the helper serves only its operator and the kyokai group.
	SocketPath = "/run/kyokai/ctl.sock"
	// AgentSocket is where the session agent reaches the helper; the
	// helper checks that a connection is its own agent.
	AgentSocket = "/run/kyokai/agent.sock"
)

func serviceFile() string { return "/etc/systemd/system/" + ServiceUnit }
func agentFile() string   { return "/etc/systemd/user/" + AgentUnit }

// Elevated reports whether this process runs as root.
func Elevated() bool { return os.Geteuid() == 0 }

// GroupSID is empty outside Windows.
func GroupSID() string { return "" }

// GroupGID is the `kyokai` group's id, when an administrator created it.
func GroupGID() (uint32, bool) {
	g, err := user.LookupGroup(Group)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(g.Gid, 10, 32)
	return uint32(id), err == nil
}

// serviceUnit runs the engine as root from boot, started again when it
// ends; systemd makes its socket folder (/run/kyokai, which everyone may
// enter) and its state folder (root only).
func serviceUnit() string {
	return fmt.Sprintf(`[Unit]
Description=Kyokai helper
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run --host helper
Restart=always
RestartSec=1
RuntimeDirectory=kyokai
RuntimeDirectoryMode=0755
StateDirectory=kyokai
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
`, svcPath())
}

// agentUnit runs the session agent in every graphical session, as its
// user, with the session's display.
func agentUnit() string {
	return fmt.Sprintf(`[Unit]
Description=Kyokai session agent
PartOf=graphical-session.target
After=graphical-session.target

[Service]
ExecStart=%s agent --socket %s
Restart=always
RestartSec=2

[Install]
WantedBy=graphical-session.target
`, svcPath(), AgentSocket)
}

// sessionUsers are the users with a running user manager, whose sessions
// get the agent at once rather than at their next sign-in.
func sessionUsers() []string {
	out, err := exec.Command("loginctl", "list-users", "--no-legend").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] != "0" {
			names = append(names, f[1])
		}
	}
	return names
}

// userCtl runs systemctl in a user's manager.
func userCtl(name string, args ...string) error {
	return systemctl(append([]string{"--user", "--machine=" + name + "@.host"}, args...)...)
}

// Install copies the app's kyokai-svc and core library, records the
// operator, and starts the engine's service and the session agent.
// Inputs: o.From, the app's bin folder; o.OperatorUID, the installing user.
// Side effects: writes under /usr/local/lib, /var/lib and /etc/systemd;
// starts both services, the agent in every running session.
func Install(o InstallOptions) error {
	if !Elevated() {
		return errors.New("installing the helper needs administrator rights")
	}
	for _, name := range binaries() {
		if !fileExists(filepath.Join(o.From, name)) {
			return fmt.Errorf("install: no %s in %s", name, o.From)
		}
	}
	_ = systemctl("stop", ServiceUnit)
	if err := os.MkdirAll(BinDir, 0o755); err != nil {
		return err
	}
	for _, name := range binaries() {
		if err := copyFile(filepath.Join(o.From, name), filepath.Join(BinDir, name)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return err
	}
	uid := o.OperatorUID
	if err := fsutil.WriteJSON(filepath.Join(StateDir, "helper.json"), Settings{OperatorUID: &uid, AppDir: o.AppDir}); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(serviceFile(), []byte(serviceUnit()), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(agentFile()), 0o755); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(agentFile(), []byte(agentUnit()), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", ServiceUnit); err != nil {
		return err
	}
	if err := systemctl("--global", "enable", AgentUnit); err != nil {
		return err
	}
	for _, name := range sessionUsers() {
		_ = userCtl(name, "daemon-reload")
		_ = userCtl(name, "restart", AgentUnit)
	}
	return nil
}

// Uninstall stops and removes both services and the helper's files and
// sockets, and with purge its state (identity and trusted devices).
func Uninstall(purge bool) error {
	if !Elevated() {
		return errors.New("removing the helper needs administrator rights")
	}
	unload()
	for _, p := range []string{serviceFile(), agentFile(), SocketPath, AgentSocket} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_ = systemctl("daemon-reload")
	if err := removeWithParent(BinDir); err != nil {
		return err
	}
	if purge {
		return os.RemoveAll(StateDir)
	}
	return nil
}

// unload stops the agent in every session and the engine's service, and
// disables both.
func unload() {
	for _, name := range sessionUsers() {
		_ = userCtl(name, "stop", AgentUnit)
	}
	_ = systemctl("--global", "disable", AgentUnit)
	_ = systemctl("disable", "--now", ServiceUnit)
}

// Installed reports whether the helper is installed, from its service file
// alone: unlike Inspect it starts no process, so the GUI asks it as it
// starts.
func Installed() bool { return fileExists(serviceFile()) }

// Inspect reports the installed helper with its state and version (`helper
// status`); it needs no rights.
// Side effects: runs systemctl and the installed engine.
func Inspect() Status {
	var s Status
	if !Installed() {
		return s
	}
	s.Installed = true
	s.Active = exec.Command("systemctl", "is-active", "--quiet", ServiceUnit).Run() == nil
	if out, err := exec.Command(svcPath(), "version").Output(); err == nil {
		s.Version = strings.TrimSpace(string(out))
	}
	return s
}

// SelfUpdate installs the app's signed bundle (bundle.json and bundle.sig
// beside its kyokai-svc, as on Windows) over the helper's files, then exits
// for systemd to start the new one and restarts the agents.
// Inputs: from, the app's bin folder (user-writable); running, this
// helper's version.
// Side effects: replaces the files in BinDir; this process exits a second
// later.
// Notes: the bundle is read once and exactly its verified bytes are
// written, so a file swapped in the app's folder after the check is not
// installed. An unsigned build needs the administrator prompt instead.
func SelfUpdate(from, running string) error {
	b, err := verifiedBundle(from, running)
	if err != nil {
		return err
	}
	if err := installBundle(BinDir, b); err != nil {
		return err
	}
	go func() {
		time.Sleep(time.Second)
		for _, name := range sessionUsers() {
			_ = userCtl(name, "restart", AgentUnit)
		}
		os.Exit(0) // systemd starts the service again (Restart=always)
	}()
	return nil
}

// Retire removes the helper from inside its own service once the app is
// gone or its operator asks: the agents, the units, the files, the state
// and the sockets. Stopping the service ends this process, so it comes
// last.
func Retire() error {
	for _, name := range sessionUsers() {
		_ = userCtl(name, "stop", AgentUnit)
	}
	_ = systemctl("--global", "disable", AgentUnit)
	_ = systemctl("disable", ServiceUnit)
	for _, p := range []string{serviceFile(), agentFile(), SocketPath, AgentSocket} {
		_ = os.Remove(p)
	}
	_ = systemctl("daemon-reload")
	if err := removeWithParent(BinDir); err != nil {
		return err
	}
	if err := os.RemoveAll(StateDir); err != nil {
		return err
	}
	return systemctl("stop", "--no-block", ServiceUnit)
}

func systemctl(args ...string) error {
	if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
