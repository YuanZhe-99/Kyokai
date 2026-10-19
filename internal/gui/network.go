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

// netForm is the Network page's editable settings. The secret field is
// write-only: it starts empty and is never filled from the engine.
type netForm struct {
	loaded     bool
	servers    string // one URL per line
	name       string
	secret     string
	hasSecret  bool
	weakSecret bool
	direct     bool
}

// networkView is the Network page: the node's live status and its settings.
func (a *App) networkView(c *ui.Context) {
	if !a.engineState(c) {
		return
	}
	a.networkStatus(c)
	a.networkSettings(c)
}

func (a *App) networkStatus(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	n := a.status.Network
	// What a device said about itself in its last session here (verified
	// then); a device never connected to shows only its Device ID.
	known := map[string]localapi.About{}
	for _, r := range a.status.Recent {
		known[r.DeviceID] = a.prefs.named(r.DeviceID, r.About)
	}
	online := n.State == "online"
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.NetworkTitle()).FontSize(sizeHeading).Bold()
		switch n.State {
		case "online":
			ui.Text(c, L.NetworkOnline()).TextColor(t.Success)
		case "connecting":
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, L.NetworkConnecting()).TextColor(t.TextMuted)
			})
		case "error":
			ui.Text(c, L.NetworkError()).TextColor(t.Danger)
			ui.Text(c, n.Error).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
		default:
			ui.Text(c, L.NetworkOff()).TextColor(t.TextMuted)
			return
		}
		if n.Address != "" {
			settingRow(c, "net-addr", L.NetworkAddress(), "", func() {
				ui.Text(c, n.Address).FontFeatures("tnum").Selectable()
			})
		}
		if n.State == "online" {
			settingRow(c, "net-nat", L.NetworkNAT(), "", func() { ui.Text(c, natLabel(n.NAT)) })
		}
		if n.ListenPort != 0 {
			settingRow(c, "net-port", L.NetworkPort(), "", func() {
				ui.Text(c, strconv.Itoa(n.ListenPort)).FontFeatures("tnum").Selectable()
			})
		}
		ui.Divider(c)
		ui.Text(c, L.NetworkDevices()).Bold()
		if len(n.Devices) == 0 {
			ui.Text(c, L.NetworkNoDevices()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, d := range n.Devices {
			about := known[d.DeviceID]
			ui.Row(c.Key("dev-" + d.DeviceID)).Gap(16).AlignItems(ui.Center).Children(func() {
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
				if ui.Button(c.Key("dev-copy-"+d.DeviceID), L.ActionCopy()).Clicked() {
					mygo.Clipboard.WriteText(d.DeviceID)
					a.update(func() { a.setToast(L.Copied(), false) })
				}
				if ui.Button(c.Key("dev-connect-"+d.DeviceID), L.ActionConnect()).Disabled(!online).Clicked() {
					a.connectDevice(d.DeviceID, "", about, nil)
				}
			})
		}
		ui.Text(c, L.NetworkDevicesHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
}

func (a *App) networkSettings(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, busy := a.net, a.busy["network"]
	a.mu.Unlock()
	if !f.loaded && !busy {
		a.loadNetwork()
	}
	card(c).Children(func() {
		ui.Text(c, L.NetworkSettings()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.NetworkSettingsHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if !f.loaded {
			ui.Spinner(c)
			return
		}
		field := func(key, label, hint string, input func()) {
			ui.Column(c.Key(key)).Gap(4).FillWidth().Children(func() {
				ui.Text(c, label)
				input()
				if hint != "" {
					ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted)
				}
			})
		}
		edit := func(e ui.Element) {
			if e.Changed() {
				a.mu.Lock()
				a.net = f
				a.mu.Unlock()
			}
		}
		field("net-servers", L.NetworkServers(), L.NetworkServersHint(), func() {
			edit(ui.TextArea(c.Key("net-servers-in"), &f.servers).FillWidth().MinHeight(inputAreaH).Disabled(busy))
		})
		field("net-name", L.NetworkName(), "", func() {
			edit(ui.TextInput(c.Key("net-name-in"), &f.name).FillWidth().Disabled(busy))
		})
		secretHint := ""
		if f.hasSecret {
			secretHint = L.NetworkSecretKeep()
		}
		field("net-secret", L.NetworkSecret(), secretHint, func() {
			edit(ui.TextInput(c.Key("net-secret-in"), &f.secret).Password().FillWidth().Disabled(busy))
		})
		if f.weakSecret {
			ui.Text(c, L.NetworkSecretWeak()).FontSize(sizeSmall).TextColor(t.Warning)
		}
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key("net-save"), L.ActionSave()).Disabled(busy).Clicked() {
				a.saveNetwork(f, false)
			}
			if f.hasSecret || strings.TrimSpace(f.servers+f.name) != "" {
				if ui.Button(c.Key("net-clear"), L.ActionClearNetwork()).Disabled(busy).Clicked() {
					go a.clearNetwork(f, func() bool { return a.confirm(L.NetworkClearConfirm(), L.ActionClearNetwork()) })
				}
			}
		})
	})
}

