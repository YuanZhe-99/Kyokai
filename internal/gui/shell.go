package gui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// homeKey is Home's tabKey.
const homeKey = "home"

// homeTab is Home as a pane of the main window (doc/en-us/gui.md,
// "Windows"): This device, connecting to another, and the recent devices
// (homeView). It is no connection: it never goes live, and closing its pane
// only takes it out of view until the sidebar's Home shows it again.
type homeTab struct{ app *App }

// tabKey is homeKey.
func (h *homeTab) tabKey() string { return homeKey }

// tabTitle is "Home".
func (h *homeTab) tabTitle() string { return L.NavHome() }

// head is Home's icon and name.
func (h *homeTab) head() paneHead { return paneHead{icon: navHome, title: L.NavHome()} }

// tabWindow is the main window, which Home never leaves.
func (h *homeTab) tabWindow() *viewerWindow { return h.app.main }

// setTabWindow does nothing: Home stays in the main window.
func (h *homeTab) setTabWindow(*viewerWindow) {}

// view is the Home page.
func (h *homeTab) view(c *ui.Context) { h.app.pageView(c, pageHome) }

// live is false: Home is no connection.
func (h *homeTab) live() bool { return false }

// disconnect does nothing.
func (h *homeTab) disconnect() {}

// syncGrab does nothing: Home takes no keys for another computer.
func (h *homeTab) syncGrab(bool) {}

// endGrab does nothing.
func (h *homeTab) endGrab() {}

// setFront does nothing: Home exchanges no clipboard.
func (h *homeTab) setFront(bool) {}

// shellView is the main window (doc/en-us/gui.md, "Windows"): the sidebar
// on the window's material, and beside it the cards that need the user
// (connection requests, a connection here, an AI task waiting) above the
// group shown; the Settings sheet over everything while open. In protected
// mode one pane: the card that turns it off over this computer's AI
// session (protectedSession), Settings holding only its AI page.
func (a *App) shellView(c *ui.Context, vw *viewerWindow, g group) {
	t := c.Theme()
	a.mu.Lock()
	protected := a.protected()
	a.mu.Unlock()
	if protected {
		ui.Column(c.Key("protected")).Fill().Padding(paneMargin).Children(func() {
			ui.Column(c).Fill().Radius(paneRadius).Clip().Background(t.Background).Border(1, t.Border).Children(func() {
				ui.Row(c.Key("protected-head")).Height(paneHeaderH).Shrink(0).FillWidth().Padding(0, 8, 0, 14).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, navAI).FontSize(navIconSize).TextColor(t.TextMuted)
					ui.Text(c, L.PickThisComputer()).FontSize(sizeBody).Bold()
					ui.Box(c).Grow(1).FillHeight().DragWindow()
					if iconButton(c, "protected-settings", shellGear, L.NavSettings()) {
						a.openSettings(pageAI)
					}
					windowButtons(c, "protected-win", vw.win, false)
				})
				ui.Column(c.Key("protected-cards")).FillWidth().Shrink(0).Padding(8, 20, 0, 20).Children(func() {
					a.requestsView(c)
					a.hostBanner(c)
					a.protectedCard(c)
				})
				a.protectedSession().view(c)
				a.toastView(c)
			})
		})
		a.settingsSheet(c, vw.win)
		return
	}
	ui.Row(c.Key("shell")).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebarView(c, vw)
		ui.Column(c.Key("main")).Grow(1).MinWidth(0).MinHeight(0).Padding(paneMargin/2, paneMargin/2, paneMargin/2, 0).Children(func() {
			a.bannersView(c)
			vw.panesView(c, g)
			a.toastView(c)
		})
	})
	a.settingsSheet(c, vw.win)
	// Ctrl+K (⌘K) opens search and closes it, unless the keys go to
	// another computer.
	if !vw.keysAway(c) && c.Shortcut(ui.Cmd, ui.KeyK) {
		a.mu.Lock()
		a.palette = paletteState{open: !a.palette.open}
		a.mu.Unlock()
	}
	a.paletteView(c)
}

