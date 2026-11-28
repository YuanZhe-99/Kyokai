package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func (ms *mobileState) autoSizeCap() string {
	if len(ms.decoders) == 0 {
		return ""
	}
	maxSide := 0
	for _, d := range ms.decoders {
		maxSide = max(maxSide, d.MaxWidth, d.MaxHeight)
	}
	switch {
	case maxSide <= 0 || maxSide >= 3840:
		return ""
	case maxSide >= 2560:
		return "1440"
	default:
		return "1080"
	}
}

func (a *App) mobileEnsureViewerUI(key string, sink TouchpadSink) *mobileViewerUI {
	ms := a.mobile
	a.mu.Lock()
	spd := a.prefs.PointerSpeed
	handle := a.prefs.ToolbarHandle
	a.mu.Unlock()
	if spd <= 0 {
		spd = 1.6
	}
	u := ms.views[key]
	if u != nil {
		u.pad.SetSink(sink)
		u.pad.SetSpeed(spd)
		u.kb.SetSink(sink, spd)
		return u
	}
	u = &mobileViewerUI{
		pad:     NewTouchpad(sink, true, spd),
		kb:      NewMobileKeyboard(sink, spd),
		zoom:    mobile.ZoomState{Scale: 1},
		curX:    0.5,
		curY:    0.5,
		curInit: true,
	}
	if handle != nil {
		u.dragHandle = ui.Point{X: handle[0], Y: handle[1]}
	} else {
		u.dragHandle = ui.Point{X: 0.5, Y: 0}
	}
	ms.views[key] = u
	return u
}

type kyokaiTouchpadSink struct {
	v     *viewer
	ui    *mobileViewerUI
	picW  float32
	picH  float32
	viewW float32
	viewH float32
}

func (s *kyokaiTouchpadSink) Move(dx, dy float32) {
	w, h := max(s.picW, 1), max(s.picH, 1)
	s.ui.curX = min(max(s.ui.curX+dx/w, 0), 1)
	s.ui.curY = min(max(s.ui.curY+dy/h, 0), 1)
	s.v.mu.Lock()
	may := s.v.control
	s.v.mu.Unlock()
	if may {
		s.v.queue(inputEvent{Kind: "pointer", Action: "move", X: s.ui.curX, Y: s.ui.curY})
	}
	if s.v.app != nil {
		s.v.app.later(func() {})
	}
}

func (s *kyokaiTouchpadSink) Button(button int, down bool) {
	act := "up"
	if down {
		act = "down"
	}
	s.v.mu.Lock()
	may := s.v.control
	s.v.mu.Unlock()
	if may {
		s.v.queue(inputEvent{Kind: "pointer", Action: act, Button: button, X: s.ui.curX, Y: s.ui.curY})
	}
}

func (s *kyokaiTouchpadSink) Scroll(dx, dy float32) {
	s.v.mu.Lock()
	may := s.v.control
	s.v.mu.Unlock()
	if may {
		s.v.queue(inputEvent{Kind: "scroll", X: s.ui.curX, Y: s.ui.curY, DX: dx, DY: dy})
	}
}

func (s *kyokaiTouchpadSink) Pinch(scale, focalX, focalY, panX, panY float32) {
	z := &s.ui.zoom
	if z.Scale <= 0 {
		z.Scale = 1
	}
	next := min(max(z.Scale*scale, 1.0), 5.0)
	k := next / z.Scale
	cx := focalX - s.viewW/2
	cy := focalY - s.viewH/2
	z.X = cx - (cx-z.X)*k + panX
	z.Y = cy - (cy-z.Y)*k + panY
	z.Scale = next
	mx := s.viewW * (z.Scale - 1) / 2
	my := s.viewH * (z.Scale - 1) / 2
	z.X = min(max(z.X, -mx), mx)
	z.Y = min(max(z.Y, -my), my)
	if s.v.app != nil {
		s.v.app.later(func() {})
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// mobileLaptopGeometry computes the upper screen height, hinge crease height, and
// lower keyboard+touchpad deck height for foldable Laptop Mode.
func mobileLaptopGeometry(winH, topSafe, botSafe float32, posture androidhost.Posture, scale float32, useSafe bool) (stageH, creaseH, deckH float32) {
	insetTop := float32(0)
	if useSafe {
		insetTop = topSafe
	}
	avail := max(winH-insetTop, 0)
	if botSafe > mobileKeyboardThreshold {
		avail = max(avail-botSafe, 0)
	}
	if scale <= 0 {
		scale = 1
	}
	if posture.HingeTop > 0 {
		hingeTopDP := float32(posture.HingeTop) / scale
		hingeBotDP := float32(posture.HingeBottom) / scale
		if hingeBotDP < hingeTopDP {
			hingeBotDP = hingeTopDP
		}
		stageH = hingeTopDP - insetTop
		creaseH = max(hingeBotDP-hingeTopDP, 0)
		deckH = winH - hingeBotDP
		if botSafe > mobileKeyboardThreshold {
			deckH = max(deckH-botSafe, 0)
		}
		if stageH >= 120 && deckH >= 180 {
			return stageH, creaseH, deckH
		}
	}
	stageH = avail * 0.48
	deckH = max(avail-stageH, 0)
	return stageH, 0, deckH
}

func mobileGeomBucket(w, h float32) string {
	orient := "P"
	if w > h {
		orient = "L"
	}
	return fmt.Sprintf("%s:%dx%d", orient, int(w/32), int(h/32))
}

// shouldAutoOpenKeyboard reports whether the letterbox blank area below a
// fitted remote screen is generous enough to open the on-screen keyboard
// automatically.
func shouldAutoOpenKeyboard(availW, availH float32, picW, picH int, _ bool) bool {
	if availW <= 0 || availH <= 0 || picW <= 0 || picH <= 0 {
		return false
	}
	_, _, _, ph := pictureBox(ui.Rect{W: availW, H: availH}, picW, picH)
	blankH := availH - ph
	return blankH >= 240 && blankH/availH >= 0.34
}

// evalMobileAutoKeyboard automatically opens or closes the virtual keyboard
// when viewport orientation/geometry enters a new bucket and vertical blank
// space crosses the auto-open threshold. Manual user toggles inside a bucket
// are preserved.
func (a *App) evalMobileAutoKeyboard(u *mobileViewerUI, availW, availH float32, picW, picH int, splitPref, autoPref, laptopActive bool) {
	if laptopActive || picW <= 0 || picH <= 0 || availW <= 0 || availH <= 0 {
		return
	}
	bucket := mobileGeomBucket(availW, availH)
	if bucket == u.autoKBGeom {
		return
	}
	u.autoKBGeom = bucket
	want := autoPref && shouldAutoOpenKeyboard(availW, availH, picW, picH, splitPref)
	if want && !u.kbOpen {
		u.kbOpen = true
		u.kbAutoOpened = true
	} else if !want && u.kbOpen && u.kbAutoOpened {
		u.kbOpen = false
		u.kbAutoOpened = false
	}
}

func mobileConfigureViewerRoot(root ui.Element, topSafe, rightSafe, nonIMESafeBot, leftSafe float32, laptopActive, useSafe bool) {
	if !laptopActive {
		return
	}
	if !useSafe {
		root.Margin(-topSafe, -rightSafe, -nonIMESafeBot, -leftSafe)
		return
	}
	root.Margin(0, 0, -nonIMESafeBot, 0)
}

func (a *App) mobileStageZoomResetPill(c *ui.Context, mc mobileColors, key string, u *mobileViewerUI) ui.Rect {
	if u.zoom.Scale <= 1.05 {
		return ui.Rect{}
	}
	zb := ui.ButtonBase(c.Key(key)).Absolute().Right(12).Bottom(12).MinWidth(52).Height(42).PaddingX(14).
		Radius(21).Background(mc.SurfaceContainerHighest.Alpha(0.90)).Border(1, mc.OutlineVariant.Alpha(0.5)).
		Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.30)).Center().Clip().Label(L.MobileA11yResetZoom())
	mobile.Ink(zb)
	zoomRect := zb.LayoutBounds()
	zb.Children(func() {
		ui.Text(c, fmt.Sprintf("%.1f×", u.zoom.Scale)).FontSize(12).FontWeight(700).TextColor(mc.OnSurface)
	})
	if zb.Clicked() {
		mygo.Haptics.Light()
		u.zoom.Reset()
	}
	if zoomRect.W <= 0 {
		a.later(func() {})
	}
	return zoomRect
}

func mobileZone2Touch(u *mobileViewerUI, padRect ui.Rect, rt androidhost.RawTouch, now time.Time) {
	if u.kb.pad == nil {
		return
	}
	lx := float32(rt.X) - padRect.X
	ly := float32(rt.Y) - padRect.Y
	switch rt.Phase {
	case "down":
		u.kb.pad.Down(rt.ID, lx, ly, now)
	case "move":
		u.kb.pad.Move(rt.ID, lx, ly, now)
	case "up":
		u.kb.pad.Up(rt.ID, now)
	case "cancel":
		u.kb.pad.Cancel(rt.ID)
	}
}

func (a *App) mobileActiveSessionView(c *ui.Context, mc mobileColors, active string, winW float32) {
	ms := a.mobile
	if ts := ms.terms[active]; ts != nil {
		a.mobileSSHView(c, mc, active, ts)
		return
	}
	a.mu.Lock()
	v := a.viewers[active]
	svc := a.services[active]
	a.mu.Unlock()

	switch {
	case v != nil:
		v.setFront(ms.inFront)
		a.mobileKyokaiViewer(c, mc, v, winW)
	case svc != nil:
		svc.setFront(ms.inFront)
		a.mobileDesktopServiceViewer(c, mc, svc, winW)
	default:
		ms.activeSession = ""
	}
}

