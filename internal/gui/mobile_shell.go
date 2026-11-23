package gui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

var mobBackIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 12H5"/><path d="m12 19-7-7 7-7"/></svg>`))

// MobileView is the root view of the Android controller app: full-screen
// active session when one is open, else the Material 3 adaptive Devices /
// Sessions / Settings shell with predictive back handling and bottom sheets.
func (a *App) MobileView(c *ui.Context) {
	if a.mobile == nil {
		a.initMobile()
	}
	ms := a.mobile
	mc := a.mobilePaletteFor(c)
	c.SetTheme(mobileUITheme(c.Theme(), mc))

	winW, _ := c.Size()
	wide := winW >= mobileWideMinW

	// Handle Android system Back / Escape in priority order:
	// open sheets/keyboard -> full-screen session -> detail/sub-page -> root.
	canPop := ms.clipSheetOpen || ms.aiSheetOpen || ms.aiHistOpen || ms.typeTextOpen || ms.menuSheetOpen ||
		ms.activeSession != "" || (ms.navTab == 0 && ms.selectedDev != "") ||
		(ms.navTab == 2 && !wide && ms.settingsOpen)
	if vUI := ms.views[ms.activeSession]; vUI != nil && vUI.kbOpen {
		canPop = true
	}
	if canPop && (c.Shortcut(0, ui.KeyBack) || c.Shortcut(0, ui.KeyEscape)) {
		a.mobileBack()
	}

	_, _, safeBottom, _ := c.SafeArea()
	imeOpen := safeBottom > mobileKeyboardThreshold

	a.mu.Lock()
	navPlacement := a.prefs.NavPlacement
	railRight := a.prefs.RailOnRight
	active := ms.activeSession
	a.mu.Unlock()
	if navPlacement == "" {
		navPlacement = "side_on_wide"
	}
	useSide := navPlacement == "side" || (navPlacement == "side_on_wide" && wide)

	if active != "" && a.mobileSessionCanvasDark(active) {
		c.Root().Background(ui.RGB(0, 0, 0))
	} else {
		c.Root().Background(mc.Surface)
	}

	ui.Column(c.Key("mob-root")).Fill().Children(func() {
		if active != "" {
			a.mobileActiveSessionView(c, mc, active, winW)
			a.toastView(c)
			a.mobileSheets(c, mc, wide)
			return
		}
		a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)

		openCount := a.mobileOpenSessionCount()
		badge := ""
		if openCount > 0 {
			badge = strconv.Itoa(openCount)
		}

		if useSide {
			ui.Row(c.Key("mob-rail-layout")).Fill().Children(func() {
				if !railRight {
					a.mobileRailView(c, mc, badge, false)
					ui.Box(c).Width(1).FillHeight().Background(mc.OutlineVariant.Alpha(0.35))
				}
				ui.Column(c.Key("mob-body")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
					a.mobileTabBody(c, mc, wide)
				})
				if railRight {
					ui.Box(c).Width(1).FillHeight().Background(mc.OutlineVariant.Alpha(0.35))
					a.mobileRailView(c, mc, badge, true)
				}
			})
		} else {
			ui.Column(c.Key("mob-compact-layout")).Fill().Children(func() {
				ui.Column(c.Key("mob-body")).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
					a.mobileTabBody(c, mc, wide)
				})
				if !imeOpen {
					if wide && navPlacement == "bottom" {
						a.mobileFloatingBottomBar(c, mc, badge, safeBottom)
					} else {
						a.mobileBottomBar(c, mc, badge, safeBottom)
					}
				}
			})
		}
		a.toastView(c)
		a.mobileSheets(c, mc, wide)
	})
}

func (a *App) mobileSessionCanvasDark(active string) bool {
	if a.mobile != nil && a.mobile.terms[active] != nil {
		return false
	}
	a.mu.Lock()
	v := a.viewers[active]
	svc := a.services[active]
	a.mu.Unlock()
	if v != nil {
		v.mu.Lock()
		ok := v.phase.State == "connected"
		v.mu.Unlock()
		return ok
	}
	if svc != nil {
		svc.mu.Lock()
		ok := svc.phase == svcRunning
		svc.mu.Unlock()
		return ok
	}
	return false
}

func (a *App) mobileBack() {
	ms := a.mobile
	if ms == nil {
		return
	}
	if ms.menuSheetOpen {
		ms.menuSheetOpen = false
		return
	}
	if ms.typeTextOpen {
		ms.typeTextOpen = false
		return
	}
	if ms.clipSheetOpen {
		ms.clipSheetOpen = false
		return
	}
	if ms.aiHistOpen {
		ms.aiHistOpen = false
		return
	}
	if ms.aiSheetOpen {
		ms.aiSheetOpen = false
		return
	}
	if ms.activeSession != "" {
		if v := ms.views[ms.activeSession]; v != nil && v.kbOpen {
			v.kbOpen = false
			v.kbAutoOpened = false
			return
		}
		ms.activeSession = ""
		a.mobileSyncSessions()
		a.homeVisibilityChanged()
		return
	}
	if ms.navTab == 0 && ms.selectedDev != "" {
		ms.selectedDev = ""
		a.homeVisibilityChanged()
		return
	}
	if ms.navTab == 2 && ms.settingsOpen {
		ms.settingsOpen = false
	}
}

type mobileNavItem struct {
	label string
	icon  *ui.SVG
	badge string
}

func mobileNavItems(sessionsBadge string) []mobileNavItem {
	return []mobileNavItem{
		{L.MobileNavDevices(), navHome, ""},
		{L.MobileNavSessions(), barDisplay, sessionsBadge},
		{L.MobileNavSettings(), shellGear, ""},
	}
}

func (a *App) selectMobileNavTab(idx int) {
	ms := a.mobile
	if idx == 2 && ms.navTab == 2 {
		ms.settingsOpen = false
	}
	ms.navTab = idx
	a.homeVisibilityChanged()
}

// mobileBottomBar renders the Material 3 bottom NavigationBar with animated
// capsule active indicators, Ink ripples, and edge-to-edge gesture clearance.
func (a *App) mobileBottomBar(c *ui.Context, mc mobileColors, sessionsBadge string, safeBottom float32) {
	ms := a.mobile
	items := mobileNavItems(sessionsBadge)
	ui.Column(c.Key("mob-bottom-wrap")).FillWidth().Shrink(0).Background(mc.SurfaceContainer).
		Margin(0, 0, -safeBottom, 0).Padding(0, 0, safeBottom, 0).Children(func() {
		ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.30))
		ui.Row(c.Key("mob-bottom-bar")).FillWidth().Height(mobileBottomBarH).
			PaddingX(10).Role(ui.RoleTabList).Children(func() {
			for i, it := range items {
				on := ms.navTab == i
				fg := mc.OnSurfaceVariant
				weight := 500
				if on {
					fg = mc.OnSecondaryContainer
					weight = 600
				}
				tab := ui.Column(c.Key("mob-nav-" + strconv.Itoa(i))).Grow(1).Basis(0).FillHeight().
					AlignItems(ui.Center).Justify(ui.Center).Gap(4).Radius(mobileNavPillRadius).Clip().
					Role(ui.RoleTab).Label(it.label).Checked(on)
				mobile.Ink(tab)
				tab.Children(func() {
					pill := ui.Box(c.Key("mob-nav-pill-"+strconv.Itoa(i))).Size(mobileNavPillW, mobileNavPillH).
						Radius(mobileNavPillRadius).Center()
					target := float32(0)
					if on {
						target = 1
					}
					alpha := pill.Animate("nav-pill", target, 180*time.Millisecond)
					if alpha > 0.01 {
						pill.Background(mc.SecondaryContainer.Alpha(alpha))
					}
					pill.Children(func() {
						ui.Icon(c, it.icon).FontSize(22).TextColor(fg)
						if it.badge != "" {
							ui.Text(c, it.badge).Absolute().Top(-2).Right(8).FontSize(10).FontWeight(700).
								Padding(1, 5).Radius(8).Background(mc.Primary).TextColor(mc.OnPrimary)
						}
					})
					ui.Text(c, it.label).FontSize(12).FontWeight(weight).TextColor(fg).NoWrap()
				})
				if tab.Clicked() {
					a.selectMobileNavTab(i)
				}
			}
		})
	})
}

