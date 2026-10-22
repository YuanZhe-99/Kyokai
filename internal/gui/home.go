package gui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

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

// The watch lease (watch.set): renewed every leaseEvery while Home shows,
// for at most leaseHosts hosts.
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
	sent  []string
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

// watchLease decides the lease to send: while Home shows, the recent hosts
// (leaseHosts at most), sent when they changed or every leaseEvery;
// hidden, none, sent once.
// Returns: whether to send, and the hosts.
func watchLease(visible bool, recent []localapi.RecentDevice, sent []string, at, now time.Time) (bool, []string) {
	if !visible {
		return len(sent) > 0, []string{}
	}
	hosts := []string{}
	for _, r := range recent {
		if len(hosts) == leaseHosts {
			break
		}
		hosts = append(hosts, r.DeviceID)
	}
	return !slices.Equal(hosts, sent) || now.Sub(at) >= leaseEvery, hosts
}

// homeVisible reports whether Home shows: the main window is visible, on
// its Kyokai tab, at the Home page (not in protected mode).
func (a *App) homeVisible() bool {
	a.mu.Lock()
	shown := a.visible && a.page == pageHome && !a.protected()
	main := a.main
	a.mu.Unlock()
	if !shown || main == nil {
		return false
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	return main.home || len(main.tabs) == 0
}

// homeVisibilityChanged sends the watch lease at once as Home shows or
// hides.
func (a *App) homeVisibilityChanged() { go a.syncWatch() }

// syncWatch sends the watch lease when watchLease says so.
func (a *App) syncWatch() {
	a.watch.mu.Lock()
	defer a.watch.mu.Unlock()
	visible := a.homeVisible()
	a.mu.Lock()
	recent := a.statusRecent()
	sent, at := a.watch.sent, a.watch.at
	a.mu.Unlock()
	send, hosts := watchLease(visible, recent, sent, at, time.Now())
	if !send {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	_, err := localapi.NewClient(a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestWatchSet, Watch: &hosts})
	if err != nil {
		return // an older engine watches nothing; asked again with the next lease
	}
	a.mu.Lock()
	a.watch.sent, a.watch.at = hosts, time.Now()
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

// devicesCard is Home's devices: a grid of cards, one per device, up to
// three side by side (homeColumnsFor).
func (a *App) devicesCard(c *ui.Context) {
	a.mu.Lock()
	rows := deviceRows(a.status, a.prefs)
	rename := a.rename
	routesEdit := a.routesEdit
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
	a.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	ui.Text(c, L.ConnectRecent()).FontSize(sizeHeading).Bold()
	if rename.device != "" {
		a.renameCard(c, rename)
	}
	if routesEdit.device != "" {
		a.routesCard(c, routesEdit)
	}
	// The columns have fixed widths, from the room the box had in the last
	// frame: tracks that share the room would grow with their content.
	box := ui.Column(c.Key("devices-box")).FillWidth().MaxWidth(homeMaxW)
	w := box.LayoutBounds().W
	now := time.Now()
	box.Children(func() {
		cols := homeColumnsFor(w)
		cardW := max((w-gap*float32(cols-1))/float32(cols), homeCardMinW)
		tracks := make([]ui.Track, cols)
		for i := range tracks {
			tracks[i] = ui.Fixed(cardW)
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
				a.deviceCard(c, r, path, on, onNetwork, wp, &ring, cardW, now)
			}
		})
	})
}

