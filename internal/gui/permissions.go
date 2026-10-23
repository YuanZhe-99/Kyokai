package gui

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// permissionPanes are System Settings' Privacy & Security panes per
// permission (macOS).
var permissionPanes = map[string]string{
	"screen":           "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"accessibility":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
	"input-monitoring": "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent",
	"microphone":       "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone",
}

// permissionsMissing reports whether a privacy permission Kyokai needs is
// missing (macOS); false where the system has none.
func permissionsMissing(p *localapi.Permissions) bool {
	return p != nil && !(p.Screen && p.Accessibility && p.InputMonitoring && p.Keychain)
}

// permissionsCard shows on Home, while one is missing, the system's privacy
// permissions Kyokai needs (macOS): under its title as Home's sections
// have one, a card of the rows Access lists them in (privacyList), each
// Allowed or with Allow (Access lists them always: privacySection).
func (a *App) permissionsCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var p *localapi.Permissions
	if a.status != nil {
		p = a.status.Permissions
	}
	helperMode := a.status != nil && a.status.RunMode == "helper"
	a.mu.Unlock()
	if p == nil || !permissionsMissing(p) {
		return
	}
	ui.Text(c, L.PermissionsTitle()).FontSize(sizeHeading).Bold().Padding(14, 2, 0, 2)
	ui.Column(c.Key("home-perms")).FillWidth().Radius(cardRadius).Border(1, t.Border).Background(t.Surface).Clip().Children(func() {
		a.privacyList(c, &rows{a: a}, "home-perm", p, helperMode)
	})
}

// askPermission asks the system for a permission. In No privileges the app
// asks for a privacy permission itself, so that a prompt is the app's own;
// the grant covers the engine, which macOS counts as part of the app. In
// the helper modes the engine asks its session agent, Kyokai Helper, whose
// own grants count. For the Keychain, the engine opens its store again and
// macOS asks. With open it also shows a privacy permission's pane in System
// Settings, where a permission asked for before is granted.
func (a *App) askPermission(name string, open bool) {
	go func() {
		a.mu.Lock()
		helperMode := a.status != nil && a.status.RunMode == "helper"
		a.mu.Unlock()
		_, pane := permissionPanes[name]
		if lib, err := coreLib(); pane && !helperMode && err == nil {
			_, _ = lib.PrivacyRequest(name)
		} else {
			_, _ = a.requestWith(localapi.Request{Type: localapi.RequestPermission, Permission: name})
		}
		if open {
			if pane, ok := permissionPanes[name]; ok {
				_ = mygo.Shell.OpenExternal(pane)
			}
		}
	}()
}

// reopen quits Kyokai and starts it again, so that the engine started anew
// sees a Screen Recording grant.
func (a *App) reopen() {
	go a.relaunch()
}

// markAsked notes that this run asked for a privacy permission (the
// Keychain needs no restart). Notes: the caller holds a.mu.
func (a *App) markAsked(name string) {
	if _, privacy := permissionPanes[name]; !privacy {
		return
	}
	if a.asked == nil {
		a.asked = map[string]bool{}
	}
	a.asked[name] = true
}

// restartAgent starts the helper's session agent anew, which then has the
// privacy permissions granted to Kyokai Helper since it started.
func (a *App) restartAgent() {
	go func() {
		_, err := a.request(localapi.RequestAgentRestart)
		a.update(func() {
			if err != nil {
				a.setToast(err.Error(), true)
			} else {
				a.setToast(L.HelperRestarted(), false)
			}
		})
	}()
}

// permissionsChanged asks once per app version, at the first status that
// names permissions, for the missing ones (macOS shows its own prompts).
// Notes: the caller holds a.mu.
func (a *App) permissionsChanged(st *localapi.Status) {
	if st.Permissions == nil || a.permissionsAsked {
		return
	}
	a.permissionsAsked = true
	if a.prefs.AskedPermissions == version.Version {
		return
	}
	p := a.prefs
	p.AskedPermissions = version.Version
	a.prefs = p
	go func() { _ = savePrefs(a.stateDir, p) }()
	for name, granted := range map[string]bool{"screen": st.Permissions.Screen,
		"accessibility": st.Permissions.Accessibility, "input-monitoring": st.Permissions.InputMonitoring} {
		if !granted {
			a.markAsked(name)
			a.askPermission(name, false)
		}
	}
}
