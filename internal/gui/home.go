package gui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// How old a host's status sample may be (doc/en-us/gui.md, "Home"): shown
// as current up to statusFresh, dimmed with its age up to statusStale, not
// shown at all after that.
const (
	statusFresh = 6 * time.Second
	statusStale = 60 * time.Second
)

// The watch lease (watch.set): renewed every leaseEvery while the main
// window is open or a status pane is, for at most leaseHosts hosts.
const (
	leaseEvery = 10 * time.Second
	leaseHosts = 8
)

// ringLen is how many samples a sparkline keeps: two minutes at the
// host's one sample every two seconds.
const ringLen = 60

// freshness is how a status sample may show.
type freshness int

const (
	statusNone  freshness = iota // too old, or none: no values
	statusDim                    // dimmed, with its age
	statusShown                  // current
)

// statusFreshness tells how a sample received at receivedMS (Unix
// milliseconds, 0 for none) may show at now: never stale data as current.
func statusFreshness(receivedMS int64, now time.Time) freshness {
	if receivedMS == 0 {
		return statusNone
	}
	switch age := now.Sub(time.UnixMilli(receivedMS)); {
	case age <= statusFresh:
		return statusShown
	case age <= statusStale:
		return statusDim
	}
	return statusNone
}

// homeColumnsFor is how many device cards fit side by side in width w:
// each at least homeCardMinW, one to homeColumns.
func homeColumnsFor(w float32) int {
	return min(max(int((w+gap)/(homeCardMinW+gap)), 1), homeColumns)
}

// ring is a sparkline's last ringLen values, oldest first once full.
type ring struct {
	vals [ringLen]float32
	n    int // values kept, ≤ ringLen
	head int // where the next goes
}

// push adds a value, dropping the oldest when full.
func (r *ring) push(v float32) {
	r.vals[r.head] = v
	r.head = (r.head + 1) % ringLen
	r.n = min(r.n+1, ringLen)
}

// values are the ring's values, oldest first.
func (r *ring) values() []float32 {
	out := make([]float32, 0, r.n)
	for i := range r.n {
		out = append(out, r.vals[(r.head-r.n+i+ringLen)%ringLen])
	}
	return out
}

// hostRings are a watched host's histories and the sample they took last.
type hostRings struct {
	cpuUser, cpuSys, memory, gpu ring
	diskRead, diskWrite, rx, tx  ring
	battery                      ring
	last                         int64 // ReceivedAt of the newest sample pushed
}

// push adds a sample to every history.
func (r *hostRings) push(s *localapi.SystemSample) {
	r.cpuUser.push(float32(s.CPU.User))
	r.cpuSys.push(float32(s.CPU.System))
	r.memory.push(float32(share(s.Memory)))
	if s.GPU != nil {
		r.gpu.push(float32(s.GPU.Usage))
	}
	if s.DiskIO != nil {
		r.diskRead.push(float32(s.DiskIO.ReadBps))
		r.diskWrite.push(float32(s.DiskIO.WriteBps))
	}
	r.rx.push(float32(s.Net.RxBps))
	r.tx.push(float32(s.Net.TxBps))
	if s.Battery != nil {
		r.battery.push(float32(s.Battery.Percent))
	}
}

// watchState is Home's status watch: the lease last sent and the hosts'
// histories. Fields are guarded by a.mu, sent and at by the lease too.
type watchState struct {
	rings map[string]*hostRings
	sent  lease
	at    time.Time
	// mu keeps the lease's requests in order (syncWatch).
	mu sync.Mutex
}

// recordSamples pushes the new samples of a status onto the hosts'
// histories and clears those of hosts whose status went too old.
// Notes: the caller holds a.mu.
func (a *App) recordSamples(st *localapi.Status, now time.Time) {
	if a.watch.rings == nil {
		a.watch.rings = map[string]*hostRings{}
	}
	for _, w := range st.Watching {
		r := a.watch.rings[w.DeviceID]
		if r == nil {
			r = &hostRings{}
			a.watch.rings[w.DeviceID] = r
		}
		if statusFreshness(w.ReceivedAt, now) == statusNone {
			*r = hostRings{}
			continue
		}
		if w.Sample == nil || w.ReceivedAt == r.last {
			continue
		}
		r.last = w.ReceivedAt
		r.push(w.Sample)
	}
}

// lease is a watch lease (watch.set): the hosts watched, and those of them
// at the slow pace. Neither is nil, so that both go as lists.
type lease struct{ hosts, slow []string }

