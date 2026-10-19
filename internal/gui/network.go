package gui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// netState is the Network page's networks: the ones kept (never a secret)
// once loaded, whether every one tries direct connections, and the form open
// for adding a network (id "") or editing one.
type netState struct {
	loaded   bool
	direct   bool
	profiles []localapi.NetworkProfile
	form     *netForm
}

// netForm is one network's editable settings. The secret field is
// write-only: it starts empty and is never filled from the engine.
type netForm struct {
	id                    string
	label, servers, name  string // servers: one URL per line
	secret                string
	hasSecret, weakSecret bool
}

// maxNetworks is how many networks the engine keeps.
const maxNetworks = 8

// networkView is the Network page: every network kept, joined or not, with
// its state and the devices it reaches, and how devices reach this one
// (direct connections, by address).
func (a *App) networkView(c *ui.Context) {
	if !a.engineState(c) {
		return
	}
	a.mu.Lock()
	loaded, busy := a.net.loaded, a.busy["network"]
	a.mu.Unlock()
	if !loaded && !busy {
		a.loadNetworks()
	}
	a.networksCard(c)
	a.connectionsCard(c)
}

// connectionsCard is how other devices reach this one: Try direct
// connections in every network (applied at once; the helper always tries
// them, so it is on and fixed in its modes) and Accept connections by
// address.
func (a *App) connectionsCard(c *ui.Context) {
	a.mu.Lock()
	net, netBusy, modeBusy := a.net, a.busy["network"], a.busy["mode"]
	a.mu.Unlock()
	helperMode := a.currentMode() != modeUser
	card(c).Children(func() {
		ui.Text(c, L.NetworkConnections()).FontSize(sizeHeading).Bold()
		directHint := L.NetworkDirectHint()
		if helperMode {
			directHint = L.NetworkDirectHelper()
		}
		direct := net.direct || helperMode
		settingRow(c, "net-direct", L.NetworkDirect(), directHint, func() {
			if ui.Switch(c.Key("net-direct-sw"), &direct).Label(L.NetworkDirect()).
				Disabled(modeBusy || netBusy || helperMode || !net.loaded).Changed() {
				a.setDirect(direct)
			}
		})
		ui.Divider(c)
		a.byAddressSettings(c)
	})
}

// networksCard lists the networks kept: each with its Join switch, Edit and
// Remove, its state and the devices it reaches, then Add a network or the
// form of a new one. A network being edited shows its form in its place.
func (a *App) networksCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	st, busy := a.net, a.busy["network"]
	states := map[string]localapi.NetworkState{}
	for _, n := range a.status.Network.Networks {
		states[n.ID] = n
	}
	// What a device said about itself in its last session here (verified
	// then); a device never connected to shows only its Device ID.
	known := map[string]localapi.About{}
	for _, r := range a.status.Recent {
		known[r.DeviceID] = a.prefs.named(r.DeviceID, r.About)
	}
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.NetworkTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.NetworksHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if !st.loaded {
			ui.Spinner(c)
			return
		}
		if len(st.profiles) == 0 && st.form == nil {
			ui.Text(c, L.NetworkNone()).TextColor(t.TextMuted)
		}
		for _, p := range st.profiles {
			ui.Divider(c)
			if st.form != nil && st.form.id == p.ID {
				a.networkForm(c, *st.form, busy)
				continue
			}
			a.networkEntry(c, p, states[p.ID], known, busy)
		}
		switch {
		case st.form != nil && st.form.id == "":
			ui.Divider(c)
			a.networkForm(c, *st.form, busy)
		case len(st.profiles) < maxNetworks:
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("net-add"), L.NetworkAdd()).Disabled(busy || st.form != nil).Clicked() {
					a.mu.Lock()
					a.net.form = &netForm{}
					a.mu.Unlock()
				}
			})
		}
		if len(st.profiles) > 0 {
			ui.Text(c, L.NetworkDevicesHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
	})
}

