package gui

import (
	"image/color"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestCardsMeetCorners renders the main window with no card, one and two
// above the panes, sidebar shown and hidden: the cards are counted, so the
// panes below give up the window's top corners while one shows, and what
// sits in the window's top-right corner (the top pane, else the first
// card) is rounded there as paneRadii says — on Windows and Linux its
// face reaches into the corner, on macOS the window's material shows.
func TestCardsMeetCorners(t *testing.T) {
	request := localapi.HostRequest{ID: 3, DeviceID: "213559601", Want: []string{localapi.PermViewScreen}}
	here := localapi.SessionInfo{ID: 4, Role: "host", DeviceID: "100000002", State: "connected", Granted: []string{localapi.PermViewScreen}}
	const w, h = 1280, 800
	for _, tc := range []struct {
		name   string
		hidden bool
		setup  func(a *App)
		cards  int
	}{
		{"none", false, func(a *App) {}, 0},
		{"none, sidebar hidden", true, func(a *App) {}, 0},
		{"a request", false, func(a *App) { a.status.Requests = []localapi.HostRequest{request} }, 1},
		{"a request and a device here", true, func(a *App) {
			a.status.Requests = []localapi.HostRequest{request}
			a.status.Sessions = []localapi.SessionInfo{here}
		}, 2},
	} {
		a := demoApp(t)
		a.prefs.SidebarHidden = tc.hidden
		tc.setup(a)
		tt := ui.NewTester(a.main.view, w, h)
		tt.Frame()
		if a.main.cards != tc.cards {
			t.Errorf("%s: %d cards counted, want %d", tc.name, a.main.cards, tc.cards)
		}
		if a.main.cardAt != 0 {
			t.Errorf("%s: corners left for a card after the cards", tc.name)
		}
		img := tt.Image()
		// 3 in from the top-right corner of what sits there, paneMargin in.
		x, y := w-int(paneMargin)-3, int(paneMargin)+3
		got := img.RGBAAt(x, y)
		material := near(got, readableTheme(ui.LightTheme()).Surface)
		if want := paneRadii(runtime.GOOS, cornerTR)[1] >= paneRadius; material != want {
			t.Errorf("%s: at the window's top-right corner %v, the window's material shows %v, want %v", tc.name, got, material, want)
		}
	}
}

// near reports whether a pixel is within a few levels of c.
func near(p color.RGBA, c ui.Color) bool {
	d := func(a, b uint8) bool { return a-b < 6 || b-a < 6 }
	return d(p.R, c.R) && d(p.G, c.G) && d(p.B, c.B)
}
