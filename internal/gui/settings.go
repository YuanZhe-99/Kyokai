package gui

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// openSettings opens the Settings sheet at a page.
func (a *App) openSettings(p page) {
	a.mu.Lock()
	a.settingsOpen, a.page, a.settingsFocus = true, p, settingsFocus{}
	a.mu.Unlock()
}

// covered reports whether something over the panes takes the keyboard, so
// that no picture does: the Settings sheet, the search palette or the +
// picker.
func (a *App) covered() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settingsOpen || a.palette.open || a.picker.at != ""
}

// settingsItem is a page of the Settings sheet's sections.
type settingsItem struct {
	page  page
	icon  *ui.SVG
	label string
}

// settingsSection is a group of the Settings sheet's sections.
type settingsSection struct {
	title string
	items []settingsItem
}

// settingsSections are the Settings sheet's sections, grouped: the app's
// own preferences, this computer, AI, and Kyokai itself.
func settingsSections() []settingsSection {
	return []settingsSection{
		{L.SettingsGroupPreferences(), []settingsItem{{pageGeneral, navGeneral, L.SettingsGeneral()}}},
		{L.SettingsGroupThisComputer(), []settingsItem{
			{pageAccess, navPermissions, L.NavAccess()},
			{pageNetwork, navNetwork, L.NavNetwork()},
			{pageSecurity, navSecurity, L.NavSecurityPrivacy()},
			{pageRunMode, rowServer, L.SettingsRunMode()},
		}},
		{L.SettingsGroupAI(), []settingsItem{{pageAI, navAI, L.NavAI()}}},
		{"Kyokai", []settingsItem{{pageAbout, navAbout, L.NavAboutUpdates()}}},
	}
}

// pageName is a Settings page's name in its sections.
func pageName(p page) string {
	for _, s := range settingsSections() {
		for _, it := range s.items {
			if it.page == p {
				return it.label
			}
		}
	}
	return ""
}

// settingsSheet is Settings (doc/en-us/gui.md, "Settings"): the top layer,
// a sheet over the whole window, sidebar included, opened by the sidebar's
// gear and never part of a split; its sections on the left under a search
// field, the page chosen beside them. While it is up, its header ends with
// the window's buttons on Windows, as the panes' it covers did. Escape, a
// click outside or × closes it.
func (a *App) settingsSheet(c *ui.Context, win *mygo.Window) {
	a.mu.Lock()
	open, pg := a.settingsOpen, a.page
	a.mu.Unlock()
	if !open {
		return
	}
	t := c.Theme()
	w, h := c.Size()
	ui.DialogBase(c, &open, func(back, panel ui.Element) {
		back.Background(sheetScrim).Padding(sheetTop, sheetX, sheetBottom, sheetX)
		panel.Radius(paneRadius).Background(t.Background).Shadow(0, 10, 30, 0, sheetShadow).Clip()
		ui.Column(c.Key("sheet")).Width(max(w-2*sheetX, 560)).Height(max(h-sheetTop-sheetBottom, 400)).Children(func() {
			ui.Row(c.Key("sheet-head")).Height(sheetHeaderH).Shrink(0).Padding(0, 12, 0, 20).AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.NavSettings()).FontSize(sizeHeading).Bold().Grow(1)
				if iconButton(c, "sheet-close", paneClose, L.ActionClose()) {
					open = false
				}
			})
			ui.Divider(c)
			ui.Row(c.Key("sheet-body")).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				ui.Scroll(c.Key("sheet-nav")).Width(settingsNavW).Shrink(0).Padding(14, 12).Gap(2).Children(func() {
					a.settingsNav(c, pg)
				})
				ui.Box(c).Width(1).Shrink(0).Background(t.Border)
				ui.Column(c.Key("sheet-page")).Grow(1).MinWidth(0).Children(func() { a.pageView(c, pg) })
			})
		})
	})
	// The window's buttons stay where the top-right pane's header holds
	// them, above the dimmed window: never beside the sheet's ×, where
	// closing Settings could close the window instead.
	if ownWindowButtons {
		ui.Overlay(c, func() {
			ui.Row(c.Key("sheet-win")).Absolute().Top(paneMargin + (paneHeaderH-winButtonH)/2).Right(2 * paneMargin).Gap(4).Children(func() {
				windowButtons(c, "sheet-win", win, true)
			})
		})
	}
	if !open {
		a.mu.Lock()
		a.settingsOpen, a.settingsFocus, a.more = false, settingsFocus{}, ""
		a.mu.Unlock()
	}
}

