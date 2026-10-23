package gui

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// tab is one pane's content (doc/en-us/gui.md, "Windows"): Home
// (*homeTab), a device's desktop (*viewer) or one of its services
// (*serviceTab). Methods run on the main thread unless they say otherwise.
type tab interface {
	// tabKey identifies the tab among every window's: homeKey, the Device
	// ID for a desktop, the Device ID and the service for a service
	// (serviceKey).
	tabKey() string
	// tabTitle names the tab and its window.
	tabTitle() string
	// head is what the pane's header and its sidebar row show.
	head() paneHead
	// tabWindow is the window the tab is in now (nil once it closed);
	// setTabWindow moves it. Safe from any goroutine.
	tabWindow() *viewerWindow
	setTabWindow(w *viewerWindow)
	// view draws the tab's content under its pane's header.
	view(c *ui.Context)
	// live reports whether the connection is under way or connected, as
	// closing a window counts them (decideClose). Safe from any goroutine.
	live() bool
	// disconnect ends the connection as its pane closes.
	disconnect()
	// syncGrab follows whether the tab's picture has the keyboard; endGrab
	// ends its keyboard grab: a tab not shown takes no keys.
	syncGrab(focused bool)
	endGrab()
	// setFront makes the tab the one in front, which alone exchanges the
	// clipboard, or not.
	setFront(on bool)
}

// paneHead is what a pane's header and its sidebar row show: the kind's
// icon, the device's name (else its Device ID), a second line (the Device
// ID beside a name, or the service) and the kind in words.
type paneHead struct {
	icon             *ui.SVG
	title, sub, kind string
}

// group is an entry of the main window's sidebar (doc/en-us/gui.md,
// "Windows"): one pane, or several beside and above each other (split),
// and the pane maximized in it, which then shows alone.
type group struct {
	split split
	zoom  tab
}

// shownTabs are the group's panes on screen: the one maximized, else all.
func (g *group) shownTabs() []tab {
	if g.zoom != nil {
		return []tab{g.zoom}
	}
	return g.split.shownTabs()
}

// viewerWindow is one of Kyokai's windows (doc/en-us/gui.md, "Windows").
// The main window holds groups of panes, Home among them, which its sidebar
// lists, and shows one group at a time; a window of its own holds one pane,
// never split, and closes with it. A pane is its connection: closing it
// disconnects. Fields after mu are guarded by it and change on the main
// thread.
type viewerWindow struct {
	app *App
	win *mygo.Window
	// main is the app's main window.
	main bool

	mu   sync.Mutex
	tabs []tab
	// groups are the sidebar's entries, in its order; cur is the one shown.
	groups []*group
	cur    *group
	// dragging: a pane was dragged over the panes in the last frame, which
	// then show where it may be dropped.
	dragging bool
	// full: the window is in full screen, which shows the pane in focus
	// alone, without its header.
	full bool
	// agreed: its user agreed to disconnect its connections, so closing it
	// once more does not ask again (closing.go).
	agreed bool
	// menu is the split menu open in a pane's header: its zone and the
	// pane's key, or "".
	menu string
}

// tabDrag is what a dragged pane carries: its tabKey.
type tabDrag struct{ key string }

// TitleBarOptions hides a window's title bar: on macOS its traffic lights
// sit at the top of the sidebar (or of a pane's header in a window of its
// own), centred on the top pane's header; on Windows the view draws
// minimize, maximize and close itself, at the end of the top-right pane's
// header (windowButtons). The window keeps its resizing edges, shadow and
// corners; its material shows around the panes.
func TitleBarOptions(o mygo.WindowOptions) mygo.WindowOptions {
	o.TitleBarStyle = mygo.TitleBarHidden
	o.HideWindowButtons = ownWindowButtons
	o.TrafficLightPosition = &mygo.Point{X: trafficX, Y: trafficY}
	o.Vibrancy = windowMaterial()
	return o
}

// ownWindowButtons tells whether the view draws its windows' buttons
// (windowButtons): on Windows, where the system's would sit on the window's
// corner rather than on a pane; macOS keeps its traffic lights.
var ownWindowButtons = runtime.GOOS == "windows"