// bannersView is what needs the user whatever shows: the connection
// requests waiting for them, a device connected here, an AI task waiting
// or working here — each a card of its own (noticeCard).
func (a *App) bannersView(c *ui.Context) {
	ui.Column(c.Key("banners")).FillWidth().Shrink(0).Children(func() {
		a.requestsView(c)
		a.hostBanner(c)
		a.aiBanner(c)
	})
}

// noticeCard is one of the cards above the panes (bannersView), or above
// protected mode's session: a pane of its own, with the panes' margin
// round it so that its edges and the gaps line up with theirs; tinted with
// tone and edged in it when one is given — the accent for a decision the
// user makes, the warning colour for someone at work on this computer.
func noticeCard(c *ui.Context, key string, tone ui.Color, body func()) {
	t := c.Theme()
	ui.Column(c.Key(key)).FillWidth().Shrink(0).Padding(paneMargin / 2).Children(func() {
		card := ui.Column(c.Key(key+"-card")).FillWidth().Radius(paneRadius).Clip().
			Background(t.Background).Border(1, t.Border).Shadow(0, 6, 18, 0, paneShadow)
		if tone.A == 0 {
			card.Padding(noticePadY, noticePadX).Gap(10).Children(body)
			return
		}
		card.Border(1, tone).Children(func() {
			ui.Column(c).FillWidth().Padding(noticePadY, noticePadX).Gap(10).Background(tone.Alpha(0.08)).Children(body)
		})
	})
}

// noticeIcon is the icon a card above the panes starts with when no device
// does: in the card's tone (the muted text's without one), on a round tile
// as large as a device's avatar.
func noticeIcon(c *ui.Context, icon *ui.SVG, tone ui.Color) {
	t := c.Theme()
	fg, face := tone, tone.Alpha(0.14)
	if tone.A == 0 {
		fg, face = t.TextMuted, tileBackground(t)
	}
	ui.Box(c).Size(avatarSize, avatarSize).Shrink(0).Radius(avatarSize / 2).Center().Background(face).Children(func() {
		ui.Icon(c, icon).FontSize(navIconSize + 2).TextColor(fg)
	})
}

// sbEntry is a sidebar entry: a group's panes in reading order, whether it
// shows, and the pane in focus in it.
type sbEntry struct {
	g     *group
	tabs  []tab
	shown bool
	focus tab
}

// entries are the main window's groups for its sidebar.
func (vw *viewerWindow) entries() []sbEntry {
	vw.mu.Lock()
	defer vw.mu.Unlock()
	out := make([]sbEntry, 0, len(vw.groups))
	for _, g := range vw.groups {
		out = append(out, sbEntry{g: g, tabs: g.split.shownTabs(), shown: g == vw.cur, focus: g.split.focus})
	}
	return out
}

// homeAlone reports whether an entry is Home by itself, which the sidebar's
// Home row stands for.
func (e sbEntry) homeAlone() bool {
	if len(e.tabs) != 1 {
		return false
	}
	_, ok := e.tabs[0].(*homeTab)
	return ok
}

