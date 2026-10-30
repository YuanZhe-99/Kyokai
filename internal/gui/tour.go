package gui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// tourStep is a step of the first-run tour (doc/en-us/gui.md, "First
// run"). Its layout and drawn illustrations follow EasyTier MyGo's tour;
// its steps are Kyokai's own.
type tourStep int

const (
	tourWelcome tourStep = iota
	tourNetwork          // join a network: through the server or direct
	tourConnect          // connect to a device: what Open as opens
	tourLetIn            // this Device ID and password; with a password or asking
	tourAI               // AI on this computer
	tourPrivacy          // macOS only: the privacy permissions
	tourAway             // when nobody is at this computer: the run modes
	tourAwake            // sleep takes it offline; Windows needs a display
	tourReady            // what was chosen; finishing switches the run mode if needed
)

// tourSteps are the tour's steps on a system: the privacy permissions only
// on macOS.
func tourSteps(goos string) []tourStep {
	steps := []tourStep{tourWelcome, tourNetwork, tourConnect, tourLetIn, tourAI}
	if goos == "darwin" {
		steps = append(steps, tourPrivacy)
	}
	return append(steps, tourAway, tourAwake, tourReady)
}

// The tour's states in gui.json (Prefs.Tour).
const (
	tourPending = "pending" // a fresh profile, until the tour is finished or skipped
	tourDone    = "done"
)

// Layout of the tour.
const (
	tourArtW    float32 = 340  // the illustration's column
	tourArtPad  float32 = 16   // around the illustration in it
	tourBodyW   float32 = 540  // the step's column, at most
	tourPadX    float32 = 44   // the steps' side margins
	tourTopH    float32 = 60   // the row of dots, language and Skip
	tourFootH   float32 = 72   // the row of Back and Continue
	tourDot     float32 = 6    // a step's dot
	tourDotOn   float32 = 20   // the current step's dot
	tourIcon    float32 = 56   // the app's mark on the welcome
	tourTitle   float32 = 26   // a step's title
	tourLead    float32 = 15   // the welcome's sentence
	tourSlide   float32 = 28   // how far a step slides in
	tourSkipW   float32 = 320  // the card Skip opens
	tourMark    float32 = 20   // a fact's round mark
	tourLangW   float32 = 170  // the language picker
	tourSheetW  float32 = 1040 // the tour over the window (from Settings), at most
	tourSheetH  float32 = 680
	tourSheetMW float32 = 720 // and at least
	tourSheetMH float32 = 520
)

// tourPopIn is how the ready step's check appears.
var tourPopIn = ui.CubicBezier(0.3, 1.6, 0.5, 1)

// tour is the tour's state while it shows (App.tour, under App.mu).
type tour struct {
	at      int  // the step shown, in tourSteps(artPlatform)
	dir     int  // the last move: 1 forward, -1 back
	mode    int  // the run mode looked at (modeUser…)
	direct  bool // the network step shows direct connections
	kind    int  // the connect step's Open as choice (openAs)
	ask     bool // letting others in shows a device without a password
	openAI  bool // finishing opens AI providers
	replay  bool // opened from Settings: closing it changes nothing
	askSkip bool // Skip asks before it skips
	failure string
}

// step is the step tr shows.
func (tr tour) step() tourStep { return tourSteps(artPlatform)[tr.at] }

// freshProfile reports whether stateDir holds nothing of an earlier run: no
// gui.json, no store of the engine, no marker of one moved to the helper.
// Only such a profile gets the tour, so that an update never shows it.
func freshProfile(stateDir string) bool {
	for _, name := range append([]string{"gui.json", "moved.json"}, userSetFiles...) {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err == nil {
			return false
		}
	}
	return true
}

// tourEdit changes the tour's state, if it still shows.
func (a *App) tourEdit(fn func(tr *tour)) {
	a.mu.Lock()
	if a.tour != nil {
		fn(a.tour)
	}
	a.mu.Unlock()
}

// openTour shows the tour again over the window, from Settings › General,
// the run mode in use looked at first.
func (a *App) openTour() {
	cur := a.currentMode()
	a.mu.Lock()
	a.settingsOpen, a.settingsFocus = false, settingsFocus{}
	a.tour = &tour{replay: true, mode: cur}
	a.mu.Unlock()
}

// closeTour closes the tour shown again and goes back to Settings.
func (a *App) closeTour() {
	a.mu.Lock()
	a.tour = nil
	a.mu.Unlock()
	a.openSettings(pageGeneral)
}

// leaveTour closes the tour shown again for a page of Settings.
func (a *App) leaveTour(p page) {
	a.mu.Lock()
	a.tour = nil
	a.mu.Unlock()
	a.openSettings(p)
}

// endTour ends the first run: the tour is done and Home shows, AI
// providers over it when the person asked.
// Side effects: writes gui.json.
func (a *App) endTour(openAI bool) {
	a.mu.Lock()
	a.tour = nil
	p := a.prefs
	a.mu.Unlock()
	p.Tour = tourDone
	a.setPrefs(p)
	if openAI {
		a.openSettings(pageAI)
	}
}

