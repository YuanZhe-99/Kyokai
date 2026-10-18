package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// Run modes as the Settings card lists them (doc/en-us/run-modes.md).
const (
	modeUser = iota
	modeFollow
	modeBackground
)

// sock is the socket of the engine this GUI uses now: its child, or the
// helper.
func (a *App) sock() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.socket
}

// usesHelper reports whether this GUI talks to the helper.
func (a *App) usesHelper() bool { return a.sock() != a.userSocket }

// currentMode is the run mode the attached engine reports.
func (a *App) currentMode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.status == nil || a.status.RunMode != "helper":
		return modeUser
	case a.status.Background:
		return modeBackground
	}
	return modeFollow
}

// runModeCard is Settings' choice of No privileges, Follow GUI and
// Background service.
func (a *App) runModeCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	busy := a.busy["mode"]
	a.mu.Unlock()
	cur := a.currentMode()
	idx := cur
	card(c).Children(func() {
		ui.Text(c, L.SettingsRunMode()).FontSize(sizeHeading).Bold()
		available := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
		settingRow(c, "mode-choice", L.SettingsRunMode(), L.RunModeHint(), func() {
			if ui.Segmented(c.Key("mode-seg"), &idx, L.RunModeUser(), L.RunModeFollow(), L.RunModeBackground()).
				Disabled(busy || !available).Changed() && idx != cur {
				a.switchMode(cur, idx)
			}
		})
		note := L.RunModeNote()
		switch {
		case !available:
			note = L.RunModeUnavailable()
		case busy:
			note = L.RunModeSwitching()
		}
		ui.Text(c, note).FontSize(sizeSmall).TextColor(t.TextMuted)
		// Direct connections open this computer to the network, so they sit
		// with the run mode; the helper always tries them. The switch
		// applies at once.
		a.mu.Lock()
		net, netBusy := a.net, a.busy["network"]
		a.mu.Unlock()
		if !net.loaded && !netBusy {
			a.loadNetwork()
		}
		helperMode := cur != modeUser
		directHint := L.NetworkDirectHint()
		if helperMode {
			directHint = L.NetworkDirectHelper()
		}
		direct := net.direct || helperMode
		settingRow(c, "net-direct", L.NetworkDirect(), directHint, func() {
			if ui.Switch(c.Key("net-direct-sw"), &direct).Label(L.NetworkDirect()).
				Disabled(busy || netBusy || helperMode || !net.loaded).Changed() {
				a.setDirect(direct)
			}
		})
		// A helper older than the app is updated on request: from a signed
		// build without a prompt, from an unsigned one with one.
		a.mu.Lock()
		helperVersion := ""
		if a.status != nil && a.status.RunMode == "helper" {
			helperVersion = a.status.Version
		}
		a.mu.Unlock()
		if helperVersion != "" && helperVersion != version.Version {
			settingRow(c, "mode-update", L.RunModeOutdated(helperVersion, version.Version), "", func() {
				if ui.Button(c.Key("mode-update-btn"), L.RunModeUpdate()).Disabled(busy).Clicked() {
					a.updateHelper()
				}
			})
		}
	})
}

// helperChanged updates, once per run and without a prompt, a helper older
// than this app: an app update replaces the app's files but not the
// helper's, and an engine of another version cannot reach updated devices.
// It waits while a session is open, since the helper restarts.
// Notes: the caller holds a.mu.
func (a *App) helperChanged(st *localapi.Status) {
	if a.helperUpdated || st.RunMode != "helper" || !version.Older(st.Version, version.Version) {
		return
	}
	for _, s := range st.Sessions {
		if s.State != "closed" && s.State != "failed" {
			return
		}
	}
	a.helperUpdated = true
	a.busy["mode"] = true
	go func() {
		err := a.signedHelperUpdate()
		a.update(func() {
			delete(a.busy, "mode")
			if err == nil {
				a.setToast(L.RunModeUpdated(), false)
			}
		})
	}()
}

// signedHelperUpdate asks the helper to install this app's signed files.
// Side effects: the helper restarts on success; an unsigned build is refused.
func (a *App) signedHelperUpdate() error {
	bin, err := engineBinary()
	if err != nil {
		return err
	}
	_, err = localapi.NewClient(helper.SocketPath).Do(a.ctx, localapi.Request{Type: localapi.RequestHelperUpdate, From: filepath.Dir(bin)})
	return err
}

// updateHelper brings the helper to this app's version.
// Side effects: the helper installs the app's signed files and restarts;
// an unsigned build is installed with one administrator prompt instead.
func (a *App) updateHelper() {
	a.update(func() { a.busy["mode"] = true })
	go func() {
		err := a.signedHelperUpdate()
		if err != nil {
			bin, berr := engineBinary()
			if berr != nil {
				err = berr
			} else {
				err = runPrivileged(append([]string{"install", "--from", filepath.Dir(bin)}, operatorArgs()...)...)
			}
		}
		a.update(func() {
			delete(a.busy, "mode")
			if err != nil {
				a.setToast(err.Error(), true)
			} else {
				a.setToast(L.RunModeUpdated(), false)
			}
		})
	}()
}

