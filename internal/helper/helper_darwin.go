package helper

// The macOS helper (doc/en-us/run-modes.md): a LaunchDaemon running the
// engine as root, and a LaunchAgent running the session agent in the
// signed-in user's session and at the login window. Both run the helper's
// own copy of Kyokai Helper.app, the bundle beside the app's engine that
// holds the core library and an engine of its own, so that macOS lists the
// agent's permissions as "Kyokai Helper".
// Adapted from EasyTier MyGo's internal/helper (same author, GPL-3.0,
// relicensed under AGPL-3.0 here).

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// Labels of the launchd jobs: the engine (a daemon) and the session agent.
const (
	DaemonLabel = "com.yuanzhe.kyokai.helper"
	AgentLabel  = "com.yuanzhe.kyokai.agent"
)

// root holds the helper's bundle, state and sockets; administrator-owned.
const root = "/Library/Application Support/Kyokai"

// BundleName is the helper's bundle, in the app's resources/bin and here.
const BundleName = core.HelperBundle

var (
	// BundleDir is the helper's copy of the engine's bundle.
	BundleDir = filepath.Join(root, BundleName)
	// BinDir holds the bundle's kyokai-svc and core library.
	BinDir = filepath.Join(BundleDir, "Contents", "MacOS")
	// StateDir is the helper's state: identity, secrets (the System
	// Keychain), logs; root only.
	StateDir = filepath.Join(root, "state")
	// SocketPath is the helper's local API socket. Every local user can
	// connect; the helper serves only its operator and the kyokai group.
	SocketPath = filepath.Join(root, "ctl.sock")
	// AgentSocket is where the session agent reaches the helper; the
	// helper checks that a connection is its own agent.
	AgentSocket = filepath.Join(root, "agent.sock")
)

func daemonPlist() string { return "/Library/LaunchDaemons/" + DaemonLabel + ".plist" }
func agentPlist() string  { return "/Library/LaunchAgents/" + AgentLabel + ".plist" }

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

