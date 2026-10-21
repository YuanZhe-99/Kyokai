package gui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

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

// hostRings are a watched host's sparklines and the sample they took last.
type hostRings struct {
	cpu, rx ring
	last    int64 // ReceivedAt of the newest sample pushed
}

// watchState is Home's status watch: the lease last sent and the hosts'
// sparklines. Fields are guarded by a.mu, sent and at by the lease too.
type watchState struct {
	rings map[string]*hostRings
	sent  []string
	at    time.Time
	// mu keeps the lease's requests in order (syncWatch).
	mu sync.Mutex
}

// recordSamples pushes the new samples of a status onto the hosts'
// sparklines and clears those of hosts whose status went too old.
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
		r.cpu.push(float32(w.Sample.CPU.Usage))
		r.rx.push(float32(w.Sample.Net.RxBps))
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
				a.deviceCard(c, r, path, on, wp, &ring, cardW, now)
			}
		})
	})
}

// deviceCard is one device's card: its header (avatar, name, Device ID,
// state and ⋯), how the network reaches it, its live status, what is saved
// for it, and its actions.
func (a *App) deviceCard(c *ui.Context, r deviceRow, path localapi.NetworkDevice, online bool, w *localapi.Watched, rings *hostRings, cardW float32, now time.Time) {
	t := c.Theme()
	live := r.session != nil && (r.session.State == "connecting" || r.session.State == "confirming" || r.session.State == "reconnecting")
	connected := r.session != nil && r.session.State == "connected"
	tunnels := slices.Contains(r.features, "tunnel")
	var services []string
	fresh := statusNone
	if w != nil {
		fresh = statusFreshness(w.ReceivedAt, now)
		if w.Sample != nil && fresh != statusNone {
			services = w.Sample.Services
		}
	}
	ui.Column(c.Key("device-"+r.id)).MinWidth(0).Padding(cardPad).Gap(10).Radius(cardRadius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		a.cardHeader(c, r, online, live, connected, tunnels)
		cardPath(c, r, path, online, live)
		a.cardStatus(c, r, w, rings, fresh, cardW, now)
		if flags := savedFlags(r.saved); len(flags) > 0 {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, f := range flags {
					savedChip(c, "chip-"+r.id+"-"+f[0], f[1], nil)
				}
			})
		}
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			label := L.ActionConnect()
			if connected || live {
				label = L.ActionShow()
			}
			if ui.PrimaryButton(c.Key("device-go-"+r.id), label).Clicked() {
				a.connectDevice(r.id, r.address, r.about, nil)
			}
			for _, s := range []string{serviceSSH, serviceRDP, serviceVNC} {
				if !slices.Contains(services, s) {
					continue
				}
				b := ui.Button(c.Key("device-"+s+"-"+r.id), serviceName(s)).Disabled(!tunnels)
				if !tunnels {
					b.Tooltip(L.CardNeedsUpdate())
				}
				if b.Clicked() {
					a.openService(r.id, r.address, s)
				}
			}
			if ui.Button(c.Key("device-ai-"+r.id), L.CardAI()).Tooltip(L.ActionConnectAI()).Clicked() {
				a.connectDevice(r.id, r.address, r.about, connectWant(true))
			}
		})
	})
}

// cardHeader is a device card's top: avatar, name and Device ID, its state
// as a chip, and the ⋯ menu.
func (a *App) cardHeader(c *ui.Context, r deviceRow, online, live, connected, tunnels bool) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		deviceAvatar(c, r.about)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			name := r.about.Name
			if name == "" {
				name = groupDigits(r.id)
			}
			ui.Text(c, name).Bold().SingleLine()
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
			a.connectDevice(r.id, r.address, r.about, nil)
		}
		if m.Item(L.ActionConnectAI()).Chosen() {
			a.connectDevice(r.id, r.address, r.about, connectWant(true))
		}
		for _, s := range []string{serviceSSH, serviceRDP, serviceVNC} {
			if m.Item(L.CardOpenService(serviceName(s))).Disabled(!tunnels).Chosen() {
				a.openService(r.id, r.address, s)
			}
		}
		m.Separator()
		if m.Item(L.ActionRename()).Chosen() {
			a.mu.Lock()
			a.rename = renameForm{device: r.id, name: a.prefs.Aliases[r.id]}
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

// cardPath is how the network reaches a device: direct or relayed with the
// latency, by address, or when it was last connected.
func cardPath(c *ui.Context, r deviceRow, path localapi.NetworkDevice, online, live bool) {
	t := c.Theme()
	dot, text := t.TextMuted.Alpha(0.5), L.CardNotOnNetwork()
	switch {
	case r.address != "":
		dot, text = t.Success, L.CardByAddress(r.address)
	case online && path.Path == "direct":
		dot, text = t.Success, L.PathDirect()+" · "+strconv.Itoa(path.LatencyMS)+" ms"
	case online && path.Path == "relay":
		dot, text = t.Warning, L.PathRelay()+" · "+strconv.Itoa(path.LatencyMS)+" ms"
	case online:
		dot, text = t.Success, L.CardOnline()
	case r.last != 0:
		text = L.DeviceLast(time.Unix(r.last, 0).Local().Format("2006-01-02 15:04"))
	}
	if r.session != nil && r.session.State == "failed" {
		dot, text = t.Danger, failureText(*r.session, helperGap{})
	}
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if live {
			busyMark(c, text, busySize)
		} else {
			ui.Box(c).Width(onlineDot).Height(onlineDot).Radius(onlineDot / 2).Background(dot)
		}
		ui.Text(c, text).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0).Tooltip(text)
	})
}

