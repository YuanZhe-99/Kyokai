package gui

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
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
		button := ev.Button
		// A move with no button held (-1) moves the pointer; the host
		// knows which buttons it holds.
		if ev.Kind == ui.InputPointerMove && button < 0 {
			button = 0
		}
		if button < 0 || button > 2 {
			return inputEvent{}, false
		}
		return inputEvent{Kind: "pointer", Action: action, Button: button, X: x, Y: y}, true
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
	v.keepSettings()
	fps := 30
	_, _ = fmt.Sscan(fpsCap, &fps)
	body, _ := json.Marshal(map[string]any{"type": "quality", "preset": qualityPresets[min(max(preset, 0), 2)], "fps": fps, "i444": full})
	select {
	case v.out <- append([]byte{packetControl}, body...):
	default:
	}
}

// setCodec sends the viewer's codec choice ("auto" or one the host
// offered); the host restarts the video in it.
func (v *viewer) setCodec(codec string) {
	v.mu.Lock()
	v.codec = codec
	v.mu.Unlock()
	v.keepSettings()
	v.sendControl(map[string]any{"type": "codec", "codec": codec})
}

// keepSettings keeps this viewer's settings for its device.
// Side effects: writes gui.json.
func (v *viewer) keepSettings() {
	if v.app == nil {
		return
	}
	v.mu.Lock()
	vp := ViewerPrefs{Preset: v.preset, FPS: v.fpsCap, Full: v.full, Codec: v.codec, Swap: v.swap, Mute: v.mute, Collapsed: v.collapsed, Original: v.original, NoClipboard: v.clip.off}
	v.mu.Unlock()
	v.app.setViewerPrefs(v.device, vp)
}

// setSound plays the host's sound or not; the host captures it only while
// asked.
func (v *viewer) setSound(on bool) {
	v.mu.Lock()
	v.mute = !on
	v.mu.Unlock()
	v.keepSettings()
	v.sendControl(map[string]any{"type": "audio", "on": on})
}

// setSwap swaps Ctrl and Cmd (the Windows key) in what this viewer sends,
// or not.
func (v *viewer) setSwap(on bool) {
	v.mu.Lock()
	v.swap = &on
	v.mu.Unlock()
	v.keepSettings()
}

// swapping is whether this viewer swaps Ctrl and Cmd now: as chosen, else
// by the two systems.
// Notes: the caller holds v.mu.
func (v *viewer) swapping() bool {
	if v.swap != nil {
		return *v.swap
	}
	return swapDefault(runtime.GOOS, v.phase.Peer.OS)
}

// swapDefault is whether Ctrl and Cmd are swapped unless chosen: between a
// Mac and Windows, so each person's shortcuts do on the other computer what
// they do on their own (Cmd+C copies on Windows, Ctrl+C on a Mac).
// Inputs: the controller's and the host's systems (GOOS names and the
// About OS names: "darwin" or "macos", "windows").
func swapDefault(mine, peer string) bool {
	mac := func(os string) bool { return os == "darwin" || os == "macos" }
	return mac(mine) && peer == "windows" || mine == "windows" && mac(peer)
}

// swapCtrlMeta exchanges Ctrl and Cmd (the Windows key) in a key event:
// in its modifiers, and the left and right keys sent by position.
func swapCtrlMeta(e inputEvent) inputEvent {
	if e.Mods != nil {
		m := *e.Mods
		m.Ctrl, m.Meta = m.Meta, m.Ctrl
		e.Mods = &m
	}
	if e.Kind == "scan" {
		switch e.Code {
		case 0x1D:
			e.Code = 0xE05B
		case 0xE05B:
			e.Code = 0x1D
		case 0xE01D:
			e.Code = 0xE05C
		case 0xE05C:
			e.Code = 0xE01D
		}
	}
	return e
}

// commandKeys are the Cmd shortcuts behind the edit commands a Mac's menu
// takes before the window sees the keys (Legacy mode): the key, and Shift.
var commandKeys = map[string]struct {
	key   string
	shift bool
}{
	"copy": {"c", false}, "cut": {"x", false}, "paste": {"v", false}, "pasteAndMatchStyle": {"v", false},
	"selectAll": {"a", false}, "undo": {"z", false}, "redo": {"z", true},
}

// commandEvents turns an edit command of the menu back into its Cmd
// shortcut, pressed and released.
// Returns: nothing for another command.
func commandEvents(command string) []inputEvent {
	k, ok := commandKeys[command]
	if !ok {
		return nil
	}
	mods := &inputMods{Meta: true, Shift: k.shift}
	return []inputEvent{{Kind: "key", Key: k.key, Down: true, Mods: mods}, {Kind: "key", Key: k.key, Down: false, Mods: mods}}
}

// choicePackets are the viewer's quality and codec choices as control
// packets, for a stream that (re)starts: the host starts each session
// with its defaults. The last, its clipboard choice, stays with this
// side's engine.
func (v *viewer) choicePackets() [][]byte {
	v.mu.Lock()
	preset, fpsCap, full, codec, mute := v.preset, v.fpsCap, v.full, v.codec, v.mute
	clip := v.clipUsePacket()
	v.mu.Unlock()
	fps := 30
	_, _ = fmt.Sscan(fpsCap, &fps)
	quality, _ := json.Marshal(map[string]any{"type": "quality", "preset": qualityPresets[min(max(preset, 0), 2)], "fps": fps, "i444": full})
	out := [][]byte{append([]byte{packetControl}, quality...)}
	if codec != "" && codec != "auto" {
		body, _ := json.Marshal(map[string]any{"type": "codec", "codec": codec})
		out = append(out, append([]byte{packetControl}, body...))
	}
	sound, _ := json.Marshal(map[string]any{"type": "audio", "on": !mute})
	return append(out, append([]byte{packetControl}, sound...), clip)
}

