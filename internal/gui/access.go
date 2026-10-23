package gui

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// accessView is the Access page (doc/en-us/gui.md, "Settings"): what this
// Mac lets Kyokai do, how another device signs in and what a correct
// password grants, the ports of this computer's services, who watches its
// status, the trusted devices, and the sound it shares.
func (a *App) accessView(c *ui.Context) {
	settingsPage(c, L.NavAccess(), L.SettingsAboutAccess(), func() {
		if !a.engineState(c) {
			return
		}
		a.privacySection(c)
		a.signInSection(c)
		a.grantsSection(c)
		a.portsSection(c)
		a.watchersSection(c)
		a.trustedSection(c)
		a.mu.Lock()
		p := a.prefs
		a.mu.Unlock()
		a.section(c, "sound", L.SectionSound(), nil, func(r *rows) { a.soundRow(c, r, p) })
	})
}

// privacySection is Access's Privacy permissions (macOS): each permission
// Kyokai needs, Allowed or with Allow.
func (a *App) privacySection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var p *localapi.Permissions
	if a.status != nil {
		p = a.status.Permissions
	}
	helperMode := a.status != nil && a.status.RunMode == "helper"
	a.mu.Unlock()
	if p == nil {
		return
	}
	a.section(c, "perms", L.PermissionsTitle(), nil, func(r *rows) {
		if helperMode {
			r.block(c, "perms-helper", func() { ui.Text(c, L.PermissionsHelperHint()).FontSize(sizeSmall).TextColor(t.Warning) })
		}
		for i, pr := range privacyRows(p) {
			s := setting{title: pr.title, about: pr.hint}
			if i == 0 {
				s.more = L.PermissionsHint()
			}
			r.row(c, "perm-"+pr.name, tile(pr.icon), s, func() { a.permissionControl(c, pr.name, pr.granted) })
		}
	})
}

// privacyRow is a privacy permission Kyokai needs (macOS).
type privacyRow struct {
	name, title, hint string
	icon              *ui.SVG
	granted           bool
}

// privacyRows are the privacy permissions Kyokai needs, as p reports them.
func privacyRows(p *localapi.Permissions) []privacyRow {
	return []privacyRow{
		{"screen", L.PermissionScreen(), L.PermissionScreenHint(), rowEye, p.Screen},
		{"accessibility", L.PermissionAccessibility(), L.PermissionAccessibilityHint(), rowMouse, p.Accessibility},
		{"input-monitoring", L.PermissionInputMonitoring(), L.PermissionInputMonitoringHint(), barKeyboard, p.InputMonitoring},
		{"keychain", L.PermissionKeychain(), L.PermissionKeychainHint(), rowLock, p.Keychain},
	}
}

// permissionControl is what a privacy permission's row offers: Allowed,
// or Allow (with Reopen Kyokai once asked in this run, since macOS counts
// a new grant only in an engine started after it; in the helper modes,
// Restart Kyokai Helper).
func (a *App) permissionControl(c *ui.Context, name string, granted bool) {
	t := c.Theme()
	a.mu.Lock()
	asked := maps.Clone(a.asked)
	helperMode := a.status != nil && a.status.RunMode == "helper"
	a.mu.Unlock()
	if granted {
		ui.Text(c, L.PermissionGranted()).TextColor(t.Success)
		return
	}
	if helperMode && name != "keychain" {
		if ui.Button(c.Key("perm-restart-"+name), L.ActionRestartHelper()).Clicked() {
			a.restartAgent()
		}
	}
	if asked[name] && !helperMode {
		if ui.Button(c.Key("perm-reopen-"+name), L.ActionReopen()).Clicked() {
			a.reopen()
		}
	}
	if ui.Button(c.Key("perm-ask-"+name), L.ActionAllowPermission()).Clicked() {
		a.mu.Lock()
		a.markAsked(name)
		a.mu.Unlock()
		a.askPermission(name, true)
	}
}

