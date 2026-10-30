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
	// area is the panes' room as the last frame laid it out; need is the
	// main window's minimum for what it shows, as last applied (fitWindow).
	area, need size
	// pointerIn is the pane under the pointer as the frame being built
	// found it (nil for none): a desktop's or a screen's keyboard grab
	// holds while the pointer is anywhere in its pane.
	pointerIn tab
	// focusGen counts the times the window became active: a grab taken
	// before the last one is renewed (viewer.syncGrab).
	focusGen uint64
}

// pointerOver reports whether the pointer is in t's pane in the frame
// being built (pointerIn).
func (vw *viewerWindow) pointerOver(t tab) bool {
	vw.mu.Lock()
	defer vw.mu.Unlock()
	return vw.pointerIn == t
}

// activeGen is how many times the window became active (focusGen).
func (vw *viewerWindow) activeGen() uint64 {
	vw.mu.Lock()
	defer vw.mu.Unlock()
	return vw.focusGen
}

// tabDrag is what a dragged pane carries: its tabKey.
type tabDrag struct{ key string }

// TitleBarOptions hides a window's title bar: on macOS its traffic lights
// sit at the top of the sidebar (or of a pane's header in a window of its
// own), centred on the top pane's header; on Windows the view draws
// minimize, maximize and close itself, at the end of the top-right pane's
// header (windowButtons). The window keeps its resizing edges, shadow and
// corners; its material shows around the panes. lightsX is how far from
// the window's edge the traffic lights start (trafficMainX, trafficOwnX):
// MyGo places them only as the window opens.
func TitleBarOptions(o mygo.WindowOptions, lightsX int) mygo.WindowOptions {
	o.TitleBarStyle = mygo.TitleBarHidden
	o.HideWindowButtons = ownWindowButtons
	o.TrafficLightPosition = &mygo.Point{X: lightsX, Y: trafficY}
	o.Vibrancy = windowMaterial()
	return o
}

// ownWindowButtons tells whether the view draws its windows' buttons
// (windowButtons): on Windows and Linux, where the system's would sit on
// the window's corner, over a pane's own controls, rather than on a pane;
// macOS keeps its traffic lights.
var ownWindowButtons = runtime.GOOS == "windows" || runtime.GOOS == "linux"