// cardStatus is a device's live status: tiles while its sample is fresh,
// dimmed with its age while stale, else the watch's state in words.
func (a *App) cardStatus(c *ui.Context, r deviceRow, w *localapi.Watched, rings *hostRings, fresh freshness, cardW float32, now time.Time) {
	t := c.Theme()
	if w == nil {
		return
	}
	if fresh == statusNone || w.Sample == nil {
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
		return
	}
	s := w.Sample
	// Fixed columns: the tiles share the card's width, whatever they hold.
	tileW := (cardW - 2*cardPad - 2 - tileGap) / 2
	tiles := ui.Grid(c.Key("tiles-"+r.id)).ColumnTracks(ui.Fixed(tileW), ui.Fixed(tileW)).Gap(tileGap)
	if fresh == statusDim {
		tiles.Opacity(0.5)
	}
	tiles.Children(func() {
		statusTile(c, "cpu", L.StatusCPU(), fmt.Sprintf("%.0f %%", s.CPU.Usage), func() {
			ui.Meter(c, s.CPU.Usage, 0, 100, &ui.MeterLevels{Warning: 80, Critical: 95}).Label(L.StatusCPU()).FillWidth()
			sparkline(c, rings.cpu.values(), 100, t.Accent)
		})
		usageTile(c, "memory", L.StatusMemory(), s.Memory, 0.8, 0.95)
		if s.GPU != nil {
			statusTile(c, "gpu", L.StatusGPU(), fmt.Sprintf("%.0f %%", s.GPU.Usage), func() {
				ui.Meter(c, s.GPU.Usage, 0, 100, &ui.MeterLevels{Warning: 80, Critical: 95}).Label(L.StatusGPU()).FillWidth()
			})
		}
		usageTile(c, "disk", L.StatusDisk(), s.Disk, 0.85, 0.95)
		statusTile(c, "net", L.StatusNetwork(), "↓"+rate(s.Net.RxBps)+" ↑"+rate(s.Net.TxBps), func() {
			sparkline(c, rings.rx.values(), 0, t.Accent)
		})
		if b := s.Battery; b != nil {
			value := strconv.Itoa(b.Percent) + " %"
			if b.Charging {
				value = L.StatusCharging(value)
			}
			statusTile(c, "battery", L.StatusBattery(), value, func() {
				ui.Meter(c, float64(b.Percent), 0, 100, &ui.MeterLevels{Warning: 20, Critical: 10}).Label(L.StatusBattery()).FillWidth()
			})
		}
		statusTile(c, "uptime", L.StatusUptime(), uptime(s.UptimeS), nil)
	})
	if fresh == statusDim {
		age := int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())
		ui.Text(c, L.StatusUpdatedAgo(strconv.Itoa(age))).FontSize(sizeSmall).TextColor(t.TextMuted)
	}
}

// statusTile is one status value: its name and value, then what more draws
// below (a meter, a sparkline).
func statusTile(c *ui.Context, key, name, value string, more func()) {
	t := c.Theme()
	ui.Column(c.Key("tile-" + key)).Gap(4).MinWidth(0).Children(func() {
		ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
			ui.Text(c, value).FontSize(sizeSmall).FontFeatures("tnum").SingleLine()
		})
		if more != nil {
			more()
		}
	})
}

// usageTile is a tile of bytes used of a total, with a meter that warns
// from warn and turns critical from crit (fractions).
func usageTile(c *ui.Context, key, name string, u localapi.Usage, warn, crit float64) {
	total := float64(u.Total)
	statusTile(c, key, name, L.StatusOf(bytesText(u.Total), bytesText(u.Used)), func() {
		ui.Meter(c, float64(u.Used), 0, max(total, 1), &ui.MeterLevels{Warning: warn * total, Critical: crit * total}).Label(name).FillWidth()
	})
}

// sparkline draws values as a line, from 0 to top (0: to their highest).
func sparkline(c *ui.Context, values []float32, top float32, col ui.Color) {
	ui.Box(c).FillWidth().Height(sparkH).Draw(func(p *ui.Painter, r ui.Rect) {
		if len(values) < 2 {
			return
		}
		hi := top
		if hi <= 0 {
			for _, v := range values {
				hi = max(hi, v)
			}
		}
		if hi <= 0 {
			hi = 1
		}
		xs := make([]float32, len(values))
		ys := make([]float32, len(values))
		for i, v := range values {
			xs[i] = float32(i+ringLen-len(values)) / float32(ringLen-1)
			ys[i] = min(v/hi, 1)
		}
		p.FillPath(chart.Area(r, xs, nil, ys, chart.CurveMonotone), col.Alpha(0.12))
		p.StrokePath(chart.Line(r, xs, ys, chart.CurveMonotone), sparkStroke, col)
	})
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
