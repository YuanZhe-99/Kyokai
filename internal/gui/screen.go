package gui

import (
	"image"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/core"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// remoteDesk is a Remote Desktop or VNC connection as its tab drives it
// (desktops.go). Calls come from the main thread and the key
// reader; each sends at once.
type remoteDesk interface {
	pointer(x, y int, buttons uint8) error
	scroll(x, y int, dx, dy float64) error
	// key presses or releases a key: by scan code (Remote Desktop) or
	// keysym (VNC).
	key(k remoteKey, down bool) error
	text(s string) error
	clipboard(text string) error
	close()
}

// screenClient is a running Remote Desktop or VNC tab: the other desktop's
// picture, fitted or at its original size as the viewer shows a host's, the
// pointer, the wheel and the keyboard on it, the text clipboard while the tab
// is in front, and a toolbar of Display, Keyboard, Ctrl+Alt+Del and Full
// screen. Fields after mu are guarded by it.
type screenClient struct {
	s    *serviceTab
	desk remoteDesk
	// pending: a redraw is asked for and not yet run (one at a time).
	pending atomic.Bool
	// pic is the picture, which takes the keyboard again after a choice in
	// the toolbar (main thread).
	pic ui.Handle

	mu sync.Mutex
	// fb is the protocol's framebuffer, decoded into on its goroutine;
	// work its copy in RGBA, updated there; shown what the bitmap shows,
	// copied from work on the main thread.
	fb          *bgra.Image
	work, shown *image.RGBA
	bm          *ui.Bitmap
	box         ui.Rect
	buttons     uint8
	original    bool
	front       bool
	// held are the keys sent down in Legacy mode, so each release goes
	// exactly when its press did.
	held map[ui.Key]remoteKey
	// clip is the text last exchanged with the other desktop's clipboard.
	clip string
}

// newScreen is a picture client for a service tab.
func newScreen(s *serviceTab) *screenClient {
	return &screenClient{s: s, held: map[ui.Key]remoteKey{}}
}

// resize makes the framebuffer the protocol decodes into, and the
// pictures, for a desktop of w by h (the protocol's goroutine).
func (sc *screenClient) resize(w, h int) *bgra.Image {
	fb := bgra.New(w, h)
	work, shown := image.NewRGBA(image.Rect(0, 0, w, h)), image.NewRGBA(image.Rect(0, 0, w, h))
	sc.mu.Lock()
	sc.fb, sc.work, sc.shown, sc.bm = fb, work, shown, nil
	sc.mu.Unlock()
	return fb
}

// updated copies a changed rectangle of the framebuffer into the picture
// and asks for a redraw (the protocol's goroutine, which alone writes fb).
func (sc *screenClient) updated(r image.Rectangle) {
	sc.mu.Lock()
	fb, work := sc.fb, sc.work
	if fb != nil && work != nil {
		r = r.Intersect(image.Rect(0, 0, fb.Width, fb.Height))
		for y := r.Min.Y; y < r.Max.Y; y++ {
			src := fb.Pix[y*fb.Stride+r.Min.X*4 : y*fb.Stride+r.Max.X*4]
			dst := work.Pix[y*work.Stride+r.Min.X*4 : y*work.Stride+r.Max.X*4]
			for i := 0; i < len(src); i += 4 {
				dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0xff
			}
		}
	}
	sc.mu.Unlock()
	if !sc.pending.Swap(true) {
		sc.s.update(sc.present)
	}
}

// present shows the newest picture (main thread).
func (sc *screenClient) present() {
	sc.pending.Store(false)
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.work == nil {
		return
	}
	copy(sc.shown.Pix, sc.work.Pix)
	if sc.bm == nil {
		sc.bm = ui.NewBitmap(sc.shown)
	} else {
		sc.bm.Update(sc.shown)
	}
}

// remoteClipboard takes the other desktop's clipboard text, which reaches
// this computer's while the tab is in front (the protocol's goroutine).
func (sc *screenClient) remoteClipboard(text string) {
	sc.mu.Lock()
	front, same := sc.front, sc.clip == text
	sc.clip = text
	sc.mu.Unlock()
	if front && !same && text != "" {
		clipboardWrite(text)
	}
}

// setFront: as the tab comes in front, this computer's clipboard text goes
// to the other desktop.
func (sc *screenClient) setFront(on bool) {
	sc.mu.Lock()
	was := sc.front
	sc.front = on
	sc.mu.Unlock()
	if !on || was {
		return
	}
	go func() {
		text := mygo.Clipboard.ReadText()
		if text == "" {
			return
		}
		sc.mu.Lock()
		same := sc.clip == text
		sc.clip = text
		sc.mu.Unlock()
		if !same {
			_ = sc.desk.clipboard(text)
		}
	}()
}

func (sc *screenClient) grabs() bool { return true }

// scan sends a key the grab took.
func (sc *screenClient) scan(ev core.KeyEvent) {
	k, ok := remoteKeysByScan[ev.Code]
	if !ok {
		k = remoteKey{scan: ev.Code}
	}
	_ = sc.desk.key(k, ev.Down != 0)
}

func (sc *screenClient) close() { sc.desk.close() }

// input passes an event on the picture to the other desktop: the pointer
// and the wheel in its pixels, keys by name and text in Legacy mode (the
// grab takes keys in Map and Translate).
func (sc *screenClient) input(ev ui.InputEvent) bool {
	sc.mu.Lock()
	box, fb := sc.box, sc.fb
	grabbed := sc.s.grabMode()
	if fb == nil {
		sc.mu.Unlock()
		return false
	}
	ox, oy, w, h := pictureBox(box, fb.Width, fb.Height)
	if sc.original {
		ox, oy, w, h = 0, 0, float32(fb.Width), float32(fb.Height)
	}
	x := int(min(max((ev.X-ox)/w, 0), 1) * float32(fb.Width-1))
	y := int(min(max((ev.Y-oy)/h, 0), 1) * float32(fb.Height-1))
	bits := map[int]uint8{0: 1, 1: 4, 2: 2}[ev.Button]
	switch ev.Kind {
	case ui.InputPointerDown:
		sc.buttons |= bits
	case ui.InputPointerUp:
		sc.buttons &^= bits
	}
	buttons := sc.buttons
	sc.mu.Unlock()
	switch ev.Kind {
	case ui.InputPointerMove, ui.InputPointerDown, ui.InputPointerUp:
		_ = sc.desk.pointer(x, y, buttons)
		return true
	case ui.InputScroll:
		dx, dy := ev.DX, ev.DY
		if !ev.Precise {
			dx, dy = dx*40, dy*40 // lines, about 40 pixels each
		}
		_ = sc.desk.scroll(x, y, float64(dx), float64(dy))
		return true
	case ui.InputText:
		if grabbed == "map" || ev.Text == "" {
			return false
		}
		_ = sc.desk.text(ev.Text)
		return true
	case ui.InputKeyDown, ui.InputKeyUp:
		if grabbed != "" {
			return false
		}
		return sc.legacyKey(ev)
	}
	return false
}

// legacyKey sends a key the toolkit reports by name, with its modifiers
// around it; keys that type go as text unless a shortcut's modifier is held.
func (sc *screenClient) legacyKey(ev ui.InputEvent) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if ev.Kind == ui.InputKeyUp {
		k, ok := sc.held[ev.Key]
		if !ok {
			return false
		}
		delete(sc.held, ev.Key)
		_ = sc.desk.key(k, false)
		sc.modifiers(ev.Mods, false)
		return true
	}
	name, ok := keyNames[ev.Key]
	k, known := remoteKeysByName[name]
	shortcut := ev.Mods&(ui.Ctrl|ui.Alt|ui.Super) != 0
	if !ok || !known || typing[ev.Key] && !shortcut {
		return false
	}
	sc.modifiers(ev.Mods, true)
	sc.held[ev.Key] = k
	_ = sc.desk.key(k, true)
	return true
}