// codecLabel is how the viewer names a codec: Auto, the codec's usual
// name, and "(software)" for libvpx's VP9 beside the system's.
func codecLabel(name string) string {
	switch name {
	case "auto", "":
		return L.ViewerCodecAuto()
	case "h264":
		return "H.264"
	case "hevc":
		return "HEVC"
	case "vp9", "vp9-sw":
		return "VP9"
	case "av1":
		return "AV1"
	case "vp8":
		return "VP8"
	}
	return name
}

// codecChoices are the Codec choice's entries: Auto, then one per codec of
// the host's offer, by name only (doc/en-us/gui.md); a host offering VP9
// twice (Media Foundation and libvpx) is asked for the first, its better one.
// Returns: the wire names and the labels shown, in the same order.
func codecChoices(offer []string) (names, labels []string) {
	names, labels = []string{"auto"}, []string{codecLabel("auto")}
	for _, n := range offer {
		if l := codecLabel(n); !slices.Contains(labels, l) {
			names, labels = append(names, n), append(labels, l)
		}
	}
	return names, labels
}

// implLabel words which encoder or decoder handles a stream and whether in
// hardware or software, as it reported.
func implLabel(i codecImpl) string {
	name := map[string]string{"videotoolbox": "VideoToolbox", "media-foundation": "Media Foundation",
		"libvpx": "libvpx", "libaom": "libaom"}[i.name]
	switch {
	case name == "":
		return L.ViewerImplUnknown()
	case i.hardware:
		return L.ViewerImplHardware(name)
	}
	return L.ViewerImplSoftware(name)
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
	grabbed := v.grabbed
	e, ok := toEvent(ev, box, start.Width, start.Height, v.held, v.grabbed)
	v.mu.Unlock()
	// A Mac's menu takes Cmd+C and the like before the picture sees the
	// keys: they come back as the menu's commands (Legacy mode; a grab
	// takes the keys first).
	if ev.Kind == ui.InputCommand && grabbed == "" && runtime.GOOS == "darwin" {
		es := commandEvents(ev.Text)
		for _, c := range es {
			if may {
				v.queue(c)
			}
		}
		return len(es) > 0
	}
	if !may || !ok {
		return false
	}
	v.queue(e)
	return true
}

// moveEvery is how often the writer sends the pointer's newest move at
// most; the moves between are left out.
const moveEvery = 8 * time.Millisecond

// queue passes one input event to the media connection's writer. A move
// becomes the pending one (a newer move replaces it before it is sent);
// any other event sends the pending move first, so the order holds.
// Notes: a full queue drops the event rather than stall the window.
func (v *viewer) queue(e inputEvent) {
	v.mu.Lock()
	if e.Kind == "pointer" && e.Action == "move" {
		v.move = &e
		v.mu.Unlock()
		select {
		case v.moved <- struct{}{}:
		default:
		}
		return
	}
	pending := v.move
	v.move = nil
	if v.swapping() {
		e = swapCtrlMeta(e)
	}
	v.mu.Unlock()
	for _, p := range []*inputEvent{pending, &e} {
		if p == nil {
			continue
		}
		select {
		case v.out <- controlPacket(*p):
		default:
		}
	}
}

// writer is the media connection's one writer, which keeps packets whole
// and in order: the queued packets, and the pending move at most every
// moveEvery, until done closes or a write fails.
func (v *viewer) writer(w io.Writer, done <-chan struct{}) {
	var last time.Time
	var later <-chan time.Time
	sendMove := func() error {
		v.mu.Lock()
		m := v.move
		v.move = nil
		v.mu.Unlock()
		if m == nil {
			return nil
		}
		last = time.Now()
		return localapi.WritePacket(w, controlPacket(*m))
	}
	for {
		var err error
		select {
		case p := <-v.out:
			err = localapi.WritePacket(w, p)
		case <-v.moved:
			if wait := moveEvery - time.Since(last); wait > 0 {
				if later == nil {
					later = time.After(wait)
				}
				continue
			}
			err = sendMove()
		case <-later:
			later = nil
			err = sendMove()
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

// keyboardModes are RustDesk's keyboard modes, in the viewer's order.
var keyboardModes = []string{"map", "translate", "legacy"}

// grabsKeyboard is whether the core can grab the keyboard here (Map and
// Translate: a hook on Windows, an event tap on macOS); elsewhere every
// viewer works in Legacy mode.
const grabsKeyboard = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

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
	w := v.home.Load()
	if w == nil {
		return
	}
	window := w.win.NativeHandle()
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
			// Several tabs share a window: the one holding the grab takes it.
			mine, may := v.window == ev.Window && v.grabbed != "", v.control
			v.mu.Unlock()
			if mine && may {
				v.queue(scanEvent(ev))
				break
			}
		}
	}
}

// scanEvent is the host's input event for a key the grab took.
func scanEvent(ev core.KeyEvent) inputEvent {
	return inputEvent{Kind: "scan", Code: ev.Code, Down: ev.Down != 0, Caps: ev.Caps != 0, Num: ev.Num != 0}
}