// windowButtons are the window's minimize, maximize or restore, and close
// on Windows and Linux (ownWindowButtons), after a separator at the end of the
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
	}, trafficOwnX))
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
// group t lands in is shown. Beside a pane without room for it on that
// side t goes on the other (sideLocked), or gets an entry of its own.
// Notes: the caller holds vw.mu.
func (vw *viewerWindow) placeLocked(t, target tab, z dropZone) {
	to := vw.groupOf(target)
	if to != nil && z != zoneMiddle {
		var ok bool
		if z, ok = vw.sideLocked(to, target, z); !ok {
			to = nil
		}
	}
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

// sideLocked is the side of target's pane in g where another goes: z when
// the pane has room to split there (fits), else the other way when it has
// room for that; false when it has neither. While the panes' room is not
// known yet, z. Notes: the caller holds vw.mu.
func (vw *viewerWindow) sideLocked(g *group, target tab, z dropZone) (dropZone, bool) {
	room, ok := g.split.roomOf(target, vw.area)
	if vw.area.w <= 0 || !ok || fits(room, z) {
		return z, true
	}
	other := zoneBottom
	if z == zoneTop || z == zoneBottom {
		other = zoneRight
	}
	return other, fits(room, other)
}

// comeApartLocked gives each pane of g an entry of its own in g's place,
// the pane in focus shown when g was. Notes: the caller holds vw.mu.
func (vw *viewerWindow) comeApartLocked(g *group) {
	i := slices.Index(vw.groups, g)
	if i < 0 {
		return
	}
	var apart []*group
	for _, t := range g.split.shownTabs() {
		n := &group{}
		n.split.single(t)
		apart = append(apart, n)
		if vw.cur == g && t == g.split.focus {
			vw.cur = n
		}
	}
	vw.groups = slices.Replace(vw.groups, i, i+1, apart...)
}

// windowNeed is the main window's content minimum while it shows g beside
// a sidebar side wide: the panes' least room (one pane's while one is
// maximized) and the margins round them, never less than mainMinW ×
// mainMinH.
func windowNeed(g group, side float32) size {
	m := g.split.root.minRoom()
	if g.zoom != nil {
		m = paneRoom
	}
	return size{max(mainMinW, side+paneMargin/2+m.w), max(mainMinH, paneMargin+m.h)}
}

// needNow is the main window's content minimum for what it shows now
// (windowNeed): mainMinW × mainMinH in protected mode, in full screen and
// with nothing shown.
func (vw *viewerWindow) needNow() size {
	a := vw.app
	a.mu.Lock()
	side, protected := shellSidebarW, a.protected()
	if a.prefs.SidebarRail {
		side = railWidth()
	}
	a.mu.Unlock()
	vw.mu.Lock()
	defer vw.mu.Unlock()
	if protected || vw.full || vw.cur == nil {
		return size{mainMinW, mainMinH}
	}
	return windowNeed(*vw.cur, side)
}

// fitLater has fitWindow run after this frame when what the main window
// shows needs another minimum.
func (vw *viewerWindow) fitLater() {
	need := vw.needNow()
	vw.mu.Lock()
	changed := need != vw.need
	vw.mu.Unlock()
	if changed && vw.win != nil {
		vw.win.Update(vw.fitWindow)
	}
}

// fitWindow makes the main window hold the group it shows (fitFor): its
// minimum follows the group, it grows on its display while smaller, and a
// group of several panes its display cannot hold comes apart, which a toast
// says.
// Side effects: the window's minimum size and bounds; the sidebar's
// entries; the toast.
// Notes: call on the main thread, after a move, a resize, a change of the
// displays and of what the window shows (fitLater).
func (vw *viewerWindow) fitWindow() {
	b, cb := vw.win.Bounds(), vw.win.ContentBounds()
	work := mygo.Screen.DisplayMatching(b).WorkArea
	// MyGo's minimum is the frame's: the content's and the frame round it.
	frame := size{float32(b.Width - cb.Width), float32(b.Height - cb.Height)}
	room := size{float32(work.Width) - frame.w, float32(work.Height) - frame.h}
	win := size{float32(cb.Width), float32(cb.Height)}
	need := vw.needNow()
	f := fitFor(need, win, room)
	if f == fitApart {
		vw.mu.Lock()
		g := vw.cur
		var shown []tab
		if g != nil {
			shown = g.split.shownTabs()
		}
		if len(shown) > 1 {
			vw.comeApartLocked(g)
		}
		vw.mu.Unlock()
		if len(shown) > 1 {
			vw.app.update(func() { vw.app.setToast(L.SplitCameApart(groupName(shown), len(shown)), false) })
			need = vw.needNow()
			f = fitFor(need, win, room)
		}
	}
	vw.mu.Lock()
	vw.need = need
	vw.mu.Unlock()
	// A display smaller than even one pane needs keeps the window on it.
	w := int(min(need.w, room.w) + frame.w + 0.999)
	h := int(min(need.h, room.h) + frame.h + 0.999)
	vw.win.SetMinimumSize(w, h)
	if f != fitGrow || vw.win.IsMaximized() {
		return
	}
	nb := b
	nb.Width, nb.Height = max(b.Width, w), max(b.Height, h)
	nb.X = max(work.X, min(b.X, work.X+work.Width-nb.Width))
	nb.Y = max(work.Y, min(b.Y, work.Y+work.Height-nb.Height))
	vw.win.SetBounds(nb)
}

// watchRoom has the main window fit the group it shows (fitWindow) as it
// moves, resizes and the displays change.
// Side effects: listeners on the window and the displays, the latter
// removed as the window closes.
func (vw *viewerWindow) watchRoom() {
	vw.win.OnMove(vw.fitWindow)
	vw.win.OnResize(vw.fitWindow)
	off := mygo.Screen.OnDisplaysChanged(vw.fitWindow)
	vw.win.OnClosed(off)
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

// tabFor is the open pane with a tabKey (Home, a desktop, a service, a
// host's status or an AI session), or nil.
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
	if t := a.aiTabs[key]; t != nil {
		return t
	}
	return nil
}

// allTabs are every open pane but Home: desktops, services, hosts' status
// and AI sessions, in no order.
func (a *App) allTabs() []tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]tab, 0, len(a.viewers)+len(a.services)+len(a.statuses)+len(a.aiTabs))
	for _, v := range a.viewers {
		out = append(out, v)
	}
	for _, s := range a.services {
		out = append(out, s)
	}
	for _, s := range a.statuses {
		out = append(out, s)
	}
	for _, t := range a.aiTabs {
		out = append(out, t)
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
// in focus alone, without its header — in the theme with readable muted
// text (readableTheme).
func (vw *viewerWindow) view(c *ui.Context) {
	c.SetTheme(readableTheme(c.Theme()))
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
	vw.pointerIn = nil // the panes drawn below say which is under the pointer
	vw.mu.Unlock()
	shown := g.shownTabs()
	switch {
	case full && g.split.focus != nil:
		alone := ui.Column(c.Key("full")).Fill()
		if alone.Hovered() {
			vw.mu.Lock()
			vw.pointerIn = g.split.focus
			vw.mu.Unlock()
		}
		alone.Children(func() { g.split.focus.view(c) })
		shown = []tab{g.split.focus}
	case vw.main:
		vw.app.shellView(c, vw, g)
		vw.fitLater()
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
// whose header ends with the window's buttons, whether a pane is dragged
// over them, and their room as the last frame laid it out.
type paneDraw struct {
	g        group
	topRight tab
	dragging bool
	area     size
}

// splits reports whether v's pane has room to split side by side and above
// and below (fits), as its group lays it out with none maximized; both
// while the panes' room is not known yet.
func (d paneDraw) splits(v tab) (across, down bool) {
	room, ok := d.g.split.roomOf(v, d.area)
	if d.area.w <= 0 || !ok {
		return true, true
	}
	return fits(room, zoneRight), fits(room, zoneBottom)
}

// panesView is the group shown: its panes with their dividers, or the one
// maximized alone; while a pane is dragged over them, where it may go.
func (vw *viewerWindow) panesView(c *ui.Context, g group) {
	area := ui.Column(c.Key("panes")).Grow(1).FillWidth().MinHeight(0)
	_, over := ui.DragOver[tabDrag](area)
	r := area.LayoutBounds()
	vw.mu.Lock()
	dragging := vw.dragging || over
	vw.dragging = over
	vw.area = size{r.W, r.H}
	vw.mu.Unlock()
	d := paneDraw{g: g, topRight: g.split.topRight(), dragging: dragging, area: size{r.W, r.H}}
	if g.zoom != nil {
		d.topRight = g.zoom
	}
	area.Children(func() {
		if g.zoom != nil {
			vw.tabPane(c, d, g.zoom)
			return
		}
		vw.paneView(c, d, g.split.root, "p", d.area)
	})
}

// paneView draws one pane of the split in its room: a tab, or its two
// halves with the divider between them (keyed by their path in the tree),
// which stops where either half would go below its least room (keepRoom).
// The divider runs
// in the gap between floating panes, so it shows only in the accent while
// hovered or dragged: the split takes a theme whose border is transparent,
// and its halves the window's again.
func (vw *viewerWindow) paneView(c *ui.Context, d paneDraw, p *pane, path string, room size) {
	if p == nil {
		return
	}
	if p.first == nil {
		vw.tabPane(c, d, p.tab)
		return
	}
	am, bm := p.first.minRoom(), p.second.minRoom()
	if p.across {
		p.ratio = keepRoom(p.ratio, room.w, am.w, bm.w)
	} else {
		p.ratio = keepRoom(p.ratio, room.h, am.h, bm.h)
	}
	ra, rb := p.halves(room)
	t := c.Theme()
	quiet := *t
	quiet.Border = ui.Transparent
	c.SetTheme(&quiet)
	first := func() { c.SetTheme(t); vw.paneView(c, d, p.first, path+"a", ra) }
	second := func() { c.SetTheme(t); vw.paneView(c, d, p.second, path+"b", rb) }
	if p.across {
		ui.SplitPercent(c.Key(path), &p.ratio, first, second).Fill()
	} else {
		ui.SplitVerticalPercent(c.Key(path), &p.ratio, first, second).Fill()
	}
	c.SetTheme(t)
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
// content, and while a pane is dragged over it the drop zones that fit
// (dropZonesFor), each named, the middle also saying which way it has no
// room.
func (vw *viewerWindow) paneBox(c *ui.Context, d paneDraw, v tab, box ui.Element, several bool) {
	t := c.Theme()
	key := v.tabKey()
	if box.Hovered() {
		vw.mu.Lock()
		vw.pointerIn = v
		vw.mu.Unlock()
	}
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
		across, down := d.splits(v)
		note := noRoomNote(across, down)
		for _, z := range dropZonesFor(across, down) {
			zone := ui.Box(c.Key("drop-" + key + "-" + strconv.Itoa(int(z.zone)))).Absolute().
				LeftPercent(z.x).TopPercent(z.y).WidthPercent(z.w).HeightPercent(z.h).Radius(8).Center()
			fg := t.TextMuted
			if _, on := ui.DragOver[tabDrag](zone); on {
				vw.mu.Lock()
				vw.dragging = true
				vw.mu.Unlock()
				zone.Background(t.Accent.Alpha(0.28)).Border(2, t.Accent)
				fg = t.Accent
			}
			// Each name in a pill of its own, readable over any picture; the
			// middle's says which way there is no room.
			zone.Children(func() {
				ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, zoneName(z.zone)).FontSize(sizeSmall).Bold().TextColor(fg).SingleLine().
						Padding(4, 10).Radius(12).Background(t.Background).Border(1, t.Border)
					if z.zone == zoneMiddle && note != "" {
						ui.Text(c, note).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().
							Padding(4, 10).Radius(12).Background(t.Background).Border(1, t.Border)
					}
				})
			})
			if dr, ok := ui.Drop[tabDrag](zone); ok {
				vw.app.dropTab(dr.key, vw, v, z.zone)
			}
		}
	})
}

