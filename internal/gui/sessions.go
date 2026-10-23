package gui

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// connectForm is Home's Connect to a device.
type connectForm struct {
	// target is a Device ID or an address, as typed.
	target string
	// as is what Open as opens: "" the desktop, a service, or actionAI (the
	// desktop asking to run AI tasks there and approve their actions).
	as string
	// identifying: the device at an address is being asked for its ID.
	identifying bool
}

// renameForm is the name being given to a recent device (Home), while
// device is set.
type renameForm struct {
	device string
	name   string
}

// accessForm is the Access and Network pages' state: the permanent
// password's form (open, and what is typed in it), whether the keys that
// can connect are shown, the connections-by-address ranges and the service
// ports while edited (nil follows the engine), and the trusted devices last
// fetched (nil until fetched).
type accessForm struct {
	permanentOpen bool
	permanent     string
	keysOpen      bool
	ranges        *string
	// ports are the service ports while edited (nil follows the engine).
	ports   *[3]string
	trusted []localapi.TrustedDevice
	loaded  bool
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
// every page, each a card in the accent (noticeCard): who it is — its
// name, then its Device ID and whether it connected before — with Deny
// and Allow across from it; what to beware of (no password, another key
// for a Device ID known here, macOS permissions missing) in one callout;
// what it asks for as tiles that toggle, as wide as the card
// (askColumns).
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
		name, facts := whoIs(prefs.named(r.DeviceID, r.About), r.DeviceID)
		var warnings []string
		switch {
		case r.NewKey:
			warnings = append(warnings, L.RequestNewKey())
		case r.Known:
			facts = append(facts, L.RequestKnown())
		default:
			facts = append(facts, L.RequestFirstTime())
		}
		if !r.Password {
			warnings = append(warnings, L.RequestNoPassword())
		}
		if perms != nil && (slices.Contains(r.Want, localapi.PermViewScreen) && !perms.Screen ||
			slices.Contains(r.Want, localapi.PermControlInput) && !perms.Accessibility) {
			warnings = append(warnings, L.RequestPermissionsMissing())
		}
		chosen, ok := grants[r.ID]
		if !ok {
			chosen = slices.Clone(r.Want)
		}
		noticeCard(c, key, t.Accent, func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, r.About)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, L.RequestTitle(name)).FontWeight(600)
					ui.Text(c, strings.Join(facts, " · ")).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
				if ui.Button(c.Key(key+"-deny"), L.ActionDeny()).Clicked() {
					a.decide(r.ID, false, nil)
				}
				if ui.PrimaryButton(c.Key(key+"-allow"), L.ActionAllow()).Clicked() {
					a.decide(r.ID, true, chosen)
				}
			})
			if len(warnings) > 0 {
				// The warning's tint on the page's face, not on the accent's.
				ui.Column(c.Key(key + "-warn")).FillWidth().Radius(tileRadius).Clip().Background(t.Background).Children(func() {
					ui.Column(c).FillWidth().Gap(6).Padding(10, 12).Background(t.Warning.Alpha(0.12)).Children(func() {
						for i, w := range warnings {
							ui.Row(c.Key(key + "-warn-" + strconv.Itoa(i))).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
								ui.Icon(c, rowAlert).FontSize(statusIconSize).TextColor(t.Warning).Shrink(0).Margin(1, 0, 0, 0)
								ui.Text(c, w).FontSize(sizeSmall).Grow(1).MinWidth(0)
							})
						}
					})
				})
			}
			ui.Text(c, L.RequestAsks()).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Margin(2, 0, 0, 0)
			asks := ui.Grid(c.Key(key + "-asks")).FillWidth().Gap(tileGap)
			asks.Columns(askColumns(asks.LayoutBounds().W, len(r.Want))).Children(func() {
				for _, p := range r.Want {
					on := slices.Contains(chosen, p)
					if toggleTile(c, key+"-"+p, permissionIcon(p), permissionShort(p), &on, false) {
						next := slices.DeleteFunc(slices.Clone(chosen), func(x string) bool { return x == p })
						if on {
							next = append(next, p)
						}
						a.mu.Lock()
						a.grants[r.ID] = next
						a.mu.Unlock()
					}
				}
			})
		})
	}
}

// askColumns are how many of a request's n permission tiles stand side by
// side in a row w wide (the last frame's; 0 before the first): all of
// them, as wide as the card, while each keeps askTileMinW; else the fewest
// rows that fit, as even as they go (five in rows of three and two, not
// four and one).
func askColumns(w float32, n int) int {
	fit := 4
	if w > 0 {
		fit = int((w + tileGap) / (askTileMinW + tileGap))
	}
	fit = max(1, min(n, fit))
	rows := (n + fit - 1) / fit
	return max(1, (n+rows-1)/max(rows, 1))
}

// whoIs is a device as a card above the panes names it: its name (its
// Device ID without one), and the facts under it, its Device ID first
// when the name took its place.
func whoIs(about localapi.About, id string) (name string, facts []string) {
	if about.Name == "" {
		return groupDigits(id), nil
	}
	return about.Name, []string{groupDigits(id)}
}