// plist writes a launchd job description.
func plist(label string, args []string, extra string) string {
	var b strings.Builder
	for _, a := range args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>AssociatedBundleIdentifiers</key><array><string>com.yuanzhe.kyokai</string></array>
%s</dict>
</plist>
`, label, b.String(), extra)
}

// xmlEscape escapes text for a property list.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// daemonJob runs the engine as root from boot; the run mode (Follow GUI or
// Background) decides when it serves sessions.
func daemonJob() string {
	return plist(DaemonLabel, []string{svcPath(), "run", "--host", "helper"},
		fmt.Sprintf("\t<key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(filepath.Join(StateDir, "helper.log"))))
}

// agentJob runs the session agent in every graphical session (as its
// user) and at the login window (as root), where it captures and injects.
func agentJob() string {
	return plist(AgentLabel, []string{svcPath(), "agent", "--socket", AgentSocket},
		"\t<key>LimitLoadToSessionType</key>\n\t<array><string>Aqua</string><string>LoginWindow</string></array>\n"+
			"\t<key>ProcessType</key><string>Interactive</string>\n")
}

// consoleUID is the user signed in at the screen, or false at the login
// window.
func consoleUID() (uint32, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat("/dev/console", &st); err != nil || st.Uid == 0 {
		return 0, false
	}
	return st.Uid, true
}

// Install copies the app's Kyokai Helper bundle, records the operator and
// loads the daemon and the session agent.
// Inputs: o.From, the app's bin folder holding kyokai-svc and the bundle;
// o.OperatorUID, the installing user.
// Side effects: writes under /Library; starts both launchd jobs.
func Install(o InstallOptions) error {
	if !Elevated() {
		return errors.New("installing the helper needs administrator rights")
	}
	src := filepath.Join(o.From, BundleName)
	if !fileExists(filepath.Join(src, "Contents", "MacOS", "kyokai-svc")) {
		return fmt.Errorf("install: no engine bundle at %s", src)
	}
	bootout()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := placeBundle(src); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return err
	}
	uid := o.OperatorUID
	if err := fsutil.WriteJSON(filepath.Join(StateDir, "helper.json"), Settings{OperatorUID: &uid, AppDir: o.AppDir}); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(daemonPlist(), []byte(daemonJob()), 0o644); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(agentPlist(), []byte(agentJob()), 0o644); err != nil {
		return err
	}
	if err := launchctl("bootstrap", "system", daemonPlist()); err != nil {
		return err
	}
	if uid, ok := consoleUID(); ok {
		return launchctl("bootstrap", fmt.Sprintf("gui/%d", uid), agentPlist())
	}
	return nil
}

// placeBundle copies a bundle into BundleDir, owned by root and writable
// by root only, replacing the one there.
// Notes: ditto keeps the code signature and its extended attributes.
func placeBundle(src string) error {
	tmp := BundleDir + ".new"
	_ = os.RemoveAll(tmp)
	if out, err := exec.Command("ditto", src, tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("copy %s: %v: %s", src, err, out)
	}
	if out, err := exec.Command("chown", "-R", "root:wheel", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("chown: %v: %s", err, out)
	}
	if out, err := exec.Command("chmod", "-R", "go-w", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("chmod: %v: %s", err, out)
	}
	return swapBundle(tmp)
}

// swapBundle puts the bundle at tmp in place of BundleDir.
func swapBundle(tmp string) error {
	old := BundleDir + ".old"
	_ = os.RemoveAll(old)
	_ = os.Rename(BundleDir, old)
	if err := os.Rename(tmp, BundleDir); err != nil {
		_ = os.Rename(old, BundleDir)
		return err
	}
	return os.RemoveAll(old)
}

// bootout stops both jobs wherever they run.
func bootout() {
	_ = exec.Command("launchctl", "bootout", "system/"+DaemonLabel).Run()
	if uid, ok := consoleUID(); ok {
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, AgentLabel)).Run()
	}
	_ = exec.Command("launchctl", "bootout", "loginwindow/"+AgentLabel).Run()
}

// Uninstall stops and removes both jobs and the helper's bundle and
// sockets, and with purge its state (identity and trusted devices).
// Side effects: removes /Library/Application Support/Kyokai once empty.
func Uninstall(purge bool) error {
	if !Elevated() {
		return errors.New("removing the helper needs administrator rights")
	}
	bootout()
	for _, p := range []string{daemonPlist(), agentPlist(), SocketPath, AgentSocket} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(BundleDir); err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(StateDir); err != nil {
			return err
		}
		forgetKey()
	}
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) && !isNotEmpty(err) {
		return err
	}
	return nil
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

// Installed reports whether the helper is installed, from its daemon's
// property list alone: unlike Inspect it starts no process, so the GUI asks
// it as it starts.
func Installed() bool { return fileExists(daemonPlist()) }

// Inspect reports the installed helper with its state and version (`helper
// status`); it needs no rights.
// Side effects: runs launchctl and the installed engine.
func Inspect() Status {
	var s Status
	if !Installed() {
		return s
	}
	s.Installed = true
	s.Active = exec.Command("launchctl", "print", "system/"+DaemonLabel).Run() == nil
	if out, err := exec.Command(svcPath(), "version").Output(); err == nil {
		s.Version = strings.TrimSpace(string(out))
	}
	return s
}

// SelfUpdate replaces the helper's bundle with the app's, when its code
// signature satisfies the installed bundle's designated requirement (the
// same signing identity), and restarts both jobs.
// Inputs: from, the app's bin folder (user-writable); running, this
// helper's version.
// Side effects: replaces BundleDir; this process exits a second later.
// Notes: the bundle is copied into a root-owned folder first and that copy
// is checked and installed, so a file swapped in the app's folder after
// the check is not installed. An ad hoc signed build never satisfies the
// requirement, so it needs the administrator prompt instead.
func SelfUpdate(from, running string) error {
	req, err := designatedRequirement(BundleDir)
	if err != nil {
		return err
	}
	tmp := BundleDir + ".new"
	_ = os.RemoveAll(tmp)
	if out, err := exec.Command("ditto", filepath.Join(from, BundleName), tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("copy: %v: %s", err, out)
	}
	if out, err := exec.Command("chown", "-R", "root:wheel", tmp).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("chown: %v: %s", err, out)
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", "-R="+req, tmp).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("the app's engine is not signed like the helper: %s", strings.TrimSpace(string(out)))
	}
	v, err := bundleVersion(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if version.Older(v, running) {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("bundle %s is older than the helper %s", v, running)
	}
	if err := swapBundle(tmp); err != nil {
		return err
	}
	go func() {
		time.Sleep(time.Second)
		if uid, ok := consoleUID(); ok {
			_ = exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", uid, AgentLabel)).Run()
		}
		os.Exit(0) // launchd starts the daemon again (KeepAlive)
	}()
	return nil
}

// designatedRequirement is the code requirement a bundle's signature
// designates (for a real identity: its identifier and certificate).
func designatedRequirement(bundle string) (string, error) {
	out, err := exec.Command("codesign", "-d", "-r-", bundle).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codesign: %v: %s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if r, ok := strings.CutPrefix(line, "designated => "); ok {
			return r, nil
		}
	}
	return "", errors.New("the helper's bundle designates no requirement")
}

// bundleVersion reads a bundle's CFBundleShortVersionString.
func bundleVersion(bundle string) (string, error) {
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString",
		filepath.Join(bundle, "Contents", "Info.plist")).Output()
	if err != nil {
		return "", fmt.Errorf("bundle version: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Retire removes the helper from inside its own daemon once the app is
// gone: both jobs, the bundle, the state and the sockets. Unloading the
// daemon ends this process, so it comes last.
func Retire() error {
	if uid, ok := consoleUID(); ok {
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, AgentLabel)).Run()
	}
	_ = exec.Command("launchctl", "bootout", "loginwindow/"+AgentLabel).Run()
	for _, p := range []string{daemonPlist(), agentPlist(), SocketPath, AgentSocket} {
		_ = os.Remove(p)
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	forgetKey()
	return launchctl("bootout", "system/"+DaemonLabel)
}

// forgetKey deletes the helper's secrets key from the System keychain,
// where the store keeps it when it runs as root; without its state the key
// unlocks nothing.
func forgetKey() {
	_ = exec.Command("security", "delete-generic-password", "-s", "com.yuanzhe.kyokai",
		"-a", "master-key", "/Library/Keychains/System.keychain").Run()
}

func launchctl(args ...string) error {
	if out, err := exec.Command("launchctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
