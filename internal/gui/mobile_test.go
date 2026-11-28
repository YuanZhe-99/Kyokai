package gui

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
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
	off := false
	a.prefs.AutoKeyboard = &off
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
	if _, ok := tt.Find("50 %"); !ok {
		t.Fatal("expected 50 % memory telemetry without 100x double-multiplication")
	}
	if _, ok := tt.Find("5000%"); ok {
		t.Fatal("unexpected 5000% memory value")
	}
	if _, ok := tt.Find("5000 %"); ok {
		t.Fatal("unexpected 5000 % memory value")
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

func makeMobileConnectedViewer(t *testing.T, a *App, dev string, vidW, vidH int) *viewer {
	t.Helper()
	v := a.newViewer(dev, localapi.About{Name: "Studio Mac", OS: "macos"}, desktopWant, false, 0)
	v.mu.Lock()
	v.phase = localapi.SessionInfo{State: "connected", DeviceID: dev, Peer: localapi.About{Name: "Studio Mac", OS: "macos"}}
	v.start = videoStart{Codec: "hevc", Width: vidW, Height: vidH}
	v.bm = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 64, 36)))
	v.control = true
	v.mu.Unlock()
	a.mobile.activeSession = v.tabKey()
	return v
}

func TestMobileCollapsedToolbarHitAndDrag(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	a := demoApp(t)
	a.initMobile()
	off := false
	a.prefs.AutoKeyboard = &off
	v := makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)

	tt := ui.NewTester(a.MobileView, 390, 844)
	tt.SetInsets(24, 0, 20, 0)
	tt.Frame()
	tt.Frame()

	foldBtn, ok := tt.Find(L.MobileA11yHideToolbar())
	if !ok {
		t.Fatal("fold toolbar button not found")
	}
	tt.FingerDown(foldBtn.X+foldBtn.W/2, foldBtn.Y+foldBtn.H/2)
	tt.FingerUp(foldBtn.X+foldBtn.W/2, foldBtn.Y+foldBtn.H/2)
	tt.Frame()
	tt.Frame()

	u := a.mobile.views[v.tabKey()]
	if u == nil || !u.barFolded {
		t.Fatal("tapping fold button did not collapse toolbar")
	}
	hbRect, ok := tt.Find(L.MobileA11yShowToolbar())
	if !ok {
		t.Fatal("collapsed toolbar handle not found")
	}
	hx, hy := hbRect.X+hbRect.W/2, hbRect.Y+hbRect.H/2
	if hx < u.lastBarRect.X || hx > u.lastBarRect.X+u.lastBarRect.W || hy < u.lastBarRect.Y || hy > u.lastBarRect.Y+u.lastBarRect.H {
		t.Fatalf("collapsed handle center (%.1f, %.1f) outside TouchBridge exclusion rect %+v", hx, hy, u.lastBarRect)
	}

	// Tapping collapsed handle must expand the toolbar cleanly.
	tt.FingerDown(hx, hy)
	tt.FingerUp(hx, hy)
	tt.Frame()
	if u.barFolded {
		t.Fatal("tapping collapsed handle did not expand toolbar")
	}

	// Fold again and drag toward the left edge: handle must move, snap left, and keep exclusion synced.
	foldBtn, ok = tt.Find(L.MobileA11yHideToolbar())
	if !ok {
		t.Fatal("fold toolbar button not found after expand")
	}
	tt.FingerDown(foldBtn.X+foldBtn.W/2, foldBtn.Y+foldBtn.H/2)
	tt.FingerUp(foldBtn.X+foldBtn.W/2, foldBtn.Y+foldBtn.H/2)
	tt.Frame()
	tt.Frame()
	hbRect, ok = tt.Find(L.MobileA11yShowToolbar())
	if !ok {
		t.Fatal("collapsed handle missing on second fold")
	}
	hx, hy = hbRect.X+hbRect.W/2, hbRect.Y+hbRect.H/2
	tt.FingerDown(hx, hy)
	tt.FingerMove(100, 320)
	tt.FingerMove(18, 380)
	tt.FingerUp(18, 380)
	for range 12 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if !u.barFolded {
		t.Fatal("dragging collapsed handle unexpectedly unfolded toolbar")
	}
	if u.dragHandle.X != 0 {
		t.Fatalf("dragged handle X = %.3f, want snapped 0 (left edge)", u.dragHandle.X)
	}
	hbRect2, ok := tt.Find(L.MobileA11yShowToolbar())
	if !ok {
		t.Fatal("collapsed handle missing after drag")
	}
	hx2, hy2 := hbRect2.X+hbRect2.W/2, hbRect2.Y+hbRect2.H/2
	if hx2 < u.lastBarRect.X || hx2 > u.lastBarRect.X+u.lastBarRect.W || hy2 < u.lastBarRect.Y || hy2 > u.lastBarRect.Y+u.lastBarRect.H {
		t.Fatalf("post-drag handle center (%.1f, %.1f) outside TouchBridge exclusion rect %+v", hx2, hy2, u.lastBarRect)
	}
}

