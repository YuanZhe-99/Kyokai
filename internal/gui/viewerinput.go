package gui

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
)

// inputEvent is one event for the host's input service (the core's
// input::Event): pointer positions are fractions of the picture.
type inputEvent struct {
	Kind   string     `json:"kind"`
	Action string     `json:"action,omitempty"`
	Button int        `json:"button"`
	X      float32    `json:"x"`
	Y      float32    `json:"y"`
	DX     float32    `json:"dx"`
	DY     float32    `json:"dy"`
	Key    string     `json:"key,omitempty"`
	Down   bool       `json:"down"`
	Mods   *inputMods `json:"mods,omitempty"`
	Text   string     `json:"text,omitempty"`
	// A key by position (kind "scan"): its scan code as Windows numbers it
	// and the controller's CapsLock and NumLock.
	Code uint32 `json:"code,omitempty"`
	Caps bool   `json:"caps,omitempty"`
	Num  bool   `json:"num,omitempty"`
}

type inputMods struct {
	Shift bool `json:"shift"`
	Ctrl  bool `json:"ctrl"`
	Alt   bool `json:"alt"`
	Meta  bool `json:"meta"`
}

// keyNames are the keys sent by name; letters and digits are added below.
var keyNames = map[ui.Key]string{
	ui.KeyEnter: "enter", ui.KeyEscape: "escape", ui.KeyBackspace: "backspace", ui.KeyTab: "tab",
	ui.KeySpace: "space", ui.KeyDelete: "delete", ui.KeyInsert: "insert", ui.KeyHome: "home",
	ui.KeyEnd: "end", ui.KeyPageUp: "page-up", ui.KeyPageDown: "page-down", ui.KeyLeft: "left",
	ui.KeyRight: "right", ui.KeyUp: "up", ui.KeyDown: "down", ui.KeyMinus: "minus", ui.KeyEqual: "equal",
	ui.KeyComma: "comma", ui.KeyPeriod: "period", ui.KeySlash: "slash", ui.KeySemicolon: "semicolon",
	ui.KeyQuote: "quote", ui.KeyBracketLeft: "bracket-left", ui.KeyBracketRight: "bracket-right",
	ui.KeyBackslash: "backslash", ui.KeyBackquote: "backquote", ui.KeyContextMenu: "context-menu",
	ui.KeyF1: "f1", ui.KeyF2: "f2", ui.KeyF3: "f3", ui.KeyF4: "f4", ui.KeyF5: "f5", ui.KeyF6: "f6",
	ui.KeyF7: "f7", ui.KeyF8: "f8", ui.KeyF9: "f9", ui.KeyF10: "f10", ui.KeyF11: "f11", ui.KeyF12: "f12",
}

// typing are the keys that also produce text: without Ctrl, Alt or Super
// they travel as text (layout and dead keys stay the controller's), with
// them as keys (shortcuts).
var typing = map[ui.Key]bool{}

func init() {
	for i, k := range []ui.Key{ui.KeyA, ui.KeyB, ui.KeyC, ui.KeyD, ui.KeyE, ui.KeyF, ui.KeyG, ui.KeyH, ui.KeyI,
		ui.KeyJ, ui.KeyK, ui.KeyL, ui.KeyM, ui.KeyN, ui.KeyO, ui.KeyP, ui.KeyQ, ui.KeyR, ui.KeyS, ui.KeyT,
		ui.KeyU, ui.KeyV, ui.KeyW, ui.KeyX, ui.KeyY, ui.KeyZ} {
		keyNames[k] = string(rune('a' + i))
		typing[k] = true
	}
	for i, k := range []ui.Key{ui.Key0, ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		keyNames[k] = string(rune('0' + i))
		typing[k] = true
	}
	for _, k := range []ui.Key{ui.KeySpace, ui.KeyMinus, ui.KeyEqual, ui.KeyComma, ui.KeyPeriod, ui.KeySlash,
		ui.KeySemicolon, ui.KeyQuote, ui.KeyBracketLeft, ui.KeyBracketRight, ui.KeyBackslash, ui.KeyBackquote} {
		typing[k] = true
	}
}

// pictureBox is where the picture shows inside an element of box size,
// fitted (Contain) and centred: offset and size.
func pictureBox(box ui.Rect, pw, ph int) (ox, oy, w, h float32) {
	if pw <= 0 || ph <= 0 || box.W <= 0 || box.H <= 0 {
		return 0, 0, 0, 0
	}
	scale := min(box.W/float32(pw), box.H/float32(ph))
	w, h = float32(pw)*scale, float32(ph)*scale
	return (box.W - w) / 2, (box.H - h) / 2, w, h
}

