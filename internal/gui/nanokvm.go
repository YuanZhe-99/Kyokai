package gui

import (
	"slices"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
	"github.com/egoist/mygo/ui"
)

// kvmForm is Access's NanoKVM USB while it is edited: the capture cards and
// serial ports the engine found (nil until it answers), and the devices
// chosen before it is offered (empty: the saved ones, else the pair found).
type kvmForm struct {
	devices       *localapi.NanokvmDevices
	looking       bool
	video, serial string
}

// loadNanokvmDevices asks the engine for its capture cards and serial
// ports; again asks again.
func (a *App) loadNanokvmDevices(again bool) {
	a.mu.Lock()
	if a.kvm.looking || a.kvm.devices != nil && !again {
		a.mu.Unlock()
		return
	}
	a.kvm.looking = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestNanokvmDevices})
		a.update(func() {
			a.kvm.looking = false
			if err == nil && resp != nil && resp.NanokvmDevices != nil {
				a.kvm.devices = resp.NanokvmDevices
			} else if a.kvm.devices == nil {
				a.kvm.devices = &localapi.NanokvmDevices{}
			}
		})
	}()
}

// kvmChoice is the device chosen among ports: the saved one, else the one
// picked here, else the one suggested; with the ids and labels of a
// Select, a saved device that is not there now labelled so.
func kvmChoice(ports []localapi.NanokvmPort, saved, picked, suggested string) (id string, ids, labels []string) {
	for _, p := range ports {
		ids, labels = append(ids, p.ID), append(labels, p.Name)
	}
	id = saved
	if id == "" {
		id = picked
	}
	if id == "" {
		id = suggested
	}
	if id != "" && !slices.Contains(ids, id) {
		ids, labels = append(ids, id), append(labels, L.NanokvmMissing(id))
	}
	return id, ids, labels
}

// nanokvmSection is Access's NanoKVM USB: the capture card and the serial
// port of the computer wired to this one, and whether other devices may
// use it (doc/en-us/remote-services.md, "NanoKVM USB"). An engine before
// 0.6.8 has none.
func (a *App) nanokvmSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	saved, f, busy := a.status.Nanokvm, a.kvm, a.busy["access"]
	a.mu.Unlock()
	if saved == nil {
		return
	}
	a.loadNanokvmDevices(false)
	var found localapi.NanokvmDevices
	var suggested localapi.Nanokvm
	if f.devices != nil {
		found = *f.devices
		if found.Suggested != nil {
			suggested = *found.Suggested
		}
	}
	video, videoIDs, videoLabels := kvmChoice(found.Video, saved.Video, f.video, suggested.Video)
	serial, serialIDs, serialLabels := kvmChoice(found.Serial, saved.Serial, f.serial, suggested.Serial)
	// A choice is kept at once: this computer opens its own NanoKVM with
	// the devices saved, offered to others or not.
	set := func(k localapi.Nanokvm) {
		a.mu.Lock()
		a.kvm.video, a.kvm.serial = k.Video, k.Serial
		a.mu.Unlock()
		a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, Nanokvm: &k}, func() {
			a.kvm.video, a.kvm.serial = "", ""
		})
	}
	a.section(c, "nanokvm", L.ServiceNanokvm(), nil, func(r *rows) {
		row := func(key string, icon *ui.SVG, s setting, cur string, ids, labels []string, apply func(string)) {
			r.row(c, key, tile(icon), s, func() {
				if len(ids) == 0 {
					ui.Text(c, L.NanokvmNoneFound()).FontSize(sizeSmall).TextColor(t.TextMuted)
					return
				}
				shown := ""
				if i := slices.Index(ids, cur); i >= 0 {
					shown = labels[i]
				}
				if ui.Select(c.Key(key+"-sel"), &shown, labels).Width(280).Label(s.title).Disabled(busy).Changed() {
					if i := slices.Index(labels, shown); i >= 0 {
						apply(ids[i])
					}
				}
			})
		}
		row("kvm-video", barDisplay, setting{title: L.NanokvmVideo(), about: L.NanokvmVideoAbout(), more: L.NanokvmHint()}, video, videoIDs, videoLabels, func(id string) {
			set(localapi.Nanokvm{On: saved.On, Video: id, Serial: serial})
		})
		row("kvm-serial", rowPlug, setting{title: L.NanokvmSerial(), about: L.NanokvmSerialAbout()}, serial, serialIDs, serialLabels, func(id string) {
			set(localapi.Nanokvm{On: saved.On, Video: video, Serial: id})
		})
		on := saved.On
		ready := video != "" && serial != ""
		r.row(c, "kvm-on", tile(kindNanokvm), setting{title: L.NanokvmOffer(), about: L.NanokvmOfferAbout()}, func() {
			if ui.Switch(c.Key("kvm-on-sw"), &on).Label(L.NanokvmOffer()).Disabled(busy || !ready && !saved.On).Changed() {
				set(localapi.Nanokvm{On: on, Video: video, Serial: serial})
			}
		})
		r.block(c, "kvm-look", func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
				switch {
				case f.looking:
					busyMark(c, L.NanokvmLooking(), busySize)
				case found.Suggested != nil:
					ui.Text(c, L.NanokvmFound()).FontSize(sizeSmall).TextColor(t.TextMuted)
				}
				ui.Box(c).Grow(1)
				if ui.Button(c.Key("kvm-look-btn"), L.NanokvmLookAgain()).Disabled(f.looking).Clicked() {
					a.loadNanokvmDevices(true)
				}
				if ui.PrimaryButton(c.Key("kvm-open-btn"), L.ActionOpen()).Disabled(!a.ownKvmReady()).Clicked() {
					a.openOwnNanokvm()
				}
			})
		})
	})
}