// mobileFloatingBottomBar renders a centered Material 3 floating capsule dock
// when "Bottom" navigation placement is selected on wide/unfolded screens.
func (a *App) mobileFloatingBottomBar(c *ui.Context, mc mobileColors, sessionsBadge string, safeBottom float32) {
	ms := a.mobile
	items := mobileNavItems(sessionsBadge)
	botPad := max(safeBottom, 12)
	ui.Box(c.Key("mob-float-wrap")).FillWidth().Shrink(0).Padding(6, 16, botPad, 16).
		Margin(0, 0, -safeBottom, 0).AlignItems(ui.Center).Background(mc.Surface).Children(func() {
		dock := ui.Row(c.Key("mob-float-dock")).FillWidth().MaxWidth(420).Height(mobileFloatDockH).
			Padding(6, 8).Gap(6).Radius(mobileFloatDockH/2).Background(mc.SurfaceContainerHigh).
			Border(1, mc.OutlineVariant.Alpha(0.45)).Shadow(0, 6, 16, 0, ui.RGBA(0, 0, 0, 0.20)).
			Role(ui.RoleTabList)
		dock.Children(func() {
			for i, it := range items {
				on := ms.navTab == i
				fg := mc.OnSurfaceVariant
				weight := 500
				if on {
					fg = mc.OnSecondaryContainer
					weight = 600
				}
				tab := ui.Row(c.Key("mob-fnav-" + strconv.Itoa(i))).Grow(1).Basis(0).FillHeight().
					PaddingX(10).Gap(6).Radius((mobileFloatDockH - 12) / 2).AlignItems(ui.Center).
					Justify(ui.Center).Clip().Role(ui.RoleTab).Label(it.label).Checked(on)
				mobile.Ink(tab)
				target := float32(0)
				if on {
					target = 1
				}
				alpha := tab.Animate("fnav-pill", target, 180*time.Millisecond)
				if alpha > 0.01 {
					tab.Background(mc.SecondaryContainer.Alpha(alpha))
				}
				tab.Children(func() {
					ui.Icon(c, it.icon).FontSize(20).TextColor(fg)
					ui.Text(c, it.label).FontSize(13).FontWeight(weight).TextColor(fg).NoWrap()
					if it.badge != "" {
						ui.Text(c, it.badge).FontSize(10).FontWeight(700).Padding(1, 5).Radius(8).
							Background(mc.Primary).TextColor(mc.OnPrimary)
					}
				})
				if tab.Clicked() {
					a.selectMobileNavTab(i)
				}
			}
		})
	})
}

func (a *App) mobileRailView(c *ui.Context, mc mobileColors, sessionsBadge string, onRight bool) {
	ms := a.mobile
	top, right, bottom, left := c.SafeArea()
	railBot := bottom
	if railBot > mobileKeyboardThreshold {
		railBot = 0
	}
	items := mobileNavItems(sessionsBadge)
	rail := ui.Column(c.Key("mob-rail")).FillHeight().Gap(10).Shrink(0).
		AlignItems(ui.Center).Background(mc.SurfaceContainer).Role(ui.RoleTabList)
	if onRight {
		rail.Width(mobileRailW+right).Margin(-top, -right, -railBot, 0).Padding(14+top, 8+right, 14+railBot, 8)
	} else {
		rail.Width(mobileRailW+left).Margin(-top, 0, -railBot, -left).Padding(14+top, 8, 14+railBot, 8+left)
	}
	rail.Children(func() {
		markBox := ui.Box(c).Size(44, 44).Radius(14).Background(mc.PrimaryContainer).Center().Margin(2, 0, 10, 0)
		markBox.Children(func() {
			ui.Icon(c, appMark).FontSize(24).TextColor(mc.OnPrimaryContainer)
		})
		for i, it := range items {
			on := ms.navTab == i
			fg := mc.OnSurfaceVariant
			weight := 500
			if on {
				fg = mc.OnSecondaryContainer
				weight = 600
			}
			tab := ui.Column(c.Key("mob-rail-" + strconv.Itoa(i))).FillWidth().Height(60).Radius(mobileNavPillRadius).
				AlignItems(ui.Center).Justify(ui.Center).Gap(4).Clip().
				Role(ui.RoleTab).Label(it.label).Checked(on)
			mobile.Ink(tab)
			tab.Children(func() {
				pill := ui.Box(c.Key("mob-rail-pill-"+strconv.Itoa(i))).Size(56, 32).Radius(mobileNavPillRadius).Center()
				target := float32(0)
				if on {
					target = 1
				}
				alpha := pill.Animate("rail-pill", target, 180*time.Millisecond)
				if alpha > 0.01 {
					pill.Background(mc.SecondaryContainer.Alpha(alpha))
				}
				pill.Children(func() {
					ui.Icon(c, it.icon).FontSize(20).TextColor(fg)
					if it.badge != "" {
						ui.Text(c, it.badge).Absolute().Top(-2).Right(4).FontSize(10).FontWeight(700).
							Padding(1, 5).Radius(8).Background(mc.Primary).TextColor(mc.OnPrimary)
					}
				})
				ui.Text(c, it.label).FontSize(11).FontWeight(weight).TextColor(fg).NoWrap()
			})
			if tab.Clicked() {
				a.selectMobileNavTab(i)
			}
		}
	})
}

func (a *App) mobileTabBody(c *ui.Context, mc mobileColors, wide bool) {
	ui.Column(c.Key("mob-safe-top")).Fill().Children(func() {
		if !a.engineState(c) {
			return
		}
		switch a.mobile.navTab {
		case 0:
			a.mobileDevicesTab(c, mc, wide)
		case 1:
			a.mobileSessionsTab(c, mc)
		case 2:
			a.mobileSettingsTab(c, mc, wide)
		}
	})
}

// ---- Tab 0: Devices & Device Detail -----------------------------------------

func (a *App) mobileDevicesTab(c *ui.Context, mc mobileColors, wide bool) {
	ms := a.mobile
	if wide {
		ui.Row(c.Key("mob-dev-split")).Fill().Children(func() {
			ui.Column(c.Key("mob-dev-left")).Width(372).FillHeight().Children(func() {
				a.mobileDevicesList(c, mc, true)
			})
			ui.Box(c).Width(1).FillHeight().Background(mc.OutlineVariant.Alpha(0.35))
			ui.Column(c.Key("mob-dev-right")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
				if ms.selectedDev == "" {
					emptyState(c, "mob-pick-dev", func() { ui.Icon(c, navHome).FontSize(36).TextColor(mc.OnSurfaceVariant) }, L.MobileNoDeviceSelected(), "", nil)
				} else {
					a.mobileDeviceDetail(c, mc, ms.selectedDev, false)
				}
			})
		})
		return
	}
	if ms.selectedDev != "" {
		a.mobileDeviceDetail(c, mc, ms.selectedDev, true)
		return
	}
	a.mobileDevicesList(c, mc, false)
}

