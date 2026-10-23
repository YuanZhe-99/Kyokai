package gui

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// metric is one of a host's status tiles (doc/en-us/gui.md, "Home"): a
// name in its colour, a caption over a large figure, a few smaller
// figures below, and the last two minutes as a dot history.
type metric struct {
	key     string
	name    string
	icon    *ui.SVG
	col     ui.Color
	caption string
	value   string
	unit    string
	subs    [][2]string // name, value
	series  []dotSeries // stacked, bottom first
	top     float32     // the history's full scale; 0 is its own highest
}

// dotSeries is one coloured part of a dot history.
type dotSeries struct {
	vals []float32
	col  ui.Color
}

// hostMetrics are the tiles a host's sample shows, in Pulse's order:
// processor, memory, graphics (when reported), disk, network, battery
// (when it has one).
func hostMetrics(s *localapi.SystemSample, r *hostRings) []metric {
	ms := []metric{{
		key: "cpu", name: L.StatusCPU(), icon: statusIconCPU, col: statusCPU,
		caption: L.StatusNow(), value: percentText(s.CPU.Usage), unit: "%",
		subs: [][2]string{
			{L.StatusApps(), percentText(s.CPU.User) + " %"},
			{L.StatusSystem(), percentText(s.CPU.System) + " %"},
			{L.StatusCores(), strconv.Itoa(s.CPU.Cores)},
		},
		series: []dotSeries{{r.cpuUser.values(), statusCPU}, {r.cpuSys.values(), statusCPUSystem}},
		top:    100,
	}}
	used, unit := sizeParts(s.Memory.Used)
	ms = append(ms, metric{
		key: "memory", name: L.StatusMemory(), icon: statusIconMemory, col: statusMemory,
		caption: L.StatusInUseOf(bytesText(s.Memory.Total)), value: used, unit: unit,
		subs: [][2]string{
			{L.StatusUsed(), percentText(share(s.Memory)) + " %"},
			{L.StatusAvailable(), bytesText(s.Memory.Total - min(s.Memory.Used, s.Memory.Total))},
			{L.StatusTotal(), bytesText(s.Memory.Total)},
		},
		series: []dotSeries{{r.memory.values(), statusMemory}},
		top:    100,
	})
	if s.GPU != nil {
		avg, peak := spread(r.gpu.values())
		ms = append(ms, metric{
			key: "gpu", name: L.StatusGPU(), icon: statusIconGPU, col: statusGPU,
			caption: L.StatusNow(), value: percentText(s.GPU.Usage), unit: "%",
			subs:   [][2]string{{L.StatusAverage(), percentText(avg) + " %"}, {L.StatusPeak(), percentText(peak) + " %"}},
			series: []dotSeries{{r.gpu.values(), statusGPU}},
			top:    100,
		})
	}
	free, unit := sizeParts(s.Disk.Total - min(s.Disk.Used, s.Disk.Total))
	disk := metric{
		key: "disk", name: L.StatusDisk(), icon: statusIconDisk, col: statusDisk,
		caption: L.StatusFreeOf(bytesText(s.Disk.Total)), value: free, unit: unit,
		series: []dotSeries{{r.diskRead.values(), statusDisk}, {r.diskWrite.values(), statusDiskWrite}},
	}
	if s.DiskIO != nil {
		disk.subs = [][2]string{{L.StatusReading(), rate(s.DiskIO.ReadBps)}, {L.StatusWriting(), rate(s.DiskIO.WriteBps)}}
	}
	disk.subs = append(disk.subs, [2]string{L.StatusUsed(), percentText(share(s.Disk)) + " %"})
	ms = append(ms, disk)
	rx, unit := sizeParts(s.Net.RxBps)
	ms = append(ms, metric{
		key: "net", name: L.StatusNetwork(), icon: statusIconNetwork, col: statusNetwork,
		caption: L.StatusDownloading(), value: rx, unit: unit + "/s",
		subs: [][2]string{
			{L.StatusUploading(), rate(s.Net.TxBps)},
			{L.StatusReceived(), bytesText(s.Net.RxTotal)},
			{L.StatusSent(), bytesText(s.Net.TxTotal)},
		},
		series: []dotSeries{{r.rx.values(), statusNetwork}, {r.tx.values(), statusNetworkUp}},
	})
	if b := s.Battery; b != nil {
		caption, col := L.StatusOnBattery(), statusBattery
		if b.Charging {
			caption = L.StatusChargingNow()
		}
		if b.Percent <= 20 && !b.Charging {
			col = statusBatteryLow
		}
		ms = append(ms, metric{
			key: "battery", name: L.StatusBattery(), icon: statusIconBattery, col: col,
			caption: caption, value: strconv.Itoa(b.Percent), unit: "%",
			series: []dotSeries{{r.battery.values(), col}},
			top:    100,
		})
	}
	return ms
}