// groupName names a group of several panes by its devices, as "Studio Mac +
// NUC".
func groupName(tabs []tab) string {
	var names []string
	for _, t := range tabs {
		if n := t.head().title; !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return strings.Join(names, " + ")
}

// sidebarView is the main window's sidebar (doc/en-us/gui.md, "Windows"),
// drawn on the window's material: hide the sidebar and + (the picker)
// beside the traffic lights; Home; the open panes, a group of several as a
// card with its panes under its name, panes in windows of their own last;
// and at the bottom search, light or dark, the settings and the network's
// state. A pane dropped on the list gets an entry of its own. Folded, it is
// the rail.
func (a *App) sidebarView(c *ui.Context, vw *viewerWindow) {
	t := c.Theme()
	bar := c.TitleBar()
	a.mu.Lock()
	rail := a.prefs.SidebarRail
	a.mu.Unlock()
	if rail {
		a.railView(c, vw)
		return
	}
	entries := vw.entries()
	ui.Column(c.Key("sidebar")).Width(shellSidebarW).Shrink(0).FillHeight().Padding(10, 10, 10, 12).Gap(2).Children(func() {
		ui.Row(c.Key("sb-top")).Height(sbTopH).Shrink(0).Gap(2).AlignItems(ui.Center).Padding(0, 0, 0, max(bar.Left-12, 0)).Children(func() {
			if iconButton(c, "sb-hide", shellSidebar, L.SidebarHide()) {
				a.setRail(true)
			}
			plus := iconButtonBase(c, "sb-new", shellPlus, L.SidebarNew())
			if plus.Clicked() {
				a.openPicker("sidebar", false, "", zoneMiddle)
			}
			a.pickerView(c, "sidebar", plus)
			ui.Box(c.Key("sb-drag")).Grow(1).FillHeight().DragWindow()
		})
		ui.Box(c).Height(8).Shrink(0)
		homeShown := slices.ContainsFunc(entries, func(e sbEntry) bool { return e.shown && e.homeAlone() })
		if sbRow(c, "sb-home", appMark, L.NavHome(), "", homeShown, false, homeShown).Clicked() {
			vw.showHome()
		}
		open := 0
		for _, e := range entries {
			if !e.homeAlone() {
				open += len(e.tabs)
			}
		}
		own := a.ownWindows()
		open += len(own)
		if open > 0 {
			ui.Row(c).Padding(16, 10, 6, 10).Children(func() {
				ui.Text(c, L.SidebarOpen()).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Grow(1)
				ui.Text(c, strconv.Itoa(open)).FontSize(sizeSmall).TextColor(t.TextMuted)
			})
		}
		list := ui.Column(c.Key("sb-list")).Grow(1).FillWidth().MinHeight(0).Gap(2)
		if _, over := ui.DragOver[tabDrag](list); over {
			list.Background(t.Accent.Alpha(0.08)).Radius(10)
		}
		if d, ok := ui.Drop[tabDrag](list); ok {
			a.moveTab(d.key, vw)
		}
		list.Children(func() {
			for i, e := range entries {
				if e.homeAlone() {
					continue
				}
				a.sbEntryView(c, vw, e, strconv.Itoa(i))
			}
			for _, w := range own {
				h := w.tab.head()
				row := sbRow(c, "sb-own-"+w.tab.tabKey(), h.icon, h.title, h.kind, false, false, false).Tooltip(L.SidebarOwnWindow())
				if row.Clicked() {
					w.win.win.Show()
					w.win.win.Focus()
				}
			}
		})
		ui.Row(c.Key("sb-bottom")).Shrink(0).Gap(2).AlignItems(ui.Center).Padding(8, 0, 0, 0).Children(func() {
			if iconButton(c, "sb-search", shellSearch, L.SidebarSearch(shortcutName("K"))) {
				a.openPalette()
			}
			a.themeButton(c)
			if iconButton(c, "sb-settings", shellGear, L.NavSettings()) {
				a.openSettings(pageGeneral)
			}
			ui.Box(c).Grow(1)
			a.networkChip(c)
		})
	})
}

// sbEntryView is one sidebar entry: a pane's row, or a group's card with
// its panes' rows under its name. A row drags its pane like its header.
func (a *App) sbEntryView(c *ui.Context, vw *viewerWindow, e sbEntry, key string) {
	t := c.Theme()
	if len(e.tabs) == 1 {
		v := e.tabs[0]
		h := v.head()
		row := sbRow(c, "sb-"+v.tabKey(), h.icon, h.title, h.kind, e.shown, false, e.shown).Drag(tabDrag{v.tabKey()})
		if row.Clicked() {
			vw.showGroup(e.g, nil)
		}
		return
	}
	card := ui.Column(c.Key("sb-group-"+key)).FillWidth().Radius(10).Padding(2, 0).Gap(0)
	if e.shown {
		card.Background(t.Background).Shadow(0, 1, 6, 0, paneShadow).Padding(4, 4)
	}
	card.Children(func() {
		if sbRow(c, "sb-group-head-"+key, paneSplitRight, groupName(e.tabs), "", false, false, e.shown).Clicked() {
			vw.showGroup(e.g, nil)
		}
		for _, v := range e.tabs {
			h := v.head()
			sel := e.shown && v == e.focus
			row := sbRow(c, "sb-"+key+"-"+v.tabKey(), h.icon, h.title, h.kind, sel, true, sel).Drag(tabDrag{v.tabKey()})
			if row.Clicked() {
				vw.showGroup(e.g, v)
			}
		}
	})
}

// sbRow is a row of the sidebar: an icon, a name (bold when strong) and a
// kind in words; the row chosen stands out (in a group's card, in the
// accent's tint), a row under a group's name is indented.
func sbRow(c *ui.Context, key string, icon *ui.SVG, title, kind string, selected, child, strong bool) ui.Element {
	t := c.Theme()
	left := float32(10)
	if child {
		left = 28
	}
	row := ui.ButtonBase(c.Key(key)).Height(sbRowH).FillWidth().Radius(8).Padding(0, 10, 0, left).Label(title)
	fg := t.TextMuted
	switch {
	case selected && child:
		row.Background(t.Accent.Alpha(0.12))
		fg = t.Accent
	case selected:
		row.Background(t.Background).Shadow(0, 1, 4, 0, paneShadow)
		fg = t.Accent
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(9).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).FontSize(navIconSize).TextColor(fg)
			name := ui.Text(c, title).SingleLine().MinWidth(0).Shrink(0).MaxWidth(sbNameMaxW)
			if strong {
				name.Bold()
			}
			if kind != "" { // it gives way to the name
				ui.Text(c, kind).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
			}
		})
	})
	return row
}

