package gui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

const sourceURL = "https://github.com/YuanZhe-99/Kyokai"

func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	pg := a.page
	a.mu.Unlock()
	ui.Column(c).Width(sidebarW).FillHeight().Background(t.Surface).Padding(16, 8).Gap(4).Children(func() {
		ui.Text(c, "Kyokai").FontSize(sizeTitle).Bold().PaddingX(10).PaddingY(6)
		link := func(key, label string, p page) {
			row := ui.ButtonBase(c.Key("nav-"+key)).Padding(8, 10).Radius(8).FillWidth().Label(label)
			if pg == p {
				row.Background(t.Accent.Alpha(0.14))
			}
			row.Children(func() { ui.Text(c, label) })
			if row.Clicked() {
				a.mu.Lock()
				a.page = p
				a.mu.Unlock()
			}
		}
		link("home", L.NavHome(), pageHome)
		link("network", L.NavNetwork(), pageNetwork)
		link("ai", L.NavAI(), pageAI)
		link("settings", L.NavSettings(), pageSettings)
	})
}

// engineState shows why there is no status yet: starting, or the error.
func (a *App) engineState(c *ui.Context) bool {
	t := c.Theme()
	a.mu.Lock()
	connected, errText, st := a.connected, a.engineErr, a.status
	a.mu.Unlock()
	if connected && st != nil {
		return true
	}
	card(c).Children(func() {
		if errText == "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, L.EngineStarting()).TextColor(t.TextMuted)
			})
			return
		}
		ui.Text(c, L.EngineDown()).Bold()
		ui.Text(c, errText).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
	})
	return false
}

func (a *App) homeView(c *ui.Context) {
	t := c.Theme()
	if !a.engineState(c) {
		return
	}
	a.mu.Lock()
	st := *a.status
	pw, show := a.password, a.showPassword && a.passwordFor == a.status.PasswordSerial
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.HomeThisDevice()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.HomeDeviceID()).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
			ui.Text(c, groupDigits(st.DeviceID)).FontSize(sizeDeviceID).Bold().FontFeatures("tnum").Selectable()
			if ui.Button(c.Key("copy-id"), L.ActionCopy()).Clicked() {
				mygo.Clipboard.WriteText(st.DeviceID)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
		})
		ui.Divider(c)
		ui.Text(c, L.HomeTemporaryPassword()).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
			shown := strings.Repeat("•", 8)
			if show {
				shown = pw
			}
			ui.Text(c, shown).FontSize(sizeTitle).FontFeatures("tnum").Selectable().Grow(1)
			label := L.ActionShow()
			if show {
				label = L.ActionHide()
			}
			if ui.Button(c.Key("show-pw"), label).Clicked() {
				if show {
					a.mu.Lock()
					a.showPassword = false
					a.mu.Unlock()
				} else {
					a.revealPassword(func(string) { a.showPassword = true })
				}
			}
			if ui.Button(c.Key("copy-pw"), L.ActionCopy()).Clicked() {
				a.revealPassword(func(p string) {
					mygo.Clipboard.WriteText(p)
					a.setToast(L.Copied(), false)
				})
			}
			if ui.Button(c.Key("new-pw"), L.ActionNewPassword()).Clicked() {
				go func() { _, _ = a.request(localapi.RequestPasswordRegenerate) }()
			}
		})
		ui.Text(c, L.HomePasswordHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if note := secretsNote(st.Persistence); note != "" && st.Persistence != "os-store" {
			ui.Text(c, note).FontSize(sizeSmall).TextColor(t.Warning)
		}
	})
	a.connectCard(c)
	a.sessionsCard(c)
}

func secretsNote(p string) string {
	switch p {
	case "os-store":
		return L.SecretsOSStore()
	case "memory-only":
		return L.SecretsMemoryOnly()
	case "unreadable":
		return L.SecretsUnreadable()
	}
	return p
}

func (a *App) settingsView(c *ui.Context) {
	a.generalSettings(c)
	if a.engineState(c) {
		a.runModeCard(c)
		a.identitySettings(c)
		a.accessSettings(c)
		a.storageSettings(c)
		a.diagnostics(c)
	}
	a.aboutSettings(c)
	a.licenses(c)
}

