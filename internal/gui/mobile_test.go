package gui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

func TestMobileViewsResponsiveAndLocales(t *testing.T) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"phone", 390, 844},
		{"foldable", 904, 1000},
		{"tablet", 1280, 800},
	}
	locales := []string{"en", "zh-Hans", "zh-Hant", "ja"}
	defer i18n.SetLocaleFunc(nil)

	for _, loc := range locales {
		loc := loc
		i18n.SetLocaleFunc(func() string { return loc })
		for _, sz := range sizes {
			a := demoApp(t)
			a.initMobile()
			a.net = netState{loaded: true}
			a.keys = keysState{loaded: true}
			a.status.Network.State = "online"
			a.status.Recent = []localapi.RecentDevice{
				{
					DeviceID: "657365333",
					Last:     1797400000,
					Features: []string{"wake", "tunnel", "nanokvm", "ai-sessions"},
					Saved:    localapi.Saved{Password: true, SSH: true},
					About:    localapi.About{Name: "Studio Mac", Maker: "Apple", Model: "Mac14,13", OS: "macos"},
				},
			}
			a.status.Network.Devices = []localapi.NetworkDevice{
				{DeviceID: "657365333", Path: "direct", LatencyMS: 8},
			}
			a.prefs.MobileRecentTargets = []string{"657 365 333", "192.168.1.20:47310"}

			tt := ui.NewTester(a.MobileView, sz.w, sz.h)

			// 1. Devices list & Device detail.
			tt.Frame()
			a.mobile.selectedDev = "657365333"
			tt.Frame()

			// 2. Sessions tab + Clipboard history sheet.
			a.mobile.navTab = 1
			a.mobile.clipSheetOpen = true
			tt.Frame()
			a.mobile.clipSheetOpen = false

			// 3. Settings menu & sub-pages.
			a.mobile.navTab = 2
			a.mobile.settingsOpen = false
			tt.Frame()
			a.mobile.settingsOpen = true
			for _, pg := range []mobileSettingsPage{
				mobSetDevice, mobSetNetworks, mobSetKeys,
				mobSetInput, mobSetAppearance, mobSetDiagnostics, mobSetAbout,
			} {
				a.mobile.settingsPage = pg
				tt.Frame()
			}

			// 4. Connected full-screen desktop viewer + keyboard + menu sheet.
			v := a.newViewer("657365333", localapi.About{Name: "Studio Mac", OS: "macos"}, desktopWant, false, 0)
			v.mu.Lock()
			v.phase = localapi.SessionInfo{State: "connected", DeviceID: "657365333", Peer: localapi.About{Name: "Studio Mac", OS: "macos"}}
			v.start = videoStart{Codec: "hevc", Width: 1920, Height: 1080}
			v.bm = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 64, 36)))
			v.control = true
			v.mu.Unlock()
			a.mobile.activeSession = v.tabKey()
			tt.Frame()

			if u := a.mobile.views[v.tabKey()]; u != nil {
				u.kbOpen = true
				tt.Frame()
				a.prefs.SplitKeyboard = true
				tt.Frame()
				a.mobile.menuSheetOpen = true
				tt.Frame()
				a.mobile.menuSheetOpen = false
			}
		}
	}
}

