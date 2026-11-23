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

// Mobile shell layout tokens and navigation icons (24×24, stroke 2).
const (
	mobileWideMinW          float32 = 600
	mobileKeyboardThreshold float32 = 120
	mobileBottomBarH        float32 = 64
	mobileRailW             float32 = 84
	mobileCardRadius        float32 = 16
	mobileBtnRadius         float32 = 12
)

var (
	mobBackIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 12H5"/><path d="m12 19-7-7 7-7"/></svg>`))
)

func mobileHeaderBar(c *ui.Context, o mobile.TopBarOptions) {
	t := c.Theme()
	bg := t.Background
	if o.Background != nil {
		bg = *o.Background
	}
	leftPad := float32(16)
	if o.Leading != nil {
		leftPad = 4
	}
	rightPad := float32(16)
	if o.Trailing != nil {
		rightPad = 8
	}
	ui.Row(c).FillWidth().Height(54).Padding(0, rightPad, 0, leftPad).Gap(4).
		Background(bg).Shrink(0).AlignItems(ui.Center).Role(ui.RoleGroup).Children(func() {
		if o.Leading != nil {
			o.Leading()
		}
		ui.Text(c, o.Title).FontSize(20).FontWeight(700).SingleLine().Grow(1).MinWidth(0).Role(ui.RoleHeading).Level(1)
		if o.Trailing != nil {
			ui.Row(c).AlignItems(ui.Center).Gap(2).Children(o.Trailing)
		}
	})
}

// MobileView is the root view of the Android controller app: full-screen
// active session when one is open, else the adaptive Devices / Sessions /
// Settings shell with predictive back handling and bottom sheets.
func (a *App) MobileView(c *ui.Context) {
	c.SetTheme(readableTheme(c.Theme()))
	t := c.Theme()

	if a.mobile == nil {
		a.initMobile()
	}
	ms := a.mobile

	winW, _ := c.Size()
	wide := winW >= mobileWideMinW

	// Handle Android system Back / Escape in priority order:
	// open sheets/keyboard -> full-screen session -> detail/sub-page -> root.
	canPop := ms.clipSheetOpen || ms.aiSheetOpen || ms.aiHistOpen || ms.typeTextOpen || ms.menuSheetOpen ||
		ms.activeSession != "" || (ms.navTab == 0 && ms.selectedDev != "") ||
		(ms.navTab == 2 && !wide && ms.settingsOpen)
	if ui := ms.views[ms.activeSession]; ui != nil && ui.kbOpen {
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
		c.Root().Background(t.Background)
	}

	ui.Column(c.Key("mob-root")).Fill().Children(func() {
		if active != "" {
			a.mobileActiveSessionView(c, active, winW)
			a.toastView(c)
			a.mobileSheets(c, wide)
			return
		}
		a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)

		openCount := a.mobileOpenSessionCount()
		badge := ""
		if openCount > 0 {
			badge = strconv.Itoa(openCount)
		}

		if useSide {
			row := ui.Row(c.Key("mob-rail-layout")).Fill()
			row.Children(func() {
				if !railRight {
					a.mobileRailView(c, badge, false)
					ui.Box(c).Width(1).FillHeight().Background(t.Border)
				}
				ui.Column(c.Key("mob-body")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
					a.mobileTabBody(c, wide)
				})
				if railRight {
					ui.Box(c).Width(1).FillHeight().Background(t.Border)
					a.mobileRailView(c, badge, true)
				}
			})
		} else {
			ui.Column(c.Key("mob-compact-layout")).Fill().Children(func() {
				ui.Column(c.Key("mob-body")).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
					a.mobileTabBody(c, wide)
				})
				if !imeOpen {
					ui.Box(c).FillWidth().Height(1).Background(t.Border)
					a.mobileBottomBar(c, badge, safeBottom)
				}
			})
		}
		a.toastView(c)
		a.mobileSheets(c, wide)
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
			return
		}
		ms.activeSession = ""
		a.mobileSyncSessions()
		a.homeVisibilityChanged()
		return
	}
	if ms.navTab == 0 && ms.selectedDev != "" {
		ms.selectedDev = ""
		return
	}
	if ms.navTab == 2 && ms.settingsOpen {
		ms.settingsOpen = false
	}
}

func (a *App) mobileBottomBar(c *ui.Context, sessionsBadge string, safeBottom float32) {
	t := c.Theme()
	ms := a.mobile
	items := []struct {
		label string
		icon  *ui.SVG
		badge string
	}{
		{L.MobileNavDevices(), navHome, ""},
		{L.MobileNavSessions(), barDisplay, sessionsBadge},
		{L.MobileNavSettings(), shellGear, ""},
	}
	// Root already pads safeBottom; negative bottom margin extends t.Surface behind the system gesture bar.
	ui.Row(c.Key("mob-bottom-bar")).FillWidth().Height(mobileBottomBarH+safeBottom).
		Padding(0, 8, safeBottom, 8).Margin(0, 0, -safeBottom, 0).Shrink(0).
		Background(t.Surface).Role(ui.RoleTabList).Children(func() {
		for i, it := range items {
			on := ms.navTab == i
			col := t.TextMuted
			weight := 500
			if on {
				col = t.Accent
				weight = 600
			}
			tab := ui.Column(c.Key("mob-nav-" + strconv.Itoa(i))).Grow(1).Basis(0).FillHeight().AlignItems(ui.Center).Justify(ui.Center).Gap(3).
				Role(ui.RoleTab).Label(it.label).Checked(on)
			tab.Children(func() {
				pill := ui.Box(c).Size(60, 30).Radius(15).Center()
				if on {
					pill.Background(t.Accent.Alpha(0.16))
				}
				pill.Children(func() {
					ui.Icon(c, it.icon).FontSize(20).TextColor(col)
					if it.badge != "" {
						ui.Text(c, it.badge).Absolute().Top(-2).Right(6).FontSize(10).FontWeight(600).PaddingX(5).Radius(8).
							Background(t.Accent).TextColor(ui.RGB(255, 255, 255))
					}
				})
				ui.Text(c, it.label).FontSize(12).FontWeight(weight).TextColor(col).NoWrap()
			})
			if tab.Clicked() {
				if i == 2 && ms.navTab == 2 {
					ms.settingsOpen = false
				}
				ms.navTab = i
				a.homeVisibilityChanged()
			}
		}
	})
}

