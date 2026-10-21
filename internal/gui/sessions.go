package gui

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// connectForm is the Home page's "connect to another device" form.
type connectForm struct {
	// target is a Device ID or an address, as typed.
	target string
	// ai also asks to run AI tasks on the host and approve their actions.
	ai bool
	// identifying: the device at an address is being asked for its ID.
	identifying bool
}

// renameForm is the name being given to a recent device (Home), while
// device is set.
type renameForm struct {
	device string
	name   string
}

// accessForm is the Permissions and Network pages' state: the permanent password field, the
// connections-by-address ranges while edited (ranges nil follows the
// engine), and the trusted devices last fetched (nil until fetched).
type accessForm struct {
	permanent string
	ranges    *string
	trusted   []localapi.TrustedDevice
	loaded    bool
}

// parseTarget reads Connect's field: nine digits (spaces allowed) are a
// Device ID, other digits nothing yet, anything else an address.
func parseTarget(s string) (device, address string) {
	compact := strings.Join(strings.Fields(s), "")
	if compact == "" {
		return "", ""
	}
	if strings.Trim(compact, "0123456789") == "" {
		if len(compact) == 9 {
			return compact, ""
		}
		return "", ""
	}
	return "", strings.TrimSpace(s)
}

// requestsView shows every controller waiting for this host's user, on
// every page: who it is, what it asks for, and Allow / Deny.
func (a *App) requestsView(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var reqs []localapi.HostRequest
	if a.connected && a.status != nil {
		reqs = a.status.Requests
	}
	grants, prefs := a.grants, a.prefs
	a.mu.Unlock()
	var perms *localapi.Permissions
	if len(reqs) > 0 {
		perms = a.status.Permissions
	}
	for _, r := range reqs {
		key := "req-" + itoa(r.ID)
		card(c.Key(key)).Border(2, t.Accent).Children(func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, r.About)
				ui.Text(c, L.RequestTitle(deviceTitle(prefs.named(r.DeviceID, r.About), r.DeviceID))).FontSize(sizeHeading).Bold()
			})
			if !r.Password {
				ui.Text(c, L.RequestNoPassword()).FontSize(sizeSmall).TextColor(t.Warning)
			}
			switch {
			case r.NewKey:
				ui.Text(c, L.RequestNewKey()).FontSize(sizeSmall).TextColor(t.Warning)
			case r.Known:
				ui.Text(c, L.RequestKnown()).FontSize(sizeSmall).TextColor(t.TextMuted)
			default:
				ui.Text(c, L.RequestFirstTime()).FontSize(sizeSmall).TextColor(t.TextMuted)
			}
			if perms != nil && (slices.Contains(r.Want, localapi.PermViewScreen) && !perms.Screen ||
				slices.Contains(r.Want, localapi.PermControlInput) && !perms.Accessibility) {
				ui.Text(c, L.RequestPermissionsMissing()).FontSize(sizeSmall).TextColor(t.Warning)
			}
			ui.Text(c, L.RequestAsks())
			chosen, ok := grants[r.ID]
			if !ok {
				chosen = slices.Clone(r.Want)
			}
			for _, p := range r.Want {
				on := slices.Contains(chosen, p)
				ui.Row(c.Key(key + "-row-" + p)).Gap(8).AlignItems(ui.Center).Children(func() {
					if ui.Switch(c.Key(key+"-"+p), &on).Label(permissionLabel(p)).Changed() {
						next := slices.DeleteFunc(slices.Clone(chosen), func(x string) bool { return x == p })
						if on {
							next = append(next, p)
						}
						a.mu.Lock()
						a.grants[r.ID] = next
						a.mu.Unlock()
					}
					ui.Text(c, permissionLabel(p))
				})
			}
			ui.Row(c).Gap(gap).Children(func() {
				if ui.PrimaryButton(c.Key(key+"-allow"), L.ActionAllow()).Clicked() {
					a.decide(r.ID, true, chosen)
				}
				if ui.Button(c.Key(key+"-deny"), L.ActionDeny()).Clicked() {
					a.decide(r.ID, false, nil)
				}
			})
		})
	}
}