func TestMiniTermANSI(t *testing.T) {
	var title string
	m := newMiniTerm(40, 6, func(s string) { title = s }, nil)
	m.Feed([]byte("\x1b]0;user@host:~/repo\x07hello\r\n\x1b[32mworld\x1b[0m"))
	if title != "user@host:~/repo" {
		t.Fatalf("title = %q, want user@host:~/repo", title)
	}
	lines, _, _ := m.Lines()
	if len(lines) < 2 || lines[0] != "hello" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestMobileToolbarTouchAndIMEInsets(t *testing.T) {
	a := demoApp(t)
	a.initMobile()
	a.net = netState{loaded: true}
	a.keys = keysState{loaded: true}

	tt := ui.NewTester(a.MobileView, 390, 844)
	tt.SetInsets(24, 0, 20, 0)
	tt.Frame()

	// TopBar should sit right below the 24dp status-bar inset (not double-padded to 48dp).
	devsRect, ok := tt.Find("Devices")
	if !ok {
		t.Fatal("Devices header not found")
	}
	if devsRect.Y > 48 {
		t.Fatalf("TopBar double-padded: Devices Y = %.1f, want <= 48", devsRect.Y)
	}
	if _, ok := tt.Find("Settings"); !ok {
		t.Fatal("bottom navigation bar missing when IME is closed")
	}

	// Opening the soft keyboard (bottom inset > 120dp) hides the bottom bar and avoids double padding.
	tt.SetInsets(24, 0, 320, 0)
	tt.Frame()
	if _, ok := tt.Find("Settings"); ok {
		t.Fatal("bottom navigation bar stayed visible over open IME keyboard")
	}

	// Connected viewer: touching the floating Keyboard button must click it rather than stage swallowing it.
	tt.SetInsets(24, 0, 20, 0)
	v := a.newViewer("657365333", localapi.About{Name: "Studio Mac", OS: "macos"}, desktopWant, false, 0)
	v.mu.Lock()
	v.phase = localapi.SessionInfo{State: "connected", DeviceID: "657365333", Peer: localapi.About{Name: "Studio Mac", OS: "macos"}}
	v.start = videoStart{Codec: "hevc", Width: 1920, Height: 1080}
	v.bm = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 64, 36)))
	v.control = true
	v.mu.Unlock()
	a.mobile.activeSession = v.tabKey()
	tt.Frame()
	tt.Frame() // second frame after LayoutBounds settles toolbar coordinates

	kbRect, ok := tt.Find("Keyboard")
	if !ok {
		t.Fatal("floating toolbar Keyboard button not found")
	}
	cx, cy := kbRect.X+kbRect.W/2, kbRect.Y+kbRect.H/2
	tt.FingerDown(cx, cy)
	tt.FingerUp(cx, cy)
	tt.Frame()

	u := a.mobile.views[v.tabKey()]
	if u == nil || !u.kbOpen {
		t.Fatal("tapping floating toolbar Keyboard button was swallowed and did not open keyboard")
	}
}

