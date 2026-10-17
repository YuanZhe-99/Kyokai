package gui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
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

// TestViewerRenders draws the viewer window's states: waiting, a picture
// with stats, and an unsupported codec.
func TestViewerRenders(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for i := range pic.Pix {
		pic.Pix[i] = byte(i * 7)
	}
	cases := map[string]func(v *viewer){
		"viewer-waiting": func(v *viewer) {},
		"viewer-picture": func(v *viewer) {
			v.start, v.bm, v.fps, v.kbps = videoStart{Codec: "raw-rgba", Width: 320, Height: 180}, ui.NewBitmap(pic), 15, 6900
		},
		"viewer-unsupported": func(v *viewer) { v.state = "This picture uses h265, which this version can't show yet." },
	}
	for name, setup := range cases {
		v := &viewer{device: "657365333"}
		setup(v)
		tt := ui.NewTester(v.view, 800, 500)
		tt.Frame()
		if dir == "" {
			continue
		}
		f, err := os.Create(filepath.Join(dir, "en-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func TestViewerInputMapping(t *testing.T) {
	// A 1600×900 picture in an 800×600 box: 800×450, 75 px bars above and below.
	box := ui.Rect{W: 800, H: 600}
	if ox, oy, w, h := pictureBox(box, 1600, 900); ox != 0 || oy != 75 || w != 800 || h != 450 {
		t.Fatalf("box %v %v %v %v", ox, oy, w, h)
	}
	held := map[ui.Key]bool{}
	cases := []struct {
		name string
		ev   ui.InputEvent
		want *inputEvent
	}{
		{"click centre", ui.InputEvent{Kind: ui.InputPointerDown, X: 400, Y: 300}, &inputEvent{Kind: "pointer", Action: "down", X: 0.5, Y: 0.5}},
		{"press on the bar", ui.InputEvent{Kind: ui.InputPointerDown, X: 400, Y: 20}, nil},
		{"release on the bar still goes", ui.InputEvent{Kind: ui.InputPointerUp, X: 400, Y: 20}, &inputEvent{Kind: "pointer", Action: "up", X: 0.5, Y: 0}},
		{"right button", ui.InputEvent{Kind: ui.InputPointerDown, Button: 1, X: 0, Y: 75}, &inputEvent{Kind: "pointer", Action: "down", Button: 1, X: 0, Y: 0}},
		{"letter types as text", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyA}, nil},
		{"text", ui.InputEvent{Kind: ui.InputText, Text: "é"}, &inputEvent{Kind: "text", Text: "é"}},
		{"shortcut goes as a key", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl},
			&inputEvent{Kind: "key", Key: "c", Down: true, Mods: &inputMods{Ctrl: true}}},
		{"its release follows", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyC},
			&inputEvent{Kind: "key", Key: "c", Down: false, Mods: &inputMods{}}},
		{"a release never pressed is dropped", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyC}, nil},
		{"enter", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}, &inputEvent{Kind: "key", Key: "enter", Down: true, Mods: &inputMods{}}},
		{"touchpad scroll in pixels", ui.InputEvent{Kind: ui.InputScroll, DY: 80, Precise: true}, &inputEvent{Kind: "scroll", DY: 2}},
	}
	for _, c := range cases {
		got, ok := toEvent(c.ev, box, 1600, 900, held)
		if (c.want == nil) != !ok {
			t.Errorf("%s: sent=%v, want sent=%v", c.name, ok, c.want != nil)
			continue
		}
		if c.want != nil && !reflect.DeepEqual(got, *c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, *c.want)
		}
	}
}