func (a *App) mobileKyokaiViewer(c *ui.Context, mc mobileColors, v *viewer, winW float32) {
	ms := a.mobile

	v.mu.Lock()
	phase := v.phase
	bm := v.bm
	start := v.start
	state := v.state
	ptr := v.pointer
	fps, kbps := v.fps, v.kbps
	host := v.host
	dec := v.decoder
	ttff := v.firstFrameMS
	asleep := v.asleep
	v.mu.Unlock()

	// Before connected or after ended: show connection / password / host confirmation panel with Back.
	if phase.State != "connected" {
		a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)
		ui.Column(c.Key("mob-vconn-" + v.tabKey())).Fill().Background(mc.Surface).Children(func() {
			m3TopBar(c, mc, mobile.TopBarOptions{
				Title: v.title,
				Leading: func() {
					if m3IconButton(c, "mob-vc-back", mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
				Trailing: func() {
					if m3IconButton(c, "mob-vc-disc", paneClose, L.MobileDisconnect(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
						v.disconnect()
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
			})
			v.connectPanel(c, phase)
		})
		return
	}

	v.present()
	v.mu.Lock()
	bm = v.bm
	v.mu.Unlock()

	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()

	sink := &kyokaiTouchpadSink{v: v}
	u := a.mobileEnsureViewerUI(v.tabKey(), sink)
	sink.ui = u
	spd := p.PointerSpeed
	if spd <= 0 {
		spd = 1.6
	}
	u.pad.SetSpeed(spd)
	if ptr.Visible && !u.pad.Active() && (u.kb.pad == nil || !u.kb.pad.Active()) {
		u.curX, u.curY = ptr.X, ptr.Y
	}

	_, winH := c.Size()
	topSafe, rightSafe, botSafe, leftSafe := c.SafeArea()
	nonIMESafeBot := botSafe
	if nonIMESafeBot > mobileKeyboardThreshold {
		nonIMESafeBot = 0
	}
	laptopActive := p.LaptopMode && ms.posture.HalfOpened
	useSafe := p.LaptopSafeArea == nil || *p.LaptopSafeArea

	availW := max(winW-leftSafe-rightSafe, 0)
	availH := max(winH-topSafe-nonIMESafeBot, 0)
	a.evalMobileAutoKeyboard(u, availW, availH, start.Width, start.Height, p.SplitKeyboard, p.autoKeyboardOn(), laptopActive)

	kbKey := v.device + ":kyokai"
	if v.kvm {
		kbKey = v.device + ":nanokvm"
	}
	osOverride := p.KeyboardOS[kbKey]
	effOS := NormalizeHostOS(osOverride, phase.Peer.OS, v.kvm)

	sendScan := func(code uint32, down, caps bool) {
		ev := inputEvent{Kind: "scan", Code: code, Down: down, Caps: caps}
		v.mu.Lock()
		may := v.control
		swap := v.swapping()
		v.mu.Unlock()
		if swap {
			ev = swapCtrlMeta(ev)
		}
		if may {
			v.queue(ev)
		}
	}

	var ox, oy, pw, ph float32
	var touchMode bool
	var stageBounds, barRect, zoomRect ui.Rect
	var stageH, creaseH, deckH float32
	if laptopActive {
		stageH, creaseH, deckH = mobileLaptopGeometry(winH, topSafe, botSafe, ms.posture, u.dipScale, useSafe)
	}

	rootCol := ui.Column(c.Key("mob-viewer-" + v.tabKey())).Fill().Background(ui.RGB(0, 0, 0))
	mobileConfigureViewerRoot(rootCol, topSafe, rightSafe, nonIMESafeBot, leftSafe, laptopActive, useSafe)
	rootCol.Children(func() {
		// Screen stage (above hinge in laptop mode, else grows to fill above keyboard).
		stage := ui.Box(c.Key("mob-vstage-" + v.tabKey())).FillWidth().Clip().Background(ui.RGB(0, 0, 0)).
			Role(ui.RoleImage).Label(L.MobileA11yRemoteScreen(v.title)).Focusable()
		stage.Draw(func(pt *ui.Painter, _ ui.Rect) { u.dipScale = pt.Scale() })
		if laptopActive {
			stage.Height(stageH).Shrink(0)
		} else {
			stage.Grow(1).Basis(0).MinHeight(0)
		}

		bounds := stage.LayoutBounds()
		stageBounds = bounds
		sink.viewW, sink.viewH = bounds.W, bounds.H
		ox, oy, pw, ph = pictureBox(bounds, start.Width, start.Height)
		if p.PictureTop {
			oy = 0
		}
		sink.picW, sink.picH = pw, ph
		touchMode = p.PointerMode == "touch"

		stage.HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputKeyDown, ui.InputKeyUp:
				return u.kb.HandleHardwareKey(ev, sendScan)
			case ui.InputText:
				if ev.Text != "" {
					v.queue(inputEvent{Kind: "text", Text: ev.Text})
					return true
				}
			}
			return false
		})

		stage.Children(func() {
			if bm != nil && state == "" && start.Placeholder == "" {
				zs := max(u.zoom.Scale, 1)
				canvas := ui.Box(c.Key("mob-vzoom")).Fill().Translate(u.zoom.X, u.zoom.Y).Scale(zs, zs)
				canvas.Children(func() {
					ui.Image(c.Key("mob-vbmp"), bm).Absolute().Left(ox).Top(oy).Width(pw).Height(ph)
					if !p.HidePointer && pw > 0 && ph > 0 {
						csz := p.CursorSize
						if csz <= 0 {
							csz = 16
						}
						cx := ox + u.curX*pw - csz/2
						cy := oy + u.curY*ph - csz/2
						ui.Box(c.Key("mob-vcur")).Absolute().Left(cx).Top(cy).Size(csz, csz).Radius(csz/2).
							Background(ui.RGBA(255, 255, 255, 0.85)).Border(2, mc.Primary)
					}
				})
			}

			// Dedicated touch/pointer hit layer beneath overlay controls so toolbar
			// and status buttons receive clicks without stage swallowing pointer down.
			hitLayer := ui.Box(c.Key("mob-vhit-" + v.tabKey())).Absolute().Left(0).Top(0).Right(0).Bottom(0)
			for _, te := range hitLayer.Touches() {
				now := time.Now()
				if !touchMode {
					switch te.Phase {
					case ui.TouchBegan:
						u.pad.Down(int(te.ID), te.X, te.Y, now)
					case ui.TouchMoved:
						u.pad.Move(int(te.ID), te.X, te.Y, now)
					case ui.TouchEnded:
						u.pad.Up(int(te.ID), now)
					case ui.TouchCanceled:
						u.pad.Cancel(int(te.ID))
					}
				}
			}
			hitLayer.HandleInput(func(ev ui.InputEvent) bool {
				switch ev.Kind {
				case ui.InputScroll:
					dx, dy := wheelNotches(ev)
					sink.Scroll(dx, dy)
					return true
				case ui.InputZoom:
					sink.Pinch(ev.Scale, ev.X, ev.Y, 0, 0)
					return true
				case ui.InputPointerDown, ui.InputPointerMove, ui.InputPointerUp:
					if ms.host != nil {
						return false
					}
					if touchMode && pw > 0 && ph > 0 {
						nx := min(max((ev.X-ox)/pw, 0), 1)
						ny := min(max((ev.Y-oy)/ph, 0), 1)
						u.curX, u.curY = nx, ny
						act := map[ui.InputKind]string{ui.InputPointerMove: "move", ui.InputPointerDown: "down", ui.InputPointerUp: "up"}[ev.Kind]
						btn := max(ev.Button, 0)
						v.queue(inputEvent{Kind: "pointer", Action: act, Button: btn, X: nx, Y: ny})
						return true
					}
					now := time.Now()
					isTouch := ev.Pointer == ui.PointerTouch
					switch ev.Kind {
					case ui.InputPointerDown:
						if !isTouch {
							if pw > 0 && ph > 0 {
								u.curX = min(max((ev.X-ox)/pw, 0), 1)
								u.curY = min(max((ev.Y-oy)/ph, 0), 1)
							}
							v.queue(inputEvent{Kind: "pointer", Action: "down", Button: max(ev.Button, 0), X: u.curX, Y: u.curY})
							return true
						}
						u.pad.Down(0, ev.X, ev.Y, now)
						return true
					case ui.InputPointerMove:
						if !isTouch {
							if pw > 0 && ph > 0 {
								u.curX = min(max((ev.X-ox)/pw, 0), 1)
								u.curY = min(max((ev.Y-oy)/ph, 0), 1)
							}
							v.queue(inputEvent{Kind: "pointer", Action: "move", X: u.curX, Y: u.curY})
							return true
						}
						u.pad.Move(0, ev.X, ev.Y, now)
						return true
					case ui.InputPointerUp:
						if !isTouch {
							v.queue(inputEvent{Kind: "pointer", Action: "up", Button: max(ev.Button, 0), X: u.curX, Y: u.curY})
							return true
						}
						u.pad.Up(0, now)
						return true
					}
				}
				return false
			})

			switch {
			case state != "":
				ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Padding(24).Gap(12).Children(func() {
					ui.Text(c, state).TextColor(ui.RGB(255, 255, 255)).TextAlign(ui.Center)
					if m3FilledButton(c, mc, "mob-vreconn", L.MobileTryAgain(), false).Clicked() {
						v.redial("", false)
					}
				})
			case bm == nil || start.Placeholder != "":
				waitTxt, _ := viewerWaitText(host.Video, start.Placeholder)
				ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Padding(24).Gap(12).Children(func() {
					if start.Placeholder == "asleep" || asleep {
						ui.Text(c, L.MobileHostAsleep()).FontSize(16).Bold().TextColor(ui.RGB(255, 255, 255))
						if m3FilledButton(c, mc, "mob-vwake-now", L.MobileActionWake(), false).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": "wake"})
							a.setToast(L.MobileWakeSent(), false)
						}
					} else {
						busyMarkIn(c, waitTxt, busySizeLarge, ui.RGB(255, 255, 255))
						ui.Text(c, waitTxt).TextColor(ui.RGB(255, 255, 255)).TextAlign(ui.Center)
					}
				})
			}

			// Video diagnostics overlay when enabled.
			if p.VideoOverlay && start.Codec != "" {
				hwTag := "SW"
				if dec.hardware {
					hwTag = "HW"
				}
				info := fmt.Sprintf("%s · %s (%s) · %d×%d · %.0f fps · %.1f Mbps · RTT %d ms · Enc %d ms · TTFF %d ms",
					codecLabel(start.Codec), dec.name, hwTag, start.Width, start.Height, fps, kbps/1000.0, host.DelayMS, host.EncodeMS, ttff)
				ui.Text(c, info).Absolute().Left(8).Bottom(8).Padding(4, 8).Radius(8).
					Background(ui.RGBA(0, 0, 0, 0.72)).TextColor(ui.RGB(235, 240, 245)).FontSize(11).FontFeatures("tnum")
			}

			// Reset zoom pill (> 1.05x).
			zoomRect = a.mobileStageZoomResetPill(c, mc, "mob-vzoom-reset", u)

			// Floating collapsible toolbar.
			barRect = a.mobileFloatingToolbar(c, mc, v.tabKey(), u, stageBounds, func() {
				u.kbAutoOpened = false
				u.kbOpen = !u.kbOpen
			}, func() {
				if !v.kvm {
					_ = a.useAILink(v.device, "")
					ms.aiSheetDevice = v.device
					ms.aiSheetOpen = true
				}
			}, func() {
				ms.menuSheetOpen = true
			})
		})

		if laptopActive && creaseH > 0 {
			ui.Box(c.Key("mob-vcrease-" + v.tabKey())).FillWidth().Height(creaseH).Shrink(0).Background(ui.RGB(0, 0, 0))
		}

		// On-screen keyboard (dedicated laptop deck below hinge in laptop mode, else dock on demand).
		if u.kbOpen || laptopActive {
			target := "Kyokai · Map"
			if v.kvm {
				target = "NanoKVM"
			}
			u.kb.View(c, "mob-kb-"+v.tabKey(), winW, MobileKeyboardOptions{
				TargetLabel: target,
				HostOS:      effOS,
				OSPicked:    osOverride != "",
				AutoLabel:   L.MobileAuto(),
				Split:       p.SplitKeyboard && !laptopActive,
				Laptop:      laptopActive,
				DeckHeight:  deckH,
				UseSafeArea: useSafe,
				NativeTouch: ms.host != nil,
				Colors:      &mc,
				Dispatch:    a.later,
				OnOSPick: func(choice string) {
					a.setMobileKeyboardOS(kbKey, choice)
				},
				OnSplit: func(sp bool) {
					a.mu.Lock()
					pr := a.prefs
					a.mu.Unlock()
					pr.SplitKeyboard = sp
					a.setPrefs(pr)
				},
				OnHide: func() {
					u.kbOpen = false
					u.kbAutoOpened = false
				},
				OnSpecial: func(action string) bool {
					if action == "ctrl_alt_del" {
						v.sendControl(map[string]any{"type": "action", "action": "ctrl-alt-del"})
						return true
					}
					return false
				},
				OnTypeText: func() {
					ms.typeTextValue = ""
					ms.typeTextOpen = true
				},
				Send: sendScan,
			})
		} else {
			u.kb.padRect = ui.Rect{}
		}

		sheetOpen := ms.clipSheetOpen || ms.aiSheetOpen || ms.aiHistOpen || ms.typeTextOpen || ms.menuSheetOpen
		canCapture := !sheetOpen && state == "" && bm != nil && start.Placeholder == "" && stageBounds.W > 0 && stageBounds.H > 0
		var padRect ui.Rect
		if u.kbOpen || laptopActive {
			padRect = u.kb.padRect
		}
		if (laptopActive || (u.kbOpen && p.SplitKeyboard && winW >= 600)) && padRect.W <= 0 {
			// First frame after opening the split/laptop deck: LayoutBounds() reflects
			// the previous frame, so request one more frame to latch padRect cleanly.
			a.later(func() {})
		}
		a.mobileUpdateTouchCapture(canCapture, stageBounds, padRect, barRect, zoomRect, func(rt androidhost.RawTouch) {
			now := time.Now()
			if rt.Zone == 2 {
				mobileZone2Touch(u, padRect, rt, now)
				return
			}
			lx := float32(rt.X) - stageBounds.X
			ly := float32(rt.Y) - stageBounds.Y
			if rt.Kind == "mouse" || touchMode {
				if pw <= 0 || ph <= 0 {
					return
				}
				nx := min(max((lx-ox)/pw, 0), 1)
				ny := min(max((ly-oy)/ph, 0), 1)
				u.curX, u.curY = nx, ny
				btn := 0
				if rt.Buttons&2 != 0 {
					btn = 1
				} else if rt.Buttons&4 != 0 {
					btn = 2
				}
				v.mu.Lock()
				may := v.control
				v.mu.Unlock()
				if may {
					switch rt.Phase {
					case "down":
						v.queue(inputEvent{Kind: "pointer", Action: "down", Button: btn, X: nx, Y: ny})
					case "move":
						v.queue(inputEvent{Kind: "pointer", Action: "move", Button: btn, X: nx, Y: ny})
					case "up", "cancel":
						v.queue(inputEvent{Kind: "pointer", Action: "up", Button: btn, X: nx, Y: ny})
					}
				}
				a.later(func() {})
				return
			}
			switch rt.Phase {
			case "down":
				u.pad.Down(rt.ID, lx, ly, now)
			case "move":
				u.pad.Move(rt.ID, lx, ly, now)
			case "up":
				u.pad.Up(rt.ID, now)
			case "cancel":
				u.pad.Cancel(rt.ID)
			}
		})
	})
}