// loadNetwork fetches the stored settings (never the secret) into the form.
func (a *App) loadNetwork() {
	a.mu.Lock()
	a.busy["network"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestNetworkGet})
		a.update(func() {
			delete(a.busy, "network")
			if err != nil || resp.Network == nil {
				a.setToast(L.EngineDown(), true)
				a.net.loaded = true
				return
			}
			a.net = formFrom(resp.Network)
		})
	}()
}

// clearNetwork forgets the network settings once confirmed (a dialog,
// so it runs off the main thread).
func (a *App) clearNetwork(f netForm, confirmed func() bool) {
	if confirmed() {
		a.saveNetwork(f, true)
	}
}

// saveNetwork sends the form to the engine (the servers, name and secret; the stored direct choice stays); clear forgets them all,
// the secret too, which turns the node off.
// Side effects: the engine stores the settings and reconnects.
// Notes: takes a.mu itself, so it is never called inside a.update.
func (a *App) saveNetwork(f netForm, clear bool) {
	// Direct connections are set in Settings, Run mode (setDirect).
	ns := &localapi.NetworkSettings{Servers: lines(f.servers), Name: strings.TrimSpace(f.name)}
	if clear {
		none := ""
		ns.Servers, ns.Name, ns.Secret = nil, "", &none
	} else if f.secret != "" {
		ns.Secret = &f.secret
	}
	a.mu.Lock()
	a.busy["network"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestNetworkSet, Network: ns})
		a.update(func() {
			delete(a.busy, "network")
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil || resp.Network == nil:
				a.setToast(L.EngineDown(), true)
			default:
				a.net = formFrom(resp.Network)
				if clear {
					a.setToast(L.NetworkCleared(), false)
				} else {
					a.setToast(L.NetworkSaved(), false)
				}
			}
		})
	}()
}

// setDirect turns direct connections on or off at once, with the servers
// and name as stored: a switch that looks on must be on. Unsaved edits of the
// other fields stay in the form.
// Side effects: the engine stores the setting and restarts its node.
func (a *App) setDirect(on bool) {
	a.mu.Lock()
	a.busy["network"] = true
	a.net.direct = on
	a.mu.Unlock()
	go func() {
		stored, err := a.requestWith(localapi.Request{Type: localapi.RequestNetworkGet})
		var resp *localapi.Response
		if err == nil && stored.Network != nil {
			ns := &localapi.NetworkSettings{Servers: stored.Network.Servers, Name: stored.Network.Name, Direct: &on}
			resp, err = a.requestWith(localapi.Request{Type: localapi.RequestNetworkSet, Network: ns})
		}
		a.update(func() {
			delete(a.busy, "network")
			if err != nil || resp == nil || resp.Network == nil {
				a.net.direct = !on
				a.setToast(L.EngineDown(), true)
				return
			}
			a.net.direct = resp.Network.Direct != nil && *resp.Network.Direct
			a.setToast(L.NetworkSaved(), false)
		})
	}()
}

func formFrom(ns *localapi.NetworkSettings) netForm {
	return netForm{
		loaded: true, servers: strings.Join(ns.Servers, "\n"), name: ns.Name,
		hasSecret: ns.HasSecret, weakSecret: ns.WeakSecret, direct: ns.Direct != nil && *ns.Direct,
	}
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