// zoneName names a drop zone where it shows on a pane.
func zoneName(z dropZone) string {
	switch z {
	case zoneLeft:
		return L.DropLeft()
	case zoneRight:
		return L.DropRight()
	case zoneTop:
		return L.DropAbove()
	case zoneBottom:
		return L.DropBelow()
	}
	return L.DropHere()
}

// noRoomNote says which way a pane has no room to split, or "".
func noRoomNote(across, down bool) string {
	switch {
	case !across:
		return L.PaneNoRoomAcross()
	case !down:
		return L.PaneNoRoomDown()
	}
	return ""
}

// noRoomHint says why a split button is greyed out: no room that way, and
// what makes it.
func noRoomHint(z dropZone) string {
	if z == zoneBottom {
		return L.PaneNoRoomDownHint()
	}
	return L.PaneNoRoomAcrossHint()
}

// paneHeader is a pane's header: the kind's icon, the device's name and
// the second line, which drag the pane (into another place, the sidebar,
// or out into a window of its own) — on an AI session the icon in the
// accent, its title as the second line (typed in place while renamed),
// Rename and a pill while its task waits or is paused; an empty part that
// moves the window; a host's AI session's Watch its screen; then, in the
// main window, split right and split down (beside the pane: another open
// pane or the + picker), greyed out with the reason while the pane has no
// room for it, and, in a group of several, maximize or restore;
// and close, except on Home alone. The top-right pane's header ends with
// the window's buttons on Windows (unseen while Settings covers it, which
// shows them in their place); a window of its own's keeps the room of the
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
	head := ui.Row(c.Key("head-"+key)).Height(paneHeaderH).Shrink(0).Padding(0, 8, 0, left).Gap(4).AlignItems(ui.Center)
	// A narrow pane folds split right, split down and maximize into ⋯.
	w := head.LayoutBounds().W
	narrow := w > 0 && w < paneNarrowW
	head.Children(func() {
		// Too narrow even for the name and the pill, it is cut at its end
		// rather than drawn under the buttons.
		title := ui.Row(c.Key("head-title-" + key)).FillHeight().Gap(8).AlignItems(ui.Center).MinWidth(0).Shrink(1).Clip().Label(h.title)
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
		ai, _ := v.(*aiTab)
		title.Children(func() {
			icon := ui.Icon(c, h.icon).FontSize(navIconSize).TextColor(t.TextMuted)
			if ai != nil {
				icon.TextColor(t.Accent)
			}
			// Room running short, the second line gives way first — it only
			// grows into what is left, so the name loses nothing to it —
			// then the name; the pill never does. Each is cut in a box of its
			// own: a text left no room would still draw its ellipsis.
			ui.Box(c).MinWidth(0).MaxWidth(paneNameMaxW).Clip().Children(func() {
				ui.Text(c, h.title).FontSize(sizeBody).Bold().SingleLine()
			})
			switch {
			case ai != nil && ai.renaming != nil:
				ai.renameField(c)
			case h.sub != "":
				ui.Box(c).MinWidth(0).Basis(0).Grow(1).Clip().Children(func() {
					ui.Text(c, h.sub).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
				})
			}
			if ai != nil {
				ai.titleExtras(c)
			}
		})
		ui.Box(c.Key("head-drag-" + key)).Grow(1).FillHeight().DragWindow()
		if ai != nil {
			ai.headButtons(c, narrow)
		}
		switch {
		case vw.main && narrow:
			vw.foldedButtons(c, d, v, several)
		case vw.main:
			across, down := d.splits(v)
			vw.splitButton(c, v, zoneRight, across)
			vw.splitButton(c, v, zoneBottom, down)
			if several {
				icon, label := paneMaximize, L.PaneMaximize()
				if d.g.zoom == v {
					icon, label = paneRestore, L.PaneRestore()
				}
				if iconButton(c, "head-zoom-"+key, icon, label) {
					vw.toggleZoom(v)
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

// toggleZoom maximizes a pane over its group, or restores the group.
// Notes: call on the main thread.
func (vw *viewerWindow) toggleZoom(v tab) {
	vw.mu.Lock()
	defer vw.mu.Unlock()
	if vw.cur == nil {
		return
	}
	if vw.cur.zoom == v {
		vw.cur.zoom = nil
		return
	}
	vw.cur.zoom = v
	vw.cur.split.focus = v
}

// othersFor are the open panes that may go beside a pane: Home and every
// other not shown in its group, by name.
func (vw *viewerWindow) othersFor(v tab) []tab {
	vw.mu.Lock()
	var here []tab
	if g := vw.groupOf(v); g != nil {
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
	return others
}

// foldedButtons are a narrow pane's split right, split down and maximize
// or restore, folded into ⋯: each split lists the other open panes, then
// Open a device… (the + picker beside this pane on that side); a split the
// pane has no room for is a greyed-out item saying why.
func (vw *viewerWindow) foldedButtons(c *ui.Context, d paneDraw, v tab, several bool) {
	key := v.tabKey()
	b := iconButtonBase(c, "head-more-"+key, rowMore, L.ActionMore())
	others := vw.othersFor(v)
	across, down := d.splits(v)
	// The menu takes the keyboard while it is open (App.menuOpened).
	chosen := vw.app.menuClosed
	b.Menu(paneMenu(vw.app, func(m *ui.Menu) {
		for _, z := range []dropZone{zoneRight, zoneBottom} {
			label, room := L.PaneSplitRight(), across
			if z == zoneBottom {
				label, room = L.PaneSplitDown(), down
			}
			if !room {
				m.Item(label + " — " + noRoomHint(z)).Disabled(true)
				continue
			}
			m.Submenu(label, func(m *ui.Menu) {
				for _, o := range others {
					oh := o.head()
					name := oh.title
					if oh.kind != "" {
						name += " · " + oh.kind
					}
					if m.Item(name).Chosen() {
						chosen()
						vw.app.dropTab(o.tabKey(), vw, v, z)
					}
				}
				if len(others) > 0 {
					m.Separator()
				}
				if m.Item(L.PaneSplitNew()).Chosen() {
					chosen()
					vw.app.openPicker("fold-"+key, true, key, z)
				}
			})
		}
		if several {
			label := L.PaneMaximize()
			if d.g.zoom == v {
				label = L.PaneRestore()
			}
			if m.Item(label).Chosen() {
				chosen()
				vw.toggleZoom(v)
			}
		}
	}))
	vw.app.pickerView(c, "fold-"+key, b)
}

// splitButton is a header's split right or split down: the + picker,
// beside this pane on that side; with other panes open, first a menu of
// them, any of which then goes beside this one (dropTab), ending with Open
// a device… for the picker. Without room (fits) it is greyed out, its
// tooltip saying why.
func (vw *viewerWindow) splitButton(c *ui.Context, v tab, z dropZone, room bool) {
	t := c.Theme()
	icon, label := paneSplitRight, L.PaneSplitRight()
	if z == zoneBottom {
		icon, label = paneSplitDown, L.PaneSplitDown()
	}
	id := strconv.Itoa(int(z)) + "-" + v.tabKey()
	b := iconButtonBase(c, "head-split-"+id, icon, label)
	if !room {
		b.Disabled(true).Opacity(0.4).Tooltip(noRoomHint(z))
	}
	vw.mu.Lock()
	open := vw.menu == id && room
	vw.mu.Unlock()
	others := vw.othersFor(v)
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
	// The list takes the keyboard while it is open (App.menuOpened).
	vw.mu.Lock()
	was := vw.menu == id
	switch {
	case open:
		vw.menu = id
	case was:
		vw.menu = ""
	}
	vw.mu.Unlock()
	switch {
	case open && !was:
		vw.app.menuOpened()
	case was && !open:
		vw.app.menuClosed()
	}
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

// tabKey is the viewer's tab's identity: the Device ID, or the device's
// NanoKVM key (serviceKey).
func (v *viewer) tabKey() string {
	if v.kvm {
		return serviceKey(v.device, serviceNanokvm)
	}
	return v.device
}

// tabTitle names the viewer's tab: the device's name and Device ID.
func (v *viewer) tabTitle() string { return v.title }

// head is the viewer's pane header: the desktop's icon, the device's name
// with its Device ID beside it.
func (v *viewer) head() paneHead {
	title, sub := splitTitle(v.title)
	if v.kvm {
		return paneHead{icon: kindNanokvm, title: title, sub: sub, kind: L.ServiceNanokvm()}
	}
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