func (a *App) generalSettings(c *ui.Context) {
	card(c).Children(func() {
		ui.Text(c, L.SettingsGeneral()).FontSize(sizeHeading).Bold()
		langs := mygo.App.Languages()
		options := []string{L.LanguageSystem()}
		for _, l := range langs {
			options = append(options, i18n.Autonym(l))
		}
		a.mu.Lock()
		p, login := a.prefs, a.openAtLogin
		a.mu.Unlock()
		cur := options[0]
		if i := slices.Index(langs, p.Language); i >= 0 {
			cur = options[i+1]
		}
		settingRow(c, "lang", L.SettingsLanguage(), "", func() {
			if ui.Select(c.Key("lang-select"), &cur, options).Width(200).Changed() {
				lang := ""
				if i := slices.Index(options, cur); i > 0 {
					lang = langs[i-1]
				}
				mygo.App.SetLocale(lang)
				p.Language = lang
				a.setPrefs(p)
			}
		})
		themes := []string{"system", "light", "dark"}
		themeIdx := max(slices.Index(themes, p.Theme), 0)
		settingRow(c, "theme", L.SettingsTheme(), "", func() {
			if ui.Segmented(c.Key("theme-seg"), &themeIdx, L.ThemeSystem(), L.ThemeLight(), L.ThemeDark()).Changed() {
				p.Theme = themes[themeIdx]
				ApplyTheme(p.Theme)
				a.setPrefs(p)
			}
		})
		settingRow(c, "login", L.SettingsOpenAtLogin(), L.SettingsOpenAtLoginHint(), func() {
			if ui.Switch(c.Key("login-sw"), &login).Label(L.SettingsOpenAtLogin()).Changed() {
				if err := mygo.App.SetOpenAtLogin(login); err != nil {
					a.update(func() { a.setToast(err.Error(), true) })
					return
				}
				a.mu.Lock()
				a.openAtLogin = login
				a.mu.Unlock()
			}
		})
		hide := !p.ShowInCapture
		settingRow(c, "capture", L.SettingsHideFromCapture(), L.SettingsHideFromCaptureHint(), func() {
			if ui.Switch(c.Key("capture-sw"), &hide).Label(L.SettingsHideFromCapture()).Changed() {
				a.setShowInCapture(p, !hide)
			}
		})
		closeIdx := 0
		if p.CloseQuits {
			closeIdx = 1
		}
		settingRow(c, "close", L.SettingsCloseButton(), "", func() {
			if ui.Segmented(c.Key("close-seg"), &closeIdx, L.CloseHide(), L.CloseQuit()).Changed() {
				p.CloseQuits = closeIdx == 1
				a.setPrefs(p)
			}
		})
	})
}

func (a *App) identitySettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	st := *a.status
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.SettingsIdentity()).FontSize(sizeHeading).Bold()
		settingRow(c, "id", L.HomeDeviceID(), "", func() {
			ui.Text(c, groupDigits(st.DeviceID)).FontFeatures("tnum").Selectable()
		})
		a.nameSetting(c, st)
		ui.Text(c, L.IdentityFingerprint())
		ui.Text(c, groupHex(st.Fingerprint)).FontSize(sizeSmall).FontFeatures("tnum").Selectable()
		ui.Text(c, L.IdentityFingerprintHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		a.identityBackup(c)
	})
}

func (a *App) storageSettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	st := *a.status
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.SettingsStorage()).FontSize(sizeHeading).Bold()
		settingRow(c, "dir", L.StorageLocation(), st.StateDir, func() {
			if ui.Button(c.Key("open-dir"), L.ActionOpenFolder()).Clicked() {
				_ = mygo.Shell.OpenPath(st.StateDir)
			}
		})
		settingRow(c, "secrets", L.StorageSecrets(), "", func() {
			col := t.TextMuted
			if st.Persistence != "os-store" {
				col = t.Warning
			}
			ui.Text(c, secretsNote(st.Persistence)).FontSize(sizeSmall).TextColor(col).MaxWidth(360)
		})
	})
}

func (a *App) diagnostics(c *ui.Context) {
	a.mu.Lock()
	st := *a.status
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.SettingsDiagnostics()).FontSize(sizeHeading).Bold()
		settingRow(c, "ver", L.DiagVersion(), "", func() { ui.Text(c, version.Version+" · ABI "+strconv.Itoa(st.ABI)) })
		settingRow(c, "eng", L.DiagEngine(), "", func() {
			ui.Text(c, L.DiagEngineRunning(st.Started.Local().Format(time.DateTime)))
		})
		settingRow(c, "mode", L.DiagRunMode(), "", func() {
			ui.Text(c, []string{L.RunModeUser(), L.RunModeFollow(), L.RunModeBackground()}[a.currentMode()])
		})
		logs := filepath.Join(st.StateDir, "logs")
		settingRow(c, "logs", L.DiagLogs(), logs, func() {
			if ui.Button(c.Key("open-logs"), L.ActionOpenFolder()).Clicked() {
				_ = mygo.Shell.OpenPath(logs)
			}
		})
	})
}

func (a *App) aboutSettings(c *ui.Context) {
	t := c.Theme()
	card(c).Children(func() {
		ui.Text(c, L.SettingsAbout()).FontSize(sizeHeading).Bold()
		ui.Text(c, "Kyokai (鏡界) "+version.Version+" — "+L.AboutTagline())
		ui.Text(c, L.AboutLicense()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if ui.Button(c.Key("source"), L.AboutSource()).Clicked() {
			_ = mygo.Shell.OpenExternal(sourceURL)
		}
		a.appUpdateSettings(c)
	})
}

func (a *App) licenses(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	open := a.showLicenses
	a.mu.Unlock()
	card(c).Children(func() {
		settingRow(c, "lic", L.SettingsLicenses(), L.LicensesHint(), func() {
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
			ui.Text(c, noticesText()).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
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

// nameSetting is the identity card's friendly-name row: the name other
// devices see beside this Device ID once their login succeeded.
func (a *App) nameSetting(c *ui.Context, st localapi.Status) {
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
	settingRow(c, "name", L.SettingsName(), L.SettingsNameHint(), func() {
		if ui.TextInput(c.Key("name-in"), &f.text).Placeholder(placeholder).Width(220).Disabled(busy).Changed() {
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
