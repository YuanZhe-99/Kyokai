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

func TestGrouping(t *testing.T) {
	cases := []struct {
		f        func(string) string
		in, want string
	}{
		{groupDigits, "482916735", "482 916 735"},
		{groupDigits, "1234", "1 234"},
		{groupHex, "59c68587466e", "59c6 8587 466e"},
	}
	for _, c := range cases {
		if got := c.f(c.in); got != c.want {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}

// demoApp is an App with the status a running engine pushes.
func demoApp(t *testing.T) *App {
	a := New("", t.TempDir(), filepath.Join(t.TempDir(), "none.sock"))
	a.connected = true
	a.status = &localapi.Status{
		Version: "0.1.0", ABI: 1, DeviceID: "482916735",
		Fingerprint: "59c68587466e421ef9a0ec25d8136406b3f5b36882c243f989c273b41cb82b99",
		StateDir:    `C:\Users\demo\AppData\Roaming\Kyokai`, RunMode: "user", Persistence: "os-store",
		PasswordSerial: 7, Started: time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local),
	}
	return a
}

// TestViewsRender renders every page in each language and theme; it fails
// on a panic. With SCREENSHOT_DIR set it writes PNGs for review.
func TestViewsRender(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	pages := map[string]func(a *App){
		"home":          func(a *App) {},
		"home-password": func(a *App) { a.password, a.passwordFor, a.showPassword = "k7mq2xpa", 7, true },
		"home-memory":   func(a *App) { a.status.Persistence = "memory-only" },
		"starting":      func(a *App) { a.connected, a.status = false, nil },
		"engine-down":   func(a *App) { a.connected, a.status, a.engineErr = false, nil, "kyokai-svc not found" },
		"settings":      func(a *App) { a.page = pageSettings },
		"licenses":      func(a *App) { a.page, a.showLicenses = pageSettings, true },
		"network-off":   func(a *App) { a.page, a.net = pageNetwork, netForm{loaded: true} },
		"home-sessions": func(a *App) {
			a.status.Network.State = "online"
			a.status.Requests = []localapi.HostRequest{{ID: 3, DeviceID: "213559601", NewKey: true, Want: []string{localapi.PermViewScreen, localapi.PermControlInput}}}
			a.status.Sessions = []localapi.SessionInfo{
				{ID: 1, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`{"refused":"password"}`)},
				{ID: 2, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`"key-changed"`)},
				{ID: 4, Role: "host", DeviceID: "100000002", State: "connected", Granted: []string{localapi.PermViewScreen}},
			}
		},
		"settings-access": func(a *App) {
			a.page, a.status.PermanentPassword = pageSettings, true
			a.access = accessForm{loaded: true, trusted: []localapi.TrustedDevice{{Key: "k", DeviceID: "213559601", Last: 1797400000, Granted: []string{localapi.PermViewScreen}}}}
		},
		"network-online": func(a *App) {
			a.page = pageNetwork
			a.status.Network = localapi.NetworkStatus{State: "online", Address: "10.126.126.236", NAT: "symmetric",
				Devices: []localapi.NetworkDevice{{DeviceID: "213559601", Path: "relay", LatencyMS: 36}, {DeviceID: "647222040", Path: "direct", LatencyMS: 4}}}
			a.net = netForm{loaded: true, servers: "tcp://example.org:11010", name: "kyokai", hasSecret: true, weakSecret: true}
		},
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for name, setup := range pages {
			for _, dark := range []bool{false, true} {
				if dark && lang != "en" {
					continue
				}
				a := demoApp(t)
				setup(a)
				tt := ui.NewTester(a.View, 1000, 900)
				tt.SetDark(dark)
				tt.Frame()
				if dir == "" {
					continue
				}
				suffix := ""
				if dark {
					suffix = "-dark"
				}
				f, err := os.Create(filepath.Join(dir, lang+"-"+name+suffix+".png"))
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

func TestServerLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"tcp://a:1", []string{"tcp://a:1"}},
		{" tcp://a:1 \r\n\n  wss://b/et\n", []string{"tcp://a:1", "wss://b/et"}},
	}
	for _, c := range cases {
		if got := serverLines(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}
