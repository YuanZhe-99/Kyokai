package gui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// How often Home looks at the addresses of the devices it shows, and the
// most it looks at once (doc/en-us/networking.md, "Routes").
const (
	probeEvery  = 60 * time.Second
	probeHosts  = 16
	routeFailed = 2 // failed dials in a row after which a route ranks last
	slowKBPS    = 8000
)

// preferNetwork is a host's chosen route naming the Kyokai network.
const preferNetwork = "network"

// routeView is one route to a device as the interface shows it: the
// network (address "") or an address, its latency now, and whether it
// answers as the device.
type routeView struct {
	address string
	// rttMS is its latency now; -1 unknown.
	rttMS int
	// answers: the network announces the device, or the address answered
	// as the device (an address not looked at yet counts as answering).
	answers bool
	// state is the latest look's state for an address ("" not looked at).
	state  string
	direct bool
	stats  localapi.RouteStats
}

// routeViews are a device's routes: the network (when this device is on
// one, or the device is online there), then its addresses in the order
// they were added.
func routeViews(r deviceRow, path localapi.NetworkDevice, online, onNetwork bool) []routeView {
	var out []routeView
	if online || onNetwork {
		v := routeView{rttMS: -1, answers: online, direct: path.Path == "direct", stats: r.netRoute}
		if online {
			v.rttMS = path.LatencyMS
		}
		out = append(out, v)
	}
	for _, rt := range r.routes {
		v := routeView{address: rt.Address, rttMS: -1, answers: true, direct: true, stats: rt.RouteStats}
		if p := rt.Probe; p != nil {
			v.state, v.answers = p.State, p.State == "ok"
			if p.RTTMS != nil && v.answers {
				v.rttMS = *p.RTTMS
			}
		}
		out = append(out, v)
	}
	return out
}

// bestRoute is the route Auto would take now, as the engine ranks them
// (`rank_routes`): those that answer, not failing, not remembered slow, by
// latency (unknown last), direct before relayed.
// Returns: false when there is no route at all.
func bestRoute(views []routeView) (routeView, bool) {
	if len(views) == 0 {
		return routeView{}, false
	}
	best := 0
	for _, v := range views {
		best = max(best, v.stats.KBPS)
	}
	key := func(v routeView) [5]int {
		b := func(x bool) int {
			if x {
				return 1
			}
			return 0
		}
		rtt := v.rttMS
		if rtt < 0 {
			rtt = 1 << 30
		}
		slow := v.stats.KBPS != 0 && v.stats.KBPS < slowKBPS && v.stats.KBPS < best/2
		return [5]int{b(!v.answers), b(v.stats.Fails >= routeFailed), b(slow), rtt, b(!v.direct)}
	}
	sorted := slices.Clone(views)
	slices.SortStableFunc(sorted, func(x, y routeView) int { kx, ky := key(x), key(y); return slices.Compare(kx[:], ky[:]) })
	return sorted[0], true
}

// routeText words a route for the card's path line and the route menu:
// "Direct · 12 ms", "Relayed · 231 ms", "192.168.1.20 · 2 ms", or the
// address and why it does not answer.
func routeText(v routeView) string {
	name := v.address
	if v.address == "" {
		name = L.RouteNetwork()
		if v.answers {
			name = L.PathRelay()
			if v.direct {
				name = L.PathDirect()
			}
		}
	}
	switch {
	case v.address != "" && v.state == "unreachable":
		return name + " · " + L.RouteNotAnswering()
	case v.state == "other-device":
		return name + " · " + L.RouteOtherDevice()
	case v.state == "incompatible" || v.state == "broken":
		return name + " · " + L.RouteNotKyokai()
	case v.address == "" && !v.answers:
		return name + " · " + L.RouteNotAnswering()
	case v.rttMS >= 0:
		return name + " · " + strconv.Itoa(v.rttMS) + " ms"
	}
	return name
}

// routeMenu is the route choice of a device: Auto, the Kyokai network, each
// address with its latency now, and Addresses….
func (a *App) routeMenu(m *ui.Menu, r deviceRow, views []routeView) {
	if m.Item(L.RouteAuto()).Checked(r.prefer == "").Chosen() {
		go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Prefer: ptr("")})
	}
	for _, v := range views {
		label, want := routeText(v), v.address
		if v.address == "" {
			label, want = L.RouteNetwork(), preferNetwork
			if v.answers {
				label += " · " + routeText(v)
			}
		}
		if m.Item(label).Checked(r.prefer == want).Chosen() {
			go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Prefer: ptr(want)})
		}
	}
	m.Separator()
	if m.Item(L.RouteAddresses()).Chosen() {
		a.mu.Lock()
		a.routesEdit = routesForm{device: r.id}
		a.mu.Unlock()
	}
}

func ptr[T any](v T) *T { return &v }

// setRoutes changes a known host's routes in the engine, and says why not.
// Notes: runs off the main thread; the status brings the change back.
func (a *App) setRoutes(change localapi.RoutesChange) bool {
	_, err := a.requestWith(localapi.Request{Type: localapi.RequestRoutesSet, Routes: &change})
	if err != nil {
		a.update(func() { a.setToast(L.RoutesFailed(), true) })
		return false
	}
	return true
}

// routesForm is the Addresses editor of one device on Home: the address
// being typed.
type routesForm struct {
	device string
	add    string
}

