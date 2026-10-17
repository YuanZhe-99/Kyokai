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
	"screen":        "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"accessibility": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
}

// permissionsCard shows the system's privacy permissions Kyokai needs
// (macOS) with Allow for a missing one. Inputs: always shows it even when
// everything is granted (Settings); Home shows it only while one is missing.
func (a *App) permissionsCard(c *ui.Context, always bool) {
	t := c.Theme()
	a.mu.Lock()
	var p *localapi.Permissions
	if a.status != nil {
		p = a.status.Permissions
	}
	a.mu.Unlock()
	if p == nil || !always && p.Screen && p.Accessibility && p.Keychain {
		return
	}
	card(c.Key("perms")).Children(func() {
		ui.Text(c, L.PermissionsTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.PermissionsHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		for _, row := range []struct {
			name, title, hint string
			granted           bool
		}{
			{"screen", L.PermissionScreen(), L.PermissionScreenHint(), p.Screen},
			{"accessibility", L.PermissionAccessibility(), L.PermissionAccessibilityHint(), p.Accessibility},
			{"keychain", L.PermissionKeychain(), L.PermissionKeychainHint(), p.Keychain},
		} {
			settingRow(c, "perm-"+row.name, row.title, row.hint, func() {
				if row.granted {
					ui.Text(c, L.PermissionGranted()).TextColor(t.Success)
					return
				}
				if ui.Button(c.Key("perm-ask-"+row.name), L.ActionAllowPermission()).Clicked() {
					a.askPermission(row.name, true)
				}
			})
		}
	})
}

// askPermission asks the system for a permission through the engine, whose
// capture and input need it (for the Keychain, the engine opens its store
// again and macOS asks); with open it also shows a privacy permission's
// pane in System Settings, where a permission asked for before is granted.
func (a *App) askPermission(name string, open bool) {
	go func() {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestPermission, Permission: name})
		if open {
			if pane, ok := permissionPanes[name]; ok {
				_ = mygo.Shell.OpenExternal(pane)
			}
		}
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
		a.askPermission("screen", false)
	}
	if !st.Permissions.Accessibility {
		a.askPermission("accessibility", false)
	}
}
