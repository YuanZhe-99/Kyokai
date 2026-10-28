package gui

import (
	"errors"
	"slices"
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
	loaded       bool
	direct       bool
	latencyFirst bool
	profiles     []localapi.NetworkProfile
	form         *netForm
	// easytier is the official EasyTier core the networks run on (its
	// tag), empty for Kyokai Network; cores are the ones downloaded and
	// downloadable, once asked (coresAsked).
	easytier   string
	cores      *localapi.Cores
	coresAsked bool
}

// take keeps what the engine answered of the networks.
func (s *netState) take(n *localapi.NetworkProfiles) {
	s.direct, s.latencyFirst, s.profiles, s.easytier = n.Direct, n.LatencyFirst, n.Profiles, n.EasyTier
}

// netForm is one network's editable settings. The secret field is
// write-only: it starts empty and is never filled from the engine.
type netForm struct {
	id                    string
	label, servers, name  string // servers: one URL per line
	secret                string
	hasSecret, weakSecret bool
	// toml is the official core's own options (EasyTier chosen).
	toml string
}

// maxNetworks is how many networks the engine keeps.
const maxNetworks = 8

// networkView is the Network page: how the networks are doing, every
// network kept with its Join switch and the devices it reaches, other ways
// in (by address), and the choices few people need.
func (a *App) networkView(c *ui.Context) {
	settingsPage(c, L.NavNetwork(), L.SettingsAboutNetwork(), func() {
		if !a.engineState(c) {
			return
		}
		a.mu.Lock()
		loaded, busy := a.net.loaded, a.busy["network"]
		a.mu.Unlock()
		if !loaded && !busy {
			a.loadNetworks()
		}
		a.networkSummary(c)
		a.engineSection(c)
		a.networksSection(c)
		a.section(c, "ways-in", L.SectionOtherWays(), nil, func(r *rows) { a.byAddressRows(c, r) })
		a.advancedSection(c)
	})
}

// networkSummary is how the networks are doing at a glance: online,
// connecting, failing or offline, and in how many of those joined.
func (a *App) networkSummary(c *ui.Context) {
	t := c.Theme()
	word, col := a.networkState(t)
	a.mu.Lock()
	joined, online := 0, 0
	for _, n := range a.status.Network.Networks {
		if n.Joined {
			joined++
			if n.State == "online" {
				online++
			}
		}
	}
	a.mu.Unlock()
	about := L.NetworkSummary(joined, online)
	if joined == 0 {
		about = L.NetworkSummaryNone()
	}
	state := func(c *ui.Context) {
		ui.Box(c).Size(40, 40).Shrink(0).Radius(10).Center().Background(col.Alpha(0.14)).Children(func() {
			ui.Icon(c, navNetwork).FontSize(20).TextColor(col)
		})
	}
	ui.Box(c).Height(18).Shrink(0)
	a.list(c, "net-summary", func(r *rows) { r.row(c, "net-state", state, setting{title: word, about: about}, nil) })
}

// advancedSection holds the network choices few people need: Try direct
// connections in every network (applied at once; the helper always tries
// them, so it is on and fixed in its modes), which makes this device listen
// and is said to, and Latency-first routing, which only changes how routes
// are chosen.
func (a *App) advancedSection(c *ui.Context) {
	a.mu.Lock()
	net, netBusy, modeBusy := a.net, a.busy["network"], a.busy["mode"]
	a.mu.Unlock()
	helperMode := a.currentMode() != modeUser
	a.section(c, "advanced", L.NetworkAdvanced(), nil, func(r *rows) {
		// Unlike a choice of protocol, direct connections make this device
		// listen on the network and answer strangers' packets: said plainly.
		direct := setting{title: L.NetworkDirect(), about: L.NetworkDirectAbout(), more: L.NetworkDirectHint() + "\n\n" + L.NetworkDirectWarning()}
		if helperMode {
			direct.about, direct.more = L.NetworkDirectHelper(), ""
		}
		on := net.direct || helperMode
		r.row(c, "net-direct", tile(barActions), direct, func() {
			if ui.Switch(c.Key("net-direct-sw"), &on).Label(L.NetworkDirect()).
				Disabled(modeBusy || netBusy || helperMode || !net.loaded).Changed() {
				a.setDirect(on)
			}
		})
		latency := net.latencyFirst
		r.row(c, "net-latency", tile(rowPulse), setting{title: L.NetworkLatencyFirst(), about: L.NetworkLatencyFirstHint()}, func() {
			if ui.Switch(c.Key("net-latency-sw"), &latency).Label(L.NetworkLatencyFirst()).
				Disabled(modeBusy || netBusy || !net.loaded).Changed() {
				a.setLatencyFirst(latency)
			}
		})
	})
}