// hostBanner shows, on every page, each device connected to this one
// right now, what it may do, and Disconnect, so the host's user always
// sees who is here and can end it.
func (a *App) hostBanner(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var here []localapi.SessionInfo
	if a.connected && a.status != nil {
		for _, s := range a.status.Sessions {
			if s.Role == "host" && (s.State == "connected" || s.State == "reconnecting") {
				here = append(here, s)
			}
		}
	}
	prefs := a.prefs
	a.mu.Unlock()
	for _, s := range here {
		key := "here-" + itoa(s.ID)
		card(c.Key(key)).Border(2, t.Warning).Children(func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, s.Peer)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, L.HostConnected(deviceTitle(prefs.named(s.DeviceID, s.Peer), s.DeviceID))).Bold()
					ui.Text(c, sessionState(s, a.outdatedHelper())).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
				if ui.Button(c.Key(key+"-close"), L.ActionDisconnect()).Clicked() {
					id := s.ID
					go func() { _, _ = a.requestWith(localapi.Request{Type: localapi.RequestClose, ID: id}) }()
				}
			})
		})
	}
}

// decide answers a host confirmation.
func (a *App) decide(id uint64, allow bool, granted []string) {
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestDecide,
			Decide: &localapi.Decision{ID: id, Allow: allow, Permissions: granted}})
		a.update(func() {
			delete(a.grants, id)
			if err != nil {
				a.setToast(L.RequestGone(), true)
			}
		})
	}()
}

// connectCard is Home's "Control another device": a Device ID, Connect
// and its options. The password is asked for in the connection's tab
// when the device wants one.
func (a *App) connectCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, online := a.connect, a.status.Network.State == "online"
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.ConnectTitle()).FontSize(sizeHeading).Bold()
		id := ui.TextInput(c.Key("conn-id"), &f.target).Placeholder(L.ConnectPlaceholder()).FillWidth().FontSize(sizeTitle).FontFeatures("tnum").Disabled(f.identifying)
		if id.Changed() {
			a.mu.Lock()
			a.connect.target = f.target
			a.mu.Unlock()
		}
		device, address := parseTarget(f.target)
		ready := (online && device != "" || address != "") && !f.identifying
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			switch {
			case f.identifying:
				ui.Row(c).Grow(1).Gap(8).AlignItems(ui.Center).Children(func() {
					busyMark(c, L.ConnectIdentifying(), busySize)
					ui.Text(c, L.ConnectIdentifying()).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
			case !online && address == "":
				ui.Text(c, L.ConnectNeedsNetwork()).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
			default:
				ui.Row(c).Grow(1)
			}
			ui.MenuButton(c.Key("conn-options"), L.ConnectOptions(), func(m *ui.Menu) {
				if m.Item(L.ConnectWithAI()).Checked(f.ai).Chosen() {
					a.mu.Lock()
					a.connect.ai = !f.ai
					a.mu.Unlock()
				}
			}).Label(L.ConnectOptions()).Tooltip(L.ConnectOptions())
			if ui.PrimaryButton(c.Key("conn-go"), L.ActionConnect()).Disabled(!ready).Clicked() {
				if address != "" {
					a.connectAddress(address, connectWant(f.ai))
				} else {
					a.connectDevice(device, "", a.recentAbout(device), connectWant(f.ai))
				}
			}
		})
	})
}

// connectAddress asks the device at an address for its Device ID, then
// connects to that device there.
// Side effects: a toast when no device answers.
func (a *App) connectAddress(address string, want []string) {
	a.mu.Lock()
	a.connect.identifying = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestIdentify, Address: address})
		a.update(func() {
			a.connect.identifying = false
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			case resp.DeviceID == "":
				a.setToast(identifyFailure(resp.Failure), true)
			}
		})
		a.mu.Lock()
		w := a.window
		a.mu.Unlock()
		// On the main thread (Window.Update) and not under a.mu, which
		// connectDevice takes itself.
		if err == nil && resp.DeviceID != "" && w != nil {
			w.Update(func() { a.connectDevice(resp.DeviceID, address, a.recentAbout(resp.DeviceID), want) })
		}
	}()
}

// identifyFailure words why no device answered at an address.
func identifyFailure(name string) string {
	switch name {
	case "unreachable":
		return L.IdentifyUnreachable()
	case "incompatible":
		return L.FailIncompatible()
	case "self":
		return L.IdentifySelf()
	}
	return L.IdentifyBroken()
}

// recentAbout is what a recent device said about itself, if anything.
func (a *App) recentAbout(device string) localapi.About {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.status.Recent {
		if r.DeviceID == device {
			return r.About
		}
	}
	return localapi.About{}
}

// connectWant is what a connection asks the host for.
func connectWant(ai bool) []string {
	want := []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard, localapi.PermFiles, localapi.PermHearSound}
	if ai {
		want = append(want, localapi.PermRunAITask, localapi.PermApproveAIAction)
	}
	return want
}