func (a *App) mobileDevicesList(c *ui.Context, mc mobileColors, masterPane bool) {
	t := c.Theme()
	ms := a.mobile

	a.mu.Lock()
	st := *a.status
	p := a.prefs
	f := a.connect
	rows := deviceRows(a.status, a.prefs)
	onlineMap := map[string]localapi.NetworkDevice{}
	for _, d := range st.Network.Devices {
		onlineMap[d.DeviceID] = d
	}
	watched := map[string]localapi.Watched{}
	for _, w := range st.Watching {
		watched[w.DeviceID] = w
	}
	ringsMap := make(map[string]hostRings, len(rows))
	for _, r := range rows {
		if rg := a.watch.rings[r.id]; rg != nil {
			ringsMap[r.id] = *rg
		}
	}
	a.mu.Unlock()

	word, col := a.networkState(t)
	m3TopBar(c, mc, mobile.TopBarOptions{
		Title: L.MobileNavDevices(),
		Trailing: func() {
			pill := m3StatusChip(c, mc, "mob-net-chip", word, col, true)
			if pill.Clicked() {
				ms.navTab = 2
				ms.settingsPage = mobSetNetworks
				ms.settingsOpen = true
				a.homeVisibilityChanged()
			}
		},
	})

	ui.Scroll(c.Key("mob-dev-scroll")).Grow(1).FillWidth().Padding(8, 16, 20, 16).Gap(12).Children(func() {
		// This device M3 tonal card.
		m3TonalCard(c, mc, "mob-self-card").Padding(14, 16).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
				badge := ui.Box(c).Size(44, 44).Radius(22).Background(mc.PrimaryContainer).Shrink(0).Center()
				badge.Children(func() { ui.Icon(c, appMark).FontSize(22).TextColor(mc.OnPrimaryContainer) })
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, L.MobileThisDevice(groupDigits(st.DeviceID))).FontSize(15).FontWeight(600).
						TextColor(mc.OnSurface).FontFeatures("tnum")
					if st.Name != "" {
						ui.Text(c, st.Name).FontSize(12).TextColor(mc.OnSurfaceVariant).SingleLine()
					}
				})
				if m3IconButton(c, "mob-copy-self", rowCopy, L.MobileCopy(), mc.OnSecondaryContainer, mc.SecondaryContainer).Clicked() {
					a.copyText(st.DeviceID)
					a.update(func() { a.setToast(L.MobileCopied(), false) })
				}
			})
		})

		if st.Persistence == "memory-only" {
			ui.Column(c.Key("mob-mem-warn")).FillWidth().Padding(14).Radius(mobileCardRadius).
				Background(mc.Warning.Alpha(0.14)).Gap(4).Children(func() {
				ui.Text(c, L.MobileMemoryOnly()).Bold().TextColor(textTone(t, mc.Warning))
				ui.Text(c, L.MobileMemoryOnlyHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
			})
		} else if st.Persistence == "unreadable" {
			ui.Row(c.Key("mob-unread-warn")).FillWidth().Padding(14).Radius(mobileCardRadius).
				Background(mc.ErrorContainer).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, L.SecretsUnreadable()).FontSize(12).TextColor(mc.OnErrorContainer).Grow(1)
				if m3TonalButton(c, mc, "mob-start-over", L.MobileForgetIdentity(), false).Height(40).Clicked() {
					a.startOver()
				}
			})
		}

		// Connect by ID or address M3 search-bar container.
		devID, addr := parseTarget(f.target)
		netOnline := st.Network.State == "online"
		ready := ((netOnline && devID != "") || addr != "") && !f.identifying
		ui.Column(c.Key("mob-connect-box")).FillWidth().Padding(12, 14).Radius(mobileCardRadius).
			Background(mc.SurfaceContainerHigh).Gap(10).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("mob-conn-in"), &f.target).Placeholder(L.MobileConnectHint()).Label(L.MobileConnectHint()).
					Grow(1).MinWidth(0).Radius(mobileBtnRadius).Background(mc.SurfaceContainerLowest).
					Border(0, ui.Transparent).FontFeatures("tnum").Disabled(f.identifying)
				if in.Changed() {
					a.mu.Lock()
					a.connect.target = f.target
					a.mu.Unlock()
				}
				if m3FilledButton(c, mc, "mob-conn-btn", L.MobileConnect(), !ready).Height(44).Clicked() || (in.Submitted() && ready) {
					a.addMobileRecentTarget(f.target)
					a.mobileConnectTarget(devID, addr)
				}
			})
			if len(p.MobileRecentTargets) > 0 {
				ui.ScrollHorizontal(c.Key("mob-recent-targets")).FillWidth().Gap(6).Children(func() {
					for i, rt := range p.MobileRecentTargets {
						rt := rt
						ch := ui.ButtonBase(c.Key("mob-rt-"+strconv.Itoa(i))).Height(32).Padding(0, 12).
							Radius(mobileChipRadius).Background(mc.SurfaceContainerLow).
							Border(1, mc.OutlineVariant.Alpha(0.5)).Center().Clip().Label(rt)
						mobile.Ink(ch)
						ch.Children(func() {
							ui.Text(c, rt).FontSize(12).FontWeight(500).TextColor(mc.OnSurfaceVariant).NoWrap()
						})
						if ch.Clicked() {
							a.mu.Lock()
							a.connect.target = rt
							a.mu.Unlock()
						}
					}
				})
			}
		})

		// Filter segmented control: All / Online / Offline.
		if len(rows) > 0 {
			m3Segmented(c, mc, "mob-dev-filter", &ms.filterMode, L.MobileFilterAll(), L.MobileFilterOnline(), L.MobileFilterOffline())
		}

		// Sort online devices first.
		sorted := slices.Clone(rows)
		slices.SortStableFunc(sorted, func(x, y deviceRow) int {
			_, xOn := onlineMap[x.id]
			_, yOn := onlineMap[y.id]
			if xOn != yOn {
				if xOn {
					return -1
				}
				return 1
			}
			return 0
		})

		filtered := slices.DeleteFunc(sorted, func(r deviceRow) bool {
			_, on := onlineMap[r.id]
			switch ms.filterMode {
			case 1:
				return !on && r.session == nil
			case 2:
				return on || r.session != nil
			default:
				return false
			}
		})

		if len(filtered) == 0 && len(rows) == 0 {
			hint := L.MobileNoDevicesHint()
			if len(st.Network.Networks) == 0 {
				hint = L.MobileNoDevicesNoNetwork()
			}
			emptyState(c, "mob-no-devs", func() { ui.Icon(c, navHome).FontSize(34).TextColor(mc.OnSurfaceVariant) }, L.MobileNoDevices(), hint, nil)
		}

		now := time.Now()
		for _, r := range filtered {
			r := r
			path, on := onlineMap[r.id]
			w, hasW := watched[r.id]
			rings := ringsMap[r.id]
			selected := masterPane && ms.selectedDev == r.id
			cardBg := mc.SurfaceContainerLow
			borderCol := mc.OutlineVariant.Alpha(0.45)
			var borderW float32 = 1
			if selected {
				cardBg = mc.SecondaryContainer.Alpha(0.55)
				borderCol = mc.Primary
				borderW = 1.5
			}
			card := ui.Column(c.Key("mob-dev-row-"+r.id)).FillWidth().Padding(14, 16).Gap(10).
				Radius(mobileCardRadius).Background(cardBg).Border(borderW, borderCol).Clip()
			mobile.Ink(card)
			card.Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					deviceAvatar(c, r.about)
					ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(3).Children(func() {
						title := r.about.Name
						if title == "" {
							title = groupDigits(r.id)
						}
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, title).FontSize(16).FontWeight(600).TextColor(mc.OnSurface).SingleLine().Grow(1).MinWidth(0)
							if on {
								m3StatusChip(c, mc, "st-"+r.id, L.CardOnline(), mc.Success, false)
							} else {
								m3StatusChip(c, mc, "st-"+r.id, L.MobileFilterOffline(), mc.OnSurfaceVariant, false)
							}
						})
						sub := groupDigits(r.id)
						if on && path.LatencyMS >= 0 {
							pName := L.MobilePathRelay()
							if path.Path == "direct" {
								pName = L.MobilePathDirect()
							}
							sub += fmt.Sprintf(" · %s %d ms", pName, path.LatencyMS)
						} else if osLabel := osName(r.about.OS); osLabel != "" {
							sub += " · " + osLabel
						}
						ui.Text(c, sub).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum").SingleLine()
					})
					if slices.Contains(r.features, "wake") {
						if m3IconButton(c, "mob-wake-"+r.id, shellSun, L.MobileActionWake(), mc.OnSurfaceVariant, mc.SurfaceContainerHigh).Clicked() {
							a.wakeHost(r.id)
						}
					}
					if m3IconButton(c, "mob-qdesk-"+r.id, kindDesktop, L.MobileOpenDesktop(), mc.OnPrimaryContainer, mc.PrimaryContainer).Clicked() {
						a.mobileOpenDesktop(r.id, "")
					}
				})
				if hasW {
					a.mobileCompactStatusStrip(c, mc, r.id, &w, &rings, now)
				}
			})
			if card.Clicked() {
				ms.selectedDev = r.id
				a.homeVisibilityChanged()
			}
		}

		// Others in my networks.
		if p.OthersInNetworks {
			others := networkOthers(st.Network.Networks, rows, st.DeviceID)
			if len(others) > 0 {
				ui.Text(c, L.MobileOthersTitle()).FontSize(14).FontWeight(600).TextColor(mc.OnSurfaceVariant).Margin(8, 0, 0, 2)
				for _, o := range others {
					o := o
					row := ui.Row(c.Key("mob-other-"+o.id)).FillWidth().MinHeight(64).Padding(12, 14).
						Radius(mobileCardRadius).Background(mc.SurfaceContainerLow).
						Border(1, mc.OutlineVariant).BorderStyle(ui.BorderDashed).AlignItems(ui.Center).Gap(12)
					row.Children(func() {
						ui.Box(c).Size(38, 38).Radius(19).Background(mc.SurfaceContainerHigh).Shrink(0).Center().Children(func() {
							ui.Icon(c, kindDesktop).FontSize(18).TextColor(mc.OnSurfaceVariant)
						})
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							name := o.path.Name
							if name == "" {
								name = groupDigits(o.id)
							}
							ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(mc.OnSurface).SingleLine()
							ui.Text(c, groupDigits(o.id)).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum").SingleLine()
						})
						if m3TonalButton(c, mc, "mob-other-conn-"+o.id, L.MobileConnect(), false).Height(40).Clicked() {
							a.mobileOpenDesktop(o.id, "")
						}
					})
				}
			}
		}
	})
}

// mobileCompactStatusStrip renders the 4-metric compact host telemetry strip
// inside a mobile device card, respecting freshness and staleness rules.
func (a *App) mobileCompactStatusStrip(c *ui.Context, mc mobileColors, devID string, w *localapi.Watched, rings *hostRings, now time.Time) {
	fresh := statusFreshness(w.ReceivedAt, now)
	if fresh == statusNone || w.Sample == nil {
		a.statusWords(c, w)
		return
	}
	figs := compactFigures(w.Sample, rings)
	col := ui.Column(c.Key("mob-cstats-"+devID)).FillWidth().Padding(10, 12).Radius(mobileTileRadius).
		Background(mc.SurfaceContainer).Gap(6)
	if fresh == statusDim {
		col.Opacity(0.55)
	}
	col.Children(func() {
		ui.Grid(c.Key("mob-cgrid-" + devID)).Columns(4).Gap(10).FillWidth().Children(func() {
			for _, f := range figs {
				ui.Column(c).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, f.name).FontSize(11).TextColor(mc.OnSurfaceVariant).SingleLine()
					ui.Row(c).Gap(2).AlignItems(ui.End).MinWidth(0).Children(func() {
						ui.Text(c, f.value).FontSize(14).FontWeight(700).TextColor(mc.OnSurface).FontFeatures("tnum").SingleLine()
						ui.Text(c, f.unit).FontSize(10).TextColor(mc.OnSurfaceVariant).SingleLine().Padding(0, 0, 1, 0)
					})
					ui.Box(c).FillWidth().Height(4).Radius(2).Background(mc.SurfaceContainerHighest).Children(func() {
						ui.Box(c).Height(4).WidthPercent(f.fill * 100).Radius(2).Background(f.col)
					})
				})
			}
		})
		if fresh == statusDim {
			age := int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())
			ui.Text(c, L.StatusUpdatedAgo(strconv.Itoa(age))).FontSize(11).TextColor(mc.OnSurfaceVariant)
		}
	})
}