// windowButtons are the window's minimize, maximize or restore, and close
// on Windows (ownWindowButtons), after a separator at the end of the
// top-right pane's header. While the Settings sheet covers the panes they
// stay in that place, above the dimmed window and apart from the sheet's ×
// (onScrim: no separator, light glyphs). The maximize button brings
// Windows 11's snap layouts, as the system's does; close is the system's
// close (OnClose decides). win is nil in the view tests.
func windowButtons(c *ui.Context, key string, win *mygo.Window, onScrim bool) {
	if !ownWindowButtons {
		return
	}
	t := c.Theme()
	button := func(name string, glyph *ui.SVG, label string) ui.Element {
		b := ui.ButtonBase(c.Key(key+"-"+name)).Size(winButtonW, winButtonH).Radius(6).Center().Shrink(0).Label(label)
		fg, hover := t.TextMuted, t.SurfaceHover
		if onScrim {
			fg, hover = scrimGlyph, scrimHover
		}
		switch {
		case b.Hovered() && name == "close":
			b.Background(closeHover)
			fg = closeHoverText
		case b.Hovered():
			b.Background(hover)
			if !onScrim {
				fg = t.Text
			}
		}
		b.Children(func() { ui.Icon(c, glyph).FontSize(winGlyph).TextColor(fg) })
		return b
	}
	if !onScrim {
		ui.Box(c).Size(1, 18).Shrink(0).Margin(0, 6).Background(t.Border)
	}
	if button("min", winMinimize, L.WindowMinimize()).Tooltip(L.WindowMinimize()).Clicked() && win != nil {
		win.Minimize()
	}
	maximized := win != nil && win.IsMaximized()
	glyph, label := winMaximize, L.WindowMaximize()
	if maximized {
		glyph, label = winRestore, L.WindowRestore()
	}
	if button("max", glyph, label).MaximizeButton().Clicked() && win != nil {
		if maximized {
			win.Unmaximize()
		} else {
			win.Maximize()
		}
	}
	if button("close", winClose, L.WindowClose()).Tooltip(L.WindowClose()).Clicked() && win != nil {
		win.Close()
	}
}

// windowMaterial is the material behind the panes: Mica on Windows 11, the
// sidebar's on macOS, none elsewhere (the view then draws the base itself).
func windowMaterial() mygo.Vibrancy {
	switch runtime.GOOS {
	case "windows":
		return mygo.VibrancyMica
	case "darwin":
		return mygo.VibrancySidebar
	}
	return mygo.VibrancyNone
}

// newViewerWindow makes an empty window of its own for one pane, shown as
// the pane comes (add).
// Side effects: a native window, kept out of capture unless Security says
// otherwise; closing it disconnects its pane.
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
	vw.win.OnClose(func(e *mygo.CloseEvent) { a.closeViewerWindow(vw, e) })
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
		vw.tabs, vw.groups, vw.cur = nil, nil, nil
		vw.mu.Unlock()
		for _, t := range tabs {
			t.disconnect()
		}
	})
	return vw
}

// watchFullScreen keeps the window's full-screen state, which shows the
// pane in focus alone, in step with the system's.
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

// groupOf is the group showing t, or nil. Notes: the caller holds vw.mu.
func (vw *viewerWindow) groupOf(t tab) *group {
	if t == nil {
		return nil
	}
	for _, g := range vw.groups {
		if p, _ := g.split.leaf(t); p != nil {
			return g
		}
	}
	return nil
}

// shown is the pane in focus in the group shown, or nil. Notes: the caller
// holds vw.mu.
func (vw *viewerWindow) shown() tab {
	if vw.cur == nil {
		return nil
	}
	return vw.cur.split.focused()
}

// homeShownLocked reports whether the main window shows Home, alone or
// beside other panes. Notes: the caller holds vw.mu.
func (vw *viewerWindow) homeShownLocked() bool {
	if !vw.main || vw.cur == nil {
		return false
	}
	for _, t := range vw.cur.shownTabs() {
		if _, ok := t.(*homeTab); ok {
			return true
		}
	}
	return false
}

// add puts a tab in this window: in the main window as an entry of its
// own, shown. Notes: call on the main thread.
func (vw *viewerWindow) add(v tab) { vw.addAt(v, nil, zoneMiddle) }

