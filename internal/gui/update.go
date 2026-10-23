package gui

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// appCheckEvery is how often the automatic app update check runs.
const appCheckEvery = 24 * time.Hour

// appUpdateLoop runs the daily app update check while the GUI runs: the
// first one shortly after launch, when one is due.
func (a *App) appUpdateLoop() {
	if !mygo.Updater.Enabled() {
		return // a development build, or an install location we cannot write
	}
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	for {
		a.mu.Lock()
		due := a.prefs.CheckAppUpdates && time.Since(a.prefs.LastAppCheck) >= appCheckEvery
		a.mu.Unlock()
		if due {
			a.checkApp(false)
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// checkApp asks the GitHub release feed for a newer signed version.
// Inputs: manual is a check the user asked for: it reports "up to date" and
// failures, and shows a version the automatic check already announced.
// Side effects: records the check in the prefs; the automatic check
// notifies once per version.
func (a *App) checkApp(manual bool) {
	a.update(func() { a.busy["app-update"] = true })
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	up, err := mygo.Updater.Check(ctx)
	var announce bool
	a.update(func() {
		delete(a.busy, "app-update")
		if err != nil {
			if manual {
				a.setToast(L.AppUpdateCheckFailed(), true)
			}
			return
		}
		a.prefs.LastAppCheck = time.Now()
		a.appUpdate = up
		switch {
		case up == nil && manual:
			a.setToast(L.AppUpToDate(), false)
		case up != nil && !manual && a.prefs.AnnouncedApp != up.Version:
			a.prefs.AnnouncedApp = up.Version
			announce = true
		}
		_ = savePrefs(a.stateDir, a.prefs)
	})
	if announce {
		a.notify("app-update", L.NotifyAppUpdateTitle(), L.AppUpdateAvailable(up.Version))
	}
}

// installApp downloads the found version, checks its signature, replaces
// the app and relaunches into it.
// Side effects: the engine stops first, because Windows cannot replace
// files a running process uses; on failure it starts again.
func (a *App) installApp() {
	a.mu.Lock()
	up := a.appUpdate
	a.mu.Unlock()
	if up == nil {
		return
	}
	a.update(func() { a.busy["app-update"], a.updating, a.updateProgress = true, true, 0 })
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Minute)
		defer cancel()
		a.engine.stop(a.userSocket)
		err := up.Install(ctx, func(done, total int64) {
			if total > 0 {
				a.update(func() { a.updateProgress = float64(done) / float64(total) })
			}
		})
		if err != nil {
			a.update(func() {
				delete(a.busy, "app-update")
				a.updating = false
				a.setToast(L.AppUpdateInstallFailed(), true)
			})
			return // connectLoop starts the engine again
		}
		a.relaunch()
	}()
}

// updateSection is About & updates' Updates: a new version with Install
// and restart, installing's progress, or Check for updates; then the
// daily check. A copy that cannot update itself says so.
func (a *App) updateSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	up, busy, updating, progress, p := a.appUpdate, a.busy["app-update"], a.updating, a.updateProgress, a.prefs
	a.mu.Unlock()
	a.section(c, "updates", L.SectionUpdates(), nil, func(r *rows) {
		if !mygo.Updater.Enabled() {
			r.row(c, "update", tile(rowDownload), setting{title: L.AppUpdateInstalled(version.Version), about: L.AppUpdateUnavailable()}, nil)
			return
		}
		switch {
		case updating:
			r.row(c, "update", tile(rowDownload), setting{title: L.AppUpdateInstalling(int(progress * 100))}, func() {
				busyMark(c, L.AppUpdateInstalling(int(progress*100)), busySize)
			})
		case up != nil:
			r.row(c, "update", tile(rowDownload), setting{title: L.AppUpdateAvailable(up.Version), about: L.AppUpdateInstalled(version.Version), col: t.Accent}, func() {
				if ui.PrimaryButton(c.Key("install-update"), L.ActionInstallUpdate()).Disabled(busy).Clicked() {
					a.installApp()
				}
			})
		default:
			r.row(c, "update", tile(rowDownload), setting{title: L.AppUpdateInstalled(version.Version)}, func() {
				if ui.Button(c.Key("check-update"), L.ActionCheckAppUpdate()).Disabled(busy).Clicked() {
					go a.checkApp(true)
				}
			})
		}
		r.row(c, "app-auto", tile(rowRefresh), setting{title: L.SettingsCheckAppUpdates(), about: L.SettingsCheckAppUpdatesHint()}, func() {
			if ui.Switch(c.Key("app-auto-sw"), &p.CheckAppUpdates).Label(L.SettingsCheckAppUpdates()).Changed() {
				a.setPrefs(p)
			}
		})
	})
}