func (a *App) mobileConnectTarget(device, address string) {
	if address != "" {
		a.identify(address, func(id, addr string) {
			a.mobileOpenDesktop(id, addr)
		})
		return
	}
	if device != "" {
		a.mobileOpenDesktop(device, "")
	}
}

func (a *App) mobileOpenDesktop(device, address string) {
	a.connectDevice(device, address, a.recentAbout(device))
	if a.mobile != nil {
		a.mobile.activeSession = device
		a.mobileSyncSessions()
		a.homeVisibilityChanged()
	}
}

func (a *App) mobileOpenNanokvm(device, address string) {
	key := a.connectNanokvm(device, address)
	if a.mobile != nil {
		a.mobile.activeSession = key
		a.mobileSyncSessions()
		a.homeVisibilityChanged()
	}
}

func (a *App) mobileOpenService(device, address, service string) {
	if service == serviceSSH {
		a.mobileStartSSH(device, address)
		return
	}
	key := a.openService(device, address, service)
	if a.mobile != nil {
		a.mobile.activeSession = key
		a.mobileSyncSessions()
		a.homeVisibilityChanged()
	}
}

func (a *App) mobileDeviceDetail(c *ui.Context, mc mobileColors, device string, showBack bool) {
	ms := a.mobile

	a.mu.Lock()
	st := *a.status
	p := a.prefs
	rows := deviceRows(a.status, a.prefs)
	var row *deviceRow
	for i := range rows {
		if rows[i].id == device {
			row = &rows[i]
			break
		}
	}
	var netDev localapi.NetworkDevice
	online := false
	for _, d := range st.Network.Devices {
		if d.DeviceID == device {
			netDev, online = d, true
			break
		}
	}
	var watched *localapi.Watched
	for i := range st.Watching {
		if st.Watching[i].DeviceID == device {
			watched = &st.Watching[i]
			break
		}
	}
	var rings hostRings
	if rg := a.watch.rings[device]; rg != nil {
		rings = *rg
	}
	a.mu.Unlock()

	if row == nil {
		m3TopBar(c, mc, mobile.TopBarOptions{
			Title: L.MobileNavDevices(),
			Leading: func() {
				if showBack {
					if m3IconButton(c, "mob-det-back", mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
						ms.selectedDev = ""
						a.homeVisibilityChanged()
					}
				}
			},
		})
		emptyState(c, "mob-dev-gone", func() { ui.Icon(c, navHome).FontSize(34).TextColor(mc.OnSurfaceVariant) }, L.MobileDeviceGone(), "", nil)
		return
	}

	r := *row
	title := r.about.Name
	if title == "" {
		title = groupDigits(r.id)
	}
	now := time.Now()
	fresh := statusNone
	var sample *localapi.SystemSample
	if watched != nil {
		fresh = statusFreshness(watched.ReceivedAt, now)
		if fresh != statusNone {
			sample = watched.Sample
		}
	}
	services := menuServices(sample, r.features)
	hasAI := len(r.features) == 0 || slices.Contains(r.features, "ai-sessions") || slices.Contains(r.features, "ai")

	m3TopBar(c, mc, mobile.TopBarOptions{
		Title: title,
		Leading: func() {
			if showBack {
				if m3IconButton(c, "mob-det-back", mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
					ms.selectedDev = ""
					a.homeVisibilityChanged()
				}
			}
		},
		Trailing: func() {
			if hasAI {
				if m3IconButton(c, "mob-det-aihist", navAI, L.MobileAiHistory(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
					ms.aiHistDevice = r.id
					ms.aiHistOpen = true
				}
			}
		},
	})

	ui.Scroll(c.Key("mob-det-scroll-"+r.id)).Grow(1).FillWidth().Padding(8, 16, 28, 16).Gap(14).Children(func() {
		// Hero identity & summary card.
		m3TonalCard(c, mc, "mob-det-head").Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
				deviceAvatar(c, r.about)
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, title).FontSize(18).FontWeight(700).TextColor(mc.OnSurface).SingleLine()
					var meta []string
					meta = append(meta, groupDigits(r.id))
					if osLabel := osName(r.about.OS); osLabel != "" {
						meta = append(meta, osLabel)
					}
					if r.about.Model != "" {
						meta = append(meta, r.about.Model)
					}
					ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum").SingleLine()
				})
				if online {
					m3StatusChip(c, mc, "det-st-"+r.id, L.CardOnline(), mc.Success, false)
				} else {
					m3StatusChip(c, mc, "det-st-"+r.id, L.MobileFilterOffline(), mc.OnSurfaceVariant, false)
				}
			})
			var subChips []string
			if online && netDev.LatencyMS >= 0 {
				pName := L.MobilePathRelay()
				if netDev.Path == "direct" {
					pName = L.MobilePathDirect()
				}
				subChips = append(subChips, fmt.Sprintf("%s · %d ms", pName, netDev.LatencyMS))
			}
			if sample != nil && sample.UptimeS > 0 {
				subChips = append(subChips, L.StatusUpFor(uptime(sample.UptimeS)))
			}
			if len(subChips) > 0 {
				ui.Text(c, strings.Join(subChips, " · ")).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum")
			}
		})

		// Primary & service actions card.
		m3Card(c, mc, "mob-det-actions").Children(func() {
			if m3FilledButton(c, mc, "mob-act-desk-"+r.id, L.MobileOpenDesktop(), false).FillWidth().Clicked() {
				a.mobileOpenDesktop(r.id, "")
			}
			hasWake := slices.Contains(r.features, "wake")
			if hasAI || hasWake {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if hasAI {
						if m3TonalButton(c, mc, "mob-act-ai-"+r.id, L.MobileAiTask(), false).Grow(1).Basis(0).Clicked() {
							_ = a.useAILink(r.id, "")
							ms.aiSheetDevice = r.id
							ms.aiSheetOpen = true
							a.mobileSyncSessions()
						}
					}
					if hasWake {
						if m3TonalButton(c, mc, "mob-act-wake-"+r.id, L.MobileActionWake(), false).Grow(1).Basis(0).Clicked() {
							a.wakeHost(r.id)
						}
					}
				})
			}
			if len(services) > 0 {
				ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
					for _, svc := range services {
						switch svc {
						case serviceSSH:
							if m3OutlinedButton(c, mc, "mob-act-ssh-"+r.id, L.MobileServiceSsh(), mc.OnSurface, false).Grow(1).MinWidth(130).Clicked() {
								a.mobileOpenService(r.id, "", serviceSSH)
							}
						case serviceRDP:
							if m3OutlinedButton(c, mc, "mob-act-rdp-"+r.id, L.MobileServiceRdp(), mc.OnSurface, false).Grow(1).MinWidth(130).Clicked() {
								a.mobileOpenService(r.id, "", serviceRDP)
							}
						case serviceVNC:
							if m3OutlinedButton(c, mc, "mob-act-vnc-"+r.id, L.MobileServiceVnc(), mc.OnSurface, false).Grow(1).MinWidth(100).Clicked() {
								a.mobileOpenService(r.id, "", serviceVNC)
							}
						case serviceNanokvm:
							if m3OutlinedButton(c, mc, "mob-act-kvm-"+r.id, L.MobileNanokvm(), mc.OnSurface, false).Grow(1).MinWidth(120).Clicked() {
								a.mobileOpenNanokvm(r.id, "")
							}
						}
					}
				})
			}
		})

		// Full Pulse-style Host Status Dashboard.
		a.mobileDeviceStatusSection(c, mc, r, online, watched, &rings, fresh, now)

		// Routes section.
		onNet := len(st.Network.Networks) > 0
		views := routeViews(r, netDev, online, onNet)
		m3Card(c, mc, "mob-det-routes").Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.MobileSectionRoutes()).FontSize(15).FontWeight(600).TextColor(mc.OnSurface).Grow(1)
				if m3IconButton(c, "mob-route-probe-"+r.id, rowRefresh, "Refresh", mc.OnSurfaceVariant, mc.SurfaceContainer).Clicked() {
					devs := []string{r.id}
					go func() {
						_, _ = a.requestWith(localapi.Request{Type: localapi.RequestRoutesProbe, Probe: &devs})
					}()
				}
			})
			routeChoice := func(id, label, sub string, chosen bool, pick func(), del func()) {
				row := ui.Row(c.Key(id)).FillWidth().MinHeight(50).Padding(8, 12).Radius(mobileTileRadius).
					AlignItems(ui.Center).Gap(10).Clip()
				mobile.Ink(row)
				if chosen {
					row.Background(mc.SecondaryContainer.Alpha(0.65))
				}
				row.Children(func() {
					radio := ui.Box(c).Size(20, 20).Radius(10).Border(2, mc.Primary).Shrink(0).Center()
					if chosen {
						radio.Children(func() { ui.Box(c).Size(10, 10).Radius(5).Background(mc.Primary) })
					}
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, label).FontWeight(500).TextColor(mc.OnSurface)
						if sub != "" {
							ui.Text(c, sub).FontSize(12).TextColor(mc.OnSurfaceVariant)
						}
					})
					if del != nil {
						if m3IconButton(c, id+"-del", paneClose, L.MobileRemove(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
							del()
						}
					}
				})
				if row.Clicked() {
					pick()
				}
			}
			routeChoice("mob-rt-auto-"+r.id, L.MobileRouteAuto(), L.MobileRouteAutoHint(), r.prefer == "", func() {
				go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Prefer: ptr("")})
			}, nil)
			for i, rv := range views {
				rv := rv
				want := rv.address
				lbl := rv.address
				if rv.address == "" {
					want = preferNetwork
					lbl = L.MobileRouteNetwork()
				}
				var delFn func()
				if rv.address != "" {
					addr := rv.address
					delFn = func() {
						go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Remove: addr})
					}
				}
				routeChoice("mob-rt-v-"+strconv.Itoa(i)+"-"+r.id, lbl, routeText(rv), r.prefer == want, func() {
					go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Prefer: ptr(want)})
				}, delFn)
			}
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("mob-rt-add-"+r.id), &ms.routeAddAddr).Placeholder(L.MobileRouteAddHint()).
					Label(L.MobileRouteAdd()).Grow(1).MinWidth(0)
				if m3TonalButton(c, mc, "mob-rt-add-btn-"+r.id, "+", strings.TrimSpace(ms.routeAddAddr) == "").Height(44).Clicked() {
					addr := strings.TrimSpace(ms.routeAddAddr)
					ms.routeAddAddr = ""
					go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Add: addr})
				}
			})
		})

		// Saved sign-ins & Remove device.
		if ms.aliasDev != r.id {
			ms.aliasDev = r.id
			ms.aliasDirty = false
		}
		if !ms.aliasDirty {
			ms.aliasEdit = p.Aliases[r.id]
		}
		m3Card(c, mc, "mob-det-saved").Children(func() {
			ui.Text(c, L.MobileSectionSignIn()).FontSize(15).FontWeight(600).TextColor(mc.OnSurface)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("mob-alias-"+r.id), &ms.aliasEdit).Placeholder(L.MobileDeviceName()).
					Label(L.MobileDeviceName()).Grow(1).MinWidth(0)
				if in.Changed() {
					ms.aliasDirty = true
				}
				if in.Submitted() || m3TonalButton(c, mc, "mob-alias-save-"+r.id, L.MobileSave(), false).Height(44).Clicked() {
					ms.aliasDirty = false
					a.setAlias(r.id, strings.TrimSpace(ms.aliasEdit))
				}
			})
			if r.saved.Password || r.saved.Key {
				ui.Row(c).FillWidth().MinHeight(46).AlignItems(ui.Center).Children(func() {
					lbl := L.MobileSavedPassword()
					if r.saved.Key && !r.saved.Password {
						lbl = L.MobileSignInKey() + " (" + L.MobileKeyAdmitted() + ")"
					}
					ui.Text(c, lbl).Grow(1).TextColor(mc.OnSurface)
					if m3OutlinedButton(c, mc, "mob-forget-pw-"+r.id, L.MobileForget(), mc.OnSurface, false).Height(40).Clicked() {
						a.forgetPassword(r.id)
					}
				})
			}
			for _, svc := range []struct {
				id   string
				has  bool
				name string
			}{
				{serviceSSH, r.saved.SSH, L.MobileServiceSsh()},
				{serviceRDP, r.saved.RDP, L.MobileServiceRdp()},
				{serviceVNC, r.saved.VNC, L.MobileServiceVnc()},
			} {
				if !svc.has {
					continue
				}
				svcID := svc.id
				ui.Row(c).FillWidth().MinHeight(46).AlignItems(ui.Center).Children(func() {
					ui.Text(c, svc.name).Grow(1).TextColor(mc.OnSurface)
					if m3OutlinedButton(c, mc, "mob-forget-"+svcID+"-"+r.id, L.MobileForget(), mc.OnSurface, false).Height(40).Clicked() {
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestCredentialsForget, DeviceID: r.id, Service: svcID})
						}()
					}
				})
			}
			ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35)).Margin(4, 0)
			rm := ui.ButtonBase(c.Key("mob-rm-dev-" + r.id)).FillWidth().Height(46).Radius(mobileBtnRadius).
				Background(mc.ErrorContainer).Center().Clip().Label(L.MobileRemoveDevice())
			mobile.Ink(rm)
			rm.Children(func() {
				ui.Text(c, L.MobileRemoveDevice()).FontSize(14).FontWeight(600).TextColor(mc.OnErrorContainer)
			})
			if rm.Clicked() {
				if a.confirm(L.MobileRemoveDeviceHint(title), L.MobileRemove()) {
					a.removeDevice(r.id)
					ms.selectedDev = ""
					a.homeVisibilityChanged()
				}
			}
		})
	})
}