// deviceRow is one device on Home: what it said about itself and its
// connection from here, if any.
type deviceRow struct {
	id string
	// about carries the name this device's user gave it, if any.
	about localapi.About
	last  int64
	// saved says what is kept for the device (flags only); features what
	// it announced in its last session.
	saved    localapi.Saved
	features []string
	// address is the address the last session dialed, empty over the
	// network.
	address string
	// session is the newest connection to it in the engine's list, or nil.
	session *localapi.SessionInfo
}

// deviceRows are the devices this one connected to, newest first, with
// the ones being connected to for the first time, by the names p gives
// them.
func deviceRows(st *localapi.Status, p Prefs) []deviceRow {
	var rows []deviceRow
	index := map[string]int{}
	for _, r := range st.Recent {
		index[r.DeviceID] = len(rows)
		saved := r.Saved
		// An engine of 0.4.0 says only whether a password is saved.
		saved.Password = saved.Password || r.SavedPassword
		rows = append(rows, deviceRow{id: r.DeviceID, about: p.named(r.DeviceID, r.About), last: r.Last, saved: saved, features: r.Features, address: r.Address})
	}
	for _, s := range st.Sessions {
		if s.Role != "controller" {
			continue
		}
		i, ok := index[s.DeviceID]
		if !ok {
			index[s.DeviceID] = len(rows)
			rows = append(rows, deviceRow{id: s.DeviceID, about: p.named(s.DeviceID, s.Peer)})
			i = len(rows) - 1
		}
		if cur := rows[i].session; cur == nil || s.ID > cur.ID {
			s := s
			rows[i].session = &s
		}
	}
	return rows
}

// forgetPassword forgets the password saved for a device.
func (a *App) forgetPassword(device string) {
	go func() {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestForgetPassword, DeviceID: device})
		a.update(func() { a.setToast(L.PasswordForgotten(), false) })
	}()
}

// renameCard edits the name a recent device has on this device only.
func (a *App) renameCard(c *ui.Context, f renameForm) {
	t := c.Theme()
	a.mu.Lock()
	var about localapi.About
	for _, r := range deviceRows(a.status, Prefs{}) {
		if r.id == f.device {
			about = r.about
		}
	}
	a.mu.Unlock()
	card(c.Key("rename-" + f.device)).Children(func() {
		ui.Text(c, L.RenameTitle(deviceTitle(about, f.device))).Bold()
		ui.Text(c, L.RenameHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
			in := ui.TextInput(c.Key("rename-in"), &f.name).Placeholder(about.Name).Grow(1)
			if in.Changed() {
				a.mu.Lock()
				a.rename.name = f.name
				a.mu.Unlock()
			}
			if ui.Button(c.Key("rename-cancel"), L.ActionCancel()).Clicked() {
				a.mu.Lock()
				a.rename = renameForm{}
				a.mu.Unlock()
			}
			if ui.PrimaryButton(c.Key("rename-save"), L.ActionSave()).Clicked() {
				a.mu.Lock()
				a.rename = renameForm{}
				a.mu.Unlock()
				a.setAlias(f.device, strings.TrimSpace(f.name))
			}
		})
	})
}

// removeDevice takes a device off Home: its connection ends, its
// sessions leave the list, its pinned key, saved password and the name
// given here are forgotten.
// Notes: call on the main thread.
func (a *App) removeDevice(device string) {
	if v := a.viewerFor(device); v != nil {
		if w := v.home.Load(); w != nil {
			w.closeTab(v)
		} else {
			v.disconnect()
		}
	}
	a.setAlias(device, "")
	a.forgetUses(device)
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestDeviceRemove, DeviceID: device})
		if err != nil {
			a.update(func() { a.setToast(L.EngineDown(), true) })
		}
	}()
}

// sessionState words a session's state, permissions or failure; behind is
// the attached helper's gap to this app, if any (outdatedHelper).
func sessionState(s localapi.SessionInfo, behind helperGap) string {
	switch s.State {
	case "connecting":
		return L.SessionConnecting()
	case "confirming":
		if s.Role == "host" {
			return L.SessionConfirmingHere()
		}
		return L.SessionConfirmingThere()
	case "reconnecting":
		return L.SessionReconnecting()
	case "connected":
		names := make([]string, len(s.Granted))
		for i, p := range s.Granted {
			names[i] = permissionLabel(p)
		}
		return L.SessionConnected(strings.Join(names, " · "))
	case "closed":
		return L.SessionClosed()
	}
	return failureText(s, behind)
}