func TestMobileNavPlacementBottomAndWide(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	a := demoApp(t)
	a.initMobile()
	tt := ui.NewTester(a.MobileView, 904, 1000)

	// Open Appearance settings on a wide foldable screen and select "Bottom".
	a.mobile.navTab = 2
	a.mobile.settingsOpen = true
	a.mobile.settingsPage = mobSetAppearance
	tt.Frame()

	botBtn, ok := tt.Find(L.MobileSettingsNavPlacementBottom())
	if !ok {
		t.Fatal("Bottom option in Navigation position not found")
	}
	tt.FingerDown(botBtn.X+botBtn.W/2, botBtn.Y+botBtn.H/2)
	tt.FingerUp(botBtn.X+botBtn.W/2, botBtn.Y+botBtn.H/2)
	tt.Frame()
	if a.prefs.NavPlacement != "bottom" {
		t.Fatalf("NavPlacement = %q after selecting Bottom, want %q", a.prefs.NavPlacement, "bottom")
	}

	// Switch back to Devices tab: wide screen with "bottom" must render the centered floating capsule bar.
	a.mobile.navTab = 0
	tt.Frame()
	devsInfo, ok := tt.AccessByRole(ui.RoleTab, L.MobileNavDevices())
	if !ok {
		t.Fatal("Devices tab missing from wide floating bottom bar")
	}
	setsInfo, ok := tt.AccessByRole(ui.RoleTab, L.MobileNavSettings())
	if !ok {
		t.Fatal("Settings tab missing from wide floating bottom bar")
	}
	devs, sets := devsInfo.Bounds, setsInfo.Bounds
	if devs.Y < 850 || sets.Y < 850 {
		t.Fatalf("expected wide floating bottom bar near bottom of 904x1000, got Devices.Y=%.1f Settings.Y=%.1f", devs.Y, sets.Y)
	}
	if devs.X < 180 || sets.X+sets.W > 740 {
		t.Fatalf("expected centered capsule dock on wide screen, got Devices.X=%.1f Settings.Right=%.1f", devs.X, sets.X+sets.W)
	}
}

func TestMobileAutoOpenKeyboard(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	// 1. Portrait 16:9 remote screen has generous blank space -> auto-opens keyboard.
	a := demoApp(t)
	a.initMobile()
	v := makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)
	tt := ui.NewTester(a.MobileView, 390, 844)
	tt.SetInsets(24, 0, 20, 0)
	tt.Frame()
	tt.Frame()

	u := a.mobile.views[v.tabKey()]
	if u == nil || !u.kbOpen || !u.kbAutoOpened {
		t.Fatalf("expected auto-open keyboard in portrait 16:9 viewer (u=%+v)", u)
	}

	// 2. Manual hide within the same geometry bucket keeps keyboard hidden.
	hideBtn, ok := tt.Find("Hide keyboard")
	if !ok {
		t.Fatal("Hide keyboard button not found")
	}
	tt.FingerDown(hideBtn.X+hideBtn.W/2, hideBtn.Y+hideBtn.H/2)
	tt.FingerUp(hideBtn.X+hideBtn.W/2, hideBtn.Y+hideBtn.H/2)
	tt.Frame()
	tt.Frame()
	if u.kbOpen || u.kbAutoOpened {
		t.Fatalf("expected manually hidden keyboard to stay closed in same geometry (kbOpen=%v)", u.kbOpen)
	}

	// 3. Auto-opened portrait keyboard closes automatically on landscape rotation.
	a2 := demoApp(t)
	a2.initMobile()
	v2 := makeMobileConnectedViewer(t, a2, "657365333", 1920, 1080)
	tt2 := ui.NewTester(a2.MobileView, 390, 844)
	tt2.SetInsets(24, 0, 20, 0)
	tt2.Frame()
	u2 := a2.mobile.views[v2.tabKey()]
	if u2 == nil || !u2.kbOpen || !u2.kbAutoOpened {
		t.Fatal("expected auto-open before rotation")
	}
	tt2.SetSize(844, 390)
	tt2.Frame()
	if u2.kbOpen {
		t.Fatal("expected auto-opened keyboard to close automatically on landscape rotation")
	}

	// 4. Setting AutoKeyboard = false disables auto-opening.
	a3 := demoApp(t)
	a3.initMobile()
	off := false
	a3.prefs.AutoKeyboard = &off
	v3 := makeMobileConnectedViewer(t, a3, "657365333", 1920, 1080)
	tt3 := ui.NewTester(a3.MobileView, 390, 844)
	tt3.Frame()
	if u3 := a3.mobile.views[v3.tabKey()]; u3 != nil && u3.kbOpen {
		t.Fatal("expected keyboard to stay closed when AutoKeyboard is disabled")
	}
}