// mobileDeviceStatusSection renders the full Pulse-style host status dashboard
// on the Device Detail screen, showing live metrics, sub-readings, and 2-minute
// dot histories when fresh samples exist, or clear freshness/credential wording.
func (a *App) mobileDeviceStatusSection(c *ui.Context, mc mobileColors, r deviceRow, online bool, w *localapi.Watched, rings *hostRings, fresh freshness, now time.Time) {
	m3Card(c, mc, "mob-det-status-"+r.id).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, L.MobileSectionStatus()).FontSize(15).FontWeight(600).TextColor(mc.OnSurface).Grow(1)
			if w != nil && w.Sample != nil && fresh == statusShown {
				ui.Text(c, L.StatusEvery()).FontSize(12).TextColor(mc.OnSurfaceVariant)
			} else if w != nil && w.Sample != nil && fresh == statusDim {
				age := int(now.Sub(time.UnixMilli(w.ReceivedAt)).Seconds())
				ui.Text(c, L.StatusUpdatedAgo(strconv.Itoa(age))).FontSize(12).TextColor(mc.OnSurfaceVariant)
			}
		})

		if w == nil || w.Sample == nil || fresh == statusNone {
			if w != nil {
				a.statusWords(c, w)
			} else if !r.saved.Password && !r.saved.Key {
				ui.Text(c, L.MobileStatusNeedsSignIn()).FontSize(13).TextColor(mc.OnSurfaceVariant)
			} else if !online {
				ui.Text(c, L.MobileOfflineHint()).FontSize(13).TextColor(mc.OnSurfaceVariant)
			} else {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					busyMark(c, L.StatusGetting(), busySize)
					ui.Text(c, L.StatusGetting()).FontSize(13).TextColor(mc.OnSurfaceVariant)
				})
			}
			return
		}

		metrics := hostMetrics(w.Sample, rings)
		box := ui.Column(c.Key("mob-stat-box-" + r.id)).FillWidth()
		boxW := box.LayoutBounds().W
		cols := 2
		if boxW > 0 && boxW < 330 {
			cols = 1
		} else if boxW >= 760 {
			cols = 3
		}
		var tracks []ui.Track
		if boxW > 0 {
			tileW := max((boxW-10*float32(cols-1))/float32(cols), 120)
			tracks = make([]ui.Track, cols)
			for i := range tracks {
				tracks[i] = ui.Fixed(tileW)
			}
		} else {
			tracks = make([]ui.Track, cols)
			for i := range tracks {
				tracks[i] = ui.Fr(1)
			}
		}
		box.Children(func() {
			g := ui.Grid(c.Key("mob-stat-grid-" + r.id)).ColumnTracks(tracks...).Gap(10).FillWidth()
			if fresh == statusDim {
				g.Opacity(0.55)
			}
			g.Children(func() {
				for _, m := range metrics {
					ui.Column(c.Key("mob-mt-" + r.id + "-" + m.key)).MinWidth(0).Padding(12).Gap(6).
						Radius(mobileTileRadius).Background(mc.SurfaceContainer).Children(func() {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, m.icon).Size(15, 15).TextColor(m.col)
							ui.Text(c, m.name).FontSize(12).FontWeight(700).TextColor(m.col).SingleLine().Grow(1).MinWidth(0)
						})
						ui.Text(c, m.caption).FontSize(11).TextColor(mc.OnSurfaceVariant).SingleLine()
						ui.Row(c).Gap(3).AlignItems(ui.End).Children(func() {
							ui.Text(c, m.value).FontSize(22).FontWeight(700).TextColor(mc.OnSurface).FontFeatures("tnum").SingleLine()
							ui.Text(c, m.unit).FontSize(12).TextColor(mc.OnSurfaceVariant).SingleLine().Padding(0, 0, 2, 0)
						})
						if len(m.subs) > 0 {
							ui.Row(c).FillWidth().Gap(6).Children(func() {
								for _, sub := range m.subs {
									ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(1).Children(func() {
										ui.Text(c, sub[0]).FontSize(10).TextColor(mc.OnSurfaceVariant).SingleLine()
										ui.Text(c, sub[1]).FontSize(12).FontWeight(600).TextColor(mc.OnSurface).FontFeatures("tnum").SingleLine()
									})
								}
							})
						}
						dotHistory(c, m.series, m.top, 4, m.col)
					})
				}
			})
		})
	})
}

