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
}

// permissionsMissing reports whether a privacy permission Kyokai needs is
// missing (macOS); false where the system has none.
func permissionsMissing(p *localapi.Permissions) bool {
	return p != nil && !(p.Screen && p.Accessibility && p.InputMonitoring && p.Keychain)
}

// permissionsCard shows the system's privacy permissions Kyokai needs
// (macOS) with Allow for a missing one. Inputs: always shows it even when
// everything is granted (Settings); Home shows it only while one is missing.
// Notes: once Screen Recording was asked for in this run, its row offers
// to reopen Kyokai, since macOS counts that grant only after a restart.
func (a *App) permissionsCard(c *ui.Context, always bool) {
	t := c.Theme()
	a.mu.Lock()
	var p *localapi.Permissions
	if a.status != nil {
		p = a.status.Permissions
	}
	screenAsked := a.screenAsked
	helperMode := a.status != nil && a.status.RunMode == "helper"
	a.mu.Unlock()
	if p == nil || !always && !permissionsMissing(p) {
		return
	}
	card(c.Key("perms")).Children(func() {
		ui.Text(c, L.PermissionsTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.PermissionsHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if helperMode {
			ui.Text(c, L.PermissionsHelperHint()).FontSize(sizeSmall).TextColor(t.Warning)
		}
		for _, row := range []struct {
			name, title, hint string
			granted           bool
		}{
			{"screen", L.PermissionScreen(), L.PermissionScreenHint(), p.Screen},
			{"accessibility", L.PermissionAccessibility(), L.PermissionAccessibilityHint(), p.Accessibility},
			{"input-monitoring", L.PermissionInputMonitoring(), L.PermissionInputMonitoringHint(), p.InputMonitoring},
			{"keychain", L.PermissionKeychain(), L.PermissionKeychainHint(), p.Keychain},
		} {
			settingRow(c, "perm-"+row.name, row.title, row.hint, func() {
				if row.granted {
					ui.Text(c, L.PermissionGranted()).TextColor(t.Success)
					return
				}
				if helperMode && row.name != "keychain" {
					if ui.Button(c.Key("perm-restart-"+row.name), L.ActionRestartHelper()).Clicked() {
						a.restartAgent()
					}
				}
				if row.name == "screen" && screenAsked && !helperMode {
					if ui.Button(c.Key("perm-reopen"), L.ActionReopen()).Clicked() {
						a.reopen()
					}
				}
				if ui.Button(c.Key("perm-ask-"+row.name), L.ActionAllowPermission()).Clicked() {
					if row.name == "screen" {
						a.mu.Lock()
						a.screenAsked = true
						a.mu.Unlock()
					}
					a.askPermission(row.name, true)
				}
			})
		}
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
	if !st.Permissions.Screen {
		a.screenAsked = true
		a.askPermission("screen", false)
	}
	if !st.Permissions.Accessibility {
		a.askPermission("accessibility", false)
	}
	if !st.Permissions.InputMonitoring {
		a.askPermission("input-monitoring", false)
	}
}
