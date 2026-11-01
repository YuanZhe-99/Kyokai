package helper

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// ServiceName is the Windows service of the helper.
const ServiceName = "KyokaiHelper"

// FirewallRule is the inbound rule that lets the helper's engine take
// direct (hole-punched) connections without Windows Firewall asking.
const FirewallRule = "Kyokai helper"

// removeFirewallRule deletes the helper's firewall rule, if there is one.
func removeFirewallRule() {
	_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+FirewallRule).Run()
}

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

// AgentSocket is unused on Windows, where the helper starts its agent and
// they talk over named pipes.
const AgentSocket = ""

// GroupGID is unused on Windows, where the group is a SID.
func GroupGID() (uint32, bool) { return 0, false }

// GroupSID is the SID of this computer's own `kyokai` group, or "" when no
// administrator created it.
// Notes: only a local group counts: a domain group of the same name (which
// an unqualified lookup also finds) is not this computer's to grant.
func GroupSID() string {
	host, err := windows.ComputerName()
	if err != nil {
		return ""
	}
	sid, domain, kind, err := windows.LookupSID("", host+`\`+Group)
	if err != nil || kind != windows.SidTypeAlias || !strings.EqualFold(domain, host) {
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
	keepPending()
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
	// A folder someone else made (users may create folders in ProgramData)
	// is moved aside, never adopted.
	if err := prepareDir(root, rootSDDL); err != nil {
		return err
	}
	if err := prepareDir(StateDir, stateSDDL); err != nil {
		return err
	}
	if err := fsutil.WriteJSON(filepath.Join(StateDir, "helper.json"), Settings{OperatorSID: o.OperatorSID, AppDir: o.AppDir}); err != nil {
		return err
	}
	// The helper always tries direct paths, whose UDP sockets Windows
	// Firewall would otherwise ask about.
	removeFirewallRule()
	if out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name="+FirewallRule,
		"dir=in", "action=allow", "program="+svcPath(), "enable=yes", "profile=any").CombinedOutput(); err != nil {
		return fmt.Errorf("firewall rule: %v: %s", err, out)
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
	removeFirewallRule()
	if err := removeWithParent(BinDir); err != nil {
		return err
	}
	_ = os.Remove(SocketPath)
	if purge {
		return removeWithParent(StateDir)
	}
	return nil
}

// openService opens the helper's service for queries, which needs no
// administrator rights.
// Returns: the service manager's and the service's handles, which the
// caller closes; an error when either does not open (no helper).
func openService() (scm, h windows.Handle, err error) {
	scm, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, err
	}
	name, _ := windows.UTF16PtrFromString(ServiceName)
	h, err = windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, 0, err
	}
	return scm, h, nil
}

// Installed reports whether the helper's service exists, from the service
// manager alone: unlike Inspect it starts no process, so the GUI asks it
// as it starts.
func Installed() bool {
	scm, h, err := openService()
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(h)
	windows.CloseServiceHandle(scm)
	return true
}

// Inspect reports the installed helper with its state and version (`helper
// status`). It asks only for query rights, so it needs no administrator.
// Side effects: runs the installed engine for its version, with no window.
func Inspect() Status {
	var st Status
	scm, h, err := openService()
	if err != nil {
		return st
	}
	defer windows.CloseServiceHandle(scm)
	defer windows.CloseServiceHandle(h)
	st.Installed = true
	var q windows.SERVICE_STATUS
	if windows.QueryServiceStatus(h, &q) == nil {
		st.Active = q.CurrentState == windows.SERVICE_RUNNING
	}
	if out, err := versionCmd().Output(); err == nil {
		st.Version = strings.TrimSpace(string(out))
	}
	return st
}

// versionCmd asks the installed engine for its version with no console:
// the engine is a console program, and started from a process without one
// (the GUI) it got a console window of its own, which Windows left blank on
// the screen when the GUI started at login.
func versionCmd() *exec.Cmd {
	cmd := exec.Command(svcPath(), "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// SelfUpdate replaces the helper's files with the signed bundle in from
// (the app's bin folder), then exits so the service manager restarts it
// with the new files.
// Inputs: from, the app folder a request named (empty: the recorded one;
// another is refused, updateSource); running, this helper's version.
// Side effects: writes into BinDir; ends this process a second later.
// Notes: installs the bytes that were verified, read once (installBundle).
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
		os.Exit(3) // the recovery action restarts the service
	}()
	return nil
}

// Retire removes the helper from inside its own service once the app is
// gone: the service is marked for deletion, the state and socket are
// deleted, and the running files go at the next restart of Windows.
// Side effects: deletes the KyokaiHelper service and the helper's state;
// the caller exits right after.
func Retire() error {
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(ServiceName); err == nil {
			_ = s.Delete()
			s.Close()
		}
		m.Disconnect()
	}
	removeFirewallRule()
	// What can go now goes. What is still open (the running engine's log,
	// its executables) goes when Windows next starts, children before
	// their folders.
	_ = os.RemoveAll(root)
	for _, dir := range []string{root, filepath.Dir(BinDir)} {
		for _, p := range deepestFirst(dir) {
			if name, err := windows.UTF16PtrFromString(p); err == nil {
				_ = windows.MoveFileEx(name, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
			}
		}
	}
	return nil
}

// keepPending drops the deletions a retired helper scheduled for the next
// restart of Windows under the helper's folders (dropPending), so a helper
// installed again before that restart keeps its files.
// Side effects: rewrites PendingFileRenameOperations when it held some.
func keepPending() {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	ops, _, err := k.GetStringsValue("PendingFileRenameOperations")
	if err != nil {
		return
	}
	keep, dropped := dropPending(ops, []string{root, filepath.Dir(BinDir)})
	switch {
	case !dropped:
	case len(keep) == 0:
		_ = k.DeleteValue("PendingFileRenameOperations")
	default:
		_ = k.SetStringsValue("PendingFileRenameOperations", keep)
	}
}

// Security of the helper's folders, as SDDL: owned by Administrators, a
// protected DACL (nothing inherited from ProgramData, which lets users
// create folders and gives their creator full control) granting SYSTEM and
// Administrators full control of everything below; users may only open
// root, to reach the socket.
const (
	rootSDDL  = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;0x1200a9;;;BU)"
	stateSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
)

// Well-known SIDs the helper's folders may belong to and grant changes to.
const (
	sidSystem = "S-1-5-18"
	sidAdmins = "S-1-5-32-544"
)

// fileDeleteChild is FILE_DELETE_CHILD: delete what a folder holds.
const fileDeleteChild = 0x40

// writeRights are the rights on a folder that let someone change it or
// what is in it: add or delete entries, write its attributes, or change
// its security.
const writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
	fileDeleteChild | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC |
	windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED

// prepareDir makes dir the helper's: a folder that is already trusted
// (checkTrusted) gets the security sddl describes; one that is not, or a
// link, is moved aside to dir.untrusted-<time>, and a new folder is created
// with that security from the start.
// Side effects: renames or creates dir; sets its owner and DACL, which
// Windows propagates to everything below.
// Notes: the folder is created with its security in one call, which fails
// if someone created it meanwhile, so no one else's folder is adopted.
func prepareDir(dir, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		if checkTrusted(dir) == nil {
			owner, _, err := sd.Owner()
			if err != nil {
				return err
			}
			dacl, _, err := sd.DACL()
			if err != nil {
				return err
			}
			return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
				owner, nil, dacl, nil)
		}
		aside := fmt.Sprintf("%s.untrusted-%d", dir, time.Now().UnixNano())
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("move aside %s, which someone else made: %w", dir, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return checkTrusted(dir)
}

// CheckState refuses to run the helper on folders it cannot trust: its
// data folder and state folder must be plain folders (no junction or link)
// owned by SYSTEM or Administrators, which no one else may change.
// Returns: why not, naming the folder.
func CheckState() error {
	for _, dir := range []string{root, StateDir} {
		if err := checkTrusted(dir); err != nil {
			return err
		}
	}
	return nil
}

// checkTrusted reports why dir is not a folder only SYSTEM and
// Administrators control: it is a link or junction (a reparse point), it
// belongs to someone else, or its DACL lets anyone else change it or what
// is in it (an inherit-only entry included, which reaches what is created
// below).
func checkTrusted(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || isReparse(fi) {
		return fmt.Errorf("%s is a link or not a folder", dir)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !trustedSID(owner) {
		return fmt.Errorf("%s belongs to %s, not SYSTEM or Administrators", dir, owner)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s is open to everyone", dir)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return fmt.Errorf("%s has an access entry of type %d", dir, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trustedSID(sid) && ace.Mask&writeRights != 0 {
			return fmt.Errorf("%s lets %s change it", dir, sid)
		}
	}
	return nil
}

// trustedSID reports whether sid is SYSTEM or Administrators.
func trustedSID(sid *windows.SID) bool {
	s := sid.String()
	return s == sidSystem || s == sidAdmins
}

// isReparse reports whether fi (from Lstat) is a reparse point: a symbolic
// link, a junction or a mount point, which could lead anywhere.
func isReparse(fi fs.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || ok && d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// localPath reports whether p is on a local drive (C:\…, not a share or a
// device path) and no folder on the way is a reparse point that could lead
// to a share.
func localPath(p string) bool {
	vol := filepath.VolumeName(p)
	if len(vol) != 2 || vol[1] != ':' {
		return false
	}
	for q := filepath.Clean(p); ; {
		fi, err := os.Lstat(q)
		if err != nil || isReparse(fi) {
			return false
		}
		up := filepath.Dir(q)
		if up == q {
			return true
		}
		q = up
	}
}

// IsHelper reports whether p, the process listening on SocketPath as a
// client sees it, is the helper itself: SYSTEM, or (a user cannot read a
// SYSTEM process's token) the KyokaiHelper service's own process. The GUI
// checks it before sending anything to the helper (localapi.RequireServer).
func IsHelper(p localapi.Peer) bool {
	if p.SID == sidSystem {
		return true
	}
	scm, h, err := openService()
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return false
	}
	return st.ProcessId != 0 && st.ProcessId == p.PID
}
