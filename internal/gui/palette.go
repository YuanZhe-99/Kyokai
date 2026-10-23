package gui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// paletteState is the search palette while it is open (doc/en-us/gui.md,
// "Windows"): the words typed and the result chosen.
type paletteState struct {
	open  bool
	query string
	sel   int
}

// paletteItem is one of the palette's results: its group, icon, title, the
// line under it, more words it is found by (a setting's), what Enter does
// to it in words (Open, Connect, Copy; "" for an action that only runs),
// and what it does.
type paletteItem struct {
	group, title, sub, words, hint string
	icon                           *ui.SVG
	do                             func()
}

// paletteGroupMax is how many results a group shows.
const paletteGroupMax = 6

// openPalette opens the search palette, empty.
func (a *App) openPalette() {
	a.mu.Lock()
	a.palette = paletteState{open: true}
	a.mu.Unlock()
}

// paletteItems are what the palette can find, by group: this device (the
// temporary password to copy and a new one, while it opens anything; the
// Device ID to copy), the panes open, the devices, actions — connect to a
// device (the + picker), Settings, the sidebar hidden or shown, the
// appearance — and Settings' settings (settingsHits).
// Notes: the closures run on the main thread.
func (a *App) paletteItems() []paletteItem {
	a.mu.Lock()
	var st *localapi.Status
	if a.connected {
		st = a.status
	}
	var rows []deviceRow
	if st != nil {
		rows = deviceRows(st, a.prefs)
	}
	rail := a.prefs.SidebarRail
	a.mu.Unlock()
	var out []paletteItem
	if st != nil {
		this := L.HomeThisDevice()
		if st.PasswordMode != "none" && st.PasswordMode != "permanent" {
			out = append(out,
				paletteItem{group: this, title: L.HomeTemporaryPassword(), sub: strings.Repeat("•", 8), hint: L.ActionCopy(), icon: rowLock, do: func() {
					a.revealPassword(func(p string) {
						clipboardWrite(p)
						a.update(func() { a.setToast(L.Copied(), false) })
					})
				}},
				paletteItem{group: this, title: L.ActionNewPassword(), sub: L.HomeTemporaryPassword(), icon: rowRefresh, do: func() {
					go func() { _, _ = a.request(localapi.RequestPasswordRegenerate) }()
				}})
		}
		id := st.DeviceID
		out = append(out, paletteItem{group: this, title: L.HomeDeviceID(), sub: groupDigits(id), hint: L.ActionCopy(), icon: rowID, do: func() {
			mygo.Clipboard.WriteText(id)
			a.update(func() { a.setToast(L.Copied(), false) })
		}})
	}
	tabs := a.allTabs()
	slices.SortFunc(tabs, func(x, y tab) int { return strings.Compare(x.tabTitle(), y.tabTitle()) })
	for _, v := range tabs {
		h := v.head()
		out = append(out, paletteItem{group: L.PaletteOpen(), title: h.title, sub: h.kind, hint: L.ActionOpen(), icon: h.icon, do: func() {
			if w := v.tabWindow(); w != nil {
				w.show(v)
			}
		}})
	}
	for _, r := range rows {
		sub := []string{groupDigits(r.id)}
		if r.session != nil && r.session.State == "connected" {
			sub = append(sub, L.CardConnected())
		}
		hint := L.ActionConnect()
		if a.tabFor(r.id) != nil {
			hint = L.ActionOpen()
		}
		out = append(out, paletteItem{group: L.HomeDevices(), title: deviceTitle(r.about, r.id), sub: strings.Join(sub, " · "), hint: hint, icon: kindDesktop, do: func() {
			a.connectDevice(r.id, "", r.about, nil)
		}})
	}
	actions := L.PaletteActions()
	at, sidebar := "sidebar", L.SidebarHide()
	if rail {
		at, sidebar = "rail", L.SidebarShow()
	}
	out = append(out,
		paletteItem{group: actions, title: L.SidebarNew(), hint: L.ActionOpen(), icon: shellPlus, do: func() { a.openPicker(at, false, "", zoneMiddle) }},
		paletteItem{group: actions, title: L.NavSettings(), hint: L.ActionOpen(), icon: shellGear, do: func() { a.openSettings(pageGeneral) }},
		paletteItem{group: actions, title: sidebar, icon: shellSidebar, do: func() { a.setRail(!rail) }},
	)
	for _, th := range []struct {
		theme, title string
		icon         *ui.SVG
	}{{"system", L.AppearanceFollow(), shellAuto}, {"light", L.AppearanceLight(), shellSun}, {"dark", L.AppearanceDark(), shellMoon}} {
		out = append(out, paletteItem{group: actions, title: th.title, icon: th.icon, do: func() {
			a.mu.Lock()
			p := a.prefs
			a.mu.Unlock()
			p.Theme = th.theme
			ApplyTheme(th.theme)
			a.setPrefs(p)
		}})
	}
	for _, h := range settingsHits() {
		out = append(out, paletteItem{group: L.NavSettings(), title: h.title, sub: L.NavSettings() + " › " + pageName(h.page), words: h.words, hint: L.ActionOpen(), icon: shellGear, do: func() {
			a.openSettings(h.page)
			a.showSetting(h)
		}})
	}
	return out
}