// settingsNav is the sheet's navigation: the search field, then the
// sections, or while it holds words the settings they find, each with
// its page.
func (a *App) settingsNav(c *ui.Context, pg page) {
	t := c.Theme()
	a.mu.Lock()
	q := a.settingsQuery
	a.mu.Unlock()
	ui.Row(c.Key("sheet-search")).Height(34).Shrink(0).Gap(8).Padding(0, 10).Radius(9).Border(1, t.Border).AlignItems(ui.Center).Margin(0, 0, 6, 0).Children(func() {
		ui.Icon(c, shellSearch).FontSize(sizeSmall + 2).TextColor(t.TextMuted)
		in := ui.TextInputBase(c.Key("sheet-search-in"), &q).Grow(1).Placeholder(L.SettingsSearch()).Label(L.SettingsSearch())
		if in.Changed() {
			a.mu.Lock()
			a.settingsQuery = q
			a.mu.Unlock()
		}
	})
	if strings.TrimSpace(q) != "" {
		hits := searchSettings(settingsHits(), q)
		if len(hits) == 0 {
			ui.Text(c, L.SettingsSearchNone()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(10)
		}
		for i, h := range hits {
			row := ui.ButtonBase(c.Key("sheet-hit-"+strconv.Itoa(i))).FillWidth().Radius(8).Padding(6, 10).Label(h.title)
			if row.Hovered() {
				row.Background(t.SurfaceHover)
			}
			row.Children(func() {
				ui.Column(c).FillWidth().Gap(2).Children(func() {
					ui.Text(c, h.title).SingleLine()
					ui.Text(c, pageName(h.page)).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
				})
			})
			if row.Clicked() {
				a.showSetting(h)
			}
		}
		return
	}
	for _, s := range settingsSections() {
		ui.Text(c, s.title).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Padding(12, 10, 4, 10)
		for _, it := range s.items {
			row := ui.ButtonBase(c.Key("sheet-nav-"+strconv.Itoa(int(it.page)))).Height(sbRowH).FillWidth().Radius(8).Padding(0, 10).Label(it.label)
			fg := t.TextMuted
			switch {
			case it.page == pg:
				row.Background(t.SurfaceHover)
				fg = t.Text
			case row.Hovered():
				row.Background(t.SurfaceHover.Alpha(0.6))
			}
			row.Children(func() {
				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, it.icon).FontSize(navIconSize).TextColor(fg)
					ui.Text(c, it.label).TextColor(fg).SingleLine()
				})
			})
			if row.Clicked() {
				a.mu.Lock()
				a.page, a.settingsFocus = it.page, settingsFocus{}
				a.mu.Unlock()
			}
		}
	}
}

// settingsFocus is the setting a search hit leads to: its title, until
// when its row stays tinted, and whether its page has scrolled it into
// view yet.
type settingsFocus struct {
	title string
	until time.Time
	shown bool
}

// focusTime is how long a setting found by search stays tinted.
const focusTime = 2 * time.Second

// showSetting opens a setting search found: its page, scrolled to it, the
// setting tinted for a moment (until the first frame after focusTime).
func (a *App) showSetting(h settingsHit) {
	a.mu.Lock()
	a.page, a.settingsFocus = h.page, settingsFocus{title: h.title, until: time.Now().Add(focusTime)}
	a.mu.Unlock()
}

// focused reports whether a setting is the one a search hit leads to and
// still tinted, and whether it is to scroll into view in this frame (only
// once).
func (a *App) focused(title string) (hit, scroll bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settingsFocus.title == "" || a.settingsFocus.title != title {
		return false, false
	}
	scroll = !a.settingsFocus.shown
	a.settingsFocus.shown = true
	return time.Now().Before(a.settingsFocus.until), scroll
}

