package gui

import (
	"image"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
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
		"settings":      func(a *App) { a.page, a.status.About = pageSettings, localapi.About{Name: "WINDOWS-KVM"} },
		"licenses":      func(a *App) { a.page, a.showLicenses = pageSettings, true },
		"network-off":   func(a *App) { a.page, a.net = pageNetwork, netForm{loaded: true} },
		"home-sessions": func(a *App) {
			a.status.Network.State = "online"
			a.status.Requests = []localapi.HostRequest{{ID: 3, DeviceID: "213559601", NewKey: true, Want: []string{localapi.PermViewScreen, localapi.PermControlInput},
				About: localapi.About{Name: "Office laptop", Maker: "LENOVO", Model: "ThinkPad X1 Carbon", OS: "windows"}}}
			a.status.Recent = []localapi.RecentDevice{
				{DeviceID: "657365333", Last: 1797400000, About: localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}},
				{DeviceID: "647222040", Last: 1797300000},
			}
			a.status.Sessions = []localapi.SessionInfo{
				{ID: 1, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`{"refused":"password"}`)},
				{ID: 2, Role: "controller", DeviceID: "647222040", State: "failed", Failure: []byte(`"key-changed"`)},
				{ID: 4, Role: "host", DeviceID: "100000002", State: "connected", Granted: []string{localapi.PermViewScreen},
					Peer: localapi.About{Name: "Living room PC", Maker: "Dell Inc.", OS: "windows"}},
			}
		},
		"settings-access": func(a *App) {
			a.page, a.status.PermanentPassword = pageSettings, true
			until := int64(1799900000)
			a.access = accessForm{loaded: true, trusted: []localapi.TrustedDevice{
				{Key: "k", DeviceID: "213559601", Last: 1797400000, Granted: []string{localapi.PermViewScreen}, About: localapi.About{Name: "Office laptop", Maker: "LENOVO"}, UnattendedUntil: &until},
				{Key: "k2", DeviceID: "647222040", Last: 1797300000, Granted: []string{localapi.PermViewScreen}},
			}}
		},
		"home-remote-ai": func(a *App) {
			a.status.AI = &localapi.AIActive{ID: 4, State: "running", Origin: "213559601"}
		},
		"ai-new": func(a *App) {
			a.page = pageAI
			s := &localapi.AISettings{Provider: "claude", ClaudeModel: "claude-opus-5-5", OpenAIModel: "gpt-6.1-sol", Approval: "flagged", Keys: &localapi.AIKeys{}}
			a.ai = aiState{loaded: true, settings: s, form: aiFormFrom(s)}
		},
		"ai-approval": func(a *App) {
			a.page = pageAI
			a.status.AI = &localapi.AIActive{ID: 2, State: "approval"}
			s := &localapi.AISettings{Provider: "claude", ClaudeModel: "claude-opus-5-5", OpenAIModel: "gpt-6.1-sol", Approval: "flagged", Keys: &localapi.AIKeys{Claude: true}}
			a.ai = aiState{loaded: true, settings: s, form: aiFormFrom(s), tasks: []localapi.AITask{
				{ID: 2, Instruction: "Save the open document", Provider: "claude", Model: "claude-opus-5-5", State: "approval",
					Text: "I will press Save.", Progress: []string{"Pressing Save."},
					Steps:    []localapi.AIStep{{Action: []byte(`{"kind":"screenshot"}`), Outcome: "ok"}},
					Approval: &localapi.AIApproval{Action: []byte(`{"kind":"key-combo","keys":["meta","r"],"repeat":1}`)}},
				{ID: 1, Instruction: "Open the settings", Provider: "claude", Model: "claude-opus-5-5", State: "done", Created: 1797400000},
			}}
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
				height := 900
				if name == "settings-access" {
					height = 1500 // the trusted devices sit below the fold
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
			v.pointer = hostPointer{X: 0.25, Y: 0.5, Visible: true}
		},
		"viewer-unsupported": func(v *viewer) { v.state = "This picture uses h265, which this version can't show yet." },
		"viewer-ai": func(v *viewer) {
			v.start, v.bm, v.fps, v.kbps = videoStart{Codec: "h264", Width: 320, Height: 180}, ui.NewBitmap(pic), 30, 1390
			v.ai = viewerAI{run: true, approve: true, task: &localapi.AITask{ID: 3, Instruction: "Rename the report to Q3.docx", State: "approval",
				Text: "I will open the file menu.", Progress: []string{"Opening the File menu."},
				Steps:    []localapi.AIStep{{Action: []byte(`{"kind":"screenshot"}`), Outcome: "ok"}, {Action: []byte(`{"kind":"click","at":{"x":24,"y":12},"button":"left","count":1,"modifiers":[]}`), Outcome: "ok"}},
				Approval: &localapi.AIApproval{Action: []byte(`{"kind":"type","text":"Q3.docx"}`)}}}
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
		"viewer-ai-new": func(v *viewer) { v.state, v.ai = "This session does not show the screen.", viewerAI{run: true} },
	}
	for name, setup := range cases {
		v := &viewer{device: "657365333"}
		setup(v)
		tt := ui.NewTester(v.view, 1100, 560)
		tt.Frame()
		tt.Frame() // the pointer marker uses the first frame's layout
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