func (a *App) setMobileKeyboardOS(kbKey, choice string) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	m := make(map[string]string, len(p.KeyboardOS)+1)
	for k, val := range p.KeyboardOS {
		m[k] = val
	}
	if choice == "" {
		delete(m, kbKey)
	} else {
		m[kbKey] = choice
	}
	p.KeyboardOS = m
	a.setPrefs(p)
}

// mobileFloatingToolbar renders either the collapsed draggable edge handle or
// the Material 3 pill toolbar inside stageBounds, returning its window-dp hit
// exclusion rect for TouchBridge.
func (a *App) mobileFloatingToolbar(c *ui.Context, mc mobileColors, key string, u *mobileViewerUI, stageBounds ui.Rect, onKB, onAI, onMenu func()) ui.Rect {
	ms := a.mobile
	areaW, areaH := stageBounds.W, stageBounds.H
	winW, winH := c.Size()
	if areaW <= 0 {
		areaW = winW
	}
	if areaH <= 0 {
		areaH = winH
	}
	if stageBounds.W <= 0 || stageBounds.H <= 0 {
		a.later(func() {})
	}

	if u.barFolded {
		maxX := max(areaW-56, 0)
		maxY := max(areaH-56, 0)
		hb := ui.ButtonBase(c.Key("mob-bar-handle-" + key))
		sizeChanged := abs32(u.barAreaW-areaW) > 1 || abs32(u.barAreaH-areaH) > 1
		if !u.barOffsetInit || (sizeChanged && !hb.Dragging()) {
			u.barOffset = ui.Point{
				X: 6 + min(max(u.dragHandle.X, 0), 1)*maxX,
				Y: 6 + min(max(u.dragHandle.Y, 0), 1)*maxY,
			}
			u.barOffsetInit = true
			u.barAreaW, u.barAreaH = areaW, areaH
		}
		hb.Absolute().Left(0).Top(0).Size(44, 44).Radius(22).
			Background(mc.SurfaceContainerHighest.Alpha(0.92)).Border(1, mc.OutlineVariant.Alpha(0.55)).
			Shadow(0, 4, 14, 0, ui.RGBA(0, 0, 0, 0.35)).Center().Clip().
			Label(L.MobileA11yShowToolbar())
		mobile.Ink(hb)
		snapEdge := func(pt ui.Point) ui.Point {
			nx := float32(0)
			if maxX > 0 {
				nx = min(max((pt.X-6)/maxX, 0), 1)
			}
			ny := float32(0)
			if maxY > 0 {
				ny = min(max((pt.Y-6)/maxY, 0), 1)
			}
			dLeft, dRight, dTop, dBot := nx, 1-nx, ny, 1-ny
			switch min(dLeft, dRight, dTop, dBot) {
			case dTop:
				ny = 0
			case dBot:
				ny = 1
			case dLeft:
				nx = 0
			case dRight:
				nx = 1
			}
			return ui.Point{X: 6 + nx*maxX, Y: 6 + ny*maxY}
		}
		hb.Draggable(&u.barOffset, ui.DraggableOptions{
			Bounds: &ui.Rect{X: 6, Y: 6, W: maxX, H: maxY},
			Rigid:  true,
			Snap:   snapEdge,
		})
		hb.Children(func() { ui.Icon(c, barUnfold).FontSize(20).TextColor(mc.OnSurface) })

		if !hb.Dragging() && !hb.Transitioning() && maxX > 0 && maxY > 0 {
			nx := min(max((u.barOffset.X-6)/maxX, 0), 1)
			ny := min(max((u.barOffset.Y-6)/maxY, 0), 1)
			if abs32(nx-u.dragHandle.X) > 0.005 || abs32(ny-u.dragHandle.Y) > 0.005 {
				u.dragHandle = ui.Point{X: nx, Y: ny}
				a.mu.Lock()
				pr := a.prefs
				a.mu.Unlock()
				pair := [2]float32{nx, ny}
				pr.ToolbarHandle = &pair
				a.setPrefs(pr)
			}
		}
		pos := u.barOffset
		if hb.Dragging() || hb.Transitioning() {
			pos = hb.DragOffset()
		}
		rect := ui.Rect{
			X: stageBounds.X + pos.X - 6,
			Y: stageBounds.Y + pos.Y - 6,
			W: 56,
			H: 56,
		}
		u.lastBarRect = rect
		if hb.Clicked() {
			mygo.Haptics.Light()
			u.barFolded = false
			a.later(func() {})
		}
		return rect
	}

	slots := 4
	if onAI != nil {
		slots = 5
	}
	wantW := float32(slots)*50 + 20
	barW := wantW
	barLeft := float32(12)
	if areaW > 0 {
		barW = min(wantW, max(areaW-24, 200))
		barLeft = max((areaW-barW)/2, 12)
	}
	bar := ui.Row(c.Key("mob-bar-"+key)).Absolute().Top(10).Left(barLeft).Width(barW).Height(48).
		PaddingX(8).Radius(24).Background(mc.SurfaceContainerHighest.Alpha(0.92)).Border(1, mc.OutlineVariant.Alpha(0.55)).
		Shadow(0, 4, 14, 0, ui.RGBA(0, 0, 0, 0.35)).AlignItems(ui.Center).Justify(ui.SpaceBetween)
	btn := func(id, label string, icon *ui.SVG, active bool, tap func()) {
		fg := mc.OnSurface
		bg := ui.Transparent
		if active {
			fg = mc.OnPrimaryContainer
			bg = mc.PrimaryContainer
		}
		b := ui.ButtonBase(c.Key("mob-tb-"+id+"-"+key)).Size(44, 38).Radius(19).Background(bg).Center().Clip().Label(label)
		mobile.Ink(b)
		b.Children(func() {
			ui.Icon(c, icon).FontSize(19).TextColor(fg)
		})
		if b.Clicked() {
			mygo.Haptics.Light()
			tap()
		}
	}
	bar.Children(func() {
		btn("back", "Back", mobBackIcon, false, func() {
			ms.activeSession = ""
			a.mobileSyncSessions()
			a.homeVisibilityChanged()
		})
		btn("kb", L.MobileKeyboard(), barKeyboard, u.kbOpen, onKB)
		if onAI != nil {
			btn("ai", L.MobileAiTask(), navAI, ms.aiSheetOpen, onAI)
		}
		btn("fold", L.MobileA11yHideToolbar(), barFold, false, func() {
			u.barFolded = true
			a.later(func() {})
		})
		btn("more", L.MobileMore(), rowMore, false, onMenu)
	})
	rect := ui.Rect{
		X: stageBounds.X + barLeft - 4,
		Y: stageBounds.Y + 6,
		W: barW + 8,
		H: 56,
	}
	u.lastBarRect = rect
	return rect
}