func TestMobileHostStatusRichDashboard(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	a := demoApp(t)
	a.initMobile()
	a.net = netState{loaded: true}
	a.keys = keysState{loaded: true}
	a.status.Recent = []localapi.RecentDevice{
		{
			DeviceID: "111222333",
			Features: []string{"desktop", "tunnel"},
			About:    localapi.About{Name: "Workstation", OS: "linux"},
		},
	}
	a.status.Network.Devices = []localapi.NetworkDevice{
		{DeviceID: "111222333", Path: "direct", LatencyMS: 6},
	}
	sample := &localapi.SystemSample{
		CPU:      localapi.CPUUse{Usage: 42, Cores: 8, User: 32, System: 10},
		Memory:   localapi.Usage{Used: 8, Total: 16},
		Disk:     localapi.Usage{Used: 256, Total: 512},
		GPU:      &localapi.GPUUse{Usage: 25},
		UptimeS:  7200,
		Services: []string{"ssh"},
	}
	a.status.Watching = []localapi.Watched{
		{DeviceID: "111222333", ReceivedAt: time.Now().UnixMilli(), Sample: sample},
	}

	tt := ui.NewTester(a.MobileView, 390, 844)

	// Device card strip shows compact 4-metric figures on the Devices list.
	tt.Frame()
	if !tt.HasText("42") {
		t.Fatalf("expected CPU 42 figure on device card, texts=%v", tt.Texts())
	}

	// Device detail shows Pulse telemetry tiles.
	a.mobile.selectedDev = "111222333"
	tt.Frame()
	for _, want := range []string{"CPU", "Memory", "50 %"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("device detail telemetry tile missing %q (texts=%v)", want, tt.Texts())
		}
	}

	// Stale sample beyond freshness window hides live figures.
	a.status.Watching[0].ReceivedAt = time.Now().Add(-5 * time.Minute).UnixMilli()
	tt.Frame()
	if _, ok := tt.Find("50 %"); ok {
		t.Fatal("stale host telemetry should not render live percentage tiles")
	}
}

