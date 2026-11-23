//go:build !android

package gui

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func init() { serviceDials[serviceSSH] = dialSSH }

// sshClient is a running SSH pane: MyGo's terminal on the shell, in the
// look chosen for the device, with its find bar, beside the files panel
// while it shows (sshpanel.go); pic is the terminal's view. find and
// panel are the main thread's.
type sshClient struct {
	s       *serviceTab
	term    *terminal.Terminal
	conn    *sshTerm
	pic     ui.Handle
	windows bool
	// title is what the program running set as the window's title.
	title atomic.Pointer[string]
	// files are the server's files once the panel first opened them.
	files   atomic.Pointer[remoteFiles]
	opening sync.Once
	find    findBar
	panel   filesPanel
}

// findBar is the terminal's find bar: open, what it looks for, and how
// many it found.
type findBar struct {
	open   bool
	text   string
	status terminal.SearchStatus
}

// newTerminal starts a terminal (tests replace it: the terminal loads
// libghostty-vt).
var newTerminal = terminal.New

// shownTitle is the title the program running set, "" when none.
func (c *sshClient) shownTitle() string {
	if t := c.title.Load(); t != nil {
		return *t
	}
	return ""
}

// findKey reports whether a key opens the find bar: ⌘F on macOS,
// Ctrl+Shift+F elsewhere (Ctrl+F is the shell's).
func findKey(mods ui.Modifiers, key ui.Key) bool {
	if key != ui.KeyF {
		return false
	}
	if runtime.GOOS == "darwin" {
		return mods == ui.Super
	}
	return mods == ui.Ctrl|ui.Shift
}

// remote starts reading the server's files the first time it is asked,
// and returns them once SFTP answered (or SCP alone stands), else nil.
func (c *sshClient) remote() *remoteFiles {
	c.opening.Do(func() {
		go func() {
			c.files.Store(openRemoteFiles(c.conn.client, c.windows, c.s.redraw))
			c.s.redraw()
		}()
	})
	return c.files.Load()
}

func (c *sshClient) view(ctx *ui.Context, s *serviceTab) {
	key := s.tabKey()
	body := ui.Row(ctx.Key("ssh-" + key)).Fill()
	w := body.LayoutBounds().W
	over := c.panel.open && w > 0 && w < filesPanelW+filesTermMinW
	body.Children(func() {
		ui.Box(ctx.Key("ssh-term-" + key)).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
			terminal.View(ctx, c.term).Fill().AutoFocus().Bind(&c.pic)
			c.findView(ctx, key)
		})
		if c.panel.open && !over {
			c.panelView(ctx, key, false)
		}
		if over {
			c.panelView(ctx, key, true)
		}
	})
}

// findView is the find bar over the terminal's top right: what to find,
// how many there are, the next and the one before, and close; Escape
// closes it.
func (c *sshClient) findView(ctx *ui.Context, key string) {
	f := &c.find
	if !f.open {
		return
	}
	t := ctx.Theme()
	ui.Row(ctx.Key("ssh-find-"+key)).Absolute().Top(8).Right(12).Width(findBarW).Padding(4, 4, 4, 8).Gap(2).AlignItems(ui.Center).
		Radius(cardRadius).Background(t.Background).Border(1, t.Border).Shadow(0, 4, 16, 0, sheetShadow).Children(func() {
		in := ui.TextInput(ctx.Key("ssh-find-in-"+key), &f.text).Placeholder(L.TermFind()).Label(L.TermFind()).Grow(1).Basis(0).MinWidth(0).AutoFocus()
		if in.Changed() {
			c.term.Search(f.text)
		}
		if in.Submitted() {
			c.term.SearchNext()
		}
		count := ""
		switch {
		case f.text == "":
		case f.status.Total == 0 && !f.status.Searching:
			count = L.TermFindNone()
		default:
			count = L.TermFindCount(f.status.Selected, f.status.Total)
		}
		ui.Text(ctx, count).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
		if iconButton(ctx, "ssh-find-prev-"+key, filesUp, L.TermFindPrevious()) {
			c.term.SearchPrevious()
		}
		if iconButton(ctx, "ssh-find-next-"+key, filesDown, L.TermFindNext()) {
			c.term.SearchNext()
		}
		if iconButton(ctx, "ssh-find-close-"+key, paneClose, L.ActionClose()) || in.Shortcut(0, ui.KeyEscape) {
			c.endFind()
		}
	})
}

// endFind closes the find bar and its search.
func (c *sshClient) endFind() {
	c.find = findBar{}
	c.term.EndSearch()
}

func (c *sshClient) close() {
	if f := c.files.Load(); f != nil {
		f.close()
	}
	if c.term != nil {
		_ = c.term.Close()
	}
	_ = c.conn.Close()
}

func (c *sshClient) grabs() bool        { return false }
func (c *sshClient) scan(core.KeyEvent) {}
func (c *sshClient) setFront(bool)      {}
func (c *sshClient) keys() *ui.Handle   { return &c.pic }
func (c *sshClient) sendInput(p []byte) {
	if c.term != nil {
		c.term.Send(p)
	}
}

// dialSSH is the SSH tab's serviceDial.
func dialSSH(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	saved := s.savedCredential(ctx)
	plan, ok := s.ask(typed, saved)
	if !ok {
		return nil, errNeedSignIn
	}
	pinned := ""
	if saved != nil {
		pinned = saved.HostKey
	}
	s.mu.Lock()
	trust := s.hostKey
	s.mu.Unlock()
	conn, seen, err := sshDial(ctx, s.app.link, s.device, s.address, plan, typed != nil, pinned, trust)
	if errors.Is(err, errHostKeyChanged) {
		s.mu.Lock()
		s.changed = seen
		s.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	c := &sshClient{s: s, conn: conn, windows: s.app.deviceOS(s.device) == "windows"}
	look := s.app.termLook(s.device)
	light, dark := look.themes()
	cursor, still := look.cursor()
	term, err := newTerminal(terminal.Options{
		Conn: conn, Theme: light, DarkTheme: dark, Font: look.font(), Cursor: cursor, NoBlink: still,
		MinimumContrast: termContrast,
		OnExit:          func(int) { s.ended(c, L.ServiceEnded()) },
		OnTitle: func(title string) {
			title = cleanTitle(title, sshTitleMax)
			c.title.Store(&title)
			s.redraw()
		},
		// A program's notice (OSC 9, OSC 777) shows as Kyokai's, naming
		// the pane it came from.
		OnNotify: func(title, body string) {
			text := cleanTitle(strings.TrimSpace(title+" "+body), sshNoticeMax)
			if text != "" {
				s.app.update(func() { s.app.setToast(L.TermNotice(s.title, text), false) })
			}
		},
		OnKey: func(mods ui.Modifiers, key ui.Key) bool {
			if !findKey(mods, key) {
				return false
			}
			c.find.open = true
			return true
		},
		OnSearch: func(st terminal.SearchStatus) { c.find.status = st },
		// A program may not write the clipboard (OSC 52): a server could
		// swap what this user copied before a paste.
		NoClipboardWrite: true,
		// Command or Control+click opens only web and mail links; any other
		// target (a file, a share, another scheme) is swallowed, since a
		// program names it and the system would run it.
		OpenLink: func(link string) bool { return !ui.SafeToOpen(link) },
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.term = term
	return c, nil
}
