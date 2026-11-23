package gui

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

var (
	kbShiftIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4l-7 7h4v8h6v-8h4z"/></svg>`))
	kbCapsIcon      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l-7 7h4v6h6v-6h4z"/><line x1="7" y1="20" x2="17" y2="20"/></svg>`))
	kbBackspaceIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 6H9l-6 6 6 6h12a1 1 0 0 0 1-1V7a1 1 0 0 0-1-1z"/><path d="M16.5 9.5l-5 5"/><path d="M11.5 9.5l5 5"/></svg>`))
	kbEnterIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 10 4 15 9 20"/><path d="M20 5v6a4 4 0 0 1-4 4H4"/></svg>`))
	kbDismissIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="4" width="20" height="11" rx="2"/><path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M8 12h8"/><path d="M9 18l3 3 3-3"/></svg>`))
	kbArrowLeft     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 12H5"/><path d="m12 19-7-7 7-7"/></svg>`))
	kbArrowUp       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5"/><path d="m5 12 7-7 7 7"/></svg>`))
	kbArrowDown     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14"/><path d="m19 12-7 7-7-7"/></svg>`))
	kbArrowRight    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="m12 5 7 7-7 7"/></svg>`))
)

// Set-1 scan codes used by the mobile on-screen keyboard and hardware key mapping.
const (
	scanEsc         uint32 = 0x01
	scanBackspace   uint32 = 0x0E
	scanTab         uint32 = 0x0F
	scanEnter       uint32 = 0x1C
	scanLCtrl       uint32 = 0x1D
	scanLShift      uint32 = 0x2A
	scanRShift      uint32 = 0x36
	scanLAlt        uint32 = 0x38
	scanSpace       uint32 = 0x39
	scanCaps        uint32 = 0x3A
	scanF1          uint32 = 0x3B
	scanNumLock     uint32 = 0x45
	scanScrollLock  uint32 = 0x46
	scanF11         uint32 = 0x57
	scanF12         uint32 = 0x58
	scanRCtrl       uint32 = 0xE01D
	scanPrintScreen uint32 = 0xE037
	scanRAlt        uint32 = 0xE038
	scanHome        uint32 = 0xE047
	scanUp          uint32 = 0xE048
	scanPageUp      uint32 = 0xE049
	scanLeft        uint32 = 0xE04B
	scanRight       uint32 = 0xE04D
	scanEnd         uint32 = 0xE04F
	scanDown        uint32 = 0xE050
	scanPageDown    uint32 = 0xE051
	scanInsert      uint32 = 0xE052
	scanDelete      uint32 = 0xE053
	scanLWin        uint32 = 0xE05B
	scanRWin        uint32 = 0xE05C
	scanMenu        uint32 = 0xE05D
	scanPause       uint32 = 0xE11D
)

func scanFn(n int) uint32 {
	if n <= 10 {
		return scanF1 + uint32(n-1)
	}
	if n == 11 {
		return scanF11
	}
	return scanF12
}

type usKeyEntry struct {
	code  uint32
	shift bool
}

var usKeyMap = map[string]usKeyEntry{
	"1": {0x02, false}, "2": {0x03, false}, "3": {0x04, false}, "4": {0x05, false},
	"5": {0x06, false}, "6": {0x07, false}, "7": {0x08, false}, "8": {0x09, false},
	"9": {0x0A, false}, "0": {0x0B, false}, "-": {0x0C, false}, "=": {0x0D, false},
	"!": {0x02, true}, "@": {0x03, true}, "#": {0x04, true}, "$": {0x05, true},
	"%": {0x06, true}, "^": {0x07, true}, "&": {0x08, true}, "*": {0x09, true},
	"(": {0x0A, true}, ")": {0x0B, true}, "_": {0x0C, true}, "+": {0x0D, true},
	"q": {0x10, false}, "w": {0x11, false}, "e": {0x12, false}, "r": {0x13, false},
	"t": {0x14, false}, "y": {0x15, false}, "u": {0x16, false}, "i": {0x17, false},
	"o": {0x18, false}, "p": {0x19, false}, "[": {0x1A, false}, "]": {0x1B, false},
	"{": {0x1A, true}, "}": {0x1B, true},
	"a": {0x1E, false}, "s": {0x1F, false}, "d": {0x20, false}, "f": {0x21, false},
	"g": {0x22, false}, "h": {0x23, false}, "j": {0x24, false}, "k": {0x25, false},
	"l": {0x26, false}, ";": {0x27, false}, "'": {0x28, false}, "`": {0x29, false},
	":": {0x27, true}, `"`: {0x28, true}, "~": {0x29, true}, `\`: {0x2B, false}, "|": {0x2B, true},
	"z": {0x2C, false}, "x": {0x2D, false}, "c": {0x2E, false}, "v": {0x2F, false},
	"b": {0x30, false}, "n": {0x31, false}, "m": {0x32, false}, ",": {0x33, false},
	".": {0x34, false}, "/": {0x35, false}, "<": {0x33, true}, ">": {0x34, true}, "?": {0x35, true},
	" ": {scanSpace, false},
}

var usCharByScan = func() map[uint32][2]string {
	plain := map[uint32]string{}
	shifted := map[uint32]string{}
	for ch, e := range usKeyMap {
		if e.shift {
			shifted[e.code] = ch
		} else {
			plain[e.code] = ch
		}
	}
	out := map[uint32][2]string{}
	for code, p := range plain {
		sh := shifted[code]
		if sh == "" {
			sh = strings.ToUpper(p)
		}
		out[code] = [2]string{p, sh}
	}
	return out
}()

var macOptionByScan = func() map[uint32][2]string {
	type row struct{ keys, plain, shifted string }
	rows := []row{
		{"`1234567890-=", "`¡™£¢∞§¶•ªº–≠", "`⁄€‹›ﬁﬂ‡°·‚—±"},
		{`qwertyuiop[]\`, "œ∑´®†¥¨ˆøπ“‘«", "Œ„´‰ˇÁ¨ˆØ∏”’»"},
		{"asdfghjkl;'", "åß∂ƒ©˙∆˚¬…æ", "ÅÍÎÏ˝ÓÔÒÚÆ"},
		{"zxcvbnm,./", "Ω≈ç√∫˜µ≤≥÷", "¸˛Ç◊ı˜Â¯˘¿"},
	}
	out := map[uint32][2]string{}
	for _, r := range rows {
		kr := []rune(r.keys)
		pr := []rune(r.plain)
		sr := []rune(r.shifted)
		for i := range kr {
			if e, ok := usKeyMap[string(kr[i])]; ok {
				out[e.code] = [2]string{string(pr[i]), string(sr[i])}
			}
		}
	}
	return out
}()

var namedKeysyms = map[uint32]uint32{
	scanEsc: 0xFF1B, scanBackspace: 0xFF08, scanTab: 0xFF09, scanEnter: 0xFF0D,
	scanLCtrl: 0xFFE3, scanRCtrl: 0xFFE4, scanLShift: 0xFFE1, scanRShift: 0xFFE2,
	scanLAlt: 0xFFE9, scanRAlt: 0xFFEA, scanLWin: 0xFFEB, scanRWin: 0xFFEC,
	scanMenu: 0xFF67, scanCaps: 0xFFE5, scanNumLock: 0xFF7F, scanScrollLock: 0xFF14,
	scanHome: 0xFF50, scanLeft: 0xFF51, scanUp: 0xFF52, scanRight: 0xFF53,
	scanDown: 0xFF54, scanPageUp: 0xFF55, scanPageDown: 0xFF56, scanEnd: 0xFF57,
	scanInsert: 0xFF63, scanDelete: 0xFFFF, scanPrintScreen: 0xFF61, scanPause: 0xFF13,
	scanSpace: 0x0020,
}

// KeysymFor returns the X11 keysym VNC expects for a PC Set-1 scan code with
// the current Shift state, or 0 when unmapped.
func KeysymFor(code uint32, shift bool) uint32 {
	if code >= scanF1 && code < scanF1+10 {
		return 0xFFBE + code - scanF1
	}
	if code == scanF11 {
		return 0xFFC8
	}
	if code == scanF12 {
		return 0xFFC9
	}
	if sym, ok := namedKeysyms[code]; ok {
		return sym
	}
	if pair, ok := usCharByScan[code]; ok {
		s := pair[0]
		if shift {
			s = pair[1]
		}
		if len(s) > 0 {
			return uint32(s[0])
		}
	}
	return 0
}

// USB HID keyboard usage (page 0x07) -> Set-1 scan code.
var hidToScan = map[uint16]uint32{
	0x04: 0x1E, 0x05: 0x30, 0x06: 0x2E, 0x07: 0x20, 0x08: 0x12, 0x09: 0x21, 0x0A: 0x22, 0x0B: 0x23,
	0x0C: 0x17, 0x0D: 0x24, 0x0E: 0x25, 0x0F: 0x26, 0x10: 0x32, 0x11: 0x31, 0x12: 0x18, 0x13: 0x19,
	0x14: 0x10, 0x15: 0x13, 0x16: 0x1F, 0x17: 0x14, 0x18: 0x16, 0x19: 0x2F, 0x1A: 0x11, 0x1B: 0x2D,
	0x1C: 0x15, 0x1D: 0x2C,
	0x1E: 0x02, 0x1F: 0x03, 0x20: 0x04, 0x21: 0x05, 0x22: 0x06, 0x23: 0x07, 0x24: 0x08, 0x25: 0x09,
	0x26: 0x0A, 0x27: 0x0B,
	0x28: 0x1C, 0x29: 0x01, 0x2A: 0x0E, 0x2B: 0x0F, 0x2C: 0x39, 0x2D: 0x0C, 0x2E: 0x0D, 0x2F: 0x1A,
	0x30: 0x1B, 0x31: 0x2B, 0x32: 0x2B, 0x33: 0x27, 0x34: 0x28, 0x35: 0x29, 0x36: 0x33, 0x37: 0x34,
	0x38: 0x35, 0x39: 0x3A,
	0x3A: 0x3B, 0x3B: 0x3C, 0x3C: 0x3D, 0x3D: 0x3E, 0x3E: 0x3F, 0x3F: 0x40, 0x40: 0x41, 0x41: 0x42,
	0x42: 0x43, 0x43: 0x44, 0x44: 0x57, 0x45: 0x58,
	0x46: 0xE037, 0x47: 0x46, 0x48: 0xE11D, 0x49: 0xE052, 0x4A: 0xE047, 0x4B: 0xE049, 0x4C: 0xE053,
	0x4D: 0xE04F, 0x4E: 0xE051, 0x4F: 0xE04D, 0x50: 0xE04B, 0x51: 0xE050, 0x52: 0xE048,
	0x53: 0x45, 0x54: 0xE035, 0x55: 0x37, 0x56: 0x4A, 0x57: 0x4E, 0x58: 0xE01C, 0x59: 0x4F, 0x5A: 0x50,
	0x5B: 0x51, 0x5C: 0x4B, 0x5D: 0x4C, 0x5E: 0x4D, 0x5F: 0x47, 0x60: 0x48, 0x61: 0x49, 0x62: 0x52,
	0x63: 0x53, 0x64: 0x56, 0x65: 0xE05D,
	0x87: 0x73, 0x88: 0x70, 0x89: 0x7D, 0x8A: 0x79, 0x8B: 0x7B,
	0xE0: 0x1D, 0xE1: 0x2A, 0xE2: 0x38, 0xE3: 0xE05B, 0xE4: 0xE01D, 0xE5: 0x36, 0xE6: 0xE038, 0xE7: 0xE05C,
}

// ScanFromKeyEvent maps a MyGo hardware key event (USB HID usage when present,
// else ui.Key fallback) to a PC Set-1 scan code.
func ScanFromKeyEvent(ev ui.InputEvent) (uint32, bool) {
	if ev.Code != 0 {
		if sc, ok := hidToScan[uint16(ev.Code)]; ok {
			return sc, true
		}
	}
	if name, ok := keyNames[ev.Key]; ok {
		if rk, ok := remoteKeysByName[name]; ok {
			return rk.scan, true
		}
	}
	return 0, false
}

// NormalizeHostOS resolves the effective host OS ("windows", "macos", "linux")
// from an optional user override and the peer's reported OS string.
func NormalizeHostOS(override, reported string, nanokvm bool) string {
	if override == "windows" || override == "macos" || override == "linux" {
		return override
	}
	if nanokvm {
		return "windows"
	}
	s := strings.ToLower(reported)
	switch {
	case strings.Contains(s, "mac") || strings.Contains(s, "darwin") || strings.Contains(s, "os x"):
		return "macos"
	case strings.Contains(s, "linux") || strings.Contains(s, "ubuntu") || strings.Contains(s, "debian") || strings.Contains(s, "fedora"):
		return "linux"
	case strings.Contains(s, "win"):
		return "windows"
	case reported == "":
		return "linux"
	default:
		return "windows"
	}
}

type kbMod int

const (
	kbShift kbMod = iota
	kbCtrl
	kbAlt
	kbWin
)

func (m kbMod) scan() uint32 {
	switch m {
	case kbShift:
		return scanLShift
	case kbCtrl:
		return scanLCtrl
	case kbAlt:
		return scanLAlt
	default:
		return scanLWin
	}
}

type kbLatch int

const (
	kbOff kbLatch = iota
	kbOnce
	kbLocked
)

type kbPage int

const (
	kbPageLetters kbPage = iota
	kbPageSymbols
	kbPageMore
	kbPageFn
)

// MobileKeyboard holds the state of the on-screen computer keyboard for
// Kyokai, NanoKVM, RDP and VNC viewers.
type MobileKeyboard struct {
	page         kbPage
	latch        [4]kbLatch
	held         [4]bool
	hostHeld     [4]bool
	caps         bool
	lastTap      [4]time.Time
	pressed      string
	downKeys     map[string]bool
	seenDown     map[string]bool
	repeatKey    string
	repeatCount  int
	repeatCancel context.CancelFunc
	popupKey     string
	popupChar    string
	popupRect    ui.Rect
	pad          *Touchpad
	padRect      ui.Rect
	hwDown       map[uint32]bool
}

// NewMobileKeyboard creates a fresh mobile keyboard state.
func NewMobileKeyboard(padSink TouchpadSink, pointerSpeed float32) *MobileKeyboard {
	kb := &MobileKeyboard{
		downKeys: map[string]bool{},
		seenDown: map[string]bool{},
		hwDown:   map[uint32]bool{},
	}
	if padSink != nil {
		kb.pad = NewTouchpad(padSink, false, pointerSpeed)
	}
	return kb
}

// SetSink updates the split-keyboard center touchpad sink and speed.
func (kb *MobileKeyboard) SetSink(sink TouchpadSink, speed float32) {
	if kb.pad != nil {
		kb.pad.SetSink(sink)
		kb.pad.SetSpeed(speed)
	}
}

// ShiftActive reports whether Shift is currently latched or held.
func (kb *MobileKeyboard) ShiftActive() bool {
	return kb.latch[kbShift] != kbOff || kb.held[kbShift]
}

func (kb *MobileKeyboard) active(m kbMod) bool {
	return kb.latch[m] != kbOff || kb.held[m]
}

// stopRepeat cancels any active long-press key repeat loop and resets repeat counters.
func (kb *MobileKeyboard) stopRepeat() {
	if kb.repeatCancel != nil {
		kb.repeatCancel()
		kb.repeatCancel = nil
	}
	kb.repeatKey = ""
	kb.repeatCount = 0
}

// startRepeat schedules repeating key presses (380ms initial delay, 55ms cadence)
// while key kID remains held down.
// Side effects: launches a cancellable goroutine invoking Dispatch onto the UI thread.
func (kb *MobileKeyboard) startRepeat(kID string, sp kbKeySpec, o MobileKeyboardOptions) {
	if o.Dispatch == nil || o.Send == nil {
		return
	}
	kb.stopRepeat()
	ctx, cancel := context.WithCancel(context.Background())
	kb.repeatKey = kID
	kb.repeatCancel = cancel
	go func() {
		timer := time.NewTimer(380 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		tick := time.NewTicker(55 * time.Millisecond)
		defer tick.Stop()
		for {
			o.Dispatch(func() {
				if ctx.Err() != nil {
					return
				}
				kb.repeatCount++
				mygo.Haptics.Selection()
				kb.activate(sp, o)
			})
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// ReleaseAll releases any modifier or hardware keys held down on the host.
func (kb *MobileKeyboard) ReleaseAll(send func(code uint32, down, caps bool)) {
	kb.stopRepeat()
	clear(kb.downKeys)
	kb.popupKey = ""
	kb.popupChar = ""
	kb.popupRect = ui.Rect{}
	for m := range 4 {
		if kb.hostHeld[m] {
			send(kbMod(m).scan(), false, kb.caps)
			kb.hostHeld[m] = false
		}
		kb.held[m] = false
		kb.latch[m] = kbOff
	}
	for code := range kb.hwDown {
		send(code, false, kb.caps)
	}
	clear(kb.hwDown)
	if kb.pad != nil {
		kb.pad.Reset()
	}
}

// HandleHardwareKey translates a physical Bluetooth/USB keyboard event into
// Set-1 scan codes; returns false for Android system keys (Back, volume).
func (kb *MobileKeyboard) HandleHardwareKey(ev ui.InputEvent, send func(code uint32, down, caps bool)) bool {
	if ev.Key == ui.KeyBack {
		return false
	}
	code, ok := ScanFromKeyEvent(ev)
	if !ok {
		return false
	}
	down := ev.Kind == ui.InputKeyDown
	if down {
		kb.hwDown[code] = true
	} else {
		delete(kb.hwDown, code)
	}
	send(code, down, kb.caps)
	return true
}

func (kb *MobileKeyboard) tapModifier(m kbMod, _ string, _ func(code uint32, down, caps bool)) {
	now := time.Now()
	twice := !kb.lastTap[m].IsZero() && now.Sub(kb.lastTap[m]) < 350*time.Millisecond
	kb.lastTap[m] = now
	if m == kbShift && (twice || kb.caps) {
		kb.latch[m] = kbOff
		kb.caps = !kb.caps
		return
	}
	switch kb.latch[m] {
	case kbOff:
		kb.latch[m] = kbOnce
	case kbOnce:
		if twice {
			kb.latch[m] = kbLocked
		} else {
			kb.latch[m] = kbOff
		}
	case kbLocked:
		kb.latch[m] = kbOff
	}
}

func (kb *MobileKeyboard) tapCode(code uint32, needShift bool, send func(code uint32, down, caps bool)) {
	if send == nil {
		return
	}
	for m := range 4 {
		if kb.held[m] && !kb.hostHeld[m] {
			send(kbMod(m).scan(), true, kb.caps)
			kb.hostHeld[m] = true
		}
	}
	var latched []kbMod
	for m := range 4 {
		if kb.latch[m] != kbOff && !kb.held[m] {
			latched = append(latched, kbMod(m))
			send(kbMod(m).scan(), true, kb.caps)
		}
	}
	extraShift := needShift && !kb.active(kbShift)
	if extraShift {
		send(scanLShift, true, kb.caps)
	}
	send(code, true, kb.caps)
	send(code, false, kb.caps)
	if extraShift {
		send(scanLShift, false, kb.caps)
	}
	for i := len(latched) - 1; i >= 0; i-- {
		send(latched[i].scan(), false, kb.caps)
	}
	for m := range 4 {
		if kb.latch[m] == kbOnce {
			kb.latch[m] = kbOff
		}
	}
}

func (kb *MobileKeyboard) shownChar(ch string, hostOS string) string {
	if ch == " " || ch == "" {
		return ch
	}
	e, ok := usKeyMap[strings.ToLower(ch)]
	if !ok {
		e, ok = usKeyMap[ch]
		if !ok {
			return ch
		}
	}
	shift := kb.active(kbShift)
	if hostOS == "macos" && kb.active(kbAlt) && !kb.active(kbCtrl) && !kb.active(kbWin) {
		if opt, ok := macOptionByScan[e.code]; ok {
			if e.shift || shift {
				return opt[1]
			}
			return opt[0]
		}
	}
	r := []rune(ch)
	if len(r) == 1 && unicode.IsLetter(r[0]) {
		if kb.caps != shift {
			return strings.ToUpper(ch)
		}
		return strings.ToLower(ch)
	}
	if e.shift || shift {
		if pair, ok := usCharByScan[e.code]; ok {
			return pair[1]
		}
	}
	return ch
}

// shiftedLegend returns the secondary Shift legend glyph for non-letter US keys
// so wide and laptop keys can hint punctuation such as ! over 1.
func shiftedLegend(ch string) string {
	e, ok := usKeyMap[ch]
	if !ok || e.shift {
		return ""
	}
	r := []rune(ch)
	if len(r) == 1 && unicode.IsLetter(r[0]) {
		return ""
	}
	if pair, ok := usCharByScan[e.code]; ok && pair[1] != ch {
		return pair[1]
	}
	return ""
}

type kbKeyKind int

const (
	kbKeyChar kbKeyKind = iota
	kbKeyCode
	kbKeyMod
	kbKeyPage
	kbKeyCombo
	kbKeyHide
	kbKeyGap
)

type kbKeySpec struct {
	label    string
	secLabel string
	icon     *ui.SVG
	iconSize float32
	kind     kbKeyKind
	char     string
	code     uint32
	mod      kbMod
	page     kbPage
	mods     []uint32
	special  string
	flex     float32
	tonal    bool
}

func kChar(c string, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 10
	}
	return kbKeySpec{label: c, secLabel: shiftedLegend(c), kind: kbKeyChar, char: c, flex: flex}
}

func kCode(label string, code uint32, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 10
	}
	return kbKeySpec{label: label, kind: kbKeyCode, code: code, flex: flex, tonal: true}
}

func kCodeIcon(label string, icon *ui.SVG, code uint32, flex float32) kbKeySpec {
	sp := kCode(label, code, flex)
	sp.icon = icon
	return sp
}

func kMod(label string, m kbMod, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 10
	}
	return kbKeySpec{label: label, kind: kbKeyMod, mod: m, flex: flex, tonal: true}
}

func kPage(label string, p kbPage, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 15
	}
	return kbKeySpec{label: label, kind: kbKeyPage, page: p, flex: flex, tonal: true}
}

func isRepeatableKey(sp kbKeySpec) bool {
	if sp.kind == kbKeyChar && sp.char == " " {
		return true
	}
	if sp.kind == kbKeyCode {
		switch sp.code {
		case scanBackspace, scanLeft, scanRight, scanUp, scanDown:
			return true
		}
	}
	return false
}

// MobileKeyboardOptions configures one render pass of the on-screen keyboard.
type MobileKeyboardOptions struct {
	TargetLabel string
	HostOS      string // "windows", "macos", "linux"
	OSPicked    bool
	AutoLabel   string
	Split       bool
	Laptop      bool
	DeckHeight  float32
	UseSafeArea bool
	NativeTouch bool
	Colors      *mobileColors
	Dispatch    func(func())
	OnOSPick    func(choice string) // "" auto, "windows", "macos", "linux"
	OnSplit     func(split bool)
	OnHide      func()
	OnSpecial   func(action string) bool
	OnTypeText  func()
	Send        func(code uint32, down, caps bool)
}

// mobileKeyboardEstimatedHeight returns an approximate dock height for auto-open
// blank-space planning without building a frame.
func mobileKeyboardEstimatedHeight(width float32, split bool) float32 {
	if width >= 600 && !split {
		return 370
	}
	return 320
}

// View renders the responsive Material 3 mobile computer keyboard or, when
// o.Laptop is true, the foldable Laptop Mode keyboard-and-touchpad deck.
// Side effects: updates internal press/repeat state and padRect bounds.
func (kb *MobileKeyboard) View(c *ui.Context, keyPrefix string, width float32, o MobileKeyboardOptions) {
	var mc mobileColors
	if o.Colors != nil {
		mc = *o.Colors
	} else {
		mc = resolveMobileColors(c.Theme().Dark, androidhost.MaterialPalette{})
	}
	clear(kb.seenDown)
	defer kb.pruneDownState()

	if o.Laptop {
		kb.renderLaptopDeck(c, mc, keyPrefix, width, o)
		return
	}

	wide := width >= 600
	split := wide && o.Split
	mac := o.HostOS == "macos"
	_, _, safeBottom, _ := c.SafeArea()
	kbBot := safeBottom
	if kbBot > mobileKeyboardThreshold {
		kbBot = 0
	}
	// Guarantee generous Android edge-to-edge gesture clearance beneath
	// Space/Enter even when the system gesture hint pill is hidden.
	chinPad := max(kbBot, 16)

	ui.Column(c.Key(keyPrefix)).FillWidth().Shrink(0).Background(mc.KbTray).
		BorderWidth(1, 0, 0, 0).BorderColor(mc.OutlineVariant.Alpha(0.35)).
		Padding(6, 6, chinPad, 6).Margin(0, 0, -kbBot, 0).Gap(4).Children(func() {
		// Top utility strip tailored to layout (no duplicate Ctrl/Alt/Tab in Full mode).
		strip := kb.stripSpecs(o.HostOS, wide && !split)
		kb.renderRow(c, mc, keyPrefix+"-strip", strip, 36, true, wide, o)

		rowH := float32(46)
		if wide && !split {
			rowH = 48
		}
		if split {
			leftRows, rightRows := kb.splitRows(o.HostOS)
			halfW := min(max(width*0.35, 176), 368)
			bodyH := rowH*float32(len(leftRows)) + 3*float32(max(len(leftRows)-1, 0))
			ui.Row(c.Key(keyPrefix + "-split-body")).FillWidth().AlignItems(ui.End).Gap(6).Children(func() {
				ui.Column(c.Key(keyPrefix + "-left")).Width(halfW).Gap(3).Children(func() {
					for i, r := range leftRows {
						kb.renderRow(c, mc, keyPrefix+"-l"+strconv.Itoa(i), r, rowH, false, false, o)
					}
				})
				kb.renderCenterPad(c, mc, keyPrefix+"-pad", bodyH, o)
				ui.Column(c.Key(keyPrefix + "-right")).Width(halfW).Gap(3).Children(func() {
					for i, r := range rightRows {
						kb.renderRow(c, mc, keyPrefix+"-r"+strconv.Itoa(i), r, rowH, false, false, o)
					}
				})
			})
		} else {
			kb.padRect = ui.Rect{}
			var rows [][]kbKeySpec
			if wide {
				rows = kb.fullRows(o.HostOS, mac)
			} else {
				rows = kb.phoneRows(o.HostOS)
			}
			ui.Column(c.Key(keyPrefix + "-rows")).FillWidth().MaxWidth(1080).AlignSelf(ui.Center).Gap(4).Children(func() {
				for i, r := range rows {
					kb.renderRow(c, mc, keyPrefix+"-row"+strconv.Itoa(i), r, rowH, false, wide, o)
				}
			})
		}

		// Bottom Material gesture chin holding dismiss chevron, host OS badge, Aa & Full/Split.
		kb.renderBottomChin(c, mc, keyPrefix, wide, o)
	})

	// Compact-phone tactile key preview bubble above the active keycap.
	if !wide && kb.popupKey != "" && kb.popupChar != "" && kb.popupRect.W > 0 {
		px := max(kb.popupRect.X+(kb.popupRect.W-46)/2, 4)
		if width > 50 && px+46 > width-4 {
			px = width - 50
		}
		py := max(kb.popupRect.Y-56, 4)
		ui.Overlay(c, func() {
			ui.Box(c.Key(keyPrefix+"-key-popup")).Absolute().Left(px).Top(py).Size(46, 50).
				Radius(14).Background(mc.SurfaceContainerHighest).
				Border(1, mc.OutlineVariant.Alpha(0.5)).
				Shadow(0, 6, 16, 0, mc.KbKeyShadow).Center().Children(func() {
				ui.Text(c, kb.popupChar).FontSize(22).FontWeight(700).TextColor(mc.OnSurface).NoWrap()
			})
		})
	}
}

func (kb *MobileKeyboard) pruneDownState() {
	for k := range kb.downKeys {
		if !kb.seenDown[k] {
			delete(kb.downKeys, k)
		}
	}
	if kb.repeatKey != "" && !kb.seenDown[kb.repeatKey] {
		kb.stopRepeat()
	}
	if kb.popupKey != "" && !kb.seenDown[kb.popupKey] {
		kb.popupKey = ""
		kb.popupChar = ""
	}
}

func (kb *MobileKeyboard) renderBottomChin(c *ui.Context, mc mobileColors, keyPrefix string, wide bool, o MobileKeyboardOptions) {
	ui.Row(c.Key(keyPrefix + "-chin")).Height(38).FillWidth().AlignItems(ui.Center).Gap(8).PaddingX(4).Children(func() {
		hide := ui.ButtonBase(c.Key(keyPrefix+"-hide")).Size(38, 34).Radius(17).
			Background(mc.KbFuncKey).Center().Clip().Label("Hide keyboard")
		mobile.Ink(hide)
		hide.Children(func() {
			ui.Icon(c, kbDismissIcon).FontSize(19).TextColor(mc.OnSurfaceVariant)
		})
		if hide.Clicked() {
			mygo.Haptics.Light()
			kb.ReleaseAll(o.Send)
			if o.OnHide != nil {
				o.OnHide()
			}
		}

		kb.renderOSBadge(c, mc, keyPrefix+"-os", o)
		ui.Box(c).Grow(1)
		kb.renderTypeTextBtn(c, mc, keyPrefix+"-typetext", o)

		if wide && o.OnSplit != nil {
			kb.renderSplitSwitch(c, mc, keyPrefix+"-sp", o)
		}
	})
}

func (kb *MobileKeyboard) renderLaptopDeck(c *ui.Context, mc mobileColors, keyPrefix string, width float32, o MobileKeyboardOptions) {
	_, _, safeBot, _ := c.SafeArea()
	if safeBot > mobileKeyboardThreshold {
		safeBot = 0
	}
	deckH := o.DeckHeight
	if deckH <= 0 {
		deckH = 420
	}
	padBot := float32(6)
	if o.UseSafeArea {
		padBot = max(safeBot, 8)
	}
	innerH := max(deckH-10-padBot, 220)
	stripH := float32(32)
	rowsAvail := max(innerH-stripH-8, 170)
	kbZoneH := min(max(rowsAvail*0.62, 160), rowsAvail-98)
	rowH := min(max((kbZoneH-12)/5, 30), 50)
	padH := max(rowsAvail-kbZoneH-4, 96)

	ui.Column(c.Key(keyPrefix+"-laptop")).FillWidth().Height(deckH).Shrink(0).Background(mc.KbTray).
		BorderWidth(1, 0, 0, 0).BorderColor(mc.OutlineVariant.Alpha(0.35)).
		Padding(4, 10, padBot, 10).Gap(4).Children(func() {
		// Compact laptop deck top utility strip.
		ui.Row(c.Key(keyPrefix + "-lap-bar")).Height(stripH).FillWidth().MaxWidth(1040).AlignSelf(ui.Center).
			AlignItems(ui.Center).Gap(6).Children(func() {
			kb.renderOSBadge(c, mc, keyPrefix+"-os", o)
			strip := kb.laptopStripSpecs()
			ui.Row(c.Key(keyPrefix + "-lap-strip")).Grow(1).Basis(0).MinWidth(0).Children(func() {
				kb.renderRow(c, mc, keyPrefix+"-lstrip", strip, stripH, true, true, o)
			})
			kb.renderTypeTextBtn(c, mc, keyPrefix+"-typetext", o)
		})

		// Top 62%: full horizontal 5-row laptop ANSI keyboard.
		rows := kb.laptopRows(o.HostOS)
		ui.Column(c.Key(keyPrefix + "-lap-rows")).FillWidth().MaxWidth(1040).AlignSelf(ui.Center).Gap(3).Children(func() {
			for i, r := range rows {
				kb.renderRow(c, mc, keyPrefix+"-lrow"+strconv.Itoa(i), r, rowH, false, true, o)
			}
		})

		// Bottom 38%: wide centered palmrest touchpad + integrated Left/Right click bar.
		padW := min(max(width*0.66, 280), 540)
		ui.Column(c.Key(keyPrefix+"-lap-pad")).Width(padW).Height(padH).AlignSelf(ui.Center).
			Gap(6).Padding(2, 0, 0, 0).Children(func() {
			pad := ui.Box(c.Key(keyPrefix+"-lap-pad-area")).Grow(1).Basis(0).MinHeight(52).FillWidth().
				Radius(18).Background(mc.SurfaceContainerLow).
				Border(1, mc.OutlineVariant.Alpha(0.55)).Center()
			kb.bindTouchpadBox(pad, o)
			pad.Children(func() {
				ui.Text(c, L.MobileTouchpad()).FontSize(12).FontWeight(500).
					TextColor(mc.OnSurfaceVariant.Alpha(0.75))
			})
			kb.renderPadClickButtons(c, mc, keyPrefix+"-lap-pbtns", 36)
		})
	})
}

func (kb *MobileKeyboard) renderOSBadge(c *ui.Context, mc mobileColors, key string, o MobileKeyboardOptions) {
	osName := map[string]string{"windows": "Windows", "macos": "macOS", "linux": "Linux"}[o.HostOS]
	if osName == "" {
		osName = "Windows"
	}
	badgeText := o.TargetLabel + " · " + osName
	if !o.OSPicked && o.AutoLabel != "" {
		badgeText += " (" + o.AutoLabel + ")"
	}
	badge := ui.ButtonBase(c.Key(key)).Height(30).PaddingX(12).Radius(15).
		Background(mc.SecondaryContainer).Center().Clip().Label(badgeText)
	mobile.Ink(badge)
	badge.Children(func() {
		ui.Text(c, badgeText).FontSize(12).FontWeight(600).TextColor(mc.OnSecondaryContainer).NoWrap()
	})
	if badge.Clicked() && o.OnOSPick != nil {
		mygo.Haptics.Light()
		kb.ReleaseAll(o.Send)
		order := []string{"", "windows", "macos", "linux"}
		cur := ""
		if o.OSPicked {
			cur = o.HostOS
		}
		next := order[0]
		for i, v := range order {
			if v == cur {
				next = order[(i+1)%len(order)]
				break
			}
		}
		o.OnOSPick(next)
	}
}

func (kb *MobileKeyboard) renderTypeTextBtn(c *ui.Context, mc mobileColors, key string, o MobileKeyboardOptions) {
	if o.OnTypeText == nil {
		return
	}
	tb := ui.ButtonBase(c.Key(key)).Height(30).PaddingX(12).Radius(15).
		Background(mc.KbFuncKey).Border(1, mc.OutlineVariant.Alpha(0.3)).Center().Clip().Label("Aa")
	mobile.Ink(tb)
	tb.Children(func() {
		ui.Text(c, "Aa").FontSize(12).FontWeight(600).TextColor(mc.OnSurface)
	})
	if tb.Clicked() {
		mygo.Haptics.Light()
		o.OnTypeText()
	}
}

func (kb *MobileKeyboard) renderSplitSwitch(c *ui.Context, mc mobileColors, keyPrefix string, o MobileKeyboardOptions) {
	idx := 0
	if o.Split {
		idx = 1
	}
	parts := ui.SegmentedBase(c.Key(keyPrefix), &idx, 2)
	track := parts.Track.Height(30).Radius(15).Background(mc.KbFuncKey).
		Border(1, mc.OutlineVariant.Alpha(0.35)).Clip()
	labels := [2]string{L.MobileKeyboardFull(), L.MobileKeyboardSplit()}
	track.Children(func() {
		for i, lbl := range labels {
			on := i == idx
			seg := parts.Segment(i).Height(30).PaddingX(12).Center().Clip()
			fg := mc.OnSurfaceVariant
			if on {
				seg.Background(mc.PrimaryContainer)
				fg = mc.OnPrimaryContainer
			}
			mobile.Ink(seg)
			seg.Children(func() {
				ui.Text(c, lbl).FontSize(12).FontWeight(600).TextColor(fg).SingleLine()
			})
		}
	})
	if track.Changed() {
		mygo.Haptics.Light()
		o.OnSplit(idx == 1)
	}
}

func (kb *MobileKeyboard) bindTouchpadBox(pad ui.Element, o MobileKeyboardOptions) {
	kb.padRect = pad.LayoutBounds()
	if kb.pad == nil {
		return
	}
	for _, te := range pad.Touches() {
		now := time.Now()
		switch te.Phase {
		case ui.TouchBegan:
			kb.pad.Down(int(te.ID), te.X, te.Y, now)
		case ui.TouchMoved:
			kb.pad.Move(int(te.ID), te.X, te.Y, now)
		case ui.TouchEnded:
			kb.pad.Up(int(te.ID), now)
		case ui.TouchCanceled:
			kb.pad.Cancel(int(te.ID))
		}
	}
	// When Android's native TouchBridge is absent (e.g. unit tests or
	// desktop view previews), fall back to HandleInput; on Android,
	// TouchBridge owns raw multi-touch over padRect so local-vs-window
	// coordinate mismatches never double-feed kb.pad.
	if !o.NativeTouch {
		pad.HandleInput(func(ev ui.InputEvent) bool {
			now := time.Now()
			switch ev.Kind {
			case ui.InputPointerDown:
				kb.pad.Down(0, ev.X, ev.Y, now)
				return true
			case ui.InputPointerMove:
				kb.pad.Move(0, ev.X, ev.Y, now)
				return true
			case ui.InputPointerUp:
				kb.pad.Up(0, now)
				return true
			}
			return false
		})
	}
}

func (kb *MobileKeyboard) renderPadClickButtons(c *ui.Context, mc mobileColors, key string, height float32) {
	ui.Row(c.Key(key)).Height(height).FillWidth().Gap(6).Children(func() {
		for _, b := range []struct {
			label string
			btn   int
		}{{"Left", 0}, {"Right", 1}} {
			el := ui.ButtonBase(c.Key(key+"-b"+strconv.Itoa(b.btn))).Grow(1).Basis(0).FillHeight().
				Radius(12).Background(mc.KbFuncKey).Border(1, mc.OutlineVariant.Alpha(0.35)).
				Center().Clip().Label(b.label)
			mobile.Ink(el)
			el.Children(func() {
				ui.Text(c, b.label).FontSize(12).FontWeight(600).TextColor(mc.OnSurfaceVariant)
			})
			if el.Clicked() && kb.pad != nil {
				mygo.Haptics.Light()
				kb.pad.sink.Button(b.btn, true)
				kb.pad.sink.Button(b.btn, false)
			}
		}
	})
}

func (kb *MobileKeyboard) renderCenterPad(c *ui.Context, mc mobileColors, key string, height float32, o MobileKeyboardOptions) {
	ui.Column(c.Key(key)).Grow(1).Basis(0).Height(height).Padding(2, 4).Gap(6).Children(func() {
		pad := ui.Box(c.Key(key+"-area")).Grow(1).FillWidth().Radius(16).
			Border(1, mc.OutlineVariant.Alpha(0.45)).Background(mc.SurfaceContainerLow).Center()
		kb.bindTouchpadBox(pad, o)
		pad.Children(func() {
			ui.Text(c, L.MobileTouchpad()).FontSize(12).FontWeight(500).TextColor(mc.OnSurfaceVariant.Alpha(0.75))
		})
		kb.renderPadClickButtons(c, mc, key+"-btns", 38)
	})
}

func (kb *MobileKeyboard) renderRow(c *ui.Context, mc mobileColors, rowKey string, specs []kbKeySpec, height float32, strip, wideHint bool, o MobileKeyboardOptions) {
	ui.Row(c.Key(rowKey)).FillWidth().Height(height).Gap(4).Children(func() {
		for i, sp := range specs {
			kID := rowKey + "-" + strconv.Itoa(i)
			if sp.kind == kbKeyGap {
				ui.Box(c.Key(kID)).Grow(sp.flex).Basis(0).FillHeight()
				continue
			}
			lit := false
			latched := false
			locked := false
			switch sp.kind {
			case kbKeyMod:
				lit = kb.active(sp.mod)
				latched = kb.latch[sp.mod] == kbOnce && !kb.held[sp.mod]
				if sp.mod == kbShift {
					locked = kb.caps || kb.latch[sp.mod] == kbLocked || kb.held[sp.mod]
				} else {
					locked = kb.latch[sp.mod] == kbLocked || kb.held[sp.mod]
				}
			case kbKeyPage:
				if strip && sp.label == "Fn" {
					lit = kb.page == kbPageFn
					locked = lit
				}
			}

			isEnter := sp.kind == kbKeyCode && sp.code == scanEnter
			radius := float32(9)
			if strip {
				radius = 10
			} else if isEnter {
				radius = 16
			}
			label := sp.label
			if sp.kind == kbKeyChar {
				label = kb.shownChar(sp.char, o.HostOS)
			}
			a11y := label
			if (a11y == "" || a11y == " ") && sp.kind == kbKeyChar && sp.char == " " {
				a11y = "Space"
			}
			icon := sp.icon
			if sp.kind == kbKeyMod && sp.mod == kbShift && icon != nil && locked {
				icon = kbCapsIcon
			}

			btn := ui.ButtonBase(c.Key(kID))
			wasPressed := kb.downKeys[kID]
			nowPressed := btn.Pressed()
			repeated := kb.repeatKey == kID && kb.repeatCount > 0
			if nowPressed {
				kb.seenDown[kID] = true
				kb.downKeys[kID] = true
				if !wasPressed {
					if sp.tonal || strip {
						mygo.Haptics.Light()
					} else {
						mygo.Haptics.Selection()
					}
					if isRepeatableKey(sp) {
						kb.startRepeat(kID, sp, o)
					}
				}
				if !wideHint && sp.kind == kbKeyChar && sp.char != " " {
					kb.popupKey = kID
					kb.popupChar = label
					kb.popupRect = btn.LayoutBounds()
				}
			} else {
				delete(kb.downKeys, kID)
				if kb.repeatKey == kID {
					kb.stopRepeat()
				}
				if kb.popupKey == kID {
					kb.popupKey = ""
					kb.popupChar = ""
				}
			}

			bg := mc.KbKey
			fg := mc.OnSurface
			if sp.tonal || strip {
				bg = mc.KbFuncKey
				fg = mc.OnSurfaceVariant
				if lit {
					fg = mc.OnPrimaryContainer
				}
			}
			switch {
			case isEnter:
				bg = mc.Primary
				fg = mc.OnPrimary
			case locked || (sp.kind == kbKeyMod && kb.held[sp.mod]):
				bg = mc.Primary
				fg = mc.OnPrimary
			case lit || latched:
				bg = mc.PrimaryContainer
				fg = mc.OnPrimaryContainer
			}
			if nowPressed {
				if isEnter || locked {
					bg = bg.Mix(mc.OnPrimary, 0.16)
				} else if sp.tonal || strip {
					bg = mc.KbFuncPressed
				} else {
					bg = mc.KbKeyPressed
				}
			}

			btn.Grow(sp.flex).Basis(0).MinWidth(0).FillHeight().Radius(radius).
				Background(bg).Border(1, mc.OutlineVariant.Alpha(0.26)).Center().Clip().Label(a11y)
			if nowPressed {
				btn.Shadow(0, 0.5, 1, 0, mc.KbKeyShadow)
			} else if !strip {
				btn.Shadow(0, 1.5, 2.5, 0, mc.KbKeyShadow)
			}
			mobile.Ink(btn)

			// Multi-touch modifier hold when Element.Touches() fires.
			for _, te := range btn.Touches() {
				if sp.kind == kbKeyMod {
					switch te.Phase {
					case ui.TouchBegan:
						kb.held[sp.mod] = true
					case ui.TouchEnded, ui.TouchCanceled:
						kb.held[sp.mod] = false
						if kb.hostHeld[sp.mod] {
							kb.hostHeld[sp.mod] = false
							o.Send(sp.mod.scan(), false, kb.caps)
						}
					}
				}
			}

			btn.Children(func() {
				if icon != nil {
					sz := sp.iconSize
					if sz <= 0 {
						sz = 18
					}
					col := ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(2)
					col.Children(func() {
						ui.Icon(c, icon).FontSize(sz).TextColor(fg)
						if locked {
							ui.Box(c).Width(14).Height(2.5).Radius(1.25).Background(fg)
						}
					})
					return
				}
				if wideHint && !strip && sp.secLabel != "" && !kb.active(kbShift) {
					ui.Text(c, sp.secLabel).Absolute().Top(3).Right(5).FontSize(10).
						FontWeight(500).TextColor(fg.Alpha(0.58)).NoWrap()
				}
				fontSize := float32(13)
				if sp.kind == kbKeyChar && len([]rune(label)) == 1 {
					fontSize = 17
					if wideHint {
						fontSize = 15
					}
				} else if strip {
					fontSize = 11.5
				}
				col := ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(1)
				col.Children(func() {
					ui.Text(c, label).FontSize(fontSize).FontWeight(500).TextColor(fg).NoWrap()
					if locked {
						ui.Box(c).Width(14).Height(2.5).Radius(1.25).Background(fg)
					}
				})
			})
			if btn.Clicked() {
				if !wasPressed && !nowPressed {
					if sp.tonal || strip {
						mygo.Haptics.Light()
					} else {
						mygo.Haptics.Selection()
					}
				}
				if !repeated {
					kb.activate(sp, o)
				}
			}
		}
	})
}

func (kb *MobileKeyboard) activate(sp kbKeySpec, o MobileKeyboardOptions) {
	switch sp.kind {
	case kbKeyMod:
		if sp.mod == kbWin && o.HostOS != "macos" {
			now := time.Now()
			twice := !kb.lastTap[kbWin].IsZero() && now.Sub(kb.lastTap[kbWin]) < 350*time.Millisecond
			if kb.latch[kbWin] == kbOnce && !twice {
				kb.lastTap[kbWin] = now
				kb.latch[kbWin] = kbOff
				if o.Send != nil {
					o.Send(scanLWin, true, kb.caps)
					o.Send(scanLWin, false, kb.caps)
				}
				return
			}
		}
		kb.tapModifier(sp.mod, o.HostOS, o.Send)
	case kbKeyPage:
		kb.page = sp.page
	case kbKeyHide:
		kb.ReleaseAll(o.Send)
		if o.OnHide != nil {
			o.OnHide()
		}
	case kbKeyCombo:
		if sp.special != "" && o.OnSpecial != nil && o.OnSpecial(sp.special) {
			return
		}
		if o.Send == nil {
			return
		}
		for _, m := range sp.mods {
			o.Send(m, true, kb.caps)
		}
		if sp.code != 0 {
			o.Send(sp.code, true, kb.caps)
			o.Send(sp.code, false, kb.caps)
		}
		for i := len(sp.mods) - 1; i >= 0; i-- {
			o.Send(sp.mods[i], false, kb.caps)
		}
	case kbKeyCode:
		kb.tapCode(sp.code, false, o.Send)
	case kbKeyChar:
		e, ok := usKeyMap[strings.ToLower(sp.char)]
		if !ok {
			e, ok = usKeyMap[sp.char]
		}
		if !ok {
			return
		}
		kb.tapCode(e.code, e.shift, o.Send)
		if (kb.page == kbPageSymbols || kb.page == kbPageMore) && sp.char == " " {
			kb.page = kbPageLetters
		}
	}
}

func (kb *MobileKeyboard) modLabel(m kbMod, hostOS string) string {
	switch m {
	case kbShift:
		return "Shift"
	case kbCtrl:
		if hostOS == "macos" {
			return "⌃Ctrl"
		}
		return "Ctrl"
	case kbAlt:
		if hostOS == "macos" {
			return "⌥Opt"
		}
		return "Alt"
	default:
		switch hostOS {
		case "macos":
			return "⌘Cmd"
		case "linux":
			return "Super"
		default:
			return "Win"
		}
	}
}

func (kb *MobileKeyboard) modOrder(hostOS string) []kbMod {
	if hostOS == "macos" {
		return []kbMod{kbCtrl, kbAlt, kbWin}
	}
	return []kbMod{kbCtrl, kbWin, kbAlt}
}

func (kb *MobileKeyboard) stripSpecs(hostOS string, fullANSI bool) []kbKeySpec {
	nextFn := kbPageFn
	if kb.page == kbPageFn {
		nextFn = kbPageLetters
	}
	if fullANSI {
		return []kbKeySpec{
			kCode("Esc", scanEsc, 10),
			kCode("Ins", scanInsert, 10),
			kCode("Home", scanHome, 10),
			kCode("PgUp", scanPageUp, 10),
			kCode("PgDn", scanPageDown, 10),
			kCode("End", scanEnd, 10),
			kCode("Del", scanDelete, 10),
			kCodeIcon("Left", kbArrowLeft, scanLeft, 9),
			kCodeIcon("Up", kbArrowUp, scanUp, 9),
			kCodeIcon("Down", kbArrowDown, scanDown, 9),
			kCodeIcon("Right", kbArrowRight, scanRight, 9),
			kPage("Fn", nextFn, 10),
		}
	}
	mac := hostOS == "macos"
	flex := float32(10)
	if mac {
		flex = 13
	}
	out := []kbKeySpec{
		kCode("Esc", scanEsc, 10),
		kCode("Tab", scanTab, 10),
	}
	for _, m := range kb.modOrder(hostOS) {
		out = append(out, kMod(kb.modLabel(m, hostOS), m, flex))
	}
	out = append(out,
		kCodeIcon("Left", kbArrowLeft, scanLeft, 9),
		kCodeIcon("Up", kbArrowUp, scanUp, 9),
		kCodeIcon("Down", kbArrowDown, scanDown, 9),
		kCodeIcon("Right", kbArrowRight, scanRight, 9),
		kPage("Fn", nextFn, 10),
	)
	return out
}

func (kb *MobileKeyboard) laptopStripSpecs() []kbKeySpec {
	nextFn := kbPageFn
	if kb.page == kbPageFn {
		nextFn = kbPageLetters
	}
	return []kbKeySpec{
		kCode("Esc", scanEsc, 10),
		kCode("Ins", scanInsert, 10),
		kCode("Home", scanHome, 10),
		kCode("PgUp", scanPageUp, 10),
		kCode("PgDn", scanPageDown, 10),
		kCode("End", scanEnd, 10),
		kCode("Del", scanDelete, 10),
		kCode("PrtSc", scanPrintScreen, 11),
		kPage("Fn", nextFn, 10),
	}
}

func charsRow(s string) []kbKeySpec {
	var out []kbKeySpec
	for _, r := range s {
		out = append(out, kChar(string(r), 10))
	}
	return out
}

func (kb *MobileKeyboard) fnRows(hostOS string) [][]kbKeySpec {
	mac := hostOS == "macos"
	var fRow []kbKeySpec
	for n := 1; n <= 12; n++ {
		fRow = append(fRow, kCode("F"+strconv.Itoa(n), scanFn(n), 10))
	}
	cmd := scanLCtrl
	p := "Ctrl+"
	if mac {
		cmd = scanLWin
		p = "⌘"
	}
	combo := func(label string, mods []uint32, code uint32, special string) kbKeySpec {
		return kbKeySpec{label: label, kind: kbKeyCombo, mods: mods, code: code, special: special, flex: 10, tonal: true}
	}
	shortcuts := []kbKeySpec{
		combo(p+"C", []uint32{cmd}, usKeyMap["c"].code, ""),
		combo(p+"V", []uint32{cmd}, usKeyMap["v"].code, ""),
		combo(p+"X", []uint32{cmd}, usKeyMap["x"].code, ""),
		combo(p+"Z", []uint32{cmd}, usKeyMap["z"].code, ""),
		combo(p+"A", []uint32{cmd}, usKeyMap["a"].code, ""),
	}
	if mac {
		shortcuts = append(shortcuts,
			combo("⌘Tab", []uint32{scanLWin}, scanTab, ""),
			combo("⌘Space", []uint32{scanLWin}, scanSpace, ""),
			combo("⌥⌘Esc", []uint32{scanLAlt, scanLWin}, scanEsc, ""),
		)
	} else {
		shortcuts = append(shortcuts, combo("Alt+Tab", []uint32{scanLAlt}, scanTab, ""))
		if hostOS == "linux" {
			shortcuts = append(shortcuts, kCode("Super", scanLWin, 10))
		} else {
			shortcuts = append(shortcuts,
				combo("Win", []uint32{scanLWin}, 0, ""),
				combo("Win+D", []uint32{scanLWin}, usKeyMap["d"].code, ""),
			)
		}
		shortcuts = append(shortcuts, combo("Ctrl+Alt+Del", []uint32{scanLCtrl, scanLAlt}, scanDelete, "ctrl_alt_del"))
	}
	rAlt := "RAlt"
	if mac {
		rAlt = "R⌥"
	}
	rWin := "RWin"
	if mac {
		rWin = "R⌘"
	} else if hostOS == "linux" {
		rWin = "RSuper"
	}
	shiftSpec := kMod("Shift", kbShift, 15)
	shiftSpec.icon = kbShiftIcon
	return [][]kbKeySpec{
		fRow,
		{
			kCode("Ins", scanInsert, 10), kCode("Home", scanHome, 10), kCode("PgUp", scanPageUp, 10),
			kCode("PrtSc", scanPrintScreen, 10), kCode("Pause", scanPause, 10), kCode("Menu", scanMenu, 10),
		},
		{
			kCode("Del", scanDelete, 10), kCode("End", scanEnd, 10), kCode("PgDn", scanPageDown, 10),
			kCode("Caps", scanCaps, 10), kCode("NumLk", scanNumLock, 10), kCode("ScrLk", scanScrollLock, 10),
		},
		shortcuts,
		{
			kPage("ABC", kbPageLetters, 15),
			shiftSpec,
			kCode("RCtrl", scanRCtrl, 10),
			kCode(rAlt, scanRAlt, 10),
			kCode(rWin, scanRWin, 12),
			kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15),
			kCodeIcon("Enter", kbEnterIcon, scanEnter, 15),
		},
	}
}

func (kb *MobileKeyboard) phoneRows(hostOS string) [][]kbKeySpec {
	abcLabel, abcNext := "?123", kbPageSymbols
	if kb.page != kbPageLetters {
		abcLabel, abcNext = "ABC", kbPageLetters
	}
	abc := kPage(abcLabel, abcNext, 16)
	space := kChar(" ", 46)
	enter := kCodeIcon("Enter", kbEnterIcon, scanEnter, 16)
	bottom := []kbKeySpec{abc, kChar(",", 11), space, kChar(".", 11), enter}

	switch kb.page {
	case kbPageSymbols:
		row3 := append([]kbKeySpec{kPage(`=\<`, kbPageMore, 15)}, charsRow(`*"':;!?`)...)
		row3 = append(row3, kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15))
		return [][]kbKeySpec{
			charsRow("1234567890"),
			charsRow(`@#$_&-+()/`),
			row3,
			bottom,
		}
	case kbPageMore:
		row2 := append(charsRow(`<>=_-+@#&`), kCode("Del", scanDelete, 10))
		row3 := append([]kbKeySpec{kPage("?123", kbPageSymbols, 15)}, charsRow(`.,!?/;:`)...)
		row3 = append(row3, kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15))
		return [][]kbKeySpec{
			charsRow(`~` + "`" + `|\{}[]^%`),
			row2,
			row3,
			bottom,
		}
	case kbPageFn:
		return kb.fnRows(hostOS)
	default:
		r2 := append([]kbKeySpec{{kind: kbKeyGap, flex: 5}}, charsRow("asdfghjkl")...)
		r2 = append(r2, kbKeySpec{kind: kbKeyGap, flex: 5})
		shift := kMod("Shift", kbShift, 15)
		shift.icon = kbShiftIcon
		r3 := append([]kbKeySpec{shift}, charsRow("zxcvbnm")...)
		r3 = append(r3, kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15))
		return [][]kbKeySpec{
			charsRow("qwertyuiop"),
			r2,
			r3,
			bottom,
		}
	}
}