func TestMobileLaptopModeDeckAndGestureChin(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	posture := androidhost.Posture{HalfOpened: true, Orientation: "horizontal", HingeTop: 450, HingeBottom: 470}
	sh, ch, dh := mobileLaptopGeometry(900, 24, 20, posture, 1, true)
	if sh != 426 || ch != 20 || dh != 430 {
		t.Fatalf("mobileLaptopGeometry(useSafe=true) = (%.1f, %.1f, %.1f), want (426, 20, 430)", sh, ch, dh)
	}
	sh2, ch2, dh2 := mobileLaptopGeometry(900, 24, 20, posture, 1, false)
	if sh2 != 450 || ch2 != 20 || dh2 != 430 {
		t.Fatalf("mobileLaptopGeometry(useSafe=false) = (%.1f, %.1f, %.1f), want (450, 20, 430)", sh2, ch2, dh2)
	}

	// 1. Half-open foldable Laptop Mode: stage above hinge, full keyboard + centered touchpad below hinge.
	a := demoApp(t)
	a.initMobile()
	a.prefs.LaptopMode = true
	a.mobile.posture = posture
	v := makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)
	tt := ui.NewTester(a.MobileView, 904, 900)
	tt.SetScale(1)
	tt.SetInsets(24, 0, 20, 0)
	tt.Frame()
	tt.Frame()

	u := a.mobile.views[v.tabKey()]
	if u == nil || u.kb.padRect.W <= 0 || u.kb.padRect.Y <= 470 {
		t.Fatalf("expected laptop deck touchpad below hinge (Y>470), got padRect=%+v", func() ui.Rect {
			if u == nil {
				return ui.Rect{}
			}
			return u.kb.padRect
		}())
	}
	if scr, ok := tt.AccessByRole(ui.RoleImage, L.MobileA11yRemoteScreen(v.title)); !ok || scr.Bounds.Y+scr.Bounds.H > 451 {
		t.Fatalf("laptop stage should sit above hingeTop=450, got ok=%v rect=%+v", ok, scr.Bounds)
	}
	for _, want := range []string{"Tab", "Caps", L.MobileTouchpad()} {
		r, ok := tt.Find(want)
		if !ok || r.Y <= 470 {
			t.Fatalf("laptop deck key/pad %q should render below hingeBottom=470, ok=%v rect=%+v", want, ok, r)
		}
	}

	// 2. Portrait phone docked keyboard must preserve >=50dp bottom gesture chin clearance even when safeBottom == 0.
	a2 := demoApp(t)
	a2.initMobile()
	makeMobileConnectedViewer(t, a2, "657365333", 1920, 1080)
	tt2 := ui.NewTester(a2.MobileView, 390, 844)
	tt2.SetInsets(24, 0, 0, 0)
	tt2.Frame()
	tt2.Frame()

	spInfo, ok := tt2.AccessByRole(ui.RoleButton, "Space")
	if !ok {
		t.Fatal("Spacebar not found on docked phone keyboard")
	}
	sp := spInfo.Bounds
	if sp.Y+sp.H > 844-50 {
		t.Fatalf("Spacebar bottom %.1f too close to screen bottom (844) without Android gesture chin clearance", sp.Y+sp.H)
	}
}

func TestMobileMaterialContrast(t *testing.T) {
	dyn := androidhost.MaterialPalette{
		Available:                    true,
		PrimaryLight:                 0xFF006A60,
		OnPrimaryLight:               0xFFFFFFFF,
		PrimaryContainerLight:        0xFF74F8E5,
		OnPrimaryContainerLight:      0xFF00201C,
		SecondaryContainerLight:      0xFFCCE8E2,
		OnSecondaryContainerLight:    0xFF05201C,
		SurfaceLight:                 0xFFF4FBF8,
		SurfaceContainerLowestLight:  0xFFFFFFFF,
		SurfaceContainerLowLight:     0xFFEEF5F2,
		SurfaceContainerLight:        0xFFE9EFEC,
		SurfaceContainerHighLight:    0xFFE3EAE7,
		SurfaceContainerHighestLight: 0xFFDDE4E1,
		OnSurfaceLight:               0xFF161D1C,
		OnSurfaceVariantLight:        0xFF3F4947,
		OutlineLight:                 0xFF6F7977,
		OutlineVariantLight:          0xFFBEC9C6,
		PrimaryDark:                  0xFF53DBC9,
		OnPrimaryDark:                0xFF003731,
		PrimaryContainerDark:         0xFF005048,
		OnPrimaryContainerDark:       0xFF74F8E5,
		SecondaryContainerDark:       0xFF334B47,
		OnSecondaryContainerDark:     0xFFCCE8E2,
		SurfaceDark:                  0xFF0E1513,
		SurfaceContainerLowestDark:   0xFF090F0E,
		SurfaceContainerLowDark:      0xFF161D1C,
		SurfaceContainerDark:         0xFF1A2120,
		SurfaceContainerHighDark:     0xFF252B2A,
		SurfaceContainerHighestDark:  0xFF2F3635,
		OnSurfaceDark:                0xFFDDE4E1,
		OnSurfaceVariantDark:         0xFFBEC9C6,
		OutlineDark:                  0xFF899390,
		OutlineVariantDark:           0xFF3F4947,
	}

	for _, tc := range []struct {
		name string
		mc   mobileColors
	}{
		{"fallback-light", resolveMobileColors(false, androidhost.MaterialPalette{})},
		{"fallback-dark", resolveMobileColors(true, androidhost.MaterialPalette{})},
		{"dynamic-light", resolveMobileColors(false, dyn)},
		{"dynamic-dark", resolveMobileColors(true, dyn)},
	} {
		check := func(pair string, fg, bg ui.Color) {
			t.Helper()
			if r := contrast(fg, bg); r < 4.5 {
				t.Errorf("%s %s contrast %.2f:1 < 4.5:1", tc.name, pair, r)
			}
		}
		check("OnPrimary/Primary", tc.mc.OnPrimary, tc.mc.Primary)
		check("OnPrimaryContainer/PrimaryContainer", tc.mc.OnPrimaryContainer, tc.mc.PrimaryContainer)
		check("OnSecondaryContainer/SecondaryContainer", tc.mc.OnSecondaryContainer, tc.mc.SecondaryContainer)
		check("OnSurface/Surface", tc.mc.OnSurface, tc.mc.Surface)
		check("OnSurface/KbKey", tc.mc.OnSurface, tc.mc.KbKey)
		if tc.mc.Dark {
			lTray, lFunc, lKey := luminance(tc.mc.KbTray), luminance(tc.mc.KbFuncKey), luminance(tc.mc.KbKey)
			if !(lKey > lFunc && lFunc > lTray) {
				t.Errorf("%s expected dark keycap luminance KbKey(%.4f) > KbFuncKey(%.4f) > KbTray(%.4f)", tc.name, lKey, lFunc, lTray)
			}
		}
	}
}

