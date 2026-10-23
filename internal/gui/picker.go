package gui

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// pickerState is the + picker while it is open (doc/en-us/gui.md,
// "Windows"): where it opened from ("" closed: the sidebar's +, the rail's,
// or a pane header's split button), the kind of pane, the words typed, the
// row chosen, and whether it opens beside a pane (its tabKey, "" the pane
// in focus; on which side).
type pickerState struct {
	at     string
	kind   string // as openAs: "" the desktop, a service, actionAI
	query  string
	chosen string // a pickRow's key
	beside bool
	target string
	zone   dropZone
}

// selfID is the picker's row of this computer (an AI task here).
const selfID = "self"

// pickRow is a device the picker lists: its key (a Device ID, an address
// typed, or selfID), what opens it, its name, the line under it — its way
// or why the kind is not offered — and the dot's colour; whether it opens
// the kind, and whether it is only in my networks (a dashed icon).
type pickRow struct {
	key, device, address string
	name, note           string
	dot                  ui.Color
	ok, peer             bool
}

// kindOffered reports whether a device opens a kind of pane: the desktop
// always; a service when its last status says it answers and the saved
// sign-in grants it; AI when its AI is ready and granted. Without a status,
// nothing is known, so everything is offered.
func kindOffered(kind string, s *localapi.SystemSample, tunnels bool) bool {
	if kind == "" || s == nil {
		return true
	}
	if kind == actionAI {
		return s.AI && slices.Contains(s.Offers, localapi.PermRunAITask)
	}
	perm := map[string]string{serviceSSH: localapi.PermUseSSH, serviceRDP: localapi.PermUseRDP, serviceVNC: localapi.PermUseVNC}[kind]
	return tunnels && slices.Contains(s.Services, kind) && slices.Contains(s.Offers, perm)
}

// kindName is a kind of pane in words: Desktop, SSH, Remote Desktop, VNC,
// AI task.
func kindName(kind string) string {
	switch kind {
	case serviceSSH, serviceRDP, serviceVNC:
		return serviceName(kind)
	case actionAI:
		return L.HomeOpenAI()
	}
	return L.PaneDesktop()
}

// kindIcon is a kind of pane's icon.
func kindIcon(kind string) *ui.SVG {
	switch kind {
	case serviceSSH, serviceRDP, serviceVNC:
		return serviceIcon(kind)
	case actionAI:
		return navAI
	}
	return kindDesktop
}