// ---- RDP / VNC Mobile Viewer ------------------------------------------------

type deskTouchpadSink struct {
	sc    *screenClient
	ui    *mobileViewerUI
	fbW   int
	fbH   int
	viewW float32
	viewH float32
	btns  uint8
}

func (s *deskTouchpadSink) Move(dx, dy float32) {
	w, h := float32(max(s.fbW, 1)), float32(max(s.fbH, 1))
	s.ui.curX = min(max(s.ui.curX+dx/w, 0), 1)
	s.ui.curY = min(max(s.ui.curY+dy/h, 0), 1)
	if s.sc.desk != nil && s.fbW > 0 && s.fbH > 0 {
		px := int(s.ui.curX * float32(s.fbW-1))
		py := int(s.ui.curY * float32(s.fbH-1))
		_ = s.sc.desk.pointer(px, py, s.btns)
	}
	if s.sc.s != nil && s.sc.s.app != nil {
		s.sc.s.app.later(func() {})
	}
}

func (s *deskTouchpadSink) Button(button int, down bool) {
	bit := map[int]uint8{0: 1, 1: 4, 2: 2}[button]
	if down {
		s.btns |= bit
	} else {
		s.btns &^= bit
	}
	if s.sc.desk != nil && s.fbW > 0 && s.fbH > 0 {
		px := int(s.ui.curX * float32(s.fbW-1))
		py := int(s.ui.curY * float32(s.fbH-1))
		_ = s.sc.desk.pointer(px, py, s.btns)
	}
}

func (s *deskTouchpadSink) Scroll(dx, dy float32) {
	if s.sc.desk != nil && s.fbW > 0 && s.fbH > 0 {
		px := int(s.ui.curX * float32(s.fbW-1))
		py := int(s.ui.curY * float32(s.fbH-1))
		_ = s.sc.desk.scroll(px, py, float64(dx*20), float64(dy*20))
	}
}

func (s *deskTouchpadSink) Pinch(scale, focalX, focalY, panX, panY float32) {
	z := &s.ui.zoom
	if z.Scale <= 0 {
		z.Scale = 1
	}
	next := min(max(z.Scale*scale, 1.0), 5.0)
	k := next / z.Scale
	cx := focalX - s.viewW/2
	cy := focalY - s.viewH/2
	z.X = cx - (cx-z.X)*k + panX
	z.Y = cy - (cy-z.Y)*k + panY
	z.Scale = next
	if s.sc.s != nil && s.sc.s.app != nil {
		s.sc.s.app.later(func() {})
	}
}

