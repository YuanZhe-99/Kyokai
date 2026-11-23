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

func (a *App) mobileActiveSessionView(c *ui.Context, active string, winW float32) {
	ms := a.mobile
	if ts := ms.terms[active]; ts != nil {
		a.mobileSSHView(c, active, ts)
		return
	}
	a.mu.Lock()
	v := a.viewers[active]
	svc := a.services[active]
	a.mu.Unlock()

	switch {
	case v != nil:
		v.setFront(ms.inFront)
		a.mobileKyokaiViewer(c, v, winW)
	case svc != nil:
		svc.setFront(ms.inFront)
		a.mobileDesktopServiceViewer(c, svc, winW)
	default:
		ms.activeSession = ""
	}
}

func (a *App) mobileKyokaiViewer(c *ui.Context, v *viewer, winW float32) {
	t := c.Theme()
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
		ui.Column(c.Key("mob-vconn-" + v.tabKey())).Fill().Background(t.Background).Children(func() {
			mobileHeaderBar(c, mobile.TopBarOptions{
				Title: v.title,
				Leading: func() {
					b := ui.ButtonBase(c.Key("mob-vc-back")).Size(48, 48).Center().Label("Back")
					b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
					if b.Clicked() {
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
				Trailing: func() {
					b := ui.ButtonBase(c.Key("mob-vc-disc")).Size(48, 48).Center().Label(L.MobileDisconnect())
					b.Children(func() { ui.Icon(c, paneClose).FontSize(18).TextColor(t.TextMuted) })
					if b.Clicked() {
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
	topSafe, rightSafe, _, leftSafe := c.SafeArea()
	laptopActive := p.LaptopMode && ms.posture.HalfOpened
	useSafe := p.LaptopSafeArea == nil || *p.LaptopSafeArea

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

	rootCol := ui.Column(c.Key("mob-viewer-" + v.tabKey())).Fill().Background(ui.RGB(0, 0, 0))
	if laptopActive && !useSafe {
		rootCol.Margin(-topSafe, -rightSafe, 0, -leftSafe)
	}
	rootCol.Children(func() {
		// Screen stage (above hinge in laptop mode, else grows to fill above keyboard).
		stage := ui.Box(c.Key("mob-vstage-" + v.tabKey())).FillWidth().Clip().Background(ui.RGB(0, 0, 0)).
			Role(ui.RoleImage).Label(L.MobileA11yRemoteScreen(v.title)).Focusable()
		stage.Draw(func(pt *ui.Painter, _ ui.Rect) { u.dipScale = pt.Scale() })
		if laptopActive {
			scale := max(u.dipScale, 1)
			if ms.posture.HingeTop > 0 {
				insetTop := topSafe
				if !useSafe {
					insetTop = 0
				}
				h := float32(ms.posture.HingeTop)/scale - insetTop
				if h > 120 {
					stage.Height(h)
				} else {
					stage.Grow(1).Basis(0)
				}
			} else {
				stage.Height(winH * 0.48)
			}
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
							Background(ui.RGBA(255, 255, 255, 0.85)).Border(2, t.Accent)
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
					if ui.PrimaryButton(c.Key("mob-vreconn"), L.MobileTryAgain()).Height(46).Clicked() {
						v.redial("", false)
					}
				})
			case bm == nil || start.Placeholder != "":
				waitTxt, _ := viewerWaitText(host.Video, start.Placeholder)
				ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Padding(24).Gap(12).Children(func() {
					if start.Placeholder == "asleep" || asleep {
						ui.Text(c, L.MobileHostAsleep()).FontSize(16).Bold().TextColor(ui.RGB(255, 255, 255))
						if ui.PrimaryButton(c.Key("mob-vwake-now"), L.MobileActionWake()).Height(48).Clicked() {
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
				ui.Text(c, info).Absolute().Left(8).Bottom(8).Padding(4, 8).Radius(6).
					Background(ui.RGBA(0, 0, 0, 0.72)).TextColor(ui.RGB(235, 240, 245)).FontSize(11).FontFeatures("tnum")
			}

			// Reset zoom pill (> 1.05x).
			if u.zoom.Scale > 1.05 {
				zb := ui.ButtonBase(c.Key("mob-vzoom-reset")).Absolute().Right(12).Bottom(12).MinWidth(48).Height(44).PaddingX(12).
					Radius(22).Background(ui.RGBA(20, 24, 28, 0.85)).Border(1, ui.RGBA(255, 255, 255, 0.22)).Center().
					Label(L.MobileA11yResetZoom())
				zoomRect = zb.LayoutBounds()
				zb.Children(func() {
					ui.Text(c, fmt.Sprintf("%.1f×", u.zoom.Scale)).FontSize(12).Bold().TextColor(ui.RGB(255, 255, 255))
				})
				if zb.Clicked() {
					u.zoom.Reset()
				}
			}

			// Floating collapsible toolbar.
			barRect = a.mobileFloatingToolbar(c, v.tabKey(), u, bounds.W, bounds.H, func() {
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

		// On-screen keyboard (always shown below hinge in laptop mode, or toggled via toolbar).
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
				Split:       p.SplitKeyboard || laptopActive,
				NativeTouch: ms.host != nil,
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
		if (u.kbOpen || laptopActive) && (p.SplitKeyboard || laptopActive) && winW >= 600 && padRect.W <= 0 {
			// First frame after opening the split keyboard: LayoutBounds() reflects
			// the previous frame, so request one more frame to latch padRect cleanly.
			a.later(func() {})
		}
		a.mobileUpdateTouchCapture(canCapture, stageBounds, padRect, barRect, zoomRect, func(rt androidhost.RawTouch) {
			now := time.Now()
			if rt.Zone == 2 {
				if u.kb.pad != nil {
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

func (a *App) mobileFloatingToolbar(c *ui.Context, key string, u *mobileViewerUI, areaW, areaH float32, onKB, onAI, onMenu func()) ui.Rect {
	ms := a.mobile
	t := c.Theme()

	if u.barFolded {
		hx := min(max(u.dragHandle.X, 0), 1) * max(areaW-44, 0)
		hy := max(6, min(max(u.dragHandle.Y, 0), 1)*max(areaH-44, 0))
		off := ui.Point{X: hx, Y: hy}
		hb := ui.ButtonBase(c.Key("mob-bar-handle-"+key)).Absolute().Left(0).Top(0).Size(44, 44).Radius(22).
			Background(ui.RGBA(18, 22, 26, 0.84)).Border(1, ui.RGBA(255, 255, 255, 0.22)).Center().
			Label(L.MobileA11yShowToolbar())
		hb.Draggable(&off, ui.DraggableOptions{
			Bounds: &ui.Rect{W: max(areaW-44, 1), H: max(areaH-44, 1)},
		})
		rect := hb.LayoutBounds()
		hb.Children(func() { ui.Icon(c, barUnfold).FontSize(18).TextColor(ui.RGB(255, 255, 255)) })
		if !hb.Dragging() && areaW > 44 && areaH > 44 {
			nx := min(max(off.X/max(areaW-44, 1), 0), 1)
			ny := min(max(off.Y/max(areaH-44, 1), 0), 1)
			// Snap to nearest edge (left, right, top, bottom).
			dLeft, dRight, dTop, dBot := nx, 1-nx, ny, 1-ny
			minD := min(dLeft, dRight, dTop, dBot)
			switch minD {
			case dTop:
				ny = 0
			case dBot:
				ny = 1
			case dLeft:
				nx = 0
			case dRight:
				nx = 1
			}
			if nx != u.dragHandle.X || ny != u.dragHandle.Y {
				u.dragHandle = ui.Point{X: nx, Y: ny}
				a.mu.Lock()
				pr := a.prefs
				a.mu.Unlock()
				pair := [2]float32{nx, ny}
				pr.ToolbarHandle = &pair
				a.setPrefs(pr)
			}
		}
		if hb.Clicked() {
			u.barFolded = false
		}
		return rect
	}

	slots := 4
	if onAI != nil {
		slots = 5
	}
	wantW := float32(slots)*50 + 16
	barW := wantW
	barLeft := float32(12)
	if areaW > 0 {
		barW = min(wantW, max(areaW-24, 200))
		barLeft = max((areaW-barW)/2, 12)
	}
	bar := ui.Row(c.Key("mob-bar-"+key)).Absolute().Top(10).Left(barLeft).Width(barW).Height(46).
		PaddingX(6).Radius(23).Background(ui.RGBA(18, 22, 26, 0.86)).Border(1, ui.RGBA(255, 255, 255, 0.18)).
		Shadow(0, 4, 14, 0, ui.RGBA(0, 0, 0, 0.35)).AlignItems(ui.Center).Justify(ui.SpaceBetween)
	rect := bar.LayoutBounds()
	btn := func(id, label string, icon *ui.SVG, active bool, tap func()) {
		fg := ui.RGB(240, 244, 248)
		b := ui.ButtonBase(c.Key("mob-tb-"+id+"-"+key)).Size(44, 38).Radius(19).Center().Label(label)
		if active {
			fg = t.Accent
			b.Background(t.Accent.Alpha(0.20))
		}
		b.Children(func() {
			ui.Icon(c, icon).FontSize(19).TextColor(fg)
		})
		if b.Clicked() {
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
		btn("fold", L.MobileA11yHideToolbar(), barFold, false, func() { u.barFolded = true })
		btn("more", L.MobileMore(), rowMore, false, onMenu)
	})
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

func (a *App) mobileDesktopServiceViewer(c *ui.Context, s *serviceTab, winW float32) {
	t := c.Theme()
	ms := a.mobile
	s.mu.Lock()
	phase, client := s.phase, s.client
	s.mu.Unlock()

	sc, ok := client.(*screenClient)
	if phase != svcRunning || !ok || sc == nil {
		a.mobileUpdateTouchCapture(false, ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}, nil)
		ui.Column(c.Key("mob-sconn-" + s.tabKey())).Fill().Background(t.Background).Children(func() {
			mobileHeaderBar(c, mobile.TopBarOptions{
				Title: s.title,
				Leading: func() {
					b := ui.ButtonBase(c.Key("mob-sc-back")).Size(48, 48).Center().Label("Back")
					b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
					if b.Clicked() {
						ms.activeSession = ""
						a.mobileSyncSessions()
					}
				},
				Trailing: func() {
					b := ui.ButtonBase(c.Key("mob-sc-disc")).Size(48, 48).Center().Label(L.MobileDisconnect())
					b.Children(func() { ui.Icon(c, paneClose).FontSize(18).TextColor(t.TextMuted) })
					if b.Clicked() {
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
	var dBounds, dBarRect ui.Rect

	ui.Column(c.Key("mob-dsvc-" + s.tabKey())).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
		stage := ui.Box(c.Key("mob-dstage-" + s.tabKey())).Grow(1).Basis(0).FillWidth().MinHeight(0).Clip().
			Role(ui.RoleImage).Label(L.MobileA11yRemoteScreen(s.name)).Focusable()
		stage.Draw(func(pt *ui.Painter, _ ui.Rect) { u.dipScale = pt.Scale() })
		bounds := stage.LayoutBounds()
		dBounds = bounds
		sink.viewW, sink.viewH = bounds.W, bounds.H
		ox, oy, pw, ph := pictureBox(bounds, fbW, fbH)
		if p.PictureTop {
			oy = 0
		}
		dOx, dOy, dPw, dPh = ox, oy, pw, ph

		// Dynamic RDP resolution matching on rotation / foldable resize.
		if s.service == serviceRDP && bounds.W > 200 && bounds.H > 200 && sc.desk != nil {
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
						Size(csz, csz).Radius(csz/2).Background(ui.RGBA(255, 255, 255, 0.85)).Border(2, t.Accent)
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

			dBarRect = a.mobileFloatingToolbar(c, s.tabKey(), u, bounds.W, bounds.H, func() {
				u.kbOpen = !u.kbOpen
			}, nil, func() {
				ms.menuSheetOpen = true
			})
		})

		if u.kbOpen {
			lbl := "Remote Desktop"
			if s.service == serviceVNC {
				lbl = "VNC"
			}
			u.kb.View(c, "mob-dkb-"+s.tabKey(), winW, MobileKeyboardOptions{
				TargetLabel: lbl,
				HostOS:      effOS,
				OSPicked:    osOverride != "",
				AutoLabel:   L.MobileAuto(),
				Split:       p.SplitKeyboard,
				NativeTouch: ms.host != nil,
				OnOSPick:    func(ch string) { a.setMobileKeyboardOS(kbKey, ch) },
				OnSplit: func(sp bool) {
					a.mu.Lock()
					pr := a.prefs
					a.mu.Unlock()
					pr.SplitKeyboard = sp
					a.setPrefs(pr)
				},
				OnHide: func() { u.kbOpen = false },
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
		if u.kbOpen {
			padRect = u.kb.padRect
		}
		if u.kbOpen && p.SplitKeyboard && winW >= 600 && padRect.W <= 0 {
			a.later(func() {})
		}
		a.mobileUpdateTouchCapture(canCapture, dBounds, padRect, dBarRect, ui.Rect{}, func(rt androidhost.RawTouch) {
			now := time.Now()
			if rt.Zone == 2 {
				if u.kb.pad != nil {
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
	ts := ms.terms[key]
	delete(ms.terms, key)
	if ms.activeSession == key {
		ms.activeSession = ""
	}
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

func (a *App) mobileSSHView(c *ui.Context, key string, ts *mobileTermSession) {
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

	ui.Column(c.Key("mob-ssh-" + key)).Fill().Background(t.Background).Children(func() {
		mobileHeaderBar(c, mobile.TopBarOptions{
			Title: ts.name + " · SSH",
			Leading: func() {
				b := ui.ButtonBase(c.Key("mob-ssh-back-"+key)).Size(48, 48).Center().Label("Back")
				b.Children(func() { ui.Icon(c, mobBackIcon).FontSize(20).TextColor(t.Text) })
				if b.Clicked() {
					ms.activeSession = ""
					a.mobileSyncSessions()
				}
			},
			Trailing: func() {
				if state == "connected" {
					fb := ui.ButtonBase(c.Key("mob-ssh-files-"+key)).Size(48, 48).Center().Label(L.MobileFilesTitle())
					col := t.TextMuted
					if filesOn {
						col = t.Accent
					}
					fb.Children(func() { ui.Icon(c, sshFiles).FontSize(20).TextColor(col) })
					if fb.Clicked() {
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
					pb := ui.ButtonBase(c.Key("mob-ssh-paste-"+key)).Size(48, 48).Center().Label(L.MobilePaste())
					pb.Children(func() { ui.Icon(c, barClipboard).FontSize(20).TextColor(t.TextMuted) })
					if pb.Clicked() {
						if clip := mygo.Clipboard.ReadText(); clip != "" {
							ts.sendText(clip)
						}
					}
				}
				xb := ui.ButtonBase(c.Key("mob-ssh-disc-"+key)).Size(48, 48).Center().Label(L.MobileDisconnect())
				xb.Children(func() { ui.Icon(c, paneClose).FontSize(18).TextColor(t.TextMuted) })
				if xb.Clicked() {
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
				ui.Column(c).FillWidth().MaxWidth(420).Padding(18).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(10).Children(func() {
					ui.Text(c, L.MobileHostKeyChanged()).FontSize(17).Bold().TextColor(textTone(t, t.Warning))
					ui.Text(c, L.MobileHostKeyChangedHint()).FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, changed).FontSize(11).Font("monospace").Selectable()
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						if ui.Button(c.Key("mob-hk-cancel"), L.MobileCancel()).Grow(1).Height(46).Clicked() {
							a.mobileCloseSSH(key)
						}
						if ui.PrimaryButton(c.Key("mob-hk-trust"), L.MobileTrustKey()).Grow(1).Height(46).Clicked() {
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
				ui.Column(c).FillWidth().MaxWidth(420).Padding(18).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(12).Children(func() {
					if errNote != "" {
						ui.Text(c, errNote).FontSize(13).TextColor(textTone(t, t.Danger))
					}
					mIdx := 0
					if form.method == methodPassword {
						mIdx = 1
					}
					if ui.Segmented(c.Key("mob-ssh-m"), &mIdx, L.MobileSignInKey(), L.MobilePassword()).FillEqually().Changed() {
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
						ui.Text(c, L.MobileSshKeyHint()).FontSize(12).TextColor(t.TextMuted)
					}
					ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.MobileRememberSignIn()).Grow(1)
						ui.Switch(c.Key("mob-ssh-rem"), &form.remember).Label(L.MobileRememberSignIn())
					})
					ts.mu.Lock()
					ts.form = form
					ts.mu.Unlock()
					if ui.PrimaryButton(c.Key("mob-ssh-go"), L.MobileConnect()).FillWidth().Height(48).Disabled(strings.TrimSpace(form.user) == "").Clicked() {
						plan := form
						a.mobileDialSSH(ts, &plan)
					}
				})
			})

		case "ended":
			emptyState(c, "mob-ssh-end", func() { ui.Icon(c, kindSSH).FontSize(36).TextColor(t.Danger) }, errNote, "", func() {
				if ui.PrimaryButton(c.Key("mob-ssh-retry"), L.MobileTryAgain()).Height(46).Clicked() {
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

func (a *App) mobileSheets(c *ui.Context, wide bool) {
	ms := a.mobile
	t := c.Theme()

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

				ui.Text(c, v.title).FontSize(17).Bold()

				if len(displays.List) > 1 {
					ui.Text(c, L.MobileDisplays()).FontSize(13).Bold().TextColor(t.TextMuted)
					ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
						for i := range displays.List {
							idx := i
							lbl := L.MobileDisplayN(i + 1)
							if i == displays.Current {
								lbl += " ✓"
							}
							if ui.Button(c.Key("mob-disp-"+strconv.Itoa(i)), lbl).Height(42).Clicked() {
								v.sendControl(map[string]any{"type": "display", "index": idx})
							}
						}
					})
				}

				ui.Text(c, L.MobileQuality()).FontSize(13).Bold().TextColor(t.TextMuted)
				pIdx := min(max(preset, 0), 2)
				if ui.Segmented(c.Key("mob-q-preset"), &pIdx, L.MobileQualitySpeed(), L.MobileQualityBalanced(), L.MobileQualityBest()).FillEqually().Changed() {
					v.setQuality(pIdx, fpsCap, false)
				}

				resList := []string{"auto", "native", "2160", "1440", "1080", "720"}
				resLabels := make([]string, len(resList))
				for i, r := range resList {
					resLabels[i] = resolutionLabel(r)
				}
				curRes := resolutionLabel(size)
				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileQualityResolution()).Grow(1)
					if ui.Select(c.Key("mob-q-res"), &curRes, resLabels).Width(150).Changed() {
						if i := slices.Index(resLabels, curRes); i >= 0 {
							v.setSize(resList[i])
						}
					}
				})

				cNames, cLabels := codecChoices(offer)
				curCodec := codecLabel(codec)
				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileQualityCodec()).Grow(1)
					if ui.Select(c.Key("mob-q-codec"), &curCodec, cLabels).Width(150).Changed() {
						if i := slices.Index(cLabels, curCodec); i >= 0 {
							v.setCodec(cNames[i])
						}
					}
				})

				sndOn := !mute
				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileSound()).Grow(1)
					if ui.Switch(c.Key("mob-q-snd"), &sndOn).Label(L.MobileSound()).Changed() {
						v.setSound(sndOn)
					}
				})

				ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
					ui.Text(c, L.MobileShareClipboard()).Grow(1)
					if ui.Switch(c.Key("mob-q-clip"), &shareClip).Label(L.MobileShareClipboard()).Changed() {
						v.setClipShare(shareClip)
					}
				})

				if ui.Button(c.Key("mob-open-cliphist"), L.MobileClipHistory()).FillWidth().Height(46).Clicked() {
					ms.menuSheetOpen = false
					ms.clipSheetOpen = true
				}

				// Host actions.
				ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
					if ui.Button(c.Key("mob-act-lock"), L.MobileActionLock()).Height(44).Clicked() {
						v.sendControl(map[string]any{"type": "action", "action": "lock"})
						ms.menuSheetOpen = false
					}
					if actions != nil && actions.CtrlAltDel {
						if ui.Button(c.Key("mob-act-cad"), "Ctrl+Alt+Del").Height(44).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": "ctrl-alt-del"})
							ms.menuSheetOpen = false
						}
					}
					if actions != nil {
						blk := "block"
						if actions.Blocked {
							blk = "unblock"
						}
						if ui.Button(c.Key("mob-act-blk"), L.MobileActionBlockInput()).Height(44).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": blk})
						}
						prv := "privacy-on"
						if actions.Privacy {
							prv = "privacy-off"
						}
						if ui.Button(c.Key("mob-act-prv"), L.MobileActionPrivacy()).Height(44).Clicked() {
							v.sendControl(map[string]any{"type": "action", "action": prv})
						}
					}
				})

				if v.hostHas(featureLockAfter) {
					ui.Row(c).FillWidth().MinHeight(44).AlignItems(ui.Center).Children(func() {
						ui.Text(c, L.ViewerLockAfter()).Grow(1)
						if ui.Switch(c.Key("mob-lock-after"), &lockAfter).Label(L.ViewerLockAfter()).Changed() {
							v.setLockAfter(lockAfter)
						}
					})
				}

				ui.Divider(c)
				if ui.Button(c.Key("mob-v-disc"), L.MobileDisconnect()).FillWidth().Height(46).Clicked() {
					v.disconnect()
					ms.menuSheetOpen = false
					ms.activeSession = ""
					a.mobileSyncSessions()
				}
			} else if svc != nil {
				ui.Text(c, svc.title).FontSize(17).Bold()
				if sc, ok := svc.running().(*screenClient); ok && sc != nil {
					if ui.Button(c.Key("mob-svc-typeclip"), L.MobileSendClipboard()).FillWidth().Height(46).Clicked() {
						go sc.typeClipboard(svc)
						ms.menuSheetOpen = false
					}
					if ui.Button(c.Key("mob-svc-cad"), "Ctrl+Alt+Del").FillWidth().Height(46).Clicked() {
						sc.ctrlAltDel()
						ms.menuSheetOpen = false
					}
				}
				if ui.Button(c.Key("mob-svc-disc"), L.MobileDisconnect()).FillWidth().Height(46).Clicked() {
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
		ui.Column(c.Key("mob-typetext-body")).FillWidth().Padding(12, 16, 20, 16).Gap(10).Children(func() {
			ui.Text(c, L.MobileTypeText()).FontSize(16).Bold()
			ui.Text(c, L.MobileTypeTextHint()).FontSize(12).TextColor(t.TextMuted)
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
				if ui.Button(c.Key("mob-tt-clip"), L.MobileSendClipboard()).Grow(1).Height(46).Clicked() {
					sendNow(mygo.Clipboard.ReadText())
					ms.typeTextOpen = false
				}
				if ui.PrimaryButton(c.Key("mob-tt-send"), L.MobileSend()).Grow(1).Height(46).Clicked() || in.Submitted() {
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
				ui.Text(c, L.MobileClipHistory()).FontSize(17).Bold().Grow(1)
				if len(entries) > 0 && ui.Button(c.Key("mob-clip-clear"), L.MobileClipHistoryClear()).Clicked() {
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
				ui.Text(c, L.MobileClipHistoryEmpty()).TextColor(t.TextMuted)
				return
			}
			ui.Scroll(c.Key("mob-clip-list")).Grow(1).FillWidth().Gap(8).Children(func() {
				for i, e := range entries {
					e := e
					id := "mob-ce-" + strconv.Itoa(i)
					ui.Column(c.Key(id)).FillWidth().Padding(12).Radius(12).Background(t.Surface).Border(1, t.Border).Gap(6).Children(func() {
						who := L.MobileClipFromPhone()
						if e.host {
							who = "↓ Host"
						}
						ui.Row(c).FillWidth().Children(func() {
							ui.Text(c, who).FontSize(12).Bold().Grow(1)
							ui.Text(c, e.at.Local().Format("15:04:05")).FontSize(11).TextColor(t.TextMuted)
						})
						switch {
						case e.thumb != nil:
							w, h := e.thumb.Size()
							ui.Image(c, e.thumb).Size(float32(w), float32(h)).Radius(6)
							if len(e.pngData) > 0 && ui.Button(c.Key(id+"-cpimg"), L.MobileCopy()).Clicked() {
								a.mobileSetHostPNG(e.pngData)
								a.setToast(L.MobileCopied(), false)
							}
						case len(e.names) > 0 || e.files > 0:
							ui.Text(c, strings.Join(e.names, ", ")).FontWeight(500)
							ui.Text(c, fmt.Sprintf("%s · %s", L.MobileClipFiles(max(e.files, 1)), sizeLabel(e.size))).FontSize(12).TextColor(t.TextMuted)
							files := collectShareFiles(e.paths)
							if len(files) == 0 {
								ui.Text(c, L.MobileClipFilesGone()).FontSize(12).TextColor(t.TextMuted)
							} else {
								ui.Row(c).FillWidth().Gap(8).Children(func() {
									if len(files) == 1 && ui.Button(c.Key(id+"-save"), L.MobileSave()).Grow(1).Height(42).Clicked() {
										f := files[0]
										go func() {
											saved, err := mygo.Dialog.SaveCopy(f, filepath.Base(f))
											if err == nil && saved != "" {
												a.update(func() { a.setToast(L.MobileFilesSaved(), false) })
											}
										}()
									}
									if ui.PrimaryButton(c.Key(id+"-share"), L.MobileFilesShare()).Grow(1).Height(42).Clicked() {
										go func() { _ = mygo.Share(mygo.ShareOptions{Files: files}) }()
									}
								})
							}
						default:
							preview := e.text
							if len(preview) > 300 {
								preview = strings.ToValidUTF8(preview[:300], "") + "…"
							}
							ui.Text(c, preview).FontSize(13)
							if ui.Button(c.Key(id+"-cptxt"), L.MobileCopy()).Clicked() {
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
		a.mobileAISheetBody(c, ms.aiSheetDevice)
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
				ui.Text(c, L.MobileAiHistory()).FontSize(17).Bold().Grow(1)
				if len(list) > 0 && ui.Button(c.Key("mob-aihist-clr"), L.MobileClipHistoryClear()).Clicked() {
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
				ui.Text(c, L.MobileAiHistoryEmpty()).TextColor(t.TextMuted)
				return
			}
			ui.Scroll(c.Key("mob-aihist-scroll")).Grow(1).FillWidth().Gap(8).Children(func() {
				for _, item := range list {
					ui.Column(c.Key("mob-aih-"+strconv.FormatUint(item.ID, 10))).FillWidth().Padding(12).Radius(12).
						Background(t.Surface).Border(1, t.Border).Gap(4).Children(func() {
						ui.Text(c, item.Instruction).FontWeight(600)
						meta := fmt.Sprintf("#%d · %s · %d steps", item.ID, item.State, item.Steps)
						if item.InputTokens > 0 || item.OutTokens > 0 {
							meta += " · " + L.MobileAiTokens(item.InputTokens, item.OutTokens)
						}
						ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
					})
				}
			})
		})
	})
}

func (a *App) mobileAISheetBody(c *ui.Context, device string) {
	t := c.Theme()
	a.mu.Lock()
	link := a.aiLinks[device]
	a.mu.Unlock()
	if link == nil {
		ui.Column(c).Fill().Padding(20).Children(func() {
			ui.Text(c, L.MobileConnecting()).TextColor(t.TextMuted)
		})
		return
	}
	snap := link.snapshot()
	if snap.task != nil {
		a.recordMobileAIHistory(device, snap.task)
	}

	ui.Column(c.Key("mob-ai-sheet-"+device)).Fill().Padding(8, 16, 16, 16).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, L.MobileAiOnHost()).FontSize(17).Bold().Grow(1)
			hb := ui.ButtonBase(c.Key("mob-ai-openhist")).Height(38).PaddingX(10).Radius(8).Background(t.Surface).Border(1, t.Border).Label(L.MobileAiHistory())
			hb.Children(func() { ui.Text(c, L.MobileAiHistory()).FontSize(12) })
			if hb.Clicked() {
				a.mobile.aiHistDevice = device
				a.mobile.aiHistOpen = true
			}
		})

		if snap.phase.State != "connected" {
			ui.Text(c, L.MobileConnecting()+" ("+snap.phase.State+")").TextColor(t.TextMuted)
			return
		}
		if !snap.run && !snap.approve {
			ui.Text(c, L.MobileAiNotAllowed()).TextColor(textTone(t, t.Danger))
			return
		}
		if snap.err != "" {
			ui.Text(c, snap.err).FontSize(12).TextColor(textTone(t, t.Danger))
		}

		ui.Scroll(c.Key("mob-ai-conv-" + device)).Grow(1).Basis(0).FillWidth().MinHeight(0).Gap(8).Children(func() {
			task := snap.task
			if task == nil {
				ui.Text(c, L.MobileAiNoTask()).TextColor(t.TextMuted)
				return
			}
			ui.Column(c).FillWidth().Padding(12).Radius(12).Background(t.Surface).Border(1, t.Border).Gap(6).Children(func() {
				ui.Text(c, task.Instruction).Bold()
				ui.Text(c, fmt.Sprintf("State: %s · Steps: %d", task.State, len(task.Steps))).FontSize(12).TextColor(t.TextMuted)
				if task.Tokens.Input > 0 || task.Tokens.Output > 0 {
					ui.Text(c, L.MobileAiTokens(int(task.Tokens.Input), int(task.Tokens.Output))).FontSize(12).TextColor(t.TextMuted)
				}
				if task.Text != "" {
					ui.Text(c, task.Text).FontSize(13).Selectable()
				}
			})

			// Safety confirmation banner when waiting for approval.
			if task.Approval != nil && snap.approve {
				ui.Column(c.Key("mob-ai-approve")).FillWidth().Padding(12).Radius(12).Background(t.Warning.Alpha(0.14)).Border(1, t.Warning).Gap(8).Children(func() {
					ui.Text(c, L.MobileAiWaitingApproval()).Bold().TextColor(textTone(t, t.Warning))
					ui.Text(c, describeAction(task.Approval.Action)).FontSize(13)
					ui.Row(c).FillWidth().Gap(8).Children(func() {
						if ui.Button(c.Key("mob-ai-dec-no"), L.MobileDecline()).Grow(1).Height(44).Clicked() {
							link.send(map[string]any{"type": "ai-decide", "id": task.ID, "approve": false})
						}
						if ui.PrimaryButton(c.Key("mob-ai-dec-yes"), L.MobileApprove()).Grow(1).Height(44).Clicked() {
							link.send(map[string]any{"type": "ai-decide", "id": task.ID, "approve": true})
						}
					})
				})
			}

			// Task controls: Pause / Resume / Stop / Export Markdown.
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				switch task.State {
				case "running":
					if ui.Button(c.Key("mob-ai-pause"), L.MobileAiPause()).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "pause"})
					}
					if ui.Button(c.Key("mob-ai-stop"), L.MobileAiStop()).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "cancel"})
					}
				case "paused":
					if ui.PrimaryButton(c.Key("mob-ai-resume"), L.MobileAiResume()).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "resume"})
					}
					if ui.Button(c.Key("mob-ai-stop2"), L.MobileAiStop()).Height(42).Clicked() {
						link.send(map[string]any{"type": "ai-command", "id": task.ID, "command": "cancel"})
					}
				}
				if a.hostHas(device, featureAISessions) {
					if ui.Button(c.Key("mob-ai-export"), L.AIExport()).Height(42).Clicked() {
						outPath := filepath.Join(os.TempDir(), fmt.Sprintf("kyokai-task-%d.md", task.ID))
						link.exportTo(task.ID, outPath)
					}
				}
			})

			for i, step := range task.Steps {
				ui.Row(c.Key("mob-aistep-"+strconv.Itoa(i))).FillWidth().Padding(6, 10).Radius(8).Background(t.Surface).Children(func() {
					ui.Text(c, describeAction(step.Action)).FontSize(12).TextColor(t.TextMuted)
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
			if ui.PrimaryButton(c.Key("mob-ai-send-"+device), L.MobileAiSend()).FillWidth().Height(46).Disabled(strings.TrimSpace(draft) == "").Clicked() || (in.Submitted() && strings.TrimSpace(draft) != "") {
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