// searchPalette is the palette's results for the words typed: the items
// with every word in their title, the line under it or their more words,
// ignoring case; within a group, titles that match first; groups in their
// order, each at most paletteGroupMax. With no words: everything but the
// settings.
func searchPalette(all []paletteItem, query string) []paletteItem {
	words := strings.Fields(strings.ToLower(query))
	var groups []string
	byGroup := map[string][2][]paletteItem{} // titles that match, then the rest
	for _, it := range all {
		if len(words) == 0 && it.group == L.NavSettings() {
			continue
		}
		title, rest := strings.ToLower(it.title), strings.ToLower(it.sub+" "+it.words)
		inTitle, inAny := true, true
		for _, w := range words {
			inTitle = inTitle && strings.Contains(title, w)
			inAny = inAny && (strings.Contains(title, w) || strings.Contains(rest, w))
		}
		if !inAny {
			continue
		}
		if !slices.Contains(groups, it.group) {
			groups = append(groups, it.group)
		}
		g := byGroup[it.group]
		if inTitle {
			g[0] = append(g[0], it)
		} else {
			g[1] = append(g[1], it)
		}
		byGroup[it.group] = g
	}
	var out []paletteItem
	for _, name := range groups {
		items := slices.Concat(byGroup[name][0], byGroup[name][1])
		out = append(out, items[:min(len(items), paletteGroupMax)]...)
	}
	return out
}