// ownWindow is a pane in a window of its own, which the sidebar lists after
// the main window's.
type ownWindow struct {
	win *viewerWindow
	tab tab
}

// ownWindows are the panes in windows of their own, oldest first.
func (a *App) ownWindows() []ownWindow {
	a.mu.Lock()
	wins := slices.Clone(a.viewerWins)
	a.mu.Unlock()
	var out []ownWindow
	for _, w := range wins {
		w.mu.Lock()
		if len(w.tabs) > 0 {
			out = append(out, ownWindow{win: w, tab: w.tabs[0]})
		}
		w.mu.Unlock()
	}
	return out
}

// railView is the sidebar folded to its icons: show the sidebar and +,
// Home, an icon per entry (a group's with how many panes it holds, named in
// its tooltip), then search, light or dark, the settings and the network's
// dot.
func (a *App) railView(c *ui.Context, vw *viewerWindow) {
	t := c.Theme()
	bar := c.TitleBar()
	entries := vw.entries()
	ui.Column(c.Key("rail")).Width(max(railW, bar.Left)).Shrink(0).FillHeight().Padding(10, 0).Gap(4).AlignItems(ui.Center).Children(func() {
		if bar.Left > 0 {
			ui.Box(c).Height(sbTopH).Shrink(0).FillWidth().DragWindow()
		}
		if iconButton(c, "rail-show", shellSidebar, L.SidebarShow()) {
			a.setRail(false)
		}
		plus := iconButtonBase(c, "rail-new", shellPlus, L.SidebarNew())
		if plus.Clicked() {
			a.openPicker("rail", false, "", zoneMiddle)
		}
		a.pickerView(c, "rail", plus)
		ui.Box(c).Width(24).Height(1).Shrink(0).Margin(6, 0).Background(t.Border)
		homeShown := slices.ContainsFunc(entries, func(e sbEntry) bool { return e.shown && e.homeAlone() })
		if railButton(c, "rail-home", appMark, L.NavHome(), 0, homeShown) {
			vw.showHome()
		}
		for i, e := range entries {
			if e.homeAlone() {
				continue
			}
			icon, name := e.tabs[0].head().icon, e.tabs[0].tabTitle()
			if len(e.tabs) > 1 {
				icon, name = paneSplitRight, groupName(e.tabs)
			}
			if railButton(c, "rail-"+strconv.Itoa(i), icon, name, len(e.tabs), e.shown) {
				vw.showGroup(e.g, nil)
			}
		}
		for _, w := range a.ownWindows() {
			if railButton(c, "rail-own-"+w.tab.tabKey(), w.tab.head().icon, w.tab.tabTitle()+" — "+L.SidebarOwnWindow(), 0, false) {
				w.win.win.Show()
				w.win.win.Focus()
			}
		}
		ui.Box(c).Grow(1)
		if iconButton(c, "rail-search", shellSearch, L.SidebarSearch(shortcutName("K"))) {
			a.openPalette()
		}
		a.themeButton(c)
		if iconButton(c, "rail-settings", shellGear, L.NavSettings()) {
			a.openSettings(pageGeneral)
		}
		a.networkDot(c)
	})
}

