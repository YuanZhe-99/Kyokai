package gui

import (
	"slices"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// viewerWindow holds viewers as tabs, one per session, as a browser holds
// pages: a tab moves to another such window when dropped on its tabs, and
// into a window of its own when dropped outside Kyokai or with "Open in new
// window". Fields after mu are guarded by it and change on the main thread.
type viewerWindow struct {
	app *App
	win *mygo.Window

	mu   sync.Mutex
	tabs []*viewer
	cur  int
}

// tabDrag is what a dragged tab carries: its session.
type tabDrag struct{ session uint64 }

// newViewerWindow opens an empty window for viewer tabs.
// Side effects: a native window, kept out of capture unless Settings says
// otherwise; closing it ends the streams of its tabs (not their sessions).
func (a *App) newViewerWindow() *viewerWindow {
	vw := &viewerWindow{app: a}
	vw.win = mygo.NewWindow(mygo.WindowOptions{
		Title: "Kyokai", Width: 1280, Height: 800, MinWidth: 480, MinHeight: 320,
		Content: ui.View(vw.view),
	})
	a.mu.Lock()
	show := a.prefs.ShowInCapture
	a.viewerWins = append(a.viewerWins, vw)
	a.mu.Unlock()
	vw.win.SetContentProtection(!show)
	vw.win.OnClosed(func() {
		a.mu.Lock()
		a.viewerWins = slices.DeleteFunc(a.viewerWins, func(w *viewerWindow) bool { return w == vw })
		a.mu.Unlock()
		vw.mu.Lock()
		tabs := vw.tabs
		vw.tabs = nil
		vw.mu.Unlock()
		for _, v := range tabs {
			v.close()
		}
	})
	return vw
}

// add puts a viewer in this window as the tab shown.
// Notes: call on the main thread.
func (vw *viewerWindow) add(v *viewer) {
	vw.mu.Lock()
	vw.tabs = append(vw.tabs, v)
	prev := vw.shown()
	vw.cur = len(vw.tabs) - 1
	vw.mu.Unlock()
	if prev != nil && prev != v {
		prev.endGrab()
	}
	v.home.Store(vw)
	vw.retitle()
	vw.win.Focus()
}

// remove takes a viewer's tab out of this window, which closes when it
// has no tab left. Returns: whether the viewer was here.
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
	vw.mu.Unlock()
	v.endGrab()
	if empty {
		vw.win.Close()
	} else {
		vw.retitle()
	}
	return true
}

// shown is the viewer of the tab shown, or nil. Notes: the caller holds
// vw.mu.
func (vw *viewerWindow) shown() *viewer {
	if vw.cur < len(vw.tabs) {
		return vw.tabs[vw.cur]
	}
	return nil
}

// retitle names the window after the tab shown.
func (vw *viewerWindow) retitle() {
	vw.mu.Lock()
	v := vw.shown()
	vw.mu.Unlock()
	if v != nil {
		vw.win.SetTitle(L.ViewerTitle(v.title))
	}
}

// moveTab moves a session's tab into another window (a new one for nil).
// Notes: call on the main thread; a tab alone in its window stays when it
// would only get a window of its own again.
func (a *App) moveTab(session uint64, to *viewerWindow) {
	a.mu.Lock()
	v := a.viewers[session]
	a.mu.Unlock()
	if v == nil {
		return
	}
	from := v.home.Load()
	if from == to {
		return
	}
	if to == nil {
		from.mu.Lock()
		alone := len(from.tabs) == 1
		from.mu.Unlock()
		if alone {
			return
		}
		to = a.newViewerWindow()
	}
	if from != nil {
		from.remove(v)
	}
	to.add(v)
}

// closeTab ends a tab's stream (not its session).
func (vw *viewerWindow) closeTab(v *viewer) {
	if vw.remove(v) {
		v.close()
	}
}

// view is the window: the tabs, then the viewer of the tab shown.
func (vw *viewerWindow) view(c *ui.Context) {
	vw.mu.Lock()
	tabs := slices.Clone(vw.tabs)
	cur := vw.cur
	vw.mu.Unlock()
	ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
		vw.tabBar(c, tabs, cur)
		if cur < len(tabs) {
			v := tabs[cur]
			ui.Column(c.Key("tab-" + itoa(v.id))).Grow(1).FillWidth().Children(func() { v.view(c) })
		}
	})
}

// tabBar is the row of tabs: a click shows one; each has Open in new
// window (with more than one tab) and Close; dragging one moves it.
func (vw *viewerWindow) tabBar(c *ui.Context, tabs []*viewer, cur int) {
	t := c.Theme()
	sel := cur
	bar := ui.TabsBase(c.Key("tabs"), &sel, len(tabs))
	bar.List.FillWidth().Padding(6, 8).Gap(4).Background(t.Background)
	if _, over := ui.DragOver[tabDrag](bar.List); over {
		bar.List.Background(t.Accent.Alpha(0.14))
	}
	if d, ok := ui.Drop[tabDrag](bar.List); ok {
		vw.app.moveTab(d.session, vw)
	}
	bar.List.Children(func() {
		for i, v := range tabs {
			tab := bar.Tab(i).Padding(4, 10).Radius(6)
			if i == cur {
				tab.Background(t.Surface)
			}
			session := v.id
			tab.Drag(tabDrag{session}).DragData(transfer.TextData(v.title), transfer.DragOptions{
				// Dropped where no Kyokai window took it: a window of its own.
				Done: func(r transfer.Result) {
					if r.Operation == transfer.None {
						vw.app.moveTab(session, nil)
					}
				},
			})
			tab.Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, v.title).FontSize(sizeSmall).NoWrap().Ellipsis("…").MaxWidth(tabTitleMaxW)
					if len(tabs) > 1 && ui.Button(c.Key("tab-out-"+itoa(session)), "⧉").Tooltip(L.ViewerTabOut()).Clicked() {
						vw.app.moveTab(session, nil)
					}
					if ui.Button(c.Key("tab-close-"+itoa(session)), "×").Tooltip(L.ViewerTabClose()).Clicked() {
						vw.closeTab(v)
					}
				})
			})
		}
	})
	if sel != cur && sel < len(tabs) && cur < len(tabs) {
		tabs[cur].endGrab()
		vw.mu.Lock()
		vw.cur = sel
		vw.mu.Unlock()
		vw.retitle()
	}
}