// settingsPage is a page of Settings: its title, one sentence on what it
// is for, then its sections, as wide as settingsPageW at most.
func settingsPage(c *ui.Context, title, about string, body func()) {
	t := c.Theme()
	ui.Column(c.Key("settings-page")).FillWidth().MaxWidth(settingsPageW).Children(func() {
		ui.Text(c, title).FontSize(sizePageTitle).Bold()
		if about != "" {
			ui.Text(c, about).TextColor(t.TextMuted).Margin(4, 0, 0, 0)
		}
		body()
	})
}

// sectionHead is a section's title on a Settings page, muted, with aside
// on its right (a note or a button; nil for none). A search hit on the
// title scrolls it into view.
func (a *App) sectionHead(c *ui.Context, key, title string, aside func()) {
	t := c.Theme()
	head := ui.Row(c.Key(key+"-head")).FillWidth().MinHeight(28).AlignItems(ui.Center).Padding(18, 4, 8, 4).Gap(gap)
	if _, scroll := a.focused(title); scroll {
		head.ScrollIntoView()
	}
	head.Children(func() {
		ui.Text(c, title).FontSize(sizeSmall).Bold().TextColor(t.TextMuted).Grow(1)
		if aside != nil {
			aside()
		}
	})
}

// section is a titled group of rows on a Settings page: its head
// (sectionHead), then its rows in one rounded list (list).
func (a *App) section(c *ui.Context, key, title string, aside func(), body func(r *rows)) {
	a.sectionHead(c, key, title, aside)
	a.list(c, key, body)
}

// list is rows in one rounded list, a line between each: lighter than the
// sheet in the dark theme, as white as it in the light one, with a border.
func (a *App) list(c *ui.Context, key string, body func(r *rows)) {
	t := c.Theme()
	bg := t.Background
	if t.Dark {
		bg = t.Surface
	}
	ui.Column(c.Key(key)).FillWidth().Radius(listRadius).Border(1, t.Border).Background(bg).Clip().Children(func() {
		body(&rows{a: a})
	})
}

// rows adds a list's rows (App.list), a line between each; each call
// takes the frame's context, which a struct must not keep.
type rows struct {
	a *App
	n int
}

// setting is a row's words: its title; its one sentence, in col when set,
// else muted; and the longer explanation How it works opens ("" for none).
type setting struct {
	title, about, more string
	col                ui.Color
}

// lead is what a row starts with: an icon in a tile, a letter, or a
// device's avatar.
type lead func(c *ui.Context)

// tile is an icon in a rounded tile, as a row starts.
func tile(icon *ui.SVG) lead {
	return func(c *ui.Context) {
		t := c.Theme()
		ui.Box(c).Size(tileSize, tileSize).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
			ui.Icon(c, icon).FontSize(navIconSize).TextColor(t.TextMuted)
		})
	}
}

// letterTile is a name's first letter in a tile, as a provider's row
// starts.
func letterTile(name string) lead {
	return func(c *ui.Context) {
		t := c.Theme()
		letter := "?"
		if r := []rune(strings.TrimSpace(name)); len(r) > 0 {
			letter = strings.ToUpper(string(r[0]))
		}
		ui.Box(c).Size(tileSize, tileSize).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
			ui.Text(c, letter).Bold().TextColor(t.TextMuted)
		})
	}
}

// avatarLead is a device's avatar, as a device's row starts.
func avatarLead(about localapi.About) lead {
	return func(c *ui.Context) { deviceAvatar(c, about) }
}

// tileBackground is a tile's face: a shade off the list it sits in.
func tileBackground(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.RGBA(255, 255, 255, 0.06)
	}
	return t.Surface
}

// line draws the line above every row but the first.
func (r *rows) line(c *ui.Context) {
	if r.n > 0 {
		ui.Box(c).Height(1).FillWidth().Shrink(0).Background(c.Theme().Border)
	}
	r.n++
}

// row is one setting: what it starts with (nil for nothing), its words,
// and its control on the right (nil for none). A search hit on it scrolls
// it into view and tints it for a moment.
func (r *rows) row(c *ui.Context, key string, ld lead, s setting, control func()) {
	t := c.Theme()
	r.line(c)
	e := ui.Row(c.Key(key)).FillWidth().MinHeight(rowMinH).Padding(9, 16).Gap(14).AlignItems(ui.Center)
	if hit, scroll := r.a.focused(s.title); hit {
		e.Background(t.Accent.Alpha(0.1))
		if scroll {
			e.ScrollIntoView()
		}
	}
	e.Children(func() {
		if ld != nil {
			ld(c)
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).AlignItems(ui.Start).Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, s.title).FontWeight(500).MinWidth(0)
				if s.more != "" {
					r.a.howItWorks(c, key+"-more", s.more)
				}
			})
			if s.about != "" {
				col := s.col
				if col.A == 0 {
					col = t.TextMuted
				}
				ui.Text(c, s.about).FontSize(sizeSmall).TextColor(col)
			}
		})
		if control != nil {
			control()
		}
	})
}

