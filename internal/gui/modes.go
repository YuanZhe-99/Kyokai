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

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
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

// runModeSection is the Run mode page's choice of No privileges, Follow
// Kyokai and Background service: each a row, the one in use marked, the
// others with Switch; while switching, its progress; a helper behind the
// app, with Update.
func (a *App) runModeSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	busy := a.busy["mode"]
	a.mu.Unlock()
	cur := a.currentMode()
	available := runtime.GOOS == "windows" || runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	a.section(c, "run-mode", L.RunModeWhere(), nil, func(r *rows) {
		for mode, m := range []struct {
			icon         *ui.SVG
			title, about string
		}{
			{rowUser, L.RunModeUser(), L.RunModeUserAbout()},
			{navSecurity, L.RunModeFollow(), L.RunModeFollowAbout()},
			{rowServer, L.RunModeBackground(), L.RunModeBackgroundAbout()},
		} {
			s := setting{title: m.title, about: m.about}
			if mode == modeFollow {
				s.more = L.RunModeNote()
			}
			r.row(c, "mode-"+strconv.Itoa(mode), tile(m.icon), s, func() {
				if mode == cur {
					ui.Text(c, L.RunModeInUse()).FontSize(sizeSmall).TextColor(textTone(t, t.Success))
					return
				}
				if ui.Button(c.Key("mode-use-"+strconv.Itoa(mode)), L.RunModeSwitch()).Disabled(busy || !available).Clicked() {
					a.switchMode(cur, mode)
				}
			})
		}
		switch {
		case !available:
			r.block(c, "mode-unavailable", func() { ui.Text(c, L.RunModeUnavailable()).FontSize(sizeSmall).TextColor(t.TextMuted) })
		case busy:
			r.block(c, "mode-busy", func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					busyMark(c, L.RunModeSwitching(), busySize)
					ui.Text(c, L.RunModeSwitching()).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
			})
		}
		// A helper behind the app is updated on request too: from a signed
		// build without a prompt, from an unsigned one with one.
		if behind := a.outdatedHelper(); behind.helper != "" {
			r.row(c, "mode-update", tile(rowDownload), setting{title: L.RunModeOutdated(behind.helper, behind.app)}, func() {
				if ui.Button(c.Key("mode-update-btn"), L.RunModeUpdate()).Disabled(busy).Clicked() {
					a.updateHelper()
				}
			})
		}
	})
}

// helperBehind reports whether the attached engine is a helper behind this
// app: an older version, or the same version from another build (a test
// build; its wire protocol may differ). A newer helper is left alone.
// Inputs: build is this app's build (version.Build).
func helperBehind(st *localapi.Status, build string) bool {
	if st == nil || st.RunMode != "helper" || version.Older(version.Version, st.Version) {
		return false
	}
	return version.Older(st.Version, version.Version) || build != "" && st.Build != build
}

// helperGap is a helper behind this app and the app, as people read them:
// versions, with their builds when the versions are the same. Empty when
// the helper is not behind.
type helperGap struct{ helper, app string }

// outdatedHelper is the attached helper's gap to this app, if it is behind.
func (a *App) outdatedHelper() helperGap {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !helperBehind(a.status, version.Build()) {
		return helperGap{}
	}
	if a.status.Version != version.Version {
		return helperGap{a.status.Version, version.Version}
	}
	short := func(b string) string {
		if len(b) > 7 {
			b = b[:7]
		}
		return b
	}
	return helperGap{a.status.Version + " (" + short(a.status.Build) + ")", version.Version + " (" + short(version.Build()) + ")"}
}