// networkEntry is one network: its name and network name, Join, Edit and
// Remove, its state (with the address, NAT type and port while online), and
// the devices it reaches.
func (a *App) networkEntry(c *ui.Context, p localapi.NetworkProfile, s localapi.NetworkState, known map[string]localapi.About, busy bool) {
	t := c.Theme()
	ui.Column(c.Key("net-" + p.ID)).Gap(8).FillWidth().Children(func() {
		ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, networkLabel(p)).Bold().SingleLine()
				if p.Label != "" {
					ui.Text(c, p.Name).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
				}
			})
			joined := p.Joined
			labelled(c, func() {
				if ui.Switch(c.Key("net-join-"+p.ID), &joined).Label(L.NetworkJoin()).Disabled(busy).Changed() {
					a.setJoined(p.ID, joined)
				}
			}, L.NetworkJoin())
			if ui.Button(c.Key("net-edit-"+p.ID), L.ActionEdit()).Disabled(busy).Clicked() {
				a.mu.Lock()
				a.net.form = &netForm{id: p.ID, label: p.Label, servers: strings.Join(p.Servers, "\n"), name: p.Name,
					hasSecret: p.HasSecret, weakSecret: p.WeakSecret}
				a.mu.Unlock()
			}
			if ui.Button(c.Key("net-remove-"+p.ID), L.ActionRemove()).Disabled(busy).Clicked() {
				go a.removeNetwork(p.ID, func() bool {
					return a.confirm(L.NetworkRemoveConfirm(networkLabel(p)), L.ActionRemove())
				})
			}
		})
		state := s.State
		if !p.Joined {
			state = "off"
		}
		switch state {
		case "online":
			ui.Text(c, L.NetworkOnline()).FontSize(sizeSmall).TextColor(t.Success)
		case "connecting":
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, L.NetworkConnecting()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
		case "error":
			ui.Text(c, L.NetworkError()).FontSize(sizeSmall).TextColor(t.Danger)
			ui.Text(c, s.Error).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
		default:
			ui.Text(c, L.NetworkNotJoined()).FontSize(sizeSmall).TextColor(t.TextMuted)
			return
		}
		if s.Address != "" {
			settingRow(c, "net-addr-"+p.ID, L.NetworkAddress(), "", func() {
				ui.Text(c, s.Address).FontFeatures("tnum").Selectable()
			})
		}
		if state == "online" {
			settingRow(c, "net-nat-"+p.ID, L.NetworkNAT(), "", func() { ui.Text(c, natLabel(s.NAT)) })
		}
		if s.ListenPort != 0 {
			settingRow(c, "net-port-"+p.ID, L.NetworkPort(), "", func() {
				ui.Text(c, strconv.Itoa(s.ListenPort)).FontFeatures("tnum").Selectable()
			})
		}
		if state != "online" {
			return
		}
		ui.Text(c, L.NetworkDevices()).FontSize(sizeSmall).Bold()
		if len(s.Devices) == 0 {
			ui.Text(c, L.NetworkNoDevices()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, d := range s.Devices {
			about := known[d.DeviceID]
			ui.Row(c.Key("dev-" + p.ID + "-" + d.DeviceID)).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					if about.Name != "" {
						ui.Text(c, about.Name).Bold().SingleLine()
					}
					ui.Text(c, groupDigits(d.DeviceID)).FontFeatures("tnum").Selectable()
				})
				path := L.PathRelay()
				if d.Path == "direct" {
					path = L.PathDirect()
				}
				ui.Text(c, path).FontSize(sizeSmall).TextColor(t.TextMuted)
				ui.Text(c, L.LatencyMS(d.LatencyMS)).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				if ui.Button(c.Key("dev-copy-"+p.ID+"-"+d.DeviceID), L.ActionCopy()).Clicked() {
					mygo.Clipboard.WriteText(d.DeviceID)
					a.update(func() { a.setToast(L.Copied(), false) })
				}
				if ui.Button(c.Key("dev-connect-"+p.ID+"-"+d.DeviceID), L.ActionConnect()).Clicked() {
					a.connectDevice(d.DeviceID, "", about, nil)
				}
			})
		}
	})
}

// networkForm adds a network (f.id "") or edits one: the name shown here,
// its servers, network name and secret (write-only), then Save and Cancel.
// Save needs a server and a network name.
func (a *App) networkForm(c *ui.Context, f netForm, busy bool) {
	t := c.Theme()
	key := "net-form-" + f.id
	edit := func(e ui.Element) {
		if e.Changed() {
			a.mu.Lock()
			if a.net.form != nil {
				*a.net.form = f
			}
			a.mu.Unlock()
		}
	}
	field := func(name, label, hint string, input func()) {
		ui.Column(c.Key(key + "-" + name)).Gap(4).FillWidth().Children(func() {
			ui.Text(c, label)
			input()
			if hint != "" {
				ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted)
			}
		})
	}
	ui.Column(c.Key(key)).Gap(gap).FillWidth().Children(func() {
		if f.id == "" {
			ui.Text(c, L.NetworkAdd()).Bold()
		}
		field("label", L.NetworkLabel(), L.NetworkLabelHint(), func() {
			edit(ui.TextInput(c.Key(key+"-label-in"), &f.label).FillWidth().Disabled(busy))
		})
		field("servers", L.NetworkServers(), L.NetworkServersHint(), func() {
			edit(ui.TextArea(c.Key(key+"-servers-in"), &f.servers).FillWidth().MinHeight(inputAreaH).Disabled(busy))
		})
		field("name", L.NetworkName(), "", func() {
			edit(ui.TextInput(c.Key(key+"-name-in"), &f.name).FillWidth().Disabled(busy))
		})
		secretHint := ""
		if f.hasSecret {
			secretHint = L.NetworkSecretKeep()
		}
		field("secret", L.NetworkSecret(), secretHint, func() {
			edit(ui.TextInput(c.Key(key+"-secret-in"), &f.secret).Password().FillWidth().Disabled(busy))
		})
		if f.weakSecret {
			ui.Text(c, L.NetworkSecretWeak()).FontSize(sizeSmall).TextColor(t.Warning)
		}
		ready := len(lines(f.servers)) > 0 && strings.TrimSpace(f.name) != ""
		ui.Row(c).Gap(gap).Children(func() {
			if ui.PrimaryButton(c.Key(key+"-save"), L.ActionSave()).Disabled(busy || !ready).Clicked() {
				a.saveNetwork(f)
			}
			if ui.Button(c.Key(key+"-cancel"), L.ActionCancel()).Disabled(busy).Clicked() {
				a.mu.Lock()
				a.net.form = nil
				a.mu.Unlock()
			}
		})
	})
}