// finishTour applies the run mode chosen, when it is not the one in use
// (one administrator prompt to install the helper), then ends the tour. A
// failure stays beside Finish until the next try.
func (a *App) finishTour() {
	cur := a.currentMode()
	a.mu.Lock()
	tr := *a.tour
	a.mu.Unlock()
	if tr.mode == cur {
		a.endTour(tr.openAI)
		return
	}
	a.update(func() {
		a.busy["mode"] = true
		if a.tour != nil {
			a.tour.failure = ""
		}
	})
	go func() {
		err := a.doSwitch(cur, tr.mode)
		a.update(func() {
			delete(a.busy, "mode")
			if err != nil {
				if a.tour != nil {
					a.tour.failure = err.Error()
				}
				return
			}
			a.setToast(L.RunModeChanged(), false)
		})
		if err == nil {
			a.later(func() { a.endTour(tr.openAI) })
		}
	}()
}

// tourShell is the main window during the first run: no sidebar; the
// step's illustration on the window's material (the traffic lights over
// it on macOS), and beside it a pane with the step, the cards that need
// the person above it.
func (a *App) tourShell(c *ui.Context, vw *viewerWindow) {
	t := c.Theme()
	top := c.TitleBar().Height
	ui.Row(c.Key("tour-shell")).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("tour-art-col")).Width(tourArtW).Shrink(0).FillHeight().Padding(top, 0, 0, 0).DragWindow().Children(func() {
			a.tourArt(c)
		})
		ui.Column(c.Key("tour-main")).Grow(1).MinWidth(0).MinHeight(0).Padding(paneMargin, paneMargin, paneMargin, 0).Children(func() {
			a.bannersView(c)
			ui.Column(c.Key("tour-pane")).Grow(1).FillWidth().MinHeight(0).Radius(paneRadius).Clip().
				Background(t.Background).Border(1, t.Border).Shadow(0, 6, 18, 0, paneShadow).Children(func() {
				a.tourBody(c, vw.win)
				a.toastView(c)
			})
		})
	})
}

// tourSheet is the tour shown again from Settings: a sheet over the window
// dimmed, as Settings is. Escape, a click outside or Close goes back to
// Settings; nothing changes unless its last step switches the run mode.
func (a *App) tourSheet(c *ui.Context, win *mygo.Window) {
	a.mu.Lock()
	show := a.tour != nil && a.tour.replay
	a.mu.Unlock()
	if !show {
		return
	}
	t := c.Theme()
	w, h := c.Size()
	open := true
	ui.DialogBase(c, &open, func(back, panel ui.Element) {
		back.Background(sheetScrim).Padding(sheetTop, sheetX, sheetBottom, sheetX)
		panel.Radius(paneRadius).Background(t.Background).Shadow(0, 10, 30, 0, sheetShadow).Clip()
		ui.Row(c.Key("tour-sheet")).AlignItems(ui.Stretch).
			Width(min(max(w-2*sheetX, tourSheetMW), tourSheetW)).
			Height(min(max(h-sheetTop-sheetBottom, tourSheetMH), tourSheetH)).
			Children(func() {
				ui.Column(c.Key("tour-art-col")).Width(tourArtW).Shrink(0).FillHeight().Background(t.Surface).Children(func() { a.tourArt(c) })
				ui.Column(c.Key("tour-main")).Grow(1).MinWidth(0).Children(func() { a.tourBody(c, nil) })
			})
	})
	if ownWindowButtons {
		ui.Overlay(c, func() {
			ui.Row(c.Key("tour-win")).Absolute().Top(paneMargin + (paneHeaderH-winButtonH)/2).Right(2 * paneMargin).Gap(4).Children(func() {
				windowButtons(c, "tour-win", win, true)
			})
		})
	}
	if !open {
		a.closeTour()
	}
}

