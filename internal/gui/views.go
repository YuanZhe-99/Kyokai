package gui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

const sourceURL = "https://github.com/YuanZhe-99/Kyokai"

// engineState shows, in place of a page, why there is no status yet:
// Starting…, or that the engine is not running and why; a profile parked
// by a move to the helper shows its own (movedCard).
// Returns: whether there is a status to show the page with.
func (a *App) engineState(c *ui.Context) bool {
	t := c.Theme()
	a.mu.Lock()
	connected, errText, st := a.connected, a.engineErr, a.status
	a.mu.Unlock()
	if connected && st != nil && st.Moved == "" {
		return true
	}
	switch {
	case connected && st != nil:
		a.movedCard(c)
	case errText == "":
		emptyState(c, "engine-starting", func() { busyMark(c, L.EngineStarting(), busySizeLarge) }, "", L.EngineStarting(), nil)
	default:
		emptyState(c, "engine-down", func() { noticeIcon(c, rowPower, t.Danger) }, L.EngineDown(), errText, nil)
	}
	return false
}

// emptyState is what a pane or page shows in place of its content,
// centred: a mark, a title and the words under it ("" for none, the words
// selectable), then its actions (nil for none).
func emptyState(c *ui.Context, key string, mark func(), title, about string, actions func()) {
	t := c.Theme()
	ui.Column(c.Key(key)).FillWidth().Padding(emptyTop, 24, 24, 24).Gap(10).AlignItems(ui.Center).Children(func() {
		mark()
		if title != "" {
			ui.Text(c, title).FontSize(sizeHeading).FontWeight(600).TextAlign(ui.Center).MaxWidth(emptyW)
		}
		if about != "" {
			ui.Text(c, about).FontSize(sizeSmall).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(emptyW).Selectable()
		}
		if actions != nil {
			ui.Row(c).Gap(8).Margin(6, 0, 0, 0).Children(actions)
		}
	})
}

func secretsNote(p string) string {
	switch p {
	case "os-store":
		return L.SecretsOSStore()
	case "memory-only":
		return L.SecretsMemoryOnly()
	case "unreadable":
		return L.SecretsUnreadable()
	case "denied":
		return L.SecretsDenied()
	}
	return p
}

// securityView is the Security page: the device identity, protected mode,
// and what Kyokai lets others see and keeps on this computer.
func (a *App) securityView(c *ui.Context) {
	settingsPage(c, L.NavSecurityPrivacy(), L.SettingsAboutSecurity(), func() {
		if !a.engineState(c) {
			return
		}
		a.identitySettings(c)
		a.keysSettings(c)
		a.privacySettings(c)
	})
}

// generalView is the General page: the app's language and look, and how it
// starts and closes.
func (a *App) generalView(c *ui.Context) {
	a.mu.Lock()
	p, login := a.prefs, a.openAtLogin
	a.mu.Unlock()
	settingsPage(c, L.SettingsGeneral(), L.SettingsAboutGeneral(), func() {
		a.section(c, "look", L.SectionLook(), nil, func(r *rows) {
			r.row(c, "lang", tile(rowGlobe), setting{title: L.SettingsLanguage(), about: L.SettingsLanguageAbout()}, func() {
				a.languageSelect(c, "lang-select", 200)
			})
			themes := []string{"system", "light", "dark"}
			themeIdx := max(slices.Index(themes, p.Theme), 0)
			r.row(c, "theme", tile(shellAuto), setting{title: L.SettingsTheme(), about: L.SettingsThemeAbout()}, func() {
				if ui.Segmented(c.Key("theme-seg"), &themeIdx, L.ThemeSystem(), L.ThemeLight(), L.ThemeDark()).Changed() {
					p.Theme = themes[themeIdx]
					ApplyTheme(p.Theme)
					a.setPrefs(p)
				}
			})
		})
		a.section(c, "start", L.SectionStart(), nil, func(r *rows) {
			r.row(c, "login", tile(rowPower), setting{title: L.SettingsOpenAtLogin(), about: L.SettingsOpenAtLoginHint()}, func() {
				if ui.Switch(c.Key("login-sw"), &login).Label(L.SettingsOpenAtLogin()).Changed() {
					a.setOpenAtLogin(login)
				}
			})
			tray := !p.ShowAtLogin
			r.row(c, "login-tray", tile(rowTray), setting{title: L.SettingsStartHidden(), about: L.SettingsStartHiddenAbout()}, func() {
				if ui.Switch(c.Key("login-tray-sw"), &tray).Label(L.SettingsStartHidden()).Changed() {
					p.ShowAtLogin = !tray
					a.setPrefs(p)
				}
			})
			closeIdx := 0
			if p.CloseQuits {
				closeIdx = 1
			}
			r.row(c, "close", tile(paneClose), setting{title: L.SettingsCloseButton(), about: L.SettingsCloseButtonAbout()}, func() {
				if ui.Segmented(c.Key("close-seg"), &closeIdx, L.CloseHide(), L.CloseQuit()).Changed() {
					p.CloseQuits = closeIdx == 1
					a.setPrefs(p)
				}
			})
			r.row(c, "tour", tile(navAbout), setting{title: L.TourAgain(), about: L.TourAgainAbout()}, func() {
				if ui.Button(c.Key("tour-again"), L.TourAgainShow()).Clicked() {
					a.openTour()
				}
			})
		})
	})
}