// addAt puts a tab in this window, shown where it was dropped on target's
// pane (placeLocked). Notes: call on the main thread.
func (vw *viewerWindow) addAt(v, target tab, z dropZone) {
	if vw.main {
		vw.app.mainWindow()
	}
	vw.mu.Lock()
	prev := vw.shown()
	if !slices.Contains(vw.tabs, v) {
		vw.tabs = append(vw.tabs, v)
	}
	vw.placeLocked(v, target, z)
	vw.mu.Unlock()
	if prev != nil && prev != v {
		prev.endGrab()
	}
	v.setTabWindow(vw)
	vw.retitle()
	vw.win.Show()
	vw.win.Focus()
	vw.app.syncFront()
}

// placeLocked puts t where it was dropped: on target's pane in the main
// window it joins target's group beside it, or in its place
// (split.place), the pane it replaces then getting an entry of its own;
// otherwise t shows in its own group, made when it has none. A tab leaving
// another group takes its pane along, and a group left empty goes. The
// group t lands in is shown. Notes: the caller holds vw.mu.
func (vw *viewerWindow) placeLocked(t, target tab, z dropZone) {
	to := vw.groupOf(target)
	if !vw.main || to == nil || target == t {
		if g := vw.groupOf(t); g != nil {
			g.split.show(t)
			vw.cur = g
			return
		}
		g := &group{}
		g.split.single(t)
		vw.groups = append(vw.groups, g)
		vw.cur = g
		return
	}
	if from := vw.groupOf(t); from != nil && from != to {
		vw.leaveGroupLocked(from, t)
	}
	to.split.place(t, target, z)
	to.zoom = nil
	vw.cur = to
	// Dropped in its place: target keeps an entry of its own.
	if p, _ := to.split.leaf(target); p == nil {
		g := &group{}
		g.split.single(target)
		vw.groups = append(vw.groups, g)
	}
}

// leaveGroupLocked takes t's pane out of g; g goes when it empties, and
// another entry shows when it was the one shown. Notes: the caller holds
// vw.mu.
func (vw *viewerWindow) leaveGroupLocked(g *group, t tab) {
	g.split.remove(t)
	if g.zoom == t {
		g.zoom = nil
	}
	if g.split.root != nil {
		return
	}
	i := slices.Index(vw.groups, g)
	vw.groups = slices.Delete(vw.groups, i, i+1)
	if vw.cur != g {
		return
	}
	vw.cur = nil
	if len(vw.groups) > 0 {
		vw.cur = vw.groups[max(i-1, 0)]
	}
}

// homeGroupLocked gives Home an entry of its own, first, and shows it.
// Notes: the caller holds vw.mu.
func (vw *viewerWindow) homeGroupLocked() {
	h := vw.app.home
	if !slices.Contains(vw.tabs, tab(h)) {
		vw.tabs = append(vw.tabs, h)
	}
	if g := vw.groupOf(h); g != nil {
		g.split.show(h)
		vw.cur = g
		return
	}
	g := &group{}
	g.split.single(h)
	vw.groups = slices.Insert(vw.groups, 0, g)
	vw.cur = g
}

// show brings a tab forward: its group shows with the focus on it.
// Notes: call on the main thread.
func (vw *viewerWindow) show(v tab) {
	vw.mu.Lock()
	prev := vw.shown()
	if g := vw.groupOf(v); g != nil {
		g.split.show(v)
		if g.zoom != nil && g.zoom != v {
			g.zoom = nil
		}
		vw.cur = g
	}
	vw.mu.Unlock()
	vw.shownChanged(prev)
	vw.win.Show()
	vw.win.Focus()
}

// showHome shows Home, in its group or in a group of its own.
// Notes: call on the main thread.
func (vw *viewerWindow) showHome() {
	vw.mu.Lock()
	prev := vw.shown()
	vw.homeGroupLocked()
	vw.mu.Unlock()
	vw.shownChanged(prev)
}