// tourBody is the step beside its illustration: the dots of the steps,
// the language and Skip (Close when shown again; the window's buttons on
// Windows in the first run), the step, then Back and Continue.
// Inputs: win is the main window during the first run, nil in the sheet.
func (a *App) tourBody(c *ui.Context, win *mygo.Window) {
	t := c.Theme()
	cur := a.currentMode()
	a.mu.Lock()
	if a.tour == nil {
		a.mu.Unlock()
		return
	}
	tr := *a.tour
	busy := a.busy["mode"]
	a.mu.Unlock()
	steps := tourSteps(artPlatform)
	step := steps[tr.at]
	last := tr.at == len(steps)-1
	move := func(by int) {
		a.tourEdit(func(tr *tour) {
			tr.at = max(0, min(tr.at+by, len(steps)-1))
			tr.dir, tr.askSkip = by, false
		})
	}
	ui.Row(c.Key("tour-top")).Height(tourTopH).Shrink(0).FillWidth().Padding(0, 12, 0, tourPadX).Gap(12).AlignItems(ui.Center).Children(func() {
		tourDots(c, len(steps), tr.at)
		ui.Box(c).Grow(1).FillHeight().DragWindow()
		a.languageSelect(c, "tour-lang", tourLangW)
		switch {
		case tr.replay:
			if ui.Button(c.Key("tour-close"), L.ActionClose()).Clicked() {
				a.closeTour()
			}
		case !last:
			if ui.Button(c.Key("tour-skip"), L.TourSkip()).Disabled(busy).Clicked() {
				a.tourEdit(func(tr *tour) { tr.askSkip = !tr.askSkip })
			}
		}
		if win != nil && ownWindowButtons {
			windowButtons(c, "tour-win", win, false)
		}
	})
	ui.Scroll(c.Key("tour-scroll")).Grow(1).MinHeight(0).FillWidth().Padding(8, tourPadX).Justify(ui.Center).ClipX().Children(func() {
		enter := &ui.Motion{X: tourSlide * float32(tr.dir)}
		if tr.dir == 0 {
			enter = &ui.Motion{Y: 10}
		}
		ui.Column(c.Key("tour-step-" + strconv.Itoa(int(step)))).FillWidth().MaxWidth(tourBodyW).
			Transition(ui.ElementTransition{Duration: 320 * time.Millisecond, Enter: enter, Exit: &ui.Motion{Scale: 0.98}}).
			Children(func() { a.tourStepView(c, tr, cur, busy) })
	})
	ui.Row(c.Key("tour-foot")).Height(tourFootH).Shrink(0).FillWidth().Padding(0, tourPadX).Gap(10).AlignItems(ui.Center).Children(func() {
		if tr.at > 0 && ui.Button(c.Key("tour-back"), L.TourBack()).Disabled(busy).Clicked() {
			move(-1)
		}
		if tr.failure != "" && !busy && last {
			ui.Text(c, tr.failure).FontSize(sizeSmall).TextColor(textTone(t, t.Danger)).Grow(1).MinWidth(0)
		} else {
			ui.Box(c).Grow(1)
		}
		if busy {
			busyMark(c, L.RunModeSwitching(), busySize)
			ui.Text(c, L.RunModeSwitching()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		label := L.TourNext()
		switch {
		case step == tourWelcome:
			label = L.TourStart()
		case last && tr.replay:
			label = L.TourDone()
		case last && tr.mode != cur:
			label = L.TourInstallFinish()
		case last:
			label = L.TourFinish()
		}
		if ui.PrimaryButton(c.Key("tour-next"), label).Disabled(busy).Clicked() {
			switch {
			case !last:
				move(1)
			case tr.replay:
				a.closeTour()
			default:
				a.finishTour()
			}
		}
	})
	if tr.askSkip && !tr.replay {
		a.tourSkipCard(c)
	}
}

// tourDots are the steps as dots, the current one long.
func tourDots(c *ui.Context, n, at int) {
	t := c.Theme()
	ui.Row(c.Key("tour-dots")).Gap(5).AlignItems(ui.Center).Label(L.TourStepOf(strconv.Itoa(at+1), strconv.Itoa(n))).Children(func() {
		for i := range n {
			dot := ui.Box(c.Key("tour-dot-" + strconv.Itoa(i)))
			w, col := tourDot, t.Border
			switch {
			case i == at:
				w, col = tourDotOn, t.Text
			case i < at:
				col = t.TextMuted
			}
			dot.Width(dot.AnimateTo("w", w, ui.Snappy)).Height(tourDot).Radius(tourDot / 2).Background(col)
		}
	})
}

// tourSkipCard asks before Skip: the first run then starts in No
// privileges, which needs no administrator.
func (a *App) tourSkipCard(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("tour-skip-card")).Absolute().Top(tourTopH-4).Right(tourPadX-12).Width(tourSkipW).
		Padding(16).Gap(10).Radius(cardRadius).Background(t.Background).Border(1, t.Border).Shadow(0, 12, 32, 0, sheetShadow.Alpha(0.4)).
		Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -6}}).
		Children(func() {
			ui.Text(c, L.TourSkipTitle()).Bold()
			ui.Text(c, L.TourSkipBody()).FontSize(sizeSmall + 1).TextColor(t.TextMuted)
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("tour-skip-keep"), L.TourSkipKeep()).Clicked() {
					a.tourEdit(func(tr *tour) { tr.askSkip = false })
				}
				if ui.PrimaryButton(c.Key("tour-skip-go"), L.TourSkip()).Clicked() {
					a.endTour(false)
				}
			})
		})
}

