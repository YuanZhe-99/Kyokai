package gui

import (
	"strings"

	"github.com/egoist/mygo/i18n"
)

// TermLook is how SSH panes' terminals look (doc/en-us/gui.md, "Terminal
// look"): the default in Settings, and a device's own choices over it. A
// field left zero follows the default; in the default it is Kyokai's own.
type TermLook struct {
	// Light and Dark are Ghostty themes by name, for light and dark
	// windows; "" is Kyokai's colors.
	Light string `json:"light,omitempty"`
	Dark  string `json:"dark,omitempty"`
	// Font is a font family; "" the system's monospaced font.
	Font string `json:"font,omitempty"`
	// Size is the text's size in DIPs; 0 is termSize.
	Size float32 `json:"size,omitempty"`
	// Cursor is "block", "bar" or "underline"; "" a block.
	Cursor string `json:"cursor,omitempty"`
	// Blink is "on" or "off"; "" blinks.
	Blink string `json:"blink,omitempty"`
}

// The terminal's text size by default, and the sizes offered.
const (
	termSize    float32 = 13
	termSizeMin float32 = 9
	termSizeMax float32 = 24
)

// termContrast is the least contrast of the terminals' text with its
// background (WCAG): a program's dim grey stays readable on any theme.
const termContrast = 4.5

// The cursor shapes and blinking a TermLook names.
var (
	termCursors = []string{"block", "bar", "underline"}
	termBlinks  = []string{"on", "off"}
)

// over is l with each field left zero taken from base.
func (l TermLook) over(base TermLook) TermLook {
	pick := func(s, b string) string {
		if s == "" {
			return b
		}
		return s
	}
	if l.Size == 0 {
		l.Size = base.Size
	}
	return TermLook{
		Light: pick(l.Light, base.Light), Dark: pick(l.Dark, base.Dark), Font: pick(l.Font, base.Font),
		Size: l.Size, Cursor: pick(l.Cursor, base.Cursor), Blink: pick(l.Blink, base.Blink),
	}
}

// termLook is the look of a device's SSH panes: its own choices over the
// default.
func (a *App) termLook(device string) TermLook {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs.Terminals[device].over(a.prefs.Terminal)
}

// sshEnv are the variables an SSH shell is asked to start with: the
// interface's language in UTF-8, so that programs write it, and the
// terminal's colors. Servers take what their AcceptEnv allows (OpenSSH:
// LANG and LC_* by default) and ignore the rest.
func sshEnv() [][2]string {
	lang := "en_US.UTF-8"
	switch loc := i18n.Locale(); {
	case strings.HasPrefix(loc, "zh-Hant"), strings.HasPrefix(loc, "zh-TW"), strings.HasPrefix(loc, "zh-HK"):
		lang = "zh_TW.UTF-8"
	case strings.HasPrefix(loc, "zh"):
		lang = "zh_CN.UTF-8"
	case strings.HasPrefix(loc, "ja"):
		lang = "ja_JP.UTF-8"
	}
	return [][2]string{{"LANG", lang}, {"COLORTERM", "truecolor"}}
}

// termKyokai is a device's choice of Kyokai's colors over a default that
// names a Ghostty theme.
const termKyokai = "kyokai"