// switchMode moves this device between its user engine and the helper.
// Side effects: may install the helper (one administrator prompt) or
// remove it (one more); moves the whole store, so the Device ID, trusted
// devices and settings stay; switches the GUI's socket.
func (a *App) switchMode(from, to int) {
	a.update(func() { a.busy["mode"] = true })
	go func() {
		err := a.doSwitch(from, to)
		a.update(func() {
			delete(a.busy, "mode")
			if err != nil {
				a.setToast(err.Error(), true)
			} else {
				a.setToast(L.RunModeChanged(), false)
			}
		})
	}()
}

func (a *App) doSwitch(from, to int) error {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	user, hlp := localapi.NewClient(a.userSocket), localapi.NewClient(helper.SocketPath)
	background := func() error {
		on := to == modeBackground
		_, err := hlp.Do(ctx, localapi.Request{Type: localapi.RequestBackground, Background: &on})
		return err
	}
	switch {
	case from != modeUser && to != modeUser:
		return background()
	case from == modeUser:
		if !helper.Inspect().Installed {
			bin, err := engineBinary()
			if err != nil {
				return err
			}
			if err := runPrivileged(append([]string{"install", "--from", filepath.Dir(bin)}, operatorArgs()...)...); err != nil {
				return err
			}
		}
		if err := waitFor(ctx, hlp); err != nil {
			return err
		}
		if _, err := localapi.MoveStore(ctx, user, hlp); err != nil {
			return err
		}
		if err := background(); err != nil {
			return err
		}
		a.useSocket(helper.SocketPath, true)
		return nil
	default: // back to No privileges
		if err := a.engine.ensure(ctx, a.userSocket); err != nil {
			return err
		}
		if _, err := localapi.MoveStore(ctx, hlp, user); err != nil {
			return err
		}
		a.useSocket(a.userSocket, false)
		// The helper holds nothing any more: remove it and its state. On
		// macOS it removes itself (no prompt); elsewhere, or when that
		// fails, the administrator removes it.
		if runtime.GOOS == "darwin" && removeHelper(ctx, hlp) {
			return nil
		}
		return runPrivileged("uninstall", "--purge")
	}
}

// removeHelper asks the macOS helper to remove itself, again while it is
// still installed: the engine that gave up the store exits and launchd
// starts it anew, so one request may reach an engine on its way out.
// Returns: whether the helper is gone.
func removeHelper(ctx context.Context, hlp *localapi.Client) bool {
	for range 20 {
		if !helper.Inspect().Installed {
			return true
		}
		_, _ = hlp.Do(ctx, localapi.Request{Type: localapi.RequestHelperRemove})
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	return !helper.Inspect().Installed
}

// useSocket points the GUI at another engine and remembers the choice.
func (a *App) useSocket(socket string, helperMode bool) {
	a.mu.Lock()
	a.socket = socket
	p := a.prefs
	reattach := a.reattach
	a.mu.Unlock()
	p.Helper = helperMode
	a.setPrefs(p)
	if reattach != nil {
		reattach()
	}
}

// waitFor waits until an engine answers on its socket.
func waitFor(ctx context.Context, c *localapi.Client) error {
	for {
		if _, err := c.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New(L.RunModeNoHelper())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// operatorArgs name this user as the helper's operator: by SID on
// Windows, by uid on macOS (where the installer itself runs as root).
func operatorArgs() []string {
	if runtime.GOOS == "darwin" {
		return []string{"--operator-uid", strconv.Itoa(os.Getuid())}
	}
	return []string{"--operator-sid", localapi.CurrentSID()}
}

// runPrivileged runs `kyokai-svc helper <args>` with administrator rights:
// the one prompt of a run-mode change (UAC on Windows, the administrator
// password on macOS).
func runPrivileged(args ...string) error {
	svc, err := engineBinary()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return runAsAdmin(svc, args)
	case "windows":
	default:
		return helper.ErrUnsupported
	}
	// Start-Process joins an -ArgumentList array with bare spaces, which
	// splits paths such as "…\Programs\Kyokai\bin": pass one string that is
	// already quoted for the Windows command line.
	line := []string{"helper"}
	for _, a := range args {
		line = append(line, windowsArg(a))
	}
	ps := fmt.Sprintf("$p = Start-Process -FilePath '%s' -ArgumentList '%s' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode",
		strings.ReplaceAll(svc, "'", "''"), strings.ReplaceAll(strings.Join(line, " "), "'", "''"))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "canceled by the user") || strings.Contains(msg, "cancelled by the user") {
			return errors.New(L.RunModeCancelled())
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// runAsAdmin runs `svc helper <args>` through macOS's administrator
// prompt (as EasyTier MyGo does).
func runAsAdmin(svc string, args []string) error {
	quoted := []string{shellQuote(svc), "helper"}
	for _, a := range args {
		quoted = append(quoted, shellQuote(a))
	}
	script := fmt.Sprintf("do shell script %q with administrator privileges", strings.Join(quoted, " "))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "(-128)") {
			return errors.New(L.RunModeCancelled())
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// windowsArg quotes one argument for a Windows command line so that
// CommandLineToArgvW (and Go's os.Args) reads it back unchanged.
// Adapted from EasyTier MyGo (same author).
func windowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			// Double the backslashes before a quote, then escape the quote.
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	// Backslashes before the closing quote are doubled too.
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}
