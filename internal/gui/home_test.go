package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// Home lays out one to three cards a row, each at least homeCardMinW.
func TestHomeColumns(t *testing.T) {
	for _, c := range []struct {
		w    float32
		want int
	}{{0, 1}, {299, 1}, {300, 1}, {611, 1}, {612, 2}, {923, 2}, {924, 3}, {1120, 3}, {4000, 3}} {
		if got := homeColumnsFor(c.w); got != c.want {
			t.Errorf("homeColumnsFor(%v) = %d, want %d", c.w, got, c.want)
		}
	}
}

// A sample shows as current for 6 s, dimmed until 60 s, then not at all.
func TestStatusFreshness(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli()) // samples carry milliseconds
	at := func(age time.Duration) int64 { return now.Add(-age).UnixMilli() }
	for _, c := range []struct {
		received int64
		want     freshness
	}{
		{0, statusNone}, {at(0), statusShown}, {at(6 * time.Second), statusShown}, {at(6*time.Second + time.Millisecond), statusDim},
		{at(60 * time.Second), statusDim}, {at(61 * time.Second), statusNone}, {at(time.Hour), statusNone},
	} {
		if got := statusFreshness(c.received, now); got != c.want {
			t.Errorf("age %v: %v, want %v", now.Sub(time.UnixMilli(c.received)), got, c.want)
		}
	}
}

// The lease goes out as Home shows, as its hosts change and every 10 s;
// hidden, an empty lease goes once.
func TestWatchLease(t *testing.T) {
	now := time.Now()
	recent := []localapi.RecentDevice{{DeviceID: "1"}, {DeviceID: "2"}}
	many := make([]localapi.RecentDevice, 12)
	for i := range many {
		many[i].DeviceID = string(rune('a' + i))
	}
	for _, c := range []struct {
		name    string
		visible bool
		recent  []localapi.RecentDevice
		sent    []string
		age     time.Duration
		send    bool
		hosts   int
	}{
		{"shown first", true, recent, nil, time.Hour, true, 2},
		{"renewed after 10 s", true, recent, []string{"1", "2"}, 10 * time.Second, true, 2},
		{"not yet", true, recent, []string{"1", "2"}, 9 * time.Second, false, 2},
		{"hosts changed", true, recent, []string{"1"}, time.Second, true, 2},
		{"at most eight", true, many, nil, 0, true, leaseHosts},
		{"hidden", false, recent, []string{"1", "2"}, time.Second, true, 0},
		{"hidden, already stopped", false, recent, []string{}, time.Hour, false, 0},
	} {
		send, hosts := watchLease(c.visible, c.recent, c.sent, now.Add(-c.age), now)
		if send != c.send || len(hosts) != c.hosts || hosts == nil {
			t.Errorf("%s: send %v hosts %v, want %v and %d", c.name, send, hosts, c.send, c.hosts)
		}
	}
}

// Histories take each new sample once and are cleared when the status
// goes too old.
func TestRecordSamples(t *testing.T) {
	a := &App{}
	now := time.Now()
	sample := func(cpu float64, at time.Time) *localapi.Status {
		return &localapi.Status{Watching: []localapi.Watched{{DeviceID: "1", State: "watching",
			Sample: &localapi.SystemSample{CPU: localapi.CPUUse{Usage: cpu, User: cpu}}, ReceivedAt: at.UnixMilli()}}}
	}
	a.recordSamples(sample(10, now), now)
	a.recordSamples(sample(10, now), now) // the same sample again
	a.recordSamples(sample(20, now.Add(2*time.Second)), now.Add(2*time.Second))
	if got := a.watch.rings["1"].cpuUser.values(); !slices.Equal(got, []float32{10, 20}) {
		t.Fatalf("cpu ring %v", got)
	}
	for i := range ringLen + 5 {
		at := now.Add(time.Duration(3+i) * time.Second)
		a.recordSamples(sample(float64(i), at), at)
	}
	if got := a.watch.rings["1"].cpuUser.values(); len(got) != ringLen || got[ringLen-1] != ringLen+4 {
		t.Fatalf("a full ring keeps the newest %d: %v", ringLen, got)
	}
	a.recordSamples(sample(1, now), now.Add(10*time.Minute))
	if a.watch.rings["1"].cpuUser.n != 0 {
		t.Fatal("a ring outlived its status")
	}
}