// tourArt is the step's illustration (tourart.go), its values animated
// here as the step's choices change.
func (a *App) tourArt(c *ui.Context) {
	t := c.Theme()
	cur := a.currentMode()
	a.mu.Lock()
	if a.tour == nil {
		a.mu.Unlock()
		return
	}
	tr := *a.tour
	var granted [4]bool
	device := ""
	if a.status != nil {
		device = groupDigits(a.status.DeviceID)
		if p := a.status.Permissions; p != nil {
			granted = [4]bool{p.Screen, p.Accessibility, p.InputMonitoring, p.Keychain}
		}
	}
	a.mu.Unlock()
	// On the window's material the drawings' faces are the page's; on the
	// sheet's surface too.
	col := artColors{text: t.Text, muted: t.TextMuted, card: t.Background, edge: t.Border, surface: t.Surface,
		flow: t.Accent, accent: t.Accent, good: t.Success, warn: t.Warning}
	box := ui.Box(c.Key("tour-art")).Grow(1).FillWidth().Clip()
	bool1 := func(b bool) float32 {
		if b {
			return 1
		}
		return 0
	}
	// Each run mode reaches one more of the screens than the one before.
	reach := func(i int) float32 { return box.AnimateTo("reach-"+strconv.Itoa(i), bool1(tr.mode >= i), ui.Smooth) }
	step := tr.step()
	mode := tr.mode
	if tr.replay && step == tourReady {
		mode = cur // the ready step shows the run mode in use, as its rows do
	}
	st := artState{
		step:    step,
		direct:  box.AnimateTo("direct", bool1(tr.direct), ui.Smooth),
		ask:     box.AnimateTo("ask", bool1(tr.ask), ui.Smooth),
		kind:    tr.kind,
		reach:   [3]float32{reach(0), reach(1), reach(2)},
		granted: granted,
		mode:    mode,
		device:  device,
		still:   c.Preferences().ReduceMotion,
	}
	box.Children(func() {
		scene := ui.Box(c.Key("tour-art-" + strconv.Itoa(int(step)))).Absolute().Top(0).Left(0).Right(0).Bottom(0).
			Transition(ui.ElementTransition{Duration: 300 * time.Millisecond, Enter: &ui.Motion{}, Exit: &ui.Motion{}})
		if step == tourReady {
			st.pop = scene.Keyframes("pop", []ui.Keyframe{{Value: 0}, {At: 450 * time.Millisecond, Value: 1, Ease: tourPopIn}},
				ui.KeyframeOptions{Delay: 300 * time.Millisecond})
		}
		s := st
		scene.Draw(func(p *ui.Painter, r ui.Rect) {
			drawArt(p, ui.Rect{X: r.X + tourArtPad, Y: r.Y + tourArtPad, W: r.W - 2*tourArtPad, H: r.H - 2*tourArtPad}, s, col)
		})
	})
}

// tourHeading is a step's title and its sentence.
func tourHeading(c *ui.Context, title, body string) {
	t := c.Theme()
	ui.Text(c, title).FontSize(tourTitle).Bold()
	ui.Text(c, body).TextColor(t.TextMuted).Margin(10, 0, 0, 0)
}

// tourFact is a line with a mark: yes (1), no (-1) or a note (0).
func tourFact(c *ui.Context, kind int, text string) {
	t := c.Theme()
	icon, ink, face := navAbout, t.TextMuted, t.Surface
	switch kind {
	case 1:
		icon, ink, face = rowCheck, textTone(t, t.Success), t.Success.Alpha(0.14)
	case -1:
		icon, ink, face = paneClose, textTone(t, t.Danger), t.Danger.Alpha(0.14)
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Margin(10, 0, 0, 0).Children(func() {
		ui.Box(c).Size(tourMark, tourMark).Shrink(0).Radius(tourMark / 2).Center().Background(face).Children(func() {
			ui.Icon(c, icon).Size(12, 12).TextColor(ink)
		})
		ui.Text(c, text).Grow(1).MinWidth(0)
	})
}

// tourStepView is the content of a step.
func (a *App) tourStepView(c *ui.Context, tr tour, cur int, busy bool) {
	switch tr.step() {
	case tourWelcome:
		tourWelcomeStep(c)
	case tourNetwork:
		a.tourNetworkStep(c, tr)
	case tourConnect:
		a.tourConnectStep(c, tr)
	case tourLetIn:
		a.tourLetInStep(c, tr)
	case tourAI:
		a.tourAIStep(c, tr)
	case tourPrivacy:
		a.tourPrivacyStep(c)
	case tourAway:
		a.tourAwayStep(c, tr, cur, busy)
	case tourAwake:
		tourAwakeStep(c)
	case tourReady:
		a.tourReadyStep(c, tr, cur, busy)
	}
}

// tourWelcomeStep: what Kyokai does, and what the tour covers.
func tourWelcomeStep(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).Size(tourIcon, tourIcon).Radius(14).Center().Background(t.Accent).Children(func() {
		ui.Icon(c, appMark).Size(32, 32).TextColor(artWhite)
	})
	ui.Text(c, L.TourWelcomeTitle()).FontSize(sizeTitle+8).Bold().Margin(16, 0, 0, 0)
	ui.Text(c, L.TourWelcomeBody()).FontSize(tourLead).TextColor(t.TextMuted).Margin(10, 0, 0, 0)
	topics := []string{L.TourTopicNetwork(), L.TourTopicConnect(), L.TourTopicLetIn(), L.TourTopicAI()}
	if artPlatform == "darwin" {
		topics = append(topics, L.TourTopicPrivacy())
	}
	topics = append(topics, L.TourTopicAway())
	ui.Row(c.Key("tour-topics")).Wrap().Gap(8).Margin(18, 0, 0, 0).Children(func() {
		for _, s := range topics {
			ui.Row(c).Height(26).Padding(0, 11).Radius(999).AlignItems(ui.Center).Background(t.Surface).Children(func() {
				ui.Text(c, s).FontSize(sizeSmall).NoWrap()
			})
		}
	})
	ui.Text(c, L.TourWelcomeNote()).FontSize(sizeSmall).TextColor(t.TextMuted).Margin(16, 0, 0, 0)
}