// failureText words why a session failed. A version mismatch names the side
// that differs: this device's helper when it is behind this app, else the
// other device's version when it is known.
func failureText(s localapi.SessionInfo, behind helperGap) string {
	raw := s.Failure
	var name string
	var refused struct {
		Refused string `json:"refused"`
	}
	if json.Unmarshal(raw, &refused) == nil && refused.Refused != "" {
		name = "refused-" + refused.Refused
	} else {
		_ = json.Unmarshal(raw, &name)
	}
	switch name {
	case "refused-password":
		return L.FailPassword()
	case "refused-denied":
		return L.FailDenied()
	case "refused-timeout":
		return L.FailTimeout()
	case "refused-busy":
		return L.FailBusy()
	case "refused-rate-limited":
		return L.FailRateLimited()
	case "refused-absent":
		return L.FailAbsent()
	case "unreachable":
		return L.FailUnreachable()
	case "wrong-device":
		return L.FailWrongDevice()
	case "key-changed":
		return L.FailKeyChanged()
	case "incompatible":
		switch {
		case behind.helper != "":
			return L.FailHelperOutdated(behind.helper, behind.app)
		case s.PeerVersion != "":
			return L.FailIncompatibleVersion(s.PeerVersion, version.Version)
		}
		return L.FailIncompatible()
	}
	return L.FailBroken()
}

// permissionLabel words a permission name.
func permissionLabel(p string) string {
	switch p {
	case localapi.PermViewScreen:
		return L.PermViewScreen()
	case localapi.PermControlInput:
		return L.PermControlInput()
	case localapi.PermRunAITask:
		return L.PermRunAITask()
	case localapi.PermApproveAIAction:
		return L.PermApproveAIAction()
	case localapi.PermClipboard:
		return L.PermClipboard()
	case localapi.PermFiles:
		return L.PermFiles()
	case localapi.PermHearSound:
		return L.PermHearSound()
	case localapi.PermSeeStatus:
		return L.PermSeeStatus()
	case localapi.PermUseSSH:
		return L.PermUseSSH()
	case localapi.PermUseRDP:
		return L.PermUseRDP()
	case localapi.PermUseVNC:
		return L.PermUseVNC()
	}
	return p
}

// passwordModes are the accepted passwords' choices, in the order shown.
var passwordModes = []string{"temporary", "permanent", "both"}

// acceptSettings is how this host accepts a connection: which passwords
// open it and whether an accepted password admits without asking.
func (a *App) acceptSettings(c *ui.Context, mode string, without, hasPermanent, busy bool) {
	idx := slices.Index(passwordModes, mode)
	if idx < 0 {
		idx = 2
	}
	settingRow(c, "pw-mode", L.PasswordModeTitle(), L.PasswordModeHint(), func() {
		if ui.Segmented(c.Key("pw-mode-seg"), &idx, L.PasswordModeTemporary(), L.PasswordModePermanent(), L.PasswordModeBoth()).Disabled(busy).Changed() {
			a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, PasswordMode: passwordModes[idx]}, nil)
		}
	})
	if passwordModes[idx] == "permanent" && !hasPermanent {
		ui.Text(c, L.PasswordModeNeedsPermanent()).FontSize(sizeSmall).TextColor(c.Theme().Warning)
	}
	settingRow(c, "without-asking", L.WithoutAskingTitle(), L.WithoutAskingHint(), func() {
		if ui.Switch(c.Key("without-asking-sw"), &without).Label(L.WithoutAskingTitle()).Disabled(busy).Changed() {
			on := without
			a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, WithoutAsking: &on}, nil)
		}
	})
}

// accessSettings is the Permissions page's card of passwords: which
// passwords open this device, the permanent password, and what a correct
// password grants without asking.
func (a *App) accessSettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy, hasPermanent := a.access, a.busy["access"], a.status.PermanentPassword
	grants := slices.Clone(a.status.PasswordGrants)
	mode, without := a.status.PasswordMode, a.status.WithoutAsking
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.SettingsPasswords()).FontSize(sizeHeading).Bold()
		hint := L.PermanentNotSet()
		if hasPermanent {
			hint = L.PermanentSet()
		}
		ui.Text(c, L.PermanentTitle())
		ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("perm-in"), &f.permanent).Password().Grow(1).Disabled(busy)
			if in.Changed() {
				a.mu.Lock()
				a.access.permanent = f.permanent
				a.mu.Unlock()
			}
			if ui.Button(c.Key("perm-set"), L.ActionSave()).Disabled(busy || f.permanent == "").Clicked() {
				p := f.permanent
				a.setPermanent(&p)
			}
			if hasPermanent {
				if ui.Button(c.Key("perm-remove"), L.ActionRemove()).Disabled(busy).Clicked() {
					a.setPermanent(nil)
				}
			}
		})
		ui.Divider(c)
		a.acceptSettings(c, mode, without, hasPermanent, busy)
		if without {
			a.passwordGrants(c, grants, busy)
		}
	})
}