func (a *App) mobileRailView(c *ui.Context, sessionsBadge string, onRight bool) {
	t := c.Theme()
	ms := a.mobile
	top, right, bottom, left := c.SafeArea()
	railBot := bottom
	if railBot > mobileKeyboardThreshold {
		railBot = 0
	}
	items := []struct {
		label string
		icon  *ui.SVG
		badge string
	}{
		{L.MobileNavDevices(), navHome, ""},
		{L.MobileNavSessions(), barDisplay, sessionsBadge},
		{L.MobileNavSettings(), shellGear, ""},
	}
	rail := ui.Column(c.Key("mob-rail")).FillHeight().Gap(8).Shrink(0).
		AlignItems(ui.Center).Background(t.Surface).Role(ui.RoleTabList)
	if onRight {
		rail.Width(mobileRailW+right).Margin(-top, -right, -railBot, 0).Padding(12+top, 6+right, 12+railBot, 6)
	} else {
		rail.Width(mobileRailW+left).Margin(-top, 0, -railBot, -left).Padding(12+top, 6, 12+railBot, 6+left)
	}
	rail.Children(func() {
		ui.Icon(c, appMark).FontSize(26).TextColor(t.Accent).Margin(4, 0, 10, 0)
		for i, it := range items {
			on := ms.navTab == i
			col := t.TextMuted
			if on {
				col = t.Accent
			}
			tab := ui.Column(c.Key("mob-rail-" + strconv.Itoa(i))).FillWidth().Height(58).Radius(14).AlignItems(ui.Center).Justify(ui.Center).Gap(3).
				Role(ui.RoleTab).Label(it.label).Checked(on)
			if on {
				tab.Background(t.Accent.Alpha(0.15))
			}
			tab.Children(func() {
				box := ui.Box(c).Size(44, 26).Center()
				box.Children(func() {
					ui.Icon(c, it.icon).FontSize(20).TextColor(col)
					if it.badge != "" {
						ui.Text(c, it.badge).Absolute().Top(-3).Right(0).FontSize(10).FontWeight(600).PaddingX(4).Radius(8).
							Background(t.Accent).TextColor(ui.RGB(255, 255, 255))
					}
				})
				ui.Text(c, it.label).FontSize(11).FontWeight(500).TextColor(col).NoWrap()
			})
			if tab.Clicked() {
				if i == 2 && ms.navTab == 2 {
					ms.settingsOpen = false
				}
				ms.navTab = i
				a.homeVisibilityChanged()
			}
		}
	})
}

func (a *App) mobileTabBody(c *ui.Context, wide bool) {
	ui.Column(c.Key("mob-safe-top")).Fill().Children(func() {
		if !a.engineState(c) {
			return
		}
		switch a.mobile.navTab {
		case 0:
			a.mobileDevicesTab(c, wide)
		case 1:
			a.mobileSessionsTab(c)
		case 2:
			a.mobileSettingsTab(c, wide)
		}
	})
}

// ---- Tab 0: Devices & Device Detail -----------------------------------------

func (a *App) mobileDevicesTab(c *ui.Context, wide bool) {
	ms := a.mobile
	t := c.Theme()
	if wide {
		ui.Row(c.Key("mob-dev-split")).Fill().Children(func() {
			ui.Column(c.Key("mob-dev-left")).Width(360).FillHeight().Children(func() {
				a.mobileDevicesList(c, true)
			})
			ui.Box(c).Width(1).FillHeight().Background(t.Border)
			ui.Column(c.Key("mob-dev-right")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
				if ms.selectedDev == "" {
					emptyState(c, "mob-pick-dev", func() { ui.Icon(c, navHome).FontSize(36).TextColor(t.TextMuted) }, L.MobileNoDeviceSelected(), "", nil)
				} else {
					a.mobileDeviceDetail(c, ms.selectedDev, false)
				}
			})
		})
		return
	}
	if ms.selectedDev != "" {
		a.mobileDeviceDetail(c, ms.selectedDev, true)
		return
	}
	a.mobileDevicesList(c, false)
}

