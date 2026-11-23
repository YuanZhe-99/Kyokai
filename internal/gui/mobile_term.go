package gui

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// miniTerm is a lightweight ANSI/VT100 terminal screen buffer used on mobile
// platforms where libghostty-vt is not bundled yet, providing clean interactive
// SSH terminal sessions with scrollback, colors and cursor navigation.
type miniTerm struct {
	mu      sync.Mutex
	cols    int
	rows    int
	curX    int
	curY    int
	grid    [][]rune
	scroll  [][]rune
	parser  int // 0 plain, 1 ESC, 2 CSI, 3 OSC, 4 OSC-ESC
	csi     string
	osc     string
	utf8Buf []byte
	altGrid [][]rune
	altX    int
	altY    int
	inAlt   bool
	onTitle func(string)
	redraw  func()
}

func newMiniTerm(cols, rows int, onTitle func(string), redraw func()) *miniTerm {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	m := &miniTerm{cols: cols, rows: rows, onTitle: onTitle, redraw: redraw}
	m.grid = makeGrid(cols, rows)
	return m
}

func makeGrid(cols, rows int) [][]rune {
	g := make([][]rune, rows)
	for y := range rows {
		row := make([]rune, cols)
		for x := range cols {
			row[x] = ' '
		}
		g[y] = row
	}
	return g
}