// modifiers presses or releases the modifier keys mods holds. Notes: the
// caller holds sc.mu.
func (sc *screenClient) modifiers(mods ui.Modifiers, down bool) {
	for _, m := range []struct {
		on bool
		k  remoteKey
	}{{mods&ui.Shift != 0, keyShift}, {mods&ui.Ctrl != 0, keyCtrl}, {mods&ui.Alt != 0, keyAlt}, {mods&ui.Super != 0, keyMeta}} {
		if m.on {
			_ = sc.desk.key(m.k, down)
		}
	}
}

// ctrlAltDel presses Ctrl+Alt+Del on the other desktop.
func (sc *screenClient) ctrlAltDel() {
	for _, k := range []remoteKey{keyCtrl, keyAlt, keyDel} {
		_ = sc.desk.key(k, true)
	}
	for _, k := range []remoteKey{keyDel, keyAlt, keyCtrl} {
		_ = sc.desk.key(k, false)
	}
}

// grabMode is the keyboard mode the tab's grab takes keys in, "" for none.
func (s *serviceTab) grabMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grabbed
}

// view is the picture on black, with the toolbar over its top edge.
func (sc *screenClient) view(c *ui.Context, s *serviceTab) {
	sc.mu.Lock()
	bm, fb, original := sc.bm, sc.fb, sc.original
	sc.mu.Unlock()
	focused := false
	defer func() { s.syncGrab(focused) }()
	ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
		stage := ui.Column(c.Key("screen-stage")).Grow(1).FillWidth().MinHeight(0)
		stage.Children(func() {
			switch {
			case bm == nil:
				ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					busyMarkIn(c, L.ViewerWaiting(), busySizeLarge, ui.Hex("#ffffff"))
				})
			case original:
				w, h := float32(fb.Width), float32(fb.Height)
				scroll := ui.ScrollBoth(c.Key("screen-scroll")).Grow(1).MinWidth(0).FillWidth()
				area := scroll.LayoutBounds()
				scroll.Children(func() {
					ui.Column(c).MinWidth(area.W).MinHeight(area.H).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
						img := ui.Image(c.Key("screen"), bm).Width(w).Height(h).Shrink(0).Focusable().
							TextCaret(ui.Rect{W: 1, H: 16}).InputMethod(false).HandleInput(sc.input).Bind(&sc.pic)
						focused = pointerKeys(img)
						sc.setBox(img.LayoutBounds())
					})
				})
			default:
				// The controller's input method stays off in the remote
				// screen, as in a desktop viewer's picture.
				img := ui.Image(c.Key("screen"), bm).Fit(ui.Contain).Grow(1).FillWidth().Focusable().
					TextCaret(ui.Rect{W: 1, H: 16}).InputMethod(false).HandleInput(sc.input).Bind(&sc.pic)
				focused = pointerKeys(img)
				sc.setBox(img.LayoutBounds())
			}
			sc.toolbar(c, s)
		})
	})
}