// showGroup shows one of the sidebar's entries. Notes: call on the main
// thread.
func (vw *viewerWindow) showGroup(g *group, focus tab) {
	vw.mu.Lock()
	prev := vw.shown()
	if slices.Contains(vw.groups, g) {
		vw.cur = g
		if focus != nil {
			g.split.show(focus)
		}
	}
	vw.mu.Unlock()
	vw.shownChanged(prev)
}

// shownChanged follows a change of the pane in focus: the one before gives
// up its keyboard grab, the window's title and the clipboard's front
// follow, and Home's status watch learns whether Home shows.
func (vw *viewerWindow) shownChanged(prev tab) {
	vw.mu.Lock()
	now := vw.shown()
	vw.mu.Unlock()
	if prev != nil && prev != now {
		prev.endGrab()
	}
	vw.retitle()
	vw.app.syncFront()
	if vw.main {
		vw.app.homeVisibilityChanged()
	}
}

// remove takes a tab out of this window. A window of its own closes when it
// has no tab left; the main window shows Home when nothing is left.
// Returns: whether the tab was here.
// Notes: call on the main thread.
func (vw *viewerWindow) remove(v tab) bool {
	vw.mu.Lock()
	i := slices.Index(vw.tabs, v)
	if i < 0 {
		vw.mu.Unlock()
		return false
	}
	vw.tabs = slices.Delete(vw.tabs, i, i+1)
	if g := vw.groupOf(v); g != nil {
		vw.leaveGroupLocked(g, v)
	}
	empty := len(vw.tabs) == 0
	if empty && !vw.main {
		// A window of its own that closes with its last pane never asks.
		vw.agreed = true
	}
	if vw.main && vw.cur == nil {
		vw.homeGroupLocked()
	}
	full := vw.full
	vw.mu.Unlock()
	v.endGrab()
	switch {
	case empty && !vw.main:
		vw.win.Close()
	case vw.main && full && v != tab(vw.app.home):
		vw.win.SetFullScreen(false)
	}
	vw.retitle()
	vw.app.syncFront()
	if vw.main {
		vw.app.homeVisibilityChanged()
	}
	return true
}

// retitle names the window after the pane in focus.
func (vw *viewerWindow) retitle() {
	if vw.win == nil {
		return
	}
	vw.mu.Lock()
	v := vw.shown()
	vw.mu.Unlock()
	if v != nil && v != tab(vw.app.home) {
		vw.win.SetTitle(L.ViewerTitle(v.tabTitle()))
	} else {
		vw.win.SetTitle("Kyokai")
	}
}

// moveTab moves a tab, by its tabKey, into another window (a new window of
// its own for nil), where it gets an entry of its own; moved to the window
// it is in, it leaves its group for an entry of its own. Home stays in the
// main window; a tab alone in a window of its own stays when it would only
// get a window of its own again.
// Notes: call on the main thread.
func (a *App) moveTab(key string, to *viewerWindow) {
	v := a.tabFor(key)
	if v == nil || v == tab(a.home) && to != a.main {
		return
	}
	from := v.tabWindow()
	if to == nil {
		if from != nil && !from.main {
			return
		}
		to = a.newViewerWindow()
	}
	if from == to {
		// Within the main window: out of its group into an entry of its own.
		to.mu.Lock()
		prev := to.shown()
		if g := to.groupOf(v); g != nil && g.split.root.first != nil {
			to.leaveGroupLocked(g, v)
			ng := &group{}
			ng.split.single(v)
			to.groups = append(to.groups, ng)
			to.cur = ng
		}
		to.mu.Unlock()
		to.shownChanged(prev)
		return
	}
	if from != nil {
		from.remove(v)
	}
	to.add(v)
}

// closeTab closes a tab, which disconnects it.
func (vw *viewerWindow) closeTab(v tab) {
	if vw.remove(v) {
		v.disconnect()
	}
}

// dropTab puts a tab, by its tabKey, where it was dropped on the pane of
// target in the main window (placeLocked), bringing it from a window of
// its own first.
// Notes: call on the main thread.
func (a *App) dropTab(key string, to *viewerWindow, target tab, z dropZone) {
	v := a.tabFor(key)
	if v == nil || !to.main {
		return
	}
	if from := v.tabWindow(); from != to {
		if from != nil {
			from.remove(v)
		}
		to.addAt(v, target, z)
		return
	}
	to.mu.Lock()
	prev := to.shown()
	to.placeLocked(v, target, z)
	to.mu.Unlock()
	to.shownChanged(prev)
}