// watchLease decides the lease to send: the hosts whose status panes are
// open, then while the main window is open (`open`) the recent hosts
// (leaseHosts in all) — at the slow pace while Home does not show, so that
// their watches stay connected (regression: they ended as Home hid, and
// every host was dialed again as it showed); sent when it changed or every
// leaseEvery; with no host, an empty lease, sent once.
// Returns: whether to send, and the lease.
func watchLease(open, visible bool, recent []localapi.RecentDevice, panes []string, sent lease, at, now time.Time) (bool, lease) {
	l := lease{hosts: slices.Clone(panes)[:min(len(panes), leaseHosts)], slow: []string{}}
	if l.hosts == nil {
		l.hosts = []string{}
	}
	if open {
		for _, r := range recent {
			if len(l.hosts) == leaseHosts {
				break
			}
			if !slices.Contains(l.hosts, r.DeviceID) {
				l.hosts = append(l.hosts, r.DeviceID)
				if !visible {
					l.slow = append(l.slow, r.DeviceID)
				}
			}
		}
	}
	if len(l.hosts) == 0 {
		return len(sent.hosts) > 0, l
	}
	changed := !slices.Equal(l.hosts, sent.hosts) || !slices.Equal(l.slow, sent.slow)
	return changed || now.Sub(at) >= leaseEvery, l
}

// homeVisible reports whether Home shows: the main window is visible with
// Home's pane in the group shown, not under the Settings sheet (nor in
// protected mode).
func (a *App) homeVisible() bool {
	a.mu.Lock()
	shown := a.visible && !a.settingsOpen && !a.protected()
	main := a.main
	a.mu.Unlock()
	if !shown || main == nil {
		return false
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	return main.homeShownLocked()
}

// mainOpen reports whether the main window is open (not closed to the
// tray), outside protected mode: Home's hosts stay watched, shown or not.
func (a *App) mainOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.visible && !a.protected()
}

// homeVisibilityChanged sends the watch lease at once as Home shows or
// hides.
func (a *App) homeVisibilityChanged() { go a.syncWatch() }

// syncWatch sends the watch lease when watchLease says so.
func (a *App) syncWatch() {
	a.watch.mu.Lock()
	defer a.watch.mu.Unlock()
	open, visible := a.mainOpen(), a.homeVisible()
	a.mu.Lock()
	recent, panes := a.statusRecent(), a.statusHosts()
	sent, at := a.watch.sent, a.watch.at
	a.mu.Unlock()
	send, l := watchLease(open, visible, recent, panes, sent, at, time.Now())
	if !send {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	_, err := localapi.NewClient(a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestWatchSet, Watch: &l.hosts, WatchSlow: &l.slow})
	if err != nil {
		return // an older engine watches nothing; asked again with the next lease
	}
	a.mu.Lock()
	a.watch.sent, a.watch.at = l, time.Now()
	a.mu.Unlock()
}

// watchLoop keeps the lease (syncWatch) every second and, while Home shows
// a host's status, redraws it so that ages and staleness stay true.
func (a *App) watchLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
		}
		a.syncWatch()
		a.probeRoutes(time.Now())
		if !a.homeVisible() {
			continue
		}
		a.mu.Lock()
		watching := a.status != nil && len(a.status.Watching) > 0
		a.mu.Unlock()
		if watching {
			a.update(func() {})
		}
	}
}

// homeView is Home (doc/en-us/gui.md, "Home"): This device and Connect to a
// device side by side (one above the other in a narrow pane), the privacy
// permissions macOS still lacks, then the devices.
func (a *App) homeView(c *ui.Context) {
	if !a.engineState(c) {
		return
	}
	box := ui.Column(c.Key("home")).FillWidth().MaxWidth(homeMaxW).Gap(gap)
	w := box.LayoutBounds().W
	box.Children(func() {
		top := ui.Grid(c.Key("home-top")).FillWidth().Gap(homeTopGap)
		// Not measured yet (the first frame), as wide as a window most often
		// is: side by side, not a jump from one above the other.
		if w == 0 || w >= homeTwoColumnsW {
			top.ColumnTracks(ui.Fr(5), ui.Fr(7))
		} else {
			top.Columns(1)
		}
		top.Children(func() {
			a.thisDeviceCard(c)
			a.connectCard(c)
		})
		a.permissionsCard(c)
		a.devicesSection(c)
	})
}

// homeCard is a card of Home's grids: as wide as its cell, the theme's
// surface, rounded and outlined.
func homeCard(c *ui.Context, key string) ui.Element {
	t := c.Theme()
	return ui.Column(c.Key(key)).MinWidth(0).Padding(16, 18).Gap(10).Radius(cardRadius).Background(t.Surface).Border(1, t.Border)
}

// statePill is a state in a pill of its colour: Online, Connected,
// Offline.
func statePill(c *ui.Context, text string, col ui.Color) {
	ui.Text(c, text).FontSize(sizeSmall).TextColor(textTone(c.Theme(), col)).SingleLine().Shrink(0).MaxWidth(homeCardMinW/2).Tooltip(text).
		Padding(2, 8).Radius(chipRadius).Background(col.Alpha(0.12))
}