// signInSection is Access's Sign-in: each way another device signs in,
// switched on its own — the temporary password, the permanent one (set,
// changed or removed in a form under its row), and the keys that can
// connect (shown under theirs, with Add a key and Generate a pair).
func (a *App) signInSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy, hasPermanent, mode := a.access, a.busy["access"], a.status.PermanentPassword, a.status.PasswordMode
	k := a.keys
	a.mu.Unlock()
	if !k.loaded && !k.busy {
		a.loadKeys()
	}
	temporary, permanent := passwordsAccepted(mode)
	setMode := func() {
		a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, PasswordMode: passwordMode(temporary, permanent)}, nil)
	}
	keysOn := slices.ContainsFunc(k.authorized, func(key localapi.AuthorizedKey) bool { return !key.Off })
	a.section(c, "sign-in", L.SignInTitle(), nil, func(r *rows) {
		if k.shown != nil && k.shown.pair {
			r.block(c, "pair-shown", func() { a.shownKey(c, *k.shown) })
		}
		r.row(c, "sign-temp", tile(rowRefresh), setting{title: L.SignInTemporary(), about: L.SignInTemporaryAbout()}, func() {
			if ui.Switch(c.Key("sign-temp-sw"), &temporary).Label(L.SignInTemporary()).Disabled(busy).Changed() {
				setMode()
			}
		})
		perm := setting{title: L.PermanentTitle(), about: L.PermanentSetAbout()}
		switch {
		case !hasPermanent && permanent:
			perm.about, perm.col = L.SignInPermanentMissing(), t.Warning
		case !hasPermanent:
			perm.about = L.PermanentNotSet()
		}
		r.row(c, "sign-perm", tile(rowLock), perm, func() {
			label := L.PermanentChange()
			if !hasPermanent {
				label = L.PermanentSetButton()
			}
			if ui.Button(c.Key("perm-open"), label).Disabled(busy || f.permanentOpen).Clicked() {
				a.mu.Lock()
				a.access.permanentOpen = true
				a.mu.Unlock()
			}
			if ui.Switch(c.Key("sign-perm-sw"), &permanent).Label(L.PermanentTitle()).Disabled(busy).Changed() {
				setMode()
			}
		})
		if f.permanentOpen {
			r.block(c, "perm-form", func() { a.permanentForm(c, f, hasPermanent, busy) })
		}
		keys := setting{title: L.SignInKeys(), about: L.SignInKeysAbout(len(k.authorized))}
		if len(k.authorized) == 0 {
			keys.about = L.SignInKeysNone()
		}
		r.row(c, "sign-keys", tile(navPermissions), keys, func() {
			label := L.ActionManage()
			if f.keysOpen {
				label = L.ActionHide()
			}
			if ui.Button(c.Key("sign-keys-open"), label).Clicked() {
				a.mu.Lock()
				a.access.keysOpen = !f.keysOpen
				a.mu.Unlock()
			}
		})
		if f.keysOpen {
			a.authorizedKeys(c, r, k)
		}
		if !temporary && !permanent && !keysOn {
			r.block(c, "sign-none", func() { ui.Text(c, L.SignInNone()).FontSize(sizeSmall).TextColor(t.Warning) })
		}
	})
}

// permanentForm sets, changes or removes the permanent password (a block
// under its row); the field is write-only and cleared once saved.
func (a *App) permanentForm(c *ui.Context, f accessForm, hasPermanent, busy bool) {
	ui.Row(c.Key("perm-form-row")).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
		in := ui.TextInput(c.Key("perm-in"), &f.permanent).Password().Placeholder(L.PermanentTitle()).Label(L.PermanentTitle()).Grow(1).Disabled(busy)
		if in.Changed() {
			a.mu.Lock()
			a.access.permanent = f.permanent
			a.mu.Unlock()
		}
		if ui.PrimaryButton(c.Key("perm-set"), L.ActionSave()).Disabled(busy || f.permanent == "").Clicked() {
			p := f.permanent
			a.setPermanent(&p)
		}
		if hasPermanent {
			if ui.Button(c.Key("perm-remove"), L.ActionRemove()).Disabled(busy).Clicked() {
				a.setPermanent(nil)
			}
		}
		if ui.Button(c.Key("perm-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
			a.mu.Lock()
			a.access.permanentOpen, a.access.permanent = false, ""
			a.mu.Unlock()
		}
	})
}

// authorizedKeys are the keys that can connect, under Sign-in's Keys: each
// switched on or off as a way to sign in, with Copy public key and Remove,
// then Add a key and Generate a pair (or their form).
func (a *App) authorizedKeys(c *ui.Context, r *rows, k keysState) {
	for _, key := range k.authorized {
		id, on := key.ID, !key.Off
		fp, kind := keyFingerprint(key.Public)
		r.row(c, "auth-"+id, nil, setting{title: key.Name, about: kind + " · " + fp}, func() {
			a.keyActions(c, "auth-"+id, key.Name, key.Public, k.busy, func() {
				a.keysCommand(localapi.Request{Type: localapi.RequestAuthorizedRemove, Authorized: &localapi.AuthorizedKey{ID: id}}, "")
			})
			if ui.Switch(c.Key("auth-"+id+"-on"), &on).Label(key.Name).Disabled(k.busy).Changed() {
				a.keysCommand(localapi.Request{Type: localapi.RequestAuthorizedSet, Authorized: &localapi.AuthorizedKey{ID: id, Off: !on}}, "")
			}
		})
	}
	switch k.form.kind {
	case "authorize", "pair":
		r.block(c, "auth-form", func() { a.keyForm(c, k) })
	default:
		r.block(c, "auth-add", func() {
			ui.Row(c).Gap(gap).Children(func() {
				if ui.Button(c.Key("auth-add-btn"), L.KeysAuthorize()).Disabled(k.busy).Clicked() {
					a.openKeyForm("authorize")
				}
				if ui.Button(c.Key("auth-pair"), L.KeysPair()).Disabled(k.busy).Clicked() {
					a.openKeyForm("pair")
				}
			})
		})
	}
}

