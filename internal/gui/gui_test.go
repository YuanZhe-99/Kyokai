package gui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestCaptureDefault protects the default posture: without a choice saved
// (no file, or one from before the setting existed) Kyokai's windows stay
// out of capture, and a saved choice survives a restart.
func TestCaptureDefault(t *testing.T) {
	dir := t.TempDir()
	if loadPrefs(dir).ShowInCapture {
		t.Fatal("no preferences file: windows shown to capture")
	}
	if err := os.WriteFile(prefsPath(dir), []byte(`{"theme":"dark","close_quits":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if loadPrefs(dir).ShowInCapture {
		t.Fatal("older preferences file: windows shown to capture")
	}
	p := loadPrefs(dir)
	p.ShowInCapture = true
	if err := savePrefs(dir, p); err != nil {
		t.Fatal(err)
	}
	if !loadPrefs(dir).ShowInCapture {
		t.Fatal("saved choice lost")
	}
}

// TestStartHiddenDefault: a start at login stays in the tray unless the
// person chose to open the window, older preferences included.
func TestStartHiddenDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(prefsPath(dir), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{prefs: loadPrefs(dir)}
	if !a.StartHidden() {
		t.Fatal("older preferences file: window opened at login")
	}
	a.prefs.ShowAtLogin = true
	if a.StartHidden() {
		t.Fatal("chosen window not opened at login")
	}
}

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
		"home":                func(a *App) {},
		"home-password":       func(a *App) { a.password, a.passwordFor, a.showPassword = "k7mq2xpa", 7, true },
		"home-memory":         func(a *App) { a.status.Persistence = "memory-only" },
		"home-permanent-only": func(a *App) { a.status.PasswordMode, a.status.PermanentPassword = "permanent", true },
		"home-permanent-none": func(a *App) { a.status.PasswordMode = "permanent" },
		"home-passwords-off":  func(a *App) { a.status.PasswordMode = "none" },
		"home-permissions":    func(a *App) { a.status.Permissions = &localapi.Permissions{Accessibility: true} },
		"starting":            func(a *App) { a.connected, a.status = false, nil },
		"engine-down":         func(a *App) { a.connected, a.status, a.engineErr = false, nil, "kyokai-svc not found" },
		"general":             func(a *App) { a.page = pageGeneral },
		"security":            func(a *App) { a.page, a.status.About = pageSecurity, localapi.About{Name: "WINDOWS-KVM"} },
		"about":               func(a *App) { a.page = pageAbout },
		"about-licenses":      func(a *App) { a.page, a.showLicenses = pageAbout, true },
		"network-none":        func(a *App) { a.page, a.net = pageNetwork, netState{loaded: true} },
		"network-add":         func(a *App) { a.page, a.net = pageNetwork, netState{loaded: true, form: &netForm{}} },
		"home-connect": func(a *App) {
			a.status.Network.State = "online"
			a.connect = connectForm{target: "657 365 333"}
			a.status.Recent = []localapi.RecentDevice{
				{DeviceID: "657365333", Last: 1797400000, SavedPassword: true, About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}},
				{DeviceID: "647222040", Last: 1797300000},
			}
		},
		"home-sessions": func(a *App) {
			a.status.Network.State = "online"
			a.status.Permissions = &localapi.Permissions{Accessibility: true, InputMonitoring: true, Keychain: true}
			a.status.Requests = []localapi.HostRequest{{ID: 3, DeviceID: "213559601", NewKey: true, Want: []string{localapi.PermViewScreen, localapi.PermControlInput},
				About: localapi.About{Name: "Office laptop", Maker: "LENOVO", Model: "ThinkPad X1 Carbon", OS: "windows"}}}
			a.status.Recent = []localapi.RecentDevice{
				{DeviceID: "657365333", Last: 1797400000, SavedPassword: true, About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}},
				{DeviceID: "647222040", Last: 1797300000},
			}
			a.status.Sessions = []localapi.SessionInfo{
				{ID: 1, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`{"refused":"password"}`)},
				{ID: 2, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`"key-changed"`)},
				{ID: 4, Role: "host", DeviceID: "100000002", State: "connected", Granted: []string{localapi.PermViewScreen},
					Peer: localapi.About{Name: "Living room PC", Maker: "Dell Inc.", OS: "windows"}},
			}
		},
		"permissions": func(a *App) {
			a.page, a.status.PermanentPassword = pageAccess, true
			a.status.PasswordGrants = []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard, localapi.PermFiles}
			a.status.PasswordMode, a.status.WithoutAsking = "both", true
			a.status.ByAddress = localapi.ByAddress{On: true, Ranges: []string{"192.168.0.0/16", "100.64.0.0/10"}, Port: 47310}
			a.access = accessForm{loaded: true, trusted: []localapi.TrustedDevice{
				{Key: "k", DeviceID: "213559601", Last: 1797400000, Granted: []string{localapi.PermViewScreen}, About: localapi.About{Name: "Office laptop", Maker: "LENOVO"}},
				{Key: "k2", DeviceID: "647222040", Last: 1797300000, Granted: []string{localapi.PermViewScreen}},
			}}
		},
		"home-address": func(a *App) {
			a.connect = connectForm{target: "office.example.org", identifying: true}
			a.prefs.Aliases = map[string]string{"657365333": "Studio"}
			a.rename = renameForm{device: "657365333", name: "Studio"}
			a.status.Recent = []localapi.RecentDevice{{DeviceID: "657365333", Last: 1797400000, Routes: []localapi.Route{{Address: "192.168.1.20"}},
				About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}}}
		},
		"protected": func(a *App) {
			// Security was open: protected mode shows the AI page only.
			a.page, a.status.Protected, a.status.PermanentPassword = pageSecurity, true, true
			a.ai = aiState{loaded: true, settings: demoAISettings(true)}
		},
		"security-no-logs": func(a *App) {
			a.page, a.prefs.NoLogs, a.status.LogsOff = pageSecurity, true, true
		},
		"network-by-address-failed": func(a *App) {
			a.page, a.net = pageNetwork, netState{loaded: true}
			a.status.ByAddress = localapi.ByAddress{On: true, Ranges: []string{"10.0.0.0/8"}}
			edited := "10.0.0.0/8\n172.16.0.0/12"
			a.access = accessForm{loaded: true, ranges: &edited}
		},
		"home-remote-ai": func(a *App) {
			a.status.AI = &localapi.AIActive{ID: 4, State: "running", Origin: "213559601"}
		},
		"ai-new": func(a *App) {
			a.page = pageAI
			s := demoAISettings(false)
			a.ai = aiState{loaded: true, settings: s, edit: &aiEdit{src: s.Sources[2], fetched: []localapi.AIModel{{ID: "qwen/qwen2.5-vl-72b-instruct"}}}}
		},
		"ai-approval": func(a *App) {
			a.page = pageAI
			a.status.AI = &localapi.AIActive{ID: 2, State: "approval"}
			s := demoAISettings(true)
			a.ai = aiState{loaded: true, settings: s, tasks: []localapi.AITask{
				{ID: 2, Instruction: "Save the open document", Source: "claude", Model: "claude-opus-5-5", State: "approval",
					Text: "I will press Save.", Progress: []string{"Pressing Save."},
					Steps:    []localapi.AIStep{{Action: []byte(`{"kind":"screenshot"}`), Outcome: "ok"}},
					Approval: &localapi.AIApproval{Action: []byte(`{"kind":"key-combo","keys":["meta","r"],"repeat":1}`)}},
				{ID: 1, Instruction: "Open the settings", Source: "claude", Model: "claude-opus-5-5", State: "done", Created: 1797400000},
			}}
		},
		// Three networks: one online, one that failed, one not joined.
		"network-online": demoNetworks,
		"network-edit": func(a *App) {
			demoNetworks(a)
			a.net.form = &netForm{id: "a1", label: "Home", servers: "tcp://example.org:11010", name: "kyokai-home", hasSecret: true, weakSecret: true}
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
				height := 900
				switch name {
				case "ai-new", "about-licenses":
					height = 2300 // the editor and the notices run long
				case "network-online", "network-edit":
					height = 1500 // three networks, then how devices reach this one
				}
				tt := ui.NewTester(a.View, 1000, height)
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

// demoNetworks shows the Network page with three networks: Home online with
// two devices, one whose node could not start, and one not joined.
func demoNetworks(a *App) {
	a.page = pageNetwork
	home := localapi.NetworkState{ID: "a1", Label: "Home", Joined: true, NetworkNode: localapi.NetworkNode{
		State: "online", Address: "10.126.126.236", NAT: "symmetric", ListenPort: 47311,
		Devices: []localapi.NetworkDevice{{DeviceID: "213559601", Path: "relay", LatencyMS: 36}, {DeviceID: "647222040", Path: "direct", LatencyMS: 4}}}}
	office := localapi.NetworkState{ID: "b2", Label: "office-et", Joined: true, NetworkNode: localapi.NetworkNode{
		State: "error", Error: "network settings: invalid peer url"}}
	lab := localapi.NetworkState{ID: "c3", Label: "lab", NetworkNode: localapi.NetworkNode{State: "off"}}
	a.status.Network = localapi.NetworkStatus{NetworkNode: home.NetworkNode, Networks: []localapi.NetworkState{home, office, lab}}
	a.net = netState{loaded: true, profiles: []localapi.NetworkProfile{
		{ID: "a1", Label: "Home", Name: "kyokai-home", Servers: []string{"tcp://example.org:11010"}, HasSecret: true, WeakSecret: true, Joined: true},
		{ID: "b2", Name: "office-et", Servers: []string{"wss://et.example.com/et"}, HasSecret: true, Joined: true},
		{ID: "c3", Name: "lab", Servers: []string{"tcp://10.0.0.2:11010"}},
	}}
}

func TestDescribeAction(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	meta := "Win"
	if runtime.GOOS == "darwin" {
		meta = "Command"
	}
	cases := []struct{ action, want string }{
		{`{"kind":"click","at":{"x":640,"y":425},"button":"left","count":1,"modifiers":[]}`, "Click at 640, 425"},
		{`{"kind":"click","at":{"x":1,"y":2},"button":"left","count":2,"modifiers":["shift"]}`, "Double-click at 1, 2, holding Shift"},
		{`{"kind":"click","at":null,"button":"right","count":1,"modifiers":[]}`, "Click where the pointer is"},
		{`{"kind":"drag","path":[{"x":1,"y":2},{"x":3,"y":4},{"x":5,"y":6}],"modifiers":[]}`, "Drag from 1, 2 to 5, 6"},
		{`{"kind":"scroll","at":null,"dx":0,"dy":-240,"modifiers":[]}`, "Scroll up"},
		{`{"kind":"type","text":"hello"}`, "Type “hello”"},
		{`{"kind":"key-combo","keys":["ctrl","shift","t"],"repeat":1}`, "Press Ctrl+Shift+T"},
		{`{"kind":"key-combo","keys":["meta","page-up"],"repeat":1}`, "Press " + meta + "+Page Up"},
		{`{"kind":"hold-key","keys":["shift"],"duration_ms":1500}`, "Hold Shift for 1.5 s"},
		{`{"kind":"wait","duration_ms":2000}`, "Wait 2 s"},
		{`{"kind":"teleport"}`, "An action that could not be read"},
		{`null`, "An action that could not be read"},
	}
	for _, c := range cases {
		if got := describeAction([]byte(c.action)); got != c.want {
			t.Errorf("%s → %q, want %q", c.action, got, c.want)
		}
	}
}

func TestLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"tcp://a:1", []string{"tcp://a:1"}},
		{" tcp://a:1 \r\n\n  wss://b/et\n", []string{"tcp://a:1", "wss://b/et"}},
	}
	for _, c := range cases {
		if got := lines(c.in); !slices.Equal(got, c.want) {
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
	big := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for i := range big.Pix {
		big.Pix[i] = byte(i / 4 % 251)
	}
	cases := map[string]func(v *viewer){
		"viewer-waiting": func(v *viewer) {},
		"viewer-picture": func(v *viewer) {
			v.start, v.bm, v.fps, v.kbps = videoStart{Codec: "raw-rgba", Width: 320, Height: 180}, ui.NewBitmap(pic), 15, 6900
			v.pointer = hostPointer{X: 0.25, Y: 0.5, Visible: true}
		},
		"viewer-unsupported": func(v *viewer) { v.state = "This picture uses h265, which this version can't show yet." },
		"viewer-no-codec": func(v *viewer) {
			v.start = videoStart{Codec: "raw-rgba", Width: 320, Height: 180, Placeholder: "no-codec"}
		},
		"viewer-displays": func(v *viewer) {
			v.start, v.bm = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic)
			v.displays.List = make([]struct {
				Width   int  `json:"width"`
				Height  int  `json:"height"`
				Primary bool `json:"primary"`
			}, 3)
			v.displays.Current = 1
		},
		// A narrow window: what does not fit goes into the toolbar's overflow menu.
		"viewer-narrow": func(v *viewer) {
			v.start, v.bm, v.fps, v.kbps = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), 60, 2400
			v.offer, v.codec, v.fpsCap = []string{"h264", "hevc", "vp9-sw"}, "auto", "60 fps"
		},
		"viewer-folded": func(v *viewer) {
			v.start, v.bm, v.folded = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), true
		},
		"viewer-clipboard": func(v *viewer) {
			v.start, v.bm, v.title = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), "Studio Mac · 657 365 333"
			v.clip = viewerClip{may: true, shown: true, front: true}
			thumb, w, h, _ := thumbnail(pngBase64(pic))
			at := time.Date(2026, 10, 19, 1, 2, 3, 0, time.Local)
			v.addClip(clipEntry{at: at, text: "copied here\nsecond line"})
			v.addClip(clipEntry{host: true, at: at.Add(time.Minute), thumb: ui.NewBitmap(thumb), width: w, height: h})
			v.addClip(clipEntry{host: true, at: at.Add(2 * time.Minute), names: []string{"report.docx", "photos"}, files: 3, size: 2_100_000})
		},
		// Original size: smaller than the area, centred on black.
		"viewer-original-small": func(v *viewer) {
			v.start, v.bm, v.original = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), true
			v.pointer = hostPointer{X: 0.25, Y: 0.5, Visible: true}
		},
		// Larger than the area: it scrolls.
		"viewer-original-large": func(v *viewer) {
			v.start, v.bm, v.original = videoStart{Codec: "h264", Width: 1600, Height: 900}, ui.NewBitmap(big), true
		},
		"viewer-actions": func(v *viewer) {
			v.start, v.bm = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic)
			v.app, v.control, v.actions = &App{}, true, &hostActions{CtrlAltDel: true}
		},
		// A Mac host offers no session actions; the toolbar still holds the
		// keyboard choices (regression: they showed for Windows hosts only).
		"viewer-actions-mac": func(v *viewer) {
			v.start, v.bm = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic)
			v.app, v.control = &App{}, true
			v.phase.Peer.OS = "macos"
		},
	}
	for name, setup := range cases {
		// Connected (pumping), as the stream shows only then.
		v := &viewer{device: "657365333", preset: 1, fpsCap: "30 fps", pumping: name != "viewer-waiting"}
		setup(v)
		width := 1100
		if name == "viewer-narrow" {
			width = 560
		}
		tt := ui.NewTester(v.view, width, 560)
		tt.Frame()
		tt.Frame() // the pointer marker uses the first frame's layout
		// At its original size the picture's box is the picture, one point
		// per pixel, whatever room the window has.
		if v.original && (v.box.W != float32(v.start.Width) || v.box.H != float32(v.start.Height)) {
			t.Errorf("%s: picture box %vx%v, want %dx%d", name, v.box.W, v.box.H, v.start.Width, v.start.Height)
		}
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

// The picture area offers Reconnect while the host will not try again, or
// once no picture of its screen showed for reconnectAfter; not while the
// host still brings it up (regression: a Mac host's "no codec" placeholder
// stayed until the pane was closed and opened again).
func TestViewerOffersReconnect(t *testing.T) {
	cases := []struct {
		name, placeholder string
		lost, want        bool
	}{
		{"no codec in common", "no-codec", false, true},
		{"a screen it cannot share", "cannot-capture", false, true},
		{"the host waking its display", "asleep", false, false},
		{"waiting for the first picture", "", false, false},
		{"waiting for long", "", true, true},
		{"the host trying again for long", "retrying", true, true},
	}
	for _, c := range cases {
		v := &viewer{device: "657365333", preset: 1, fpsCap: "30 fps", pumping: true,
			start: videoStart{Codec: "raw-rgba", Width: 320, Height: 180, Placeholder: c.placeholder}, screenLost: c.lost}
		tt := ui.NewTester(v.view, 1100, 560)
		tt.Frame()
		if got := tt.HasText(L.ViewerReconnect()); got != c.want {
			t.Errorf("%s: Reconnect shown %v, want %v", c.name, got, c.want)
		}
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
		name    string
		ev      ui.InputEvent
		grabbed string
		want    *inputEvent
	}{
		{"click centre", ui.InputEvent{Kind: ui.InputPointerDown, X: 400, Y: 300}, "", &inputEvent{Kind: "pointer", Action: "down", X: 0.5, Y: 0.5}},
		{"press on the bar", ui.InputEvent{Kind: ui.InputPointerDown, X: 400, Y: 20}, "", nil},
		{"release on the bar still goes", ui.InputEvent{Kind: ui.InputPointerUp, X: 400, Y: 20}, "", &inputEvent{Kind: "pointer", Action: "up", X: 0.5, Y: 0}},
		{"right button", ui.InputEvent{Kind: ui.InputPointerDown, Button: 1, X: 0, Y: 75}, "", &inputEvent{Kind: "pointer", Action: "down", Button: 1, X: 0, Y: 0}},
		// MyGo reports a move with no button held as button -1: the pointer
		// follows it (it used to be dropped, so the pointer moved on clicks only).
		{"hover moves the pointer", ui.InputEvent{Kind: ui.InputPointerMove, Button: -1, X: 200, Y: 300}, "", &inputEvent{Kind: "pointer", Action: "move", X: 0.25, Y: 0.5}},
		{"a drag keeps its button", ui.InputEvent{Kind: ui.InputPointerMove, Button: 0, X: 400, Y: 300}, "", &inputEvent{Kind: "pointer", Action: "move", X: 0.5, Y: 0.5}},
		{"hover on the bar", ui.InputEvent{Kind: ui.InputPointerMove, Button: -1, X: 400, Y: 20}, "", nil},
		{"no button pressed is no press", ui.InputEvent{Kind: ui.InputPointerDown, Button: -1, X: 400, Y: 300}, "", nil},
		{"letter types as text", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyA}, "", nil},
		{"text", ui.InputEvent{Kind: ui.InputText, Text: "é"}, "", &inputEvent{Kind: "text", Text: "é"}},
		{"shortcut goes as a key", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl}, "",
			&inputEvent{Kind: "key", Key: "c", Down: true, Mods: &inputMods{Ctrl: true}}},
		{"its release follows", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyC}, "",
			&inputEvent{Kind: "key", Key: "c", Down: false, Mods: &inputMods{}}},
		{"a release never pressed is dropped", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyC}, "", nil},
		{"enter", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}, "", &inputEvent{Kind: "key", Key: "enter", Down: true, Mods: &inputMods{}}},
		{"touchpad scroll in pixels", ui.InputEvent{Kind: ui.InputScroll, DY: 80, Precise: true}, "", &inputEvent{Kind: "scroll", DY: 2}},
		// While the core grabs the keyboard, keys come from the grab; text that
		// still reaches the window (characters another program injects as text)
		// goes as text, and a release whose press was sent still goes.
		{"held before the grab", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyF5}, "", &inputEvent{Kind: "key", Key: "f5", Down: true, Mods: &inputMods{}}},
		{"map: no keys", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}, "map", nil},
		{"map: injected text", ui.InputEvent{Kind: ui.InputText, Text: "a"}, "map", &inputEvent{Kind: "text", Text: "a"}},
		{"map: the release of a sent press", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyF5}, "map", &inputEvent{Kind: "key", Key: "f5", Mods: &inputMods{}}},
		{"translate: no keys", ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, Mods: ui.Ctrl}, "translate", nil},
		{"translate: text", ui.InputEvent{Kind: ui.InputText, Text: "ü"}, "translate", &inputEvent{Kind: "text", Text: "ü"}},
	}
	// At its original size the box is the picture: a point is a pixel.
	original := ui.Rect{W: 1600, H: 900}
	if got, ok := toEvent(ui.InputEvent{Kind: ui.InputPointerDown, X: 400, Y: 225}, original, 1600, 900, held, ""); !ok || got.X != 0.25 || got.Y != 0.25 {
		t.Fatalf("original size: %+v %v", got, ok)
	}
	for _, c := range cases {
		got, ok := toEvent(c.ev, box, 1600, 900, held, c.grabbed)
		if (c.want == nil) != !ok {
			t.Errorf("%s: sent=%v, want sent=%v", c.name, ok, c.want != nil)
			continue
		}
		if c.want != nil && !reflect.DeepEqual(got, *c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, *c.want)
		}
	}
}

// TestPointerMovesCoalesce: moves wait as one pending move, the newest
// replacing the older, and any other event sends the pending move first.
func TestPointerMovesCoalesce(t *testing.T) {
	v := &viewer{out: make(chan []byte, 8), moved: make(chan struct{}, 1)}
	move := func(x float32) inputEvent { return inputEvent{Kind: "pointer", Action: "move", X: x, Y: 0.5} }
	v.queue(move(0.1))
	v.queue(move(0.2))
	if len(v.out) != 0 || v.move == nil || v.move.X != 0.2 {
		t.Fatalf("pending %+v, queued %d", v.move, len(v.out))
	}
	press := inputEvent{Kind: "pointer", Action: "down", X: 0.3, Y: 0.5}
	v.queue(press)
	if v.move != nil || len(v.out) != 2 {
		t.Fatalf("pending %+v, queued %d", v.move, len(v.out))
	}
	for _, want := range []inputEvent{move(0.2), press} {
		if got, wantP := <-v.out, controlPacket(want); !bytes.Equal(got, wantP) {
			t.Errorf("sent %s, want %s", got, wantP)
		}
	}
	// The writer sends a pending move, then nothing more for it.
	v.queue(move(0.4))
	var buf bytes.Buffer
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { v.writer(&buf, done); close(stopped) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		v.mu.Lock()
		sent := v.move == nil
		v.mu.Unlock()
		if sent || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	<-stopped
	p, err := localapi.ReadPacket(&buf, nil)
	if err != nil || !bytes.Equal(p, controlPacket(move(0.4))) {
		t.Fatalf("wrote %s (%v)", p, err)
	}
}

// TestCodecChoice: the viewer names codecs as people know them (never by
// their wire names), and a (re)started stream tells the host the bar's
// quality and codec.
// pngBase64 is an image as a clipboard packet carries it.
func pngBase64(img image.Image) string {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// TestClipboardHistory: entries go newest first by when they crossed, at
// most clipKeep; an image becomes a thumbnail that fits.
func TestClipboardHistory(t *testing.T) {
	v := &viewer{}
	at := time.Unix(1797400000, 0)
	for i := range clipKeep + 5 {
		v.addClip(clipEntry{at: at.Add(time.Duration(i) * time.Second), text: strconv.Itoa(i)})
	}
	// One decoded late (an image) still goes in its place: after 34…30.
	v.addClip(clipEntry{at: at.Add(30 * time.Second), text: "late"})
	h := v.clip.history
	if len(h) != clipKeep || h[0].text != strconv.Itoa(clipKeep+4) || h[5].text != "late" {
		t.Fatalf("history %d, first %q", len(h), h[0].text)
	}
	thumb, w, h2, ok := thumbnail(pngBase64(image.NewRGBA(image.Rect(0, 0, 1920, 1080))))
	if !ok || w != 1920 || h2 != 1080 || thumb.Bounds().Dx() != clipThumbW || thumb.Bounds().Dy() != 135 {
		t.Fatalf("thumbnail %v %dx%d %v", thumb.Bounds(), w, h2, ok)
	}
	if _, _, _, ok := thumbnail("not base64!"); ok {
		t.Fatal("garbage made a thumbnail")
	}
	for n, want := range map[int64]string{820: "820 B", 2_100_000: "2.1 MB", 1500: "1.5 kB"} {
		if got := sizeLabel(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

// TestFrontFollowsTheWindowUsedLast: only the pane in focus in the window
// used last is in front (none while Home is), and each change tells the
// engine once.
func TestFrontFollowsTheWindowUsedLast(t *testing.T) {
	a := &App{viewers: map[string]*viewer{}}
	a.home = &homeTab{app: a}
	newV := func(device string) *viewer {
		v := &viewer{app: a, device: device, out: make(chan []byte, 8)}
		a.viewers[device] = v
		return v
	}
	x, y, z := newV("111111111"), newV("222222222"), newV("333333333")
	a.main = &viewerWindow{app: a, main: true}
	for _, v := range []tab{x, y} {
		a.main.tabs = append(a.main.tabs, v)
		a.main.placeLocked(v, nil, zoneMiddle)
	}
	other := &viewerWindow{app: a, tabs: []tab{z}}
	other.placeLocked(z, nil, zoneMiddle)
	sent := func(v *viewer) string {
		select {
		case p := <-v.out:
			return string(p[1:])
		default:
			return ""
		}
	}
	a.lastWin = a.main
	a.syncFront()
	if x.clip.front || !y.clip.front || z.clip.front {
		t.Fatal("the main window's shown tab should be in front")
	}
	if sent(y) != `{"front":true,"share":true,"type":"clipboard-use"}` || sent(x) != "" {
		t.Fatal("only the change is sent")
	}
	a.lastWin = other
	a.syncFront()
	if y.clip.front || !z.clip.front || sent(y) == "" || sent(z) == "" {
		t.Fatal("the other window's tab should be in front now")
	}
	a.lastWin = a.main
	a.main.homeGroupLocked()
	a.syncFront()
	if x.clip.front || y.clip.front || z.clip.front {
		t.Fatal("while Home shows no pane is in front")
	}
}

func TestCodecChoice(t *testing.T) {
	for name, want := range map[string]string{"h264": "H.264", "hevc": "HEVC", "vp9": "VP9", "vp9-sw": "VP9", "av1": "AV1", "vp8": "VP8"} {
		if got := codecLabel(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	// The choice names codecs only: VP9 once, the host's better one.
	names, labels := codecChoices([]string{"hevc", "vp9", "vp9-sw", "av1"})
	if !slices.Equal(names, []string{"auto", "hevc", "vp9", "av1"}) || !slices.Equal(labels[1:], []string{"HEVC", "VP9", "AV1"}) {
		t.Errorf("choices %q %q", names, labels)
	}
	// Hardware or software is said as each side reports it.
	for i, want := range map[codecImpl]string{
		{"videotoolbox", true}: L.ViewerImplHardware("VideoToolbox"),
		{"libaom", false}:      L.ViewerImplSoftware("libaom"),
		{"", false}:            L.ViewerImplUnknown(),
	} {
		if got := implLabel(i); got != want {
			t.Errorf("%v: %q, want %q", i, got, want)
		}
	}
	v := &viewer{preset: 2, fpsCap: "60 fps", full: true, codec: "auto"}
	got := v.choicePackets()
	// The clipboard choice goes last; it stays with this side's engine.
	if len(got) != 3 || !strings.Contains(string(got[0]), `"fps":60`) || !strings.Contains(string(got[0]), `"preset":"best"`) || !strings.Contains(string(got[0]), `"i444":true`) || string(got[1][1:]) != `{"on":true,"type":"audio"}` ||
		string(got[2][1:]) != `{"front":false,"share":true,"type":"clipboard-use"}` {
		t.Fatalf("auto: %q", got)
	}
	v.codec, v.mute, v.clip.off = "av1", true, true
	got = v.choicePackets()
	if len(got) != 4 || string(got[1][1:]) != `{"codec":"av1","type":"codec"}` || string(got[2][1:]) != `{"on":false,"type":"audio"}` ||
		string(got[3][1:]) != `{"front":false,"share":false,"type":"clipboard-use"}` {
		t.Fatalf("av1, muted: %q", got)
	}
}

// TestScanEvent: a key the grab took crosses as the host's scan event
// (input::Event::Scan), with the controller's lock state.
func TestScanEvent(t *testing.T) {
	var got map[string]any
	b, _ := json.Marshal(scanEvent(core.KeyEvent{Window: 7, Code: 0xE01D, Down: 1, Caps: 1}))
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["kind"] != "scan" || got["code"] != float64(0xE01D) || got["down"] != true || got["caps"] != true || got["num"] != nil {
		t.Fatalf("%s", b)
	}
}

// TestDeviceLogoName: the maker decides first, then the model, the
// friendly name and the system; only whole words match.
func TestDeviceLogoName(t *testing.T) {
	for _, tc := range []struct {
		about localapi.About
		want  string
	}{
		{localapi.About{Maker: "LENOVO", Model: "20XW0055US", OS: "windows"}, "lenovo"},
		{localapi.About{Maker: "Dell Inc.", Model: "XPS 13 9310", OS: "windows"}, "dell"},
		{localapi.About{Maker: "ASUSTeK COMPUTER INC.", OS: "windows"}, "asus"},
		{localapi.About{Maker: "Hewlett-Packard", OS: "windows"}, "hp"},
		{localapi.About{Maker: "Apple", Model: "MacBookPro18,3", OS: "macos"}, "apple"},
		{localapi.About{Maker: "innotek GmbH", Model: "VirtualBox", OS: "windows"}, "oracle"},
		// No maker the table knows: the name, then the system.
		{localapi.About{Maker: "QEMU", Name: "My ThinkPad", OS: "windows"}, "lenovo"},
		{localapi.About{Maker: "QEMU", Model: "Standard PC (Q35 + ICH9, 2009)", OS: "windows"}, "windows"},
		{localapi.About{Maker: "Micro-Star International", Name: "php-dev", OS: ""}, ""},
		{localapi.About{}, ""},
	} {
		if got := deviceLogoName(tc.about); got != tc.want {
			t.Errorf("deviceLogoName(%+v) = %q, want %q", tc.about, got, tc.want)
		}
	}
}

// TestVendorLogosParse: every bundled logo parses, and every logo the
// keyword table names is bundled, so no match draws nothing.
func TestVendorLogosParse(t *testing.T) {
	files, err := fs.Glob(logoFS, "assets/logos/*.svg")
	if err != nil || len(files) == 0 {
		t.Fatalf("no logos embedded: %v", err)
	}
	for _, f := range files {
		b, _ := logoFS.ReadFile(f)
		if _, err := ui.ParseSVG(b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, k := range vendorKeywords {
		if loadLogo(k.logo) == nil {
			t.Errorf("keyword logo %q is missing or unparsable", k.logo)
		}
		for _, w := range k.words {
			if w != strings.ToLower(w) {
				t.Errorf("keyword %q must be lower-case", w)
			}
		}
	}
}

// TestDeviceTitle: a name goes beside the Device ID, never instead of it.
func TestDeviceTitle(t *testing.T) {
	if got := deviceTitle(localapi.About{Name: "Living room"}, "482916735"); got != "Living room · 482 916 735" {
		t.Errorf("named: %q", got)
	}
	if got := deviceTitle(localapi.About{}, "482916735"); got != "482 916 735" {
		t.Errorf("unnamed: %q", got)
	}
}

// TestWindowsArg guards the helper install on Windows: the app's folder
// can contain spaces, and an unquoted space split it in two.
func TestWindowsArg(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`install`, `install`},
		{`S-1-5-21-1-2-3-1001`, `S-1-5-21-1-2-3-1001`},
		{`C:\Users\a b\AppData\Local\Programs\Kyokai\bin`, `"C:\Users\a b\AppData\Local\Programs\Kyokai\bin"`},
		{`C:\dir with space\`, `"C:\dir with space\\"`},
		{`say "hi"`, `"say \"hi\""`},
		{`a\"b`, `"a\\\"b"`},
		{``, `""`},
	} {
		if got := windowsArg(tc.in); got != tc.want {
			t.Errorf("windowsArg(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// demoAISettings are the AI settings the screenshots show: the two default
// sources and an OpenRouter one.
func demoAISettings(keys bool) *localapi.AISettings {
	return &localapi.AISettings{
		Sources: []localapi.AISource{
			{ID: "claude", Name: "Claude", Template: "anthropic", Protocol: "anthropic", URL: "https://api.anthropic.com/v1", Models: []localapi.AIModel{{ID: "claude-opus-5-5"}}},
			{ID: "openai", Name: "OpenAI", Template: "openai", Protocol: "openai-responses", URL: "https://api.openai.com/v1", Models: []localapi.AIModel{{ID: "gpt-6.1-sol"}}},
			{ID: "openrouter", Name: "OpenRouter", Template: "openrouter", Protocol: "openai-chat", URL: "https://openrouter.ai/api/v1",
				Models: []localapi.AIModel{{ID: "anthropic/claude-opus-5-5", Name: "Anthropic: Claude Opus 5.5"}, {ID: "google/gemini-3-pro"}}},
		},
		Model:    &localapi.AIModelRef{Source: "claude", ID: "claude-opus-5-5"},
		Approval: "flagged",
		Keys:     map[string]bool{"claude": keys, "openrouter": keys},
	}
}

// The main window (doc/en-us/gui.md, "Windows"), in every language and both
// themes: the sidebar lists Home and the connections (none, one, or a
// group of several as one entry); the pane shown has its header and its
// picture, one still waiting for the device's user the password field; the
// window's controls keep their room; folded, the sidebar is a rail;
// Settings is a sheet over everything.
func TestShellRenders(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for i := range pic.Pix {
		pic.Pix[i] = byte(i * 5)
	}
	a := &App{viewers: map[string]*viewer{}, services: map[string]*serviceTab{}, busy: map[string]bool{}}
	a.connected = true
	a.status = &localapi.Status{DeviceID: "482916735", Persistence: "os-store", Network: localapi.NetworkStatus{NetworkNode: localapi.NetworkNode{State: "online",
		Devices: []localapi.NetworkDevice{{DeviceID: "657365333", Path: "direct", LatencyMS: 3}, {DeviceID: "402118905", Path: "relay", LatencyMS: 48}}}}}
	// For the + picker: one device offering SSH, one whose saved sign-in
	// does not grant it, one without status.
	a.status.Recent = []localapi.RecentDevice{
		{DeviceID: "657365333", Last: 1797400000, Features: []string{"tunnel"}, About: localapi.About{Name: "Studio Mac", Maker: "Apple", OS: "macos"}},
		{DeviceID: "402118905", Last: 1797300000, Features: []string{"tunnel"}, About: localapi.About{Name: "NUC", Maker: "Intel", OS: "windows"}},
		{DeviceID: "647222040", Last: 1797200000},
	}
	a.status.Watching = []localapi.Watched{
		{DeviceID: "657365333", ReceivedAt: time.Now().UnixMilli(), Sample: &localapi.SystemSample{Services: []string{serviceSSH}, Offers: []string{localapi.PermUseSSH, localapi.PermRunAITask}, AI: true}},
		{DeviceID: "402118905", ReceivedAt: time.Now().UnixMilli(), Sample: &localapi.SystemSample{Services: []string{serviceSSH}}},
	}
	a.home = &homeTab{app: a}
	var tabs []tab
	for i, c := range []struct{ title, device, state string }{
		{"Office laptop · 213 559 601", "213559601", "connected"},
		{"Studio Mac · 657 365 333", "657365333", "confirming"},
		{"647 222 040", "647222040", "failed"},
		{"NUC · 402 118 905", "402118905", "connected"},
	} {
		v := &viewer{app: a, device: c.device, title: c.title, preset: 1, fpsCap: "30 fps",
			start: videoStart{Codec: "vp9-sw", Width: 320, Height: 180}, bm: ui.NewBitmap(pic),
			phase: localapi.SessionInfo{ID: uint64(i + 1), DeviceID: c.device, State: c.state, Failure: []byte(`{"refused":"password"}`)}}
		v.pumping = c.state == "connected"
		tabs = append(tabs, v)
		a.viewers[c.device] = v
	}
	dir := os.Getenv("SCREENSHOT_DIR")
	type drop struct {
		tab, target int
		zone        dropZone
	}
	defer func(own bool) { ownWindowButtons = own }(ownWindowButtons)
	shots := []struct {
		name     string
		cur      int // -1: Home
		empty    bool
		mac      bool
		drops    []drop
		rail     bool
		settings bool
		// width: the window's, else 1280; drag: a pane dragged over the
		// panes.
		width int
		drag  bool
		// The + picker open where it opened from, of a kind, with words
		// typed; search open with words typed.
		pick, kind, query string
		palette           bool
	}{
		{name: "shell-empty", cur: -1, empty: true}, {name: "shell-home", cur: -1},
		{name: "shell-picture"}, {name: "shell-waiting", cur: 1}, {name: "shell-failed", cur: 2},
		// The window's controls: the traffic lights over the sidebar, or
		// the buttons the view draws on the top-right pane's header.
		{name: "shell-titlebar-mac", mac: true}, {name: "shell-titlebar-windows", cur: -1},
		// A group: two side by side; one full height beside two stacked;
		// four in two columns.
		{name: "shell-split-columns", drops: []drop{{1, 0, zoneRight}}},
		{name: "shell-split-three", drops: []drop{{1, 0, zoneRight}, {2, 1, zoneBottom}}},
		{name: "shell-split-four", drops: []drop{{1, 0, zoneRight}, {2, 1, zoneBottom}, {3, 0, zoneBottom}}},
		{name: "shell-rail", rail: true, drops: []drop{{1, 0, zoneRight}}},
		{name: "shell-rail-mac", mac: true, rail: true, drops: []drop{{1, 0, zoneRight}}},
		// Panes at their minimum; a pane with no room to split right;
		// the drop zones that fit.
		{name: "shell-split-min", width: 989, drops: []drop{{1, 0, zoneRight}}},
		{name: "shell-split-room", width: 1600, drops: []drop{{1, 0, zoneRight}, {2, 1, zoneBottom}}},
		{name: "shell-drop-room", width: 1600, drag: true, drops: []drop{{1, 0, zoneRight}, {2, 1, zoneBottom}}},
		{name: "shell-settings", cur: -1, settings: true},
		{name: "shell-picker", cur: -1, pick: "sidebar"},
		{name: "shell-picker-ssh", cur: -1, pick: "sidebar", kind: serviceSSH},
		{name: "shell-picker-ai", cur: -1, pick: "sidebar", kind: actionAI},
		{name: "shell-picker-typed", cur: -1, pick: "sidebar", query: "192.168.1.20"},
		{name: "shell-picker-rail", cur: -1, rail: true, pick: "rail"},
		{name: "shell-picker-split", pick: "split-2-213559601"},
		{name: "shell-palette", cur: -1, palette: true},
		{name: "shell-palette-query", palette: true, query: "pass"},
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, dark := range []bool{false, true} {
			if dark && lang != "en" {
				continue
			}
			for _, shot := range shots {
				vw := &viewerWindow{app: a, main: true}
				a.main = vw
				vw.homeGroupLocked()
				for _, v := range tabs {
					if shot.empty {
						break
					}
					vw.tabs = append(vw.tabs, v)
					vw.placeLocked(v, nil, zoneMiddle)
				}
				for _, d := range shot.drops {
					vw.placeLocked(tabs[d.tab], tabs[d.target], d.zone)
				}
				if shot.cur < 0 {
					vw.homeGroupLocked()
				} else if len(shot.drops) == 0 {
					vw.placeLocked(tabs[shot.cur], nil, zoneMiddle)
				}
				a.prefs.SidebarRail = shot.rail
				a.settingsOpen, a.page = shot.settings, pageGeneral
				a.picker, a.palette = pickerState{}, paletteState{}
				if shot.pick != "" {
					a.picker = pickerState{at: shot.pick, kind: shot.kind, query: shot.query}
				}
				if shot.palette {
					a.palette = paletteState{open: true, query: shot.query}
				}
				cur, want := vw.cur, vw.cur.split
				ownWindowButtons = !shot.mac
				width := 1280
				if shot.width > 0 {
					width = shot.width
				}
				tt := ui.NewTester(vw.view, width, 800)
				tt.SetDark(dark)
				if shot.mac {
					tt.SetTitleBar(ui.TitleBar{Height: 28, Left: trafficMainX + 54})
				}
				tt.Frame()
				tt.Frame()
				if shot.drag {
					// NUC's sidebar row dragged over the lower right pane.
					r, _ := tt.Find("NUC")
					tt.Press(r.X+r.W/2, r.Y+r.H/2)
					for _, x := range []float32{400, 800, 1200, 1450} {
						tt.Move(x, 600)
						tt.Frame()
					}
				}
				if vw.cur != cur || vw.cur.split != want {
					t.Fatalf("%s: rendering changed the pane shown", shot.name)
				}
				if dir == "" {
					continue
				}
				suffix := ""
				if dark {
					suffix = "-dark"
				}
				f, err := os.Create(filepath.Join(dir, lang+"-"+shot.name+suffix+".png"))
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

// TestIncompatibleNamesTheOutdatedSide: a version mismatch blames this
// device's helper when it is behind the app, and names the other device's
// version when it is known.
func TestIncompatibleNamesTheOutdatedSide(t *testing.T) {
	failed := localapi.SessionInfo{State: "failed", Failure: json.RawMessage(`"incompatible"`)}
	withPeer := failed
	withPeer.PeerVersion = "0.1.2"
	old := helperGap{"0.1.1", version.Version}
	for _, tc := range []struct {
		name   string
		s      localapi.SessionInfo
		behind helperGap
		want   string
	}{
		{"older helper", withPeer, old, L.FailHelperOutdated("0.1.1", version.Version)},
		{"other device", withPeer, helperGap{}, L.FailIncompatibleVersion("0.1.2", version.Version)},
		{"unknown", failed, helperGap{}, L.FailIncompatible()},
	} {
		if got := sessionState(tc.s, tc.behind); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestHelperBehind: a helper of an older version, or of the same version
// from another build (a test build, possibly another wire protocol), is
// updated; a newer one, the same build, or the app's own engine is not.
func TestHelperBehind(t *testing.T) {
	helper := func(v, build string) *localapi.Status {
		return &localapi.Status{RunMode: "helper", Version: v, Build: build}
	}
	for _, tc := range []struct {
		name  string
		st    *localapi.Status
		build string
		want  bool
	}{
		{"older version", helper("0.1.1", "a"), "b", true},
		{"same version, other build", helper(version.Version, "a"), "b", true},
		{"same version, build not reported", helper(version.Version, ""), "b", true},
		{"same build", helper(version.Version, "b"), "b", false},
		{"app without a build record", helper(version.Version, "a"), "", false},
		{"newer helper", helper("99.0.0", "a"), "b", false},
		{"own engine", &localapi.Status{RunMode: "user", Version: "0.1.1"}, "b", false},
		{"not attached", nil, "b", false},
	} {
		if got := helperBehind(tc.st, tc.build); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestViewerPictureTakesText: macOS makes text of keys only for a focused
// element that takes text, and the viewer sends letters as that text.
func TestViewerPictureTakesText(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	v := &viewer{device: "657365333", preset: 1, fpsCap: "30 fps", control: true, app: &App{}}
	v.phase.State = "connected"
	v.start, v.bm = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic)
	tt := ui.NewTester(v.view, 1100, 560)
	tt.Frame()
	tt.Frame()
	v.mu.Lock()
	box := v.box
	v.mu.Unlock()
	tt.SetFocused(true)
	tt.ClickAt(box.X+box.W/2, box.Y+box.H/2)
	tt.Frame()
	if active, _ := tt.TextInputActive(); !active {
		t.Fatal("the focused picture does not take text")
	}
}

// TestKeyboardAcrossSystems: Ctrl and Cmd are swapped by default between a
// Mac and Windows only, in modifiers and keys sent by position; a Mac
// menu's edit commands come back as Cmd shortcuts.
func TestKeyboardAcrossSystems(t *testing.T) {
	for _, tc := range []struct {
		mine, peer string
		want       bool
	}{
		{"darwin", "windows", true}, {"windows", "macos", true},
		{"darwin", "macos", false}, {"windows", "windows", false}, {"darwin", "", false},
	} {
		if got := swapDefault(tc.mine, tc.peer); got != tc.want {
			t.Errorf("%s → %s: %v", tc.mine, tc.peer, got)
		}
	}
	key := swapCtrlMeta(inputEvent{Kind: "key", Key: "c", Down: true, Mods: &inputMods{Meta: true, Shift: true}})
	if !key.Mods.Ctrl || key.Mods.Meta || !key.Mods.Shift {
		t.Errorf("mods %+v", *key.Mods)
	}
	for from, to := range map[uint32]uint32{0x1D: 0xE05B, 0xE05B: 0x1D, 0xE01D: 0xE05C, 0xE05C: 0xE01D, 0x2E: 0x2E} {
		if got := swapCtrlMeta(inputEvent{Kind: "scan", Code: from}).Code; got != to {
			t.Errorf("scan %#x → %#x, want %#x", from, got, to)
		}
	}
	got := commandEvents("redo")
	if len(got) != 2 || got[0].Key != "z" || !got[0].Down || got[1].Down || !got[0].Mods.Meta || !got[0].Mods.Shift {
		t.Errorf("redo %+v", got)
	}
	if commandEvents("bold") != nil {
		t.Error("a command without a shortcut sends nothing")
	}
}

// TestViewerToolbarDrags: the toolbar's grip drags it along the picture's
// top edge, as far as the edges and no further; where it is let go is kept
// for every viewer, it opens there, folding keeps it there, and a press on
// it sends the host nothing.
func TestViewerToolbarDrags(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}}
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	open := func() (*viewer, *ui.Tester) {
		v := a.newViewer("482916735", localapi.About{}, nil)
		v.start, v.bm, v.pumping, v.control = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), true, true
		tt := ui.NewTester(v.view, 1100, 560)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.Frame()
		tt.Frame()
		return v, tt
	}
	const width = 1100
	// drag presses on the grip and moves the pointer by dx.
	drag := func(v *viewer, tt *ui.Tester, dx float32) {
		x := (width-v.barW)/2 + v.barOff.X + 14
		y := barTop + 16
		tt.Move(x, y)
		tt.Press(x, y)
		for i := 1; i <= 10; i++ {
			tt.Move(x+dx*float32(i)/10, y)
		}
		tt.Release(x+dx, y)
		tt.Frame()
	}
	v, tt := open()
	if v.barW == 0 || v.barOff.X != 0 {
		t.Fatalf("the toolbar opens %v wide at %v, want in the middle", v.barW, v.barOff)
	}
	free := width - v.barW - 2*barMargin
	drag(v, tt, -200)
	if at := a.toolbarAt(); math.Abs(float64(at+200/free)) > 0.01 {
		t.Errorf("dragged 200 to the left: kept at %v, want %v", at, -200/free)
	}
	if len(v.out) != 0 {
		t.Errorf("a press on the toolbar sent %d packets to the host", len(v.out))
	}
	drag(v, tt, 2000)
	if at := a.toolbarAt(); at != 0.5 || v.barOff.X != free/2 {
		t.Errorf("dragged past the right edge: kept at %v (offset %v), want 0.5 (%v)", at, v.barOff.X, free/2)
	}
	// Another viewer opens it where it was left.
	a.mu.Lock()
	delete(a.viewers, "482916735")
	a.mu.Unlock()
	other, ott := open()
	if other.barOff.X != free/2 {
		t.Errorf("another viewer opens it at %v, want %v", other.barOff.X, free/2)
	}
	if err := ott.Click(L.ViewerHideBar()); err != nil {
		t.Fatal(err)
	}
	ott.Frame()
	if !other.folded {
		t.Fatal("⌃ did not fold the toolbar")
	}
	ott.Frame()
	if want := (width - other.barW - 2*barMargin) / 2; other.barOff.X != want {
		t.Errorf("folded, the handle is at %v, want %v at the right", other.barOff.X, want)
	}
	if err := ott.Click(L.ViewerShowBar()); err != nil {
		t.Fatal(err)
	}
	ott.Frame()
	if other.folded {
		t.Error("⌄ did not open the toolbar")
	}
}

// TestViewerToolbarMenus: the toolbar's menus choose the quality and send
// the host's session actions.
func TestViewerToolbarMenus(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}}
	v := a.newViewer("482916735", localapi.About{}, nil)
	v.start, v.bm, v.pumping, v.control = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 320, 180))), true, true
	v.actions = &hostActions{}
	tt := ui.NewTester(v.view, 1100, 560)
	tt.Frame()
	tt.Frame()
	if err := tt.Click(L.ViewerQuality()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(L.QualityBest()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.preset != 2 {
		t.Errorf("chose Sharper: preset %d", v.preset)
	}
	for len(v.out) > 0 {
		<-v.out
	}
	if err := tt.Click(L.ViewerActions()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(L.ViewerLock()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(v.out) != 1 || !strings.Contains(string(<-v.out), `"lock"`) {
		t.Error("Lock screen sent no lock action")
	}
}

// TestViewerSettingsPerDevice: a device's viewer opens with the settings
// chosen for it last time; another device with the defaults.
func TestViewerSettingsPerDevice(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}}
	v := a.newViewer("482916735", localapi.About{}, nil)
	v.setQuality(2, "60 fps", true)
	v.setCodec("av1")
	v.setSwap(false)
	v.mu.Lock()
	v.folded = true
	v.mu.Unlock()
	v.keepSettings()
	again := a.newViewer("482916735", localapi.About{}, nil)
	if again.preset != 2 || again.fpsCap != "60 fps" || !again.full || again.codec != "av1" || again.swap == nil || *again.swap || !again.folded {
		t.Errorf("reopened %d %q %v %q %v %v", again.preset, again.fpsCap, again.full, again.codec, again.swap, again.folded)
	}
	if other := a.newViewer("123456789", localapi.About{}, nil); other.preset != 1 || other.codec != "auto" || other.swap != nil {
		t.Errorf("another device %d %q", other.preset, other.codec)
	}
	if p := loadPrefs(a.stateDir); p.Viewers["482916735"].Codec != "av1" {
		t.Errorf("gui.json %+v", p.Viewers)
	}
}

// TestLiveSound: the host's sound reaches the player as it arrives and
// ends when closed; what waits to be heard stays within a target that grows
// when playing runs dry while sound comes (up to 300 ms), shrinks after a
// calm spell, and drops the oldest sound beyond it.
// A full video queue or a decoder error leaves frames out until a key
// frame, which alone opens the gate again; the host is asked for one at
// once and again each second while it does not come (regression: asked
// once, a lost key frame stopped the picture for good).
func TestVideoGate(t *testing.T) {
	video := make(chan []byte, 1)
	var g videoGate
	key, delta := []byte{packetFrame, keyFrame}, []byte{packetFrame, 0}
	t0 := time.Unix(1000, 0)
	for i, c := range []struct {
		p         []byte
		at        time.Duration
		drain     bool
		sent, ask bool
	}{
		{delta, 0, false, true, false},                       // room: queued
		{delta, 0, false, false, true},                       // full: left out, a key frame asked for
		{delta, 100 * time.Millisecond, false, false, false}, // left out
		{key, 200 * time.Millisecond, false, false, false},   // the key frame finds it full: too soon to ask
		{key, 1200 * time.Millisecond, false, false, true},   // full again: asked again
		{delta, 1300 * time.Millisecond, true, false, false}, // room again, but it needs its reference
		{key, 1400 * time.Millisecond, false, true, false},   // a key frame starts over
		{delta, 1500 * time.Millisecond, true, true, false},
	} {
		if c.drain {
			<-video
		}
		sent, ask := g.pass(c.p, video, t0.Add(c.at))
		if sent != c.sent || ask != c.ask {
			t.Fatalf("step %d: sent %v ask %v", i, sent, ask)
		}
	}
	<-video
	// A frame the decoder failed on closes the gate; the watchdog asks
	// again each second until a key frame comes.
	if !g.broke(t0.Add(3 * time.Second)) {
		t.Fatal("a decoder error asks for a key frame")
	}
	if sent, _ := g.pass(delta, video, t0.Add(3100*time.Millisecond)); sent {
		t.Fatal("a delta frame after a decoder error")
	}
	if g.again(false, t0.Add(3500*time.Millisecond)) || !g.again(false, t0.Add(4*time.Second)) {
		t.Fatal("the watchdog asks once a second while the gate is closed")
	}
	if sent, _ := g.pass(key, video, t0.Add(4100*time.Millisecond)); !sent || g.again(false, t0.Add(9*time.Second)) {
		t.Fatal("a key frame opens the gate, and nothing more is asked")
	}
}

// TestQueueKeepsReleases: a full input queue drops presses, never a
// release (regression: a dropped release left a key or a button held on
// the host); the releases kept aside go once the queue drained.
func TestQueueKeepsReleases(t *testing.T) {
	v := &viewer{out: make(chan []byte, 1), moved: make(chan struct{}, 1)}
	press := inputEvent{Kind: "scan", Code: 0x1E, Down: true}
	steps := []struct {
		e    inputEvent
		kept bool
	}{
		{press, false}, // queued: the queue is full now
		{inputEvent{Kind: "pointer", Action: "down"}, false},
		{inputEvent{Kind: "text", Text: "x"}, false},
		{inputEvent{Kind: "scan", Code: 0x1E}, true},
		{inputEvent{Kind: "pointer", Action: "up"}, true},
		{inputEvent{Kind: "key", Key: "a"}, true},
	}
	var want []inputEvent
	for _, s := range steps {
		v.queue(s.e)
		if s.kept {
			want = append(want, s.e)
		}
	}
	if len(v.out) != 1 {
		t.Fatalf("queued %d", len(v.out))
	}
	if got := v.takeReleases(); !reflect.DeepEqual(got, want) {
		t.Errorf("kept %+v, want %+v", got, want)
	}
	// The writer sends them after what was queued.
	var buf bytes.Buffer
	done := make(chan struct{})
	v.queue(inputEvent{Kind: "scan", Code: 0x2A}) // the queue is still full: kept
	finished := make(chan struct{})
	go func() { v.writer(&buf, done); close(finished) }()
	time.Sleep(100 * time.Millisecond)
	close(done)
	<-finished
	var kinds []string
	for {
		p, err := localapi.ReadPacket(&buf, nil)
		if err != nil {
			break
		}
		var m struct {
			Event inputEvent `json:"event"`
		}
		_ = json.Unmarshal(p[1:], &m)
		kinds = append(kinds, fmt.Sprintf("%s %x %v", m.Event.Kind, m.Event.Code, m.Event.Down))
	}
	if want := []string{"scan 1e true", "scan 2a false"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("written %q, want %q", kinds, want)
	}
}

// TestGrabFor: a key the grab took goes to the tab of its window holding
// the grab; once the grab stepped aside, its releases still go to the tab
// whose grab ended last, presses to none.
func TestGrabFor(t *testing.T) {
	now := time.Now()
	a := &viewer{window: 7, control: true, ungrabbed: now.Add(-time.Minute)}
	b := &viewer{window: 7, control: true, ungrabbed: now}
	other := &viewer{window: 9, control: true, grabbed: "map"}
	viewers := []*viewer{a, b, other}
	if v := grabFor(viewers, core.KeyEvent{Window: 7, Down: 0}); v != b {
		t.Error("a release after the grab ended goes to the tab that held it last")
	}
	if v := grabFor(viewers, core.KeyEvent{Window: 7, Down: 1}); v != nil {
		t.Error("a press without a grab")
	}
	a.grabbed = "translate"
	if v := grabFor(viewers, core.KeyEvent{Window: 7, Down: 1}); v != a {
		t.Error("the tab holding the grab")
	}
}

// TestInputStalled: the keyboard grab steps aside while the session
// reconnects, input was refused for more than inputPauseGrace, or a host
// that sends video has sent nothing for stalledFeed; it grabs again once
// input flows.
func TestInputStalled(t *testing.T) {
	now := time.Unix(1000, 0)
	for i, c := range []struct {
		state            string
		paused           bool
		pausedFor, quiet time.Duration
		video            bool
		want             bool
	}{
		{"connected", false, 0, 0, true, false},
		{"reconnecting", false, 0, 0, true, true},
		{"connected", true, 300 * time.Millisecond, 0, true, false},
		{"connected", true, 600 * time.Millisecond, 0, true, true},
		{"connected", false, 0, 4 * time.Second, true, true},
		{"connected", false, 0, 4 * time.Second, false, false}, // no video: nothing to wait for
	} {
		v := &viewer{phase: localapi.SessionInfo{State: c.state}, paused: c.paused,
			pausedAt: now.Add(-c.pausedFor), heardAt: now.Add(-c.quiet)}
		if c.video {
			v.start.Width = 1920
		}
		if got := v.inputStalled(now); got != c.want {
			t.Errorf("case %d: stalled %v", i, got)
		}
	}
}

// TestViewerNeedsKey: a picture that stopped while the host says it sends
// frames needs a key frame; a still screen (no frames) and a 0.4.0 host
// (which does not say) do not.
func TestViewerNeedsKey(t *testing.T) {
	for _, c := range []struct {
		since    time.Duration
		frames   int
		pictured bool
		want     bool
	}{
		{500 * time.Millisecond, 30, true, false},
		{2 * time.Second, 30, true, true},
		// A still screen after a picture sends nothing: nothing to ask.
		{10 * time.Second, 0, true, false},
		{3 * time.Second, 1, true, true},
		// No picture yet: asked whatever the host says (regression: a host
		// whose encoder gave nothing was never asked again).
		{3 * time.Second, 0, false, true},
		{500 * time.Millisecond, 0, false, false},
	} {
		if got := viewerNeedsKey(c.since, c.frames, c.pictured); got != c.want {
			t.Errorf("%v %d %v: %v", c.since, c.frames, c.pictured, got)
		}
	}
}

// TestViewerAsleep: the viewer offers to wake the host's screen when the
// host says its display is off or asleep, and only then (regression: the
// offer showed while the host's display was on and its video was stuck); a
// host that does not say (0.5.2 and older) gets the old guess after
// wakeAfter.
func TestViewerAsleep(t *testing.T) {
	for _, c := range []struct {
		since       time.Duration
		video, code string
		want        bool
	}{
		{10 * time.Second, "live", "", false},
		{10 * time.Second, "recovering", "", false},
		{0, "dark", "", true},
		{0, "placeholder", "asleep", true},
		{10 * time.Second, "placeholder", "agent", false},
		{2 * time.Second, "", "", false},
		{4 * time.Second, "", "", true},
	} {
		if got := viewerAsleep(c.since, c.video, c.code); got != c.want {
			t.Errorf("%v %q %q: %v", c.since, c.video, c.code, got)
		}
	}
}

func TestLiveSound(t *testing.T) {
	var played int64
	clock := time.Unix(0, 0)
	s := newLiveSound(func() int64 { return played }, func() time.Time { return clock })
	packet := make([]float32, soundRate/100*soundChannels) // 10 ms
	buf := make([]float32, 1<<16)
	tick := func() { clock = clock.Add(10 * time.Millisecond) }
	ahead := func() int64 { return s.handed + int64(len(s.buf)/soundChannels) - played }
	read := func() int {
		t.Helper()
		got := make(chan int)
		go func() { n, _ := s.Read(buf); got <- n }()
		select {
		case n := <-got:
			return n
		case <-time.After(time.Second):
			t.Fatal("Read waits")
			return 0
		}
	}
	// Nothing plays before the target is gathered: playing as each packet
	// came would run dry between them.
	for range soundStart/(soundRate/100) - 1 {
		s.feed(packet)
		tick()
	}
	if !s.filling {
		t.Fatal("played before the target was gathered")
	}
	s.feed(packet)
	if n := read(); n != soundStart*soundChannels {
		t.Fatalf("first read %d samples, want the target", n)
	}
	// Played steadily, sound passes on as it comes.
	played += soundRate / 100
	tick()
	s.feed(packet)
	if n := read(); n != len(packet) {
		t.Fatalf("steady read %d", n)
	}
	// A burst beyond the target and its slack is cut back to the target.
	for range soundSlack / (soundRate / 100) {
		s.feed(packet)
	}
	if a := ahead(); a != s.target+soundSlack {
		t.Fatalf("%d frames ahead within the slack", a)
	}
	s.feed(packet)
	if a := ahead(); a != s.target {
		t.Fatalf("%d frames ahead after a burst, want %d", a, s.target)
	}
	// Playing ran dry while sound came (jitter): it gathers again, more.
	played = s.handed
	tick()
	s.feed(packet)
	if !s.filling || s.target != soundStart+soundGrow {
		t.Fatalf("after running dry: filling %v, target %d", s.filling, s.target)
	}
	for range (soundStart + soundGrow) / (soundRate / 100) {
		s.feed(packet)
	}
	read()
	// After the host's silence (a long pause) it gathers the same target.
	played = s.handed
	clock = clock.Add(time.Second)
	s.feed(packet)
	if !s.filling || s.target != soundStart+soundGrow {
		t.Fatalf("after silence: filling %v, target %d", s.filling, s.target)
	}
	for range s.target / (soundRate / 100) {
		s.feed(packet)
	}
	read()
	// A calm spell lowers it again.
	clock = clock.Add(soundCalm + time.Second)
	s.feed(packet)
	if s.target != soundStart+soundGrow-soundShrink {
		t.Fatalf("target %d after calm", s.target)
	}
	done := make(chan error)
	s.mu.Lock()
	s.buf = s.buf[:0]
	s.mu.Unlock()
	go func() { _, err := s.Read(buf); done <- err }()
	s.close()
	if err := <-done; err != io.EOF {
		t.Fatalf("closed: %v", err)
	}
}

// TestRemoveNetworkReturns: removing a network after the confirmation
// (off the main thread) finishes and frees the page; clearing the network
// settings once took the app lock twice and froze the window.
func TestRemoveNetworkReturns(t *testing.T) {
	a := &App{busy: map[string]bool{}, ctx: context.Background(), socket: filepath.Join(t.TempDir(), "none.sock")}
	done := make(chan struct{})
	go func() {
		a.removeNetwork("a1", func() bool { return true })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("clearing the network settings did not return")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		a.mu.Lock()
		busy := a.busy["network"]
		a.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the network page stayed busy")
		}
	}
}

// TestConnectTarget reads Connect's field as a Device ID or an address.
func TestConnectTarget(t *testing.T) {
	for _, c := range []struct{ in, device, address string }{
		{"482 916 735", "482916735", ""},
		{"482916735", "482916735", ""},
		{"48291673", "", ""},
		{"", "", ""},
		{" 192.168.1.20 ", "", "192.168.1.20"},
		{"office.example.org:47312", "", "office.example.org:47312"},
		{"fe80::1", "", "fe80::1"},
		{"100.101.102.103", "", "100.101.102.103"},
	} {
		device, address := parseTarget(c.in)
		if device != c.device || address != c.address {
			t.Errorf("parseTarget(%q) = %q, %q; want %q, %q", c.in, device, address, c.device, c.address)
		}
	}
}

// TestDeviceRowsUseLocalNames: a name given here replaces the one the
// device gives itself, and a recent device keeps its routes and the one
// chosen.
func TestDeviceRowsUseLocalNames(t *testing.T) {
	st := &localapi.Status{
		Recent: []localapi.RecentDevice{{DeviceID: "657365333", Routes: []localapi.Route{{Address: "192.168.1.20"}}, Prefer: "192.168.1.20", About: localapi.About{Name: "Studio Mac"}}},
		Sessions: []localapi.SessionInfo{{ID: 3, Role: "controller", DeviceID: "424829924", State: "failed"},
			{ID: 4, Role: "host", DeviceID: "100000002", State: "connected"}},
	}
	rows := deviceRows(st, Prefs{Aliases: map[string]string{"657365333": "Studio", "424829924": "Attic"}})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (hosts only)", len(rows))
	}
	if rows[0].about.Name != "Studio" || len(rows[0].routes) != 1 || rows[0].prefer != "192.168.1.20" {
		t.Errorf("recent row = %q at %v (%q)", rows[0].about.Name, rows[0].routes, rows[0].prefer)
	}
	if rows[1].about.Name != "Attic" {
		t.Errorf("session row = %q, want the local name", rows[1].about.Name)
	}
	if got := deviceRows(st, Prefs{})[0].about.Name; got != "Studio Mac" {
		t.Errorf("without a local name = %q", got)
	}
}

// TestTunnelsAreNotDesktopConnections: a tunnel to a device's service is
// not its desktop connection — no tab follows it and Home does not count it
// (regression: a refused RDP/VNC/SSH tunnel opened a connection tab asking
// for the Kyokai password).
func TestTunnelsAreNotDesktopConnections(t *testing.T) {
	tunnel := localapi.SessionInfo{ID: 5, Role: "controller", DeviceID: "657365333", Service: "vnc", State: "connected"}
	refused := localapi.SessionInfo{ID: 6, Role: "controller", DeviceID: "657365333", Service: "rdp", State: "failed",
		Failure: json.RawMessage(`{"refused":"password"}`)}
	st := &localapi.Status{
		Recent:   []localapi.RecentDevice{{DeviceID: "657365333"}},
		Sessions: []localapi.SessionInfo{tunnel, refused},
	}
	if s := deviceRows(st, Prefs{})[0].session; s != nil {
		t.Errorf("Home counts tunnel %d as the desktop connection", s.ID)
	}
	a := New("", t.TempDir(), "")
	a.status = st
	// Opening a tab waits for a window this test has none of.
	done := make(chan struct{})
	go func() { a.syncViewers(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a tunnel opened a connection tab")
	}
	if len(a.viewers) != 0 {
		t.Errorf("tunnels opened %d connection tab(s)", len(a.viewers))
	}
	if !desktopController(localapi.SessionInfo{Role: "controller"}) || desktopController(tunnel) ||
		desktopController(localapi.SessionInfo{Role: "host"}) {
		t.Error("desktopController")
	}
}

// TestAClosedSessionGetsNoTab: a session this GUI closed still reads
// connected while it says goodbye; no tab opens for it, and it is forgotten
// once it ended (regression: closing a tab or a window opened a new tab for
// the same device at once, which said the session ended).
func TestAClosedSessionGetsNoTab(t *testing.T) {
	a := New("", t.TempDir(), "")
	closing := localapi.SessionInfo{ID: 7, Role: "controller", DeviceID: "657365333", State: "connected"}
	a.status = &localapi.Status{Sessions: []localapi.SessionInfo{closing}}
	a.dropSession(closing.ID)
	// Opening a tab waits for a window this test has none of.
	done := make(chan struct{})
	go func() { a.syncViewers(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a session this GUI closed opened a tab")
	}
	if len(a.viewers) != 0 {
		t.Errorf("%d tab(s) for a closed session", len(a.viewers))
	}
	closing.State = "closed"
	a.status = &localapi.Status{Sessions: []localapi.SessionInfo{closing}}
	a.syncViewers()
	if a.closed[closing.ID] {
		t.Error("an ended session stays noted")
	}
}

// TestServicePorts: the Permissions page shows a standard port as an
// empty field and sends one back as 0 (the engine's standard), and
// refuses what is not a port.
func TestServicePorts(t *testing.T) {
	if got := portFields(localapi.ServicePorts{SSH: 22, RDP: 13389}); got != [3]string{"", "13389", ""} {
		t.Errorf("portFields = %q", got)
	}
	for _, c := range []struct {
		in   [3]string
		want localapi.ServicePorts
		ok   bool
	}{
		{[3]string{"", "", ""}, localapi.ServicePorts{}, true},
		{[3]string{" 2222 ", "", "5901"}, localapi.ServicePorts{SSH: 2222, VNC: 5901}, true},
		{[3]string{"0", "", ""}, localapi.ServicePorts{}, false},
		{[3]string{"", "65536", ""}, localapi.ServicePorts{}, false},
		{[3]string{"", "", "vnc"}, localapi.ServicePorts{}, false},
	} {
		got, ok := parsePorts(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parsePorts(%q) = %+v, %v", c.in, got, ok)
		}
	}
}

// Regression: as the shown tab changed, the tab just hidden ended the grab
// the newly shown one had taken; only the tab holding a window's grab ends
// it.
func TestGrabTurnLetsOnlyTheHolderEndAGrab(t *testing.T) {
	var holders map[uintptr]any
	a, b := "tab a", "tab b"
	steps := []struct {
		tab    any
		window uintptr
		mode   string
		call   bool
	}{
		{a, 1, "map", true},
		{b, 1, "map", true},  // b is shown: it takes the window's grab
		{a, 1, "off", false}, // a, hidden, no longer holds it
		{b, 1, "translate", true},
		{a, 2, "map", true}, // another window's grab is its own
		{b, 1, "off", true},
		{b, 1, "off", false},
		{a, 2, "off", true},
	}
	for i, s := range steps {
		if got := grabTurn(&holders, s.window, s.tab, s.mode); got != s.call {
			t.Fatalf("step %d: %v asks %q of window %d: call %v, want %v", i, s.tab, s.mode, s.window, got, s.call)
		}
	}
}