// metricTile draws one metric: compact on a device card (name, figure,
// a short history; clicking opens the details), full on the details
// (with its caption and smaller figures).
// Returns: whether it was clicked.
func metricTile(c *ui.Context, key string, m metric, full bool) bool {
	t := c.Theme()
	rows, valueSize, pad := dotRowsCard, sizeTitle, float32(10)
	if full {
		rows, valueSize, pad = dotRowsFull, sizeMetric, cardPad
	}
	body := func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, m.icon).Size(statusIconSize, statusIconSize).TextColor(m.col)
				ui.Text(c, m.name).FontSize(sizeSmall).Bold().TextColor(m.col).SingleLine().Grow(1).MinWidth(0)
				if !full {
					ui.Text(c, "›").FontSize(sizeSmall).TextColor(t.TextMuted)
				}
			})
			if full {
				ui.Text(c, m.caption).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
			}
			ui.Row(c).Gap(3).AlignItems(ui.End).Children(func() {
				ui.Text(c, m.value).FontSize(valueSize).Bold().FontFeatures("tnum").SingleLine()
				ui.Text(c, m.unit).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().Padding(0, 0, 3, 0)
			})
			if full && len(m.subs) > 0 {
				ui.Row(c).FillWidth().Gap(gap).Children(func() {
					for _, s := range m.subs {
						ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, s[0]).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
							ui.Text(c, s[1]).Bold().FontFeatures("tnum").SingleLine()
						})
					}
				})
			}
			dotHistory(c, m.series, m.top, rows, m.col)
		})
	}
	if full {
		ui.Row(c.Key(key)).MinWidth(0).AlignItems(ui.Start).Padding(pad).Radius(cardRadius).Background(t.Surface).Border(1, t.Border).Children(body)
		return false
	}
	b := ui.ButtonBase(c.Key(key)).MinWidth(0).Padding(pad).Radius(tileRadius).Background(m.col.Alpha(tileTint(t))).
		Label(m.name + " " + m.value + " " + m.unit).Tooltip(L.StatusDetails())
	b.Children(body)
	return b.Clicked()
}

// tileTint is how strongly a compact tile takes its metric's colour.
func tileTint(t *ui.Theme) float32 {
	if t.Dark {
		return 0.14
	}
	return 0.08
}