// thisDeviceCard is Home's This device: the networks' state, the Device ID
// with Copy, and the temporary password, masked, with Show, Copy and New
// password (or what stands in for it when only the permanent password, or
// neither, lets devices in); a warning when secrets are not kept, and Start
// over when the saved identity cannot be unlocked.
func (a *App) thisDeviceCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	st := *a.status
	startingOver := a.busy["start-over"]
	a.mu.Unlock()
	word, col := a.networkState(t)
	homeCard(c, "this-device").Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, L.HomeThisDevice()).Bold().Grow(1)
			statePill(c, word, col)
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, groupDigits(st.DeviceID)).FontSize(sizeDeviceID).Bold().FontFeatures("tnum").NoWrap().Selectable().MinWidth(0)
			if iconButton(c, "copy-id", rowCopy, L.ActionCopy()) {
				mygo.Clipboard.WriteText(st.DeviceID)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
		})
		ui.Divider(c)
		// With only the permanent password accepted, or neither, the
		// temporary one opens nothing and is not shown.
		switch {
		case st.PasswordMode == "none":
			ui.Text(c, L.HomePasswordsOff()).FontSize(sizeSmall).TextColor(t.TextMuted)
		case st.PasswordMode == "permanent" && st.PermanentPassword:
			ui.Text(c, L.HomePermanentOnly()).FontSize(sizeSmall).TextColor(t.TextMuted)
		case st.PasswordMode == "permanent":
			ui.Text(c, L.HomePermanentMissing()).FontSize(sizeSmall).TextColor(textTone(t, t.Warning))
		default:
			a.passwordRow(c, "")
		}
		if note := secretsNote(st.Persistence); note != "" && st.Persistence != "os-store" {
			ui.Text(c, note).FontSize(sizeSmall).TextColor(textTone(t, t.Warning))
		}
		if st.Persistence == "unreadable" {
			ui.Row(c).FillWidth().Gap(gap).AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.StartOverHint()).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
				if ui.Button(c.Key("start-over"), L.ActionStartOver()).Disabled(startingOver).Clicked() {
					a.startOver()
				}
			})
		}
	})
}

// passwordRow is the temporary password, masked, with Show (Hide), Copy
// and New password, its controls keyed with prefix (Home's This device and
// the tour's).
func (a *App) passwordRow(c *ui.Context, prefix string) {
	t := c.Theme()
	a.mu.Lock()
	pw, show := a.password, a.showPassword && a.status != nil && a.passwordFor == a.status.PasswordSerial
	a.mu.Unlock()
	ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		// Short of room it wraps, a word a line, rather than losing words.
		ui.Text(c, L.HomeTemporaryPassword()).FontSize(sizeSmall).TextColor(t.TextMuted).Shrink(1)
		shown := strings.Repeat("•", 8)
		if show {
			shown = pw
		}
		ui.Text(c, shown).FontSize(sizeHeading).FontFeatures("tnum").Selectable().Margin(0, 0, 0, 8)
		ui.Box(c).Grow(1)
		icon, label := rowEye, L.ActionShow()
		if show {
			icon, label = rowEyeOff, L.ActionHide()
		}
		if iconButton(c, prefix+"show-pw", icon, label) {
			if show {
				a.mu.Lock()
				a.showPassword = false
				a.mu.Unlock()
			} else {
				a.revealPassword(func(string) { a.update(func() { a.showPassword = true }) })
			}
		}
		if iconButton(c, prefix+"copy-pw", rowCopy, L.ActionCopy()) {
			a.revealPassword(func(p string) {
				clipboardWrite(p)
				a.update(func() { a.setToast(L.Copied(), false) })
			})
		}
		if iconButton(c, prefix+"new-pw", rowRefresh, L.ActionNewPassword()) {
			go func() { _, _ = a.request(localapi.RequestPasswordRegenerate) }()
		}
	})
}

// openAs are Open as's choices on Home, in its order: the desktop, SSH,
// Remote Desktop, VNC, an AI task.
var openAs = []string{"", serviceSSH, serviceRDP, serviceVNC, actionAI}