func (a *App) mobileDevicesList(c *ui.Context, masterPane bool) {
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
	a.mu.Unlock()

	word, col := a.networkState(t)
	mobileHeaderBar(c, mobile.TopBarOptions{
		Title: L.MobileNavDevices(),
		Trailing: func() {
			statePill(c, word, col)
		},
	})

	ui.Scroll(c.Key("mob-dev-scroll")).Grow(1).FillWidth().Padding(8, 16, 20, 16).Gap(12).Children(func() {
		// This device compact banner + memory-only / unreadable warnings.
		ui.Row(c.Key("mob-self-card")).FillWidth().Padding(12, 14).Radius(mobileCardRadius).Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Gap(12).Children(func() {
			badge := ui.Box(c).Size(40, 40).Radius(12).Background(t.Accent.Alpha(0.14)).Shrink(0).Center()
			badge.Children(func() { ui.Icon(c, appMark).FontSize(20).TextColor(t.Accent) })
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.MobileThisDevice(groupDigits(st.DeviceID))).FontSize(15).FontWeight(600).FontFeatures("tnum")
				if st.Name != "" {
					ui.Text(c, st.Name).FontSize(12).TextColor(t.TextMuted).SingleLine()
				}
			})
			copyBtn := ui.ButtonBase(c.Key("mob-copy-self")).Size(40, 40).Radius(10).Background(t.Background).Border(1, t.Border).Center().Label(L.MobileCopy())
			copyBtn.Children(func() { ui.Icon(c, rowCopy).FontSize(17).TextColor(t.TextMuted) })
			if copyBtn.Clicked() {
				a.copyText(st.DeviceID)
				a.update(func() { a.setToast(L.MobileCopied(), false) })
			}
		})

		if st.Persistence == "memory-only" {
			ui.Column(c.Key("mob-mem-warn")).FillWidth().Padding(12).Radius(12).Background(t.Warning.Alpha(0.14)).Gap(4).Children(func() {
				ui.Text(c, L.MobileMemoryOnly()).Bold().TextColor(textTone(t, t.Warning))
				ui.Text(c, L.MobileMemoryOnlyHint()).FontSize(12).TextColor(t.TextMuted)
			})
		} else if st.Persistence == "unreadable" {
			ui.Row(c.Key("mob-unread-warn")).FillWidth().Padding(12).Radius(12).Background(t.Danger.Alpha(0.12)).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, L.SecretsUnreadable()).FontSize(12).Grow(1)
				if ui.Button(c.Key("mob-start-over"), L.MobileForgetIdentity()).Clicked() {
					a.startOver()
				}
			})
		}

		// Connect by ID or address bar.
		devID, addr := parseTarget(f.target)
		netOnline := st.Network.State == "online"
		ready := ((netOnline && devID != "") || addr != "") && !f.identifying
		ui.Column(c.Key("mob-connect-box")).FillWidth().Padding(12, 14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("mob-conn-in"), &f.target).Placeholder(L.MobileConnectHint()).Label(L.MobileConnectHint()).
					Grow(1).MinWidth(0).FontFeatures("tnum").Disabled(f.identifying)
				if in.Changed() {
					a.mu.Lock()
					a.connect.target = f.target
					a.mu.Unlock()
				}
				if ui.PrimaryButton(c.Key("mob-conn-btn"), L.MobileConnect()).Disabled(!ready).Clicked() || (in.Submitted() && ready) {
					a.addMobileRecentTarget(f.target)
					a.mobileConnectTarget(devID, addr)
				}
			})
			if len(p.MobileRecentTargets) > 0 {
				ui.ScrollHorizontal(c.Key("mob-recent-targets")).FillWidth().Gap(6).Children(func() {
					for i, rt := range p.MobileRecentTargets {
						rt := rt
						ch := ui.ButtonBase(c.Key("mob-rt-"+strconv.Itoa(i))).Padding(4, 10).Radius(8).Background(t.Background).Border(1, t.Border).Label(rt)
						ch.Children(func() { ui.Text(c, rt).FontSize(12).TextColor(t.TextMuted).NoWrap() })
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
			ui.Segmented(c.Key("mob-dev-filter"), &ms.filterMode, L.MobileFilterAll(), L.MobileFilterOnline(), L.MobileFilterOffline()).FillEqually()
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
			emptyState(c, "mob-no-devs", func() { ui.Icon(c, navHome).FontSize(34).TextColor(t.TextMuted) }, L.MobileNoDevices(), hint, nil)
		}

		now := time.Now()
		for _, r := range filtered {
			r := r
			path, on := onlineMap[r.id]
			w, hasW := watched[r.id]
			selected := masterPane && ms.selectedDev == r.id
			cardBg := t.Surface
			borderCol := t.Border
			if selected {
				cardBg = t.Accent.Alpha(0.10)
				borderCol = t.Accent
			}
			card := ui.Row(c.Key("mob-dev-row-"+r.id)).FillWidth().MinHeight(72).Padding(12, 14).Radius(mobileCardRadius).
				Background(cardBg).Border(1, borderCol).AlignItems(ui.Center).Gap(12)
			card.Children(func() {
				deviceAvatar(c, r.about)
				ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
					title := r.about.Name
					if title == "" {
						title = groupDigits(r.id)
					}
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, title).FontSize(15).FontWeight(600).SingleLine().Grow(1).MinWidth(0)
						if on {
							statePill(c, L.CardOnline(), t.Success)
						} else {
							statePill(c, L.MobileFilterOffline(), t.TextMuted)
						}
					})
					sub := groupDigits(r.id)
					if on && path.LatencyMS >= 0 {
						pName := L.MobilePathRelay()
						if path.Path == "direct" {
							pName = L.MobilePathDirect()
						}
						sub += fmt.Sprintf(" · %s %d ms", pName, path.LatencyMS)
					}
					if hasW && w.Sample != nil && statusFreshness(w.ReceivedAt, now) != statusNone {
						cpu := int(w.Sample.CPU.User + w.Sample.CPU.System)
						mem := int(share(w.Sample.Memory))
						sub += fmt.Sprintf(" · CPU %d%% · RAM %d%%", cpu, mem)
					}
					ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
				})
				if slices.Contains(r.features, "wake") {
					wb := ui.ButtonBase(c.Key("mob-wake-"+r.id)).Size(40, 40).Radius(10).Center().Label(L.MobileActionWake())
					wb.Children(func() { ui.Icon(c, shellSun).FontSize(18).TextColor(t.TextMuted) })
					if wb.Clicked() {
						a.wakeHost(r.id)
					}
				}
				db := ui.ButtonBase(c.Key("mob-qdesk-"+r.id)).Size(42, 42).Radius(mobileBtnRadius).Background(t.Accent.Alpha(0.14)).Center().Label(L.MobileOpenDesktop())
				db.Children(func() { ui.Icon(c, kindDesktop).FontSize(19).TextColor(t.Accent) })
				if db.Clicked() {
					a.mobileOpenDesktop(r.id, "")
				}
			})
			if card.Clicked() {
				ms.selectedDev = r.id
			}
		}

		// Others in my networks.
		if p.OthersInNetworks {
			others := networkOthers(st.Network.Networks, rows, st.DeviceID)
			if len(others) > 0 {
				ui.Text(c, L.MobileOthersTitle()).FontSize(14).Bold().TextColor(t.TextMuted).Margin(8, 0, 0, 0)
				for _, o := range others {
					o := o
					row := ui.Row(c.Key("mob-other-"+o.id)).FillWidth().MinHeight(58).Padding(10, 14).Radius(14).
						Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Gap(10)
					row.Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							name := o.path.Name
							if name == "" {
								name = groupDigits(o.id)
							}
							ui.Text(c, name).FontWeight(600)
							ui.Text(c, groupDigits(o.id)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
						})
						if ui.Button(c.Key("mob-other-conn-"+o.id), L.MobileConnect()).Clicked() {
							a.mobileOpenDesktop(o.id, "")
						}
					})
				}
			}
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