// pickRows are the rows the picker lists for a kind and the words typed:
// for an AI task, this computer first; a Device ID typed that no row has,
// or an address typed (with a dot or a colon, or anything without spaces
// that nothing else matches); the devices this one knows (greyed with the
// reason when they do not offer the kind); then for the desktop, the
// devices of my networks never connected to. The words match a name, a
// Device ID (spaces aside) or an address.
func pickRows(kind, query string, rows []deviceRow, watched map[string]*localapi.SystemSample, online map[string]localapi.NetworkDevice, others []networkOther, success, warning, muted ui.Color) []pickRow {
	words := strings.Fields(strings.ToLower(query))
	match := func(texts ...string) bool {
		hay := strings.ToLower(strings.Join(texts, " "))
		for _, w := range words {
			if !strings.Contains(hay, w) && !strings.Contains(strings.ReplaceAll(hay, " ", ""), w) {
				return false
			}
		}
		return true
	}
	var out, found []pickRow
	if kind == actionAI && match(L.PickThisComputer()) {
		out = append(out, pickRow{key: selfID, device: selfID, name: L.PickThisComputer(), note: L.PickThisComputerNote(), dot: success, ok: true})
	}
	for _, r := range rows {
		var addrs []string
		for _, rt := range r.routes {
			addrs = append(addrs, rt.Address)
		}
		if !match(append([]string{r.name(), r.id, r.about.Name}, addrs...)...) {
			continue
		}
		row := pickRow{key: r.id, device: r.id, name: deviceTitle(r.about, r.id), dot: muted, ok: kindOffered(kind, watched[r.id], slices.Contains(r.features, "tunnel"))}
		path, on := online[r.id]
		switch {
		case r.session != nil && r.session.State == "connected":
			row.note, row.dot = L.CardConnected(), success
		case on && path.Path == "direct":
			row.note, row.dot = L.PathDirect()+" · "+L.LatencyMS(path.LatencyMS), success
		case on:
			row.note, row.dot = L.PathRelay()+" · "+L.LatencyMS(path.LatencyMS), warning
		case r.last != 0:
			row.note = L.DeviceLast(time.Unix(r.last, 0).Local().Format("2006-01-02 15:04"))
		default:
			row.note = L.CardOffline()
		}
		if !row.ok {
			row.note, row.dot = L.PickNotOffered(kindName(kind)), muted
		}
		found = append(found, row)
	}
	if kind == "" {
		for _, o := range others {
			if !match(o.id, o.network) {
				continue
			}
			dot, way := success, L.PathDirect()
			if o.path.Path != "direct" {
				dot, way = warning, L.PathRelay()
			}
			found = append(found, pickRow{key: o.id, device: o.id, name: groupDigits(o.id), note: o.network + " · " + way + " · " + L.LatencyMS(o.path.LatencyMS), dot: dot, ok: true, peer: true})
		}
	}
	device, address := parseTarget(query)
	listed := slices.ContainsFunc(found, func(r pickRow) bool { return r.device == device })
	switch {
	case device != "" && !listed:
		out = append(out, pickRow{key: device, device: device, name: groupDigits(device), note: L.PickByID(), dot: muted, ok: true})
	case address != "" && !strings.ContainsAny(address, " \t") && (strings.ContainsAny(address, ".:") || len(out)+len(found) == 0):
		out = append(out, pickRow{key: address, address: address, name: address, note: L.PickByAddress(), dot: muted, ok: true})
	}
	return append(out, found...)
}

// openPicker opens the + picker from an anchor ("sidebar", "rail" or a split
// button's id), beside a pane (its tabKey) on a side when it is a split
// button's. The kind chosen last stays.
func (a *App) openPicker(at string, beside bool, target string, z dropZone) {
	a.mu.Lock()
	a.picker = pickerState{at: at, beside: beside, target: target, zone: z, kind: a.picker.kind}
	a.mu.Unlock()
}

// nextRow is the row ↓ (step 1) or ↑ (step -1) chooses from row i (-1:
// none chosen): the next that opens the kind, around the ends; -1 when none
// does.
func nextRow(rows []pickRow, i, step int) int {
	n := len(rows)
	if i < 0 && step < 0 {
		i = n
	}
	for k := 1; k <= n; k++ {
		if j := ((i+step*k)%n + n) % n; rows[j].ok {
			return j
		}
	}
	return -1
}