// trustedCard is the Permissions page's list of the devices this one
// allowed, with their last connection and Remove.
func (a *App) trustedCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy, prefs := a.access, a.busy["access"], a.prefs
	a.mu.Unlock()
	if !f.loaded && !busy {
		a.loadTrusted()
	}
	card(c).Children(func() {
		ui.Text(c, L.TrustedTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.TrustedHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if f.loaded && len(f.trusted) == 0 {
			ui.Text(c, L.TrustedNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, d := range f.trusted {
			hint := L.TrustedLast(time.Unix(d.Last, 0).Local().Format(time.DateTime))
			settingRow(c, "trust-"+d.Key, deviceTitle(prefs.named(d.DeviceID, d.About), d.DeviceID), hint, func() {
				if ui.Button(c.Key("trust-rm-"+d.Key), L.ActionRemove()).Disabled(busy).Clicked() {
					a.untrust(d.Key)
				}
			})
		}
	})
}

// protected reports whether protected mode is on (caller holds a.mu).
func (a *App) protected() bool {
	// The last status counts while the engine is away.
	return a.status != nil && a.status.Protected
}

// protectedSetting is the Security page's protected mode: on only with a
// permanent password, after a confirmation.
func (a *App) protectedSetting(c *ui.Context) {
	a.mu.Lock()
	hasPermanent, busy := a.status.PermanentPassword, a.busy["access"]
	a.mu.Unlock()
	on := false
	settingRow(c, "protected", L.ProtectedTitle(), L.ProtectedHint(), func() {
		if ui.Switch(c.Key("protected-sw"), &on).Label(L.ProtectedTitle()).Disabled(busy || !hasPermanent).Changed() && on {
			// A dialog: off the main thread.
			go func() {
				if a.confirm(L.ProtectedConfirm(), L.ProtectedTurnOn()) {
					a.accessCommand(localapi.Request{Type: localapi.RequestProtect, Protect: &localapi.ProtectRequest{On: true}}, nil)
				}
			}()
		}
	})
	if !hasPermanent {
		ui.Text(c, L.ProtectedNeedsPermanent()).FontSize(sizeSmall).TextColor(c.Theme().TextMuted)
	}
}

// protectedCard tops the AI page in protected mode: what it is, and the
// permanent password to turn it off.
func (a *App) protectedCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	password, busy := a.unlock, a.busy["access"]
	a.mu.Unlock()
	card(c).Border(2, t.Accent).Children(func() {
		ui.Text(c, L.ProtectedTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.ProtectedOnHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
			in := ui.TextInput(c.Key("unlock-in"), &password).Password().Placeholder(L.PermanentTitle()).Grow(1).Disabled(busy)
			if in.Changed() {
				a.mu.Lock()
				a.unlock = password
				a.mu.Unlock()
			}
			if ui.Button(c.Key("unlock-go"), L.ProtectedTurnOff()).Disabled(busy || password == "").Clicked() {
				a.mu.Lock()
				a.unlock = ""
				a.mu.Unlock()
				a.mu.Lock()
				a.busy["access"] = true
				a.mu.Unlock()
				go func() {
					_, err := a.requestWith(localapi.Request{Type: localapi.RequestProtect, Protect: &localapi.ProtectRequest{Password: password}})
					a.update(func() {
						delete(a.busy, "access")
						if err != nil {
							a.setToast(L.ProtectedWrongPassword(), true)
						}
					})
				}()
			}
		})
	})
}

