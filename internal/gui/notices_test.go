package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestNoticesRender renders in the shell every state that needs the user
// above the panes or in place of a page (doc/en-us/gui.md): a connection
// request with its warnings and one from a device allowed before, a
// device connected here, this computer's AI task waiting and a
// controller's at work, all of them at once, Starting…, the engine down,
// a profile moved to the helper, protected mode with a request, Home's
// privacy permissions, its rename and Addresses editors, and a desktop's
// pane waiting for its device's user — four languages and both themes;
// it fails on a panic. With SCREENSHOT_DIR set it writes PNGs for review.
func TestNoticesRender(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	request := localapi.HostRequest{ID: 3, DeviceID: "213559601", NewKey: true,
		Want:  []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard, localapi.PermFiles},
		About: localapi.About{Name: "Office laptop", Maker: "LENOVO", Model: "ThinkPad X1 Carbon", OS: "windows"}}
	here := localapi.SessionInfo{ID: 4, Role: "host", DeviceID: "100000002", State: "connected",
		Granted: []string{localapi.PermViewScreen, localapi.PermControlInput}, Peer: localapi.About{Name: "Living room PC", Maker: "Dell Inc.", OS: "windows"}}
	studio := localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"}
	shots := map[string]func(a *App){
		"request": func(a *App) {
			a.status.Permissions = &localapi.Permissions{Accessibility: true, InputMonitoring: true, Keychain: true}
			a.status.Requests = []localapi.HostRequest{request}
		},
		"request-known": func(a *App) {
			a.status.Requests = []localapi.HostRequest{{ID: 5, DeviceID: "657365333", Known: true, Password: true,
				Want: []string{localapi.PermViewScreen, localapi.PermControlInput}, About: studio}}
		},
		"connected-here": func(a *App) { a.status.Sessions = []localapi.SessionInfo{here} },
		"ai-waiting":     func(a *App) { a.status.AI = &localapi.AIActive{ID: 3, State: "approval"} },
		"ai-remote":      func(a *App) { a.status.AI = &localapi.AIActive{ID: 9, State: "running", Origin: "657365333"} },
		"all": func(a *App) {
			a.status.Requests = []localapi.HostRequest{request}
			a.status.Sessions = []localapi.SessionInfo{here}
			a.status.AI = &localapi.AIActive{ID: 9, State: "running", Origin: "657365333"}
		},
		"starting":    func(a *App) { a.connected, a.status = false, nil },
		"engine-down": func(a *App) { a.connected, a.status, a.engineErr = false, nil, "kyokai-svc not found" },
		"moved":       func(a *App) { a.status.Moved = "helper" },
		"protected": func(a *App) {
			a.status.Protected, a.status.PermanentPassword = true, true
			a.status.Requests = []localapi.HostRequest{request}
			a.ai = aiState{loaded: true, settings: demoAISettings(true)}
		},
		"privacy": func(a *App) {
			a.status.Permissions = &localapi.Permissions{Accessibility: true, Keychain: true}
		},
		"rename": func(a *App) {
			a.prefs.Aliases = map[string]string{"657365333": "Studio"}
			a.rename = renameForm{device: "657365333", name: "Studio"}
			a.status.Recent = []localapi.RecentDevice{{DeviceID: "657365333", Last: 1797400000, About: studio}}
		},
		"routes": func(a *App) {
			a.routesEdit = routesForm{device: "657365333"}
			a.status.Recent = []localapi.RecentDevice{{DeviceID: "657365333", Last: 1797400000, About: studio,
				Routes: []localapi.Route{{Address: "192.168.1.20"}, {Address: "studio.local"}}}}
		},
		"connect-panel": func(a *App) {
			v := &viewer{app: a, device: "657365333", title: "Studio Mac · 657 365 333",
				phase: localapi.SessionInfo{ID: 1, DeviceID: "657365333", State: "confirming", Peer: studio}}
			a.viewers[v.device] = v
			a.main.tabs = append(a.main.tabs, v)
			a.main.placeLocked(v, nil, zoneMiddle)
		},
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, dark := range []bool{false, true} {
			if dark && lang != "en" {
				continue
			}
			for name, setup := range shots {
				a := demoApp(t)
				setup(a)
				tt := ui.NewTester(a.main.view, 1280, 860)
				tt.SetDark(dark)
				tt.Frame()
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

// TestAskColumns: a request's tiles stand side by side as wide as the
// card while each keeps its least width, else in the fewest rows that
// fit, as even as they go; before the first frame at most four a row.
func TestAskColumns(t *testing.T) {
	for _, c := range []struct {
		w    float32
		n    int
		want int
	}{
		{0, 2, 2}, {0, 6, 3},
		{1000, 2, 2}, {1000, 4, 4}, {1000, 7, 4},
		{710, 5, 3}, {710, 4, 4}, {710, 8, 4},
		{300, 4, 2}, {300, 3, 2}, {100, 3, 1}, {1000, 0, 1},
	} {
		if got := askColumns(c.w, c.n); got != c.want {
			t.Errorf("askColumns(%v, %d) = %d, want %d", c.w, c.n, got, c.want)
		}
	}
}

// TestNoticesByKeyboard: Tab reaches every control on the cards above the
// panes — a request's Deny, Allow and tiles, Disconnect, the AI card's
// Review and Stop — and Space switches a tile, which Allow then leaves
// out.
func TestNoticesByKeyboard(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	want := []string{localapi.PermViewScreen, localapi.PermClipboard}
	a.status.Requests = []localapi.HostRequest{{ID: 3, DeviceID: "213559601", Want: want}}
	a.status.Sessions = []localapi.SessionInfo{{ID: 4, Role: "host", DeviceID: "100000002", State: "connected", Granted: []string{localapi.PermViewScreen}}}
	a.status.AI = &localapi.AIActive{ID: 9, State: "running", Origin: "657365333"}
	tt := ui.NewTester(a.main.view, 1280, 860)
	tt.Frame()
	tt.Frame()
	reached := map[string]bool{}
	controls := []string{L.ActionDeny(), L.ActionAllow(), permissionShort(localapi.PermViewScreen), permissionShort(localapi.PermClipboard),
		L.ActionDisconnect(), L.AIReview(), L.AIActionStop()}
	for range 80 {
		tt.Key(0, ui.KeyTab)
		tt.Frame()
		for _, s := range controls {
			if tt.Focused(s) {
				reached[s] = true
			}
		}
	}
	for _, s := range controls {
		if !reached[s] {
			t.Errorf("Tab never reaches %q", s)
		}
	}
	for range 80 {
		if tt.Focused(permissionShort(localapi.PermClipboard)) {
			break
		}
		tt.Key(0, ui.KeyTab)
		tt.Frame()
	}
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if got := a.grants[3]; !slices.Equal(got, want[:1]) {
		t.Fatalf("Space on the clipboard's tile: grants %v, want %v", got, want[:1])
	}
}

// TestDeviceName: a device is called by the name given here, else its own
// as the recent devices know it, else as a connection now knows it — a
// controller's AI at work here names its device though it never was a
// recent one (regression: "An AI task from 725 980 054") — else by its
// Device ID.
func TestDeviceName(t *testing.T) {
	a := demoApp(t)
	a.status.Recent = []localapi.RecentDevice{{DeviceID: "657365333", About: localapi.About{Name: "Studio Mac"}}}
	a.status.Sessions = []localapi.SessionInfo{{ID: 4, Role: "host", DeviceID: "725980054", State: "connected", Peer: localapi.About{Name: "WINDOWS-DEV-KIT"}}}
	a.prefs.Aliases = map[string]string{"402118905": "NUC"}
	for _, c := range []struct{ id, want string }{
		{"657365333", "Studio Mac"},
		{"725980054", "WINDOWS-DEV-KIT"},
		{"402118905", "NUC"},
		{"213559601", "213 559 601"},
	} {
		if got := a.deviceName(c.id); got != c.want {
			t.Errorf("deviceName(%s) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestRequestGrantsLess: a connection request's permissions are tiles
// that start on, as asked; a click on one leaves it out of what Allow
// grants.
func TestRequestGrantsLess(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	want := []string{localapi.PermViewScreen, localapi.PermControlInput, localapi.PermClipboard}
	a.status.Requests = []localapi.HostRequest{{ID: 3, DeviceID: "213559601", Want: want}}
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	tt.Frame()
	if err := tt.Click(permissionShort(localapi.PermClipboard)); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if got := a.grants[3]; !slices.Equal(got, want[:2]) {
		t.Fatalf("grants %v, want %v", got, want[:2])
	}
}