// pickerView is the + picker (doc/en-us/gui.md, "Windows") below its
// anchor while it opened from at: the kind of pane (Desktop, SSH, Remote
// Desktop, VNC, AI task); a search by name, Device ID or address, where ↑
// and ↓ choose a device and Enter opens it (or the first that can); the
// devices (pickRows); then On its own or Beside current, and Open.
func (a *App) pickerView(c *ui.Context, at string, anchor ui.Element) {
	t := c.Theme()
	a.mu.Lock()
	p := a.picker
	if p.at != at {
		a.mu.Unlock()
		return
	}
	st := a.status
	if st == nil || !a.connected {
		st = &localapi.Status{}
	}
	rows := deviceRows(st, a.prefs)
	watched := map[string]*localapi.SystemSample{}
	now := time.Now()
	for _, w := range st.Watching {
		if statusFreshness(w.ReceivedAt, now) != statusNone {
			watched[w.DeviceID] = w.Sample
		}
	}
	online := map[string]localapi.NetworkDevice{}
	for _, d := range st.Network.Devices {
		online[d.DeviceID] = d
	}
	others := networkOthers(st.Network.Networks, rows, st.DeviceID)
	a.mu.Unlock()
	list := pickRows(p.kind, p.query, rows, watched, online, others, t.Success, t.Warning, t.TextMuted)
	chosen := slices.IndexFunc(list, func(r pickRow) bool { return r.key == p.chosen && r.ok })
	moved := false
	open := true
	set := func(change func(p *pickerState)) {
		a.mu.Lock()
		change(&a.picker)
		a.mu.Unlock()
	}
	ui.PopoverBase(c, anchor, &open, func(panel ui.Element) {
		panel.Width(pickerW).Margin(6, 0, 0, 0).Radius(paneRadius).Background(t.Background).Border(1, t.Border).Shadow(0, 18, 50, 0, sheetShadow).Clip()
		ui.Column(c.Key("picker")).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(14, 14, 8, 14).Gap(8).Children(func() {
				ui.Text(c, L.ActionOpen()).FontSize(sizeSmall).Bold().TextColor(t.TextMuted)
				ui.Grid(c.Key("picker-kinds")).Columns(len(openAs)).Gap(6).FillWidth().Children(func() {
					for _, k := range openAs {
						on := p.kind == k
						if pickerKind(c, k, on) {
							set(func(p *pickerState) { p.kind, p.chosen = k, "" })
							a.pickSearch.Focus(c)
						}
					}
				})
			})
			ui.Row(c.Key("picker-search")).Height(34).Margin(4, 14, 2, 14).Gap(8).Padding(0, 10).Radius(9).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, shellSearch).FontSize(sizeSmall + 2).TextColor(t.TextMuted)
				q := p.query
				in := ui.TextInputBase(c.Key("picker-search-in"), &q).Grow(1).Placeholder(L.PickSearch()).Label(L.PickSearch()).AutoFocus().Bind(&a.pickSearch)
				if in.Changed() {
					set(func(p *pickerState) { p.query = q })
				}
				step := 0
				if in.Shortcut(0, ui.KeyDown) {
					step = 1
				}
				if in.Shortcut(0, ui.KeyUp) {
					step = -1
				}
				if i := nextRow(list, chosen, step); step != 0 && i >= 0 {
					chosen, moved = i, true
					set(func(p *pickerState) { p.chosen = list[i].key })
				}
				if in.Submitted() {
					i := chosen
					if i < 0 {
						i = slices.IndexFunc(list, func(r pickRow) bool { return r.ok })
					}
					if i >= 0 {
						open = false
						a.pickOpen(p, list[i])
					}
				}
			})
			ui.Scroll(c.Key("picker-rows")).FillWidth().MaxHeight(pickerRowsMaxH).Padding(4, 8).Gap(2).Children(func() {
				peers := false
				// Peers come last: a list that starts with one has no devices.
				if len(list) == 0 || !list[0].peer {
					ui.Text(c, L.HomeDevices()).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Padding(6, 8, 4, 8)
				}
				if len(list) == 0 {
					ui.Text(c, L.PickNone()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(4, 8)
				}
				for i, r := range list {
					if r.peer && !peers {
						peers = true
						ui.Text(c, L.HomeOthersTitle()).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Padding(8, 8, 4, 8)
					}
					if pickerRow(c, r, i == chosen, moved && i == chosen) {
						set(func(p *pickerState) { p.chosen = r.key })
					}
				}
			})
			ui.Divider(c)
			ui.Row(c.Key("picker-foot")).FillWidth().Padding(10, 14).Gap(8).AlignItems(ui.Center).Children(func() {
				where := 0
				if p.beside {
					where = 1
				}
				if ui.Segmented(c.Key("picker-where"), &where, L.PickAlone(), L.PickBeside()).Changed() {
					set(func(p *pickerState) { p.beside = where == 1 })
				}
				ui.Box(c).Grow(1)
				label := L.PickChoose()
				if chosen >= 0 {
					label = L.PickOpenKind(kindName(p.kind))
					if p.kind == actionAI {
						label = L.PickStartAI()
					}
				}
				if ui.PrimaryButton(c.Key("picker-go"), label).Disabled(chosen < 0).Clicked() {
					open = false
					a.pickOpen(p, list[chosen])
				}
			})
		})
	})
	if !open {
		a.mu.Lock()
		if a.picker.at == at {
			a.picker = pickerState{kind: a.picker.kind}
		}
		a.mu.Unlock()
	}
}

