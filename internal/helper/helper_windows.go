package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
)

// ServiceName is the Windows service of the helper.
const ServiceName = "KyokaiHelper"

// BinDir holds the helper's administrator-owned copies of the app's files,
// so a user cannot swap what the service runs.
var BinDir = filepath.Join(os.Getenv("ProgramFiles"), "Kyokai", "helper")

// root is the helper's data folder: SYSTEM and Administrators only, except
// that users may open it to reach the socket.
var root = filepath.Join(os.Getenv("ProgramData"), "Kyokai")

// StateDir is the helper's state: identity, secrets (DPAPI as SYSTEM),
// trusted devices, logs.
var StateDir = filepath.Join(root, "state")

// SocketPath is the helper's local API socket. Every local user can
// connect; the helper serves only the operator and the group.
var SocketPath = filepath.Join(root, "ctl.sock")

// Elevated reports whether this process has administrator rights (an
// elevated token; the service runs as SYSTEM).
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// GroupSID is the SID of the local `kyokai` group, or "" when no
// administrator created it.
func GroupSID() string {
	sid, _, _, err := windows.LookupSID("", Group)
	if err != nil {
		return ""
	}
	return sid.String()
}

// Install copies the app's engine and core, locks down the state folder,
// records the operator and registers and starts an automatic service.
// Inputs: o.From, the app's bin folder; o.OperatorSID, the installing user.
// Side effects: writes under Program Files and ProgramData; creates the
// KyokaiHelper service running as SYSTEM.
// Notes: the service always runs; the run mode (Follow GUI or Background)
// decides when it serves sessions.
func Install(o InstallOptions) error {
	if !Elevated() {
		return errors.New("installing the helper needs administrator rights")
	}
	if _, err := windows.StringToSid(o.OperatorSID); err != nil {
		return fmt.Errorf("operator %q: %w", o.OperatorSID, err)
	}
	stopService()
	if err := os.MkdirAll(BinDir, 0o755); err != nil {
		return err
	}
	for _, name := range binaries() {
		src := filepath.Join(o.From, name)
		if !fileExists(src) {
			return fmt.Errorf("install %s: missing in %s", name, o.From)
		}
		if err := copyFile(src, filepath.Join(BinDir, name)); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return err
	}
	// SYSTEM and Administrators only; users may only open the folder that
	// holds the socket (not inherited by the state folder).
	if out, err := exec.Command("icacls", root, "/inheritance:r", "/grant:r",
		"*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F", "*S-1-5-32-545:RX").CombinedOutput(); err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	if err := fsutil.WriteJSON(filepath.Join(StateDir, "helper.json"), Settings{OperatorSID: o.OperatorSID}); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		s, err = m.CreateService(ServiceName, svcPath(), mgr.Config{
			DisplayName: "Kyokai helper",
			Description: "Lets Kyokai show and operate this computer's sign-in and administrator prompts.",
			StartType:   mgr.StartAutomatic,
		}, "run", "--host", "helper")
		if err != nil {
			return err
		}
	}
	defer s.Close()
	// Restart after a failure, and after a self-update (which exits).
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 2 * time.Second}}, 60)
	return s.Start()
}

// stopService stops the helper service if it runs, waiting up to 10 s.
func stopService() {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return
	}
	defer s.Close()
	if st, err := s.Control(svc.Stop); err == nil {
		for i := 0; i < 50 && st.State != svc.Stopped; i++ {
			time.Sleep(200 * time.Millisecond)
			st, _ = s.Query()
		}
	}
}

// Uninstall stops and deletes the service and its files, and with purge
// its state (the helper's identity and trusted devices) too.
// Side effects: removes the Kyokai folders under Program Files and
// ProgramData once they are empty.
func Uninstall(purge bool) error {
	if !Elevated() {
		return errors.New("removing the helper needs administrator rights")
	}
	stopService()
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(ServiceName); err == nil {
			_ = s.Delete()
			s.Close()
		}
		m.Disconnect()
	}
	if err := removeWithParent(BinDir); err != nil {
		return err
	}
	_ = os.Remove(SocketPath)
	if purge {
		return removeWithParent(StateDir)
	}
	return nil
}

// Inspect reports the installed helper. It asks only for query rights, so
// it works without administrator rights (the GUI).
func Inspect() Status {
	var st Status
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return st
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(ServiceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return st
	}
	defer windows.CloseServiceHandle(h)
	st.Installed = true
	var q windows.SERVICE_STATUS
	if windows.QueryServiceStatus(h, &q) == nil {
		st.Active = q.CurrentState == windows.SERVICE_RUNNING
	}
	if out, err := exec.Command(svcPath(), "version").Output(); err == nil {
		st.Version = strings.TrimSpace(string(out))
	}
	return st
}