// ---- Tab 1: Open Sessions ---------------------------------------------------

func (a *App) mobileSessionsTab(c *ui.Context, mc mobileColors) {
	ms := a.mobile

	m3TopBar(c, mc, mobile.TopBarOptions{
		Title: L.MobileNavSessions(),
		Trailing: func() {
			if m3IconButton(c, "mob-sess-clip", barClipboard, L.MobileClipHistory(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
				ms.clipSheetOpen = true
			}
		},
	})

	type termEntry struct {
		key string
		ts  *mobileTermSession
	}
	a.mu.Lock()
	viewers := make([]*viewer, 0, len(a.viewers))
	for _, v := range a.viewers {
		viewers = append(viewers, v)
	}
	services := make([]*serviceTab, 0, len(a.services))
	for _, s := range a.services {
		services = append(services, s)
	}
	links := make([]*aiLink, 0, len(a.aiLinks))
	for _, l := range a.aiLinks {
		links = append(links, l)
	}
	terms := make([]termEntry, 0, len(ms.terms))
	for k, ts := range ms.terms {
		terms = append(terms, termEntry{k, ts})
	}
	a.mu.Unlock()

	slices.SortFunc(viewers, func(x, y *viewer) int { return cmp.Compare(x.tabKey(), y.tabKey()) })
	slices.SortFunc(services, func(x, y *serviceTab) int { return cmp.Compare(x.tabKey(), y.tabKey()) })
	slices.SortFunc(links, func(x, y *aiLink) int { return cmp.Compare(x.device, y.device) })
	slices.SortFunc(terms, func(x, y termEntry) int { return cmp.Compare(x.key, y.key) })

	if len(viewers) == 0 && len(services) == 0 && len(links) == 0 && len(terms) == 0 {
		emptyState(c, "mob-no-sess", func() { ui.Icon(c, barDisplay).FontSize(36).TextColor(mc.OnSurfaceVariant) }, L.MobileNoSessions(), "", nil)
		return
	}

	ui.Scroll(c.Key("mob-sess-scroll")).Grow(1).FillWidth().Padding(8, 16, 24, 16).Gap(10).Children(func() {
		sessRow := func(key, title, kind, state string, icon *ui.SVG, onOpen, onClose func()) {
			row := ui.Row(c.Key("mob-srow-"+key)).FillWidth().MinHeight(70).Padding(12, 16).
				Radius(mobileCardRadius).Background(mc.SurfaceContainerLow).
				Border(1, mc.OutlineVariant.Alpha(0.45)).AlignItems(ui.Center).Gap(12).Clip()
			mobile.Ink(row)
			row.Children(func() {
				badge := ui.Box(c).Size(44, 44).Radius(22).Background(mc.PrimaryContainer).Shrink(0).Center()
				badge.Children(func() { ui.Icon(c, icon).FontSize(20).TextColor(mc.OnPrimaryContainer) })
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(mc.OnSurface).SingleLine()
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, kind).FontSize(12).TextColor(mc.OnSurfaceVariant).SingleLine()
						col := mc.OnSurfaceVariant
						if state == "connected" {
							col = mc.Success
						} else if state == "connecting" || state == "confirming" || state == "reconnecting" {
							col = mc.Warning
						}
						m3StatusChip(c, mc, "sr-"+key, state, col, false)
					})
				})
				if m3IconButton(c, "mob-sclose-"+key, paneClose, L.MobileDisconnect(), mc.OnSurfaceVariant, mc.SurfaceContainerHigh).Clicked() {
					onClose()
				}
			})
			if row.Clicked() {
				onOpen()
			}
		}

		for _, v := range viewers {
			v := v
			kind := L.MobileDesktop()
			ic := kindDesktop
			if v.kvm {
				kind = L.MobileNanokvm()
				ic = kindNanokvm
			}
			v.mu.Lock()
			st := v.phase.State
			v.mu.Unlock()
			sessRow(v.tabKey(), v.title, kind, st, ic, func() {
				ms.activeSession = v.tabKey()
				a.mobileSyncSessions()
			}, func() {
				v.disconnect()
				a.mobileSyncSessions()
			})
		}

		for _, te := range terms {
			key, ts := te.key, te.ts
			ts.mu.Lock()
			st := ts.state
			ts.mu.Unlock()
			sessRow(key, ts.name, L.MobileServiceSsh(), st, kindSSH, func() {
				ms.activeSession = key
				a.mobileSyncSessions()
			}, func() {
				a.mobileCloseSSH(key)
				a.mobileSyncSessions()
			})
		}

		for _, s := range services {
			s := s
			s.mu.Lock()
			st := s.phase
			s.mu.Unlock()
			ic := kindRDP
			if s.service == serviceVNC {
				ic = kindVNC
			}
			sessRow(s.tabKey(), s.name, s.kind, st, ic, func() {
				ms.activeSession = s.tabKey()
				a.mobileSyncSessions()
			}, func() {
				s.disconnect()
				a.mobileSyncSessions()
			})
		}

		for _, l := range links {
			l := l
			snap := l.snapshot()
			sessRow("ailink-"+l.device, groupDigits(l.device), L.MobileAiTask(), snap.phase.State, navAI, func() {
				ms.aiSheetDevice = l.device
				ms.aiSheetOpen = true
			}, func() {
				a.releaseAILink(l.device)
				a.mobileSyncSessions()
			})
		}
	})
}

// ---- Tab 2: Settings, Networks, Keys, Input, Appearance, Diagnostics --------