func (kb *MobileKeyboard) fullRows(hostOS string, _ bool) [][]kbKeySpec {
	if kb.page != kbPageLetters {
		return kb.phoneRows(hostOS)
	}
	var leftMods, rightMods []kbKeySpec
	order := kb.modOrder(hostOS)
	for _, m := range order {
		leftMods = append(leftMods, kMod(kb.modLabel(m, hostOS), m, 13))
	}
	for i := len(order) - 1; i >= 0; i-- {
		rightMods = append(rightMods, kMod(kb.modLabel(order[i], hostOS), order[i], 13))
	}
	r1 := append([]kbKeySpec{kChar("`", 10)}, charsRow("1234567890")...)
	r1 = append(r1, kChar("-", 10), kChar("=", 10), kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 20))

	r2 := append([]kbKeySpec{kCode("Tab", scanTab, 15)}, charsRow("qwertyuiop")...)
	r2 = append(r2, kChar("[", 10), kChar("]", 10), kChar(`\`, 15))

	r3 := append([]kbKeySpec{kCode("Caps", scanCaps, 18)}, charsRow("asdfghjkl")...)
	r3 = append(r3, kChar(";", 10), kChar("'", 10), kCodeIcon("Enter", kbEnterIcon, scanEnter, 22))

	lShift := kMod("Shift", kbShift, 23)
	lShift.icon = kbShiftIcon
	rShift := kMod("Shift", kbShift, 27)
	rShift.icon = kbShiftIcon
	r4 := append([]kbKeySpec{lShift}, charsRow("zxcvbnm")...)
	r4 = append(r4, kChar(",", 10), kChar(".", 10), kChar("/", 10), rShift)

	r5 := []kbKeySpec{kPage("?123", kbPageSymbols, 15)}
	r5 = append(r5, leftMods...)
	r5 = append(r5, kChar(" ", 60))
	r5 = append(r5, rightMods...)
	return [][]kbKeySpec{r1, r2, r3, r4, r5}
}

func (kb *MobileKeyboard) laptopRows(hostOS string) [][]kbKeySpec {
	if kb.page != kbPageLetters {
		return kb.phoneRows(hostOS)
	}
	r1 := append([]kbKeySpec{kChar("`", 10)}, charsRow("1234567890")...)
	r1 = append(r1, kChar("-", 10), kChar("=", 10), kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 20))

	r2 := append([]kbKeySpec{kCode("Tab", scanTab, 15)}, charsRow("qwertyuiop")...)
	r2 = append(r2, kChar("[", 10), kChar("]", 10), kChar(`\`, 15))

	r3 := append([]kbKeySpec{kCode("Caps", scanCaps, 18)}, charsRow("asdfghjkl")...)
	r3 = append(r3, kChar(";", 10), kChar("'", 10), kCodeIcon("Enter", kbEnterIcon, scanEnter, 22))

	lShift := kMod("Shift", kbShift, 22)
	lShift.icon = kbShiftIcon
	rShift := kMod("Shift", kbShift, 18)
	rShift.icon = kbShiftIcon
	r4 := append([]kbKeySpec{lShift}, charsRow("zxcvbnm")...)
	r4 = append(r4, kChar(",", 10), kChar(".", 10), kChar("/", 10), rShift, kCodeIcon("Up", kbArrowUp, scanUp, 10))

	order := kb.modOrder(hostOS)
	r5 := []kbKeySpec{kPage("?123", kbPageSymbols, 13)}
	for _, m := range order {
		r5 = append(r5, kMod(kb.modLabel(m, hostOS), m, 11))
	}
	r5 = append(r5, kChar(" ", 44))
	rightOrder := []kbMod{kbAlt, kbCtrl}
	if hostOS == "macos" {
		rightOrder = []kbMod{kbWin, kbAlt}
	}
	for _, m := range rightOrder {
		r5 = append(r5, kMod(kb.modLabel(m, hostOS), m, 11))
	}
	r5 = append(r5,
		kCodeIcon("Left", kbArrowLeft, scanLeft, 9),
		kCodeIcon("Down", kbArrowDown, scanDown, 9),
		kCodeIcon("Right", kbArrowRight, scanRight, 9),
	)
	return [][]kbKeySpec{r1, r2, r3, r4, r5}
}

func (kb *MobileKeyboard) splitRows(hostOS string) ([][]kbKeySpec, [][]kbKeySpec) {
	var base [][]kbKeySpec
	switch kb.page {
	case kbPageLetters:
		r3 := append([]kbKeySpec{{kind: kbKeyGap, flex: 5}}, charsRow("asdfghjkl")...)
		r3 = append(r3, kCodeIcon("Enter", kbEnterIcon, scanEnter, 15))
		shift := kMod("Shift", kbShift, 15)
		shift.icon = kbShiftIcon
		r4 := append([]kbKeySpec{shift}, charsRow("zxcvbnm")...)
		r4 = append(r4, kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15))
		base = [][]kbKeySpec{
			charsRow("1234567890"),
			charsRow("qwertyuiop"),
			r3,
			r4,
		}
	case kbPageFn:
		base = kb.fnRows(hostOS)[:4]
	default:
		base = kb.phoneRows(hostOS)[:3]
	}
	var left, right [][]kbKeySpec
	for _, row := range base {
		var total float32
		for _, k := range row {
			total += k.flex
		}
		var sum float32
		var l, r []kbKeySpec
		for _, k := range row {
			if (sum+k.flex/2)*2 <= total {
				l = append(l, k)
			} else {
				r = append(r, k)
			}
			sum += k.flex
		}
		left = append(left, l)
		right = append(right, r)
	}
	if kb.page == kbPageFn {
		shift := kMod("Shift", kbShift, 15)
		shift.icon = kbShiftIcon
		left = append(left, []kbKeySpec{kPage("ABC", kbPageLetters, 15), shift})
		right = append(right, []kbKeySpec{kCodeIcon("Backspace", kbBackspaceIcon, scanBackspace, 15), kCodeIcon("Enter", kbEnterIcon, scanEnter, 15)})
	} else {
		abcLabel, abcNext := "?123", kbPageSymbols
		if kb.page != kbPageLetters {
			abcLabel, abcNext = "ABC", kbPageLetters
		}
		left = append(left, []kbKeySpec{kPage(abcLabel, abcNext, 15), kChar(",", 10), kChar(" ", 30)})
		right = append(right, []kbKeySpec{kChar(" ", 30), kChar(".", 10), kCodeIcon("Enter", kbEnterIcon, scanEnter, 12)})
	}
	return left, right
}
