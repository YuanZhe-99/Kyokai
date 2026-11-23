package gui

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
	page     kbPage
	latch    [4]kbLatch
	held     [4]bool
	hostHeld [4]bool
	caps     bool
	lastTap  [4]time.Time
	pressed  string
	pad      *Touchpad
	padRect  ui.Rect
	hwDown   map[uint32]bool
}

// NewMobileKeyboard creates a fresh mobile keyboard state.
func NewMobileKeyboard(padSink TouchpadSink, pointerSpeed float32) *MobileKeyboard {
	kb := &MobileKeyboard{hwDown: map[uint32]bool{}}
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

// ReleaseAll releases any modifier or hardware keys held down on the host.
func (kb *MobileKeyboard) ReleaseAll(send func(code uint32, down, caps bool)) {
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

func (kb *MobileKeyboard) tapModifier(m kbMod, hostOS string, send func(code uint32, down, caps bool)) {
	now := time.Now()
	twice := !kb.lastTap[m].IsZero() && now.Sub(kb.lastTap[m]) < 350*time.Millisecond
	kb.lastTap[m] = now
	if m == kbShift && (twice || kb.caps) {
		kb.latch[m] = kbOff
		kb.caps = !kb.caps
		return
	}
	if m == kbWin && hostOS != "macos" && kb.latch[m] == kbOff && !twice {
		// On Windows and Linux, tapping Win/Super alone opens Start / Overview
		// unless double-tapped to lock. Still allow once-latch via Shift/Ctrl/Alt,
		// or latch Win on single tap so Win+R works seamlessly on touchscreens:
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
	label   string
	kind    kbKeyKind
	char    string
	code    uint32
	mod     kbMod
	page    kbPage
	mods    []uint32
	special string
	flex    float32
	tonal   bool
}

func kChar(c string, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 10
	}
	return kbKeySpec{label: c, kind: kbKeyChar, char: c, flex: flex}
}

func kCode(label string, code uint32, flex float32) kbKeySpec {
	if flex == 0 {
		flex = 10
	}
	return kbKeySpec{label: label, kind: kbKeyCode, code: code, flex: flex, tonal: true}
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

// MobileKeyboardOptions configures one render pass of the on-screen keyboard.
type MobileKeyboardOptions struct {
	TargetLabel string
	HostOS      string // "windows", "macos", "linux"
	OSPicked    bool
	AutoLabel   string
	Split       bool
	NativeTouch bool
	OnOSPick    func(choice string) // "" auto, "windows", "macos", "linux"
	OnSplit     func(split bool)
	OnHide      func()
	OnSpecial   func(action string) bool
	OnTypeText  func()
	Send        func(code uint32, down, caps bool)
}

// View renders the responsive mobile computer keyboard.
func (kb *MobileKeyboard) View(c *ui.Context, keyPrefix string, width float32, o MobileKeyboardOptions) {
	t := c.Theme()
	wide := width >= 600
	split := wide && o.Split
	mac := o.HostOS == "macos"
	_, _, safeBottom, _ := c.SafeArea()
	kbBot := safeBottom
	if kbBot > mobileKeyboardThreshold {
		kbBot = 0
	}

	ui.Column(c.Key(keyPrefix)).FillWidth().Shrink(0).Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		Padding(4, 6, 4+kbBot, 6).Margin(0, 0, -kbBot, 0).Gap(3).Children(func() {
		// Header bar: OS badge, Type text button, Full/Split toggle on wide screens, Hide button.
		ui.Row(c.Key(keyPrefix + "-hdr")).Height(36).FillWidth().AlignItems(ui.Center).Gap(6).PaddingX(4).Children(func() {
			osName := map[string]string{"windows": "Windows", "macos": "macOS", "linux": "Linux"}[o.HostOS]
			if osName == "" {
				osName = "Windows"
			}
			badgeText := o.TargetLabel + " · " + osName
			if !o.OSPicked && o.AutoLabel != "" {
				badgeText += " (" + o.AutoLabel + ")"
			}
			badge := ui.ButtonBase(c.Key(keyPrefix+"-os")).Padding(4, 10).Radius(8).Background(t.Accent.Alpha(0.16)).Label(badgeText)
			badge.Children(func() {
				ui.Text(c, badgeText).FontSize(12).FontWeight(600).TextColor(t.Accent).NoWrap()
			})
			if badge.Clicked() && o.OnOSPick != nil {
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

			ui.Box(c).Grow(1)

			if o.OnTypeText != nil {
				tb := ui.ButtonBase(c.Key(keyPrefix+"-typetext")).Padding(4, 10).Radius(8).Background(t.Background).Border(1, t.Border)
				tb.Children(func() {
					ui.Text(c, "Aa").FontSize(12).FontWeight(600).TextColor(t.Text)
				})
				if tb.Clicked() {
					o.OnTypeText()
				}
			}

			if wide && o.OnSplit != nil {
				for _, mode := range []struct {
					label string
					sp    bool
				}{{"Full", false}, {"Split", true}} {
					active := o.Split == mode.sp
					bg := t.Background
					fg := t.TextMuted
					if active {
						bg = t.Accent.Alpha(0.16)
						fg = t.Accent
					}
					mb := ui.ButtonBase(c.Key(keyPrefix+"-sp-"+mode.label)).Height(28).PaddingX(10).Radius(8).Background(bg).Border(1, t.Border).Center().Label(mode.label)
					mb.Children(func() {
						ui.Text(c, mode.label).FontSize(12).FontWeight(600).TextColor(fg)
					})
					if mb.Clicked() && !active {
						o.OnSplit(mode.sp)
					}
				}
			}

			hide := ui.ButtonBase(c.Key(keyPrefix+"-hide")).Size(38, 32).Radius(8).Center().Label("Hide keyboard")
			hide.Children(func() {
				ui.Icon(c, barUnfold).FontSize(16).TextColor(t.TextMuted)
			})
			if hide.Clicked() {
				kb.ReleaseAll(o.Send)
				if o.OnHide != nil {
					o.OnHide()
				}
			}
		})

		// Top computer-key strip.
		strip := kb.stripSpecs(o.HostOS)
		kb.renderRow(c, keyPrefix+"-strip", strip, 38, true, o)

		rowH := float32(45)
		if wide && !split {
			rowH = 48
		}
		if split {
			leftRows, rightRows := kb.splitRows(o.HostOS)
			halfW := min(max(width*0.36, 180), 372)
			ui.Row(c.Key(keyPrefix + "-split-body")).FillWidth().AlignItems(ui.End).Gap(6).Children(func() {
				ui.Column(c.Key(keyPrefix + "-left")).Width(halfW).Gap(2).Children(func() {
					for i, r := range leftRows {
						kb.renderRow(c, keyPrefix+"-l"+strconv.Itoa(i), r, rowH, false, o)
					}
				})
				kb.renderCenterPad(c, keyPrefix+"-pad", rowH*float32(len(leftRows)), o)
				ui.Column(c.Key(keyPrefix + "-right")).Width(halfW).Gap(2).Children(func() {
					for i, r := range rightRows {
						kb.renderRow(c, keyPrefix+"-r"+strconv.Itoa(i), r, rowH, false, o)
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
			ui.Column(c.Key(keyPrefix + "-rows")).FillWidth().MaxWidth(1100).AlignSelf(ui.Center).Gap(2).Children(func() {
				for i, r := range rows {
					kb.renderRow(c, keyPrefix+"-row"+strconv.Itoa(i), r, rowH, false, o)
				}
			})
		}
	})
}

func (kb *MobileKeyboard) renderCenterPad(c *ui.Context, key string, height float32, o MobileKeyboardOptions) {
	t := c.Theme()
	ui.Column(c.Key(key)).Grow(1).Basis(0).Height(height).Padding(2, 4).Gap(4).Children(func() {
		pad := ui.Box(c.Key(key+"-area")).Grow(1).FillWidth().Radius(14).Border(1, t.Border).Background(t.Background).Center()
		kb.padRect = pad.LayoutBounds()
		if kb.pad != nil {
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
		pad.Children(func() {
			ui.Text(c, "Touchpad").FontSize(12).TextColor(t.TextMuted)
		})
		ui.Row(c.Key(key + "-btns")).Height(38).FillWidth().Gap(4).Children(func() {
			for _, b := range []struct {
				label string
				btn   int
			}{{"Left", 0}, {"Right", 1}} {
				el := ui.ButtonBase(c.Key(key+"-b"+strconv.Itoa(b.btn))).Grow(1).Basis(0).FillHeight().Radius(10).Background(t.Background).Border(1, t.Border).Center().Label(b.label)
				el.Children(func() { ui.Text(c, b.label).FontSize(12).FontWeight(500) })
				if el.Clicked() && kb.pad != nil {
					kb.pad.sink.Button(b.btn, true)
					kb.pad.sink.Button(b.btn, false)
				}
			}
		})
	})
}

func (kb *MobileKeyboard) renderRow(c *ui.Context, rowKey string, specs []kbKeySpec, height float32, strip bool, o MobileKeyboardOptions) {
	t := c.Theme()
	ui.Row(c.Key(rowKey)).FillWidth().Height(height).Gap(3).Children(func() {
		for i, sp := range specs {
			kID := rowKey + "-" + strconv.Itoa(i)
			if sp.kind == kbKeyGap {
				ui.Box(c.Key(kID)).Grow(sp.flex).Basis(0).FillHeight()
				continue
			}
			lit := false
			locked := false
			switch sp.kind {
			case kbKeyMod:
				lit = kb.active(sp.mod)
				if sp.mod == kbShift {
					locked = kb.caps || kb.latch[sp.mod] == kbLocked
				} else {
					locked = kb.latch[sp.mod] == kbLocked
				}
			case kbKeyPage:
				if strip && sp.label == "Fn" {
					lit = kb.page == kbPageFn
				}
			}
			isEnter := sp.kind == kbKeyCode && sp.code == scanEnter
			bg := t.Background
			fg := t.Text
			switch {
			case lit || isEnter:
				bg = t.Accent
				fg = ui.RGB(255, 255, 255)
			case sp.tonal || strip:
				bg = t.SurfaceHover
			}
			radius := float32(10)
			if isEnter {
				radius = 18
			}
			label := sp.label
			if sp.kind == kbKeyChar {
				label = kb.shownChar(sp.char, o.HostOS)
			}
			a11y := label
			if a11y == "" && sp.kind == kbKeyChar && sp.char == " " {
				a11y = "Space"
			}
			btn := ui.ButtonBase(c.Key(kID)).Grow(sp.flex).Basis(0).MinWidth(0).FillHeight().Radius(radius).
				Background(bg).Border(1, t.Border.Alpha(0.5)).Center().Label(a11y)
			// Multi-touch modifier hold when Element.Touches() fires (U1).
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
				fontSize := float32(13)
				if sp.kind == kbKeyChar && len([]rune(label)) == 1 {
					fontSize = 18
				}
				col := ui.Column(c).AlignItems(ui.Center).Justify(ui.Center).Gap(1)
				col.Children(func() {
					ui.Text(c, label).FontSize(fontSize).FontWeight(500).TextColor(fg).NoWrap()
					if locked {
						ui.Box(c).Width(12).Height(2).Radius(1).Background(fg)
					}
				})
			})
			if btn.Clicked() {
				mygo.Haptics.Light()
				kb.activate(sp, o)
			}
		}
	})
}

func (kb *MobileKeyboard) activate(sp kbKeySpec, o MobileKeyboardOptions) {
	switch sp.kind {
	case kbKeyMod:
		if sp.mod == kbWin && o.HostOS != "macos" && kb.latch[sp.mod] == kbOff {
			// Tap Win/Super latches once so Win+D / Super+L can be typed with
			// one finger; tapping Win while latched sends standalone Win tap!
			kb.tapModifier(sp.mod, o.HostOS, o.Send)
			return
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
			return "⌃ Ctrl"
		}
		return "Ctrl"
	case kbAlt:
		if hostOS == "macos" {
			return "⌥ Opt"
		}
		return "Alt"
	default:
		switch hostOS {
		case "macos":
			return "⌘ Cmd"
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

func (kb *MobileKeyboard) stripSpecs(hostOS string) []kbKeySpec {
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
	nextFn := kbPageFn
	if kb.page == kbPageFn {
		nextFn = kbPageLetters
	}
	out = append(out,
		kCode("←", scanLeft, 10),
		kCode("↑", scanUp, 10),
		kCode("↓", scanDown, 10),
		kCode("→", scanRight, 10),
		kPage("Fn", nextFn, 10),
	)
	return out
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
			kMod("Shift", kbShift, 15),
			kCode("RCtrl", scanRCtrl, 10),
			kCode(rAlt, scanRAlt, 10),
			kCode(rWin, scanRWin, 12),
			kCode("⌫", scanBackspace, 15),
			kCode("⏎", scanEnter, 15),
		},
	}
}

func (kb *MobileKeyboard) phoneRows(hostOS string) [][]kbKeySpec {
	abcLabel, abcNext := "?123", kbPageSymbols
	if kb.page != kbPageLetters {
		abcLabel, abcNext = "ABC", kbPageLetters
	}
	abc := kPage(abcLabel, abcNext, 15)
	hide := kbKeySpec{label: "⌨", kind: kbKeyHide, flex: 10, tonal: true}
	bottom := []kbKeySpec{abc, kChar(",", 10), hide, kChar(" ", 40), kChar(".", 10), kCode("⏎", scanEnter, 15)}

	switch kb.page {
	case kbPageSymbols:
		row3 := append([]kbKeySpec{kPage(`=\<`, kbPageMore, 15)}, charsRow(`*"':;!?`)...)
		row3 = append(row3, kCode("⌫", scanBackspace, 15))
		return [][]kbKeySpec{
			charsRow("1234567890"),
			charsRow(`@#$_&-+()/`),
			row3,
			bottom,
		}
	case kbPageMore:
		row2 := append(charsRow(`<>=_-+@#&`), kCode("Del", scanDelete, 10))
		row3 := append([]kbKeySpec{kPage("?123", kbPageSymbols, 15)}, charsRow(`.,!?/;:`)...)
		row3 = append(row3, kCode("⌫", scanBackspace, 15))
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
		r3 := append([]kbKeySpec{kMod("⇧", kbShift, 15)}, charsRow("zxcvbnm")...)
		r3 = append(r3, kCode("⌫", scanBackspace, 15))
		return [][]kbKeySpec{
			charsRow("qwertyuiop"),
			r2,
			r3,
			bottom,
		}
	}
}

func (kb *MobileKeyboard) fullRows(hostOS string, mac bool) [][]kbKeySpec {
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
	r1 = append(r1, kChar("-", 10), kChar("=", 10), kCode("⌫", scanBackspace, 20))

	r2 := append([]kbKeySpec{kCode("Tab", scanTab, 15)}, charsRow("qwertyuiop")...)
	r2 = append(r2, kChar("[", 10), kChar("]", 10), kChar(`\`, 15))

	r3 := append([]kbKeySpec{kCode("Caps", scanCaps, 18)}, charsRow("asdfghjkl")...)
	r3 = append(r3, kChar(";", 10), kChar("'", 10), kCode("⏎", scanEnter, 22))

	r4 := append([]kbKeySpec{kMod("Shift", kbShift, 23)}, charsRow("zxcvbnm")...)
	r4 = append(r4, kChar(",", 10), kChar(".", 10), kChar("/", 10), kMod("Shift", kbShift, 27))

	r5 := []kbKeySpec{kPage("?123", kbPageSymbols, 15)}
	r5 = append(r5, leftMods...)
	r5 = append(r5, kChar(" ", 60))
	r5 = append(r5, rightMods...)
	r5 = append(r5, kbKeySpec{label: "⌨", kind: kbKeyHide, flex: 12, tonal: true})
	return [][]kbKeySpec{r1, r2, r3, r4, r5}
}

func (kb *MobileKeyboard) splitRows(hostOS string) ([][]kbKeySpec, [][]kbKeySpec) {
	var base [][]kbKeySpec
	switch kb.page {
	case kbPageLetters:
		r3 := append([]kbKeySpec{{kind: kbKeyGap, flex: 5}}, charsRow("asdfghjkl")...)
		r3 = append(r3, kCode("⏎", scanEnter, 15))
		r4 := append([]kbKeySpec{kMod("⇧", kbShift, 15)}, charsRow("zxcvbnm")...)
		r4 = append(r4, kCode("⌫", scanBackspace, 15))
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
		left = append(left, []kbKeySpec{kPage("ABC", kbPageLetters, 15), kMod("Shift", kbShift, 15)})
		right = append(right, []kbKeySpec{kCode("⌫", scanBackspace, 15), kCode("⏎", scanEnter, 15)})
	} else {
		abcLabel, abcNext := "?123", kbPageSymbols
		if kb.page != kbPageLetters {
			abcLabel, abcNext = "ABC", kbPageLetters
		}
		left = append(left, []kbKeySpec{kPage(abcLabel, abcNext, 15), kChar(",", 10), kChar(" ", 30)})
		right = append(right, []kbKeySpec{kChar(" ", 30), kChar(".", 10), {label: "⌨", kind: kbKeyHide, flex: 10, tonal: true}})
	}
	return left, right
}