// tabFor is the open pane with a tabKey (Home, a desktop, a service or a
// host's status), or nil.
func (a *App) tabFor(key string) tab {
	if key == homeKey {
		return a.home
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if v := a.viewers[key]; v != nil {
		return v
	}
	if s := a.services[key]; s != nil {
		return s
	}
	for _, s := range a.statuses {
		if s.tabKey() == key {
			return s
		}
	}
	return nil
}

// allTabs are every open pane but Home: desktops, services and hosts'
// status, in no order.
func (a *App) allTabs() []tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]tab, 0, len(a.viewers)+len(a.services)+len(a.statuses))
	for _, v := range a.viewers {
		out = append(out, v)
	}
	for _, s := range a.services {
		out = append(out, s)
	}
	for _, s := range a.statuses {
		out = append(out, s)
	}
	return out
}

// keysAway reports whether the keyboard focus is on what sends the keys to
// another computer — a desktop's picture, a service's screen or terminal —
// which then gets the window's shortcuts too (Ctrl+K in a shell).
func (vw *viewerWindow) keysAway(c *ui.Context) bool {
	vw.mu.Lock()
	tabs := slices.Clone(vw.tabs)
	vw.mu.Unlock()
	for _, v := range tabs {
		var h *ui.Handle
		switch v := v.(type) {
		case *viewer:
			h = &v.pic
		case *serviceTab:
			v.mu.Lock()
			if v.client != nil {
				h = v.client.keys()
			}
			v.mu.Unlock()
		}
		if h != nil && c.Focused(*h) {
			return true
		}
	}
	return false
}

// view is the window: the main window's sidebar and the group shown
// (shellView), a window of its own's one pane, or in full screen the pane
// in focus alone, without its header.
func (vw *viewerWindow) view(c *ui.Context) {
	t := c.Theme()
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(t.Surface)
	}
	vw.mu.Lock()
	tabs := slices.Clone(vw.tabs)
	var g group
	if vw.cur != nil {
		g = *vw.cur
	}
	full := vw.full
	vw.mu.Unlock()
	shown := g.shownTabs()
	switch {
	case full && g.split.focus != nil:
		ui.Column(c.Key("full")).Fill().Children(func() { g.split.focus.view(c) })
		shown = []tab{g.split.focus}
	case vw.main:
		vw.app.shellView(c, vw, g)
	case g.split.focus != nil:
		ui.Column(c).Fill().Padding(paneMargin / 2).Children(func() {
			vw.tabPane(c, paneDraw{g: g, topRight: g.split.focus}, g.split.focus)
		})
	}
	// A tab not shown takes no keys (regression: a grab stayed on behind
	// Home, and keys typed there went to the other computer).
	for _, v := range tabs {
		if !slices.Contains(shown, v) {
			v.syncGrab(false)
		}
	}
}

// paneDraw is how the panes of the group shown draw: the group, the pane
// whose header ends with the window's buttons, and whether a pane is
// dragged over them.
type paneDraw struct {
	g        group
	topRight tab
	dragging bool
}

// panesView is the group shown: its panes with their dividers, or the one
// maximized alone; while a pane is dragged over them, where it may go.
func (vw *viewerWindow) panesView(c *ui.Context, g group) {
	area := ui.Column(c.Key("panes")).Grow(1).FillWidth().MinHeight(0)
	_, over := ui.DragOver[tabDrag](area)
	vw.mu.Lock()
	dragging := vw.dragging || over
	vw.dragging = over
	vw.mu.Unlock()
	d := paneDraw{g: g, topRight: g.split.topRight(), dragging: dragging}
	if g.zoom != nil {
		d.topRight = g.zoom
	}
	area.Children(func() {
		if g.zoom != nil {
			vw.tabPane(c, d, g.zoom)
			return
		}
		vw.paneView(c, d, g.split.root, "p")
	})
}