// byAddressSettings is the Network page's connections by address: the
// switch, and the ranges they are accepted from (edited, then saved).
func (a *App) byAddressSettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	by, edited, busy := a.status.ByAddress, a.access.ranges, a.busy["access"]
	a.mu.Unlock()
	on := by.On
	settingRow(c, "by-address", L.ByAddressTitle(), L.ByAddressHint(), func() {
		if ui.Switch(c.Key("by-address-sw"), &on).Label(L.ByAddressTitle()).Disabled(busy).Changed() {
			a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings,
				ByAddress: &localapi.ByAddress{On: on, Ranges: by.Ranges}}, nil)
		}
	})
	if !by.On {
		return
	}
	if by.Port == 0 {
		ui.Text(c, L.ByAddressFailed()).FontSize(sizeSmall).TextColor(t.Warning)
	}
	ranges := strings.Join(by.Ranges, "\n")
	if edited != nil {
		ranges = *edited
	}
	ui.Column(c.Key("by-address-ranges")).Gap(4).FillWidth().Children(func() {
		ui.Text(c, L.ByAddressRanges())
		if ui.TextArea(c.Key("by-address-ranges-in"), &ranges).FillWidth().MinHeight(inputAreaH).Disabled(busy).Changed() {
			a.mu.Lock()
			a.access.ranges = &ranges
			a.mu.Unlock()
		}
		ui.Text(c, L.ByAddressRangesHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
	if edited != nil {
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key("by-address-save"), L.ActionSave()).Disabled(busy).Clicked() {
				a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings,
					ByAddress: &localapi.ByAddress{On: true, Ranges: append([]string{}, lines(ranges)...)}}, func() {
					a.access.ranges = nil
					a.setToast(L.ByAddressSaved(), false)
				})
			}
			if ui.Button(c.Key("by-address-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
				a.mu.Lock()
				a.access.ranges = nil
				a.mu.Unlock()
			}
		})
	}
}

// allPermissions are the permissions in the order the Permissions page lists them.
var allPermissions = []string{
	localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard,
	localapi.PermFiles, localapi.PermHearSound, localapi.PermRunAITask, localapi.PermApproveAIAction,
	localapi.PermSeeStatus, localapi.PermUseSSH, localapi.PermUseRDP, localapi.PermUseVNC,
}

// passwordGrants is the Permissions page's choice of what a correct password
// grants without asking.
func (a *App) passwordGrants(c *ui.Context, grants []string, busy bool) {
	t := c.Theme()
	ui.Text(c, L.PasswordGrantsTitle())
	ui.Text(c, L.PasswordGrantsHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	for _, p := range allPermissions {
		on := slices.Contains(grants, p)
		ui.Row(c.Key("pwgrant-row-" + p)).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Switch(c.Key("pwgrant-"+p), &on).Label(permissionLabel(p)).Disabled(busy).Changed() {
				// Never nil: an empty list (no grants) must not read as missing.
				next := slices.DeleteFunc(append([]string{}, grants...), func(x string) bool { return x == p })
				if on {
					next = append(next, p)
				}
				a.accessCommand(localapi.Request{Type: localapi.RequestPasswordGrants, Grants: &next}, func() {})
			}
			ui.Text(c, permissionLabel(p))
		})
	}
}

func (a *App) loadTrusted() {
	a.mu.Lock()
	a.busy["access"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestTrustedList})
		a.update(func() {
			delete(a.busy, "access")
			a.access.loaded = true
			if err == nil {
				a.access.trusted = resp.Trusted
			}
		})
	}()
}

// setPermanent sets (or with nil removes) the permanent password.
func (a *App) setPermanent(p *string) {
	a.accessCommand(localapi.Request{Type: localapi.RequestPermanentSet, Permanent: &localapi.PermanentPassword{Password: p}},
		func() {
			a.access.permanent = ""
			a.setToast(L.PermanentSaved(), false)
		})
}

func (a *App) untrust(key string) {
	a.accessCommand(localapi.Request{Type: localapi.RequestTrustedRemove, Key: key}, func() {
		a.access.loaded = false // fetch the list again
	})
}

// accessCommand sends a request of the access card and reports the result.
// Inputs: then runs after success; nil when the pushed status is enough.
func (a *App) accessCommand(req localapi.Request, then func()) {
	a.mu.Lock()
	a.busy["access"] = true
	a.mu.Unlock()
	go func() {
		_, err := a.requestWith(req)
		a.update(func() {
			delete(a.busy, "access")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			case then != nil:
				then()
			}
		})
	}()
}

// newRequests reports the ids in next that prev did not have.
func newRequests(prev, next *localapi.Status) []localapi.HostRequest {
	var out []localapi.HostRequest
	for _, r := range next.Requests {
		if prev == nil || !slices.ContainsFunc(prev.Requests, func(p localapi.HostRequest) bool { return p.ID == r.ID }) {
			out = append(out, r)
		}
	}
	return out
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