// block is a row of free content across the list: a form, a list of
// devices, a key shown once.
func (r *rows) block(c *ui.Context, key string, body func()) {
	r.line(c)
	ui.Column(c.Key(key)).FillWidth().Padding(12, 16).Gap(gap).Children(body)
}

// howItWorks is a row's "How it works": a small ⓘ beside its title that
// opens its longer explanation in a popover.
func (a *App) howItWorks(c *ui.Context, key, text string) {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(20, 20).Radius(10).Center().Shrink(0).Label(L.HowItWorks()).Tooltip(L.HowItWorks())
	a.mu.Lock()
	open := a.more == key
	a.mu.Unlock()
	if b.Hovered() || open {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() { ui.Icon(c, navAbout).FontSize(sizeSmall + 2).TextColor(t.TextMuted) })
	if b.Clicked() {
		open = !open
	}
	ui.Popover(c, b, &open, func() {
		ui.Text(c, text).FontSize(sizeSmall).MaxWidth(moreW)
	})
	a.mu.Lock()
	switch {
	case open:
		a.more = key
	case a.more == key:
		a.more = ""
	}
	a.mu.Unlock()
}

// toggleTile is a permission as a tile that toggles: its icon and name,
// in the accent's tint with a check while on. Returns: whether it was
// clicked, the new state in *on.
func toggleTile(c *ui.Context, key string, icon *ui.SVG, label string, on *bool, disabled bool) bool {
	t := c.Theme()
	b := ui.ToggleBase(c.Key(key), on).Height(permTileH).Radius(11).Padding(10, 12).Label(label).Disabled(disabled)
	fg, edge, face := t.TextMuted, t.Border, t.Background
	if t.Dark {
		face = t.Surface
	}
	switch {
	case *on:
		fg, edge, face = t.Accent, t.Accent, t.Accent.Alpha(0.12)
	case b.Hovered():
		face = t.SurfaceHover
	}
	b.Border(1, edge).Background(face)
	changed := b.Changed()
	b.Children(func() {
		ui.Column(c).Fill().Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon).FontSize(navIconSize + 2).TextColor(fg)
				ui.Box(c).Grow(1)
				if *on {
					ui.Box(c).Size(16, 16).Radius(8).Center().Background(t.Accent).Children(func() {
						ui.Icon(c, rowCheck).FontSize(10).TextColor(t.AccentText)
					})
				}
			})
			ui.Text(c, label).FontSize(sizeSmall).SingleLine()
		})
	})
	return changed
}

// settingsHit is a setting search finds: the page it is on, its title
// (a row's or a section's), and more words that find it.
type settingsHit struct {
	page  page
	title string
	words string
}