// ownKvmReady reports whether this computer's own NanoKVM can be opened:
// its capture card and serial port are saved.
func (a *App) ownKvmReady() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status == nil {
		return false
	}
	k := a.status.Nanokvm
	return k != nil && k.Video != "" && k.Serial != ""
}

// openOwnNanokvm opens this computer's own NanoKVM in a pane of the main
// window: a NanoKVM viewer of its own Device ID, which the engine serves
// here, without a network session.
// Returns: the pane's tabKey.
// Notes: call on the main thread.
func (a *App) openOwnNanokvm() string {
	a.mu.Lock()
	own := a.status.DeviceID
	a.mu.Unlock()
	return a.connectNanokvm(own, "")
}

// kvmUser names the controller using this computer's NanoKVM: its name,
// else its Device ID.
func (a *App) kvmUser() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status == nil {
		return ""
	}
	for _, s := range a.status.Sessions {
		if s.Role == "host" && s.Service == serviceNanokvm && s.State == "connected" {
			return deviceTitle(a.prefs.named(s.DeviceID, s.Peer), s.DeviceID)
		}
	}
	return ""
}

// ownPanel is the pane of this computer's own NanoKVM before and after
// its picture: opening, or why it ended with Open again — or, while a
// controller uses it, who and Take over, which ends their session.
func (v *viewer) ownPanel(c *ui.Context, phase localapi.SessionInfo) {
	t := c.Theme()
	v.mu.Lock()
	note, inUse := v.state, v.inUse
	v.mu.Unlock()
	ui.Column(c).Fill().Background(t.Background).AlignItems(ui.Center).Justify(ui.Center).Padding(0, paneMargin*2).Children(func() {
		homeCard(c, "connect-panel-"+v.tabKey()).FillWidth().MaxWidth(connectPanelW).Children(func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, kindNanokvm).FontSize(sizeHeading).TextColor(t.TextMuted)
				ui.Text(c, L.ServiceNanokvm()).FontSize(sizeHeading).Bold()
			})
			switch phase.State {
			case "", "connecting", "reconnecting":
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					busyMark(c, L.NanokvmOpening(), busySize)
					ui.Text(c, L.NanokvmOpening()).TextColor(t.TextMuted)
				})
				return
			}
			text := note
			if text == "" {
				text = sessionState(phase, helperGap{})
			}
			ui.Text(c, text).TextColor(textTone(t, t.Danger))
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1)
				if inUse {
					if ui.PrimaryButton(c.Key("kvm-take-over"), L.NanokvmTakeOver()).Clicked() {
						v.mu.Lock()
						v.takeOver = true
						v.mu.Unlock()
						v.redial("", false)
					}
					return
				}
				if ui.PrimaryButton(c.Key("kvm-again"), L.ViewerReconnect()).Clicked() {
					v.redial("", false)
				}
			})
		})
	})
}