// railButton is an entry of the rail: its icon, a count when it holds
// several panes, its name in the tooltip. Returns: whether it was clicked.
func railButton(c *ui.Context, key string, icon *ui.SVG, name string, count int, selected bool) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(36, 36).Radius(9).Center().Shrink(0).Label(name).Tooltip(name)
	fg := t.TextMuted
	switch {
	case selected:
		b.Background(t.Background).Shadow(0, 1, 4, 0, paneShadow)
		fg = t.Accent
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Icon(c, icon).FontSize(navIconSize).TextColor(fg)
		if count > 1 {
			ui.Text(c, strconv.Itoa(count)).FontSize(10).Bold().TextColor(t.TextMuted).
				Absolute().Right(2).Bottom(1).Padding(0, 3).Radius(6).Background(t.Surface)
		}
	})
	return b.Clicked()
}

// setRail folds the sidebar to its icons, or unfolds it, and keeps the
// choice.
func (a *App) setRail(on bool) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	p.SidebarRail = on
	a.setPrefs(p)
}

// themeButton is the sidebar's appearance: follow the system, light or
// dark in turn, its icon saying which is chosen.
func (a *App) themeButton(c *ui.Context) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	icon, label, next := shellAuto, L.AppearanceFollow(), "light"
	switch p.Theme {
	case "light":
		icon, label, next = shellSun, L.AppearanceLight(), "dark"
	case "dark":
		icon, label, next = shellMoon, L.AppearanceDark(), "system"
	}
	if iconButton(c, "appearance", icon, label) {
		p.Theme = next
		ApplyTheme(next)
		a.setPrefs(p)
	}
}

// networkState is how the networks are doing for the sidebar: a word and
// its colour (from the theme); before the engine says, Starting…, or that
// it is not running.
func (a *App) networkState(t *ui.Theme) (string, ui.Color) {
	a.mu.Lock()
	var st *localapi.Status
	if a.connected {
		st = a.status
	}
	down := a.engineErr != ""
	a.mu.Unlock()
	switch {
	case st == nil && down:
		return L.ShellEngineDown(), t.Danger
	case st == nil:
		return L.EngineStarting(), t.Warning
	}
	switch st.Network.State {
	case "online":
		return L.ShellOnline(), t.Success
	case "connecting":
		return L.NetworkConnecting(), t.Warning
	case "error":
		return L.NetworkError(), t.Danger
	}
	return L.ShellOffline(), t.TextMuted
}

// networkChip is the networks' state at the sidebar's bottom: a dot and a
// word, which open Settings at Network.
func (a *App) networkChip(c *ui.Context) {
	t := c.Theme()
	word, col := a.networkState(t)
	b := ui.ButtonBase(c.Key("sb-network")).Height(paneButton).Padding(0, 10).Radius(7).Label(word).Tooltip(L.NavNetwork())
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(onlineDot, onlineDot).Radius(onlineDot / 2).Background(col)
			ui.Text(c, word).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
		})
	})
	if b.Clicked() {
		a.openSettings(pageNetwork)
	}
}

// networkDot is the rail's networks' state: the dot alone, named in its
// tooltip.
func (a *App) networkDot(c *ui.Context) {
	t := c.Theme()
	word, col := a.networkState(t)
	b := ui.ButtonBase(c.Key("rail-network")).Size(36, 36).Radius(9).Center().Label(word).Tooltip(word)
	b.Children(func() { ui.Box(c).Size(onlineDot+2, onlineDot+2).Radius(onlineDot/2 + 1).Background(col) })
	if b.Clicked() {
		a.openSettings(pageNetwork)
	}
}