// tourStarting stands in for what the engine has not told yet.
func tourStarting(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(16, 0, 0, 0).Children(func() {
		busyMark(c, L.EngineStarting(), busySize)
		ui.Text(c, L.EngineStarting()).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
}

// tourNetworkStep: devices meet in EasyTier networks, through the server
// or directly; in the first run the Network page's form adds one, shown
// again the networks kept and Open Network settings.
func (a *App) tourNetworkStep(c *ui.Context, tr tour) {
	t := c.Theme()
	tourHeading(c, L.TourNetworkTitle(), L.TourNetworkBody())
	idx := 0
	if tr.direct {
		idx = 1
	}
	ui.Row(c).Margin(18, 0, 6, 0).Children(func() {
		if ui.Segmented(c.Key("tour-direct"), &idx, L.TourViaServer(), L.TourDirect()).Changed() {
			a.tourEdit(func(tr *tour) { tr.direct = idx == 1 })
		}
	})
	if tr.direct {
		ask := L.TourDirectAskWin()
		switch artPlatform {
		case "darwin":
			ask = L.TourDirectAskMac()
		case "linux":
			ask = L.TourDirectAskLinux()
		}
		tourFact(c, 1, L.TourDirectYes())
		tourFact(c, -1, L.TourDirectListens()+" "+ask)
		tourFact(c, 0, L.TourDirectOff())
	} else {
		tourFact(c, 1, L.TourViaServerYes())
		tourFact(c, 0, L.TourViaServerNote())
	}
	a.mu.Lock()
	ready := a.connected && a.status != nil
	st, busy := a.net, a.busy["network"]
	states := map[string]string{}
	if ready {
		for _, n := range a.status.Network.Networks {
			states[n.ID] = n.State
		}
	}
	a.mu.Unlock()
	if !ready {
		tourStarting(c)
		return
	}
	if !st.loaded && !busy {
		a.loadNetworks()
	}
	ui.Column(c.Key("tour-nets")).FillWidth().Margin(18, 0, 0, 0).Gap(gap).Children(func() {
		if len(st.profiles) > 0 {
			ui.Column(c.Key("tour-net-list")).FillWidth().Radius(listRadius).Border(1, t.Border).Clip().Children(func() {
				r := &rows{a: a}
				for _, p := range st.profiles {
					state := states[p.ID]
					if !p.Joined {
						state = "off"
					}
					word, col := L.NetworkNotJoined(), t.TextMuted
					switch state {
					case "online":
						word, col = L.NetworkOnline(), t.Success
					case "connecting":
						word, col = L.NetworkConnecting(), t.Warning
					case "error":
						word, col = L.NetworkError(), t.Danger
					}
					r.row(c, "tour-net-"+p.ID, tile(rowGlobe), setting{title: networkLabel(p)}, func() { statePill(c, word, col) })
				}
			})
		}
		switch {
		case tr.replay:
			if len(st.profiles) == 0 {
				ui.Text(c, L.NetworkNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
			}
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("tour-open-net"), L.TourOpenNetwork()).Clicked() {
					a.leaveTour(pageNetwork)
				}
			})
		case st.form != nil && st.form.id == "":
			ui.Column(c.Key("tour-net-form")).FillWidth().Padding(cardPad).Radius(cardRadius).Border(1, t.Border).Children(func() {
				a.networkForm(c, *st.form, busy)
			})
		case len(st.profiles) < maxNetworks:
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("tour-net-add"), L.NetworkAdd()).Disabled(busy || !st.loaded).Clicked() {
					a.mu.Lock()
					a.net.form = &netForm{}
					a.mu.Unlock()
				}
				ui.Text(c, L.TourNetworkLater()).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			})
		}
	})
}

// tourConnectStep: a Device ID or an address on Home, and what Open as
// opens there.
func (a *App) tourConnectStep(c *ui.Context, tr tour) {
	tourHeading(c, L.TourConnectTitle(), L.TourConnectBody())
	idx := tr.kind
	ui.Row(c).Margin(18, 0, 6, 0).Children(func() {
		kinds := []string{L.PaneDesktop(), L.ServiceSSH(), L.ServiceRDP(), L.ServiceVNC(), L.HomeOpenAI()}
		if ui.Segmented(c.Key("tour-kind"), &idx, kinds...).Changed() {
			a.tourEdit(func(tr *tour) { tr.kind = idx })
		}
	})
	lines := []string{L.TourKindDesktop(), L.TourKindSSH(), L.TourKindRDP(), L.TourKindVNC(), L.TourKindAI()}
	tourFact(c, 0, lines[min(max(idx, 0), len(lines)-1)])
	tourFact(c, 1, L.TourConnectChecked())
}