// hostBanner shows, on every page, each device connected to this one
// right now as a card in the warning colour (noticeCard): who it is, what
// it may do, and Disconnect, so the host's user always sees who is here
// and can end it.
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
		name, facts := whoIs(prefs.named(s.DeviceID, s.Peer), s.DeviceID)
		facts = append(facts, sessionState(s, a.outdatedHelper()))
		noticeCard(c, key, t.Warning, func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, s.Peer)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, L.HostConnected(name)).FontWeight(600)
					ui.Text(c, strings.Join(facts, " · ")).FontSize(sizeSmall).TextColor(t.TextMuted)
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

// identify asks the device at an address for its Device ID, then opens it
// there (open, called on the main thread).
// Side effects: a toast when no device answers.
func (a *App) identify(address string, open func(device, address string)) {
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
		// On the main thread and not under a.mu, which what opens takes
		// itself.
		if err == nil && resp.DeviceID != "" {
			a.later(func() { open(resp.DeviceID, address) })
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
	if a.status == nil {
		return localapi.About{}
	}
	for _, r := range a.status.Recent {
		if r.DeviceID == device {
			return r.About
		}
	}
	return localapi.About{}
}

// desktopWant is what a desktop's connection asks the host for: the
// screen, the mouse and keyboard, the clipboard, files and sound (its AI
// asks on a link of its own, aiWant).
var desktopWant = []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard, localapi.PermFiles, localapi.PermHearSound}

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
	// routes are its addresses besides the Kyokai network, netRoute how the
	// network went, and prefer the route chosen by hand (empty: Auto).
	routes   []localapi.Route
	netRoute localapi.RouteStats
	prefer   string
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
		rows = append(rows, deviceRow{id: r.DeviceID, about: p.named(r.DeviceID, r.About), last: r.Last, saved: saved, features: r.Features,
			routes: r.Routes, netRoute: r.NetworkRoute, prefer: r.Prefer})
	}
	for _, s := range st.Sessions {
		if !desktopController(s) {
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

// renameCard edits the name a recent device has on this device only: a
// card of Home's over its devices, the device's avatar and what the name
// is for, then the name (taking the keys: Enter saves it, Escape leaves
// it), Cancel and Save.
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
	homeCard(c, "rename-"+f.device).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			deviceAvatar(c, about)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.RenameTitle(deviceTitle(about, f.device))).FontWeight(600)
				ui.Text(c, L.RenameHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			in := ui.TextInput(c.Key("rename-in"), &f.name).Placeholder(about.Name).Label(L.RenameTitle(deviceTitle(about, f.device))).Grow(1).MinWidth(0).AutoFocus()
			if in.Changed() {
				a.mu.Lock()
				a.rename.name = f.name
				a.mu.Unlock()
			}
			if ui.Button(c.Key("rename-cancel"), L.ActionCancel()).Clicked() || in.Shortcut(0, ui.KeyEscape) {
				a.mu.Lock()
				a.rename = renameForm{}
				a.mu.Unlock()
			}
			if ui.PrimaryButton(c.Key("rename-save"), L.ActionSave()).Clicked() || in.Submitted() {
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

// passwordsAccepted are the two passwords' check boxes for an accepted
// passwords mode (an engine that says nothing accepts both).
func passwordsAccepted(mode string) (temporary, permanent bool) {
	switch mode {
	case "temporary":
		return true, false
	case "permanent":
		return false, true
	case "none":
		return false, false
	}
	return true, true
}

// passwordMode is the accepted passwords mode for the two check boxes.
func passwordMode(temporary, permanent bool) string {
	switch {
	case temporary && permanent:
		return "both"
	case temporary:
		return "temporary"
	case permanent:
		return "permanent"
	}
	return "none"
}

// protected reports whether protected mode is on (caller holds a.mu).
func (a *App) protected() bool {
	// The last status counts while the engine is away.
	return a.status != nil && a.status.Protected
}

// protectedSetting is Privacy's protected mode: on only with a permanent
// password, after a confirmation.
func (a *App) protectedSetting(c *ui.Context, r *rows) {
	a.mu.Lock()
	hasPermanent, busy := a.status.PermanentPassword, a.busy["access"]
	a.mu.Unlock()
	on := false
	s := setting{title: L.ProtectedTitle(), about: L.ProtectedAbout(), more: L.ProtectedHint()}
	if !hasPermanent {
		s.about = L.ProtectedNeedsPermanent()
	}
	r.row(c, "protected", tile(navSecurity), s, func() {
		if ui.Switch(c.Key("protected-sw"), &on).Label(L.ProtectedTitle()).Disabled(busy || !hasPermanent).Changed() && on {
			// A dialog: off the main thread.
			go func() {
				if a.confirm(L.ProtectedConfirm(), L.ProtectedTurnOn()) {
					a.accessCommand(localapi.Request{Type: localapi.RequestProtect, Protect: &localapi.ProtectRequest{On: true}}, nil)
				}
			}()
		}
	})
}

// protectedCard tops protected mode's session (noticeCard): what it is,
// and across from it the permanent password to turn it off (Enter too).
func (a *App) protectedCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	password, busy := a.unlock, a.busy["access"]
	a.mu.Unlock()
	noticeCard(c, "protected-card", ui.Color{}, func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			noticeIcon(c, aiShield, t.Accent)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.ProtectedTitle()).FontWeight(600)
				ui.Text(c, L.ProtectedOnHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
			in := ui.TextInput(c.Key("unlock-in"), &password).Password().Placeholder(L.PermanentTitle()).Label(L.PermanentTitle()).Width(unlockW).Shrink(1).MinWidth(0).Disabled(busy)
			if in.Changed() {
				a.mu.Lock()
				a.unlock = password
				a.mu.Unlock()
			}
			if ui.Button(c.Key("unlock-go"), L.ProtectedTurnOff()).Disabled(busy || password == "").Clicked() || in.Submitted() && password != "" && !busy {
				a.mu.Lock()
				a.unlock = ""
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

// byAddressRows are the Network page's connections by address: the switch,
// and while it is on the ranges they are accepted from (edited, then saved).
func (a *App) byAddressRows(c *ui.Context, r *rows) {
	t := c.Theme()
	a.mu.Lock()
	by, edited, busy := a.status.ByAddress, a.access.ranges, a.busy["access"]
	a.mu.Unlock()
	on := by.On
	s := setting{title: L.ByAddressTitle(), about: L.ByAddressAbout(), more: L.ByAddressHint()}
	if by.On && by.Port == 0 {
		s.about, s.col = L.ByAddressFailed(), t.Warning
	}
	r.row(c, "by-address", tile(rowGlobe), s, func() {
		if ui.Switch(c.Key("by-address-sw"), &on).Label(L.ByAddressTitle()).Disabled(busy).Changed() {
			a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings,
				ByAddress: &localapi.ByAddress{On: on, Ranges: by.Ranges}}, nil)
		}
	})
	if !by.On {
		return
	}
	ranges := strings.Join(by.Ranges, "\n")
	if edited != nil {
		ranges = *edited
	}
	r.block(c, "by-address-ranges", func() {
		ui.Text(c, L.ByAddressRanges()).FontWeight(500)
		if ui.TextArea(c.Key("by-address-ranges-in"), &ranges).FillWidth().MinHeight(inputAreaH).Disabled(busy).Changed() {
			a.mu.Lock()
			a.access.ranges = &ranges
			a.mu.Unlock()
		}
		ui.Text(c, L.ByAddressRangesHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if edited == nil {
			return
		}
		ui.Row(c).Gap(gap).FillWidth().Children(func() {
			ui.Box(c).Grow(1)
			if ui.Button(c.Key("by-address-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
				a.mu.Lock()
				a.access.ranges = nil
				a.mu.Unlock()
			}
			if ui.PrimaryButton(c.Key("by-address-save"), L.ActionSave()).Disabled(busy).Clicked() {
				a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings,
					ByAddress: &localapi.ByAddress{On: true, Ranges: append([]string{}, lines(ranges)...)}}, func() {
					a.access.ranges = nil
					a.setToast(L.ByAddressSaved(), false)
				})
			}
		})
	})
}

// allPermissions are the permissions in the order the Permissions page lists them.
var allPermissions = []string{
	localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard,
	localapi.PermFiles, localapi.PermHearSound, localapi.PermRunAITask, localapi.PermApproveAIAction,
	localapi.PermSeeStatus, localapi.PermUseSSH, localapi.PermUseRDP, localapi.PermUseVNC,
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

// desktopController reports whether a session is this device's desktop
// connection to another: a controller's, not a tunnel to one of its
// services, which belongs to its service tab (regression: a refused tunnel
// opened a connection tab of its own asking for the Kyokai password).
func desktopController(s localapi.SessionInfo) bool {
	return s.Role == "controller" && s.Service == ""
}

// servicePortNames are the services whose ports the Permissions page sets,
// in its order.
var servicePortNames = [3]string{serviceSSH, serviceRDP, serviceVNC}

// standardPorts are SSH's, Remote Desktop's and VNC's standard ports.
var standardPorts = [3]int{22, 3389, 5900}

// portFields are the engine's service ports as the page's fields: empty
// for a standard port (an older engine reports none).
func portFields(p localapi.ServicePorts) [3]string {
	var out [3]string
	for i, port := range [3]int{p.SSH, p.RDP, p.VNC} {
		if port != 0 && port != standardPorts[i] {
			out[i] = strconv.Itoa(port)
		}
	}
	return out
}

// parsePorts reads the page's fields: empty is the standard port.
// Returns: the ports to set, and false when a field is not a port
// (1–65535).
func parsePorts(fields [3]string) (localapi.ServicePorts, bool) {
	var ports [3]int
	for i, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > 65535 {
			return localapi.ServicePorts{}, false
		}
		ports[i] = n
	}
	return localapi.ServicePorts{SSH: ports[0], RDP: ports[1], VNC: ports[2]}, true
}