// settingsHits are the settings search finds: every row with a fixed title,
// and the sections whose rows are the user's own (networks, keys, devices,
// providers), with the words of their buttons. Each one shows on its page
// (TestSettingsHitsShow).
func settingsHits() []settingsHit {
	join := func(words ...string) string { return strings.Join(words, " ") }
	var grants []string
	for _, p := range allPermissions {
		grants = append(grants, permissionShort(p), permissionLabel(p))
	}
	hits := []settingsHit{
		{pageGeneral, L.SettingsLanguage(), L.SettingsLanguageAbout()},
		{pageGeneral, L.SettingsTheme(), join(L.SettingsThemeAbout(), L.ThemeSystem(), L.ThemeLight(), L.ThemeDark())},
		{pageGeneral, L.SettingsOpenAtLogin(), L.SettingsOpenAtLoginHint()},
		{pageGeneral, L.SettingsStartHidden(), L.SettingsStartHiddenAbout()},
		{pageGeneral, L.SettingsCloseButton(), join(L.SettingsCloseButtonAbout(), L.CloseHide(), L.CloseQuit())},
		{pageAccess, L.SignInTemporary(), L.SignInTemporaryAbout()},
		{pageAccess, L.PermanentTitle(), L.PermanentSetAbout()},
		{pageAccess, L.SignInKeys(), join(L.KeysAuthorize(), L.KeysPair())},
		{pageAccess, L.WithoutAskingTitle(), L.WithoutAskingAbout()},
		{pageAccess, L.PasswordGrantsTitle(), join(grants...)},
		{pageAccess, L.ServicePortsTitle(), join(L.ServiceSSH(), L.ServiceRDP(), L.ServiceVNC())},
		{pageAccess, L.WatchersTitle(), L.PermSeeStatus()},
		{pageAccess, L.TrustedTitle(), L.TrustedHint()},
		{pageAccess, L.SettingsSoundSource(), L.SoundSourceAbout()},
		{pageNetwork, L.NetworkTitle(), join(L.NetworkAdd(), L.NetworkJoin())},
		{pageNetwork, L.ByAddressTitle(), L.ByAddressAbout()},
		{pageNetwork, L.NetworkDirect(), L.NetworkDirectAbout()},
		{pageNetwork, L.NetworkLatencyFirst(), L.NetworkLatencyFirstHint()},
		{pageSecurity, L.HomeDeviceID(), L.IdentityIDAbout()},
		{pageSecurity, L.SettingsName(), L.SettingsNameAbout()},
		{pageSecurity, L.IdentityFingerprint(), ""},
		{pageSecurity, L.BackupTitle(), join(L.BackupAbout(), L.ActionBackUp(), L.ActionRestore())},
		{pageSecurity, L.ResetTitle(), L.ResetAbout()},
		{pageSecurity, L.KeysMineTitle(), join(L.KeysGenerate(), L.KeysImport())},
		{pageSecurity, L.SavedTitle(), L.SavedForgetAll()},
		{pageSecurity, L.SettingsHideFromCapture(), L.HideFromCaptureAbout()},
		{pageSecurity, L.NoLogsTitle(), L.NoLogsAbout()},
		{pageSecurity, L.StorageSecrets(), ""},
		{pageSecurity, L.ProtectedTitle(), L.ProtectedAbout()},
		{pageRunMode, L.RunModeUser(), L.RunModeUserAbout()},
		{pageRunMode, L.RunModeFollow(), L.RunModeFollowAbout()},
		{pageRunMode, L.RunModeBackground(), L.RunModeBackgroundAbout()},
		{pageAI, L.AIModel(), L.AIModelAbout()},
		{pageAI, L.AIApproval(), L.AIApprovalAbout()},
		{pageAI, L.AISources(), L.AIAddSource()},
		{pageAI, L.AIWhoMay(), L.AIWhoMayAbout()},
		{pageAbout, L.SettingsLicenses(), L.AboutLicense()},
		{pageAbout, L.SettingsDiagnostics(), join(L.DiagLogs(), L.ActionSaveLogs(), L.StorageLocation())},
	}
	if mygo.Updater.Enabled() {
		hits = append(hits, settingsHit{pageAbout, L.SettingsCheckAppUpdates(), join(L.ActionCheckAppUpdate(), L.SectionUpdates())})
	}
	if runtime.GOOS == "darwin" {
		hits = append(hits, settingsHit{pageAccess, L.PermissionsTitle(),
			join(L.PermissionScreen(), L.PermissionAccessibility(), L.PermissionInputMonitoring(), L.PermissionKeychain())})
	}
	return hits
}

// searchSettings lists the settings whose title or words hold every word of
// q, ignoring case, titles that match first.
func searchSettings(all []settingsHit, q string) []settingsHit {
	words := strings.Fields(strings.ToLower(q))
	var byTitle, byWords []settingsHit
	for _, h := range all {
		title, more := strings.ToLower(h.title), strings.ToLower(h.words)
		inTitle, inAny := true, true
		for _, w := range words {
			inTitle = inTitle && strings.Contains(title, w)
			inAny = inAny && (strings.Contains(title, w) || strings.Contains(more, w))
		}
		switch {
		case inTitle:
			byTitle = append(byTitle, h)
		case inAny:
			byWords = append(byWords, h)
		}
	}
	return slices.Concat(byTitle, byWords)
}