// toEvent turns a viewer input event into the host's, or reports false
// for one to leave alone. held tracks keys sent down, so a key's release is
// sent exactly when its press was. grabbed is the keyboard mode the core
// grabs keys in for this viewer ("" for none): then keys come from the
// grab, and text only in Translate mode.
func toEvent(ev ui.InputEvent, box ui.Rect, pw, ph int, held map[ui.Key]bool, grabbed string) (inputEvent, bool) {
	ox, oy, w, h := pictureBox(box, pw, ph)
	pos := func() (float32, float32, bool) {
		if w <= 0 || h <= 0 {
			return 0, 0, false
		}
		x, y := (ev.X-ox)/w, (ev.Y-oy)/h
		return min(max(x, 0), 1), min(max(y, 0), 1), x >= 0 && x <= 1 && y >= 0 && y <= 1
	}
	mods := &inputMods{Shift: ev.Mods&ui.Shift != 0, Ctrl: ev.Mods&ui.Ctrl != 0, Alt: ev.Mods&ui.Alt != 0, Meta: ev.Mods&ui.Super != 0}
	switch ev.Kind {
	case ui.InputPointerMove, ui.InputPointerDown, ui.InputPointerUp:
		x, y, inside := pos()
		action := map[ui.InputKind]string{ui.InputPointerMove: "move", ui.InputPointerDown: "down", ui.InputPointerUp: "up"}[ev.Kind]
		// Presses outside the picture (the black bars) do nothing; a release
		// always goes, so nothing stays held.
		if !inside && ev.Kind != ui.InputPointerUp {
			return inputEvent{}, false
		}
		if ev.Button < 0 || ev.Button > 2 {
			return inputEvent{}, false
		}
		return inputEvent{Kind: "pointer", Action: action, Button: ev.Button, X: x, Y: y}, true
	case ui.InputScroll:
		dx, dy := ev.DX, ev.DY
		if ev.Precise {
			// Touchpads report pixels: about 40 to a line.
			dx, dy = dx/40, dy/40
		}
		if dx == 0 && dy == 0 {
			return inputEvent{}, false
		}
		return inputEvent{Kind: "scroll", DX: min(max(dx, -100), 100), DY: min(max(dy, -100), 100)}, true
	case ui.InputKeyDown:
		name, ok := keyNames[ev.Key]
		if !ok || grabbed != "" || (typing[ev.Key] && !mods.Ctrl && !mods.Alt && !mods.Meta) {
			return inputEvent{}, false
		}
		held[ev.Key] = true
		return inputEvent{Kind: "key", Key: name, Down: true, Mods: mods}, true
	case ui.InputKeyUp:
		name, ok := keyNames[ev.Key]
		if !ok || !held[ev.Key] {
			return inputEvent{}, false
		}
		delete(held, ev.Key)
		return inputEvent{Kind: "key", Key: name, Down: false, Mods: mods}, true
	case ui.InputText:
		if ev.Text == "" || grabbed == "map" {
			return inputEvent{}, false
		}
		return inputEvent{Kind: "text", Text: ev.Text}, true
	}
	return inputEvent{}, false
}

// qualityPresets are RustDesk's image qualities, in the viewer's order.
var qualityPresets = []string{"speed", "balanced", "best"}

// fpsCaps are the frame-rate caps the viewer offers, as it shows them
// (setQuality reads the number).
var fpsCaps = []string{"15 fps", "30 fps", "60 fps"}

// setQuality sends the viewer's quality preset and frame-rate cap to the
// host's QoS, and whether it wants full colour (4:4:4) where both sides
// do it, which may restart the video in another codec.
func (v *viewer) setQuality(preset int, fpsCap string, full bool) {
	v.mu.Lock()
	v.preset, v.fpsCap, v.full = preset, fpsCap, full
	v.mu.Unlock()
	fps := 30
	_, _ = fmt.Sscan(fpsCap, &fps)
	body, _ := json.Marshal(map[string]any{"type": "quality", "preset": qualityPresets[min(max(preset, 0), 2)], "fps": fps, "i444": full})
	select {
	case v.out <- append([]byte{packetControl}, body...):
	default:
	}
}

// controlPacket wraps an input event as a media control packet.
func controlPacket(e inputEvent) []byte {
	body, _ := json.Marshal(map[string]any{"type": "input", "event": e})
	return append([]byte{packetControl}, body...)
}