// A card offers a service only when it answers and the saved sign-in
// grants it without asking, and the AI only when it is also ready there;
// the most used come first.
func TestCardActions(t *testing.T) {
	all := []string{"use-ssh", "use-rdp", "use-vnc", "run-ai-task"}
	for _, c := range []struct {
		name    string
		sample  *localapi.SystemSample
		tunnels bool
		uses    map[string]int
		want    []string
	}{
		{"no status", nil, true, nil, nil},
		{"all granted, default order", &localapi.SystemSample{Services: []string{"ssh", "rdp", "vnc"}, Offers: all, AI: true}, true, nil, []string{"rdp", "ssh", "vnc", "ai"}},
		{"AI not granted", &localapi.SystemSample{Services: []string{"rdp"}, Offers: []string{"use-rdp"}, AI: true}, true, nil, []string{"rdp"}},
		{"AI granted but not ready", &localapi.SystemSample{Offers: all}, true, nil, nil},
		{"service not answering", &localapi.SystemSample{Services: []string{"ssh"}, Offers: all}, true, nil, []string{"ssh"}},
		{"service needs the host's user", &localapi.SystemSample{Services: []string{"ssh", "vnc"}, Offers: []string{"use-vnc"}}, true, nil, []string{"vnc"}},
		{"host without tunnels", &localapi.SystemSample{Services: []string{"ssh"}, Offers: all, AI: true}, false, nil, []string{"ai"}},
		{"most used first", &localapi.SystemSample{Services: []string{"ssh", "rdp", "vnc"}, Offers: all, AI: true}, true,
			map[string]int{"vnc": 2, "ai": 5, "ssh": 2}, []string{"ai", "ssh", "vnc", "rdp"}},
	} {
		if got := cardActions(c.sample, c.tunnels, c.uses); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Tile figures keep three digits.
func TestSizeParts(t *testing.T) {
	for _, c := range []struct {
		n          uint64
		value, uni string
	}{{512, "512", "B"}, {1_020, "1.02", "KB"}, {6_100_000_000, "6.10", "GB"}, {79_900_000_000, "79.9", "GB"}, {470_000_000_000, "470", "GB"}} {
		if v, u := sizeParts(c.n); v != c.value || u != c.uni {
			t.Errorf("sizeParts(%d) = %s %s, want %s %s", c.n, v, u, c.value, c.uni)
		}
	}
}

// demoHome is Home with devices in every state a card shows.
func demoHome(a *App) {
	now := time.Now()
	a.status.Network = localapi.NetworkStatus{NetworkNode: localapi.NetworkNode{State: "online", Devices: []localapi.NetworkDevice{
		{DeviceID: "657365333", Path: "direct", LatencyMS: 12}, {DeviceID: "213559601", Path: "relay", LatencyMS: 231}, {DeviceID: "100000004", Path: "direct", LatencyMS: 3}}}}
	tunnel := []string{"key-login", "tunnel", "status", "input-sync"}
	a.status.Recent = []localapi.RecentDevice{
		{DeviceID: "657365333", Last: 1797400000, Features: tunnel, Saved: localapi.Saved{Password: true, SSH: true},
			Routes: []localapi.Route{{Address: "192.168.1.20", RouteStats: localapi.RouteStats{LastOK: 1797400000, KBPS: 24000}, Probe: &localapi.RouteProbe{State: "ok", RTTMS: ptr(2)}},
				{Address: "studio.example.org:47310", Probe: &localapi.RouteProbe{State: "unreachable"}}},
			About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}},
		{DeviceID: "213559601", Last: 1797300000, Features: tunnel, Saved: localapi.Saved{Key: true, RDP: true}, Prefer: "network",
			About: localapi.About{Name: "Office laptop", Maker: "LENOVO", OS: "windows"}},
		{DeviceID: "647222040", Last: 1797200000, Features: tunnel, About: localapi.About{Name: "NUC", OS: "windows"}},
		{DeviceID: "100000004", Last: 1797100000, About: localapi.About{Name: "Old PC", OS: "windows"}},
		{DeviceID: "100000005", Last: 1797000000, Features: tunnel, Saved: localapi.Saved{Password: true}},
		{DeviceID: "100000006", Last: 1796900000, Features: tunnel, Saved: localapi.Saved{Password: true}, About: localapi.About{Name: "Media box"}},
	}
	sample := &localapi.SystemSample{UptimeS: 3*86400 + 4*3600, CPU: localapi.CPUUse{Usage: 37, Cores: 8, User: 26, System: 11},
		Memory: localapi.Usage{Used: 8_100_000_000, Total: 16_000_000_000}, GPU: &localapi.GPUUse{Usage: 12},
		Disk: localapi.Usage{Used: 470_000_000_000, Total: 512_000_000_000}, Net: localapi.NetRates{RxBps: 1_200_000, TxBps: 80_000, RxTotal: 1_520_000_000, TxTotal: 2_510_000_000},
		DiskIO:  &localapi.DiskIO{ReadBps: 4_420_000, WriteBps: 1_250_000},
		Battery: &localapi.Battery{Percent: 82, Charging: true}, Services: []string{"ssh", "vnc"},
		Offers: []string{"view-screen", "control-input", "see-status", "use-ssh", "use-vnc", "run-ai-task"}, AI: true}
	desktop := *sample
	desktop.GPU, desktop.Battery, desktop.Services, desktop.Offers, desktop.AI = nil, nil, []string{"rdp", "ssh"}, []string{"see-status", "use-rdp"}, false
	a.status.Watching = []localapi.Watched{
		{DeviceID: "657365333", State: "watching", Sample: sample, ReceivedAt: now.Add(-time.Second).UnixMilli()},
		{DeviceID: "213559601", State: "watching", Sample: &desktop, ReceivedAt: now.Add(-25 * time.Second).UnixMilli()},
		{DeviceID: "647222040", State: "no-credential"},
		{DeviceID: "100000004", State: "unsupported"},
		{DeviceID: "100000005", State: "connecting"},
		{DeviceID: "100000006", State: "offline", ReceivedAt: now.Add(-10 * time.Minute).UnixMilli()},
	}
	a.status.Sessions = []localapi.SessionInfo{{ID: 9, Role: "controller", DeviceID: "657365333", State: "connected"}}
	for i := range 40 {
		at := now.Add(time.Duration(i-40) * 2 * time.Second)
		w := a.status.Watching[0]
		s := *w.Sample
		s.CPU.User = 14 + float64((i*7)%30)
		s.CPU.System = 4 + float64((i*3)%10)
		s.Memory.Used = uint64(7_000_000_000 + (i*90_000_000)%2_000_000_000)
		s.DiskIO = &localapi.DiskIO{ReadBps: uint64((i * 1_700_000) % 9_000_000), WriteBps: uint64((i * 600_000) % 3_000_000)}
		s.Net.TxBps = uint64(40_000 + (i*50_000)%400_000)
		s.Net.RxBps = uint64(200_000 + (i*130_000)%1_500_000)
		w.Sample, w.ReceivedAt = &s, at.UnixMilli()
		a.recordSamples(&localapi.Status{Watching: []localapi.Watched{w}}, at)
	}
}

// TestHomeCardsRender renders Home's device grid — a fresh status with
// sparklines, a stale one, no saved sign-in, an older Kyokai, getting
// status, offline, connected — at several widths, in each language and
// both themes; with SCREENSHOT_DIR set it writes PNGs. Also the Security
// page's keys and the Permissions page's watchers.
func TestHomeCardsRender(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	shots := []struct {
		name   string
		w, h   int
		setup  func(a *App)
		inMain bool
	}{
		{"home-grid", 1280, 1500, demoHome, false},
		{"home-grid-narrow", 820, 2600, demoHome, false},
		{"host-status", 1100, 1100, func(a *App) { demoHome(a); a.statusOf = "657365333" }, false},
		{"home-routes", 1280, 1700, func(a *App) { demoHome(a); a.routesEdit = routesForm{device: "657365333", add: "203.0.113.7"} }, false},
		{"security-keys", 1000, 1900, func(a *App) {
			a.page = pageSecurity
			demoHome(a)
			a.keys = keysState{loaded: true,
				mine:       []localapi.LoginKey{{ID: "1", Name: "This laptop", Type: "ed25519", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop"}},
				authorized: []localapi.AuthorizedKey{{ID: "2", Name: "Phone", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFk1y9lH1qL2rC0gGJ7bH0cE4YqJ6m3z5Wq4A1Xb8yZp phone"}},
				shown:      &shownKey{title: "Private key of “Desk”", pem: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"}, // gitleaks:allow: a made-up key for the screenshot
				form:       keyForm{kind: "import", locked: true, text: "-----BEGIN OPENSSH PRIVATE KEY-----"}}                                                                 // gitleaks:allow: a made-up key for the screenshot
		}, false},
		{"permissions-watchers", 1000, 1300, func(a *App) {
			a.page = pagePermissions
			a.access = accessForm{loaded: true}
			a.status.Watchers = []localapi.Watcher{{DeviceID: "213559601", Since: 1797400000, About: localapi.About{Name: "Office laptop", Maker: "LENOVO"}}}
		}, false},
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, s := range shots {
			for _, dark := range []bool{false, true} {
				a := demoApp(t)
				s.setup(a)
				tt := ui.NewTester(a.View, s.w, s.h)
				tt.SetDark(dark)
				tt.Frame()
				tt.Frame() // the grid takes its columns from its width
				if dir == "" {
					continue
				}
				suffix := ""
				if dark {
					suffix = "-dark"
				}
				f, err := os.Create(filepath.Join(dir, lang+"-"+s.name+suffix+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
		}
	}
	i18n.SetLocaleFunc(nil)
}

// A service tab renders while connecting, asking for a sign-in (with the
// domain for Remote Desktop), and after a changed SSH key; nothing it shows
// is a password.
func TestServiceTabsRender(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	a := demoApp(t)
	for _, c := range []struct {
		name string
		s    *serviceTab
	}{
		{"service-connecting", &serviceTab{service: serviceSSH, phase: svcConnecting}},
		{"service-form-rdp", &serviceTab{service: serviceRDP, phase: svcForm, form: credForm{method: methodAccount, user: "yuan"}, note: "The user name or password was not accepted."}},
		{"service-form-vnc", &serviceTab{service: serviceVNC, phase: svcForm, form: credForm{method: methodAccount}}},
		{"service-form-vnc-password", &serviceTab{service: serviceVNC, phase: svcForm, form: credForm{method: methodPassword}}},
		{"service-form-ssh-key", &serviceTab{service: serviceSSH, phase: svcForm, form: credForm{method: methodKey, user: "yuan", remember: true}}},
		{"service-host-key", &serviceTab{service: serviceSSH, phase: svcEnded, note: "This device's SSH key is not the one it had before.", changed: "ssh-ed25519 AAAA"}},
	} {
		c.s.app, c.s.device, c.s.title = a, "657365333", serviceName(c.s.service)+" · Studio Mac · 657 365 333"
		tt := ui.NewTester(c.s.view, 1000, 600)
		tt.Frame()
		if dir == "" {
			continue
		}
		f, err := os.Create(filepath.Join(dir, "en-"+c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

// The card shows the route Auto would take: one that answers, not failing,
// not remembered slow, by latency, direct before relayed.
func TestBestRoute(t *testing.T) {
	ms := func(v int) *int { return &v }
	row := deviceRow{
		netRoute: localapi.RouteStats{KBPS: 20000},
		routes: []localapi.Route{
			{Address: "192.168.1.20", Probe: &localapi.RouteProbe{State: "ok", RTTMS: ms(2)}},
			{Address: "office.example.org", Probe: &localapi.RouteProbe{State: "other-device", RTTMS: ms(1)}},
			{Address: "10.9.9.9", Probe: &localapi.RouteProbe{State: "unreachable"}},
		},
	}
	relay := localapi.NetworkDevice{Path: "relay", LatencyMS: 231}
	for _, c := range []struct {
		name  string
		edit  func(r *deviceRow)
		path  localapi.NetworkDevice
		on    bool
		want  string
		label string
	}{
		{"the LAN beats the relay", func(*deviceRow) {}, relay, true, "192.168.1.20", "192.168.1.20 · 2 ms"},
		{"failing twice goes behind", func(r *deviceRow) { r.routes[0].Fails = 2 }, relay, true, "", "Relayed · 231 ms"},
		{"remembered slow goes behind", func(r *deviceRow) { r.routes[0].KBPS = 900 }, relay, true, "", "Relayed · 231 ms"},
		{"offline network: the address", func(*deviceRow) {}, localapi.NetworkDevice{}, false, "192.168.1.20", "192.168.1.20 · 2 ms"},
	} {
		r := row
		r.routes = slices.Clone(row.routes)
		c.edit(&r)
		best, ok := bestRoute(routeViews(r, c.path, c.on, true))
		if !ok || best.address != c.want || routeText(best) != c.label {
			t.Errorf("%s: %q %q, want %q %q", c.name, best.address, routeText(best), c.want, c.label)
		}
	}
	if v := routeViews(row, relay, true, true); routeText(v[2]) != "office.example.org · another device answers" {
		t.Errorf("another device: %q", routeText(v[2]))
	}
}