func (a *App) mobileDesktopServiceViewer(c *ui.Context, mc mobileColors, s *serviceTab, winW float32) {
	ms := a.mobile
	s.mu.Lock()
	phase, client := s.phase, s.client
	s.mu.Unlock()

	sc, ok := client.(*screenClient)
	if phase != svcRunning || !ok || sc == nil {
		a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)
		ui.Column(c.Key("mob-sconn-" + s.tabKey())).Fill().Background(mc.Surface).Children(func() {
			m3TopBar(c, mc, mobile.TopBarOptions{
				Title: s.title,
				Leading: func() {
					if m3IconButton(c, "mob-sc-back", mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
				Trailing: func() {
					if m3IconButton(c, "mob-sc-disc", paneClose, L.MobileDisconnect(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
						s.disconnect()
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
			})
			s.view(c)
		})
		return
	}

	sc.mu.Lock()
	bm, fb := sc.bm, sc.fb
	sc.mu.Unlock()

	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()

	fbW, fbH := 0, 0
	if fb != nil {
		fbW, fbH = fb.Width, fb.Height
	}
	sink := &deskTouchpadSink{sc: sc, fbW: fbW, fbH: fbH}
	u := a.mobileEnsureViewerUI(s.tabKey(), sink)
	sink.ui = u

	_, winH := c.Size()
	topSafe, rightSafe, botSafe, leftSafe := c.SafeArea()
	nonIMESafeBot := botSafe
	if nonIMESafeBot > mobileKeyboardThreshold {
		nonIMESafeBot = 0
	}
	laptopActive := p.LaptopMode && ms.posture.HalfOpened
	useSafe := p.LaptopSafeArea == nil || *p.LaptopSafeArea

	availW := max(winW-leftSafe-rightSafe, 0)
	availH := max(winH-topSafe-nonIMESafeBot, 0)
	a.evalMobileAutoKeyboard(u, availW, availH, fbW, fbH, p.SplitKeyboard, p.autoKeyboardOn(), laptopActive)

	kbKey := s.device + ":" + s.service
	osOverride := p.KeyboardOS[kbKey]
	defaultOS := a.deviceOS(s.device)
	if s.service == serviceRDP && defaultOS == "" {
		defaultOS = "windows"
	}
	effOS := NormalizeHostOS(osOverride, defaultOS, false)

	sendScan := func(code uint32, down, _ bool) {
		if sc.desk == nil {
			return
		}
		if s.service == serviceVNC {
			sym := KeysymFor(code, u.kb.ShiftActive())
			_ = sc.desk.key(remoteKey{scan: code, sym: sym}, down)
		} else {
			_ = sc.desk.key(remoteKey{scan: code}, down)
		}
	}

	var dOx, dOy, dPw, dPh float32
	var dBounds, dBarRect, dZoomRect ui.Rect
	var stageH, creaseH, deckH float32
	if laptopActive {
		stageH, creaseH, deckH = mobileLaptopGeometry(winH, topSafe, botSafe, ms.posture, u.dipScale, useSafe)
	}

	rootCol := ui.Column(c.Key("mob-dsvc-" + s.tabKey())).Fill().Background(ui.RGB(0, 0, 0))
	mobileConfigureViewerRoot(rootCol, topSafe, rightSafe, nonIMESafeBot, leftSafe, laptopActive, useSafe)
	rootCol.Children(func() {
		stage := ui.Box(c.Key("mob-dstage-" + s.tabKey())).FillWidth().Clip().
			Role(ui.RoleImage).Label(L.MobileA11yRemoteScreen(s.name)).Focusable()
		stage.Draw(func(pt *ui.Painter, _ ui.Rect) { u.dipScale = pt.Scale() })
		if laptopActive {
			stage.Height(stageH).Shrink(0)
		} else {
			stage.Grow(1).Basis(0).MinHeight(0)
		}
		bounds := stage.LayoutBounds()
		dBounds = bounds
		sink.viewW, sink.viewH = bounds.W, bounds.H
		ox, oy, pw, ph := pictureBox(bounds, fbW, fbH)
		if p.PictureTop {
			oy = 0
		}
		dOx, dOy, dPw, dPh = ox, oy, pw, ph

		// Dynamic RDP resolution matching on rotation / foldable resize / laptop half-open.
		if s.service == serviceRDP && bounds.W > 200 && bounds.H > 120 && sc.desk != nil {
			scFactor := max(u.dipScale, 1)
			wantW := (int(bounds.W*scFactor) / 2) * 2
			wantH := (int(bounds.H*scFactor) / 2) * 2
			if wantW > 0 && wantH > 0 && (absInt(wantW-fbW) > 64 || absInt(wantH-fbH) > 64) {
				_ = sc.desk.setDisplay(min(wantW, 3840), min(wantH, 2160), 100)
			}
		}

		stage.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputKeyDown || ev.Kind == ui.InputKeyUp {
				return u.kb.HandleHardwareKey(ev, sendScan)
			}
			if ev.Kind == ui.InputText && ev.Text != "" && sc.desk != nil {
				_ = sc.desk.text(ev.Text)
				return true
			}
			return false
		})

		stage.Children(func() {
			if bm == nil {
				ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					busyMarkIn(c, L.MobileWaitingForPicture(), busySizeLarge, ui.RGB(255, 255, 255))
				})
			} else {
				zs := max(u.zoom.Scale, 1)
				ui.Box(c).Fill().Translate(u.zoom.X, u.zoom.Y).Scale(zs, zs).Children(func() {
					ui.Image(c.Key("mob-dbmp-"+s.tabKey()), bm).Absolute().Left(ox).Top(oy).Width(pw).Height(ph)
					csz := p.CursorSize
					if csz <= 0 {
						csz = 16
					}
					ui.Box(c).Absolute().Left(ox+u.curX*pw-csz/2).Top(oy+u.curY*ph-csz/2).
						Size(csz, csz).Radius(csz/2).Background(ui.RGBA(255, 255, 255, 0.85)).Border(2, mc.Primary)
				})
			}

			hitLayer := ui.Box(c.Key("mob-dhit-" + s.tabKey())).Absolute().Left(0).Top(0).Right(0).Bottom(0)
			for _, te := range hitLayer.Touches() {
				now := time.Now()
				if p.PointerMode != "touch" {
					switch te.Phase {
					case ui.TouchBegan:
						u.pad.Down(int(te.ID), te.X, te.Y, now)
					case ui.TouchMoved:
						u.pad.Move(int(te.ID), te.X, te.Y, now)
					case ui.TouchEnded:
						u.pad.Up(int(te.ID), now)
					case ui.TouchCanceled:
						u.pad.Cancel(int(te.ID))
					}
				}
			}
			hitLayer.HandleInput(func(ev ui.InputEvent) bool {
				switch ev.Kind {
				case ui.InputScroll:
					dx, dy := wheelNotches(ev)
					sink.Scroll(dx, dy)
					return true
				case ui.InputZoom:
					sink.Pinch(ev.Scale, ev.X, ev.Y, 0, 0)
					return true
				case ui.InputPointerDown, ui.InputPointerMove, ui.InputPointerUp:
					if ms.host != nil {
						return false
					}
					if p.PointerMode == "touch" && pw > 0 && ph > 0 && sc.desk != nil {
						u.curX = min(max((ev.X-ox)/pw, 0), 1)
						u.curY = min(max((ev.Y-oy)/ph, 0), 1)
						sink.Button(max(ev.Button, 0), ev.Kind == ui.InputPointerDown)
						return true
					}
					now := time.Now()
					switch ev.Kind {
					case ui.InputPointerDown:
						u.pad.Down(0, ev.X, ev.Y, now)
						return true
					case ui.InputPointerMove:
						u.pad.Move(0, ev.X, ev.Y, now)
						return true
					case ui.InputPointerUp:
						u.pad.Up(0, now)
						return true
					}
				}
				return false
			})

			dZoomRect = a.mobileStageZoomResetPill(c, mc, "mob-dzoom-reset", u)

			dBarRect = a.mobileFloatingToolbar(c, mc, s.tabKey(), u, bounds, func() {
				u.kbAutoOpened = false
				u.kbOpen = !u.kbOpen
			}, nil, func() {
				ms.menuSheetOpen = true
			})
		})

		if laptopActive && creaseH > 0 {
			ui.Box(c.Key("mob-dcrease-" + s.tabKey())).FillWidth().Height(creaseH).Shrink(0).Background(ui.RGB(0, 0, 0))
		}

		if u.kbOpen || laptopActive {
			lbl := "Remote Desktop"
			if s.service == serviceVNC {
				lbl = "VNC"
			}
			u.kb.View(c, "mob-dkb-"+s.tabKey(), winW, MobileKeyboardOptions{
				TargetLabel: lbl,
				HostOS:      effOS,
				OSPicked:    osOverride != "",
				AutoLabel:   L.MobileAuto(),
				Split:       p.SplitKeyboard && !laptopActive,
				Laptop:      laptopActive,
				DeckHeight:  deckH,
				UseSafeArea: useSafe,
				NativeTouch: ms.host != nil,
				Colors:      &mc,
				Dispatch:    a.later,
				OnOSPick:    func(ch string) { a.setMobileKeyboardOS(kbKey, ch) },
				OnSplit: func(sp bool) {
					a.mu.Lock()
					pr := a.prefs
					a.mu.Unlock()
					pr.SplitKeyboard = sp
					a.setPrefs(pr)
				},
				OnHide: func() {
					u.kbOpen = false
					u.kbAutoOpened = false
				},
				OnSpecial: func(act string) bool {
					if act == "ctrl_alt_del" {
						sc.ctrlAltDel()
						return true
					}
					return false
				},
				OnTypeText: func() {
					ms.typeTextValue = ""
					ms.typeTextOpen = true
				},
				Send: sendScan,
			})
		} else {
			u.kb.padRect = ui.Rect{}
		}

		sheetOpen := ms.clipSheetOpen || ms.aiSheetOpen || ms.aiHistOpen || ms.typeTextOpen || ms.menuSheetOpen
		canCapture := !sheetOpen && bm != nil && dBounds.W > 0 && dBounds.H > 0
		var padRect ui.Rect
		if u.kbOpen || laptopActive {
			padRect = u.kb.padRect
		}
		if (laptopActive || (u.kbOpen && p.SplitKeyboard && winW >= 600)) && padRect.W <= 0 {
			a.later(func() {})
		}
		a.mobileUpdateTouchCapture(canCapture, dBounds, padRect, dBarRect, dZoomRect, func(rt androidhost.RawTouch) {
			now := time.Now()
			if rt.Zone == 2 {
				mobileZone2Touch(u, padRect, rt, now)
				return
			}
			lx := float32(rt.X) - dBounds.X
			ly := float32(rt.Y) - dBounds.Y
			if rt.Kind == "mouse" || p.PointerMode == "touch" {
				if dPw <= 0 || dPh <= 0 || sc.desk == nil || fbW <= 0 || fbH <= 0 {
					return
				}
				u.curX = min(max((lx-dOx)/dPw, 0), 1)
				u.curY = min(max((ly-dOy)/dPh, 0), 1)
				btn := 0
				if rt.Buttons&2 != 0 {
					btn = 1
				} else if rt.Buttons&4 != 0 {
					btn = 2
				}
				switch rt.Phase {
				case "down":
					sink.Button(btn, true)
				case "move":
					px := int(u.curX * float32(fbW-1))
					py := int(u.curY * float32(fbH-1))
					_ = sc.desk.pointer(px, py, sink.btns)
				case "up", "cancel":
					sink.Button(btn, false)
				}
				a.later(func() {})
				return
			}
			switch rt.Phase {
			case "down":
				u.pad.Down(rt.ID, lx, ly, now)
			case "move":
				u.pad.Move(rt.ID, lx, ly, now)
			case "up":
				u.pad.Up(rt.ID, now)
			case "cancel":
				u.pad.Cancel(rt.ID)
			}
		})
	})
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---- SSH + SFTP/SCP Mobile Session ------------------------------------------

func (a *App) mobileStartSSH(device, address string) {
	ms := a.mobile
	n := 1
	key := serviceKey(device, serviceSSH, n)
	for ms.terms[key] != nil {
		n++
		key = serviceKey(device, serviceSSH, n)
	}
	about := a.recentAbout(device)
	a.mu.Lock()
	about = a.prefs.named(device, about)
	a.mu.Unlock()
	name := deviceTitle(about, device)
	ts := &mobileTermSession{
		app:     a,
		device:  device,
		name:    name,
		address: address,
		term: newMiniTerm(80, 24, nil, func() {
			a.update(func() {})
		}),
		state: "connecting",
		form:  credForm{method: methodKey, remember: true},
	}
	ms.terms[key] = ts
	ms.activeSession = key
	a.mobileSyncSessions()
	a.mobileDialSSH(ts, nil)
}

func (a *App) mobileCloseSSH(key string) {
	ms := a.mobile
	if ms == nil {
		return
	}
	a.mu.Lock()
	ts := ms.terms[key]
	delete(ms.terms, key)
	if ms.activeSession == key {
		ms.activeSession = ""
	}
	a.mu.Unlock()
	if ts != nil {
		ts.mu.Lock()
		conn, files := ts.conn, ts.files
		ts.conn, ts.files = nil, nil
		ts.mu.Unlock()
		if files != nil {
			files.close()
		}
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func (a *App) mobileDialSSH(ts *mobileTermSession, typed *credForm) {
	ts.mu.Lock()
	ts.state = "connecting"
	ts.errNote = ""
	trust := ts.trust
	ts.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		saved, _ := a.link.Credential(ctx, ts.device, serviceSSH)
		plan, ok := signInPlan(serviceSSH, typed, saved)
		if !ok {
			a.update(func() {
				ts.mu.Lock()
				ts.form = askedFor(serviceSSH, plan, saved)
				ts.state = "form"
				ts.mu.Unlock()
			})
			return
		}
		pinned := ""
		if saved != nil {
			pinned = saved.HostKey
		}
		conn, seen, err := sshDial(ctx, a.link, ts.device, ts.address, plan, typed != nil, pinned, trust)
		if err != nil {
			a.update(func() {
				ts.mu.Lock()
				defer ts.mu.Unlock()
				if errors.Is(err, errHostKeyChanged) {
					ts.changed = seen
					ts.state = "hostkey"
					return
				}
				if errors.Is(err, errSignInFailed) || errors.Is(err, errNeedSignIn) {
					ts.form = askedFor(serviceSSH, plan, saved)
					ts.state = "form"
					ts.errNote = L.MobileSvcSignInFailed()
					return
				}
				ts.state = "ended"
				ts.errNote = serviceFailure(err)
			})
			return
		}
		a.update(func() {
			ts.mu.Lock()
			ts.conn = conn
			ts.state = "connected"
			ts.mu.Unlock()
		})
		buf := make([]byte, 4096)
		for {
			n, rerr := conn.Read(buf)
			if n > 0 {
				ts.term.Feed(buf[:n])
			}
			if rerr != nil {
				a.update(func() {
					ts.mu.Lock()
					if ts.conn == conn {
						ts.state = "ended"
						ts.errNote = L.MobileSessionEnded()
					}
					ts.mu.Unlock()
				})
				return
			}
		}
	}()
}

func (a *App) mobileSSHView(c *ui.Context, mc mobileColors, key string, ts *mobileTermSession) {
	t := c.Theme()
	ms := a.mobile
	a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)

	ts.mu.Lock()
	state := ts.state
	errNote := ts.errNote
	changed := ts.changed
	form := ts.form
	filesOn := ts.filesOn
	ts.mu.Unlock()

	ui.Column(c.Key("mob-ssh-" + key)).Fill().Background(mc.Surface).Children(func() {
		m3TopBar(c, mc, mobile.TopBarOptions{
			Title: ts.name + " · SSH",
			Leading: func() {
				if m3IconButton(c, "mob-ssh-back-"+key, mobBackIcon, "Back", mc.OnSurface, ui.Transparent).Clicked() {
					ms.activeSession = ""
					a.mobileSyncSessions()
				}
			},
			Trailing: func() {
				if state == "connected" {
					col := mc.OnSurfaceVariant
					bg := ui.Transparent
					if filesOn {
						col = mc.OnPrimaryContainer
						bg = mc.PrimaryContainer
					}
					if m3IconButton(c, "mob-ssh-files-"+key, sshFiles, L.MobileFilesTitle(), col, bg).Clicked() {
						ts.mu.Lock()
						ts.filesOn = !ts.filesOn
						needOpen := ts.filesOn && ts.files == nil && ts.conn != nil
						conn := ts.conn
						ts.mu.Unlock()
						if needOpen {
							win := a.deviceOS(ts.device) == "windows"
							go func() {
								rf := openRemoteFiles(conn.client, win, func() { a.update(func() {}) })
								a.update(func() {
									ts.mu.Lock()
									ts.files = rf
									ts.mu.Unlock()
								})
							}()
						}
					}
					if m3IconButton(c, "mob-ssh-paste-"+key, barClipboard, L.MobilePaste(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
						if clip := mygo.Clipboard.ReadText(); clip != "" {
							ts.sendText(clip)
						}
					}
				}
				if m3IconButton(c, "mob-ssh-disc-"+key, paneClose, L.MobileDisconnect(), mc.OnSurfaceVariant, ui.Transparent).Clicked() {
					a.mobileCloseSSH(key)
					a.mobileSyncSessions()
				}
			},
		})

		switch state {
		case "connecting":
			emptyState(c, "mob-ssh-wait", func() { busyMark(c, L.MobileConnecting(), busySizeLarge) }, L.MobileConnecting(), "", nil)

		case "hostkey":
			ui.Column(c.Key("mob-ssh-hk")).Fill().AlignItems(ui.Center).Justify(ui.Center).Padding(24).Children(func() {
				m3Card(c, mc, "mob-ssh-hk-card").MaxWidth(420).Children(func() {
					ui.Text(c, L.MobileHostKeyChanged()).FontSize(17).Bold().TextColor(textTone(t, mc.Warning))
					ui.Text(c, L.MobileHostKeyChangedHint()).FontSize(13).TextColor(mc.OnSurfaceVariant)
					ui.Text(c, changed).FontSize(11).Font("monospace").TextColor(mc.OnSurface).Selectable()
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						if m3OutlinedButton(c, mc, "mob-hk-cancel", L.MobileCancel(), mc.OnSurfaceVariant, false).Grow(1).Clicked() {
							a.mobileCloseSSH(key)
						}
						if m3FilledButton(c, mc, "mob-hk-trust", L.MobileTrustKey(), false).Grow(1).Clicked() {
							ts.mu.Lock()
							ts.trust = changed
							ts.mu.Unlock()
							a.mobileDialSSH(ts, nil)
						}
					})
				})
			})

		case "form":
			ui.Scroll(c.Key("mob-ssh-form")).Grow(1).FillWidth().Padding(20).AlignItems(ui.Center).Children(func() {
				m3Card(c, mc, "mob-ssh-form-card").MaxWidth(420).Gap(12).Children(func() {
					if errNote != "" {
						ui.Text(c, errNote).FontSize(13).TextColor(mc.Error)
					}
					mIdx := 0
					if form.method == methodPassword {
						mIdx = 1
					}
					if m3Segmented(c, mc, "mob-ssh-m", &mIdx, L.MobileSignInKey(), L.MobilePassword()) {
						if mIdx == 1 {
							form.method = methodPassword
						} else {
							form.method = methodKey
						}
					}
					ui.TextInput(c.Key("mob-ssh-user"), &form.user).Placeholder(L.MobileUserName()).Label(L.MobileUserName()).FillWidth()
					if form.method == methodPassword {
						ui.TextInput(c.Key("mob-ssh-pw"), &form.password).Password().Placeholder(L.MobilePassword()).Label(L.MobilePassword()).FillWidth()
					} else {
						ui.Text(c, L.MobileSshKeyHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
					}
					ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileRememberSignIn()).Grow(1).TextColor(mc.OnSurface)
						m3Switch(c, "mob-ssh-rem", &form.remember, L.MobileRememberSignIn())
					})
					ts.mu.Lock()
					ts.form = form
					ts.mu.Unlock()
					if m3FilledButton(c, mc, "mob-ssh-go", L.MobileConnect(), strings.TrimSpace(form.user) == "").FillWidth().Clicked() {
						plan := form
						a.mobileDialSSH(ts, &plan)
					}
				})
			})

		case "ended":
			emptyState(c, "mob-ssh-end", func() { ui.Icon(c, kindSSH).FontSize(36).TextColor(mc.Error) }, errNote, "", func() {
				if m3FilledButton(c, mc, "mob-ssh-retry", L.MobileTryAgain(), false).Clicked() {
					a.mobileDialSSH(ts, nil)
				}
			})

		case "connected":
			if filesOn {
				a.mobileSSHFilesView(c, key, ts)
				return
			}
			termBox := ui.Scroll(c.Key("mob-ssh-out-"+key)).Grow(1).Basis(0).FillWidth().MinHeight(0).
				Background(ui.RGB(10, 12, 16)).Padding(8, 10)
			tb := termBox.LayoutBounds()
			if tb.W > 120 && tb.H > 80 {
				cols := max(int((tb.W-20)/7.8), 40)
				rows := max(int((tb.H-16)/17.0), 12)
				if ts.term.Resize(cols, rows) && ts.conn != nil {
					_ = ts.conn.Resize(cols, rows)
				}
			}
			lines, _, _ := ts.term.Lines()
			termBox.Children(func() {
				for i, line := range lines {
					ui.Text(c.Key("tl-"+strconv.Itoa(i)), line).Font("monospace").FontSize(13).
						TextColor(ui.RGB(232, 236, 242)).NoWrap()
				}
			})
			// Shell input bar + mobile extra-key bar.
			ui.Row(c.Key("mob-ssh-inrow-"+key)).FillWidth().Padding(6, 8).Gap(6).Background(t.Surface).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("mob-ssh-in-"+key), &ts.input).Placeholder("Command…").Grow(1).MinWidth(0).Font("monospace")
				if in.Changed() && (ts.ctrl || ts.alt) && len(ts.input) == 1 {
					ch := ts.input
					ts.input = ""
					ts.sendText(ch)
				}
				if in.Submitted() || ui.PrimaryButton(c.Key("mob-ssh-enter-"+key), "⏎").Height(40).Clicked() {
					cmd := ts.input
					ts.input = ""
					ts.sendText(cmd + "\r")
				}
			})
			ts.viewExtraBar(c, key)
		}
	})
}

