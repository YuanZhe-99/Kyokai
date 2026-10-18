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
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// connectForm is the Home page's "connect to another device" form.
type connectForm struct {
	deviceID string
	// ai also asks to run AI tasks on the host and approve their actions.
	ai bool
}

// accessForm is the Settings page's permanent password field and the
// trusted devices last fetched (nil until fetched).
type accessForm struct {
	permanent string
	trusted   []localapi.TrustedDevice
	loaded    bool
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
	grants := a.grants
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
				ui.Text(c, L.RequestTitle(deviceTitle(r.About, r.DeviceID))).FontSize(sizeHeading).Bold()
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
	a.mu.Unlock()
	for _, s := range here {
		key := "here-" + itoa(s.ID)
		card(c.Key(key)).Border(2, t.Warning).Children(func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, s.Peer)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, L.HostConnected(deviceTitle(s.Peer, s.DeviceID))).Bold()
					ui.Text(c, sessionState(s)).FontSize(sizeSmall).TextColor(t.TextMuted)
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
		id := ui.TextInput(c.Key("conn-id"), &f.deviceID).Placeholder("482 916 735").FillWidth().FontSize(sizeTitle).FontFeatures("tnum")
		if id.Changed() {
			a.mu.Lock()
			a.connect.deviceID = f.deviceID
			a.mu.Unlock()
		}
		device := strings.Join(strings.Fields(f.deviceID), "")
		ready := online && len(device) == 9
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if !online {
				ui.Text(c, L.ConnectNeedsNetwork()).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
			} else {
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
				a.connectDevice(device, a.recentAbout(device), connectWant(f.ai))
			}
		})
	})
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
	want := []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard, localapi.PermFiles}
	if ai {
		want = append(want, localapi.PermRunAITask, localapi.PermApproveAIAction)
	}
	return want
}

// deviceRow is one device on Home: what it said about itself and its
// connection from here, if any.
type deviceRow struct {
	id    string
	about localapi.About
	last  int64
	saved bool
	// session is the newest connection to it in the engine's list, or nil.
	session *localapi.SessionInfo
}

// deviceRows are the devices this one connected to, newest first, with
// the ones being connected to for the first time.
func deviceRows(st *localapi.Status) []deviceRow {
	var rows []deviceRow
	index := map[string]int{}
	for _, r := range st.Recent {
		index[r.DeviceID] = len(rows)
		rows = append(rows, deviceRow{id: r.DeviceID, about: r.About, last: r.Last, saved: r.SavedPassword})
	}
	for _, s := range st.Sessions {
		if s.Role != "controller" {
			continue
		}
		i, ok := index[s.DeviceID]
		if !ok {
			index[s.DeviceID] = len(rows)
			rows = append(rows, deviceRow{id: s.DeviceID, about: s.Peer})
			i = len(rows) - 1
		}
		if cur := rows[i].session; cur == nil || s.ID > cur.ID {
			s := s
			rows[i].session = &s
		}
	}
	return rows
}

// devicesCard is Home's recent devices, one card each as RustDesk shows
// them: a click connects (or shows its tab), its menu connects with AI,
// forgets its password or old identity, or removes it.
func (a *App) devicesCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	rows := deviceRows(a.status)
	online := map[string]bool{}
	for _, d := range a.status.Network.Devices {
		online[d.DeviceID] = true
	}
	a.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	ui.Text(c, L.ConnectRecent()).FontSize(sizeHeading).Bold()
	ui.Row(c).FillWidth().Wrap().Gap(gap).AlignItems(ui.Start).Children(func() {
		for _, r := range rows {
			a.deviceCard(c, t, r, online[r.id])
		}
	})
}