// connectCard is Home's Connect to a device: a Device ID or an address,
// Connect (Enter too), and what to open there (Open as). The password is
// asked for in the pane that opens when the device wants one.
func (a *App) connectCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	f, online := a.connect, a.status.Network.State == "online"
	a.mu.Unlock()
	device, address := parseTarget(f.target)
	ready := (online && device != "" || address != "") && !f.identifying
	homeCard(c, "connect").Gap(12).Children(func() {
		ui.Text(c, L.SidebarNew()).Bold()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			id := ui.TextInput(c.Key("conn-id"), &f.target).Placeholder(L.ConnectPlaceholder()).Label(L.ConnectPlaceholder()).
				Grow(1).MinWidth(0).FontFeatures("tnum").Disabled(f.identifying)
			if id.Changed() {
				a.mu.Lock()
				a.connect.target = f.target
				a.mu.Unlock()
			}
			if ui.PrimaryButton(c.Key("conn-go"), L.ActionConnect()).Disabled(!ready).Clicked() || id.Submitted() && ready {
				a.connectAs(device, address, f.as, nil)
			}
		})
		// Too narrow for its five segments (a pane beside another, a small
		// window), Open as is a menu.
		as := ui.Row(c.Key("conn-as-row")).FillWidth().Gap(10).AlignItems(ui.Center).Wrap()
		narrow := as.LayoutBounds().W > 0 && as.LayoutBounds().W < openAsSegmentsW
		as.Children(func() {
			ui.Text(c, L.HomeOpenAs()).FontSize(sizeSmall).TextColor(t.TextMuted)
			idx := max(slices.Index(openAs, f.as), 0)
			kinds := []string{L.PaneDesktop(), L.ServiceSSH(), L.ServiceRDP(), L.ServiceVNC(), L.HomeOpenAI()}
			var changed bool
			if narrow {
				cur := kinds[idx]
				changed = ui.Select(c.Key("conn-as-sel"), &cur, kinds).Width(openAsMenuW).Label(L.HomeOpenAs()).Changed()
				idx = max(slices.Index(kinds, cur), 0)
			} else {
				changed = ui.Segmented(c.Key("conn-as"), &idx, kinds...).Changed()
			}
			if changed {
				a.mu.Lock()
				a.connect.as = openAs[idx]
				a.mu.Unlock()
			}
		})
		switch {
		case f.identifying:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				busyMark(c, L.ConnectIdentifying(), busySize)
				ui.Text(c, L.ConnectIdentifying()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
		case !online && address == "":
			ui.Text(c, L.ConnectNeedsNetwork()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
	})
}

// connectAs opens what Open as names on a device, by its Device ID or at an
// address (asked for its Device ID first): its desktop, one of its
// services, or a session with its AI. placed, when set, then gets the
// pane's tabKey (the + picker puts it beside another).
// Notes: call on the main thread; placed runs on it.
func (a *App) connectAs(device, address, as string, placed func(key string)) {
	open := func(device, address string) {
		key := device
		switch as {
		case serviceSSH, serviceRDP, serviceVNC:
			key = a.openService(device, address, as)
		case actionAI:
			key = a.openAISession(device, address, "", nil, zoneMiddle).tabKey()
		default:
			a.connectDevice(device, address, a.recentAbout(device))
		}
		if placed != nil {
			placed(key)
		}
	}
	if address != "" {
		a.identify(address, open)
		return
	}
	open(device, "")
}