// paletteView is the search palette over the main window (doc/en-us/gui.md,
// "Windows"): a field, the results by group with the word typed marked, ↑
// and ↓ to choose, Enter or a click to open it (or copy it, for the
// password and the Device ID), Escape or a click outside to close.
func (a *App) paletteView(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	p := a.palette
	a.mu.Unlock()
	if !p.open {
		return
	}
	results := searchPalette(a.paletteItems(), p.query)
	sel := min(max(p.sel, 0), max(len(results)-1, 0))
	moved := false
	open := true
	run := func(it paletteItem) {
		open = false
		a.mu.Lock()
		a.palette = paletteState{}
		a.mu.Unlock()
		it.do()
	}
	ui.DialogBase(c, &open, func(back, panel ui.Element) {
		back.Background(paletteScrim).Padding(paletteTop, 16, 16, 16).Justify(ui.Start)
		panel.Width(paletteW).Radius(paneRadius).Background(t.Background).Border(1, t.Border).Shadow(0, 24, 64, 0, sheetShadow).Clip()
		ui.Column(c.Key("palette")).FillWidth().Children(func() {
			ui.Row(c.Key("palette-head")).Height(54).Padding(0, 16).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, shellSearch).FontSize(navIconSize + 2).TextColor(t.TextMuted)
				q := p.query
				in := ui.TextInputBase(c.Key("palette-in"), &q).Grow(1).FontSize(sizeHeading).Placeholder(L.PaletteSearch()).Label(L.PaletteSearch()).AutoFocus()
				if in.Changed() {
					a.mu.Lock()
					a.palette.query, a.palette.sel = q, 0
					a.mu.Unlock()
				}
				step := 0
				if in.Shortcut(0, ui.KeyDown) {
					step = 1
				}
				if in.Shortcut(0, ui.KeyUp) {
					step = -1
				}
				if step != 0 && len(results) > 0 {
					sel, moved = (sel+step+len(results))%len(results), true
					a.mu.Lock()
					a.palette.sel = sel
					a.mu.Unlock()
				}
				if in.Submitted() && len(results) > 0 {
					run(results[sel])
				}
				// The dialog keeps the window's Ctrl+K from it: closed here.
				if in.Shortcut(ui.Cmd, ui.KeyK) {
					open = false
				}
				keycap(c, "Esc")
			})
			ui.Divider(c)
			ui.Scroll(c.Key("palette-results")).FillWidth().MaxHeight(paletteMaxH).Padding(4, 8, 8, 8).Gap(2).Children(func() {
				if len(results) == 0 {
					ui.Text(c, L.PaletteNone()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(12, 8)
				}
				group := ""
				for i, it := range results {
					if it.group != group {
						group = it.group
						ui.Text(c, group).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Padding(10, 8, 4, 8)
					}
					row := ui.ButtonBase(c.Key("palette-row-"+strconv.Itoa(i))).FillWidth().Radius(9).Padding(6, 8).Label(it.title)
					switch {
					case i == sel:
						row.Background(t.Accent.Alpha(0.12))
						if moved {
							row.ScrollIntoView()
						}
					case row.Hovered():
						row.Background(t.SurfaceHover)
					}
					row.Children(func() {
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Box(c).Size(tileSize, tileSize).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
								ui.Icon(c, it.icon).FontSize(navIconSize).TextColor(t.TextMuted)
							})
							ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
								marked(c, it.title, p.query)
								if it.sub != "" {
									ui.Text(c, it.sub).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
								}
							})
							if i == sel {
								keycap(c, strings.TrimSpace("↵ "+it.hint))
							}
						})
					})
					if row.Clicked() {
						run(it)
					}
				}
			})
			ui.Divider(c)
			ui.Row(c.Key("palette-foot")).Padding(10, 16).Gap(14).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					keycap(c, "↑")
					keycap(c, "↓")
					ui.Text(c, L.PaletteMove()).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					keycap(c, "↵")
					ui.Text(c, L.ActionOpen()).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
				ui.Box(c).Grow(1)
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					keycap(c, shortcutName("K"))
					ui.Text(c, L.PaletteAnywhere()).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
			})
		})
	})
	if !open {
		a.mu.Lock()
		a.palette = paletteState{}
		a.mu.Unlock()
	}
}

// marked is a result's title with the first word typed marked where it
// first shows (unmarked where lower case changes the title's length).
func marked(c *ui.Context, title, query string) {
	t := c.Theme()
	words := strings.Fields(strings.ToLower(query))
	lower := strings.ToLower(title)
	i := -1
	if len(words) > 0 && len(lower) == len(title) {
		i = strings.Index(lower, words[0])
	}
	if i < 0 {
		ui.Text(c, title).SingleLine()
		return
	}
	n := len(words[0])
	ui.RichText(c, ui.Span{Text: title[:i]}, ui.Span{Text: title[i : i+n], Weight: 700, Color: t.Accent}, ui.Span{Text: title[i+n:]}).SingleLine()
}

// keycap is a key's name in a small frame, as the palette shows its keys.
func keycap(c *ui.Context, key string) {
	t := c.Theme()
	ui.Text(c, key).FontSize(sizeSmall).TextColor(t.TextMuted).NoWrap().Padding(1, 6).Radius(5).Border(1, t.Border)
}

// shortcutName is a shortcut with the platform's modifier, in its usual
// writing: ⌘K on macOS, Ctrl+K elsewhere.
func shortcutName(key string) string {
	if ui.Cmd == ui.Super {
		return "⌘" + key
	}
	return "Ctrl+" + key
}