func (a *App) mobileSSHFilesView(c *ui.Context, key string, ts *mobileTermSession) {
	t := c.Theme()
	ts.mu.Lock()
	rf := ts.files
	ts.mu.Unlock()
	if rf == nil {
		emptyState(c, "mob-files-open", func() { busyMark(c, L.FilesOpening(), busySizeLarge) }, L.FilesOpening(), "", nil)
		return
	}
	p := &ts.panel
	if p.root == "" {
		p.root = rf.home
	}
	if rf.sftp == nil {
		ui.Column(c.Key("mob-scp-" + key)).Grow(1).FillWidth().Padding(16).Gap(10).Children(func() {
			ui.Text(c, L.MobileFilesNoSftp()).TextColor(t.TextMuted)
			ui.TextInput(c.Key("mob-scp-path"), &p.scpPath).Placeholder("/remote/path/file").FillWidth()
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if ui.Button(c.Key("mob-scp-up"), L.MobileFilesUpload()).Grow(1).Height(46).Clicked() {
					dest := strings.TrimSpace(p.scpPath)
					if dest != "" {
						go func() {
							picked, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Multiple: true})
							if err == nil && len(picked) > 0 {
								rf.upload(picked, dest)
							}
						}()
					}
				}
				if ui.PrimaryButton(c.Key("mob-scp-dl"), L.MobileFilesDownload()).Grow(1).Height(46).Clicked() {
					src := strings.TrimSpace(p.scpPath)
					if src != "" {
						a.mobileDownloadAndShare(rf, src)
					}
				}
			})
		})
		return
	}

	dir := rf.snapshot(p.root)
	ui.Column(c.Key("mob-sftp-" + key)).Grow(1).Basis(0).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("mob-sftp-bar")).FillWidth().Padding(8, 12).Gap(8).Background(t.Surface).AlignItems(ui.Center).Children(func() {
			upBtn := ui.ButtonBase(c.Key("mob-sftp-up")).Size(40, 40).Radius(10).Background(t.Background).Border(1, t.Border).Center().Disabled(p.root == "/").Label("Up")
			upBtn.Children(func() { ui.Icon(c, filesUp).FontSize(17).TextColor(t.Text) })
			if upBtn.Clicked() {
				p.root = path.Dir(p.root)
			}
			ui.Text(c, p.root).FontSize(13).SingleLine().Grow(1).MinWidth(0)
			mkBtn := ui.ButtonBase(c.Key("mob-sftp-mkdir")).Size(40, 40).Radius(10).Background(t.Background).Border(1, t.Border).Center().Label("New folder")
			mkBtn.Children(func() { ui.Icon(c, filesNewDir).FontSize(17).TextColor(t.Text) })
			if mkBtn.Clicked() {
				p.name = "new-folder"
				if err := rf.sftp.Mkdir(path.Join(p.root, p.name)); err == nil {
					rf.read(p.root)
				}
			}
			if ui.PrimaryButton(c.Key("mob-sftp-upload"), L.MobileFilesUpload()).Height(40).Clicked() {
				targetDir := p.root
				go func() {
					picked, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Multiple: true})
					if err == nil && len(picked) > 0 {
						rf.upload(picked, targetDir)
					}
				}()
			}
		})

		ui.Scroll(c.Key("mob-sftp-list")).Grow(1).FillWidth().Padding(8, 12).Gap(6).Children(func() {
			if dir.loading && dir.entries == nil {
				ui.Text(c, L.FilesReading()).TextColor(t.TextMuted)
				return
			}
			if len(dir.entries) == 0 {
				ui.Text(c, L.MobileFilesEmpty()).TextColor(t.TextMuted)
				return
			}
			for _, e := range dir.entries {
				e := e
				full := path.Join(p.root, e.name)
				row := ui.Row(c.Key("mob-f-"+full)).FillWidth().MinHeight(52).Padding(8, 12).Radius(12).
					Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Gap(10)
				row.Children(func() {
					ic := filesFile
					col := t.TextMuted
					if e.dir {
						ic = rowFolder
						col = t.Accent
					}
					ui.Icon(c, ic).FontSize(20).TextColor(col)
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, e.name).SingleLine()
						if !e.dir {
							ui.Text(c, sizeLabel(e.size)).FontSize(11).TextColor(t.TextMuted)
						}
					})
					if !e.dir {
						dlBtn := ui.ButtonBase(c.Key("mob-fdl-"+full)).Size(38, 38).Radius(10).Background(t.Background).Border(1, t.Border).Center().Label(L.MobileFilesDownload())
						dlBtn.Children(func() { ui.Icon(c, filesDown).FontSize(16).TextColor(t.Text) })
						if dlBtn.Clicked() {
							a.mobileDownloadAndShare(rf, full)
						}
					}
					delBtn := ui.ButtonBase(c.Key("mob-fdel-"+full)).Size(38, 38).Radius(10).Center().Label(L.MobileFilesDelete())
					delBtn.Children(func() { ui.Icon(c, paneClose).FontSize(16).TextColor(t.TextMuted) })
					if delBtn.Clicked() {
						if a.confirm(L.MobileFilesDeleteAsk(e.name), L.MobileFilesDelete()) {
							go func() {
								if e.dir {
									_ = rf.sftp.RemoveDirectory(full)
								} else {
									_ = rf.sftp.Remove(full)
								}
								rf.read(p.root)
							}()
						}
					}
				})
				if e.dir && row.Clicked() {
					p.root = full
					rf.read(full)
				}
			}
		})
	})
}

func (a *App) mobileDownloadAndShare(rf *remoteFiles, remotePath string) {
	tmpDir := filepath.Join(os.TempDir(), "kyokai-dl-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if os.MkdirAll(tmpDir, 0o700) != nil {
		return
	}
	rf.download([]string{remotePath}, tmpDir)
	a.update(func() { a.setToast(L.MobileFilesDownloading(path.Base(remotePath)), false) })
	go func() {
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			for _, x := range rf.list() {
				if x.src == remotePath && x.dst == tmpDir {
					rf.mu.Lock()
					st, at := x.state, x.at
					rf.mu.Unlock()
					if st == xferDone && at != "" {
						_ = mygo.Share(mygo.ShareOptions{Files: collectShareFiles([]string{at})})
						return
					}
					if st == xferFailed || st == xferCanceled {
						return
					}
				}
			}
		}
	}()
}