// dotHistory draws a history as columns of dots, newest on the right:
// each column lights as many dots, from the bottom, as its values reach
// of top (0: the highest value shown), each series in its colour.
func dotHistory(c *ui.Context, series []dotSeries, top float32, rows int, col ui.Color) {
	h := float32(rows)*dotPitch - (dotPitch - dotSize) + 2*dotInset
	ui.Box(c).FillWidth().Height(h).Radius(tileRadius - 2).Background(col.Alpha(0.08)).Draw(func(p *ui.Painter, r ui.Rect) {
		cols := min(int((r.W-2*dotInset+dotPitch-dotSize)/dotPitch), ringLen)
		if cols <= 0 {
			return
		}
		hi := top
		if hi <= 0 {
			for i := range ringLen {
				var sum float32
				for _, s := range series {
					if i < len(s.vals) {
						sum += s.vals[i]
					}
				}
				hi = max(hi, sum)
			}
		}
		x0 := r.X + r.W - dotInset - float32(cols)*dotPitch + (dotPitch - dotSize)
		for i := range cols {
			x := x0 + float32(i)*dotPitch
			// Column i shows the value cols-1-i samples back.
			back := cols - 1 - i
			lit := 0
			for _, s := range series {
				n := 0
				if j := len(s.vals) - 1 - back; j >= 0 && hi > 0 {
					// Rounded, but anything above nothing lights a dot.
					v := s.vals[j] / hi * float32(rows)
					n = int(math.Round(float64(v)))
					if n == 0 && v > 0 {
						n = 1
					}
				}
				for k := lit; k < min(lit+n, rows); k++ {
					p.Fill(dotRect(r, x, k, rows), s.col, dotSize/2)
				}
				lit = min(lit+n, rows)
			}
			for k := lit; k < rows; k++ {
				p.Fill(dotRect(r, x, k, rows), col.Alpha(0.22), dotSize/2)
			}
		}
	})
}

// dotRect is the dot in row k (0 at the bottom) of a column at x.
func dotRect(r ui.Rect, x float32, k, rows int) ui.Rect {
	y := r.Y + dotInset + float32(rows-1-k)*dotPitch
	return ui.Rect{X: x, Y: y, W: dotSize, H: dotSize}
}

// statusTab is a host's status in full as a pane of its own, opened from
// its card's figures on Home (openStatus); the watch lease keeps its host
// while it is open (syncWatch).
type statusTab struct {
	app    *App
	device string
	name   string // the device's title (deviceTitle) as it opened
	win    atomic.Pointer[viewerWindow]
}

// statusKey is a host's status pane's tabKey.
func statusKey(device string) string { return "status-" + device }

// openStatus shows a host's status in a pane: the one open, or a new one
// in the main window.
// Notes: call on the main thread.
func (a *App) openStatus(device string) {
	if t := a.tabFor(statusKey(device)); t != nil {
		if w := t.tabWindow(); w != nil {
			w.show(t)
		}
		return
	}
	a.mu.Lock()
	about := a.prefs.named(device, localapi.About{})
	for _, r := range a.statusRecent() {
		if r.DeviceID == device {
			about = a.prefs.named(device, r.About)
		}
	}
	s := &statusTab{app: a, device: device, name: deviceTitle(about, device)}
	a.statuses[device] = s
	a.mu.Unlock()
	a.main.add(s)
	a.homeVisibilityChanged()
}

// tabKey is the status pane's identity.
func (s *statusTab) tabKey() string { return statusKey(s.device) }

// tabTitle names the window while the pane is in focus.
func (s *statusTab) tabTitle() string { return L.StatusPaneTitle(s.name) }

// head is the pane's header: the status icon, the device's name, Status.
func (s *statusTab) head() paneHead {
	title, _ := splitTitle(s.name)
	return paneHead{icon: rowPulse, title: title, sub: L.PaneStatus(), kind: L.PaneStatus()}
}

// tabWindow is the window the pane is in, or nil.
func (s *statusTab) tabWindow() *viewerWindow { return s.win.Load() }

// setTabWindow notes the window the pane moved to.
func (s *statusTab) setTabWindow(w *viewerWindow) { s.win.Store(w) }

// view is the host's status in full, scrolled.
func (s *statusTab) view(c *ui.Context) {
	ui.Scroll(c.Key("status-" + s.device)).Grow(1).Fill().Padding(24).Children(func() { s.app.hostStatusView(c, s.device) })
}

// live reports false: closing the pane ends no connection.
func (s *statusTab) live() bool { return false }

// disconnect forgets the pane as it closes; the next lease drops its host.
func (s *statusTab) disconnect() {
	s.win.Store(nil)
	s.app.mu.Lock()
	if s.app.statuses[s.device] == s {
		delete(s.app.statuses, s.device)
	}
	s.app.mu.Unlock()
	s.app.homeVisibilityChanged()
}