// tourLetInStep: this Device ID and its temporary password, what a correct
// password lets in and what asks, and that the person here always wins.
func (a *App) tourLetInStep(c *ui.Context, tr tour) {
	t := c.Theme()
	tourHeading(c, L.TourLetInTitle(), L.TourLetInBody())
	a.mu.Lock()
	ready := a.connected && a.status != nil
	var id, mode string
	if ready {
		id, mode = a.status.DeviceID, a.status.PasswordMode
	}
	a.mu.Unlock()
	if !ready {
		tourStarting(c)
	} else {
		ui.Column(c.Key("tour-device")).FillWidth().Margin(18, 0, 0, 0).Padding(12, 16).Gap(8).Radius(cardRadius).Border(1, t.Border).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.HomeDeviceID()).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
				ui.Text(c, groupDigits(id)).FontSize(sizeHeading + 2).Bold().FontFeatures("tnum").NoWrap().Selectable()
				if iconButton(c, "tour-copy-id", rowCopy, L.ActionCopy()) {
					mygo.Clipboard.WriteText(id)
					a.update(func() { a.setToast(L.Copied(), false) })
				}
			})
			if temporary, _ := passwordsAccepted(mode); temporary {
				ui.Divider(c)
				a.passwordRow(c, "tour-")
			}
		})
		ui.Text(c, L.TourPasswordNote()).FontSize(sizeSmall).TextColor(t.TextMuted).Margin(6, 0, 0, 0)
	}
	idx := 0
	if tr.ask {
		idx = 1
	}
	ui.Row(c).Margin(18, 0, 6, 0).Children(func() {
		if ui.Segmented(c.Key("tour-ask"), &idx, L.TourWithPassword(), L.TourWithoutPassword()).Changed() {
			a.tourEdit(func(tr *tour) { tr.ask = idx == 1 })
		}
	})
	if tr.ask {
		tourFact(c, 0, L.TourWithoutAsk())
	} else {
		tourFact(c, 1, L.TourWithYes())
		tourFact(c, -1, L.TourWithNo())
	}
	tourFact(c, 1, L.TourYouWin())
}

// stopKeys is the shortcut that stops a running AI task, as this system
// names its keys.
func stopKeys(goos string) string {
	if goos == "darwin" {
		return "Control+Option+Shift+Esc"
	}
	return "Ctrl+Alt+Shift+Esc"
}

// tourAIStep: an AI task on this computer, with its provider's key, its
// approvals and the person's own input that pauses it; the first run may
// open AI providers when it ends (no key is typed in the tour).
func (a *App) tourAIStep(c *ui.Context, tr tour) {
	t := c.Theme()
	tourHeading(c, L.TourAITitle(), L.TourAIBody())
	ui.Box(c).Height(8)
	tourFact(c, 1, L.TourAIKey())
	tourFact(c, 1, L.TourAIApprove())
	tourFact(c, 1, L.TourAIPause())
	tourFact(c, 0, L.TourAIStop(stopKeys(artPlatform)))
	if tr.replay {
		ui.Row(c).Margin(18, 0, 0, 0).Children(func() {
			if ui.Button(c.Key("tour-open-ai"), L.TourOpenAINow()).Clicked() {
				a.leaveTour(pageAI)
			}
		})
		return
	}
	ui.Row(c.Key("tour-ai-row")).FillWidth().Gap(14).Padding(12, 16).Radius(cardRadius).Border(1, t.Border).AlignItems(ui.Center).Margin(20, 0, 0, 0).Children(func() {
		ui.Box(c).Size(tileSize, tileSize).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
			ui.Icon(c, navAI).Size(16, 16).TextColor(t.TextMuted)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, L.TourOpenAI())
			ui.Text(c, L.TourOpenAIAbout()).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		on := tr.openAI
		if ui.Switch(c.Key("tour-ai-sw"), &on).Label(L.TourOpenAI()).Changed() {
			a.tourEdit(func(tr *tour) { tr.openAI = on })
		}
	})
}

// tourPrivacyStep (macOS): the permissions macOS asks for, as Access lists
// them, each Allowed or with Allow; one skipped stays on Home.
func (a *App) tourPrivacyStep(c *ui.Context) {
	t := c.Theme()
	tourHeading(c, L.TourPrivacyTitle(), L.TourPrivacyBody())
	a.mu.Lock()
	var p = a.status
	helperMode := p != nil && p.RunMode == "helper"
	a.mu.Unlock()
	if p == nil || p.Permissions == nil {
		tourStarting(c)
		return
	}
	perms := *p.Permissions
	ui.Column(c.Key("tour-perms")).FillWidth().Margin(18, 0, 0, 0).Radius(listRadius).Border(1, t.Border).Clip().Children(func() {
		a.privacyList(c, &rows{a: a}, "tour-perm", &perms, helperMode)
	})
	ui.Text(c, L.TourPrivacyLater()).FontSize(sizeSmall).TextColor(t.TextMuted).Margin(10, 0, 0, 0)
}