// grantsSection is Access's When the password is right: whether a correct
// password (or key) admits without asking, and then what it grants, as
// tiles that toggle; anything else waits for the user.
func (a *App) grantsSection(c *ui.Context) {
	a.mu.Lock()
	busy, without := a.busy["access"], a.status.WithoutAsking
	grants := slices.Clone(a.status.PasswordGrants)
	a.mu.Unlock()
	a.section(c, "without", L.SectionPasswordRight(), nil, func(r *rows) {
		r.row(c, "without-asking", tile(navSecurity), setting{title: L.WithoutAskingTitle(), about: L.WithoutAskingAbout(), more: L.WithoutAskingHint()}, func() {
			if ui.Switch(c.Key("without-asking-sw"), &without).Label(L.WithoutAskingTitle()).Disabled(busy).Changed() {
				on := without
				a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, WithoutAsking: &on}, nil)
			}
		})
	})
	if !without {
		return
	}
	t := c.Theme()
	a.sectionHead(c, "grants", L.PasswordGrantsTitle(), func() {
		ui.Text(c, L.PasswordGrantsNote()).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
	ui.Grid(c.Key("grants")).Columns(4).Gap(10).FillWidth().Children(func() {
		for _, p := range allPermissions {
			on := slices.Contains(grants, p)
			if toggleTile(c, "pwgrant-"+p, permissionIcon(p), permissionShort(p), &on, busy) {
				// Never nil: an empty list (no grants) must not read as missing.
				next := slices.DeleteFunc(append([]string{}, grants...), func(x string) bool { return x == p })
				if on {
					next = append(next, p)
				}
				a.accessCommand(localapi.Request{Type: localapi.RequestPasswordGrants, Grants: &next}, func() {})
			}
		}
	})
}

// permissionIcon is a permission's icon on its tile.
func permissionIcon(p string) *ui.SVG {
	switch p {
	case localapi.PermViewScreen:
		return rowEye
	case localapi.PermControlInput:
		return rowMouse
	case localapi.PermClipboard:
		return barClipboard
	case localapi.PermFiles:
		return rowFolder
	case localapi.PermHearSound:
		return barSound
	case localapi.PermRunAITask:
		return navAI
	case localapi.PermApproveAIAction:
		return rowCheck
	case localapi.PermSeeStatus:
		return rowPulse
	case localapi.PermUseSSH:
		return kindSSH
	case localapi.PermUseRDP:
		return kindRDP
	case localapi.PermUseVNC:
		return kindVNC
	}
	return navPermissions
}

// permissionShort is a permission's short name, as its tile shows it.
func permissionShort(p string) string {
	switch p {
	case localapi.PermControlInput:
		return L.PermShortInput()
	case localapi.PermClipboard:
		return L.PermShortClipboard()
	case localapi.PermSeeStatus:
		return L.PermShortStatus()
	case localapi.PermUseSSH:
		return L.ServiceSSH()
	case localapi.PermUseRDP:
		return L.ServiceRDP()
	case localapi.PermUseVNC:
		return L.ServiceVNC()
	}
	return permissionLabel(p)
}

// portsSection is Access's ports of this computer's SSH, Remote Desktop and
// VNC (edited, then saved): another device only names the service, and its
// tunnel reaches the port set here.
func (a *App) portsSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	set, edited, busy := a.status.ServicePorts, a.access.ports, a.busy["access"]
	a.mu.Unlock()
	fields := portFields(set)
	if edited != nil {
		fields = *edited
	}
	a.section(c, "ports", L.ServicePortsTitle(), nil, func(r *rows) {
		for i, s := range servicePortNames {
			icon := map[string]*ui.SVG{serviceSSH: kindSSH, serviceRDP: kindRDP, serviceVNC: kindVNC}[s]
			st := setting{title: serviceName(s), about: L.ServicePortStandard(strconv.Itoa(standardPorts[i]))}
			if i == 0 {
				st.more = L.ServicePortsHint()
			}
			r.row(c, "svc-port-"+s, tile(icon), st, func() {
				in := ui.TextInput(c.Key("svc-port-in-"+s), &fields[i]).Placeholder(strconv.Itoa(standardPorts[i])).Label(serviceName(s)).Width(110).Disabled(busy)
				if in.Changed() {
					a.mu.Lock()
					a.access.ports = &fields
					a.mu.Unlock()
				}
			})
		}
		if edited == nil {
			return
		}
		ports, ok := parsePorts(fields)
		r.block(c, "svc-ports-save", func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
				if !ok {
					ui.Text(c, L.ServicePortsInvalid()).FontSize(sizeSmall).TextColor(t.Danger)
				}
				ui.Box(c).Grow(1)
				if ui.Button(c.Key("svc-ports-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
					a.mu.Lock()
					a.access.ports = nil
					a.mu.Unlock()
				}
				if ui.PrimaryButton(c.Key("svc-ports-save-btn"), L.ActionSave()).Disabled(busy || !ok).Clicked() {
					a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, ServicePorts: &ports}, func() {
						a.access.ports = nil
						a.setToast(L.ServicePortsSaved(), false)
					})
				}
			})
		})
	})
}

