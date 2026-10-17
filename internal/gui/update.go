package gui

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

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
		a.mu.Lock()
		a.quitting = true
		a.mu.Unlock()
		mygo.App.Relaunch()
	}()
}

// appUpdateSettings is the update part of the About card.
func (a *App) appUpdateSettings(c *ui.Context) {
	t := c.Theme()
	if !mygo.Updater.Enabled() {
		ui.Text(c, L.AppUpdateUnavailable()).FontSize(sizeSmall).TextColor(t.TextMuted)
		return
	}
	a.mu.Lock()
	up, busy, updating, progress, p := a.appUpdate, a.busy["app-update"], a.updating, a.updateProgress, a.prefs
	a.mu.Unlock()
	switch {
	case updating:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, L.AppUpdateInstalling(int(progress*100))).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
	case up != nil:
		ui.Row(c).Gap(8).Padding(8, 12).Radius(8).Background(t.Accent.Alpha(0.1)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, L.AppUpdateAvailable(up.Version)).FontSize(sizeSmall).Grow(1)
			if ui.Button(c, L.ActionInstallUpdate()).Disabled(busy).Clicked() {
				a.installApp()
			}
		})
	default:
		if ui.Button(c.Key("check-update"), L.ActionCheckAppUpdate()).Disabled(busy).Clicked() {
			go a.checkApp(true)
		}
	}
	settingRow(c, "app-auto", L.SettingsCheckAppUpdates(), L.SettingsCheckAppUpdatesHint(), func() {
		if ui.Switch(c.Key("app-auto-sw"), &p.CheckAppUpdates).Label(L.SettingsCheckAppUpdates()).Changed() {
			a.setPrefs(p)
		}
	})
}