func (m *miniTerm) Resize(cols, rows int) (changed bool) {
	if cols < 20 {
		cols = 20
	}
	if rows < 6 {
		rows = 6
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cols == m.cols && rows == m.rows {
		return false
	}
	next := makeGrid(cols, rows)
	for y := 0; y < min(rows, m.rows); y++ {
		copy(next[y][:min(cols, m.cols)], m.grid[y][:min(cols, m.cols)])
	}
	m.cols, m.rows = cols, rows
	m.grid = next
	if m.curX >= cols {
		m.curX = cols - 1
	}
	if m.curY >= rows {
		m.curY = rows - 1
	}
	return true
}

func (m *miniTerm) Feed(data []byte) {
	var title string
	m.mu.Lock()
	m.utf8Buf = append(m.utf8Buf, data...)
	for len(m.utf8Buf) > 0 {
		if m.utf8Buf[0] < 0x80 {
			ch := rune(m.utf8Buf[0])
			m.utf8Buf = m.utf8Buf[1:]
			if t := m.stepLocked(ch); t != "" {
				title = t
			}
			continue
		}
		if !utf8.FullRune(m.utf8Buf) {
			break
		}
		r, n := utf8.DecodeRune(m.utf8Buf)
		m.utf8Buf = m.utf8Buf[n:]
		if t := m.stepLocked(r); t != "" {
			title = t
		}
	}
	m.mu.Unlock()
	if title != "" && m.onTitle != nil {
		m.onTitle(title)
	}
	if m.redraw != nil {
		m.redraw()
	}
}

func (m *miniTerm) stepLocked(ch rune) string {
	switch m.parser {
	case 1: // ESC
		switch ch {
		case '[':
			m.parser = 2
			m.csi = ""
		case ']':
			m.parser = 3
			m.osc = ""
		case 'c':
			m.grid = makeGrid(m.cols, m.rows)
			m.curX, m.curY = 0, 0
			m.parser = 0
		default:
			m.parser = 0
		}
		return ""
	case 2: // CSI
		if ch >= 0x40 && ch <= 0x7E {
			m.execCSILocked(m.csi, ch)
			m.parser = 0
		} else if len(m.csi) < 64 {
			m.csi += string(ch)
		} else {
			m.parser = 0
		}
		return ""
	case 3: // OSC
		if ch == 0x07 {
			m.parser = 0
			return m.parseOSCLocked(m.osc)
		}
		if ch == 0x1B {
			m.parser = 4
			return ""
		}
		if len(m.osc) < 256 {
			m.osc += string(ch)
		}
		return ""
	case 4: // OSC + ESC
		m.parser = 0
		if ch == '\\' {
			return m.parseOSCLocked(m.osc)
		}
		return ""
	}

	switch ch {
	case 0x1B:
		m.parser = 1
	case '\r':
		m.curX = 0
	case '\n':
		m.lineFeedLocked()
	case '\b':
		if m.curX > 0 {
			m.curX--
		}
	case '\t':
		m.curX = min((m.curX/8+1)*8, m.cols-1)
	default:
		if ch >= 0x20 {
			if m.curX >= m.cols {
				m.curX = 0
				m.lineFeedLocked()
			}
			m.grid[m.curY][m.curX] = ch
			m.curX++
		}
	}
	return ""
}

func (m *miniTerm) lineFeedLocked() {
	if m.curY+1 < m.rows {
		m.curY++
		return
	}
	if !m.inAlt {
		saved := make([]rune, m.cols)
		copy(saved, m.grid[0])
		m.scroll = append(m.scroll, saved)
		if len(m.scroll) > 500 {
			m.scroll = m.scroll[len(m.scroll)-500:]
		}
	}
	copy(m.grid[0:], m.grid[1:])
	blank := make([]rune, m.cols)
	for i := range blank {
		blank[i] = ' '
	}
	m.grid[m.rows-1] = blank
}

func (m *miniTerm) parseOSCLocked(osc string) string {
	if strings.HasPrefix(osc, "0;") || strings.HasPrefix(osc, "2;") {
		return cleanTitle(osc[2:], sshTitleMax)
	}
	return ""
}

func parseCSIParams(s string) []int {
	s = strings.TrimLeft(s, "?><=")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	out := make([]int, len(parts))
	for i, p := range parts {
		var v int
		_, _ = fmt.Sscanf(p, "%d", &v)
		out[i] = v
	}
	return out
}

func paramAt(p []int, i, def int) int {
	if i < len(p) && p[i] > 0 {
		return p[i]
	}
	return def
}

func (m *miniTerm) execCSILocked(params string, final rune) {
	priv := strings.HasPrefix(params, "?")
	nums := parseCSIParams(params)
	switch final {
	case 'A':
		m.curY = max(0, m.curY-paramAt(nums, 0, 1))
	case 'B':
		m.curY = min(m.rows-1, m.curY+paramAt(nums, 0, 1))
	case 'C':
		m.curX = min(m.cols-1, m.curX+paramAt(nums, 0, 1))
	case 'D':
		m.curX = max(0, m.curX-paramAt(nums, 0, 1))
	case 'G':
		m.curX = min(max(paramAt(nums, 0, 1)-1, 0), m.cols-1)
	case 'H', 'f':
		r := min(max(paramAt(nums, 0, 1)-1, 0), m.rows-1)
		c := min(max(paramAt(nums, 1, 1)-1, 0), m.cols-1)
		m.curY, m.curX = r, c
	case 'J':
		mode := 0
		if len(nums) > 0 {
			mode = nums[0]
		}
		switch mode {
		case 0:
			for x := m.curX; x < m.cols; x++ {
				m.grid[m.curY][x] = ' '
			}
			for y := m.curY + 1; y < m.rows; y++ {
				for x := 0; x < m.cols; x++ {
					m.grid[y][x] = ' '
				}
			}
		case 1:
			for y := 0; y < m.curY; y++ {
				for x := 0; x < m.cols; x++ {
					m.grid[y][x] = ' '
				}
			}
			for x := 0; x <= min(m.curX, m.cols-1); x++ {
				m.grid[m.curY][x] = ' '
			}
		case 2, 3:
			m.grid = makeGrid(m.cols, m.rows)
			if mode == 3 {
				m.scroll = nil
			}
		}
	case 'K':
		mode := 0
		if len(nums) > 0 {
			mode = nums[0]
		}
		switch mode {
		case 0:
			for x := m.curX; x < m.cols; x++ {
				m.grid[m.curY][x] = ' '
			}
		case 1:
			for x := 0; x <= min(m.curX, m.cols-1); x++ {
				m.grid[m.curY][x] = ' '
			}
		case 2:
			for x := 0; x < m.cols; x++ {
				m.grid[m.curY][x] = ' '
			}
		}
	case 'P':
		n := paramAt(nums, 0, 1)
		row := m.grid[m.curY]
		if m.curX < m.cols {
			end := min(m.curX+n, m.cols)
			copy(row[m.curX:], row[end:])
			for x := m.cols - (end - m.curX); x < m.cols; x++ {
				row[x] = ' '
			}
		}
	case 'h':
		if priv && (paramAt(nums, 0, 0) == 1049 || paramAt(nums, 0, 0) == 47) && !m.inAlt {
			m.inAlt = true
			m.altGrid, m.altX, m.altY = m.grid, m.curX, m.curY
			m.grid = makeGrid(m.cols, m.rows)
			m.curX, m.curY = 0, 0
		}
	case 'l':
		if priv && (paramAt(nums, 0, 0) == 1049 || paramAt(nums, 0, 0) == 47) && m.inAlt {
			m.inAlt = false
			if m.altGrid != nil {
				m.grid, m.curX, m.curY = m.altGrid, m.altX, m.altY
				m.altGrid = nil
			}
		}
	}
}

func (m *miniTerm) Lines() ([]string, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := len(m.scroll) + m.rows
	out := make([]string, 0, total)
	for _, r := range m.scroll {
		out = append(out, strings.TrimRight(string(r), " "))
	}
	for y, r := range m.grid {
		line := r
		if y == m.curY && m.curX >= 0 && m.curX < len(r) {
			cp := make([]rune, len(r))
			copy(cp, r)
			if cp[m.curX] == ' ' {
				cp[m.curX] = '▋'
			}
			line = cp
		}
		out = append(out, strings.TrimRight(string(line), " "))
	}
	return out, m.cols, m.rows
}

// mobileTermSession manages an SSH shell + SFTP/SCP session on Android.
type mobileTermSession struct {
	app     *App
	device  string
	name    string
	address string
	conn    *sshTerm
	term    *miniTerm
	ctrl    bool
	alt     bool
	input   string
	files   *remoteFiles
	filesOn bool
	panel   filesPanel
	state   string // "connecting", "form", "hostkey", "connected", "ended"
	errNote string
	changed string
	trust   string
	form    credForm
	mu      sync.Mutex
}

func (s *mobileTermSession) writeBytes(b []byte) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_, _ = conn.Write(b)
	}
}