func (a *App) mobileSettingsTab(c *ui.Context, mc mobileColors, wide bool) {
	t := c.Theme()
	ms := a.mobile
	a.mu.Lock()
	st := *a.status
	p := a.prefs
	keysCount := len(a.keys.mine)
	a.mu.Unlock()

	netWord, _ := a.networkState(t)
	netSub := netWord
	if n := len(st.Network.Networks); n > 0 {
		netSub = netWord + " · " + L.MobileSettingsSummaryNetworks(n)
	}
	inSub := L.MobilePointerTrackpad()
	if p.PointerMode == "touch" {
		inSub = L.MobilePointerTouch()
	}
	if p.AutoKeyboard == nil || *p.AutoKeyboard {
		inSub += " · " + L.MobileAutoKeyboard()
	}
	appSub := map[string]string{"system": L.MobileThemeSystem(), "light": L.MobileThemeLight(), "dark": L.MobileThemeDark()}[p.Theme]
	if appSub == "" {
		appSub = L.MobileThemeSystem()
	}
	navSub := map[string]string{
		"bottom":       L.MobileSettingsNavPlacementBottom(),
		"side_on_wide": L.MobileSettingsNavPlacementSideOnWide(),
		"side":         L.MobileSettingsNavPlacementSide(),
	}[p.NavPlacement]
	if navSub == "" {
		navSub = L.MobileSettingsNavPlacementSideOnWide()
	}
	appSub += " · " + navSub

	cats := []struct {
		p     mobileSettingsPage
		label string
		sub   string
		icon  *ui.SVG
	}{
		{mobSetDevice, L.MobileSectionThisDevice(), groupDigits(st.DeviceID), rowID},
		{mobSetNetworks, L.MobileNetworks(), netSub, navNetwork},
		{mobSetKeys, L.MobileSignInKeys(), L.MobileSettingsSummaryKeys(keysCount), navSecurity},
		{mobSetInput, L.MobileSectionInput(), inSub, rowMouse},
		{mobSetAppearance, L.MobileSectionAppearance(), appSub, shellAuto},
		{mobSetDiagnostics, L.MobileDiagnostics(), L.MobileDiagnosticsHint(), rowLog},
		{mobSetAbout, L.MobileSectionAbout(), version.Version, navAbout},
	}

	if wide {
		ui.Row(c.Key("mob-set-split")).Fill().Children(func() {
			ui.Column(c.Key("mob-set-left")).Width(296).FillHeight().Children(func() {
				m3TopBar(c, mc, mobile.TopBarOptions{Title: L.MobileNavSettings()})
				ui.Scroll(c.Key("mob-set-cats")).Grow(1).FillWidth().Padding(8, 12).Gap(4).Children(func() {
					for _, cat := range cats {
						cat := cat
						on := ms.settingsPage == cat.p
						row := ui.Row(c.Key("mob-cat-"+strconv.Itoa(int(cat.p)))).FillWidth().MinHeight(56).
							Padding(8, 14).Radius(28).AlignItems(ui.Center).Gap(12).Clip()
						mobile.Ink(row)
						fg := mc.OnSurface
						icCol := mc.OnSurfaceVariant
						if on {
							row.Background(mc.SecondaryContainer)
							fg = mc.OnSecondaryContainer
							icCol = mc.OnSecondaryContainer
						}
						row.Children(func() {
							ui.Icon(c, cat.icon).FontSize(20).TextColor(icCol)
							ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
								ui.Text(c, cat.label).FontWeight(600).TextColor(fg).SingleLine()
								ui.Text(c, cat.sub).FontSize(11).TextColor(mc.OnSurfaceVariant).SingleLine()
							})
						})
						if row.Clicked() {
							ms.settingsPage = cat.p
						}
					}
				})
			})
			ui.Box(c).Width(1).FillHeight().Background(mc.OutlineVariant.Alpha(0.35))
			ui.Column(c.Key("mob-set-right")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
				a.mobileSettingsContent(c, mc, ms.settingsPage)
			})
		})
		return
	}

	if !ms.settingsOpen {
		m3TopBar(c, mc, mobile.TopBarOptions{Title: L.MobileNavSettings()})
		ui.Scroll(c.Key("mob-set-menu")).Grow(1).FillWidth().Padding(10, 16, 24, 16).Gap(8).Children(func() {
			for _, cat := range cats {
				cat := cat
				row := ui.Row(c.Key("mob-smenu-"+strconv.Itoa(int(cat.p)))).FillWidth().MinHeight(64).
					Padding(12, 16).Radius(mobileCardRadius).Background(mc.SurfaceContainerLow).
					Border(1, mc.OutlineVariant.Alpha(0.45)).AlignItems(ui.Center).Gap(12).Clip()
				mobile.Ink(row)
				row.Children(func() {
					badge := ui.Box(c).Size(40, 40).Radius(20).Background(mc.PrimaryContainer).Shrink(0).Center()
					badge.Children(func() { ui.Icon(c, cat.icon).FontSize(19).TextColor(mc.OnPrimaryContainer) })
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, cat.label).FontSize(15).FontWeight(600).TextColor(mc.OnSurface).SingleLine()
						ui.Text(c, cat.sub).FontSize(12).TextColor(mc.OnSurfaceVariant).SingleLine()
					})
					ui.Icon(c, aiChevronRight).FontSize(16).TextColor(mc.OnSurfaceVariant)
				})
				if row.Clicked() {
					ms.settingsPage = cat.p
					ms.settingsOpen = true
				}
			}
		})
		return
	}

	curTitle := L.MobileNavSettings()
	for _, cat := range cats {
		if cat.p == ms.settingsPage {
			curTitle = cat.label
			break
		}
	}
	m3TopBar(c, mc, mobile.TopBarOptions{
		Title: curTitle,
		Leading: func() {
			if m3IconButton(c, "mob-set-back", mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
				ms.settingsOpen = false
			}
		},
	})
	ui.Column(c.Key("mob-set-body")).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
		a.mobileSettingsContent(c, mc, ms.settingsPage)
	})
}

