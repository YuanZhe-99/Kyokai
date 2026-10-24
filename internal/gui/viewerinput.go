package gui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
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
// grab, and text is whatever still reaches the window — the characters
// Translate mode leaves to the controller's layout, or characters another
// program injects as text (the
// grab leaves those alone; regression: Kyokai inside a RustDesk session
// dropped them in Map mode).
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
		if ev.Text == "" {
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
	vp := ViewerPrefs{Preset: v.preset, FPS: v.fpsCap, Full: v.full, Codec: v.codec, Swap: v.swap, Mute: v.mute, Folded: v.folded,
		Original: v.original, NoClipboard: v.clip.off}
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
	if ev.Kind == ui.InputPointerMove || ev.Kind == ui.InputPointerDown {
		v.app.menuClosed() // a native menu takes the pointer while it is open
	}
	now := time.Now()
	v.mu.Lock()
	box, start, may := v.box, v.start, v.control
	if v.held == nil {
		v.held = map[ui.Key]bool{}
	}
	// A key the grab should have taken that reached the window got past it
	// (the system did not hand it to the grab): it goes as in Legacy mode,
	// and the grab is renewed. So do a Mac menu's commands.
	grabbed := v.grabbed
	held := grabbed != "" && now.Sub(v.grabbedAt) > grabMissGrace
	passed := held && grabLetPass(grabbed, ev, runtime.GOOS)
	command := ev.Kind == ui.InputCommand && runtime.GOOS == "darwin" && (grabbed == "" || held)
	if passed || command && grabbed != "" {
		name := ev.Text
		if n, ok := keyNames[ev.Key]; ok && passed {
			name = n
		}
		v.missedKeyLocked(name, now)
		grabbed = ""
	}
	e, ok := toEvent(ev, box, start.Width, start.Height, v.held, grabbed)
	v.mu.Unlock()
	// A Mac's menu takes Cmd+C and the like before the picture sees the
	// keys: they come back as the menu's commands (Legacy mode, or a grab
	// they got past).
	if command {
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

// release reports whether an event lets go of a key or a button: those are
// never dropped, so nothing stays held on the host.
func release(e inputEvent) bool {
	switch e.Kind {
	case "pointer":
		return e.Action == "up"
	case "key", "scan":
		return !e.Down
	}
	return false
}

// queue passes one input event to the media connection's writer. A move
// becomes the pending one (a newer move replaces it before it is sent);
// any other event sends the pending move first, so the order holds.
// Notes: a full queue drops a press rather than stall the window; a
// release it keeps aside, and the writer sends it once the queue drains.
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
			if release(*p) {
				v.mu.Lock()
				v.releases = append(v.releases, *p)
				v.mu.Unlock()
				select {
				case v.moved <- struct{}{}:
				default:
				}
			}
		}
	}
}

// takeReleases hands over the releases a full queue kept aside.
func (v *viewer) takeReleases() []inputEvent {
	v.mu.Lock()
	defer v.mu.Unlock()
	r := v.releases
	v.releases = nil
	return r
}