func (s *mobileTermSession) sendText(txt string) {
	if txt == "" {
		return
	}
	if len(txt) == 1 && (s.ctrl || s.alt) {
		ch := txt[0]
		var out []byte
		if s.alt {
			out = append(out, 0x1B)
		}
		if s.ctrl {
			if ch >= 'a' && ch <= 'z' {
				ch = ch - 'a' + 1
			} else if ch >= 'A' && ch <= 'Z' {
				ch = ch - 'A' + 1
			}
		}
		out = append(out, ch)
		s.ctrl, s.alt = false, false
		s.writeBytes(out)
		return
	}
	s.writeBytes([]byte(txt))
}

func (s *mobileTermSession) sendSpecial(seq string) {
	mygo.Haptics.Selection()
	if s.alt {
		seq = "\x1b" + seq
	}
	s.ctrl, s.alt = false, false
	s.writeBytes([]byte(seq))
}

func (s *mobileTermSession) viewExtraBar(c *ui.Context, key string) {
	t := c.Theme()
	extra := func(id, label string, on bool, tap func()) {
		bg := t.SurfaceHover
		fg := t.Text
		if on {
			bg = t.Accent
			fg = ui.RGB(255, 255, 255)
		}
		b := ui.ButtonBase(c.Key(key + "-ex-" + id)).MinWidth(44).Height(38).PaddingX(10).Radius(8).Background(bg).Center().Label(label)
		b.Children(func() { ui.Text(c, label).FontSize(13).FontWeight(600).TextColor(fg).NoWrap() })
		if b.Clicked() {
			tap()
		}
	}
	ui.ScrollHorizontal(c.Key(key+"-extrabar")).FillWidth().Shrink(0).Background(t.Surface).Padding(4, 6).Gap(4).Children(func() {
		extra("esc", "Esc", false, func() { s.sendSpecial("\x1b") })
		extra("tab", "Tab", false, func() { s.sendSpecial("\t") })
		extra("ctrl", "Ctrl", s.ctrl, func() { s.ctrl = !s.ctrl })
		extra("alt", "Alt", s.alt, func() { s.alt = !s.alt })
		extra("left", "←", false, func() { s.sendSpecial("\x1b[D") })
		extra("up", "↑", false, func() { s.sendSpecial("\x1b[A") })
		extra("down", "↓", false, func() { s.sendSpecial("\x1b[B") })
		extra("right", "→", false, func() { s.sendSpecial("\x1b[C") })
		for _, ch := range []string{"|", "~", "/", "-", "_", "$"} {
			ch := ch
			extra("ch-"+ch, ch, false, func() { s.sendText(ch) })
		}
		extra("home", "Home", false, func() { s.sendSpecial("\x1b[H") })
		extra("end", "End", false, func() { s.sendSpecial("\x1b[F") })
		extra("pgup", "PgUp", false, func() { s.sendSpecial("\x1b[5~") })
		extra("pgdn", "PgDn", false, func() { s.sendSpecial("\x1b[6~") })
	})
}