// runModeView is the Run mode page: where this device runs
// (runModeSection).
func (a *App) runModeView(c *ui.Context) {
	settingsPage(c, L.SettingsRunMode(), L.SettingsAboutRunMode(), func() {
		if a.engineState(c) {
			a.runModeSection(c)
		}
	})
}

// aboutView is the About & updates page: Kyokai, its license and the open
// source licenses; updates; and diagnostics.
func (a *App) aboutView(c *ui.Context) {
	settingsPage(c, L.NavAboutUpdates(), L.SettingsAboutAbout(), func() {
		a.aboutSection(c)
		a.updateSection(c)
		if a.engineState(c) {
			a.diagnostics(c)
		}
	})
}

// identitySettings is the Security & privacy page's Device identity: the
// Device ID, the friendly name, the fingerprint, back up or restore, and
// reset.
func (a *App) identitySettings(c *ui.Context) {
	a.mu.Lock()
	st := *a.status
	a.mu.Unlock()
	a.section(c, "identity", L.SettingsIdentity(), nil, func(r *rows) {
		r.row(c, "id", tile(rowID), setting{title: L.HomeDeviceID(), about: L.IdentityIDAbout()}, func() {
			ui.Text(c, groupDigits(st.DeviceID)).FontFeatures("tnum").Selectable()
			if ui.Button(c.Key("id-copy"), L.ActionCopy()).Clicked() {
				mygo.Clipboard.WriteText(st.DeviceID)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
		})
		a.nameSetting(c, r, st)
		r.row(c, "fingerprint", tile(navSecurity), setting{title: L.IdentityFingerprint(), about: groupHex(st.Fingerprint), more: L.IdentityFingerprintHint()}, func() {
			if ui.Button(c.Key("fp-copy"), L.ActionCopy()).Clicked() {
				mygo.Clipboard.WriteText(st.Fingerprint)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
		})
		a.identityBackup(c, r)
	})
}

// privacySettings is the Security & privacy page's Privacy: Hide Kyokai
// from screen capture, No logs, how secrets are kept, and protected mode.
func (a *App) privacySettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	st, p := *a.status, a.prefs
	a.mu.Unlock()
	a.section(c, "privacy", L.SettingsPrivacy(), nil, func(r *rows) {
		hide := !p.ShowInCapture
		r.row(c, "capture", tile(rowEyeOff), setting{title: L.SettingsHideFromCapture(), about: L.HideFromCaptureAbout(), more: L.SettingsHideFromCaptureHint()}, func() {
			if ui.Switch(c.Key("capture-sw"), &hide).Label(L.SettingsHideFromCapture()).Changed() {
				if hide {
					a.setShowInCapture(p, false)
					return
				}
				// Shown, Kyokai's windows take the input of the people
				// controlling this device too: ask first (a dialog: off the
				// main thread).
				go func() {
					if a.confirm(L.SettingsHideFromCaptureConfirm(), L.SettingsHideFromCaptureOff()) {
						a.mu.Lock()
						p := a.prefs
						a.mu.Unlock()
						a.setShowInCapture(p, true)
					}
				}()
			}
		})
		off := p.NoLogs
		logs := setting{title: L.NoLogsTitle(), about: L.NoLogsAbout(), more: L.NoLogsHint()}
		if off && !st.LogsOff {
			logs.about, logs.col = L.NoLogsEngineBehind(), t.Warning
		}
		r.row(c, "no-logs", tile(rowLog), logs, func() {
			if ui.Switch(c.Key("no-logs-sw"), &off).Label(L.NoLogsTitle()).Changed() {
				if !off {
					a.setNoLogs(p, false)
					return
				}
				// A dialog: off the main thread.
				go func() {
					if a.confirm(L.NoLogsConfirm(), L.NoLogsTurnOn()) {
						a.mu.Lock()
						p := a.prefs
						a.mu.Unlock()
						a.setNoLogs(p, true)
					}
				}()
			}
		})
		secrets := setting{title: L.StorageSecrets(), about: secretsNote(st.Persistence)}
		if st.Persistence != "os-store" {
			secrets.col = t.Warning
		}
		r.row(c, "secrets", tile(rowLock), secrets, nil)
		a.protectedSetting(c, r)
	})
}

// diagnostics is About & updates' Diagnostics: the version and ABI, when
// the engine started, the run mode, the data folder, and the logs (Open
// folder, Save logs…) or that there are none.
func (a *App) diagnostics(c *ui.Context) {
	a.mu.Lock()
	st := *a.status
	busy := a.busy["logs"]
	a.mu.Unlock()
	a.section(c, "diagnostics", L.SettingsDiagnostics(), nil, func(r *rows) {
		r.row(c, "ver", tile(navAbout), setting{title: L.DiagVersion(), about: version.Version + " · ABI " + strconv.Itoa(st.ABI)}, nil)
		r.row(c, "eng", tile(rowServer), setting{title: L.DiagEngine(), about: L.DiagEngineRunning(st.Started.Local().Format(time.DateTime))}, nil)
		r.row(c, "mode", tile(navSecurity), setting{title: L.DiagRunMode(), about: []string{L.RunModeUser(), L.RunModeFollow(), L.RunModeBackground()}[a.currentMode()]}, nil)
		r.row(c, "dir", tile(rowFolder), setting{title: L.StorageLocation(), about: st.StateDir}, func() {
			if ui.Button(c.Key("open-dir"), L.ActionOpenFolder()).Clicked() {
				_ = mygo.Shell.OpenPath(st.StateDir)
			}
		})
		if st.LogsOff {
			r.row(c, "logs", tile(rowLog), setting{title: L.DiagLogs(), about: L.DiagLogsOff()}, nil)
			return
		}
		logs := filepath.Join(st.StateDir, "logs")
		r.row(c, "logs", tile(rowLog), setting{title: L.DiagLogs(), about: logs}, func() {
			if ui.Button(c.Key("open-logs"), L.ActionOpenFolder()).Clicked() {
				_ = mygo.Shell.OpenPath(logs)
			}
			if ui.Button(c.Key("save-logs"), L.ActionSaveLogs()).Disabled(busy).Clicked() {
				go a.saveLogs(st.StateDir)
			}
		})
	})
}

// saveLogs asks where to save, then writes there the engine's logs (the
// helper's and its agent's in the helper modes, which only administrators
// open) and, when the engine keeps them elsewhere, this GUI's own.
// Side effects: writes a file readable only by this user.
func (a *App) saveLogs(engineDir string) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent: a.window, Title: L.ActionSaveLogs(), DefaultPath: "kyokai-logs-" + time.Now().Format("20060102-150405") + ".txt",
		Filters: []mygo.FileFilter{{Name: L.LogsFileKind(), Extensions: []string{"txt"}}},
	})
	if err != nil || path == "" {
		return
	}
	a.update(func() { a.busy["logs"] = true })
	resp, err := a.requestWith(localapi.Request{Type: localapi.RequestLogsRead})
	var refused *localapi.RequestError
	var werr error
	if err == nil {
		text := resp.Document
		if filepath.Clean(engineDir) != filepath.Clean(a.stateDir) {
			text += engine.ReadLogs(a.stateDir)
		}
		werr = os.WriteFile(path, []byte(text), 0o600)
	}
	a.update(func() {
		delete(a.busy, "logs")
		switch {
		case errors.As(err, &refused):
			a.setToast(refused.Message, true)
		case err != nil:
			a.setToast(L.EngineDown(), true)
		case werr != nil:
			a.setToast(werr.Error(), true)
		default:
			a.setToast(L.LogsSaved(), false)
		}
	})
}