type fakeAndroidHost struct {
	androidhost.Host
	mu       sync.Mutex
	sessions []string
}

func (f *fakeAndroidHost) SetSessions(text string) {
	f.mu.Lock()
	f.sessions = append(f.sessions, text)
	f.mu.Unlock()
}

func (f *fakeAndroidHost) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sessions))
	copy(out, f.sessions)
	return out
}

func stubMobileKeepAwake(t *testing.T) (acquired, released *int) {
	t.Helper()
	var acq, rel int
	prev := mobileKeepAwake
	t.Cleanup(func() { mobileKeepAwake = prev })
	mobileKeepAwake = func() func() {
		acq++
		return func() { rel++ }
	}
	return &acq, &rel
}

func TestMobileBackgroundPowerSavingDefault(t *testing.T) {
	acq, rel := stubMobileKeepAwake(t)
	a := demoApp(t)
	a.initMobile()
	h := &fakeAndroidHost{}
	a.mobile.host = h
	a.visible = true

	if a.prefs.KeepBackgroundSessions {
		t.Fatal("KeepBackgroundSessions should default to false (power-saving mode)")
	}

	v := makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)
	svc := &serviceTab{app: a, device: "222333444", service: serviceRDP, name: "Win", kind: "Remote Desktop", phase: svcRunning}
	a.services[svc.tabKey()] = svc
	a.mobile.terms["555000555:ssh:1"] = &mobileTermSession{app: a, device: "555000555", name: "Linux", state: "connected"}
	a.aiLinks["333444555"] = &aiLink{app: a, device: "333444555", panes: 2}

	a.mobileSyncSessions()
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("power-saving mode should not start foreground SessionService, got %v", calls)
	}
	if *acq != 1 || *rel != 0 {
		t.Fatalf("expected wake lock acquired once while viewing, got acq=%d rel=%d", *acq, *rel)
	}

	a.mobileEnterBackground()

	if a.visible {
		t.Fatal("mobileEnterBackground should mark app not visible")
	}
	if *rel != 1 {
		t.Fatalf("expected wake lock released on background, got rel=%d", *rel)
	}
	if a.mobile.activeSession != "" {
		t.Fatalf("activeSession = %q, want empty after background disconnect", a.mobile.activeSession)
	}
	if n := a.mobileOpenSessionCount(); n != 0 {
		t.Fatalf("open session count = %d after backgrounding in power-saving mode, want 0", n)
	}
	if _, ok := a.viewers[v.tabKey()]; ok {
		t.Fatal("viewer remained open after backgrounding in power-saving mode")
	}
	if len(a.services) != 0 || len(a.mobile.terms) != 0 || len(a.aiLinks) != 0 {
		t.Fatalf("expected all sessions closed, got services=%d terms=%d aiLinks=%d", len(a.services), len(a.mobile.terms), len(a.aiLinks))
	}
}

