package gui

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
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

// themes are the terminal's colors for light and dark windows.
func (l TermLook) themes() (light, dark *terminal.Theme) {
	return termTheme(l.Light, false), termTheme(l.Dark, true)
}

// font is the terminal's font.
func (l TermLook) font() terminal.Font {
	size := l.Size
	if size == 0 {
		size = termSize
	}
	return terminal.Font{Family: l.Font, Size: size}
}

// cursor is the terminal's cursor shape, and whether it keeps still.
func (l TermLook) cursor() (terminal.CursorStyle, bool) {
	shape := terminal.CursorBlock
	switch l.Cursor {
	case "bar":
		shape = terminal.CursorBar
	case "underline":
		shape = terminal.CursorUnderline
	}
	return shape, l.Blink == "off"
}

var (
	// termThemes are the Ghostty themes read so far, by name and darkness
	// (nil: a name Ghostty no longer has).
	termThemes   = map[string]*terminal.Theme{}
	termThemesMu sync.Mutex
	// termNames are the Ghostty themes' names, read once.
	termNames = sync.OnceValue(terminal.GhosttyThemes)
)

// termTheme is a Ghostty theme by name, or Kyokai's colors for "",
// termKyokai and a name Ghostty does not have: the terminal's own for the
// appearance, with the accent as the cursor and its tint as the selection.
func termTheme(name string, dark bool) *terminal.Theme {
	if name != "" && name != termKyokai {
		termThemesMu.Lock()
		th, ok := termThemes[name]
		if !ok {
			th, _ = terminal.GhosttyTheme(name)
			termThemes[name] = th
		}
		termThemesMu.Unlock()
		if th != nil {
			return th
		}
	}
	th, ut := terminal.LightTheme(), ui.LightTheme()
	if dark {
		th, ut = terminal.DarkTheme(), ui.DarkTheme()
	}
	th.Cursor = ut.Accent
	th.Selection = ut.Accent.Alpha(0.3)
	return th
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

// setTermLook changes the default look (device "") or a device's own
// choices; a zero look clears the device's.
// Side effects: writes gui.json; the device's open SSH panes, or all of
// them, take the colors and the font at once — the cursor with their next
// connection.
func (a *App) setTermLook(device string, l TermLook) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	if device == "" {
		p.Terminal = l
	} else {
		devs := make(map[string]TermLook, len(p.Terminals)+1)
		for k, v := range p.Terminals {
			devs[k] = v
		}
		if l == (TermLook{}) {
			delete(devs, device)
		} else {
			devs[device] = l
		}
		p.Terminals = devs
	}
	_ = a.setPrefs(p)
	a.mu.Lock()
	tabs := make([]*serviceTab, 0, len(a.services))
	for _, s := range a.services {
		if device == "" || s.device == device {
			tabs = append(tabs, s)
		}
	}
	a.mu.Unlock()
	for _, s := range tabs {
		if c, ok := s.running().(*sshClient); ok {
			look := p.Terminals[s.device].over(p.Terminal)
			c.term.SetTheme(look.themes())
			c.term.SetFont(look.font())
		}
	}
}

// termKyokai is a device's choice of Kyokai's colors over a default that
// names a Ghostty theme.
const termKyokai = "kyokai"

// termLookForm edits a look, a row each (row places a control under its
// title): the themes for light and dark windows (any of Ghostty's, typed
// to find), the font, its size, the cursor and its blinking. With base, a
// device's form, each choice first offers the default, which it words.
// Returns: the look, changed or not, and whether it was.
func termLookForm(c *ui.Context, key string, l TermLook, base *TermLook, row func(id, title string, control func())) (TermLook, bool) {
	changed := false
	// A device's form, in the files panel, takes the panel's width.
	field := func(e ui.Element) ui.Element {
		if base != nil {
			return e.FillWidth()
		}
		return e.Width(termFieldW)
	}
	// pick is a choice of values worded by labels: def is the value a zero
	// field means in the default; a device's form adds the default first,
	// worded by its value from.
	pick := func(id, title string, combo bool, cur string, values, labels []string, def, from string) string {
		options, vals := labels, values
		if base != nil {
			if from == "" {
				from = def
			}
			word := from
			if i := slices.Index(values, from); i >= 0 {
				word = labels[i]
			}
			options = append([]string{L.TermUseDefault(word)}, labels...)
			vals = append([]string{""}, values...)
		} else if cur == "" {
			cur = def
		}
		at := max(slices.Index(vals, cur), 0)
		shown := options[at]
		row(key+"-"+id, title, func() {
			var e ui.Element
			if combo {
				e = ui.Combobox(c.Key(key+"-"+id+"-in"), &shown, options)
			} else {
				e = ui.Select(c.Key(key+"-"+id+"-in"), &shown, options)
			}
			if field(e).Label(title).Changed() {
				if i := slices.Index(options, shown); i >= 0 {
					cur, changed = vals[i], true
				}
			}
		})
		if base == nil && cur == def {
			return ""
		}
		return cur
	}
	names := termNames()
	themes := append([]string{termKyokai}, names...)
	themeLabels := append([]string{L.TermThemeKyokai()}, names...)
	l.Light = pick("light", L.TermLight(), true, l.Light, themes, themeLabels, termKyokai, baseOf(base).Light)
	l.Dark = pick("dark", L.TermDark(), true, l.Dark, themes, themeLabels, termKyokai, baseOf(base).Dark)
	row(key+"-font", L.TermFont(), func() {
		ph := L.TermFontSystem()
		switch {
		case base != nil && base.Font != "":
			ph = L.TermUseDefault(base.Font)
		case base != nil:
			ph = L.TermUseDefault(ph)
		}
		if field(ui.TextInput(c.Key(key+"-font-in"), &l.Font).Placeholder(ph)).Label(L.TermFont()).Changed() {
			changed = true
		}
	})
	l.Font = strings.TrimSpace(l.Font)
	var sizes []string
	for s := termSizeMin; s <= termSizeMax; s++ {
		sizes = append(sizes, strconv.Itoa(int(s)))
	}
	size := ""
	if l.Size != 0 {
		size = strconv.Itoa(int(l.Size))
	}
	from := ""
	if b := baseOf(base); b.Size != 0 {
		from = strconv.Itoa(int(b.Size))
	}
	size = pick("size", L.TermSize(), false, size, sizes, sizes, strconv.Itoa(int(termSize)), from)
	l.Size = 0
	if n, err := strconv.Atoi(size); err == nil {
		l.Size = float32(n)
	}
	l.Cursor = pick("cursor", L.TermCursor(), false, l.Cursor, termCursors,
		[]string{L.TermCursorBlock(), L.TermCursorBar(), L.TermCursorUnderline()}, termCursors[0], baseOf(base).Cursor)
	l.Blink = pick("blink", L.TermBlink(), false, l.Blink, termBlinks,
		[]string{L.TermBlinkOn(), L.TermBlinkOff()}, termBlinks[0], baseOf(base).Blink)
	return l, changed
}

