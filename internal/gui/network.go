package gui

import (
	"errors"
	"strings"

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
		ui.Divider(c)
		ui.Text(c, L.NetworkDevices()).Bold()
		if len(n.Devices) == 0 {
			ui.Text(c, L.NetworkNoDevices()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, d := range n.Devices {
			ui.Row(c.Key("dev-" + d.DeviceID)).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, groupDigits(d.DeviceID)).FontFeatures("tnum").Selectable().Grow(1)
				path := L.PathRelay()
				if d.Path == "direct" {
					path = L.PathDirect()
				}
				ui.Text(c, path).FontSize(sizeSmall).TextColor(t.TextMuted)
				ui.Text(c, L.LatencyMS(d.LatencyMS)).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
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
		helperMode := a.currentMode() != modeUser
		directHint := L.NetworkDirectHint()
		if helperMode {
			directHint = L.NetworkDirectHelper()
		}
		settingRow(c, "net-direct", L.NetworkDirect(), directHint, func() {
			edit(ui.Switch(c.Key("net-direct-sw"), &f.direct).Label(L.NetworkDirect()).Disabled(busy || helperMode))
		})
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key("net-save"), L.ActionSave()).Disabled(busy).Clicked() {
				a.saveNetwork(f, false)
			}
			if f.hasSecret || strings.TrimSpace(f.servers) != "" {
				if ui.Button(c.Key("net-leave"), L.ActionLeaveNetwork()).Disabled(busy).Clicked() {
					a.saveNetwork(f, true)
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

// saveNetwork sends the form to the engine; leave clears the servers and
// the name, which turns the node off.
// Side effects: the engine stores the settings and reconnects.
func (a *App) saveNetwork(f netForm, leave bool) {
	direct := f.direct
	ns := &localapi.NetworkSettings{Servers: serverLines(f.servers), Name: strings.TrimSpace(f.name), Direct: &direct}
	if leave {
		ns.Servers, ns.Name = nil, ""
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
				if leave {
					a.setToast(L.NetworkLeft(), false)
				} else {
					a.setToast(L.NetworkSaved(), false)
				}
			}
		})
	}()
}

func formFrom(ns *localapi.NetworkSettings) netForm {
	return netForm{
		loaded: true, servers: strings.Join(ns.Servers, "\n"), name: ns.Name,
		hasSecret: ns.HasSecret, weakSecret: ns.WeakSecret, direct: ns.Direct != nil && *ns.Direct,
	}
}

// serverLines splits the servers field into URLs, dropping blank lines.
func serverLines(s string) []string {
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