func TestMobileKeepBackgroundSessions(t *testing.T) {
	_, rel := stubMobileKeepAwake(t)
	a := demoApp(t)
	a.initMobile()
	h := &fakeAndroidHost{}
	a.mobile.host = h
	a.visible = true
	a.prefs.KeepBackgroundSessions = true

	v := makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)
	a.mobileSyncSessions()
	calls := h.calls()
	if len(calls) != 1 || calls[0] == "" {
		t.Fatalf("expected SessionService started when KeepBackgroundSessions is true, got %v", calls)
	}

	a.mobileEnterBackground()
	if a.visible {
		t.Fatal("mobileEnterBackground should mark app not visible")
	}
	if *rel != 1 {
		t.Fatalf("expected screen wake lock released in background even when keeping sessions, got rel=%d", *rel)
	}
	if _, ok := a.viewers[v.tabKey()]; !ok {
		t.Fatal("viewer disconnected despite KeepBackgroundSessions = true")
	}
	if calls := h.calls(); len(calls) != 1 || calls[0] == "" {
		t.Fatalf("SessionService should remain active while keeping background sessions, got %v", calls)
	}

	a.mu.Lock()
	a.prefs.KeepBackgroundSessions = false
	a.mu.Unlock()
	a.mobileSyncSessions()
	calls = h.calls()
	if len(calls) != 2 || calls[1] != "" {
		t.Fatalf("expected SessionService stopped when KeepBackgroundSessions turned off, got %v", calls)
	}
}

func TestMobileTaskRemovedStopsEngineAndSessions(t *testing.T) {
	stubMobileKeepAwake(t)
	a := demoApp(t)
	a.initMobile()
	h := &fakeAndroidHost{}
	a.mobile.host = h
	a.visible = true
	a.prefs.KeepBackgroundSessions = true

	done := make(chan struct{})
	a.mobile.engineDone = done
	go func() {
		<-a.ctx.Done()
		close(done)
	}()

	_ = makeMobileConnectedViewer(t, a, "657365333", 1920, 1080)
	svc := &serviceTab{app: a, device: "222333444", service: serviceVNC, name: "Mac", kind: "VNC", phase: svcRunning}
	a.services[svc.tabKey()] = svc
	a.mobile.terms["555000555:ssh:1"] = &mobileTermSession{app: a, device: "555000555", name: "Linux", state: "connected"}
	a.aiLinks["333444555"] = &aiLink{app: a, device: "333444555", panes: 1}

	a.mobileOnTaskRemoved()

	if !a.quitting {
		t.Fatal("mobileOnTaskRemoved should mark app quitting")
	}
	if a.ctx.Err() == nil {
		t.Fatal("mobileOnTaskRemoved should cancel app context to stop in-process engine")
	}
	if n := a.mobileOpenSessionCount(); n != 0 {
		t.Fatalf("open session count = %d after task removed, want 0", n)
	}
	select {
	case <-done:
	default:
		t.Fatal("mobileOnTaskRemoved returned before engineDone closed")
	}
}

func TestMobileSettingsKeepBackgroundToggle(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })

	a := demoApp(t)
	a.initMobile()
	a.mobile.navTab = 2
	a.mobile.settingsOpen = true
	a.mobile.settingsPage = mobSetDevice

	tt := ui.NewTester(a.MobileView, 390, 844)
	tt.Frame()

	swInfo, ok := tt.AccessByRole(ui.RoleSwitch, L.MobileKeepBackground())
	if !ok {
		t.Fatalf("switch %q not found on Device settings page", L.MobileKeepBackground())
	}
	r := swInfo.Bounds
	tt.FingerDown(r.X+r.W/2, r.Y+r.H/2)
	tt.FingerUp(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !a.prefs.KeepBackgroundSessions {
		t.Fatal("tapping KeepBackgroundSessions switch did not enable it")
	}

	tt.FingerDown(r.X+r.W/2, r.Y+r.H/2)
	tt.FingerUp(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if a.prefs.KeepBackgroundSessions {
		t.Fatal("tapping KeepBackgroundSessions switch again did not disable it")
	}
}