func (a *App) mobileSettingsContent(c *ui.Context, mc mobileColors, pg mobileSettingsPage) {
	ms := a.mobile
	a.mu.Lock()
	st := *a.status
	p := a.prefs
	a.mu.Unlock()

	switch pg {
	case mobSetNetworks:
		ui.Scroll(c.Key("mob-pg-net")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(12).Children(func() {
			a.mu.Lock()
			loaded, busy := a.net.loaded, a.busy["network"]
			a.mu.Unlock()
			if !loaded && !busy {
				a.loadNetworks()
			}
			a.networkSummary(c)
			a.networksSection(c)
			a.advancedSection(c)
		})
	case mobSetKeys:
		ui.Scroll(c.Key("mob-pg-keys")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(12).Children(func() {
			a.keysSettings(c)
		})
	case mobSetDevice:
		if !ms.devNameDirty {
			ms.devNameEdit = st.Name
		}
		ui.Scroll(c.Key("mob-pg-dev")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			m3Card(c, mc, "mob-id-box").Children(func() {
				ui.Text(c, L.MobileDeviceId()).FontSize(12).TextColor(mc.OnSurfaceVariant)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, groupDigits(st.DeviceID)).FontSize(24).FontWeight(700).TextColor(mc.OnSurface).FontFeatures("tnum").Grow(1)
					if m3TonalButton(c, mc, "mob-set-copyid", L.MobileCopy(), false).Height(42).Clicked() {
						a.copyText(st.DeviceID)
						a.update(func() { a.setToast(L.MobileCopied(), false) })
					}
				})
				ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))
				ui.Text(c, L.MobileDeviceName()).FontSize(12).TextColor(mc.OnSurfaceVariant)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					in := ui.TextInput(c.Key("mob-devname-in"), &ms.devNameEdit).
						Placeholder(strings.TrimSpace(ms.info.Manufacturer + " " + ms.info.Model)).Grow(1).MinWidth(0)
					if in.Changed() {
						ms.devNameDirty = true
					}
					if m3TonalButton(c, mc, "mob-devname-save", L.MobileSave(), false).Height(42).Clicked() || in.Submitted() {
						nm := strings.TrimSpace(ms.devNameEdit)
						ms.devNameDirty = false
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestNameSet, Name: &nm})
						}()
					}
				})
				pub := st.PublishName
				ui.Row(c).FillWidth().MinHeight(50).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.SettingsNamePublish()).TextColor(mc.OnSurface).Grow(1)
					if m3Switch(c, "mob-pub-name", &pub, L.SettingsNamePublish()) {
						on := pub
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestNameSet, PublishName: &on})
						}()
					}
				})
				noLogs := p.NoLogs
				ui.Row(c).FillWidth().MinHeight(50).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.NoLogsTitle()).TextColor(mc.OnSurface).Grow(1)
					if m3Switch(c, "mob-nologs", &noLogs, L.NoLogsTitle()) {
						a.setNoLogs(p, noLogs)
					}
				})
			})
			a.mobileIdentityBackupCard(c, mc, st.DeviceID)
		})

	case mobSetInput:
		ui.Scroll(c.Key("mob-pg-input")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			m3Card(c, mc, "mob-in-card").Gap(14).Children(func() {
				pmIdx := 0
				if p.PointerMode == "touch" {
					pmIdx = 1
				}
				ui.Text(c, L.ViewerPointer()).FontWeight(600).TextColor(mc.OnSurface)
				if m3Segmented(c, mc, "mob-pmode", &pmIdx, L.MobilePointerTrackpad(), L.MobilePointerTouch()) {
					if pmIdx == 1 {
						p.PointerMode = "touch"
					} else {
						p.PointerMode = "trackpad"
					}
					a.setPrefs(p)
				}

				spd := float64(p.PointerSpeed)
				if spd <= 0 {
					spd = 1.6
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobilePointerSpeed()).TextColor(mc.OnSurface).Grow(1)
					ui.Text(c, fmt.Sprintf("%.1f×", spd)).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum")
				})
				if ui.Slider(c.Key("mob-pspeed"), &spd, 0.5, 3.0).Step(0.1).FillWidth().Changed() {
					p.PointerSpeed = float32(spd)
					a.setPrefs(p)
				}

				csz64 := float64(p.CursorSize)
				if csz64 <= 0 {
					csz64 = 16
				}
				csz := float32(csz64)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileCursorSize()).TextColor(mc.OnSurface).Grow(1)
					ui.Text(c, fmt.Sprintf("%d dp", int(csz64))).FontSize(12).TextColor(mc.OnSurfaceVariant).FontFeatures("tnum")
				})
				if ui.Slider(c.Key("mob-csize"), &csz64, 10, 34).Step(1).FillWidth().Changed() {
					p.CursorSize = float32(csz64)
					a.setPrefs(p)
				}
				ui.Row(c.Key("mob-csize-prev")).FillWidth().Height(48).Radius(mobileTileRadius).
					Background(ui.RGB(20, 24, 28)).AlignItems(ui.Center).Justify(ui.SpaceAround).PaddingX(16).Children(func() {
					ui.Text(c, "S").FontSize(11).TextColor(ui.RGB(160, 160, 160))
					ui.Box(c).Size(csz, csz).Radius(csz/2).Background(ui.RGBA(255, 255, 255, 0.9)).Border(2, mc.Primary)
					ui.Text(c, "M").FontSize(11).TextColor(ui.RGB(160, 160, 160))
					ui.Text(c, "L").FontSize(11).TextColor(ui.RGB(160, 160, 160))
				})

				ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))

				autoKB := p.autoKeyboardOn()
				ui.Row(c).FillWidth().MinHeight(52).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobileAutoKeyboard()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobileAutoKeyboardHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-autokb", &autoKB, L.MobileAutoKeyboard()) {
						v := autoKB
						p.AutoKeyboard = &v
						a.setPrefs(p)
					}
				})

				splitKB := p.SplitKeyboard
				ui.Row(c).FillWidth().MinHeight(52).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobileSplitKeyboard()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobileSplitKeyboardHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-splitkb", &splitKB, L.MobileSplitKeyboard()) {
						p.SplitKeyboard = splitKB
						a.setPrefs(p)
					}
				})

				picTop := p.PictureTop
				ui.Row(c).FillWidth().MinHeight(52).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobilePictureTop()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobilePictureTopHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-pictop", &picTop, L.MobilePictureTop()) {
						p.PictureTop = picTop
						a.setPrefs(p)
					}
				})

				laptop := p.LaptopMode
				ui.Row(c).FillWidth().MinHeight(52).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobileLaptopMode()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobileLaptopModeHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-laptop", &laptop, L.MobileLaptopMode()) {
						p.LaptopMode = laptop
						a.setPrefs(p)
					}
				})

				safeArea := p.LaptopSafeArea == nil || *p.LaptopSafeArea
				ui.Row(c).FillWidth().MinHeight(52).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobileLaptopSafeArea()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobileLaptopSafeAreaHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-laptopsafe", &safeArea, L.MobileLaptopSafeArea()) {
						v := safeArea
						p.LaptopSafeArea = &v
						a.setPrefs(p)
					}
				})
			})
		})

	case mobSetAppearance:
		ui.Scroll(c.Key("mob-pg-app")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			m3Card(c, mc, "mob-app-card").Gap(14).Children(func() {
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileLanguage()).FontWeight(500).TextColor(mc.OnSurface).Grow(1)
					a.languageSelect(c, "mob-lang-sel", 180)
				})
				themes := []string{"system", "light", "dark"}
				tIdx := max(slices.Index(themes, p.Theme), 0)
				ui.Text(c, L.MobileTheme()).FontWeight(600).TextColor(mc.OnSurface)
				if m3Segmented(c, mc, "mob-theme-seg", &tIdx, L.MobileThemeSystem(), L.MobileThemeLight(), L.MobileThemeDark()) {
					p.Theme = themes[tIdx]
					ApplyTheme(p.Theme)
					a.setPrefs(p)
				}

				navs := []string{"bottom", "side_on_wide", "side"}
				curNav := p.NavPlacement
				if curNav == "" {
					curNav = "side_on_wide"
				}
				nIdx := slices.Index(navs, curNav)
				if nIdx < 0 {
					nIdx = 1
				}
				ui.Text(c, L.MobileSettingsNavPlacement()).FontWeight(600).TextColor(mc.OnSurface)
				ui.Text(c, L.MobileSettingsNavPlacementDesc()).FontSize(12).TextColor(mc.OnSurfaceVariant)
				if m3Segmented(c, mc, "mob-nav-place", &nIdx, L.MobileSettingsNavPlacementBottom(), L.MobileSettingsNavPlacementSideOnWide(), L.MobileSettingsNavPlacementSide()) {
					p.NavPlacement = navs[nIdx]
					a.setPrefs(p)
				}

				if curNav != "bottom" {
					rIdx := 0
					if p.RailOnRight {
						rIdx = 1
					}
					ui.Text(c, L.MobileSettingsRailSide()).FontWeight(600).TextColor(mc.OnSurface)
					if m3Segmented(c, mc, "mob-rail-side", &rIdx, L.MobileSettingsRailSideLeft(), L.MobileSettingsRailSideRight()) {
						p.RailOnRight = rIdx == 1
						a.setPrefs(p)
					}
				}

				others := p.OthersInNetworks
				ui.Row(c).FillWidth().MinHeight(50).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileOthersInNetworks()).FontWeight(500).TextColor(mc.OnSurface).Grow(1)
					if m3Switch(c, "mob-others-sw", &others, L.MobileOthersInNetworks()) {
						p.OthersInNetworks = others
						a.setPrefs(p)
					}
				})
			})
		})

	case mobSetDiagnostics:
		ui.Scroll(c.Key("mob-pg-diag")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			m3Card(c, mc, "mob-diag-card").Gap(10).Children(func() {
				overlay := p.VideoOverlay
				ui.Row(c).FillWidth().MinHeight(50).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, L.MobileShowVideoDetails()).FontWeight(500).TextColor(mc.OnSurface)
						ui.Text(c, L.MobileShowVideoDetailsHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
					if m3Switch(c, "mob-vid-ovl", &overlay, L.MobileShowVideoDetails()) {
						p.VideoOverlay = overlay
						a.setPrefs(p)
					}
				})
				ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))
				buildStr := version.Version
				if b := version.Build(); len(b) >= 7 {
					buildStr += " (" + b[:7] + ")"
				}
				ui.Text(c, fmt.Sprintf("App: %s · Core ABI %d · Secrets: %s", buildStr, st.ABI, st.Persistence)).FontSize(12).TextColor(mc.OnSurfaceVariant)
				if ms.info.Model != "" {
					ui.Text(c, fmt.Sprintf("Android %s (API %d) · %s %s · %s", ms.info.AndroidVersion, ms.info.SDK, ms.info.Manufacturer, ms.info.Model, ms.info.ABIs)).FontSize(12).TextColor(mc.OnSurfaceVariant)
				}
				if ms.posture.Angle >= 0 || ms.posture.Orientation != "" {
					ui.Text(c, fmt.Sprintf("Hinge: %.0f° · %s · half-open: %v (%d–%d px)", ms.posture.Angle, ms.posture.Orientation, ms.posture.HalfOpened, ms.posture.HingeTop, ms.posture.HingeBottom)).FontSize(12).TextColor(mc.OnSurfaceVariant)
				}
				for _, d := range ms.decoders {
					hw := "SW"
					if d.Hardware {
						hw = "HW"
					}
					ui.Text(c, fmt.Sprintf("Decoder %s: %s (%s, max %d×%d)", strings.ToUpper(d.Codec), d.Name, hw, d.MaxWidth, d.MaxHeight)).FontSize(12).TextColor(mc.OnSurfaceVariant)
				}
				ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))
				ui.Text(c, L.MobileLogsPrivacy()).FontSize(12).TextColor(mc.OnSurfaceVariant)
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if m3FilledButton(c, mc, "mob-share-logs", L.MobileShareLogs(), false).Grow(1).Clicked() {
						go a.mobileShareLogs()
					}
					if m3TonalButton(c, mc, "mob-copy-report", L.MobileCopyReport(), false).Grow(1).Clicked() {
						logs := engine.ReadLogs(a.stateDir)
						if logs == "" {
							logs = fmt.Sprintf("Kyokai %s (Core ABI %d, device %s)", version.Version, st.ABI, st.DeviceID)
						}
						a.copyText(logs)
						a.update(func() { a.setToast(L.MobileCopied(), false) })
					}
				})
			})
		})

	case mobSetAbout:
		ui.Scroll(c.Key("mob-pg-about")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			a.aboutView(c)
		})
	}
}

func (a *App) mobileIdentityBackupCard(c *ui.Context, mc mobileColors, deviceID string) {
	a.mu.Lock()
	pass, busy := a.passphrase, a.busy["identity"]
	a.mu.Unlock()
	m3Card(c, mc, "mob-id-backup-card").Children(func() {
		ui.Text(c, L.BackupTitle()).FontWeight(600).TextColor(mc.OnSurface)
		ui.Text(c, L.BackupAbout()).FontSize(12).TextColor(mc.OnSurfaceVariant)
		in := ui.TextInput(c.Key("mob-backup-pass"), &pass).Password().Placeholder(L.BackupPassphrase()).
			Label(L.BackupPassphrase()).FillWidth().Disabled(busy)
		if in.Changed() {
			a.mu.Lock()
			a.passphrase = pass
			a.mu.Unlock()
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if m3TonalButton(c, mc, "mob-backup-save", L.ActionBackUp(), busy || pass == "").Grow(1).Clicked() {
				go a.backUp(pass, deviceID)
			}
			if m3OutlinedButton(c, mc, "mob-backup-restore", L.ActionRestore(), mc.Primary, busy || pass == "").Grow(1).Clicked() {
				go a.restore(pass)
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.ResetTitle()).FontWeight(600).TextColor(mc.OnSurface)
				ui.Text(c, L.ResetAbout()).FontSize(12).TextColor(mc.OnSurfaceVariant)
			})
			if m3OutlinedButton(c, mc, "mob-reset-go", L.ActionReset(), mc.Error, busy).Height(42).Clicked() {
				go a.resetIdentity()
			}
		})
	})
}

func (a *App) mobileShareLogs() {
	logs := engine.ReadLogs(a.stateDir)
	if logs == "" {
		logs = "Kyokai " + version.Version + " — no log entries.\n"
	}
	tmp := filepath.Join(os.TempDir(), "kyokai-logs.txt")
	if err := os.WriteFile(tmp, []byte(logs), 0o600); err != nil {
		a.update(func() { a.setToast(err.Error(), true) })
		return
	}
	_ = mygo.Share(mygo.ShareOptions{Files: []string{tmp}})
}