// syncGrab, endGrab and setFront do nothing: the pane takes no keys and no
// clipboard.
func (s *statusTab) syncGrab(bool) {}
func (s *statusTab) endGrab()      {}
func (s *statusTab) setFront(bool) {}

// statusHosts are the hosts whose status panes are open.
// Notes: the caller holds a.mu.
func (a *App) statusHosts() []string {
	hosts := make([]string, 0, len(a.statuses))
	for device := range a.statuses {
		hosts = append(hosts, device)
	}
	slices.Sort(hosts)
	return hosts
}

// hostStatusView is a watched host's status in full (its pane): its tiles
// with every figure, dimmed with their age while stale, else why there are
// none.
func (a *App) hostStatusView(c *ui.Context, device string) {
	t := c.Theme()
	a.mu.Lock()
	rows := deviceRows(a.status, a.prefs)
	var w *localapi.Watched
	for _, x := range a.status.Watching {
		if x.DeviceID == device {
			x := x
			w = &x
		}
	}
	var rings hostRings
	if r := a.watch.rings[device]; r != nil {
		rings = *r
	}
	a.mu.Unlock()
	i := slices.IndexFunc(rows, func(r deviceRow) bool { return r.id == device })
	if i < 0 {
		return // removed from Home: its pane stays empty until closed
	}
	r := rows[i]
	now := time.Now()
	ui.Column(c).FillWidth().MaxWidth(homeMaxW).Gap(gap).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			deviceAvatar(c, r.about)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, r.name()).FontSize(sizeTitle).Bold().SingleLine()
				line := groupDigits(r.id)
				if w != nil && w.Sample != nil && statusFreshness(w.ReceivedAt, now) != statusNone {
					line += " · " + L.StatusUpFor(uptime(w.Sample.UptimeS)) + " · " + L.StatusEvery()
				}
				ui.Text(c, line).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
			})
		})
		fresh := statusNone
		if w != nil {
			fresh = statusFreshness(w.ReceivedAt, now)
		}
		if w == nil || w.Sample == nil || fresh == statusNone {
			a.statusWords(c, w)
			return
		}
		box := ui.Column(c.Key("status-box")).FillWidth()
		width := box.LayoutBounds().W
		box.Children(func() {
			cols := min(max(int((width+gap)/(statusTileMinW+gap)), 1), 3)
			tileW := max((width-gap*float32(cols-1))/float32(cols), 1)
			tracks := make([]ui.Track, cols)
			for i := range tracks {
				tracks[i] = ui.Fixed(tileW)
			}
			g := ui.Grid(c.Key("status-tiles")).ColumnTracks(tracks...).Gap(gap)
			if fresh == statusDim {
				g.Opacity(0.5)
			}
			g.Children(func() {
				for _, m := range hostMetrics(w.Sample, &rings) {
					metricTile(c, "full-"+m.key, m, true)
				}
			})
		})
		if fresh == statusDim {
			age := int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())
			ui.Text(c, L.StatusUpdatedAgo(strconv.Itoa(age))).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
	})
}

// percentText writes a percentage without decimals.
func percentText(v float64) string { return fmt.Sprintf("%.0f", v) }

// share is how much of a total is used, in percent.
func share(u localapi.Usage) float64 {
	if u.Total == 0 {
		return 0
	}
	return float64(min(u.Used, u.Total)) * 100 / float64(u.Total)
}

// spread is the average and the highest of values.
func spread(values []float32) (avg, peak float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
		peak = max(peak, float64(v))
	}
	return sum / float64(len(values)), peak
}

// sizeParts writes a size as a figure of three digits and its unit, as
// the tiles show them: "6.10" "GB", "79.9" "GB", "470" "GB".
func sizeParts(n uint64) (string, string) {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v, i = v/1000, i+1
	}
	if i == 0 {
		return strconv.FormatUint(n, 10), "B"
	}
	prec := 2
	switch {
	case v >= 100:
		prec = 0
	case v >= 10:
		prec = 1
	}
	return strconv.FormatFloat(v, 'f', prec, 64), units[i]
}