func TestMobileCompactSettingsDrilldownAndSessionOrder(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	a := demoApp(t)
	a.initMobile()
	a.net = netState{loaded: true}
	a.keys = keysState{loaded: true}
	a.status.Recent = []localapi.RecentDevice{
		{
			DeviceID: "111222333",
			Features: []string{"tunnel"},
			About:    localapi.About{Name: "Linux Box", OS: "linux"},
		},
	}
	a.status.Watching = []localapi.Watched{
		{
			DeviceID:   "111222333",
			ReceivedAt: 9999999999999,
			Sample: &localapi.SystemSample{
				Services: []string{"ssh"},
				Nanokvm:  false,
				Memory:   localapi.Usage{Used: 8, Total: 16},
			},
		},
	}

	tt := ui.NewTester(a.MobileView, 390, 844)

	// Device detail hides unsupported services (e.g. NanoKVM, RDP, VNC) and renders Memory 50% (not 5000%).
	a.mobile.selectedDev = "111222333"
	tt.Frame()
	if _, ok := tt.Find("NanoKVM"); ok {
		t.Fatal("NanoKVM button shown for host without NanoKVM")
	}
	if _, ok := tt.Find("Terminal (SSH)"); !ok {
		t.Fatal("Terminal (SSH) button missing for host advertising ssh")
	}
	if _, ok := tt.Find("Remote Desktop"); ok {
		t.Fatal("Remote Desktop button shown when host sample only advertises ssh")
	}
	if _, ok := tt.Find("Memory 50%"); !ok {
		t.Fatal("expected Memory 50% summary without 100x double-multiplication")
	}

	// Compact Settings shows category menu first, pushes sub-page on tap, and pops on Back.
	a.mobile.navTab = 2
	tt.Frame()
	if _, ok := tt.Find("Device ID"); ok {
		t.Fatal("compact Settings opened sub-page content before tapping menu item")
	}
	devRow, ok := tt.Find("This device")
	if !ok {
		t.Fatal("Settings menu item 'This device' not found")
	}
	cx, cy := devRow.X+devRow.W/2, devRow.Y+devRow.H/2
	tt.FingerDown(cx, cy)
	tt.FingerUp(cx, cy)
	tt.Frame()
	if !a.mobile.settingsOpen {
		t.Fatal("tapping Settings menu item did not push sub-page")
	}
	if _, ok := tt.Find("Device ID"); !ok {
		t.Fatal("This device sub-page content not shown after drill-down")
	}
	a.mobileBack()
	if a.mobile.settingsOpen {
		t.Fatal("mobileBack did not pop compact Settings sub-page back to menu")
	}

	// Multiple open sessions preserve deterministic ordering across calls and frames.
	_ = a.newViewer("999000111", localapi.About{Name: "Zeta"}, desktopWant, false, 0)
	_ = a.newViewer("111000999", localapi.About{Name: "Alpha"}, desktopWant, false, 0)
	a.mobile.terms["555000555:ssh:1"] = &mobileTermSession{app: a, device: "555000555", name: "Mid", state: "connected"}

	want := a.mobileOpenSessionTitles()
	for range 20 {
		got := a.mobileOpenSessionTitles()
		if len(got) != 3 || got[0] != "Alpha · 111 000 999" || got[1] != "Mid · SSH" || got[2] != "Zeta · 999 000 111" {
			t.Fatalf("session titles = %v, want [Alpha · 111 000 999, Mid · SSH, Zeta · 999 000 111] (first=%v)", got, want)
		}
	}

	// Touchpad SetSink updates picture dimensions after initial zero-size frame.
	v := a.viewerFor("111000999")
	v.mu.Lock()
	v.phase = localapi.SessionInfo{State: "connected", DeviceID: "111000999", Peer: localapi.About{Name: "Alpha"}}
	v.start = videoStart{Codec: "h264", Width: 1920, Height: 1080}
	v.bm = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 64, 36)))
	v.control = true
	v.mu.Unlock()
	sink0 := &kyokaiTouchpadSink{v: v, picW: 0, picH: 0}
	u := a.mobileEnsureViewerUI(v.tabKey(), sink0)
	sink0.ui = u
	sink1 := &kyokaiTouchpadSink{v: v, ui: u, picW: 400, picH: 225, viewW: 400, viewH: 225}
	_ = a.mobileEnsureViewerUI(v.tabKey(), sink1)
	sink1.Move(100, 0)
	if u.curX <= 0.7 || u.curX >= 0.8 {
		t.Fatalf("Touchpad SetSink did not update picture width: curX = %.3f, want ~0.75", u.curX)
	}

	// Split-keyboard center touchpad does not snap curX/curY back to stale host
	// pointer coordinates while a finger is active on u.kb.pad.
	v.mu.Lock()
	v.pointer = hostPointer{Visible: true, X: 0.1, Y: 0.1}
	v.mu.Unlock()
	a.prefs.SplitKeyboard = true
	a.mobile.activeSession = v.tabKey()
	u.kbOpen = true
	wtt := ui.NewTester(a.MobileView, 904, 1000)
	wtt.Frame()
	wtt.Frame() // second frame so stage.LayoutBounds() has non-zero picture dimensions
	now := time.Now()
	u.kb.pad.Down(0, 20, 20, now)
	u.kb.pad.Move(0, 120, 20, now.Add(20*time.Millisecond))
	movedX := u.curX
	if movedX <= 0.12 {
		t.Fatalf("split keyboard center touchpad did not advance cursor: curX = %.3f", movedX)
	}
	wtt.Frame()
	if u.curX != movedX {
		t.Fatalf("host pointer overwrote active split-keyboard touchpad cursor: got %.3f, want %.3f", u.curX, movedX)
	}
	u.kb.pad.Up(0, now.Add(40*time.Millisecond))
}