// paneView draws one pane of the split: a tab, or its two halves with the
// divider between them (keyed by their path in the tree). The divider runs
// in the gap between floating panes, so it shows only in the accent while
// hovered or dragged: the split takes a theme whose border is transparent,
// and its halves the window's again.
func (vw *viewerWindow) paneView(c *ui.Context, d paneDraw, p *pane, path string) {
	if p == nil {
		return
	}
	if p.first == nil {
		vw.tabPane(c, d, p.tab)
		return
	}
	t := c.Theme()
	quiet := *t
	quiet.Border = ui.Transparent
	c.SetTheme(&quiet)
	first := func() { c.SetTheme(t); vw.paneView(c, d, p.first, path+"a") }
	second := func() { c.SetTheme(t); vw.paneView(c, d, p.second, path+"b") }
	if p.across {
		ui.SplitPercent(c.Key(path), &p.ratio, first, second).Fill()
	} else {
		ui.SplitVerticalPercent(c.Key(path), &p.ratio, first, second).Fill()
	}
	c.SetTheme(t)
}

// dropZones are where on a pane a dragged pane may go, in percent of the
// pane: beside it on each side (splitting it), or in its place.
var dropZones = []struct {
	zone       dropZone
	x, y, w, h float32
}{
	{zoneLeft, 0, 0, 25, 100}, {zoneRight, 75, 0, 25, 100},
	{zoneTop, 25, 0, 50, 25}, {zoneBottom, 25, 75, 50, 25}, {zoneMiddle, 25, 25, 50, 50},
}

// tabPane is a floating pane: rounded, on the window's material, its header
// and its tab's content; in a group of several, an accent edge on the pane
// in focus; while a pane is dragged over the main window's panes, where a
// drop would put it. The focus follows the keyboard into a pane.
// Notes: half the margin between panes goes round each pane, the other
// half round the panes (shellView): a split's divider is a line where its
// two sides meet, which then runs in the middle of the gap (paneView).
func (vw *viewerWindow) tabPane(c *ui.Context, d paneDraw, v tab) {
	t := c.Theme()
	key := v.tabKey()
	several := d.g.split.root != nil && d.g.split.root.first != nil && d.g.zoom == nil
	holder := ui.Column(c.Key("pane-at-" + key)).Fill().MinWidth(0).MinHeight(0).Padding(paneMargin / 2)
	var box ui.Element
	holder.Children(func() {
		box = ui.Column(c.Key("pane-"+key)).Grow(1).FillWidth().MinWidth(0).MinHeight(0).Radius(paneRadius).Clip().
			Background(t.Background).Border(1, t.Border).Shadow(0, 6, 18, 0, paneShadow)
		vw.paneBox(c, d, v, box, several)
	})
}

// paneBox fills a pane's box: the accent edge in focus, its header and its
// content, and the drop zones while a pane is dragged over it (tabPane).
func (vw *viewerWindow) paneBox(c *ui.Context, d paneDraw, v tab, box ui.Element, several bool) {
	t := c.Theme()
	key := v.tabKey()
	if several && v == d.g.split.focus {
		box.Border(2, t.Accent)
	}
	if box.FocusWithin() && v != d.g.split.focus {
		vw.mu.Lock()
		if vw.cur != nil {
			vw.cur.split.focus = v
		}
		vw.mu.Unlock()
		vw.retitle()
		vw.app.syncFront()
	}
	box.Children(func() {
		vw.paneHeader(c, d, v)
		ui.Column(c.Key("tab-" + key)).Grow(1).FillWidth().MinHeight(0).Children(func() { v.view(c) })
		if !d.dragging || !vw.main {
			return
		}
		for _, z := range dropZones {
			zone := ui.Box(c.Key("drop-" + key + "-" + strconv.Itoa(int(z.zone)))).Absolute().
				LeftPercent(z.x).TopPercent(z.y).WidthPercent(z.w).HeightPercent(z.h).Radius(8)
			if _, on := ui.DragOver[tabDrag](zone); on {
				vw.mu.Lock()
				vw.dragging = true
				vw.mu.Unlock()
				zone.Background(t.Accent.Alpha(0.28)).Border(2, t.Accent)
			}
			if dr, ok := ui.Drop[tabDrag](zone); ok {
				vw.app.dropTab(dr.key, vw, v, z.zone)
			}
		}
	})
}

