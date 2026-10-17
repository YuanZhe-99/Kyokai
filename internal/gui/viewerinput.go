package gui

import (
	"encoding/json"
	"fmt"

	"github.com/egoist/mygo/ui"
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
// sent exactly when its press was.
func toEvent(ev ui.InputEvent, box ui.Rect, pw, ph int, held map[ui.Key]bool) (inputEvent, bool) {
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
		if !ok || (typing[ev.Key] && !mods.Ctrl && !mods.Alt && !mods.Meta) {
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
		if ev.Text == "" {
			return inputEvent{}, false
		}
		return inputEvent{Kind: "text", Text: ev.Text}, true
	}
	return inputEvent{}, false
}

// qualityPresets are RustDesk's image qualities, in the viewer's order.
var qualityPresets = []string{"speed", "balanced", "best"}

// fpsCaps are the frame-rate caps the viewer offers.
var fpsCaps = []string{"15", "30", "60"}

// setQuality sends the viewer's quality preset and frame-rate cap to the
// host's QoS.
func (v *viewer) setQuality(preset int, fpsCap string) {
	v.mu.Lock()
	v.preset, v.fpsCap = preset, fpsCap
	v.mu.Unlock()
	fps := 30
	_, _ = fmt.Sscan(fpsCap, &fps)
	body, _ := json.Marshal(map[string]any{"type": "quality", "preset": qualityPresets[min(max(preset, 0), 2)], "fps": fps})
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
	e, ok := toEvent(ev, box, start.Width, start.Height, v.held)
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