// tourAwayStep: the run modes by how far others reach while nobody is at
// this computer, one to choose (applied on finishing), and Open at login.
func (a *App) tourAwayStep(c *ui.Context, tr tour, cur int, busy bool) {
	t := c.Theme()
	tourHeading(c, L.TourAwayTitle(), L.TourAwayBody())
	a.mu.Lock()
	login := a.openAtLogin
	a.mu.Unlock()
	options := []struct{ line, tag string }{
		{L.TourAwayUser(), L.TourNoInstall()},
		{L.TourAwayFollow(), L.TourOnePrompt()},
		{L.TourAwayBackground(), L.TourOnePrompt()},
	}
	ui.Column(c.Key("tour-modes")).FillWidth().Gap(8).Margin(18, 0, 0, 0).Children(func() {
		for m, o := range options {
			on := m == tr.mode
			b := ui.ToggleBase(c.Key("tour-mode-"+strconv.Itoa(m)), &on).FillWidth().Padding(12, 14).Radius(11).
				Label(modeTitle(m)).Disabled(busy)
			face, edge := t.Background, t.Border
			switch {
			case m == tr.mode:
				face, edge = t.Accent.Alpha(0.1), t.Accent
			case b.Hovered():
				face = t.SurfaceHover
			}
			b.Background(face).Border(1, edge)
			if b.Changed() {
				a.tourEdit(func(tr *tour) { tr.mode = m })
			}
			b.Children(func() {
				ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
					ring := t.Border
					if m == tr.mode {
						ring = t.Accent
					}
					ui.Box(c).Size(16, 16).Shrink(0).Radius(8).Border(1.6, ring).Center().Margin(2, 0, 0, 0).Children(func() {
						if m == tr.mode {
							ui.Box(c).Size(8, 8).Radius(4).Background(t.Accent)
						}
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, modeTitle(m)).Bold()
						ui.Text(c, o.line).FontSize(sizeSmall + 0.5).TextColor(t.TextMuted)
					})
					tag, tone := o.tag, t.TextMuted
					switch {
					case tr.replay && m == cur:
						tag, tone = L.RunModeInUse(), t.Success
					case m != modeUser:
						tone = t.Warning
					}
					ui.Text(c, tag).FontSize(sizeSmall).TextColor(textTone(t, tone)).NoWrap().Shrink(0).
						Padding(2, 8).Radius(chipRadius).Background(tone.Alpha(0.12))
				})
			})
		}
	})
	if artPlatform == "darwin" && tr.mode != modeUser {
		ui.Text(c, L.TourAwayHelperMac()).FontSize(sizeSmall).TextColor(t.TextMuted).Margin(8, 0, 0, 0)
	}
	sub := L.TourLoginOff()
	switch {
	case tr.mode == modeBackground:
		sub = L.TourLoginService()
	case login:
		sub = L.TourLoginOn()
	}
	ui.Row(c.Key("tour-login-row")).FillWidth().Gap(14).Padding(10, 16).Radius(cardRadius).Border(1, t.Border).AlignItems(ui.Center).Margin(16, 0, 0, 0).Children(func() {
		ui.Box(c).Size(tileSize, tileSize).Shrink(0).Radius(8).Center().Background(tileBackground(t)).Children(func() {
			ui.Icon(c, rowPower).Size(16, 16).TextColor(t.TextMuted)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, L.SettingsOpenAtLogin())
			ui.Text(c, sub).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		on := login
		if ui.Switch(c.Key("tour-login"), &on).Label(L.SettingsOpenAtLogin()).Changed() {
			a.setOpenAtLogin(on)
		}
	})
}

// tourAwakeStep: sleep takes this computer offline in every run mode, and
// where this system keeps it awake; on Windows, that the picture needs a
// display (a monitor or a dummy plug), else Remote Desktop or a virtual
// display driver; on Linux, that the picture is the desktop someone is
// signed in to. A Mac shares its screen without one.
func tourAwakeStep(c *ui.Context) {
	t := c.Theme()
	tourHeading(c, L.TourAwakeTitle(), L.TourAwakeBody())
	tips := []string{L.TourAwakeWin1(), L.TourAwakeWin2()}
	switch artPlatform {
	case "darwin":
		tips = []string{L.TourAwakeMac1(), L.TourAwakeMac2()}
	case "linux":
		tips = []string{L.TourAwakeLinux1(), L.TourAwakeLinux2()}
	}
	for i, tip := range tips {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Margin(12, 0, 0, 0).Children(func() {
			ui.Box(c).Size(tourMark, tourMark).Shrink(0).Radius(tourMark / 2).Center().Background(t.Surface).Children(func() {
				ui.Text(c, strconv.Itoa(i+1)).FontSize(sizeSmall).Bold()
			})
			ui.Text(c, tip).Grow(1).MinWidth(0)
		})
	}
	if url := powerSettingsURL(artPlatform); url != "" {
		ui.Row(c).Margin(16, 0, 0, 0).Children(func() {
			if ui.Button(c.Key("tour-power"), L.TourOpenPower()).Clicked() {
				go func() { _ = mygo.Shell.OpenExternal(url) }()
			}
		})
	}
	if artPlatform == "darwin" {
		return
	}
	warn := textTone(t, t.Warning)
	ui.Row(c.Key("tour-display")).FillWidth().Gap(12).Padding(14, 16).Radius(cardRadius).AlignItems(ui.Start).
		Background(t.Warning.Alpha(0.12)).Border(1, t.Warning.Alpha(0.3)).Margin(18, 0, 0, 0).Children(func() {
		ui.Icon(c, kindDesktop).Size(16, 16).TextColor(warn).Margin(1, 0, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			title, body := L.TourDisplayTitle(), L.TourDisplayBody()
			if artPlatform == "linux" {
				title, body = L.TourDisplayTitleLinux(), L.TourDisplayBodyLinux()
			}
			ui.Text(c, title).Bold()
			ui.Text(c, body).FontSize(sizeSmall + 1)
		})
	})
}