// paneHeader is a pane's header: the kind's icon, the device's name and
// the second line, which drag the pane (into another place, the sidebar,
// or out into a window of its own); an empty part that moves the window;
// then, in the main window, split right and split down (beside the pane:
// another open pane) and, in a group of several, maximize or restore; and
// close, except on Home alone. The top-right pane's header ends with the
// window's buttons on Windows (unseen while Settings covers it, which shows
// them in their place); a window of its own's keeps the room of the
// traffic lights on macOS.
func (vw *viewerWindow) paneHeader(c *ui.Context, d paneDraw, v tab) {
	t := c.Theme()
	key := v.tabKey()
	h := v.head()
	left := float32(14)
	if !vw.main {
		left = max(c.TitleBar().Left-paneMargin+8, 14)
	}
	several := d.g.split.root != nil && d.g.split.root.first != nil
	ui.Row(c.Key("head-"+key)).Height(paneHeaderH).Shrink(0).Padding(0, 8, 0, left).Gap(4).AlignItems(ui.Center).Children(func() {
		title := ui.Row(c.Key("head-title-" + key)).Gap(8).AlignItems(ui.Center).MinWidth(0).Shrink(1).Label(h.title)
		if v == tab(vw.app.home) {
			title.Drag(tabDrag{key})
		} else {
			title.Drag(tabDrag{key}).DragData(transfer.TextData(v.tabTitle()), transfer.DragOptions{
				// Dropped where no Kyokai window took it: a window of its own;
				// a drag given up with Escape leaves the pane where it is.
				Done: func(r transfer.Result) {
					if r.Operation == transfer.None && !r.Escaped {
						vw.app.moveTab(key, nil)
					}
				},
			})
		}
		title.Children(func() {
			ui.Icon(c, h.icon).FontSize(navIconSize).TextColor(t.TextMuted)
			ui.Text(c, h.title).FontSize(sizeBody).Bold().SingleLine().MinWidth(0)
			if h.sub != "" {
				ui.Text(c, h.sub).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().MinWidth(0)
			}
		})
		ui.Box(c.Key("head-drag-" + key)).Grow(1).FillHeight().DragWindow()
		if vw.main {
			vw.splitButton(c, v, zoneRight)
			vw.splitButton(c, v, zoneBottom)
			if several {
				icon, label := paneMaximize, L.PaneMaximize()
				if d.g.zoom == v {
					icon, label = paneRestore, L.PaneRestore()
				}
				if iconButton(c, "head-zoom-"+key, icon, label) {
					vw.mu.Lock()
					if vw.cur != nil {
						if vw.cur.zoom == v {
							vw.cur.zoom = nil
						} else {
							vw.cur.zoom = v
							vw.cur.split.focus = v
						}
					}
					vw.mu.Unlock()
				}
			}
		}
		// Home alone has nothing to close to: its sidebar row stays.
		if (v != tab(vw.app.home) || several) && iconButton(c, "head-close-"+key, paneClose, L.PaneClose()) {
			vw.closeTab(v)
		}
		if v == d.topRight {
			// Under the Settings sheet they keep their room, unseen: the
			// sheet shows them in this very place, above the dimming.
			vw.app.mu.Lock()
			shown := float32(1)
			if vw.main && vw.app.settingsOpen {
				shown = 0
			}
			vw.app.mu.Unlock()
			ui.Row(c.Key("head-win")).Gap(4).AlignItems(ui.Center).Opacity(shown).Children(func() {
				windowButtons(c, "head-win", vw.win, false)
			})
		}
	})
}