// routesCard edits a device's routes, a card of Home's over its devices:
// the device's avatar, what routes are and the route chosen; every route
// with its latency now, when it was last used and the bitrate it settled
// on, Remove for an address, a line between each; a field to add one
// (Enter adds it), and Done.
func (a *App) routesCard(c *ui.Context, f routesForm) {
	t := c.Theme()
	a.mu.Lock()
	var row deviceRow
	for _, r := range deviceRows(a.status, a.prefs) {
		if r.id == f.device {
			row = r
		}
	}
	var path localapi.NetworkDevice
	online := false
	for _, d := range a.status.Network.Devices {
		if d.DeviceID == f.device {
			path, online = d, true
		}
	}
	onNetwork := len(a.status.Network.Networks) > 0
	a.mu.Unlock()
	if row.id == "" {
		return
	}
	views := routeViews(row, path, online, onNetwork)
	close := func() {
		a.mu.Lock()
		a.routesEdit = routesForm{}
		a.mu.Unlock()
	}
	homeCard(c, "routes-"+f.device).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			deviceAvatar(c, row.about)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.RoutesTitle(row.name())).FontWeight(600)
				ui.Text(c, L.RoutesHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
			chosen := L.RouteAuto()
			if row.prefer == preferNetwork {
				chosen = L.RouteNetwork()
			} else if row.prefer != "" {
				chosen = row.prefer
			}
			ui.Button(c.Key("routes-choice"), L.RouteChosen(chosen)).Menu(func(m *ui.Menu) { a.routeMenu(m, row, views) })
		})
		lines := &rows{a: a}
		for _, v := range views {
			lines.line(c)
			ui.Row(c.Key("route-" + v.address)).FillWidth().MinHeight(routeRowH).Gap(gap).AlignItems(ui.Center).Children(func() {
				dot := t.Success
				if !v.answers {
					dot = t.TextMuted.Alpha(0.5)
				}
				ui.Box(c).Width(onlineDot).Height(onlineDot).Radius(onlineDot / 2).Background(dot)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					label := routeText(v)
					if v.address == "" && v.answers {
						label = L.RouteNetwork() + " · " + label
					}
					ui.Text(c, label).SingleLine().FontFeatures("tnum")
					ui.Text(c, routeHistory(v.stats)).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
				})
				if v.address != "" && ui.Button(c.Key("route-rm-"+v.address), L.ActionRemove()).Clicked() {
					go a.setRoutes(localapi.RoutesChange{DeviceID: f.device, Remove: v.address})
				}
			})
		}
		lines.line(c)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("routes-in"), &f.add).Placeholder(L.RoutesAddHint()).Label(L.RoutesAddHint()).Grow(1).MinWidth(0)
			if in.Changed() {
				a.mu.Lock()
				a.routesEdit.add = f.add
				a.mu.Unlock()
			}
			add := strings.TrimSpace(f.add)
			if ui.Button(c.Key("routes-add"), L.RoutesAdd()).Disabled(add == "").Clicked() || in.Submitted() && add != "" {
				a.mu.Lock()
				a.routesEdit.add = ""
				a.mu.Unlock()
				go a.setRoutes(localapi.RoutesChange{DeviceID: f.device, Add: add})
			}
			if ui.PrimaryButton(c.Key("routes-done"), L.RoutesDone()).Clicked() {
				close()
			}
		})
	})
}

// routeHistory words what earlier sessions remember of a route: when it was
// last used and the bitrate the host settled on.
func routeHistory(s localapi.RouteStats) string {
	parts := []string{L.RouteNeverUsed()}
	if s.LastOK > 0 {
		parts[0] = L.RouteLastUsed(time.Unix(s.LastOK, 0).Local().Format("2006-01-02 15:04"))
	}
	if s.KBPS > 0 {
		parts = append(parts, strconv.FormatFloat(float64(s.KBPS)/1000, 'f', 1, 64)+" Mbit/s")
	}
	if s.Fails >= routeFailed {
		parts = append(parts, L.RouteFailing())
	}
	return strings.Join(parts, " · ")
}

// probeList lists the shown devices whose addresses Home looks at:
// those that keep any.
func probeList(recent []localapi.RecentDevice) []string {
	var out []string
	for _, r := range recent {
		if len(r.Routes) > 0 && len(out) < probeHosts {
			out = append(out, r.DeviceID)
		}
	}
	return out
}

// probeRoutes asks the engine to look at the addresses of the devices Home
// shows, at most every probeEvery while it is visible.
func (a *App) probeRoutes(now time.Time) {
	if !a.homeVisible() {
		return
	}
	a.mu.Lock()
	due := now.Sub(a.probedAt) >= probeEvery
	devices := probeList(a.statusRecent())
	if due {
		a.probedAt = now
	}
	a.mu.Unlock()
	if !due || len(devices) == 0 {
		return
	}
	_, _ = a.requestWith(localapi.Request{Type: localapi.RequestRoutesProbe, Probe: &devices})
}

// routeOf is the route a device's live controller session takes: an
// address, or "" over the Kyokai network.
func (a *App) routeOf(device string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status == nil {
		return ""
	}
	for _, s := range a.status.Sessions {
		if s.Role == "controller" && s.DeviceID == device && s.Route != nil && s.State != "closed" && s.State != "failed" {
			return *s.Route
		}
	}
	return ""
}