// powerSettingsURL opens the system's power settings: Windows' Power &
// sleep, macOS's Energy; "" on Linux, whose desktops share no address
// for them.
func powerSettingsURL(goos string) string {
	switch goos {
	case "darwin":
		return "x-apple.systempreferences:com.apple.Energy-Settings.extension"
	case "linux":
		return ""
	}
	return "ms-settings:powersleep"
}

// tourReadyStep: what was chosen and what finishing does; shown again
// from Settings, the run mode in use and, when another was looked at,
// Switch.
func (a *App) tourReadyStep(c *ui.Context, tr tour, cur int, busy bool) {
	shown := tr.mode
	switch {
	case tr.replay && tr.mode != cur:
		shown = cur
		tourHeading(c, L.TourReplayTitle(), L.TourReplayOther(modeTitle(cur), modeTitle(tr.mode)))
	case tr.replay:
		shown = cur
		tourHeading(c, L.TourReplayTitle(), L.TourReplayCurrent(modeTitle(cur)))
	case tr.mode == modeUser:
		tourHeading(c, L.TourReadyTitle(), L.TourReadyUser())
	case artPlatform == "darwin":
		tourHeading(c, L.TourReadyTitle(), L.TourReadyAuthMac())
	case artPlatform == "linux":
		tourHeading(c, L.TourReadyTitle(), L.TourReadyAuthLinux())
	default:
		tourHeading(c, L.TourReadyTitle(), L.TourReadyAuthWin())
	}
	a.mu.Lock()
	login := a.openAtLogin
	id := ""
	if a.status != nil {
		id = groupDigits(a.status.DeviceID)
	}
	var joined []string
	for _, p := range a.net.profiles {
		if p.Joined {
			joined = append(joined, networkLabel(p))
		}
	}
	a.mu.Unlock()
	nets := L.TourNoNetwork()
	if len(joined) > 0 {
		nets = strings.Join(joined, ", ")
	}
	opens := L.TourOff()
	if login {
		opens = L.TourOn()
	}
	ui.Column(c).FillWidth().Margin(14, 0, 0, 0).Children(func() {
		title := L.TourSummary()
		if tr.replay {
			title = L.TourSummaryNow()
		}
		a.section(c, "tour-sum", title, nil, func(r *rows) {
			value := func(s string) func() {
				return func() { ui.Text(c, s).TextColor(c.Theme().TextMuted).NoWrap() }
			}
			if id != "" {
				r.row(c, "tour-sum-id", tile(rowID), setting{title: L.HomeDeviceID()}, value(id))
			}
			r.row(c, "tour-sum-net", tile(rowGlobe), setting{title: L.NetworkTitle()}, value(nets))
			r.row(c, "tour-sum-mode", tile(navSecurity), setting{title: L.SettingsRunMode()}, value(modeTitle(shown)))
			r.row(c, "tour-sum-login", tile(rowPower), setting{title: L.SettingsOpenAtLogin()}, value(opens))
			if !tr.replay {
				ai := L.TourAILater()
				if tr.openAI {
					ai = L.TourAIAfter()
				}
				r.row(c, "tour-sum-ai", tile(navAI), setting{title: L.TourTopicAI()}, value(ai))
			}
		})
	})
	if tr.replay && tr.mode != cur {
		ui.Row(c).Margin(16, 0, 0, 0).Children(func() {
			if ui.Button(c.Key("tour-switch"), L.TourSwitchTo(modeTitle(tr.mode))).Disabled(busy).Clicked() {
				a.closeTour()
				a.switchMode(cur, tr.mode)
			}
		})
	}
}

// modeTitle is a run mode's name.
func modeTitle(m int) string {
	return []string{L.RunModeUser(), L.RunModeFollow(), L.RunModeBackground()}[min(max(m, 0), modeBackground)]
}

// languageSelect chooses the interface language, the system's first.
// Side effects: switches the language at once and saves it.
func (a *App) languageSelect(c *ui.Context, key string, width float32) {
	langs := mygo.App.Languages()
	options := []string{L.LanguageSystem()}
	for _, l := range langs {
		options = append(options, i18n.Autonym(l))
	}
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	cur := options[0]
	if i := slices.Index(langs, p.Language); i >= 0 {
		cur = options[i+1]
	}
	if ui.Select(c.Key(key), &cur, options).Width(width).Label(L.SettingsLanguage()).Changed() {
		lang := ""
		if i := slices.Index(options, cur); i > 0 {
			lang = langs[i-1]
		}
		mygo.App.SetLocale(lang)
		p.Language = lang
		a.setPrefs(p)
	}
}

// setOpenAtLogin starts Kyokai at login or no longer does.
// Side effects: changes the system's login items; a refusal shows as a
// toast.
func (a *App) setOpenAtLogin(on bool) {
	if err := mygo.App.SetOpenAtLogin(on); err != nil {
		a.update(func() { a.setToast(err.Error(), true) })
		return
	}
	a.mu.Lock()
	a.openAtLogin = on
	a.mu.Unlock()
}