// splitButton is a header's split right or split down: the + picker,
// beside this pane on that side; with other panes open, first a menu of
// them, any of which then goes beside this one (dropTab), ending with Open
// a device… for the picker.
func (vw *viewerWindow) splitButton(c *ui.Context, v tab, z dropZone) {
	t := c.Theme()
	icon, label := paneSplitRight, L.PaneSplitRight()
	if z == zoneBottom {
		icon, label = paneSplitDown, L.PaneSplitDown()
	}
	id := strconv.Itoa(int(z)) + "-" + v.tabKey()
	b := iconButtonBase(c, "head-split-"+id, icon, label)
	vw.mu.Lock()
	open := vw.menu == id
	g := vw.groupOf(v)
	var here []tab
	if g != nil {
		here = g.split.shownTabs()
	}
	vw.mu.Unlock()
	var others []tab
	for _, o := range append([]tab{vw.app.home}, vw.app.allTabs()...) {
		if !slices.Contains(here, o) {
			others = append(others, o)
		}
	}
	slices.SortFunc(others, func(x, y tab) int { return strings.Compare(x.tabTitle(), y.tabTitle()) })
	pick := func() { vw.app.openPicker("split-"+id, true, v.tabKey(), z) }
	if b.Clicked() {
		if len(others) == 0 {
			pick()
		} else {
			open = !open
		}
	}
	row := func(key string, icon *ui.SVG, title, kind string) bool {
		r := ui.ButtonBase(c.Key(key)).Padding(6, 8).Radius(8).FillWidth().Label(title)
		if r.Hovered() {
			r.Background(t.SurfaceHover)
		}
		r.Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon).FontSize(navIconSize).TextColor(t.TextMuted)
				ui.Text(c, title).SingleLine()
				if kind != "" {
					ui.Text(c, kind).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
				}
			})
		})
		return r.Clicked()
	}
	if open {
		ui.Popover(c, b, &open, func() {
			ui.Column(c).Width(splitMenuW).Padding(6).Gap(2).Children(func() {
				for _, o := range others {
					oh := o.head()
					if row("split-to-"+id+"-"+o.tabKey(), oh.icon, oh.title, oh.kind) {
						open = false
						vw.app.dropTab(o.tabKey(), vw, v, z)
					}
				}
				ui.Divider(c)
				if row("split-new-"+id, shellPlus, L.PaneSplitNew(), "") {
					open = false
					pick()
				}
			})
		})
	}
	vw.mu.Lock()
	switch {
	case open:
		vw.menu = id
	case vw.menu == id:
		vw.menu = ""
	}
	vw.mu.Unlock()
	vw.app.pickerView(c, "split-"+id, b)
}

// iconButton is a borderless icon button of a pane's header or the
// sidebar, named for assistive technology by its tooltip.
// Returns: whether it was clicked.
func iconButton(c *ui.Context, key string, icon *ui.SVG, label string) bool {
	return iconButtonBase(c, key, icon, label).Clicked()
}

// iconButtonBase is iconButton's element, for a menu anchored to it.
func iconButtonBase(c *ui.Context, key string, icon *ui.SVG, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(paneButton, paneButton).Radius(7).Center().Shrink(0).Label(label).Tooltip(label)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() { ui.Icon(c, icon).FontSize(navIconSize).TextColor(t.TextMuted) })
	return b
}

// disconnect ends the viewer's connection, as closing its pane does.
func (v *viewer) disconnect() {
	// Closed first, then the session read: a connect answered meanwhile
	// sees the tab closed and closes its session itself (redial).
	v.close()
	id := v.session()
	// Noted here, on the main thread, before any status that still lists
	// it as connected reaches syncViewers.
	v.app.dropSession(id)
	go v.app.closeSession(id)
}

// tabKey is the viewer's tab's identity: the Device ID.
func (v *viewer) tabKey() string { return v.device }

// tabTitle names the viewer's tab: the device's name and Device ID.
func (v *viewer) tabTitle() string { return v.title }

// head is the viewer's pane header: the desktop's icon, the device's name
// with its Device ID beside it.
func (v *viewer) head() paneHead {
	title, sub := splitTitle(v.title)
	return paneHead{icon: kindDesktop, title: title, sub: sub, kind: L.PaneDesktop()}
}

// splitTitle parts a device's title (deviceTitle) into its name and its
// Device ID; a device without a name has its Device ID alone.
func splitTitle(s string) (name, id string) {
	if name, id, ok := strings.Cut(s, " · "); ok {
		return name, id
	}
	return s, ""
}

// tabWindow is the window the viewer's tab is in, nil once it closed.
func (v *viewer) tabWindow() *viewerWindow { return v.home.Load() }

// setTabWindow records the window the viewer's tab moved to.
func (v *viewer) setTabWindow(w *viewerWindow) { v.home.Store(w) }