// deviceCard is one device's card.
func (a *App) deviceCard(c *ui.Context, t *ui.Theme, r deviceRow, online bool) {
	state, col := L.DeviceLast(time.Unix(r.last, 0).Local().Format(time.DateTime)), t.TextMuted
	if r.last == 0 {
		state = ""
	}
	if s := r.session; s != nil {
		switch s.State {
		case "connected":
			state, col = L.NetworkOnline(), t.Success
		case "closed":
		case "failed":
			state, col = failureText(s.Failure), t.Danger
		default:
			state = sessionState(*s)
		}
	}
	keyChanged := r.session != nil && r.session.State == "failed" && string(r.session.Failure) == `"key-changed"`
	box := ui.ButtonBase(c.Key("device-"+r.id)).Width(deviceCardW).Radius(cardRadius).Background(t.Surface).Border(1, t.Border).Label(deviceTitle(r.about, r.id))
	if box.Clicked() {
		a.connectDevice(r.id, r.about, nil)
	}
	box.Children(func() {
		ui.Column(c).FillWidth().Padding(12).Gap(8).Children(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, r.about)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					name := r.about.Name
					if name == "" {
						name = groupDigits(r.id)
					}
					ui.Text(c, name).Bold().Ellipsis("…").NoWrap()
					if r.about.Name != "" {
						ui.Text(c, groupDigits(r.id)).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
					}
				})
				more := ui.ButtonBase(c.Key("device-menu-"+r.id)).Padding(2, 8).Radius(6).Label(L.DeviceMenu()).Tooltip(L.DeviceMenu())
				more.Children(func() { ui.Text(c, "⋯").TextColor(t.TextMuted) })
				more.Menu(func(m *ui.Menu) {
					if m.Item(L.ActionConnect()).Chosen() {
						a.connectDevice(r.id, r.about, nil)
					}
					if m.Item(L.ActionConnectAI()).Chosen() {
						a.connectDevice(r.id, r.about, connectWant(true))
					}
					m.Separator()
					if r.saved && m.Item(L.ActionForgetPassword()).Chosen() {
						a.forgetPassword(r.id)
					}
					if keyChanged && m.Item(L.ActionForgetIdentity()).Chosen() {
						device := r.id
						go func() { _, _ = a.requestWith(localapi.Request{Type: localapi.RequestHostForget, DeviceID: device}) }()
					}
					if m.Item(L.ActionRemove()).Chosen() {
						a.removeDevice(r.id)
					}
				})
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				dot := t.TextMuted.Alpha(0.5)
				if online {
					dot = t.Success
				}
				ui.Box(c).Width(onlineDot).Height(onlineDot).Radius(onlineDot / 2).Background(dot)
				ui.Text(c, state).FontSize(sizeSmall).TextColor(col).Ellipsis("…").NoWrap().Grow(1)
			})
			if r.saved {
				ui.Text(c, L.ConnectPasswordSaved()).FontSize(sizeSmall).TextColor(t.TextMuted)
			}
		})
	})
}

// forgetPassword forgets the password saved for a device.
func (a *App) forgetPassword(device string) {
	go func() {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestForgetPassword, DeviceID: device})
		a.update(func() { a.setToast(L.PasswordForgotten(), false) })
	}()
}

// removeDevice takes a device off Home: its connection ends, and its
// pinned key and saved password are forgotten.
// Notes: call on the main thread.
func (a *App) removeDevice(device string) {
	if v := a.viewerFor(device); v != nil {
		if w := v.home.Load(); w != nil {
			w.closeTab(v)
		} else {
			v.disconnect()
		}
	}
	go func() {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestForgetPassword, DeviceID: device})
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestHostForget, DeviceID: device})
	}()
}

// sessionState words a session's state, permissions or failure.
func sessionState(s localapi.SessionInfo) string {
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
	return failureText(s.Failure)
}

// failureText words why a session failed.
func failureText(raw json.RawMessage) string {
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
	}
	return p
}

// accessSettings is Settings' card for the permanent password and the
// trusted devices.
func (a *App) accessSettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy, hasPermanent := a.access, a.busy["access"], a.status.PermanentPassword
	grants := slices.Clone(a.status.PasswordGrants)
	a.mu.Unlock()
	if !f.loaded && !busy {
		a.loadTrusted()
	}
	card(c).Children(func() {
		ui.Text(c, L.SettingsAccess()).FontSize(sizeHeading).Bold()
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
		a.passwordGrants(c, grants, busy)
		ui.Divider(c)
		ui.Text(c, L.TrustedTitle())
		ui.Text(c, L.TrustedHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if f.loaded && len(f.trusted) == 0 {
			ui.Text(c, L.TrustedNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, d := range f.trusted {
			hint := L.TrustedLast(time.Unix(d.Last, 0).Local().Format(time.DateTime))
			if d.UnattendedUntil != nil {
				hint = L.TrustedUnattendedUntil(time.Unix(*d.UnattendedUntil, 0).Local().Format(time.DateTime))
			}
			settingRow(c, "trust-"+d.Key, deviceTitle(d.About, d.DeviceID), hint, func() {
				on := d.UnattendedUntil != nil
				sw := ui.Switch(c.Key("trust-un-"+d.Key), &on).Label(L.TrustedUnattended()).Disabled(busy || !hasPermanent && !on)
				if !hasPermanent && !on {
					sw.Tooltip(L.TrustedUnattendedNeedsPermanent())
				} else {
					sw.Tooltip(L.TrustedUnattendedHint())
				}
				if sw.Changed() {
					days := 0
					if on {
						days = 30
					}
					a.accessCommand(localapi.Request{Type: localapi.RequestUnattended, Key: d.Key, Days: days}, func() {
						a.access.loaded = false // fetch the list again
					})
				}
				ui.Text(c, L.TrustedUnattended()).FontSize(sizeSmall)
				if ui.Button(c.Key("trust-rm-"+d.Key), L.ActionRemove()).Disabled(busy).Clicked() {
					a.untrust(d.Key)
				}
			})
		}
	})
}

// allPermissions are the permissions in the order Settings lists them.
var allPermissions = []string{
	localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard,
	localapi.PermFiles, localapi.PermRunAITask, localapi.PermApproveAIAction,
}

// passwordGrants is the Access card's choice of what a correct password
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
			default:
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