// setBox keeps where the picture is, for input.
func (sc *screenClient) setBox(box ui.Rect) {
	sc.mu.Lock()
	sc.box = box
	sc.mu.Unlock()
}

// toolbar is the tab's controls over the picture's top edge, as the
// viewer's: Display (Fit or Original size), Keyboard (the keyboard mode),
// Ctrl+Alt+Del and Full screen.
func (sc *screenClient) toolbar(c *ui.Context, s *serviceTab) {
	t := c.Theme()
	back := func() { sc.pic.Focus(c) }
	sc.mu.Lock()
	original := sc.original
	sc.mu.Unlock()
	ui.Row(c.Key("screen-bar-lane")).Absolute().Top(barTop).Left(0).Right(0).Justify(ui.Center).PassThrough().Children(func() {
		ui.Row(c.Key("screen-bar")).AlignItems(ui.Center).Gap(2).Padding(2, 6).Radius(barRadius).
			Background(t.Surface).Border(1, t.Border).Shadow(0, 2, 10, 0, ui.RGBA(0, 0, 0, 0.3)).Children(func() {
			barMenu(c, "screen-display", barDisplay, L.ViewerDisplay(), func(m *ui.Menu) {
				for i, label := range []string{L.ViewerFit(), L.ViewerOriginal()} {
					if m.Item(label).Checked(original == (i == 1)).Chosen() {
						sc.mu.Lock()
						sc.original = i == 1
						sc.mu.Unlock()
						back()
					}
				}
			})
			if grabsKeyboard {
				barMenu(c, "screen-keyboard", barKeyboard, L.ViewerKeyboard(), func(m *ui.Menu) {
					mode := s.app.keyboardMode()
					for i, label := range []string{L.ViewerKeyboardMap(), L.ViewerKeyboardTranslate(), L.ViewerKeyboardLegacy()} {
						if m.Item(label).Checked(keyboardModes[i] == mode).Chosen() {
							s.app.setKeyboardMode(keyboardModes[i])
							back()
						}
					}
				})
			}
			barMenu(c, "screen-actions", barActions, L.ViewerActions(), func(m *ui.Menu) {
				if m.Item(L.ViewerCtrlAltDel()).Chosen() {
					go sc.ctrlAltDel()
					back()
				}
			})
			if w := s.tabWindow(); w != nil {
				full, icon, label := w.fullScreen(), barFullScreen, L.ViewerFullScreen()
				if full {
					icon, label = barExitFullScreen, L.ViewerExitFullScreen()
				}
				if barButton(c, "screen-full-screen", icon, label).Clicked() {
					w.win.SetFullScreen(!full)
					back()
				}
			}
		})
	})
}