// aboutSection is About & updates' Kyokai: the version with the source
// code, and the license with the open source notices it ships (shown on
// request).
func (a *App) aboutSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	open := a.showLicenses
	a.mu.Unlock()
	a.section(c, "kyokai", "Kyokai", nil, func(r *rows) {
		r.row(c, "version", tile(appMark), setting{title: "Kyokai (鏡界) " + version.Version, about: L.AboutTagline()}, func() {
			if ui.Button(c.Key("source"), L.AboutSource()).Clicked() {
				_ = mygo.Shell.OpenExternal(sourceURL)
			}
		})
		r.row(c, "lic", tile(rowLog), setting{title: L.SettingsLicenses(), about: L.AboutLicense()}, func() {
			label := L.ActionShow()
			if open {
				label = L.ActionHide()
			}
			if ui.Button(c.Key("lic-toggle"), label).Clicked() {
				a.mu.Lock()
				a.showLicenses = !open
				a.mu.Unlock()
			}
		})
		if open {
			r.block(c, "lic-text", func() { ui.Text(c, noticesText()).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable() })
		}
	})
}

// noticesText reads the shipped THIRD_PARTY_NOTICES.md (resources/licenses
// in a package; the repository root in development).
func noticesText() string {
	var paths []string
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		paths = append(paths, filepath.Join(res, "licenses", "THIRD_PARTY_NOTICES.md"))
	}
	paths = append(paths, "THIRD_PARTY_NOTICES.md")
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			return string(data)
		}
	}
	return sourceURL
}