// helperChanged updates, once per run and without a prompt, a helper
// behind this app (helperBehind): an app update replaces the app's files but
// not the helper's, and an engine of another build may not reach updated
// devices.
// It waits while a session is open, since the helper restarts.
// Notes: the caller holds a.mu.
func (a *App) helperChanged(st *localapi.Status) {
	if a.helperUpdated || !helperBehind(st, version.Build()) {
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
		bin, err := engineBinary()
		if err != nil {
			return err
		}
		if !helper.Installed() {
			if err := runPrivileged(append([]string{"install", "--from", filepath.Dir(bin)}, operatorArgs()...)...); err != nil {
				return err
			}
		}
		if err := waitFor(ctx, hlp); err != nil {
			return err
		}
		// Both engines must be the same build to move the whole store: an
		// older helper is brought up to this app first.
		if st, err := hlp.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil && helperBehind(st.Status, version.Build()) {
			if err := a.signedHelperUpdate(); err != nil {
				if err := runPrivileged(append([]string{"install", "--from", filepath.Dir(bin)}, operatorArgs()...)...); err != nil {
					return err
				}
			}
			if err := waitForBuild(ctx, hlp); err != nil {
				return err
			}
		}
		if err := a.move(ctx, user, hlp, "helper"); err != nil {
			return err
		}
		// The GUI follows the identity before anything else can fail.
		if err := a.useSocket(helper.SocketPath, true); err != nil {
			a.update(func() { a.setToast(err.Error(), true) })
		}
		if err := background(); err != nil {
			a.update(func() { a.setToast(err.Error(), true) })
		}
		return nil
	default: // back to No privileges
		// The user engine may be parked (its store moved out): it starts
		// and takes the store back.
		if err := a.engine.ensure(ctx, a.userSocket); err != nil {
			return err
		}
		if err := a.move(ctx, hlp, user, "user"); err != nil {
			return err
		}
		if err := a.useSocket(a.userSocket, false); err != nil {
			a.update(func() { a.setToast(err.Error(), true) })
		}
		// The helper holds nothing any more: it removes itself, without a
		// prompt; only when that fails does the administrator remove it.
		if removeHelper(ctx, hlp) {
			return nil
		}
		return runPrivileged("uninstall", "--purge")
	}
}

// move moves the whole store between this device's engines; when the
// destination holds another identity with its own settings, the person
// chooses whether it goes.
// Inputs: dest is "helper" or "user", for the source's marker.
func (a *App) move(ctx context.Context, from, to *localapi.Client, dest string) error {
	_, err := localapi.MoveStore(ctx, from, to, localapi.MoveOptions{To: dest})
	if !errors.Is(err, localapi.ErrOtherIdentity) {
		return err
	}
	if !a.confirm(L.RunModeReplaceIdentity(), L.RunModeReplace()) {
		return errors.New(L.RunModeKept())
	}
	_, err = localapi.MoveStore(ctx, from, to, localapi.MoveOptions{To: dest, Replace: true})
	return err
}