// writer is the media connection's one writer, which keeps packets whole
// and in order: the queued packets, the releases a full queue kept aside
// once it drained, and the pending move at most every moveEvery, until
// done closes or a write fails.
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
	sendReleases := func() error {
		if len(v.out) > 0 {
			return nil // after the queue, which came first
		}
		for _, r := range v.takeReleases() {
			if err := localapi.WritePacket(w, controlPacket(r)); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		var err error
		select {
		case p := <-v.out:
			if err = localapi.WritePacket(w, p); err == nil {
				err = sendReleases()
			}
		case <-v.moved:
			if err = sendReleases(); err != nil {
				return
			}
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
// in it the next time the pointer is over its picture.
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

// setToolbarAt remembers where the viewers' toolbar sits (Prefs.ToolbarAt).
// Side effects: writes gui.json.
func (a *App) setToolbarAt(at float32) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	p.ToolbarAt = at
	a.setPrefs(p)
}

// toolbarAt is where the viewers' toolbar sits (Prefs.ToolbarAt), held
// within its range.
func (a *App) toolbarAt() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return min(max(a.prefs.ToolbarAt, -0.5), 0.5)
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

// grabKeyboard asks the core to grab `window`'s keyboard in `mode` for tab
// t, or to end the grab ("off"), when grabTurn says the call is t's.
// Returns: the core's answer; nil for an end that was not t's to make.
func (a *App) grabKeyboard(lib *core.Lib, window uintptr, t any, mode string) error {
	a.mu.Lock()
	call := grabTurn(&a.grabbing, window, t, mode)
	a.mu.Unlock()
	if !call {
		return nil
	}
	err := lib.KeyboardGrab(window, mode)
	if err != nil && mode != "off" {
		a.mu.Lock()
		if a.grabbing[window] == t {
			delete(a.grabbing, window)
		}
		a.mu.Unlock()
	}
	return err
}

// grabTurn keeps which tab holds each window's keyboard grab and decides
// whether t's call reaches the core. The core holds one grab per window,
// while each tab follows its own: the tab that asked last holds it, and
// only that tab ends it (regression: as the shown tab changed, the tab
// just hidden ended the grab the newly shown one had taken a moment
// before; the shown tab still took itself for grabbing, dropped the
// window's own keys, and only text reached the host — no Tab, Enter,
// Delete or Shift).
// Notes: the caller holds a.mu.
func grabTurn(holders *map[uintptr]any, window uintptr, t any, mode string) bool {
	if *holders == nil {
		*holders = map[uintptr]any{}
	}
	if mode != "off" {
		(*holders)[window] = t
		return true
	}
	if holder, held := (*holders)[window]; !held || holder != t {
		return false
	}
	delete(*holders, window)
	return true
}

// inputStalled reports whether the session cannot take input now: it is
// reconnecting, the engine has refused input for more than
// inputPauseGrace, or no frame nor stats came for stalledFeed while the
// host sends video. The keyboard grab steps aside meanwhile, so the
// controller's own keyboard keeps working (Alt+Tab, the Windows key, its
// input method, other programs) instead of feeding a session that drops
// what it is given.
// Notes: the caller holds v.mu.
func (v *viewer) inputStalled(now time.Time) bool {
	return v.phase.State == "reconnecting" ||
		v.paused && now.Sub(v.pausedAt) > inputPauseGrace ||
		v.start.Width > 0 && now.Sub(v.heardAt) > stalledFeed
}

// inputPauseGrace is how long the engine may refuse input before the
// keyboard grab steps aside.
const inputPauseGrace = 500 * time.Millisecond

// syncGrab has the core grab the keyboard for this viewer's window while
// the pointer is anywhere in its pane (inPane: not covered, see covered) in
// Map or Translate mode and the session takes input, and ends the grab
// otherwise; a grab it holds is renewed after a key got past it and once
// its window became active again (grabCall). A grab the core refuses
// leaves the viewer in Legacy mode.
// Side effects: starts the app's key reader with the first grab; asks how
// the grab stands while it holds (askGrabState). Call it on the main
// thread.
func (v *viewer) syncGrab(inPane bool) {
	want := ""
	now := time.Now()
	v.mu.Lock()
	stalled := v.inputStalled(now)
	v.mu.Unlock()
	if inPane && v.control && !stalled {
		if mode := v.app.keyboardMode(); mode != "legacy" {
			want = mode
		}
	}
	w := v.home.Load()
	var active uint64
	if w != nil {
		active = w.activeGen()
	}
	v.mu.Lock()
	have, refused := v.grabbed, v.noGrab
	renewing := want != "" && want == have && (v.renew || v.grabGen != active)
	v.mu.Unlock()
	if !grabCall(want, have, refused, renewing) {
		if want != "" {
			v.askGrabState(now)
		}
		return
	}
	lib, err := coreLib()
	if err != nil || w == nil {
		return
	}
	window := w.win.NativeHandle()
	mode := want
	if mode == "" {
		mode = "off"
	}
	err = v.app.grabKeyboard(lib, window, v, mode)
	v.mu.Lock()
	v.window = window
	if v.noGrab = err != nil; v.noGrab {
		want = ""
	}
	if have != "" && want == "" {
		v.ungrabbed = now
	}
	if want != "" {
		v.grabbedAt, v.renew, v.grabGen = now, false, active
		v.kb.at = time.Time{} // ask how the renewed grab stands at once
	}
	v.grabbed = want
	v.mu.Unlock()
	if want != "" {
		v.app.keyReader.Do(func() { go v.app.readKeys(lib) })
		v.askGrabState(now)
	}
}

// grabCall decides whether a viewer asks the core about its grab: when what
// it wants changes, and to renew the grab it holds (renewing); never to
// take a grab the core refused.
func grabCall(want, have string, refused, renewing bool) bool {
	if refused && want != "" {
		return false
	}
	return want != have || renewing
}

// grabStatus is how the core last said a viewer's keyboard grab stands
// (core.KeyboardState), for the toolbar's dots and the log.
type grabStatus struct {
	core.KeyboardState
	// at: when the core was asked; missed: when a key the grab should have
	// taken last reached the window.
	at, missed time.Time
	// logged is the state the log last noted as letting keys pass ("" while
	// the grab takes them).
	logged string
	// asking: a redraw that asks again is on its way.
	asking bool
}

// Timings of the grab's status: how often a viewer that grabs asks the core
// how its grab stands; how soon after the grab was taken a key reaching the
// window was typed before it (not missed); how long a missed key keeps the
// second dot amber.
const (
	grabAskEvery  = 500 * time.Millisecond
	grabMissGrace = 150 * time.Millisecond
	grabMissShown = 3 * time.Second
)

// askGrabState asks the core, at most every grabAskEvery, how the grab
// stands; notes in the log when it starts letting keys pass and when it
// takes them again; asks for a renewal where one helps (the tap or hook
// off, the grab gone or another window's); and has the window drawn again
// to ask once more while the grab lasts.
// Notes: call on the main thread while the viewer grabs.
func (v *viewer) askGrabState(now time.Time) {
	v.mu.Lock()
	due, window := now.Sub(v.kb.at) >= grabAskEvery, v.window
	v.mu.Unlock()
	if due {
		if lib, err := coreLib(); err == nil {
			if s, err := lib.KeyboardState(window); err == nil {
				v.noteGrabState(s, now)
			}
		}
	}
	v.mu.Lock()
	again := !v.kb.asking
	v.kb.asking = true
	v.mu.Unlock()
	if again {
		time.AfterFunc(grabAskEvery, func() {
			v.update(func() {
				v.mu.Lock()
				v.kb.asking = false
				v.mu.Unlock()
			})
		})
	}
}

// noteGrabState keeps what the core said of the grab (askGrabState).
// Side effects: a log line as the grab starts letting keys pass, with why,
// and as it takes them again — not for another program in front, which is
// what switching programs does (a key missed meanwhile says it); asks for a
// renewal where one helps.
func (v *viewer) noteGrabState(s core.KeyboardState, now time.Time) {
	v.mu.Lock()
	v.kb.KeyboardState, v.kb.at = s, now
	logged := v.kb.logged
	passing := s.State != "taking" && s.State != "behind"
	if passing {
		v.kb.logged = s.State
	} else {
		v.kb.logged = ""
	}
	switch s.State {
	case "off", "none", "other-window":
		v.renew = true
	}
	device := v.device
	v.mu.Unlock()
	switch {
	case passing && logged != s.State:
		log.Printf("keyboard: the grab for %s lets keys pass: %s%s; renewing it where that helps", device, s.State, holderNote(s.Holder))
	case !passing && logged != "":
		log.Printf("keyboard: the grab for %s takes the keys again", device)
	}
}

// holderNote names the program a grab state names, for the log.
func holderNote(holder string) string {
	if holder == "" {
		return ""
	}
	return " (" + holder + ")"
}

// grabLetPass reports whether a key that reached the window had to get past
// a grab in mode: in Map every key goes by position, so any key the window
// sees did; in Translate the keys that type stay the controller's by design,
// so only those that never type (Enter, Esc, Backspace, Tab, Delete,
// Insert, the arrows, Home, End, Page Up and Down, the F keys) and shortcuts
// did — Cmd or Ctrl on a Mac, Win, or Ctrl or Alt alone, elsewhere (Ctrl
// and Alt together are AltGr, which types).
func grabLetPass(mode string, ev ui.InputEvent, goos string) bool {
	if ev.Kind != ui.InputKeyDown {
		return false
	}
	if _, ok := keyNames[ev.Key]; !ok {
		return false
	}
	if mode == "map" {
		return true
	}
	ctrl, alt, meta := ev.Mods&ui.Ctrl != 0, ev.Mods&ui.Alt != 0, ev.Mods&ui.Super != 0
	if meta || goos == "darwin" && ctrl || goos != "darwin" && ctrl != alt {
		return true
	}
	return !typing[ev.Key] && !punctuation[ev.Key]
}

// punctuation are the named keys besides the letters and digits (typing)
// that type a character, which Translate leaves to the controller's layout:
// Space and the punctuation keys.
var punctuation = map[ui.Key]bool{
	ui.KeySpace: true, ui.KeyMinus: true, ui.KeyEqual: true, ui.KeyComma: true, ui.KeyPeriod: true,
	ui.KeySlash: true, ui.KeySemicolon: true, ui.KeyQuote: true, ui.KeyBracketLeft: true,
	ui.KeyBracketRight: true, ui.KeyBackslash: true, ui.KeyBackquote: true,
}

// dot is one of the toolbar's keyboard dots: off, on, or amber.
type dot int

const (
	dotOff dot = iota
	dotOn
	dotWarn
)

// grabDots are the toolbar's keyboard dots: the first on while the viewer
// holds the grab; the second on while the core says the grab takes the keys
// and none got past it within grabMissShown, amber otherwise; both off
// without a grab.
func grabDots(grabbing bool, s grabStatus, now time.Time) (first, second dot) {
	if !grabbing {
		return dotOff, dotOff
	}
	if s.State != "taking" && s.State != "" || !s.missed.IsZero() && now.Sub(s.missed) < grabMissShown {
		return dotOn, dotWarn
	}
	if s.State == "" {
		return dotOn, dotOff // not asked yet
	}
	return dotOn, dotOn
}

// grabHint says in the keyboard button's tooltip how the grab stands.
func grabHint(grabbing bool, s grabStatus, now time.Time) string {
	if !grabbing {
		return L.ViewerKeyboardGrabOff()
	}
	if !s.missed.IsZero() && now.Sub(s.missed) < grabMissShown && s.State == "taking" {
		return L.ViewerKeyboardGrabMissed()
	}
	switch s.State {
	case "taking", "":
		return L.ViewerKeyboardGrabTaking()
	case "secure-input":
		if s.Holder != "" {
			return L.ViewerKeyboardGrabSecureBy(s.Holder)
		}
		return L.ViewerKeyboardGrabSecure()
	case "behind":
		return L.ViewerKeyboardGrabBehind()
	case "no-reader":
		return L.ViewerKeyboardGrabNoReader()
	}
	return L.ViewerKeyboardGrabRenewing()
}

// missedKeyLocked notes a key that got past the grab: the second dot turns
// amber, the grab is renewed, and the log says so once in a while.
// Notes: the caller holds v.mu.
func (v *viewer) missedKeyLocked(name string, now time.Time) {
	first := v.kb.missed.IsZero() || now.Sub(v.kb.missed) >= grabMissShown
	v.kb.missed, v.renew = now, true
	if first {
		state := v.kb.State
		if state == "" {
			state = "not asked yet"
		}
		log.Printf("keyboard: %s for %s got past the %s grab (the core last said: %s%s); sent it as typed, renewing the grab",
			name, v.device, v.grabbed, state, holderNote(v.kb.Holder))
	}
}

// endGrab ends this viewer's keyboard grab as its window closes, which
// releases the keys it held.
func (v *viewer) endGrab() {
	v.mu.Lock()
	window, grabbed := v.window, v.grabbed
	v.grabbed = ""
	if grabbed != "" {
		v.ungrabbed = time.Now()
	}
	v.mu.Unlock()
	if lib, err := coreLib(); err == nil && grabbed != "" {
		_ = v.app.grabKeyboard(lib, window, v, "off")
	}
}

// readKeys passes the keys the core's keyboard grab takes to the viewer
// whose window they were taken for, as scan-code input events.
// Notes: runs for the app's lifetime once a viewer grabbed the keyboard;
// keys for a window that closed are dropped (its session ends). It asks
// again well within the core's READER_GONE (a second), which takes keys
// only while the GUI reads them.
func (a *App) readKeys(lib *core.Lib) {
	kb := lib.Keyboard()
	for {
		ev, ok, err := kb.Read(250 * time.Millisecond)
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
		if v := grabFor(viewers, ev); v != nil {
			v.queue(scanEvent(ev))
		} else {
			a.takeKey(ev) // a Remote Desktop or VNC tab's (servicetab.go)
		}
	}
}

// grabFor is the viewer a key the grab took belongs to: of the tabs that
// share its window, the one holding the grab; a release, which the core
// also queues as a grab ends, goes to the tab whose grab there ended last
// (regression: those releases were dropped, and a key stayed held on the
// host when the grab stepped aside). Nil for none that may control input.
func grabFor(viewers []*viewer, ev core.KeyEvent) *viewer {
	var ended *viewer
	var at time.Time
	for _, v := range viewers {
		v.mu.Lock()
		here, grabbing, ungrabbed, may := v.window == ev.Window, v.grabbed != "", v.ungrabbed, v.control
		v.mu.Unlock()
		switch {
		case !here || !may:
		case grabbing:
			return v
		case ev.Down == 0 && ungrabbed.After(at):
			ended, at = v, ungrabbed
		}
	}
	return ended
}

// scanEvent is the host's input event for a key the grab took.
func scanEvent(ev core.KeyEvent) inputEvent {
	return inputEvent{Kind: "scan", Code: ev.Code, Down: ev.Down != 0, Caps: ev.Caps != 0, Num: ev.Num != 0}
}

// pointerKeys gives a picture the text focus while the pointer is over it,
// or anywhere in its pane where the grab holds (inPane), unless something
// over the panes takes the keyboard (covered): typed text then goes to the
// session the grab sends the keys to, with panes side by side the one under
// the pointer (doc/en-us/computer-use.md, "Keyboard modes").
func pointerKeys(img ui.Element, inPane, covered bool) {
	if (inPane || img.Hovered()) && !covered && !img.Focused() {
		img.Focus()
	}
}

// inPane reports whether the pointer is in the viewer's pane in the frame
// being built (viewerWindow.pointerIn).
func (v *viewer) inPane() bool {
	w := v.home.Load()
	return w != nil && w.pointerOver(v)
}