// networkLabel is the name a network is shown by: the one given here, else
// its network name.
func networkLabel(p localapi.NetworkProfile) string {
	if p.Label != "" {
		return p.Label
	}
	return p.Name
}

// loadNetworks fetches the networks kept (never a secret).
func (a *App) loadNetworks() {
	a.mu.Lock()
	a.busy["network"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestNetworkProfiles})
		a.update(func() {
			delete(a.busy, "network")
			a.net.loaded = true
			if err != nil || resp.Networks == nil {
				a.setToast(L.EngineDown(), true)
				return
			}
			a.net.direct, a.net.profiles = resp.Networks.Direct, resp.Networks.Profiles
		})
	}()
}

// changeNetworks sends one request that changes the networks and takes the
// list it answers; done runs then (on the main thread, the app lock held).
// A refusal shows the engine's reason, and the page reads the networks again
// so that no switch shows what was only asked.
// Notes: takes a.mu itself, so it is never called inside a.update.
func (a *App) changeNetworks(req localapi.Request, done func()) {
	a.mu.Lock()
	a.busy["network"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(req)
		a.update(func() {
			delete(a.busy, "network")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
				a.net.loaded = false
			case err != nil || resp.Networks == nil:
				a.setToast(L.EngineDown(), true)
				a.net.loaded = false
			default:
				a.net.direct, a.net.profiles = resp.Networks.Direct, resp.Networks.Profiles
				if done != nil {
					done()
				}
			}
		})
	}()
}

// saveNetwork adds the form's network or changes it; the secret goes only
// when one was typed (the stored one stays otherwise).
// Side effects: the engine stores it and (re)starts its node.
func (a *App) saveNetwork(f netForm) {
	label, name := strings.TrimSpace(f.label), strings.TrimSpace(f.name)
	change := &localapi.NetworkChange{ID: f.id, Label: &label, Servers: lines(f.servers), Name: &name}
	if f.secret != "" {
		change.Secret = &f.secret
	}
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkProfileSet, Profile: change}, func() {
		a.net.form = nil
		a.setToast(L.NetworkSaved(), false)
	})
}

// setJoined joins a network or leaves it, shown at once.
// Side effects: the engine starts or stops its node.
func (a *App) setJoined(id string, joined bool) {
	a.mu.Lock()
	for i := range a.net.profiles {
		if a.net.profiles[i].ID == id {
			a.net.profiles[i].Joined = joined
		}
	}
	a.mu.Unlock()
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkProfileSet,
		Profile: &localapi.NetworkChange{ID: id, Joined: &joined}}, nil)
}

// removeNetwork forgets a network once confirmed (a dialog, so it runs off
// the main thread), its secret too, which leaves it.
func (a *App) removeNetwork(id string, confirmed func() bool) {
	if !confirmed() {
		return
	}
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkProfileRemove,
		Profile: &localapi.NetworkChange{ID: id}}, func() {
		if a.net.form != nil && a.net.form.id == id {
			a.net.form = nil
		}
		a.setToast(L.NetworkRemoved(), false)
	})
}

// setDirect tries direct connections in every network, or not, at once: a
// switch that looks on must be on.
// Side effects: the engine stores the choice and restarts its nodes.
func (a *App) setDirect(on bool) {
	a.mu.Lock()
	a.net.direct = on
	a.mu.Unlock()
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkDirect, Direct: &on}, func() {
		a.setToast(L.NetworkSaved(), false)
	})
}

// lines splits a field of one entry per line, dropping blank lines.
func lines(s string) []string {
	var out []string
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// natLabel words a NAT type name for people.
func natLabel(nat string) string {
	switch nat {
	case "open", "no-pat":
		return L.NatOpen()
	case "full-cone", "restricted", "port-restricted", "symmetric-easy":
		return L.NatCone()
	case "symmetric", "symmetric-firewall":
		return L.NatSymmetric()
	}
	return L.NatUnknown()
}