func (a *App) toastView(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	msg, isErr, at := a.toast, a.toastErr, a.toastAt
	a.mu.Unlock()
	if msg == "" || time.Since(at) > 4*time.Second {
		return
	}
	bg, fg := t.Surface, t.Text
	if isErr {
		bg, fg = t.Danger, ui.Hex("#ffffff")
	}
	ui.Row(c).FillWidth().Padding(10, 16).Background(bg).Border(1, t.Border).Children(func() {
		ui.Text(c, msg).TextColor(fg)
	})
}

// ApplyTheme sets the appearance: "system", "light" or "dark".
func ApplyTheme(theme string) {
	switch theme {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// nameForm is the friendly-name field while it is edited.
type nameForm struct {
	text    string
	editing bool
}

// nameSetting is Device identity's friendly-name row: the name other
// devices see beside this Device ID once their login succeeded.
func (a *App) nameSetting(c *ui.Context, r *rows, st localapi.Status) {
	a.mu.Lock()
	f, busy := a.name, a.busy["name"]
	a.mu.Unlock()
	if !f.editing {
		f.text = st.Name
	}
	// Empty follows the computer's name, which the field then suggests.
	placeholder := ""
	if st.Name == "" {
		placeholder = st.About.Name
	}
	r.row(c, "name", tile(rowTag), setting{title: L.SettingsName(), about: L.SettingsNameAbout(), more: L.SettingsNameHint()}, func() {
		if ui.TextInput(c.Key("name-in"), &f.text).Placeholder(placeholder).Width(200).Disabled(busy).Changed() {
			a.mu.Lock()
			a.name = nameForm{text: f.text, editing: true}
			a.mu.Unlock()
		}
		if ui.Button(c.Key("name-save"), L.ActionSave()).Disabled(busy || !f.editing).Clicked() {
			a.saveName(f.text)
		}
	})
}

// saveName stores the friendly name ("" follows the computer's name).
// Side effects: asks the engine; the status push shows the new name.
func (a *App) saveName(name string) {
	a.mu.Lock()
	a.busy["name"] = true
	a.mu.Unlock()
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestNameSet, Name: &name})
		a.update(func() {
			delete(a.busy, "name")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			default:
				a.name = nameForm{}
				a.setToast(L.NameSaved(), false)
			}
		})
	}()
}