// devicesSection is Home's devices: their title and how many, Others in my
// networks and All or Online beside it; the address and rename editors when
// open; a grid of cards of equal height, one per device, up to three side by
// side (homeColumnsFor); and, with Others in my networks on, the Kyokai
// devices the joined networks reach that this one never connected to.
func (a *App) devicesSection(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	rows := deviceRows(a.status, a.prefs)
	rename, routesEdit := a.rename, a.routesEdit
	onNetwork := len(a.status.Network.Networks) > 0
	online := map[string]localapi.NetworkDevice{}
	for _, d := range a.status.Network.Devices {
		online[d.DeviceID] = d
	}
	watched := map[string]localapi.Watched{}
	for _, w := range a.status.Watching {
		watched[w.DeviceID] = w
	}
	rings := map[string]hostRings{}
	for id, r := range a.watch.rings {
		rings[id] = *r
	}
	p, onlyOnline := a.prefs, a.homeOnline
	nets, self := a.status.Network.Networks, a.status.DeviceID
	a.mu.Unlock()
	known := rows
	if onlyOnline {
		rows = slices.DeleteFunc(slices.Clone(rows), func(r deviceRow) bool {
			_, on := online[r.id]
			return !on && r.session == nil
		})
	}
	ui.Row(c.Key("devices-head")).FillWidth().Gap(10).AlignItems(ui.Center).Padding(14, 2, 0, 2).Children(func() {
		ui.Text(c, L.HomeDevices()).FontSize(sizeHeading).Bold()
		ui.Text(c, strconv.Itoa(len(rows))).FontSize(sizeSmall).TextColor(t.TextMuted)
		ui.Box(c).Grow(1)
		others := p.OthersInNetworks
		ui.Text(c, L.HomeOthersSwitch()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if ui.Switch(c.Key("others-sw"), &others).Label(L.HomeOthersSwitch()).Changed() {
			p.OthersInNetworks = others
			a.setPrefs(p)
		}
		idx := 0
		if onlyOnline {
			idx = 1
		}
		if ui.Segmented(c.Key("devices-filter"), &idx, L.HomeFilterAll(), L.CardOnline()).Changed() {
			a.mu.Lock()
			a.homeOnline = idx == 1
			a.mu.Unlock()
		}
	})
	if rename.device != "" {
		a.renameCard(c, rename)
	}
	if routesEdit.device != "" {
		a.routesCard(c, routesEdit)
	}
	// The columns have fixed widths, from the room the box had in the last
	// frame: tracks that share the room would grow with their content.
	box := ui.Column(c.Key("devices-box")).FillWidth()
	w := box.LayoutBounds().W
	now := time.Now()
	cols := homeColumnsFor(w)
	cardW := max((w-gap*float32(cols-1))/float32(cols), homeCardMinW)
	tracks := make([]ui.Track, cols)
	for i := range tracks {
		tracks[i] = ui.Fixed(cardW)
	}
	box.Children(func() {
		if len(rows) == 0 {
			ui.Text(c, L.HomeDevicesNone()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(4, 2)
			return
		}
		ui.Grid(c.Key("devices")).ColumnTracks(tracks...).Gap(gap).Children(func() {
			for _, r := range rows {
				w, ok := watched[r.id]
				var wp *localapi.Watched
				if ok {
					wp = &w
				}
				path, on := online[r.id]
				ring := rings[r.id]
				a.deviceCard(c, r, path, on, onNetwork, wp, &ring, now)
			}
		})
	})
	if p.OthersInNetworks {
		a.othersSection(c, networkOthers(nets, known, self), tracks)
	}
}

// deviceCard is one device's card, as high as the others: its header
// (avatar, name, Device ID and system, its state, ⋯), how the network
// reaches it, four figures from its live status (a click opens its status
// in a pane) or why there are none, and its actions — Open while
// connected, else Connect, then its services and AI as icons.
func (a *App) deviceCard(c *ui.Context, r deviceRow, path localapi.NetworkDevice, online, onNetwork bool, w *localapi.Watched, rings *hostRings, now time.Time) {
	t := c.Theme()
	live := r.session != nil && (r.session.State == "connecting" || r.session.State == "confirming" || r.session.State == "reconnecting")
	connected := r.session != nil && r.session.State == "connected"
	tunnels := slices.Contains(r.features, "tunnel")
	fresh := statusNone
	var sample *localapi.SystemSample
	if w != nil {
		fresh = statusFreshness(w.ReceivedAt, now)
		if fresh != statusNone {
			sample = w.Sample
		}
	}
	a.mu.Lock()
	uses := a.prefs.Uses[r.id]
	a.mu.Unlock()
	actions := cardActions(sample, tunnels, uses)
	ui.Column(c.Key("device-"+r.id)).MinWidth(0).Height(homeCardH).Padding(14, 14, 14, 16).Gap(8).Radius(cardRadius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		a.cardHeader(c, r, online, live, connected, tunnels)
		a.cardPath(c, r, path, online, onNetwork, live)
		a.cardFigures(c, r, w, rings, fresh, now)
		ui.Box(c).Grow(1)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			var main ui.Element
			switch {
			case connected || live:
				main = ui.Button(c.Key("device-go-"+r.id), L.ActionOpen())
			case online:
				main = ui.PrimaryButton(c.Key("device-go-"+r.id), L.ActionConnect())
			default:
				main = ui.Button(c.Key("device-go-"+r.id), L.ActionConnect())
			}
			if main.Grow(1).Clicked() {
				a.connectDevice(r.id, "", r.about)
			}
			for _, act := range actions {
				icon, tip := navAI, L.ActionConnectAI()
				if act != actionAI {
					icon, tip = serviceIcon(act), L.CardOpenService(serviceName(act))
				}
				b := ui.ButtonBase(c.Key("device-"+act+"-"+r.id)).Size(homeActionW, homeActionW).Radius(8).Center().Shrink(0).
					Border(1, t.Border).Label(tip).Tooltip(tip)
				if b.Hovered() {
					b.Background(t.SurfaceHover)
				}
				b.Children(func() { ui.Icon(c, icon).FontSize(navIconSize).TextColor(t.TextMuted) })
				if b.Clicked() {
					a.runAction(r, act)
				}
			}
		})
	})
}

// serviceIcon is a service's icon: SSH, Remote Desktop or VNC.
func serviceIcon(service string) *ui.SVG {
	switch service {
	case serviceSSH:
		return kindSSH
	case serviceRDP:
		return kindRDP
	}
	return kindVNC
}

// actionAI is the AI action of a device card, beside its services.
const actionAI = "ai"

// cardActions are the actions a device card offers beside Connect: the
// services the host reports answering, and its AI when it is ready, each
// only when the saved password or key grants it without asking (what
// needs the host's user stays in the ⋯ menu); the most used first, then
// Remote Desktop, SSH, VNC and AI.
// Inputs: s is the host's current sample (nil: none, so nothing); uses
// counts how often each action was used for this device.
func cardActions(s *localapi.SystemSample, tunnels bool, uses map[string]int) []string {
	if s == nil {
		return nil
	}
	perm := map[string]string{serviceRDP: localapi.PermUseRDP, serviceSSH: localapi.PermUseSSH, serviceVNC: localapi.PermUseVNC, actionAI: localapi.PermRunAITask}
	var out []string
	for _, act := range []string{serviceRDP, serviceSSH, serviceVNC, actionAI} {
		ready := tunnels && slices.Contains(s.Services, act)
		if act == actionAI {
			ready = s.AI
		}
		if ready && slices.Contains(s.Offers, perm[act]) {
			out = append(out, act)
		}
	}
	slices.SortStableFunc(out, func(x, y string) int { return uses[y] - uses[x] })
	return out
}