// ---- Bottom Sheets: Menu, Type Text, Clipboard History, AI Task & History ---

func (a *App) mobileSheets(c *ui.Context, mc mobileColors, _ bool) {
	ms := a.mobile

	// 1. Session options menu sheet (⋮).
	mobile.Sheet(c, &ms.menuSheetOpen, mobile.SheetOptions{Detents: []float32{0.62, 0.9}, Label: L.MobileMore()}, func() {
		a.mu.Lock()
		v := a.viewers[ms.activeSession]
		svc := a.services[ms.activeSession]
		a.mu.Unlock()
		ui.Scroll(c.Key("mob-menu-scroll")).Grow(1).FillWidth().Padding(12, 16, 24, 16).Gap(12).Children(func() {
			if v != nil {
				v.mu.Lock()
				displays := v.displays
				preset := v.preset
				fpsCap := v.fpsCap
				size := v.size
				codec := v.codec
				offer := slices.Clone(v.offer)
				mute := v.mute
				shareClip := !v.clip.off
				lockAfter := v.lockAfter
				actions := v.actions
				v.mu.Unlock()

				ui.Text(c, v.title).FontSize(18).FontWeight(700).TextColor(mc.OnSurface)

				if len(displays.List) > 1 {
					ui.Text(c, L.MobileDisplays()).FontSize(13).FontWeight(600).TextColor(mc.OnSurfaceVariant)
					ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
						for i := range displays.List {
							idx := i
							lbl := L.MobileDisplayN(i + 1)
							var btn ui.Element
							if i == displays.Current {
								btn = m3TonalButton(c, mc, "mob-disp-"+strconv.Itoa(i), lbl+" ✓", false)
							} else {
								btn = m3OutlinedButton(c, mc, "mob-disp-"+strconv.Itoa(i), lbl, mc.OnSurfaceVariant, false)
							}
							if btn.Clicked() {
								v.sendControl(map[string]any{"type": "display", "index": idx})
							}
						}
					})
				}

				ui.Text(c, L.MobileQuality()).FontSize(13).FontWeight(600).TextColor(mc.OnSurfaceVariant)
				pIdx := min(max(preset, 0), 2)
				if m3Segmented(c, mc, "mob-q-preset", &pIdx, L.MobileQualitySpeed(), L.MobileQualityBalanced(), L.MobileQualityBest()) {
					v.setQuality(pIdx, fpsCap, false)
				}

				m3Card(c, mc, "mob-menu-opts").Children(func() {
					resList := []string{"auto", "native", "2160", "1440", "1080", "720"}
					resLabels := make([]string, len(resList))
					for i, r := range resList {
						resLabels[i] = resolutionLabel(r)
					}
					curRes := resolutionLabel(size)
					ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileQualityResolution()).Grow(1).TextColor(mc.OnSurface)
						if ui.Select(c.Key("mob-q-res"), &curRes, resLabels).Width(150).Changed() {
							if i := slices.Index(resLabels, curRes); i >= 0 {
								v.setSize(resList[i])
							}
						}
					})

					cNames, cLabels := codecChoices(offer)
					curCodec := codecLabel(codec)
					ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileQualityCodec()).Grow(1).TextColor(mc.OnSurface)
						if ui.Select(c.Key("mob-q-codec"), &curCodec, cLabels).Width(150).Changed() {
							if i := slices.Index(cLabels, curCodec); i >= 0 {
								v.setCodec(cNames[i])
							}
						}
					})

					sndOn := !mute
					ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileSound()).Grow(1).TextColor(mc.OnSurface)
						if m3Switch(c, "mob-q-snd", &sndOn, L.MobileSound()) {
							v.setSound(sndOn)
						}
					})

					ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileShareClipboard()).Grow(1).TextColor(mc.OnSurface)
						if m3Switch(c, "mob-q-clip", &shareClip, L.MobileShareClipboard()) {
							v.setClipShare(shareClip)
						}
					})

					if v.hostHas(featureLockAfter) {
						ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
							ui.Text(c, L.ViewerLockAfter()).Grow(1).TextColor(mc.OnSurface)
							if m3Switch(c, "mob-lock-after", &lockAfter, L.ViewerLockAfter()) {
								v.setLockAfter(lockAfter)
							}
						})
					}
				})

				if m3TonalButton(c, mc, "mob-open-cliphist", L.MobileClipHistory(), false).FillWidth().Clicked() {
					ms.menuSheetOpen = false
					ms.clipSheetOpen = true
				}

				// Host actions.
				ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
					if m3OutlinedButton(c, mc, "mob-act-lock", L.MobileActionLock(), mc.OnSurface, false).Clicked() {
						v.sendControl(map[string]any{"type": "action", "action": "lock"})
						ms.menuSheetOpen = false
					}
					if actions != nil && actions.CtrlAltDel {
						if m3OutlinedButton(c, mc, "mob-act-cad", "Ctrl+Alt+Del", mc.OnSurface, false).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": "ctrl-alt-del"})
							ms.menuSheetOpen = false
						}
					}
					if actions != nil {
						blk := "block"
						if actions.Blocked {
							blk = "unblock"
						}
						if m3OutlinedButton(c, mc, "mob-act-blk", L.MobileActionBlockInput(), mc.OnSurface, false).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": blk})
						}
						prv := "privacy-on"
						if actions.Privacy {
							prv = "privacy-off"
						}
						if m3OutlinedButton(c, mc, "mob-act-prv", L.MobileActionPrivacy(), mc.OnSurface, false).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": prv})
						}
					}
				})

				ui.Box(c).FillWidth().Height(1).Background(mc.OutlineVariant.Alpha(0.35))
				if m3OutlinedButton(c, mc, "mob-v-disc", L.MobileDisconnect(), mc.Error, false).FillWidth().Clicked() {
					v.disconnect()
					ms.menuSheetOpen = false
					ms.activeSession = ""
					a.mobileSyncSessions()
				}
			} else if svc != nil {
				ui.Text(c, svc.title).FontSize(18).FontWeight(700).TextColor(mc.OnSurface)
				if sc, ok := svc.running().(*screenClient); ok && sc != nil {
					if m3TonalButton(c, mc, "mob-svc-typeclip", L.MobileSendClipboard(), false).FillWidth().Clicked() {
						go sc.typeClipboard(svc)
						ms.menuSheetOpen = false
					}
					if m3OutlinedButton(c, mc, "mob-svc-cad", "Ctrl+Alt+Del", mc.OnSurface, false).FillWidth().Clicked() {
						sc.ctrlAltDel()
						ms.menuSheetOpen = false
					}
				}
				if m3OutlinedButton(c, mc, "mob-svc-disc", L.MobileDisconnect(), mc.Error, false).FillWidth().Clicked() {
					svc.disconnect()
					ms.menuSheetOpen = false
					ms.activeSession = ""
					a.mobileSyncSessions()
				}
			}
		})
	})

	// 2. System-IME Type Text sheet.
	mobile.Sheet(c, &ms.typeTextOpen, mobile.SheetOptions{Detents: []float32{0.72, 0.92}, Label: L.MobileTypeText()}, func() {
		ui.Column(c.Key("mob-typetext-body")).FillWidth().Padding(12, 16, 20, 16).Gap(12).Children(func() {
			ui.Text(c, L.MobileTypeText()).FontSize(17).FontWeight(700).TextColor(mc.OnSurface)
			ui.Text(c, L.MobileTypeTextHint()).FontSize(12).TextColor(mc.OnSurfaceVariant)
			in := ui.TextInput(c.Key("mob-typetext-in"), &ms.typeTextValue).FillWidth().AutoFocus()
			sendNow := func(txt string) {
				if txt == "" {
					return
				}
				a.mu.Lock()
				v := a.viewers[ms.activeSession]
				svc := a.services[ms.activeSession]
				a.mu.Unlock()
				if v != nil {
					v.queue(inputEvent{Kind: "text", Text: txt})
				} else if svc != nil {
					if sc, ok := svc.running().(*screenClient); ok && sc != nil && sc.desk != nil {
						_ = sc.desk.text(txt)
					}
				}
			}
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if m3TonalButton(c, mc, "mob-tt-clip", L.MobileSendClipboard(), false).Grow(1).Clicked() {
					sendNow(mygo.Clipboard.ReadText())
					ms.typeTextOpen = false
				}
				if m3FilledButton(c, mc, "mob-tt-send", L.MobileSend(), false).Grow(1).Clicked() || in.Submitted() {
					sendNow(ms.typeTextValue)
					ms.typeTextValue = ""
					ms.typeTextOpen = false
				}
			})
		})
	})

	// 3. Clipboard History sheet (with Save & Share for received files/folders).
	mobile.Sheet(c, &ms.clipSheetOpen, mobile.SheetOptions{Detents: []float32{0.6, 0.92}, Label: L.MobileClipHistory()}, func() {
		var entries []clipEntry
		a.mu.Lock()
		for _, v := range a.viewers {
			v.mu.Lock()
			entries = append(entries, v.clip.history...)
			v.mu.Unlock()
		}
		a.mu.Unlock()
		slices.SortStableFunc(entries, func(a, b clipEntry) int { return b.at.Compare(a.at) })

		ui.Column(c.Key("mob-clip-sheet")).Fill().Padding(8, 16, 20, 16).Gap(10).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.MobileClipHistory()).FontSize(17).FontWeight(700).TextColor(mc.OnSurface).Grow(1)
				if len(entries) > 0 && m3TonalButton(c, mc, "mob-clip-clear", L.MobileClipHistoryClear(), false).Height(38).Clicked() {
					a.mu.Lock()
					for _, v := range a.viewers {
						v.mu.Lock()
						v.clip.history = nil
						v.mu.Unlock()
					}
					a.mu.Unlock()
				}
			})
			if len(entries) == 0 {
				ui.Text(c, L.MobileClipHistoryEmpty()).TextColor(mc.OnSurfaceVariant)
				return
			}
			ui.Scroll(c.Key("mob-clip-list")).Grow(1).FillWidth().Gap(8).Children(func() {
				for i, e := range entries {
					e := e
					id := "mob-ce-" + strconv.Itoa(i)
					m3Card(c, mc, id).Padding(14).Gap(6).Children(func() {
						who := L.MobileClipFromPhone()
						if e.host {
							who = "↓ Host"
						}
						ui.Row(c).FillWidth().Children(func() {
							ui.Text(c, who).FontSize(12).FontWeight(600).TextColor(mc.Primary).Grow(1)
							ui.Text(c, e.at.Local().Format("15:04:05")).FontSize(11).TextColor(mc.OnSurfaceVariant)
						})
						switch {
						case e.thumb != nil:
							w, h := e.thumb.Size()
							ui.Image(c, e.thumb).Size(float32(w), float32(h)).Radius(8)
							if len(e.pngData) > 0 && m3TonalButton(c, mc, id+"-cpimg", L.MobileCopy(), false).Height(38).Clicked() {
								a.mobileSetHostPNG(e.pngData)
								a.setToast(L.MobileCopied(), false)
							}
						case len(e.names) > 0 || e.files > 0:
							ui.Text(c, strings.Join(e.names, ", ")).FontWeight(600).TextColor(mc.OnSurface)
							ui.Text(c, fmt.Sprintf("%s · %s", L.MobileClipFiles(max(e.files, 1)), sizeLabel(e.size))).FontSize(12).TextColor(mc.OnSurfaceVariant)
							files := collectShareFiles(e.paths)
							if len(files) == 0 {
								ui.Text(c, L.MobileClipFilesGone()).FontSize(12).TextColor(mc.OnSurfaceVariant)
							} else {
								ui.Row(c).FillWidth().Gap(8).Children(func() {
									if len(files) == 1 && m3OutlinedButton(c, mc, id+"-save", L.MobileSave(), mc.OnSurface, false).Grow(1).Clicked() {
										f := files[0]
										go func() {
											saved, err := mygo.Dialog.SaveCopy(f, filepath.Base(f))
											if err == nil && saved != "" {
												a.update(func() { a.setToast(L.MobileFilesSaved(), false) })
											}
										}()
									}
									if m3FilledButton(c, mc, id+"-share", L.MobileFilesShare(), false).Grow(1).Clicked() {
										go func() { _ = mygo.Share(mygo.ShareOptions{Files: files}) }()
									}
								})
							}
						default:
							preview := e.text
							if len(preview) > 300 {
								preview = strings.ToValidUTF8(preview[:300], "") + "…"
							}
							ui.Text(c, preview).FontSize(13).TextColor(mc.OnSurface)
							if m3TonalButton(c, mc, id+"-cptxt", L.MobileCopy(), false).Height(38).Clicked() {
								a.copyText(e.text)
								a.setToast(L.MobileCopied(), false)
							}
						}
					})
				}
			})
		})
	})

	// 4. Host AI Task sheet.
	mobile.Sheet(c, &ms.aiSheetOpen, mobile.SheetOptions{Detents: []float32{0.76, 0.94}, Label: L.MobileAiOnHost()}, func() {
		a.mobileAISheetBody(c, mc, ms.aiSheetDevice)
	})

	// 5. Persisted AI Task History sheet.
	mobile.Sheet(c, &ms.aiHistOpen, mobile.SheetOptions{Detents: []float32{0.55, 0.9}, Label: L.MobileAiHistory()}, func() {
		dev := ms.aiHistDevice
		a.mu.Lock()
		list := slices.Clone(a.prefs.MobileAIHistory[dev])
		p := a.prefs
		a.mu.Unlock()

		ui.Column(c.Key("mob-aihist-body")).Fill().Padding(8, 16, 20, 16).Gap(10).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.MobileAiHistory()).FontSize(17).FontWeight(700).TextColor(mc.OnSurface).Grow(1)
				if len(list) > 0 && m3TonalButton(c, mc, "mob-aihist-clr", L.MobileClipHistoryClear(), false).Height(38).Clicked() {
					hist := make(map[string][]MobileAITaskSummary, len(p.MobileAIHistory))
					for k, v := range p.MobileAIHistory {
						if k != dev {
							hist[k] = v
						}
					}
					p.MobileAIHistory = hist
					a.setPrefs(p)
				}
			})
			if len(list) == 0 {
				ui.Text(c, L.MobileAiHistoryEmpty()).TextColor(mc.OnSurfaceVariant)
				return
			}
			ui.Scroll(c.Key("mob-aihist-scroll")).Grow(1).FillWidth().Gap(8).Children(func() {
				for _, item := range list {
					m3Card(c, mc, "mob-aih-"+strconv.FormatUint(item.ID, 10)).Padding(14).Gap(4).Children(func() {
						ui.Text(c, item.Instruction).FontWeight(600).TextColor(mc.OnSurface)
						meta := fmt.Sprintf("#%d · %s · %d steps", item.ID, item.State, item.Steps)
						if item.InputTokens > 0 || item.OutTokens > 0 {
							meta += " · " + L.MobileAiTokens(item.InputTokens, item.OutTokens)
						}
						ui.Text(c, meta).FontSize(12).TextColor(mc.OnSurfaceVariant)
					})
				}
			})
		})
	})
}