// pickerKind is one of the picker's kinds: its icon over its name, in the
// accent's tint while chosen. Returns: whether it was clicked.
func pickerKind(c *ui.Context, kind string, on bool) bool {
	t := c.Theme()
	b := ui.ToggleBase(c.Key("picker-kind-"+kind), &on).MinHeight(56).Radius(10).Padding(8, 4).Label(kindName(kind))
	fg, face, edge := t.TextMuted, t.Background, t.Border
	switch {
	case on:
		fg, face, edge = t.Accent, t.Accent.Alpha(0.12), t.Accent
	case b.Hovered():
		face = t.SurfaceHover
	}
	b.Background(face).Border(1, edge)
	b.Children(func() {
		ui.Column(c).Fill().Gap(4).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Icon(c, kindIcon(kind)).FontSize(navIconSize + 2).TextColor(fg)
			ui.Text(c, kindName(kind)).FontSize(sizeSmall).TextColor(textTone(t, fg)).TextAlign(ui.Center)
		})
	})
	return b.Changed()
}

// pickerRow is one of the picker's devices: its icon (dashed for one only in
// my networks), name and the line under it with its dot, a check while
// chosen; greyed and inert when it does not offer the kind. reveal scrolls
// it into view (chosen with a key). Returns: whether it was clicked.
func pickerRow(c *ui.Context, r pickRow, chosen, reveal bool) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("picker-row-"+r.key)).Height(pickerRowH).FillWidth().Radius(9).Padding(0, 10).Label(r.name).Disabled(!r.ok)
	if reveal {
		b.ScrollIntoView()
	}
	switch {
	case chosen:
		b.Background(t.Accent.Alpha(0.12))
	case r.ok && b.Hovered():
		b.Background(t.SurfaceHover)
	}
	if !r.ok {
		b.Opacity(0.55)
	}
	b.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			icon := ui.Box(c).Size(30, 30).Shrink(0).Radius(8).Center()
			if r.peer {
				icon.Border(1, t.Border).BorderStyle(ui.BorderDashed)
			} else {
				icon.Background(tileBackground(t))
			}
			icon.Children(func() { ui.Icon(c, kindDesktop).FontSize(navIconSize).TextColor(t.TextMuted) })
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, r.name).FontWeight(500).FontFeatures("tnum").SingleLine()
				ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
					ui.Box(c).Size(onlineDot, onlineDot).Shrink(0).Radius(onlineDot / 2).Background(r.dot)
					ui.Text(c, r.note).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().MinWidth(0)
				})
			})
			if chosen {
				ui.Icon(c, rowCheck).FontSize(navIconSize).TextColor(t.Accent)
			}
		})
	})
	return b.Clicked()
}

// pickOpen opens what the picker chose: a session with this computer's
// AI, or the kind on a device by its Device ID or at an address (once the
// device there answered); beside a pane when asked (the pane in focus as
// it was chosen, or the one whose split button opened the picker), on its
// side, else as an entry of its own.
// Notes: call on the main thread.
func (a *App) pickOpen(p pickerState, r pickRow) {
	a.mu.Lock()
	a.picker = pickerState{kind: p.kind}
	a.mu.Unlock()
	var target tab
	if p.beside {
		target = a.tabFor(p.target)
		if p.target == "" {
			a.main.mu.Lock()
			target = a.main.shown()
			a.main.mu.Unlock()
		}
	}
	z := p.zone
	if z == zoneMiddle {
		z = zoneRight
	}
	if r.device == selfID {
		a.openAISession("", "", "", target, z)
		return
	}
	a.connectAs(r.device, r.address, p.kind, func(key string) {
		if target != nil && a.tabFor(key) != target {
			a.dropTab(key, a.main, target, z)
		}
	})
}