// waitForBuild waits until the helper restarted on this app's build.
func waitForBuild(ctx context.Context, hlp *localapi.Client) error {
	for {
		if st, err := hlp.Do(ctx, localapi.Request{Type: localapi.RequestStatus}); err == nil && !helperBehind(st.Status, version.Build()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// removeHelper asks the helper to remove itself, again while it is still
// installed: the engine that gave up the store exits and the service
// manager starts it anew, so one request may reach an engine on its way
// out.
// Returns: whether the helper is gone.
func removeHelper(ctx context.Context, hlp *localapi.Client) bool {
	for range 20 {
		if !helper.Installed() {
			return true
		}
		_, _ = hlp.Do(ctx, localapi.Request{Type: localapi.RequestHelperRemove})
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	return !helper.Installed()
}

// useSocket points the GUI at another engine and remembers the choice; the
// pages forget what the previous engine told them.
// Returns: an error when the choice could not be saved (the next start
// may then use the other engine).
func (a *App) useSocket(socket string, helperMode bool) error {
	a.mu.Lock()
	a.socket = socket
	p := a.prefs
	reattach := a.reattach
	a.net = netState{}
	a.ai = aiState{}
	a.soundDevices, a.soundDevicesLoaded = nil, false
	a.mu.Unlock()
	p.Helper = helperMode
	err := a.setPrefs(p)
	if reattach != nil {
		reattach()
	}
	return err
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
// Windows, by uid on macOS and Linux (where the installer itself runs as
// root). An AppImage, whose folder is a mount that goes when it quits,
// gives the helper no app folder to watch (run-modes.md, "Self-disable").
func operatorArgs() []string {
	if runtime.GOOS == "linux" && os.Getenv("APPIMAGE") != "" {
		return []string{"--operator-uid", strconv.Itoa(os.Getuid()), "--app-dir="}
	}
	if runtime.GOOS != "windows" {
		return []string{"--operator-uid", strconv.Itoa(os.Getuid())}
	}
	return []string{"--operator-sid", localapi.CurrentSID()}
}

// runPrivileged runs `kyokai-svc helper <args>` with administrator rights:
// the one prompt of a run-mode change (UAC on Windows, the administrator
// password on macOS, polkit's on Linux).
func runPrivileged(args ...string) error {
	svc, err := engineBinary()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return runAsAdmin(svc, args)
	case "linux":
		return runPkexec(svc, args)
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

// runPkexec runs `svc helper <args>` as root through pkexec, which asks
// through the desktop's polkit agent.
// Notes: pkexec exits 126 when the person dismissed the prompt and 127
// when they could not authenticate.
func runPkexec(svc string, args []string) error {
	out, err := exec.Command("pkexec", append([]string{svc, "helper"}, args...)...).CombinedOutput()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && (exit.ExitCode() == 126 || exit.ExitCode() == 127) {
		return errors.New(L.RunModeCancelled())
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
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

// movedToHelper reports whether this profile's user engine gave its store
// to the helper (the marker `moved.json` in its state folder says so).
func movedToHelper(stateDir string) bool {
	var m struct {
		To string `json:"to"`
	}
	ok, err := fsutil.ReadJSON(filepath.Join(stateDir, "moved.json"), &m)
	return ok && err == nil && m.To == "helper"
}

// movedCard is shown while this profile's own engine is parked: its
// identity and settings moved to Kyokai Helper, which is not there. The
// person installs the helper again (its state may still be there) or starts
// over here with a new identity.
func (a *App) movedCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	busy := a.busy["mode"]
	a.mu.Unlock()
	emptyState(c, "moved", func() { noticeIcon(c, rowServer, t.Warning) }, L.RunModeMovedTitle(), L.RunModeMovedHint(), func() {
		if ui.PrimaryButton(c.Key("moved-install"), L.RunModeMovedInstall()).Disabled(busy).Clicked() {
			a.reinstallHelper()
		}
		if ui.Button(c.Key("moved-start-over"), L.RunModeMovedStartOver()).Disabled(busy).Clicked() {
			go func() {
				if !a.confirm(L.RunModeMovedStartOverConfirm(), L.RunModeMovedStartOver()) {
					return
				}
				if _, err := a.requestWith(localapi.Request{Type: localapi.RequestStoreForgetMove}); err != nil {
					a.update(func() { a.setToast(err.Error(), true) })
				}
			}()
		}
		if busy {
			busyMark(c, L.RunModeSwitching(), busySize)
		}
	})
}

// reinstallHelper installs Kyokai Helper again (one administrator prompt)
// and attaches to it.
func (a *App) reinstallHelper() {
	a.update(func() { a.busy["mode"] = true })
	go func() {
		err := func() error {
			bin, err := engineBinary()
			if err != nil {
				return err
			}
			if err := runPrivileged(append([]string{"install", "--from", filepath.Dir(bin)}, operatorArgs()...)...); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
			defer cancel()
			if err := waitFor(ctx, localapi.NewClient(helper.SocketPath)); err != nil {
				return err
			}
			return a.useSocket(helper.SocketPath, true)
		}()
		a.update(func() {
			delete(a.busy, "mode")
			if err != nil {
				a.setToast(err.Error(), true)
			}
		})
	}()
}