// setLatencyFirst prefers the lowest-latency route in every network, or
// the fewest hops; the engine restarts its networks.
func (a *App) setLatencyFirst(on bool) {
	a.mu.Lock()
	a.net.latencyFirst = on
	a.mu.Unlock()
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkLatencyFirst, LatencyFirst: &on}, func() {
		a.setToast(L.NetworkSaved(), false)
	})
}

// networksSection lists the networks kept, Add a network beside its title:
// each with its state, Join and a menu of Edit and Remove, the devices it
// reaches under it while online, and its form under it while edited (a new
// network's on top).
func (a *App) networksSection(c *ui.Context) {
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
	aside := func() {
		if len(st.profiles) >= maxNetworks {
			return
		}
		if ui.Button(c.Key("net-add"), L.NetworkAdd()).Disabled(busy || st.form != nil || !st.loaded).Clicked() {
			a.mu.Lock()
			a.net.form = &netForm{}
			a.mu.Unlock()
		}
	}
	a.section(c, "networks", L.NetworkTitle(), aside, func(r *rows) {
		if !st.loaded {
			r.block(c, "net-loading", func() { busyMark(c, L.NetworkTitle(), busySizeLarge) })
			return
		}
		if st.form != nil && st.form.id == "" {
			r.block(c, "net-form-new", func() { a.networkForm(c, *st.form, busy) })
		}
		if len(st.profiles) == 0 && st.form == nil {
			r.block(c, "net-none", func() {
				ui.Text(c, L.NetworkNone())
				ui.Text(c, L.NetworksHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
		}
		for i, p := range st.profiles {
			a.networkRows(c, r, p, states[p.ID], known, busy, i == 0)
			if st.form != nil && st.form.id == p.ID {
				r.block(c, "net-form-"+p.ID, func() { a.networkForm(c, *st.form, busy) })
			}
		}
	})
}

// networkRows are one network's rows: its name, network name and state (its
// devices, address, NAT type and port while online; why it failed), Join
// and a menu of Edit and Remove; while online, the devices it reaches, each
// with Copy and Connect.
func (a *App) networkRows(c *ui.Context, r *rows, p localapi.NetworkProfile, s localapi.NetworkState, known map[string]localapi.About, busy, first bool) {
	t := c.Theme()
	state := s.State
	if !p.Joined {
		state = "off"
	}
	var parts []string
	if p.Label != "" {
		parts = append(parts, p.Name)
	}
	var col ui.Color
	switch state {
	case "online":
		parts = append(parts, L.NetworkDeviceCount(len(s.Devices)))
		if s.Address != "" {
			parts = append(parts, s.Address)
		}
		parts = append(parts, natLabel(s.NAT))
		if s.ListenPort != 0 {
			parts = append(parts, L.NetworkListenPort(strconv.Itoa(s.ListenPort)))
		}
		if s.Unreachable != "" {
			parts, col = append(parts, L.NetworkUnreachable()), t.Warning
		}
	case "connecting":
		parts = append(parts, L.NetworkConnecting())
	case "error":
		parts, col = []string{L.NetworkErrorReason(s.Error)}, t.Danger
	default:
		parts = append(parts, L.NetworkNotJoined())
	}
	set := setting{title: networkLabel(p), about: strings.Join(parts, " · "), col: col}
	if first {
		set.more = L.NetworksHint()
	}
	r.row(c, "net-"+p.ID, tile(navNetwork), set, func() {
		switch state {
		case "online":
			ui.Text(c, L.NetworkOnline()).FontSize(sizeSmall).TextColor(textTone(t, t.Success))
		case "connecting":
			busyMark(c, L.NetworkConnecting(), busySize)
		}
		joined := p.Joined
		if ui.Switch(c.Key("net-join-"+p.ID), &joined).Label(L.NetworkJoin()).Disabled(busy).Changed() {
			a.setJoined(p.ID, joined)
		}
		iconButtonBase(c, "net-more-"+p.ID, rowMore, L.ActionMore()).Disabled(busy).Menu(func(m *ui.Menu) {
			if m.Item(L.ActionEdit()).Chosen() {
				a.mu.Lock()
				a.net.form = &netForm{id: p.ID, label: p.Label, servers: strings.Join(p.Servers, "\n"), name: p.Name,
					hasSecret: p.HasSecret, weakSecret: p.WeakSecret, toml: p.TOML}
				a.mu.Unlock()
			}
			if m.Item(L.ActionRemove()).Chosen() {
				go a.removeNetwork(p.ID, func() bool {
					return a.confirm(L.NetworkRemoveConfirm(networkLabel(p)), L.ActionRemove())
				})
			}
		})
	})
	if state != "online" {
		return
	}
	for i, d := range s.Devices {
		about := known[d.DeviceID]
		path := L.PathRelay()
		if d.Path == "direct" {
			path = L.PathDirect()
		}
		ds := setting{title: deviceTitle(about, d.DeviceID), about: path + " · " + L.LatencyMS(d.LatencyMS)}
		if i == 0 {
			ds.more = L.NetworkDevicesHint()
		}
		r.row(c, "dev-"+p.ID+"-"+d.DeviceID, avatarLead(about), ds, func() {
			if ui.Button(c.Key("dev-copy-"+p.ID+"-"+d.DeviceID), L.ActionCopy()).Clicked() {
				mygo.Clipboard.WriteText(d.DeviceID)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
			if ui.Button(c.Key("dev-connect-"+p.ID+"-"+d.DeviceID), L.ActionConnect()).Clicked() {
				a.connectDevice(d.DeviceID, "", about)
			}
		})
	}
}

// networkForm adds a network (f.id "") or edits one: the name shown here,
// its servers, network name and secret (write-only), then Save and Cancel.
// Save needs a server and a network name.
func (a *App) networkForm(c *ui.Context, f netForm, busy bool) {
	t := c.Theme()
	a.mu.Lock()
	easytier := a.net.easytier != ""
	a.mu.Unlock()
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
			ui.Text(c, L.NetworkSecretWeak()).FontSize(sizeSmall).TextColor(textTone(t, t.Warning))
		}
		if easytier {
			field("toml", L.NetworkOptions(), L.NetworkOptionsHint(), func() {
				edit(ui.TextArea(c.Key(key+"-toml-in"), &f.toml).FillWidth().MinHeight(tomlAreaH).Disabled(busy))
			})
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
			a.net.take(resp.Networks)
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
				a.net.take(resp.Networks)
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
	a.mu.Lock()
	if a.net.easytier != "" {
		change.TOML = &f.toml
	}
	a.mu.Unlock()
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

// engineChoice is one entry of the Runs on menu: Kyokai Network (tag "")
// or an official EasyTier core, downloaded or to download.
type engineChoice struct {
	tag            string
	pre, installed bool
}

// engineChoices are the Runs on menu's entries: Kyokai Network, then the
// official cores this system can run, newest first, each once (one only
// downloaded, no longer listed, after them); the one in use is always among
// them.
func engineChoices(cur string, cores *localapi.Cores) []engineChoice {
	out := []engineChoice{{installed: true}}
	has := func(tag string) bool {
		return slices.ContainsFunc(out, func(e engineChoice) bool { return e.tag == tag })
	}
	var installed []string
	if cores != nil {
		installed = cores.Installed
		for _, r := range cores.Releases {
			out = append(out, engineChoice{tag: r.Tag, pre: r.Prerelease, installed: slices.Contains(installed, r.Tag)})
		}
	}
	for _, tag := range append(installed, cur) {
		if tag != "" && !has(tag) {
			out = append(out, engineChoice{tag: tag, installed: true})
		}
	}
	return out
}

// label words an entry of the Runs on menu.
func (e engineChoice) label() string {
	if e.tag == "" {
		return L.NetworkEngineKyokai()
	}
	version := strings.TrimPrefix(e.tag, "v")
	s := L.NetworkEngineEasyTier(version)
	if e.pre {
		s = L.NetworkEngineEasyTierPreview(version)
	}
	if !e.installed {
		s = L.NetworkEngineToDownload(s)
	}
	return s
}

// optionsLost lists, one line a network, the EasyTier options that
// switching back to Kyokai Network removes; none when no network sets any
// (a TOML of comments only sets none), so switching back and forth without
// changing anything asks nothing.
func optionsLost(profiles []localapi.NetworkProfile) []string {
	var out []string
	for _, p := range profiles {
		if len(p.Options) > 0 {
			out = append(out, networkLabel(p)+": "+strings.Join(p.Options, ", "))
		}
	}
	return out
}

// engineSection is Runs on: Kyokai Network, built in, or an official
// EasyTier core, downloaded as it is chosen. The list of cores is asked
// once the networks are loaded.
func (a *App) engineSection(c *ui.Context) {
	a.mu.Lock()
	st, busy, downloading := a.net, a.busy["network"] || a.busy["cores"], a.busy["cores"]
	a.mu.Unlock()
	if st.loaded && !st.coresAsked {
		a.loadCores()
	}
	choices := engineChoices(st.easytier, st.cores)
	labels := make([]string, len(choices))
	cur := ""
	for i, e := range choices {
		labels[i] = e.label()
		if e.tag == st.easytier {
			cur = labels[i]
		}
	}
	ui.Box(c).Height(18).Shrink(0)
	a.list(c, "engine", func(r *rows) {
		r.row(c, "net-engine", tile(navNetwork), setting{title: L.NetworkEngine(), about: L.NetworkEngineAbout(), more: L.NetworkEngineHint()}, func() {
			if downloading {
				busyMark(c, L.NetworkEngineDownloading(), busySize)
			}
			if ui.Select(c.Key("net-engine-sel"), &cur, labels).Width(engineSelectW).Disabled(busy || !st.loaded).Changed() {
				if i := slices.Index(labels, cur); i >= 0 {
					go a.setEngine(choices[i])
				}
			}
		})
	})
}

// loadCores asks which official cores are downloaded and downloadable (a
// request to GitHub through the engine).
func (a *App) loadCores() {
	a.mu.Lock()
	a.net.coresAsked = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestCores})
		a.update(func() {
			if err == nil && resp.Cores != nil {
				a.net.cores = resp.Cores
			}
		})
	}()
}

// setEngine runs the networks on what was chosen: back to Kyokai Network
// after a warning only when some network sets EasyTier options (which are
// removed); to a core not downloaded yet after downloading it.
// Notes: runs off the main thread (the warning is a dialog).
func (a *App) setEngine(e engineChoice) {
	a.mu.Lock()
	st := a.net
	a.mu.Unlock()
	if e.tag == st.easytier {
		return
	}
	redraw := func() { a.update(func() {}) } // the menu shows what is in use again
	if lost := optionsLost(st.profiles); e.tag == "" && len(lost) > 0 &&
		!a.confirm(L.NetworkEngineBackConfirm(strings.Join(lost, "\n")), L.NetworkEngineBack()) {
		redraw()
		return
	}
	if !e.installed {
		a.update(func() { a.busy["cores"] = true })
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestCoreInstall, Tag: e.tag})
		a.update(func() {
			delete(a.busy, "cores")
			if err == nil && resp.Cores != nil && a.net.cores != nil {
				a.net.cores.Installed = resp.Cores.Installed
			}
		})
		if err != nil {
			a.update(func() { a.setToast(L.NetworkEngineDownloadFailed(), true) })
			return
		}
	}
	tag := e.tag
	a.changeNetworks(localapi.Request{Type: localapi.RequestNetworkBackend, EasyTier: &tag}, func() {
		a.setToast(L.NetworkSaved(), false)
	})
}
