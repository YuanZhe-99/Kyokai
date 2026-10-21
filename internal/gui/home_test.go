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

// Sparklines take each new sample once and are cleared when the status
// goes too old.
func TestRecordSamples(t *testing.T) {
	a := &App{}
	now := time.Now()
	sample := func(cpu float64, at time.Time) *localapi.Status {
		return &localapi.Status{Watching: []localapi.Watched{{DeviceID: "1", State: "watching",
			Sample: &localapi.SystemSample{CPU: localapi.CPUUse{Usage: cpu}}, ReceivedAt: at.UnixMilli()}}}
	}
	a.recordSamples(sample(10, now), now)
	a.recordSamples(sample(10, now), now) // the same sample again
	a.recordSamples(sample(20, now.Add(2*time.Second)), now.Add(2*time.Second))
	if got := a.watch.rings["1"].cpu.values(); !slices.Equal(got, []float32{10, 20}) {
		t.Fatalf("cpu ring %v", got)
	}
	for i := range ringLen + 5 {
		at := now.Add(time.Duration(3+i) * time.Second)
		a.recordSamples(sample(float64(i), at), at)
	}
	if got := a.watch.rings["1"].cpu.values(); len(got) != ringLen || got[ringLen-1] != ringLen+4 {
		t.Fatalf("a full ring keeps the newest %d: %v", ringLen, got)
	}
	a.recordSamples(sample(1, now), now.Add(10*time.Minute))
	if a.watch.rings["1"].cpu.n != 0 {
		t.Fatal("a ring outlived its status")
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
			About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}},
		{DeviceID: "213559601", Last: 1797300000, Features: tunnel, Saved: localapi.Saved{Key: true, RDP: true},
			About: localapi.About{Name: "Office laptop", Maker: "LENOVO", OS: "windows"}},
		{DeviceID: "647222040", Last: 1797200000, Features: tunnel, About: localapi.About{Name: "NUC", OS: "windows"}},
		{DeviceID: "100000004", Last: 1797100000, About: localapi.About{Name: "Old PC", OS: "windows"}},
		{DeviceID: "100000005", Last: 1797000000, Features: tunnel, Saved: localapi.Saved{Password: true}},
		{DeviceID: "100000006", Last: 1796900000, Features: tunnel, Saved: localapi.Saved{Password: true}, About: localapi.About{Name: "Media box"}},
	}
	sample := &localapi.SystemSample{UptimeS: 3*86400 + 4*3600, CPU: localapi.CPUUse{Usage: 37, Cores: 8},
		Memory: localapi.Usage{Used: 8_100_000_000, Total: 16_000_000_000}, GPU: &localapi.GPUUse{Usage: 12},
		Disk: localapi.Usage{Used: 470_000_000_000, Total: 512_000_000_000}, Net: localapi.NetRates{RxBps: 1_200_000, TxBps: 80_000},
		Battery: &localapi.Battery{Percent: 82, Charging: true}, Services: []string{"ssh", "vnc"}}
	desktop := *sample
	desktop.GPU, desktop.Battery, desktop.Services = nil, nil, []string{"rdp"}
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
		s.CPU.Usage = 20 + float64((i*7)%40)
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
		{"security-keys", 1000, 1900, func(a *App) {
			a.page = pageSecurity
			demoHome(a)
			a.keys = keysState{loaded: true,
				mine:       []localapi.LoginKey{{ID: "1", Name: "This laptop", Type: "ed25519", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop"}},
				authorized: []localapi.AuthorizedKey{{ID: "2", Name: "Phone", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFk1y9lH1qL2rC0gGJ7bH0cE4YqJ6m3z5Wq4A1Xb8yZp phone"}},
				shown:      &shownKey{title: "Private key of “Desk”", pem: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----"},
				form:       keyForm{kind: "import", locked: true, text: "-----BEGIN OPENSSH PRIVATE KEY-----"}}
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
		{"service-form-rdp", &serviceTab{service: serviceRDP, phase: svcForm, form: credForm{user: "yuan"}, note: "The user name or password was not accepted."}},
		{"service-form-vnc", &serviceTab{service: serviceVNC, phase: svcForm, formUser: true}},
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