// baseOf is a device form's default, or none.
func baseOf(base *TermLook) TermLook {
	if base == nil {
		return TermLook{}
	}
	return *base
}

// termPreview shows a look as a few lines of a shell, in light and dark
// mode side by side, or one above the other (stacked, where it is
// narrow): the themes' colors, the font at its size, and the cursor's
// shape.
func termPreview(c *ui.Context, key string, l TermLook, stacked bool) {
	light, dark := l.themes()
	var box ui.Element
	if stacked {
		box = ui.Column(c.Key(key))
	} else {
		box = ui.Row(c.Key(key))
	}
	box.Gap(gap).FillWidth().Children(func() {
		termSample(c, key+"-light", light, l, L.TermPreviewLight(), stacked)
		termSample(c, key+"-dark", dark, l, L.TermPreviewDark(), stacked)
	})
}

// termSample is one theme's preview: a prompt, a listing, an error and a
// warning in the program colors a shell uses most, then the cursor.
func termSample(c *ui.Context, key string, th *terminal.Theme, l TermLook, label string, stacked bool) {
	f := l.font()
	family := f.Family
	if family == "" {
		family = "monospace"
	}
	fg, p := th.Foreground, th.Palette
	type span struct {
		text string
		col  ui.Color
		bold bool
	}
	prompt := []span{{"me@studio", p[2], true}, {":", fg, false}, {"~/projects", p[4], true}, {" $", fg, false}}
	lines := [][]span{
		append(slices.Clone(prompt), span{" ls", fg, false}),
		{{"docs", p[4], true}, {"  build.sh", p[2], false}, {"  notes.md", fg, false}},
		{{"error:", p[1], true}, {" no such file", fg, false}},
		{{"warning:", p[3], true}, {" disk 91% full", fg, false}},
	}
	cursor := th.Cursor
	if cursor == (ui.Color{}) {
		cursor = fg
	}
	lineH := f.Size * 1.4
	cellW := f.Size * 0.6
	card := ui.Column(c.Key(key)).MinWidth(0).FillWidth().Padding(10, 12).Radius(chipRadius).Background(th.Background).
		Border(1, th.Foreground.Alpha(0.12)).Clip().Label(label)
	if !stacked {
		card.Grow(1).Basis(0)
	}
	card.Children(func() {
		text := func(s span) {
			tx := ui.Text(c, strings.ReplaceAll(s.text, " ", " ")).Font(family).FontSize(f.Size).TextColor(s.col).SingleLine()
			if s.bold {
				tx.Bold()
			}
		}
		for _, line := range lines {
			ui.Row(c).Height(lineH).AlignItems(ui.Center).Children(func() {
				for _, s := range line {
					text(s)
				}
			})
		}
		ui.Row(c).Height(lineH).AlignItems(ui.Center).Children(func() {
			for _, s := range prompt {
				text(s)
			}
			// A space after the prompt, then the cursor.
			mark := ui.Box(c).Shrink(0).Margin(0, 0, 0, cellW).Background(cursor)
			switch l.Cursor {
			case "bar":
				mark.Size(2, f.Size*1.2)
			case "underline":
				mark.Size(cellW, 2).AlignSelf(ui.End).Margin(0, 0, 3, 0)
			default:
				mark.Size(cellW, f.Size*1.2)
			}
		})
	})
}