// runAction opens a device's service or a session with its AI, and counts
// the use (cardActions orders by it).
func (a *App) runAction(r deviceRow, act string) {
	a.noteUse(r.id, act)
	if act == actionAI {
		a.openAISession(r.id, "", "", nil, zoneMiddle)
		return
	}
	a.openService(r.id, "", act)
}

// name is the device's name, or its Device ID when it gave none.
func (r deviceRow) name() string {
	if r.about.Name != "" {
		return r.about.Name
	}
	return groupDigits(r.id)
}

// cardHeader is a device card's top: avatar, name, Device ID and system,
// its state as a pill, and the ⋯ menu.
func (a *App) cardHeader(c *ui.Context, r deviceRow, online, live, connected, tunnels bool) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		deviceAvatar(c, r.about)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, r.name()).Bold().SingleLine()
			var sub []string
			if r.about.Name != "" {
				sub = append(sub, groupDigits(r.id))
			}
			if os := osName(r.about.OS); os != "" {
				sub = append(sub, os)
			}
			if len(sub) > 0 {
				ui.Text(c, strings.Join(sub, " · ")).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
			}
		})
		text, col := L.CardOffline(), t.TextMuted
		switch {
		case connected:
			text, col = L.CardConnected(), t.Success
		case live:
			text, col = sessionState(*r.session, a.outdatedHelper()), t.Accent
		case online:
			text, col = L.CardOnline(), t.Success
		}
		statePill(c, text, col)
		a.deviceMenu(c, r, tunnels)
	})
}

// osName is a system's name as people know it, from what a device reports
// ("macos"), or "" when it said nothing.
func osName(os string) string {
	switch strings.ToLower(os) {
	case "macos", "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return os
}

// figure is one of a device card's figures: a name, a value with its unit,
// and how full its bar is (0 to 1), in the metric's colour.
type figure struct {
	name, value, unit string
	fill              float32
	col               ui.Color
}

// compactFigures are a card's four figures: the processor, the memory, the
// download rate (its bar against the last two minutes' highest) and the
// battery when there is one, else the graphics, else the free disk.
func compactFigures(s *localapi.SystemSample, r *hostRings) []figure {
	used, usedUnit := sizeParts(s.Memory.Used)
	rx, rxUnit := sizeParts(s.Net.RxBps)
	var peak float32
	for _, v := range r.rx.values() {
		peak = max(peak, v)
	}
	netFill := float32(0)
	if peak > 0 {
		netFill = min(float32(s.Net.RxBps)/peak, 1)
	}
	out := []figure{
		{L.StatusCPU(), percentText(s.CPU.Usage), "%", float32(s.CPU.Usage / 100), statusCPU},
		{L.StatusMemory(), used, usedUnit, float32(share(s.Memory) / 100), statusMemory},
		{L.StatusNetwork(), rx, rxUnit + "/s", netFill, statusNetwork},
	}
	switch {
	case s.Battery != nil:
		col := statusBattery
		if s.Battery.Percent <= 20 && !s.Battery.Charging {
			col = statusBatteryLow
		}
		out = append(out, figure{L.StatusBattery(), strconv.Itoa(s.Battery.Percent), "%", float32(s.Battery.Percent) / 100, col})
	case s.GPU != nil:
		out = append(out, figure{L.StatusGPU(), percentText(s.GPU.Usage), "%", float32(s.GPU.Usage / 100), statusGPU})
	default:
		free, freeUnit := sizeParts(s.Disk.Total - min(s.Disk.Used, s.Disk.Total))
		out = append(out, figure{L.StatusDisk(), free, freeUnit, float32(share(s.Disk) / 100), statusDisk})
	}
	for i := range out {
		out[i].fill = min(max(out[i].fill, 0), 1)
	}
	return out
}

// cardFigures are a device's live status on its card: four figures with a
// bar each, which open its status in a pane, dimmed with their age while
// stale; else the watch's state in words.
func (a *App) cardFigures(c *ui.Context, r deviceRow, w *localapi.Watched, rings *hostRings, fresh freshness, now time.Time) {
	t := c.Theme()
	if w == nil {
		return
	}
	if fresh == statusNone || w.Sample == nil {
		a.statusWords(c, w)
		return
	}
	b := ui.ButtonBase(c.Key("figures-" + r.id)).FillWidth().Radius(6).Label(L.StatusOpen()).Tooltip(L.StatusOpen())
	if fresh == statusDim {
		b.Opacity(0.5)
	}
	b.Children(func() {
		ui.Grid(c.Key("figures-grid-" + r.id)).Columns(4).Gap(10).FillWidth().Children(func() {
			for _, f := range compactFigures(w.Sample, rings) {
				ui.Column(c).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, f.name).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
					ui.Row(c).Gap(3).AlignItems(ui.End).MinWidth(0).Children(func() {
						ui.Text(c, f.value).Bold().FontFeatures("tnum").SingleLine()
						ui.Text(c, f.unit).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
					})
					ui.Box(c).Height(3).Radius(2).Margin(3, 0, 0, 0).Background(t.SurfaceHover).Children(func() {
						ui.Box(c).Height(3).WidthPercent(f.fill * 100).Radius(2).Background(f.col)
					})
				})
			}
		})
	})
	if b.Clicked() {
		a.openStatus(r.id)
	}
	if fresh == statusDim {
		age := int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())
		ui.Text(c, L.StatusUpdatedAgo(strconv.Itoa(age))).FontSize(sizeSmall).TextColor(t.TextMuted)
	}
}