func (a *App) mobileAISheetBody(c *ui.Context, mc mobileColors, device string) {
	t := c.Theme()
	a.mu.Lock()
	link := a.aiLinks[device]
	a.mu.Unlock()
	if link == nil {
		ui.Column(c).Fill().Padding(20).Children(func() {
			ui.Text(c, L.MobileConnecting()).TextColor(mc.OnSurfaceVariant)
		})
		return
	}
	snap := link.snapshot()
	if snap.task != nil {
		a.recordMobileAIHistory(device, snap.task)
	}

	ui.Column(c.Key("mob-ai-sheet-"+device)).Fill().Padding(8, 16, 16, 16).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, L.MobileAiOnHost()).FontSize(17).FontWeight(700).TextColor(mc.OnSurface).Grow(1)
			if m3TonalButton(c, mc, "mob-ai-openhist", L.MobileAiHistory(), false).Height(38).Clicked() {
				a.mobile.aiHistDevice = device
				a.mobile.aiHistOpen = true
			}
		})

		if snap.phase.State != "connected" {
			ui.Text(c, L.MobileConnecting()+" ("+snap.phase.State+")").TextColor(mc.OnSurfaceVariant)
			return
		}
		if !snap.run && !snap.approve {
			ui.Text(c, L.MobileAiNotAllowed()).TextColor(mc.Error)
			return
		}
		if snap.err != "" {
			ui.Text(c, snap.err).FontSize(12).TextColor(mc.Error)
		}

		ui.Scroll(c.Key("mob-ai-conv-" + device)).Grow(1).Basis(0).FillWidth().MinHeight(0).Gap(8).Children(func() {
			task := snap.task
			if task == nil {
				ui.Text(c, L.MobileAiNoTask()).TextColor(mc.OnSurfaceVariant)
				return
			}
			m3Card(c, mc, "mob-ai-taskcard").Padding(14).Gap(6).Children(func() {
				ui.Text(c, task.Instruction).FontWeight(700).TextColor(mc.OnSurface)
				ui.Text(c, fmt.Sprintf("State: %s · Steps: %d", task.State, len(task.Steps))).FontSize(12).TextColor(mc.OnSurfaceVariant)
				if task.Tokens.Input > 0 || task.Tokens.Output > 0 {
					ui.Text(c, L.MobileAiTokens(int(task.Tokens.Input), int(task.Tokens.Output))).FontSize(12).TextColor(mc.OnSurfaceVariant)
				}
				if task.Text != "" {
					ui.Text(c, task.Text).FontSize(13).TextColor(mc.OnSurface).Selectable()
				}
			})

			// Safety confirmation banner when waiting for approval.
			if task.Approval != nil && snap.approve {
				ui.Column(c.Key("mob-ai-approve")).FillWidth().Padding(14).Radius(mobileCardRadius).
					Background(mc.Warning.Alpha(0.14)).Border(1, mc.Warning).Gap(8).Children(func() {
					ui.Text(c, L.MobileAiWaitingApproval()).FontWeight(700).TextColor(textTone(t, mc.Warning))
					ui.Text(c, describeAction(task.Approval.Action)).FontSize(13).TextColor(mc.OnSurface)
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						if m3OutlinedButton(c, mc, "mob-ai-dec-no", L.MobileDecline(), mc.Error, false).Grow(1).Clicked() {
							link.send(map[string]any{"type": "ai-decide", "id": task.ID, "approve": false})
						}
						if m3FilledButton(c, mc, "mob-ai-dec-yes", L.MobileApprove(), false).Grow(1).Clicked() {
							link.send(map[string]any{"type": "ai-decide", "id": task.ID, "approve": true})
						}
					})
				})
			}

			// Task controls: Pause / Resume / Stop / Export Markdown.
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				switch task.State {
				case "running":
					if m3TonalButton(c, mc, "mob-ai-pause", L.MobileAiPause(), false).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "pause"})
					}
					if m3OutlinedButton(c, mc, "mob-ai-stop", L.MobileAiStop(), mc.Error, false).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "cancel"})
					}
				case "paused":
					if m3FilledButton(c, mc, "mob-ai-resume", L.MobileAiResume(), false).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "resume"})
					}
					if m3OutlinedButton(c, mc, "mob-ai-stop2", L.MobileAiStop(), mc.Error, false).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "cancel"})
					}
				}
				if a.hostHas(device, featureAISessions) {
					if m3OutlinedButton(c, mc, "mob-ai-export", L.AIExport(), mc.OnSurface, false).Height(42).Clicked() {
						outPath := filepath.Join(os.TempDir(), fmt.Sprintf("kyokai-task-%d.md", task.ID))
						link.exportTo(task.ID, outPath)
					}
				}
			})

			for i, step := range task.Steps {
				ui.Row(c.Key("mob-aistep-"+strconv.Itoa(i))).FillWidth().Padding(8, 12).Radius(12).
					Background(mc.SurfaceContainerLow).Children(func() {
					ui.Text(c, describeAction(step.Action)).FontSize(12).TextColor(mc.OnSurfaceVariant)
				})
			}
		})

		if snap.run {
			draft := snap.draft
			in := ui.TextInput(c.Key("mob-ai-input-"+device), &draft).Placeholder(L.MobileAiTaskHint()).FillWidth()
			if in.Changed() {
				link.setDraft(draft)
			}
			activeTask := snap.task != nil && (snap.task.State == "running" || snap.task.State == "paused" || snap.task.State == "approval")
			if m3FilledButton(c, mc, "mob-ai-send-"+device, L.MobileAiSend(), strings.TrimSpace(draft) == "").FillWidth().Clicked() || (in.Submitted() && strings.TrimSpace(draft) != "") {
				txt := strings.TrimSpace(draft)
				link.setDraft("")
				if activeTask || (snap.task != nil && snap.task.State == "done") {
					link.send(map[string]any{"type": "ai-reply", "id": snap.task.ID, "text": txt})
				} else {
					key := strconv.FormatInt(time.Now().UnixNano(), 36)
					link.send(map[string]any{"type": "ai-start", "key": key, "instruction": txt})
				}
			}
		}
	})
}
