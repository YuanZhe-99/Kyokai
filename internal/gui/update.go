package gui

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// appCheckEvery is how often the automatic app update check runs.
const appCheckEvery = 24 * time.Hour

// autoIdleAfter is how long the computer goes unused before autoIdle
// installs.
const autoIdleAfter = 15 * time.Minute

// Prefs.AutoInstall's modes.
const (
	autoIdle  = "idle"
	autoDaily = "daily"
)

// appUpdateLoop runs the daily app update check while the GUI runs (the
// first one shortly after launch, when one is due; a failed one again an
// hour later) and, each minute, the automatic install (autoInstall).
func (a *App) appUpdateLoop() {
	if !mygo.Updater.Enabled() {
		return // a development build, or an install location we cannot write
	}
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	var tried time.Time
	for {
		a.mu.Lock()
		p := a.prefs
		a.mu.Unlock()
		if (p.CheckAppUpdates || p.AutoInstall != "") && time.Since(p.LastAppCheck) >= appCheckEvery && time.Since(tried) >= time.Hour {
			tried = time.Now()
			a.checkApp(false)
		}
		a.autoInstall()
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// autoFacts are what decides whether an automatic install starts now
// (autoInstallNow).
type autoFacts struct {
	// mode and hour are Prefs.AutoInstall and Prefs.AutoInstallHour.
	mode string
	hour int
	now  time.Time
	// idle is how long the computer has gone unused (asked only for
	// autoIdle).
	idle time.Duration
	// found: an update is known that has not failed to install in this run.
	found bool
	// live: a session, a connection request, an AI task, a live tab or
	// another update is under way, or the engine's state is not known.
	live bool
}

// autoInstallNow reports whether an automatic install starts: never while
// anything is live; autoIdle once the computer has gone unused for
// autoIdleAfter, autoDaily within the chosen hour.
func autoInstallNow(f autoFacts) bool {
	if !f.found || f.live {
		return false
	}
	switch f.mode {
	case autoIdle:
		return f.idle >= autoIdleAfter
	case autoDaily:
		return f.now.Hour() == f.hour
	}
	return false
}

// autoInstall installs a found update without asking when autoInstallNow
// says so.
// Side effects: may stop the engine, replace the app and relaunch it
// (installApp).
func (a *App) autoInstall() {
	a.mu.Lock()
	p, up, st := a.prefs, a.appUpdate, a.status
	f := autoFacts{mode: p.AutoInstall, hour: p.AutoInstallHour, now: time.Now(),
		found: up != nil && up.Version != a.autoFailed,
		live:  a.updating || a.busy["app-update"] || st == nil || st.AI != nil || len(st.Requests) > 0}
	if st != nil {
		for _, s := range st.Sessions {
			if s.State != "closed" && s.State != "failed" {
				f.live = true
			}
		}
	}
	a.mu.Unlock()
	if f.mode == "" || !f.found || f.live {
		return
	}
	for _, t := range a.allTabs() {
		if t.live() {
			return
		}
	}
	if f.mode == autoIdle {
		f.idle = mygo.Power.IdleTime()
	}
	if autoInstallNow(f) {
		a.installApp(true)
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
// Inputs: auto is an automatic install (autoInstall): a failure is not
// retried in this run, and a hidden window stays hidden after the relaunch.
// Side effects: the engine stops first, because Windows cannot replace
// files a running process uses; on failure it starts again.
func (a *App) installApp(auto bool) {
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
				if auto {
					a.autoFailed = up.Version
				}
				a.setToast(L.AppUpdateInstallFailed(), true)
			})
			return // connectLoop starts the engine again
		}
		if auto {
			a.mu.Lock()
			p := a.prefs
			p.ResumeInTray = !a.visible
			a.mu.Unlock()
			_ = a.setPrefs(p)
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
					a.installApp(false)
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
		modes, labels := []string{"", autoIdle, autoDaily}, []string{L.AutoInstallOff(), L.AutoInstallIdle(), L.AutoInstallDaily()}
		mode := labels[max(slices.Index(modes, p.AutoInstall), 0)]
		r.row(c, "auto-install", tile(rowDownload), setting{title: L.SettingsAutoInstall(), about: L.SettingsAutoInstallHint()}, func() {
			ui.Row(c.Key("auto-install-ctl")).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Select(c.Key("auto-install-sel"), &mode, labels).Width(240).Changed() {
					p.AutoInstall = modes[slices.Index(labels, mode)]
					a.setPrefs(p)
				}
				if p.AutoInstall == autoDaily {
					hours := make([]string, 24)
					for h := range hours {
						hours[h] = fmt.Sprintf("%02d:00", h)
					}
					hour := hours[min(max(p.AutoInstallHour, 0), 23)]
					if ui.Select(c.Key("auto-install-hour"), &hour, hours).Width(96).Changed() {
						p.AutoInstallHour = slices.Index(hours, hour)
						a.setPrefs(p)
					}
				}
			})
		})
	})
}