// networkOther is a Kyokai device a joined network reaches that this one
// never connected to: its Device ID, the network's name, and how it is
// reached.
type networkOther struct {
	id      string
	network string
	path    localapi.NetworkDevice
}

// networkOthers are the Kyokai devices the joined networks reach that are
// not on Home (known) and not this device, each once with its best way:
// direct before relayed, then the lowest latency; by Device ID.
func networkOthers(nets []localapi.NetworkState, known []deviceRow, self string) []networkOther {
	best := map[string]networkOther{}
	for _, n := range nets {
		if !n.Joined || n.State != "online" {
			continue
		}
		for _, d := range n.Devices {
			if d.DeviceID == self || slices.ContainsFunc(known, func(r deviceRow) bool { return r.id == d.DeviceID }) {
				continue
			}
			o, seen := best[d.DeviceID]
			better := !seen || d.Path == "direct" && o.path.Path != "direct" ||
				d.Path == o.path.Path && d.LatencyMS < o.path.LatencyMS
			if better {
				best[d.DeviceID] = networkOther{id: d.DeviceID, network: n.Label, path: d}
			}
		}
	}
	out := slices.Collect(maps.Values(best))
	slices.SortFunc(out, func(x, y networkOther) int { return strings.Compare(x.id, y.id) })
	return out
}

// othersSection is Home's Kyokai devices in the joined networks that this
// one never connected to (Others in my networks): their title and how
// many, then dashed cards in the devices' columns — the Device ID, the
// network and how it reaches the device, and Connect.
func (a *App) othersSection(c *ui.Context, others []networkOther, tracks []ui.Track) {
	t := c.Theme()
	ui.Row(c.Key("others-head")).FillWidth().Gap(10).AlignItems(ui.Center).Padding(10, 2, 0, 2).Children(func() {
		ui.Text(c, L.HomeOthersTitle()).Bold()
		ui.Text(c, L.HomeOthersNote(len(others))).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
	if len(others) == 0 {
		ui.Text(c, L.HomeOthersNone()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(0, 2)
		return
	}
	ui.Grid(c.Key("others")).ColumnTracks(tracks...).Gap(gap).Children(func() {
		for _, o := range others {
			dot, path := t.Success, L.PathDirect()
			if o.path.Path != "direct" {
				dot, path = t.Warning, L.PathRelay()
			}
			ui.Row(c.Key("other-"+o.id)).MinWidth(0).Height(homeOtherH).Padding(0, 10, 0, 12).Gap(10).AlignItems(ui.Center).
				Radius(cardRadius).Border(1, t.Border).BorderStyle(ui.BorderDashed).Children(func() {
				ui.Box(c).Size(30, 30).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
					ui.Icon(c, kindDesktop).FontSize(navIconSize).TextColor(t.TextMuted)
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, groupDigits(o.id)).Bold().FontFeatures("tnum").SingleLine()
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						ui.Box(c).Size(onlineDot, onlineDot).Shrink(0).Radius(onlineDot / 2).Background(dot)
						ui.Text(c, o.network+" · "+path+" · "+L.LatencyMS(o.path.LatencyMS)).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().MinWidth(0)
					})
				})
				if ui.Button(c.Key("other-go-"+o.id), L.ActionConnect()).Clicked() {
					a.connectDevice(o.id, "", localapi.About{})
				}
			})
		}
	})
}

// deviceMenu is a device card's ⋯ menu.
func (a *App) deviceMenu(c *ui.Context, r deviceRow, tunnels bool) {
	keyChanged := r.session != nil && r.session.State == "failed" && string(r.session.Failure) == `"key-changed"`
	more := iconButtonBase(c, "device-menu-"+r.id, rowMore, L.ActionMore())
	more.Menu(func(m *ui.Menu) {
		if m.Item(L.ActionConnect()).Chosen() {
			a.connectDevice(r.id, "", r.about)
		}
		if m.Item(L.ActionConnectAI()).Chosen() {
			a.runAction(r, actionAI)
		}
		for _, s := range []string{serviceRDP, serviceSSH, serviceVNC} {
			if m.Item(L.CardOpenService(serviceName(s))).Disabled(!tunnels).Chosen() {
				a.runAction(r, s)
			}
		}
		m.Separator()
		if m.Item(L.ActionRename()).Chosen() {
			a.mu.Lock()
			a.rename = renameForm{device: r.id, name: a.prefs.Aliases[r.id]}
			a.mu.Unlock()
		}
		if m.Item(L.RouteAddresses()).Chosen() {
			a.mu.Lock()
			a.routesEdit = routesForm{device: r.id}
			a.mu.Unlock()
		}
		if m.Item(L.CardSaved()).Chosen() {
			a.openSettings(pageSecurity)
			a.homeVisibilityChanged()
		}
		if keyChanged && m.Item(L.ActionForgetIdentity()).Chosen() {
			device := r.id
			go func() { _, _ = a.requestWith(localapi.Request{Type: localapi.RequestHostForget, DeviceID: device}) }()
		}
		if m.Item(L.ActionRemove()).Chosen() {
			a.removeDevice(r.id)
		}
	})
}