func (a *App) mobileDeviceDetail(c *ui.Context, device string, showBack bool) {
	t := c.Theme()
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
	a.mu.Unlock()

	if row == nil {
		mobileHeaderBar(c, mobile.TopBarOptions{
			Title: L.MobileNavDevices(),
			Leading: func() {
				if showBack {
					b := ui.ButtonBase(c.Key("mob-det-back")).Size(48, 48).Center().Label("Back")
					b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
					if b.Clicked() {
						ms.selectedDev = ""
					}
				}
			},
		})
		emptyState(c, "mob-dev-gone", func() { ui.Icon(c, navHome).FontSize(34).TextColor(t.TextMuted) }, L.MobileDeviceGone(), "", nil)
		return
	}

	r := *row
	title := r.about.Name
	if title == "" {
		title = groupDigits(r.id)
	}
	var sample *localapi.SystemSample
	if watched != nil && statusFreshness(watched.ReceivedAt, time.Now()) != statusNone {
		sample = watched.Sample
	}
	services := menuServices(sample, r.features)
	hasAI := len(r.features) == 0 || slices.Contains(r.features, "ai-sessions") || slices.Contains(r.features, "ai")

	mobileHeaderBar(c, mobile.TopBarOptions{
		Title: title,
		Leading: func() {
			if showBack {
				b := ui.ButtonBase(c.Key("mob-det-back")).Size(48, 48).Center().Label("Back")
				b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
				if b.Clicked() {
					ms.selectedDev = ""
				}
			}
		},
		Trailing: func() {
			if hasAI {
				histBtn := ui.ButtonBase(c.Key("mob-det-aihist")).Size(48, 48).Center().Label(L.MobileAiHistory())
				histBtn.Children(func() { ui.Icon(c, navAI).FontSize(20).TextColor(t.TextMuted) })
				if histBtn.Clicked() {
					ms.aiHistDevice = r.id
					ms.aiHistOpen = true
				}
			}
		},
	})

	ui.Scroll(c.Key("mob-det-scroll-"+r.id)).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
		// Identity & status summary card.
		ui.Column(c.Key("mob-det-head")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
				deviceAvatar(c, r.about)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, title).FontSize(17).Bold()
					sub := groupDigits(r.id)
					if r.about.Model != "" {
						sub += " · " + r.about.Model
					}
					ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				})
				if online {
					statePill(c, L.CardOnline(), t.Success)
				} else {
					statePill(c, L.MobileFilterOffline(), t.TextMuted)
				}
			})
			if sample != nil {
				ui.Divider(c)
				ui.Row(c).FillWidth().Gap(12).Wrap().Children(func() {
					ui.Text(c, fmt.Sprintf("%s %d%%", L.MobileStatCpu(), int(sample.CPU.User+sample.CPU.System))).FontSize(12).TextColor(t.TextMuted)
					ui.Text(c, fmt.Sprintf("%s %d%%", L.MobileStatMemory(), int(share(sample.Memory)))).FontSize(12).TextColor(t.TextMuted)
					if sample.Disk.Total > 0 {
						ui.Text(c, fmt.Sprintf("%s %d%%", L.MobileStatDisk(), int(share(sample.Disk)))).FontSize(12).TextColor(t.TextMuted)
					}
				})
			}
		})

		// Primary action buttons: Open Desktop, plus only the capabilities and services this host offers.
		ui.Column(c.Key("mob-det-actions")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(10).Children(func() {
			if ui.PrimaryButton(c.Key("mob-act-desk-"+r.id), L.MobileOpenDesktop()).FillWidth().Height(48).Clicked() {
				a.mobileOpenDesktop(r.id, "")
			}
			hasWake := slices.Contains(r.features, "wake")
			if hasAI || hasWake {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if hasAI {
						if ui.Button(c.Key("mob-act-ai-"+r.id), L.MobileAiTask()).Grow(1).Basis(0).Height(46).Clicked() {
							_ = a.useAILink(r.id, "")
							ms.aiSheetDevice = r.id
							ms.aiSheetOpen = true
							a.mobileSyncSessions()
						}
					}
					if hasWake {
						if ui.Button(c.Key("mob-act-wake-"+r.id), L.MobileActionWake()).Grow(1).Basis(0).Height(46).Clicked() {
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
							if ui.Button(c.Key("mob-act-ssh-"+r.id), L.MobileServiceSsh()).Grow(1).MinWidth(130).Height(46).Clicked() {
								a.mobileOpenService(r.id, "", serviceSSH)
							}
						case serviceRDP:
							if ui.Button(c.Key("mob-act-rdp-"+r.id), L.MobileServiceRdp()).Grow(1).MinWidth(130).Height(46).Clicked() {
								a.mobileOpenService(r.id, "", serviceRDP)
							}
						case serviceVNC:
							if ui.Button(c.Key("mob-act-vnc-"+r.id), L.MobileServiceVnc()).Grow(1).MinWidth(100).Height(46).Clicked() {
								a.mobileOpenService(r.id, "", serviceVNC)
							}
						case serviceNanokvm:
							if ui.Button(c.Key("mob-act-kvm-"+r.id), L.MobileNanokvm()).Grow(1).MinWidth(120).Height(46).Clicked() {
								a.mobileOpenNanokvm(r.id, "")
							}
						}
					}
				})
			}
		})

		// Routes section.
		onNet := len(st.Network.Networks) > 0
		views := routeViews(r, netDev, online, onNet)
		ui.Column(c.Key("mob-det-routes")).FillWidth().Padding(14).Radius(mobileCardRadius).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.MobileSectionRoutes()).Bold().Grow(1)
				rb := ui.ButtonBase(c.Key("mob-route-probe-"+r.id)).Size(38, 38).Radius(10).Background(t.Background).Border(1, t.Border).Center().Label("Refresh")
				rb.Children(func() { ui.Icon(c, rowRefresh).FontSize(16).TextColor(t.TextMuted) })
				if rb.Clicked() {
					devs := []string{r.id}
					go func() {
						_, _ = a.requestWith(localapi.Request{Type: localapi.RequestRoutesProbe, Probe: &devs})
					}()
				}
			})
			routeChoice := func(id, label, sub string, chosen bool, pick func(), del func()) {
				row := ui.Row(c.Key(id)).FillWidth().MinHeight(48).Padding(8, 10).Radius(10).AlignItems(ui.Center).Gap(10)
				if chosen {
					row.Background(t.Accent.Alpha(0.14))
				}
				row.Children(func() {
					radio := ui.Box(c).Size(18, 18).Radius(9).Border(2, t.Accent).Shrink(0).Center()
					if chosen {
						radio.Children(func() { ui.Box(c).Size(8, 8).Radius(4).Background(t.Accent) })
					}
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, label).FontWeight(500)
						if sub != "" {
							ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted)
						}
					})
					if del != nil {
						db := ui.ButtonBase(c.Key(id+"-del")).Size(40, 40).Radius(10).Center().Label(L.MobileRemove())
						db.Children(func() { ui.Icon(c, paneClose).FontSize(16).TextColor(t.TextMuted) })
						if db.Clicked() {
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
				ui.TextInput(c.Key("mob-rt-add-"+r.id), &ms.routeAddAddr).Placeholder(L.MobileRouteAddHint()).Label(L.MobileRouteAdd()).Grow(1).MinWidth(0)
				if ui.Button(c.Key("mob-rt-add-btn-"+r.id), "+").Disabled(strings.TrimSpace(ms.routeAddAddr) == "").Clicked() {
					addr := strings.TrimSpace(ms.routeAddAddr)
					ms.routeAddAddr = ""
					go a.setRoutes(localapi.RoutesChange{DeviceID: r.id, Add: addr})
				}
			})
		})

		// Saved sign-ins & Remove device.
		ui.Column(c.Key("mob-det-saved")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
			ui.Text(c, L.MobileSectionSignIn()).Bold()
			alias := p.Aliases[r.id]
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("mob-alias-"+r.id), &alias).Placeholder(L.MobileDeviceName()).Label(L.MobileDeviceName()).Grow(1).MinWidth(0)
				if in.Submitted() {
					a.setAlias(r.id, strings.TrimSpace(alias))
				}
				if ui.Button(c.Key("mob-alias-save-"+r.id), L.MobileSave()).Clicked() {
					a.setAlias(r.id, strings.TrimSpace(alias))
				}
			})
			if r.saved.Password || r.saved.Key {
				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					lbl := L.MobileSavedPassword()
					if r.saved.Key && !r.saved.Password {
						lbl = L.MobileSignInKey() + " (" + L.MobileKeyAdmitted() + ")"
					}
					ui.Text(c, lbl).Grow(1)
					if ui.Button(c.Key("mob-forget-pw-"+r.id), L.MobileForget()).Clicked() {
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
				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					ui.Text(c, svc.name).Grow(1)
					if ui.Button(c.Key("mob-forget-"+svcID+"-"+r.id), L.MobileForget()).Clicked() {
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestCredentialsForget, DeviceID: r.id, Service: svcID})
						}()
					}
				})
			}
			ui.Divider(c)
			rm := ui.Button(c.Key("mob-rm-dev-"+r.id), L.MobileRemoveDevice()).FillWidth().Height(46)
			if rm.Clicked() {
				if a.confirm(L.MobileRemoveDeviceHint(title), L.MobileRemove()) {
					a.removeDevice(r.id)
					ms.selectedDev = ""
				}
			}
		})
	})
}

