package gui

import (
	"slices"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// viewerWindow holds connections as tabs, as a browser holds pages
// (doc/en-us/gui.md, "Connections"). The main window is one, with Home as
// its first tab, which cannot close; a tab moves to another window when
// dropped on its tabs, and into a window of its own when dropped outside
// Kyokai or with "Open in new window". A tab is its connection: closing it
// disconnects. Fields after mu are guarded by it and change on the main
// thread.
type viewerWindow struct {
	app *App
	win *mygo.Window
	// main is the app's main window, whose Home tab shows the app.
	main bool

	mu   sync.Mutex
	tabs []*viewer
	cur  int
	// home: the main window shows Home rather than a connection.
	home bool
	// full: the window is in full screen, which hides the tabs.
	full bool
}

// tabDrag is what a dragged tab carries: its connection's Device ID.
type tabDrag struct{ device string }

// TitleBarOptions hides a window's title bar, so that the tab strip is the
// title bar: the window's own controls (the traffic lights on macOS;
// minimize, maximize and close on Windows) sit in it, as tall as the strip,
// and its empty part moves the window (tabBar).
func TitleBarOptions(o mygo.WindowOptions) mygo.WindowOptions {
	o.TitleBarStyle = mygo.TitleBarHidden
	o.TitleBarHeight = int(tabStripH)
	o.TrafficLightPosition = &mygo.Point{X: trafficX, Y: trafficY}
	return o
}

// newViewerWindow makes an empty window for connection tabs, shown as its
// first tab comes (add).
// Side effects: a native window, kept out of capture unless Security says
// otherwise; closing it disconnects its tabs.
func (a *App) newViewerWindow() *viewerWindow {
	vw := &viewerWindow{app: a}
	vw.win = mygo.NewWindow(TitleBarOptions(mygo.WindowOptions{
		Title: "Kyokai", Width: 1280, Height: 800, MinWidth: 480, MinHeight: 320,
		// Shown (add) once its display affinity is set: never captured for a
		// moment.
		Hidden:  true,
		Content: ui.View(vw.view),
	}))
	a.mu.Lock()
	show := a.prefs.ShowInCapture
	a.viewerWins = append(a.viewerWins, vw)
	a.mu.Unlock()
	a.syncDock()
	vw.win.SetContentProtection(!show)
	vw.watchFullScreen()
	a.watchFocus(vw)
	vw.win.OnClosed(func() {
		a.mu.Lock()
		a.viewerWins = slices.DeleteFunc(a.viewerWins, func(w *viewerWindow) bool { return w == vw })
		if a.lastWin == vw {
			a.lastWin = a.main
		}
		a.mu.Unlock()
		a.syncDock()
		vw.mu.Lock()
		tabs := vw.tabs
		vw.tabs = nil
		vw.mu.Unlock()
		for _, v := range tabs {
			v.disconnect()
		}
	})
	return vw
}

// watchFullScreen keeps the window's full-screen state, which hides the
// tabs, in step with the system's.
func (vw *viewerWindow) watchFullScreen() {
	set := func(on bool) func() {
		return func() {
			vw.mu.Lock()
			vw.full = on
			vw.mu.Unlock()
			vw.win.Update(func() {})
		}
	}
	vw.win.OnEnterFullScreen(set(true))
	vw.win.OnLeaveFullScreen(set(false))
}

// fullScreen reports whether the window is in full screen.
func (vw *viewerWindow) fullScreen() bool {
	vw.mu.Lock()
	defer vw.mu.Unlock()
	return vw.full
}

// add puts a viewer in this window as the last tab, shown.
// Notes: call on the main thread.
func (vw *viewerWindow) add(v *viewer) {
	vw.mu.Lock()
	prev := vw.shown()
	vw.tabs = append(vw.tabs, v)
	vw.cur, vw.home = len(vw.tabs)-1, false
	vw.mu.Unlock()
	if prev != nil && prev != v {
		prev.endGrab()
	}
	v.home.Store(vw)
	vw.retitle()
	vw.win.Show()
	vw.win.Focus()
	vw.app.syncFront()
}

// show brings a viewer's tab to the front of its window.
// Notes: call on the main thread.
func (vw *viewerWindow) show(v *viewer) {
	vw.mu.Lock()
	prev := vw.shown()
	if i := slices.Index(vw.tabs, v); i >= 0 {
		vw.cur, vw.home = i, false
	}
	vw.mu.Unlock()
	if prev != nil && prev != v {
		prev.endGrab()
	}
	vw.retitle()
	vw.win.Show()
	vw.win.Focus()
	vw.app.syncFront()
}

// remove takes a viewer's tab out of this window. A window of its own
// closes when it has no tab left; the main window shows Home.
// Returns: whether the tab was here.
// Notes: call on the main thread.
func (vw *viewerWindow) remove(v *viewer) bool {
	vw.mu.Lock()
	i := slices.Index(vw.tabs, v)
	if i < 0 {
		vw.mu.Unlock()
		return false
	}
	vw.tabs = slices.Delete(vw.tabs, i, i+1)
	if vw.cur >= i && vw.cur > 0 {
		vw.cur--
	}
	empty := len(vw.tabs) == 0
	if empty {
		vw.home = true
	}
	full := vw.full
	vw.mu.Unlock()
	v.endGrab()
	switch {
	case empty && !vw.main:
		vw.win.Close()
	case empty && full:
		vw.win.SetFullScreen(false)
		vw.retitle()
	default:
		vw.retitle()
	}
	vw.app.syncFront()
	return true
}

// shown is the viewer of the tab shown, or nil for Home. Notes: the
// caller holds vw.mu.
func (vw *viewerWindow) shown() *viewer {
	if vw.home || vw.cur >= len(vw.tabs) {
		return nil
	}
	return vw.tabs[vw.cur]
}

// retitle names the window after the tab shown.
func (vw *viewerWindow) retitle() {
	vw.mu.Lock()
	v := vw.shown()
	vw.mu.Unlock()
	if v != nil {
		vw.win.SetTitle(L.ViewerTitle(v.title))
	} else {
		vw.win.SetTitle("Kyokai")
	}
}

// moveTab moves a device's tab into another window (a new one for nil).
// Notes: call on the main thread; a tab alone in a window of its own stays
// when it would only get a window of its own again.
func (a *App) moveTab(device string, to *viewerWindow) {
	v := a.viewerFor(device)
	if v == nil {
		return
	}
	from := v.home.Load()
	if from == to {
		return
	}
	if to == nil {
		if from != nil && !from.main {
			from.mu.Lock()
			alone := len(from.tabs) == 1
			from.mu.Unlock()
			if alone {
				return
			}
		}
		to = a.newViewerWindow()
	}
	if from != nil {
		from.remove(v)
	}
	to.add(v)
}

// closeTab closes a tab, which disconnects its device.
func (vw *viewerWindow) closeTab(v *viewer) {
	if vw.remove(v) {
		v.disconnect()
	}
}

// view is the window: the tab strip, which is its title bar (none in full
// screen), then Home or the connection shown.
func (vw *viewerWindow) view(c *ui.Context) {
	vw.mu.Lock()
	tabs := slices.Clone(vw.tabs)
	cur, home, full := vw.cur, vw.home || len(vw.tabs) == 0, vw.full
	vw.mu.Unlock()
	ui.Column(c).Fill().Children(func() {
		if !full {
			vw.tabBar(c, tabs, cur, home)
		}
		switch {
		case vw.main && home:
			ui.Column(c.Key("home")).Grow(1).FillWidth().Children(func() { vw.app.View(c) })
		case cur < len(tabs):
			v := tabs[cur]
			ui.Column(c.Key("tab-" + v.device)).Grow(1).FillWidth().Children(func() { v.view(c) })
		}
	})
	// A tab not shown takes no keys (regression: a grab stayed on behind
	// the Home tab, and keys typed there went to the other computer).
	for i, v := range tabs {
		if i != cur || vw.main && home {
			v.syncGrab(false)
		}
	}
}

// tabBar is the tab strip, which is the window's title bar: room for the
// window's own controls (c.TitleBar), Home first in the main window, then
// one tab per connection with Open in new window and Close, and an empty
// part that moves the window (a double click zooms it, as a title bar
// does). Dragging a tab moves it; a tab dropped on the strip comes here.
func (vw *viewerWindow) tabBar(c *ui.Context, tabs []*viewer, cur int, home bool) {
	t := c.Theme()
	first := 0 // the index of the first connection's tab
	if vw.main {
		first = 1
	}
	shown := cur + first
	if home && vw.main {
		shown = 0
	}
	sel := shown
	room := c.TitleBar()
	strip := ui.Row(c.Key("strip")).FillWidth().Shrink(0).Height(max(room.Height, tabStripH)).
		Padding(0, room.Right+8, 0, room.Left+8).AlignItems(ui.Center).Background(t.Background)
	if _, over := ui.DragOver[tabDrag](strip); over {
		strip.Background(t.Accent.Alpha(0.14))
	}
	if d, ok := ui.Drop[tabDrag](strip); ok {
		vw.app.moveTab(d.device, vw)
	}
	strip.Children(func() {
		bar := ui.TabsBase(c.Key("tabs"), &sel, len(tabs)+first)
		bar.List.Gap(4).MinWidth(0).Clip()
		vw.tabList(c, bar, tabs, cur, home, first)
		ui.Box(c.Key("strip-drag")).Grow(1).FillHeight().DragWindow()
	})
	if sel == shown {
		return
	}
	vw.mu.Lock()
	prev := vw.shown()
	if vw.main && sel == 0 {
		vw.home = true
	} else if i := sel - first; i >= 0 && i < len(vw.tabs) {
		vw.cur, vw.home = i, false
	}
	vw.mu.Unlock()
	if prev != nil {
		prev.endGrab()
	}
	vw.retitle()
	vw.app.syncFront()
}

// tabList fills the strip's tab list: Home (the Kyokai tab) in the main
// window, then each connection's tab, which drags into another window.
func (vw *viewerWindow) tabList(c *ui.Context, bar ui.TabsParts, tabs []*viewer, cur int, home bool, first int) {
	t := c.Theme()
	bar.List.Children(func() {
		if vw.main {
			tab := bar.Tab(0).Padding(4, 10).Radius(6)
			if home {
				tab.Background(t.Surface)
			}
			tab.Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, appMark).FontSize(navIconSize).TextColor(t.Accent)
					ui.Text(c, "Kyokai").FontSize(sizeSmall)
				})
			})
		}
		for i, v := range tabs {
			tab := bar.Tab(i+first).Padding(4, 10).Radius(6)
			if i == cur && !home {
				tab.Background(t.Surface)
			}
			device := v.device
			tab.Drag(tabDrag{device}).DragData(transfer.TextData(v.title), transfer.DragOptions{
				// Dropped where no Kyokai window took it: a window of its own.
				Done: func(r transfer.Result) {
					if r.Operation == transfer.None {
						vw.app.moveTab(device, nil)
					}
				},
			})
			tab.Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, v.title).FontSize(sizeSmall).SingleLine().MaxWidth(tabTitleMaxW)
					if (vw.main || len(tabs) > 1) && tabIcon(c, "tab-out-"+device, "⧉", L.ViewerTabOut()) {
						vw.app.moveTab(device, nil)
					}
					if tabIcon(c, "tab-close-"+device, "×", L.ViewerTabClose()) {
						vw.closeTab(v)
					}
				})
			})
		}
	})
}

// tabIcon is a small borderless button in a tab, named for assistive
// technology by its tooltip. Returns: whether it was clicked.
func tabIcon(c *ui.Context, key, glyph, label string) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Padding(1, 6).Radius(4).Label(label).Tooltip(label)
	clicked := b.Clicked()
	b.Children(func() { ui.Text(c, glyph).FontSize(sizeSmall).TextColor(t.TextMuted) })
	return clicked
}

// disconnect ends the viewer's connection, as closing its tab does.
func (v *viewer) disconnect() {
	id := v.session()
	v.close()
	go func() { _, _ = v.app.requestWith(localapi.Request{Type: localapi.RequestClose, ID: id}) }()
}