// cardPath is how a device is reached: the route in use while connected,
// else the one chosen by hand or the one Auto would take now (direct or
// relayed through the network, or an address, with its latency), or when
// it was last connected. It opens the route menu (`routeMenu`).
func (a *App) cardPath(c *ui.Context, r deviceRow, path localapi.NetworkDevice, online, onNetwork, live bool) {
	t := c.Theme()
	views := routeViews(r, path, online, onNetwork)
	dot, text := t.TextMuted.Alpha(0.5), L.CardNotOnNetwork()
	pick, ok := bestRoute(views)
	if r.prefer != "" {
		want := r.prefer
		if want == preferNetwork {
			want = ""
		}
		i := slices.IndexFunc(views, func(v routeView) bool { return v.address == want })
		pick, ok = routeView{}, i >= 0
		if ok {
			pick = views[i]
		}
	}
	if r.session != nil && r.session.Route != nil && (live || r.session.State == "connected") {
		i := slices.IndexFunc(views, func(v routeView) bool { return v.address == *r.session.Route })
		if i >= 0 {
			pick, ok = views[i], true
		}
	}
	switch {
	case ok && pick.answers:
		dot, text = t.Success, routeText(pick)
		if pick.address == "" && !pick.direct {
			dot = t.Warning
		}
	case ok && pick.address != "":
		text = routeText(pick)
	case r.last != 0:
		text = L.DeviceLast(time.Unix(r.last, 0).Local().Format("2006-01-02 15:04"))
	}
	if r.prefer != "" {
		text = L.RouteFixed(text)
	}
	if r.session != nil && r.session.State == "failed" {
		dot, text = t.Danger, failureText(*r.session, helperGap{})
	}
	b := ui.ButtonBase(c.Key("device-route-" + r.id)).FillWidth().Radius(6).Label(L.RouteChoose()).Tooltip(text)
	b.Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if live {
				busyMark(c, text, busySize)
			} else {
				ui.Box(c).Width(onlineDot).Height(onlineDot).Radius(onlineDot / 2).Background(dot)
			}
			ui.Text(c, text).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, "▾").FontSize(sizeSmall).TextColor(t.TextMuted)
		})
	})
	if len(r.features) > 0 || len(r.routes) > 0 || r.last != 0 {
		b.Menu(func(m *ui.Menu) { a.routeMenu(m, r, views) })
	}
}

// statusWords says in words why a host's status does not show: getting
// it, no saved password or key, an older Kyokai, refused, or how old the
// last one is.
func (a *App) statusWords(c *ui.Context, w *localapi.Watched) {
	t := c.Theme()
	if w == nil {
		return
	}
	text := ""
	switch w.State {
	case "connecting":
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			busyMark(c, L.StatusGetting(), busySize)
			ui.Text(c, L.StatusGetting()).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		return
	case "no-credential":
		text = L.StatusNoCredential()
	case "unsupported":
		text = L.StatusUnsupported()
	case "refused":
		text = L.StatusRefused()
	}
	if text == "" && w.ReceivedAt != 0 {
		text = L.StatusFrom(time.UnixMilli(w.ReceivedAt).Local().Format("15:04"))
	}
	if text != "" {
		ui.Text(c, text).FontSize(sizeSmall).TextColor(t.TextMuted)
	}
}

// bytesText writes a size in bytes, one decimal below ten: "8.1 GB", "470 GB".
func bytesText(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v, i = v/1000, i+1
	}
	if i == 0 {
		return strconv.FormatUint(n, 10) + " B"
	}
	prec := 1
	if v >= 10 {
		prec = 0
	}
	return strconv.FormatFloat(v, 'f', prec, 64) + " " + units[i]
}

// rate writes bytes per second: "1.2 MB/s".
func rate(bps uint64) string { return bytesText(bps) + "/s" }

// uptime writes how long a host is up: "3 d 4 h", "5 h 12 min", "7 min".
func uptime(s uint64) string {
	d, h, m := s/86400, s/3600%24, s/60%60
	switch {
	case d > 0:
		return fmt.Sprintf("%d d %d h", d, h)
	case h > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d min", m)
}