// watchersSection is Access's devices watching this computer's status,
// with when they started.
func (a *App) watchersSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var watchers []localapi.Watcher
	if a.status != nil {
		watchers = a.status.Watchers
	}
	prefs := a.prefs
	a.mu.Unlock()
	a.section(c, "watchers", L.WatchersTitle(), nil, func(r *rows) {
		if len(watchers) == 0 {
			r.block(c, "watchers-none", func() { ui.Text(c, L.WatchersNone()).FontSize(sizeSmall).TextColor(t.TextMuted) })
		}
		for i, w := range watchers {
			about := prefs.named(w.DeviceID, w.About)
			s := setting{title: deviceTitle(about, w.DeviceID), about: L.WatchersSince(time.Unix(w.Since, 0).Local().Format("15:04"))}
			if i == 0 {
				s.more = L.WatchersHint()
			}
			r.row(c, "watcher-"+w.DeviceID, avatarLead(about), s, nil)
		}
	})
}

// soundRow is Access's choice of the sound this device shares with the
// devices that control it: what it plays, or one input device.
func (a *App) soundRow(c *ui.Context, r *rows, p Prefs) {
	a.loadSoundDevices()
	a.mu.Lock()
	devices := a.soundDevices
	a.mu.Unlock()
	ids, labels := []string{""}, []string{L.SettingsSoundSystem()}
	for _, d := range devices {
		name := d.Name
		if name == "" {
			name = L.SettingsSoundUnnamed()
		}
		ids, labels = append(ids, d.ID), append(labels, name)
	}
	// A device chosen before that is gone now still shows as chosen.
	if !slices.Contains(ids, p.SoundSource) {
		ids, labels = append(ids, p.SoundSource), append(labels, L.SettingsSoundMissing())
	}
	picked := labels[slices.Index(ids, p.SoundSource)]
	r.row(c, "sound-source", tile(barSound), setting{title: L.SettingsSoundSource(), about: L.SoundSourceAbout(), more: L.SettingsSoundSourceHint()}, func() {
		if ui.Select(c.Key("sound-source-sel"), &picked, labels).Width(240).Changed() {
			if i := slices.Index(labels, picked); i >= 0 {
				a.setSoundSource(p, ids[i])
			}
		}
	})
}

// trustedSection is Access's devices this one allowed, with their last
// connection and Remove.
func (a *App) trustedSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy, prefs := a.access, a.busy["access"], a.prefs
	a.mu.Unlock()
	if !f.loaded && !busy {
		a.loadTrusted()
	}
	a.section(c, "trusted", L.TrustedTitle(), nil, func(r *rows) {
		if f.loaded && len(f.trusted) == 0 {
			r.block(c, "trusted-none", func() { ui.Text(c, L.TrustedHint()).FontSize(sizeSmall).TextColor(t.TextMuted) })
		}
		for _, d := range f.trusted {
			about := prefs.named(d.DeviceID, d.About)
			last := L.TrustedLast(time.Unix(d.Last, 0).Local().Format(time.DateTime))
			r.row(c, "trust-"+d.Key, avatarLead(about), setting{title: deviceTitle(about, d.DeviceID), about: last}, func() {
				if ui.Button(c.Key("trust-rm-"+d.Key), L.ActionRemove()).Disabled(busy).Clicked() {
					a.untrust(d.Key)
				}
			})
		}
	})
}