// deviceCard is one device's card: its header (avatar, name, Device ID,
// state and ⋯), how the network reaches it, its live status, what is saved
// for it, and its actions.
func (a *App) deviceCard(c *ui.Context, r deviceRow, path localapi.NetworkDevice, online, onNetwork bool, w *localapi.Watched, rings *hostRings, cardW float32, now time.Time) {
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
	ui.Column(c.Key("device-"+r.id)).MinWidth(0).Padding(cardPad).Gap(10).Radius(cardRadius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		a.cardHeader(c, r, online, live, connected, tunnels)
		a.cardPath(c, r, path, online, onNetwork, live)
		a.cardStatus(c, r, w, rings, fresh, cardW, now)
		if flags := savedFlags(r.saved); len(flags) > 0 {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, f := range flags {
					savedChip(c, "chip-"+r.id+"-"+f[0], f[1], nil)
				}
			})
		}
		ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			label := L.ActionConnect()
			if connected || live {
				label = L.ActionShow()
			}
			if ui.PrimaryButton(c.Key("device-go-"+r.id), label).Grow(1).Clicked() {
				a.connectDevice(r.id, "", r.about, nil)
			}
			for _, act := range actions {
				name, tip := L.CardAI(), L.ActionConnectAI()
				if act != actionAI {
					name, tip = serviceName(act), L.CardOpenService(serviceName(act))
				}
				if ui.Button(c.Key("device-"+act+"-"+r.id), name).Grow(1).Tooltip(tip).Clicked() {
					a.runAction(r, act)
				}
			}
		})
	})
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

// runAction opens a device's service or connects to it with its AI, and
// counts the use (cardActions orders by it).
func (a *App) runAction(r deviceRow, act string) {
	a.noteUse(r.id, act)
	if act == actionAI {
		a.connectDevice(r.id, "", r.about, connectWant(true))
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

// cardHeader is a device card's top: avatar, name and Device ID, its state
// as a chip, and the ⋯ menu.
func (a *App) cardHeader(c *ui.Context, r deviceRow, online, live, connected, tunnels bool) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		deviceAvatar(c, r.about)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, r.name()).Bold().SingleLine()
			if r.about.Name != "" {
				ui.Text(c, groupDigits(r.id)).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
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
		ui.Text(c, text).FontSize(sizeSmall).TextColor(col).SingleLine().MaxWidth(homeCardMinW/2).Tooltip(text).
			Padding(2, 8).Radius(chipRadius).Background(col.Alpha(0.12))
		a.deviceMenu(c, r, tunnels)
	})
}

// deviceMenu is a device card's ⋯ menu.
func (a *App) deviceMenu(c *ui.Context, r deviceRow, tunnels bool) {
	t := c.Theme()
	keyChanged := r.session != nil && r.session.State == "failed" && string(r.session.Failure) == `"key-changed"`
	more := ui.ButtonBase(c.Key("device-menu-"+r.id)).Padding(2, 8).Radius(6).Label(L.DeviceMenu()).Tooltip(L.DeviceMenu())
	more.Children(func() { ui.Text(c, "⋯").TextColor(t.TextMuted) })
	more.Menu(func(m *ui.Menu) {
		if m.Item(L.ActionConnect()).Chosen() {
			a.connectDevice(r.id, "", r.about, nil)
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
			a.mu.Lock()
			a.page = pageSecurity
			a.mu.Unlock()
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

// cardStatus is a device's live status: compact tiles while its sample is
// fresh (one clicked opens the details), dimmed with its age while stale,
// else the watch's state in words.
func (a *App) cardStatus(c *ui.Context, r deviceRow, w *localapi.Watched, rings *hostRings, fresh freshness, cardW float32, now time.Time) {
	t := c.Theme()
	if w == nil {
		return
	}
	if fresh == statusNone || w.Sample == nil {
		a.statusWords(c, w)
		return
	}
	// Fixed columns: the tiles share the card's width, whatever they hold.
	tileW := (cardW - 2*cardPad - 2 - tileGap) / 2
	tiles := ui.Grid(c.Key("tiles-"+r.id)).ColumnTracks(ui.Fixed(tileW), ui.Fixed(tileW)).Gap(tileGap)
	if fresh == statusDim {
		tiles.Opacity(0.5)
	}
	open := false
	tiles.Children(func() {
		for _, m := range hostMetrics(w.Sample, rings) {
			if metricTile(c, "tile-"+r.id+"-"+m.key, m, false) {
				open = true
			}
		}
	})
	if open {
		a.mu.Lock()
		a.statusOf = r.id
		a.mu.Unlock()
	}
	line := L.StatusUpFor(uptime(w.Sample.UptimeS))
	if fresh == statusDim {
		line = L.StatusUpdatedAgo(strconv.Itoa(int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())))
	}
	ui.Text(c, line).FontSize(sizeSmall).TextColor(t.TextMuted)
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