// ---- Tab 1: Open Sessions ---------------------------------------------------

func (a *App) mobileSessionsTab(c *ui.Context) {
	t := c.Theme()
	ms := a.mobile

	mobileHeaderBar(c, mobile.TopBarOptions{
		Title: L.MobileNavSessions(),
		Trailing: func() {
			cb := ui.ButtonBase(c.Key("mob-sess-clip")).Size(48, 48).Center().Label(L.MobileClipHistory())
			cb.Children(func() { ui.Icon(c, barClipboard).FontSize(20).TextColor(t.TextMuted) })
			if cb.Clicked() {
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
		emptyState(c, "mob-no-sess", func() { ui.Icon(c, barDisplay).FontSize(36).TextColor(t.TextMuted) }, L.MobileNoSessions(), "", nil)
		return
	}

	ui.Scroll(c.Key("mob-sess-scroll")).Grow(1).FillWidth().Padding(8, 16, 24, 16).Gap(10).Children(func() {
		sessRow := func(key, title, sub string, icon *ui.SVG, onOpen func(), onClose func()) {
			row := ui.Row(c.Key("mob-srow-"+key)).FillWidth().MinHeight(66).Padding(12, 14).Radius(mobileCardRadius).
				Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Gap(12)
			row.Children(func() {
				badge := ui.Box(c).Size(40, 40).Radius(12).Background(t.Accent.Alpha(0.14)).Shrink(0).Center()
				badge.Children(func() { ui.Icon(c, icon).FontSize(20).TextColor(t.Accent) })
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, title).FontSize(15).FontWeight(600).SingleLine()
					ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).SingleLine()
				})
				xb := ui.ButtonBase(c.Key("mob-sclose-"+key)).Size(42, 42).Radius(10).Center().Label(L.MobileDisconnect())
				xb.Children(func() { ui.Icon(c, paneClose).FontSize(18).TextColor(t.TextMuted) })
				if xb.Clicked() {
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
			sessRow(v.tabKey(), v.title, kind+" · "+st, ic, func() {
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
			sessRow(key, ts.name, L.MobileServiceSsh()+" · "+st, kindSSH, func() {
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
			sessRow(s.tabKey(), s.name, s.kind+" · "+st, ic, func() {
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
			sessRow("ailink-"+l.device, groupDigits(l.device), L.MobileAiTask()+" · "+snap.phase.State, navAI, func() {
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

func (a *App) mobileSettingsTab(c *ui.Context, wide bool) {
	ms := a.mobile
	t := c.Theme()
	cats := []struct {
		p     mobileSettingsPage
		label string
		icon  *ui.SVG
	}{
		{mobSetDevice, L.MobileSectionThisDevice(), rowID},
		{mobSetNetworks, L.MobileNetworks(), navNetwork},
		{mobSetKeys, L.MobileSignInKeys(), navSecurity},
		{mobSetInput, L.MobileSectionInput(), rowMouse},
		{mobSetAppearance, L.MobileSectionAppearance(), shellAuto},
		{mobSetDiagnostics, L.MobileDiagnostics(), rowLog},
		{mobSetAbout, L.MobileSectionAbout(), navAbout},
	}

	if wide {
		ui.Row(c.Key("mob-set-split")).Fill().Children(func() {
			ui.Column(c.Key("mob-set-left")).Width(280).FillHeight().Children(func() {
				mobileHeaderBar(c, mobile.TopBarOptions{Title: L.MobileNavSettings()})
				ui.Scroll(c.Key("mob-set-cats")).Grow(1).FillWidth().Padding(8, 12).Gap(4).Children(func() {
					for _, cat := range cats {
						cat := cat
						on := ms.settingsPage == cat.p
						row := ui.Row(c.Key("mob-cat-" + strconv.Itoa(int(cat.p)))).FillWidth().Height(48).PaddingX(12).Radius(12).AlignItems(ui.Center).Gap(10)
						if on {
							row.Background(t.Accent.Alpha(0.15))
						}
						row.Children(func() {
							col := t.TextMuted
							if on {
								col = t.Accent
							}
							ui.Icon(c, cat.icon).FontSize(18).TextColor(col)
							ui.Text(c, cat.label).FontWeight(500).TextColor(t.Text)
						})
						if row.Clicked() {
							ms.settingsPage = cat.p
						}
					}
				})
			})
			ui.Box(c).Width(1).FillHeight().Background(t.Border)
			ui.Column(c.Key("mob-set-right")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
				a.mobileSettingsContent(c, ms.settingsPage)
			})
		})
		return
	}

	if !ms.settingsOpen {
		mobileHeaderBar(c, mobile.TopBarOptions{Title: L.MobileNavSettings()})
		ui.Scroll(c.Key("mob-set-menu")).Grow(1).FillWidth().Padding(10, 16, 24, 16).Gap(8).Children(func() {
			for _, cat := range cats {
				cat := cat
				row := ui.Row(c.Key("mob-smenu-"+strconv.Itoa(int(cat.p)))).FillWidth().MinHeight(56).Padding(12, 14).
					Radius(mobileCardRadius).Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Gap(12)
				row.Children(func() {
					badge := ui.Box(c).Size(36, 36).Radius(10).Background(t.Accent.Alpha(0.12)).Shrink(0).Center()
					badge.Children(func() { ui.Icon(c, cat.icon).FontSize(18).TextColor(t.Accent) })
					ui.Text(c, cat.label).FontSize(15).FontWeight(500).TextColor(t.Text).Grow(1).MinWidth(0)
					ui.Icon(c, aiChevronRight).FontSize(16).TextColor(t.TextMuted)
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
	mobileHeaderBar(c, mobile.TopBarOptions{
		Title: curTitle,
		Leading: func() {
			b := ui.ButtonBase(c.Key("mob-set-back")).Size(48, 48).Center().Label("Back")
			b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
			if b.Clicked() {
				ms.settingsOpen = false
			}
		},
	})
	ui.Column(c.Key("mob-set-body")).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
		a.mobileSettingsContent(c, ms.settingsPage)
	})
}

func (a *App) mobileSettingsContent(c *ui.Context, pg mobileSettingsPage) {
	t := c.Theme()
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
			ui.Column(c.Key("mob-id-box")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(10).Children(func() {
				ui.Text(c, L.MobileDeviceId()).FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, groupDigits(st.DeviceID)).FontSize(22).Bold().FontFeatures("tnum").Grow(1)
					if ui.Button(c.Key("mob-set-copyid"), L.MobileCopy()).Height(44).Clicked() {
						a.copyText(st.DeviceID)
						a.update(func() { a.setToast(L.MobileCopied(), false) })
					}
				})
				ui.Divider(c)
				ui.Text(c, L.MobileDeviceName()).FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					in := ui.TextInput(c.Key("mob-devname-in"), &ms.devNameEdit).Placeholder(strings.TrimSpace(ms.info.Manufacturer + " " + ms.info.Model)).Grow(1).MinWidth(0)
					if in.Changed() {
						ms.devNameDirty = true
					}
					if ui.Button(c.Key("mob-devname-save"), L.MobileSave()).Height(44).Clicked() || in.Submitted() {
						nm := strings.TrimSpace(ms.devNameEdit)
						ms.devNameDirty = false
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestNameSet, Name: &nm})
						}()
					}
				})
				pub := st.PublishName
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.SettingsNamePublish()).Grow(1)
					if ui.Switch(c.Key("mob-pub-name"), &pub).Label(L.SettingsNamePublish()).Changed() {
						on := pub
						go func() {
							_, _ = a.requestWith(localapi.Request{Type: localapi.RequestNameSet, PublishName: &on})
						}()
					}
				})
				noLogs := p.NoLogs
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.NoLogsTitle()).Grow(1)
					if ui.Switch(c.Key("mob-nologs"), &noLogs).Label(L.NoLogsTitle()).Changed() {
						a.setNoLogs(p, noLogs)
					}
				})
			})
			a.mobileIdentityBackupCard(c, st.DeviceID)
		})

	case mobSetInput:
		ui.Scroll(c.Key("mob-pg-input")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			ui.Column(c.Key("mob-in-card")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(12).Children(func() {
				// Pointer mode: Trackpad vs Touch.
				pmIdx := 0
				if p.PointerMode == "touch" {
					pmIdx = 1
				}
				ui.Text(c, L.ViewerPointer()).Bold()
				if ui.Segmented(c.Key("mob-pmode"), &pmIdx, L.MobilePointerTrackpad(), L.MobilePointerTouch()).FillEqually().Changed() {
					if pmIdx == 1 {
						p.PointerMode = "touch"
					} else {
						p.PointerMode = "trackpad"
					}
					a.setPrefs(p)
				}

				// Pointer speed slider.
				spd := float64(p.PointerSpeed)
				if spd <= 0 {
					spd = 1.6
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobilePointerSpeed()).Grow(1)
					ui.Text(c, fmt.Sprintf("%.1f×", spd)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				})
				if ui.Slider(c.Key("mob-pspeed"), &spd, 0.5, 3.0).Step(0.1).FillWidth().Changed() {
					p.PointerSpeed = float32(spd)
					a.setPrefs(p)
				}

				// Cursor size slider + live preview.
				csz64 := float64(p.CursorSize)
				if csz64 <= 0 {
					csz64 = 16
				}
				csz := float32(csz64)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileCursorSize()).Grow(1)
					ui.Text(c, fmt.Sprintf("%d dp", int(csz64))).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				})
				if ui.Slider(c.Key("mob-csize"), &csz64, 10, 34).Step(1).FillWidth().Changed() {
					p.CursorSize = float32(csz64)
					a.setPrefs(p)
				}
				ui.Row(c.Key("mob-csize-prev")).FillWidth().Height(48).Radius(10).Background(ui.RGB(20, 24, 28)).AlignItems(ui.Center).Justify(ui.SpaceAround).PaddingX(16).Children(func() {
					ui.Text(c, "S").FontSize(11).TextColor(ui.RGB(160, 160, 160))
					ui.Box(c).Size(csz, csz).Radius(csz/2).Background(ui.RGBA(255, 255, 255, 0.9)).Border(2, t.Accent)
					ui.Text(c, "M").FontSize(11).TextColor(ui.RGB(160, 160, 160))
					ui.Text(c, "L").FontSize(11).TextColor(ui.RGB(160, 160, 160))
				})

				ui.Divider(c)
				splitKB := p.SplitKeyboard
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, L.MobileSplitKeyboard()).FontWeight(500)
						ui.Text(c, L.MobileSplitKeyboardHint()).FontSize(12).TextColor(t.TextMuted)
					})
					if ui.Switch(c.Key("mob-splitkb"), &splitKB).Label(L.MobileSplitKeyboard()).Changed() {
						p.SplitKeyboard = splitKB
						a.setPrefs(p)
					}
				})

				picTop := p.PictureTop
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, L.MobilePictureTop()).FontWeight(500)
						ui.Text(c, L.MobilePictureTopHint()).FontSize(12).TextColor(t.TextMuted)
					})
					if ui.Switch(c.Key("mob-pictop"), &picTop).Label(L.MobilePictureTop()).Changed() {
						p.PictureTop = picTop
						a.setPrefs(p)
					}
				})

				laptop := p.LaptopMode
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, L.MobileLaptopMode()).FontWeight(500)
						ui.Text(c, L.MobileLaptopModeHint()).FontSize(12).TextColor(t.TextMuted)
					})
					if ui.Switch(c.Key("mob-laptop"), &laptop).Label(L.MobileLaptopMode()).Changed() {
						p.LaptopMode = laptop
						a.setPrefs(p)
					}
				})

				safeArea := p.LaptopSafeArea == nil || *p.LaptopSafeArea
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, L.MobileLaptopSafeArea()).FontWeight(500)
						ui.Text(c, L.MobileLaptopSafeAreaHint()).FontSize(12).TextColor(t.TextMuted)
					})
					if ui.Switch(c.Key("mob-laptopsafe"), &safeArea).Label(L.MobileLaptopSafeArea()).Changed() {
						v := safeArea
						p.LaptopSafeArea = &v
						a.setPrefs(p)
					}
				})
			})
		})

	case mobSetAppearance:
		ui.Scroll(c.Key("mob-pg-app")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			ui.Column(c.Key("mob-app-card")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(12).Children(func() {
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileLanguage()).Grow(1)
					a.languageSelect(c, "mob-lang-sel", 180)
				})
				themes := []string{"system", "light", "dark"}
				tIdx := max(slices.Index(themes, p.Theme), 0)
				ui.Text(c, L.MobileTheme()).FontWeight(500)
				if ui.Segmented(c.Key("mob-theme-seg"), &tIdx, L.MobileThemeSystem(), L.MobileThemeLight(), L.MobileThemeDark()).FillEqually().Changed() {
					p.Theme = themes[tIdx]
					ApplyTheme(p.Theme)
					a.setPrefs(p)
				}

				navs := []string{"bottom", "side_on_wide", "side"}
				curNav := p.NavPlacement
				if curNav == "" {
					curNav = "side_on_wide"
				}
				nIdx := max(slices.Index(navs, curNav), 1)
				ui.Text(c, L.MobileSettingsNavPlacement()).FontWeight(500)
				ui.Text(c, L.MobileSettingsNavPlacementDesc()).FontSize(12).TextColor(t.TextMuted)
				if ui.Segmented(c.Key("mob-nav-place"), &nIdx, L.MobileSettingsNavPlacementBottom(), L.MobileSettingsNavPlacementSideOnWide(), L.MobileSettingsNavPlacementSide()).FillEqually().Changed() {
					p.NavPlacement = navs[nIdx]
					a.setPrefs(p)
				}

				rIdx := 0
				if p.RailOnRight {
					rIdx = 1
				}
				ui.Text(c, L.MobileSettingsRailSide()).FontWeight(500)
				if ui.Segmented(c.Key("mob-rail-side"), &rIdx, L.MobileSettingsRailSideLeft(), L.MobileSettingsRailSideRight()).FillEqually().Changed() {
					p.RailOnRight = rIdx == 1
					a.setPrefs(p)
				}

				others := p.OthersInNetworks
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileOthersInNetworks()).Grow(1)
					if ui.Switch(c.Key("mob-others-sw"), &others).Label(L.MobileOthersInNetworks()).Changed() {
						p.OthersInNetworks = others
						a.setPrefs(p)
					}
				})
			})
		})

	case mobSetDiagnostics:
		ui.Scroll(c.Key("mob-pg-diag")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(14).Children(func() {
			ui.Column(c.Key("mob-diag-card")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
				overlay := p.VideoOverlay
				ui.Row(c).FillWidth().MinHeight(48).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, L.MobileShowVideoDetails()).FontWeight(500)
						ui.Text(c, L.MobileShowVideoDetailsHint()).FontSize(12).TextColor(t.TextMuted)
					})
					if ui.Switch(c.Key("mob-vid-ovl"), &overlay).Label(L.MobileShowVideoDetails()).Changed() {
						p.VideoOverlay = overlay
						a.setPrefs(p)
					}
				})
				ui.Divider(c)
				buildStr := version.Version
				if b := version.Build(); len(b) >= 7 {
					buildStr += " (" + b[:7] + ")"
				}
				ui.Text(c, fmt.Sprintf("App: %s · Core ABI %d · Secrets: %s", buildStr, st.ABI, st.Persistence)).FontSize(12).TextColor(t.TextMuted)
				if ms.info.Model != "" {
					ui.Text(c, fmt.Sprintf("Android %s (API %d) · %s %s · %s", ms.info.AndroidVersion, ms.info.SDK, ms.info.Manufacturer, ms.info.Model, ms.info.ABIs)).FontSize(12).TextColor(t.TextMuted)
				}
				if ms.posture.Angle >= 0 {
					ui.Text(c, fmt.Sprintf("Hinge: %.0f° (half-open: %v)", ms.posture.Angle, ms.posture.HalfOpened)).FontSize(12).TextColor(t.TextMuted)
				}
				for _, d := range ms.decoders {
					hw := "SW"
					if d.Hardware {
						hw = "HW"
					}
					ui.Text(c, fmt.Sprintf("Decoder %s: %s (%s, max %d×%d)", strings.ToUpper(d.Codec), d.Name, hw, d.MaxWidth, d.MaxHeight)).FontSize(12).TextColor(t.TextMuted)
				}
				ui.Divider(c)
				ui.Text(c, L.MobileLogsPrivacy()).FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if ui.PrimaryButton(c.Key("mob-share-logs"), L.MobileShareLogs()).Grow(1).Height(46).Clicked() {
						go a.mobileShareLogs()
					}
					if ui.Button(c.Key("mob-copy-report"), L.MobileCopyReport()).Grow(1).Height(46).Clicked() {
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

func (a *App) mobileIdentityBackupCard(c *ui.Context, deviceID string) {
	t := c.Theme()
	a.mu.Lock()
	pass, busy := a.passphrase, a.busy["identity"]
	a.mu.Unlock()
	ui.Column(c.Key("mob-id-backup-card")).FillWidth().Padding(14).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(10).Children(func() {
		ui.Text(c, L.BackupTitle()).Bold()
		ui.Text(c, L.BackupAbout()).FontSize(12).TextColor(t.TextMuted)
		in := ui.TextInput(c.Key("mob-backup-pass"), &pass).Password().Placeholder(L.BackupPassphrase()).Label(L.BackupPassphrase()).FillWidth().Disabled(busy)
		if in.Changed() {
			a.mu.Lock()
			a.passphrase = pass
			a.mu.Unlock()
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.Button(c.Key("mob-backup-save"), L.ActionBackUp()).Grow(1).Height(44).Disabled(busy || pass == "").Clicked() {
				go a.backUp(pass, deviceID)
			}
			if ui.Button(c.Key("mob-backup-restore"), L.ActionRestore()).Grow(1).Height(44).Disabled(busy || pass == "").Clicked() {
				go a.restore(pass)
			}
		})
		ui.Divider(c)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, L.ResetTitle()).FontWeight(600)
				ui.Text(c, L.ResetAbout()).FontSize(12).TextColor(t.TextMuted)
			})
			if ui.Button(c.Key("mob-reset-go"), L.ActionReset()).Height(44).Disabled(busy).Clicked() {
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