// sendInput passes one viewer event to the host, on the media connection.
func (v *viewer) sendInput(ev ui.InputEvent) bool {
	v.mu.Lock()
	box, start, may := v.box, v.start, v.control
	if v.held == nil {
		v.held = map[ui.Key]bool{}
	}
	e, ok := toEvent(ev, box, start.Width, start.Height, v.held, v.grabbed)
	v.mu.Unlock()
	if !may || !ok {
		return false
	}
	// One writer keeps packets whole and in order; a full queue drops the
	// event rather than stall the window.
	select {
	case v.out <- controlPacket(e):
	default:
	}
	return true
}

// keyboardModes are RustDesk's keyboard modes, in the viewer's order.
var keyboardModes = []string{"map", "translate", "legacy"}

// grabsKeyboard is whether the core can grab the keyboard here (Map and
// Translate); elsewhere every viewer works in Legacy mode.
const grabsKeyboard = runtime.GOOS == "windows"

// setKeyboardMode remembers the viewers' keyboard mode; each viewer grabs
// in it the next time its picture has the focus.
func (a *App) setKeyboardMode(mode string) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	p.KeyboardMode = mode
	a.setPrefs(p)
}

// setShowPointer remembers whether the viewers mark the host's pointer.
func (a *App) setShowPointer(show bool) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	p.HidePointer = !show
	a.setPrefs(p)
}

// showPointer reports whether the viewers mark the host's pointer (on
// unless switched off).
func (a *App) showPointer() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.prefs.HidePointer
}

// keyboardMode is the remembered keyboard mode (Map unless chosen).
func (a *App) keyboardMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch m := a.prefs.KeyboardMode; {
	case !grabsKeyboard:
		return "legacy"
	case m == "translate" || m == "legacy":
		return m
	}
	return "map"
}

// syncGrab has the core grab the keyboard for this viewer's window while
// its picture has the focus in Map or Translate mode, and ends the grab
// otherwise. A grab the core refuses leaves the viewer in Legacy mode.
// Side effects: starts the app's key reader with the first grab. Call it
// on the main thread.
func (v *viewer) syncGrab(focused bool) {
	want := ""
	if focused && v.control {
		if mode := v.app.keyboardMode(); mode != "legacy" {
			want = mode
		}
	}
	v.mu.Lock()
	have, refused := v.grabbed, v.noGrab
	v.mu.Unlock()
	if want == have || refused && want != "" {
		return
	}
	lib, err := coreLib()
	if err != nil {
		return
	}
	window := v.win.NativeHandle()
	mode := want
	if mode == "" {
		mode = "off"
	}
	err = lib.KeyboardGrab(window, mode)
	v.mu.Lock()
	v.window = window
	if v.noGrab = err != nil; v.noGrab {
		want = ""
	}
	v.grabbed = want
	v.mu.Unlock()
	if want != "" {
		v.app.keyReader.Do(func() { go v.app.readKeys(lib) })
	}
}

// endGrab ends this viewer's keyboard grab as its window closes, which
// releases the keys it held.
func (v *viewer) endGrab() {
	v.mu.Lock()
	window, grabbed := v.window, v.grabbed
	v.grabbed = ""
	v.mu.Unlock()
	if lib, err := coreLib(); err == nil && grabbed != "" {
		_ = lib.KeyboardGrab(window, "off")
	}
}

// readKeys passes the keys the core's keyboard grab takes to the viewer
// whose window they were taken for, as scan-code input events.
// Notes: runs for the app's lifetime once a viewer grabbed the keyboard;
// keys for a window that closed are dropped (its session ends).
func (a *App) readKeys(lib *core.Lib) {
	kb := lib.Keyboard()
	for {
		ev, ok, err := kb.Read(time.Second)
		if err != nil {
			return
		}
		if !ok {
			continue
		}
		a.mu.Lock()
		viewers := make([]*viewer, 0, len(a.viewers))
		for _, v := range a.viewers {
			viewers = append(viewers, v)
		}
		a.mu.Unlock()
		for _, v := range viewers {
			v.mu.Lock()
			mine, may := v.window == ev.Window, v.control
			v.mu.Unlock()
			if mine && may {
				select {
				case v.out <- controlPacket(scanEvent(ev)):
				default:
				}
				break
			}
		}
	}
}

// scanEvent is the host's input event for a key the grab took.
func scanEvent(ev core.KeyEvent) inputEvent {
	return inputEvent{Kind: "scan", Code: ev.Code, Down: ev.Down != 0, Caps: ev.Caps != 0, Num: ev.Num != 0}
}
