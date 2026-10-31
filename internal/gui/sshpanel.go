package gui

import (
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// filesPanel is an SSH pane's files panel as it shows (doc/en-us/gui.md,
// "Files over SSH"): Files or Transfers, the folder shown and the folders
// open in it, the entry chosen, one being renamed or deleted, the path
// typed for SCP, and why the last change failed. Main thread only.
type filesPanel struct {
	open bool
	tab  int
	// root is the folder the tree starts at ("" the home folder).
	root     string
	expanded map[string]bool
	chosen   string
	renaming string
	name     string
	deleting *remoteAt
	scpPath  string
	note     string
	// restToo answers a name taken for the rest of its batch too.
	restToo bool
}

// remoteAt is an entry of the server by its path.
type remoteAt struct {
	path string
	dir  bool
}

// The panel's tabs.
const (
	tabFiles = iota
	tabTransfers
	tabSettings
)

// headButtons are an SSH pane's own header buttons: its files panel.
func (s *serviceTab) headButtons(c *ui.Context) {
	sc, ok := s.running().(*sshClient)
	if !ok {
		return
	}
	key := s.tabKey()
	t := c.Theme()
	files := ui.ButtonBase(c.Key("head-files-"+key)).Size(paneButton, paneButton).Radius(7).Center().Shrink(0).Label(L.FilesPanel()).Tooltip(L.FilesPanel())
	ink := t.TextMuted
	switch {
	case sc.panel.open:
		files.Background(t.Accent.Alpha(0.12))
		ink = t.Accent
	case files.Hovered():
		files.Background(t.SurfaceHover)
	}
	files.Children(func() { ui.Icon(c, sshFiles).FontSize(navIconSize).TextColor(ink) })
	if files.Clicked() {
		sc.panel.open = !sc.panel.open
	}
}

// lookView is a device's terminal look: its own choice for each part, or
// the default from Settings, and Use the defaults for all of them.
func (s *serviceTab) lookView(c *ui.Context) {
	t := c.Theme()
	a := s.app
	a.mu.Lock()
	own, base := a.prefs.Terminals[s.device], a.prefs.Terminal
	a.mu.Unlock()
	ui.Text(c, L.TermLookFor(deviceShown(s.name))).Bold()
	ui.Text(c, L.TermLookHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	l, changed := termLookForm(c, "look-"+s.tabKey(), own, &base, func(id, title string, control func()) {
		ui.Column(c.Key(id)).Gap(4).FillWidth().Children(func() {
			ui.Text(c, title).FontSize(sizeSmall).TextColor(t.TextMuted)
			control()
		})
	})
	ui.Text(c, L.TermPreview()).FontSize(sizeSmall).TextColor(t.TextMuted)
	termPreview(c, "look-preview-"+s.tabKey(), l.over(base), true)
	if changed {
		a.setTermLook(s.device, l)
	}
	ui.Text(c, L.TermCursorLater()).FontSize(sizeSmall).TextColor(t.TextMuted)
	if ui.Button(c.Key("look-clear-"+s.tabKey()), L.TermUseDefaults()).Disabled(own == (TermLook{})).Clicked() {
		a.setTermLook(s.device, TermLook{})
	}
}

// panelView is the files panel: beside the terminal, or over its right
// side (over) where the pane is too narrow for both.
func (c *sshClient) panelView(ctx *ui.Context, key string, over bool) {
	t := ctx.Theme()
	p := &c.panel
	r := c.remote()
	if !over {
		ui.Box(ctx.Key("ssh-files-line-" + key)).Width(1).FillHeight().Background(t.Border)
	}
	col := ui.Column(ctx.Key("ssh-files-" + key)).Width(filesPanelW).FillHeight().Background(t.Background)
	if over {
		col.Absolute().Top(0).Right(0).Bottom(0).Shadow(-8, 0, 24, 0, sheetShadow)
	}
	col.Children(func() {
		running := 0
		if r != nil {
			for _, x := range r.list() {
				if x.state == xferWaiting || x.state == xferRunning {
					running++
				}
			}
		}
		transfers := L.FilesTransfers()
		if running > 0 {
			transfers = L.FilesTransfersCount(running)
		}
		ui.Box(ctx).Padding(12, 12, 8, 12).FillWidth().Children(func() {
			panelTabs(ctx, "ssh-files-tab-"+key, &p.tab, L.FilesTab(), transfers, L.FilesSettings())
		})
		switch {
		case p.tab == tabSettings:
			ui.Scroll(ctx.Key("ssh-look-"+key)).Grow(1).FillWidth().MinHeight(0).Padding(4, 12, 12, 12).Gap(10).Children(func() {
				c.s.lookView(ctx)
			})
		case r == nil:
			ui.Row(ctx).Padding(12).Gap(8).AlignItems(ui.Center).Children(func() {
				busyMark(ctx, L.FilesOpening(), busySize)
				ui.Text(ctx, L.FilesOpening()).TextColor(t.TextMuted)
			})
		case p.tab == tabTransfers:
			c.transfersView(ctx, key, r)
		case r.sftp == nil:
			c.scpView(ctx, key, r)
		default:
			c.browseView(ctx, key, r)
		}
	})
	if r != nil {
		c.questionView(ctx, key, r)
	}
}

// browseView is the Files tab over SFTP: where it is, what can be done
// there, the tree, and where files dropped go.
func (c *sshClient) browseView(ctx *ui.Context, key string, r *remoteFiles) {
	t := ctx.Theme()
	p := &c.panel
	if p.root == "" {
		p.root = r.home
	}
	if p.expanded == nil {
		p.expanded = map[string]bool{}
	}
	app := c.s.app
	app.mu.Lock()
	hidden := app.prefs.HiddenFiles
	app.mu.Unlock()
	ui.Row(ctx.Key("ssh-files-bar-"+key)).Padding(0, 8, 8, 12).Gap(2).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Row(ctx).Grow(1).Basis(0).MinWidth(0).Gap(2).AlignItems(ui.Center).Clip().Children(func() {
			c.crumbs(ctx, key, r.home)
		})
		if iconButton(ctx, "ssh-files-up-"+key, filesUp, L.FilesUp()) && p.root != "/" {
			p.root = path.Dir(p.root)
		}
		if iconButton(ctx, "ssh-files-mkdir-"+key, filesNewDir, L.FilesNewFolder()) {
			c.newFolder(r, p.root)
		}
		if iconButton(ctx, "ssh-files-upload-"+key, filesUpload, L.FilesUpload()) {
			c.chooseUpload(r, p.root)
		}
		if iconButton(ctx, "ssh-files-refresh-"+key, rowRefresh, L.FilesRefresh()) {
			r.read(p.root)
			for d := range p.expanded {
				r.read(d)
			}
		}
		eye, label := rowEye, L.FilesShowHidden()
		if hidden {
			eye, label = rowEyeOff, L.FilesHideHidden()
		}
		if iconButton(ctx, "ssh-files-hidden-"+key, eye, label) {
			app.mu.Lock()
			pr := app.prefs
			app.mu.Unlock()
			pr.HiddenFiles = !hidden
			_ = app.setPrefs(pr)
		}
	})
	if p.note != "" {
		ui.Text(ctx, p.note).FontSize(sizeSmall).TextColor(textTone(t, t.Danger)).Padding(0, 12, 8, 12)
	}
	marks := transferMarks(r.list())
	tree := ui.Scroll(ctx.Key("ssh-files-tree-"+key)).Grow(1).FillWidth().MinHeight(0).Padding(0, 6)
	if files := tree.DroppedFiles(); len(files) > 0 {
		r.upload(files, p.root)
		p.tab = tabTransfers
	}
	dropOver := ""
	tree.Children(func() {
		dropOver = c.treeRows(ctx, key, r, p.root, 0, hidden, marks)
	})
	switch {
	case dropOver != "":
		dropOver = L.FilesDropInto(path.Base(dropOver))
	case tree.FileDragOver():
		dropOver = L.FilesDropInto(path.Base(p.root))
	}
	hint, edge, fill, ink := L.FilesDropHint(), t.Border, t.Background, t.TextMuted
	if dropOver != "" {
		hint, edge, fill, ink = dropOver, t.Accent, t.Accent.Alpha(0.12), textTone(t, t.Accent)
	}
	ui.Row(ctx.Key("ssh-files-drop-"+key)).Margin(8, 12, 12, 12).Padding(10, 12).Gap(8).AlignItems(ui.Center).
		Radius(chipRadius).Border(1, edge).Background(fill).Children(func() {
		ui.Icon(ctx, filesUpload).FontSize(navIconSize).TextColor(ink)
		ui.Text(ctx, hint).FontSize(sizeSmall).TextColor(ink)
	})
}

// crumbs are the folder shown as a path from the home folder ("~"), each
// folder on the way a button to it.
func (c *sshClient) crumbs(ctx *ui.Context, key, home string) {
	t := ctx.Theme()
	p := &c.panel
	type crumb struct{ name, at string }
	var way []crumb
	rest := p.root
	switch {
	case rest == home:
		way = []crumb{{"~", home}}
		rest = ""
	case strings.HasPrefix(rest, home+"/"):
		way = []crumb{{"~", home}}
		rest = strings.TrimPrefix(rest, home)
	default:
		way = []crumb{{"/", "/"}}
	}
	at := strings.TrimSuffix(way[0].at, "/")
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			at += "/" + part
			way = append(way, crumb{part, at})
		}
	}
	// The folders nearest the shown one stay: the path is cut at its start.
	if len(way) > 3 {
		way = append([]crumb{{"…", way[len(way)-3].at}}, way[len(way)-2:]...)
	}
	for i, w := range way {
		if i > 0 {
			ui.Text(ctx, "/").TextColor(t.TextMuted)
		}
		last := i == len(way)-1
		b := ui.ButtonBase(ctx.Key("ssh-crumb-"+key+"-"+w.at)).Padding(2, 4).Radius(5).MinWidth(0).Shrink(1).Clip().Label(w.at)
		if b.Hovered() {
			b.Background(t.SurfaceHover)
		}
		b.Children(func() {
			tx := ui.Text(ctx, w.name).SingleLine().TextColor(t.TextMuted)
			if last {
				tx.Bold().TextColor(t.Text)
			}
		})
		if b.Clicked() {
			p.root = w.at
		}
	}
}

// treeRows are a folder's entries in the tree, the open folders' inside
// them.
// Returns: the folder files are dragged over, "" when none.
func (c *sshClient) treeRows(ctx *ui.Context, key string, r *remoteFiles, dir string, depth int, hidden bool, marks map[string]string) string {
	t := ctx.Theme()
	p := &c.panel
	d := r.snapshot(dir)
	pad := 4 + float32(depth)*filesIndent
	if d.err != nil {
		ui.Text(ctx, fileError(d.err)).FontSize(sizeSmall).TextColor(textTone(t, t.Danger)).Padding(4, 8, 4, pad+20)
		return ""
	}
	if d.loading && d.entries == nil {
		ui.Text(ctx, L.FilesReading()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(4, 8, 4, pad+20)
		return ""
	}
	if len(d.entries) == 0 && depth == 0 {
		ui.Text(ctx, L.FilesEmpty()).FontSize(sizeSmall).TextColor(t.TextMuted).Padding(8, 12)
	}
	over := ""
	for _, e := range d.entries {
		if !hidden && strings.HasPrefix(e.name, ".") {
			continue
		}
		at := path.Join(dir, e.name)
		open := e.dir && p.expanded[at]
		row := ui.ButtonBase(ctx.Key("ssh-f-"+key+"-"+at)).Height(filesRowH).FillWidth().Padding(0, 8, 0, pad).Gap(6).
			AlignItems(ui.Center).Radius(8).Label(e.name)
		if e.dir {
			if files := row.DroppedFiles(); len(files) > 0 {
				r.upload(files, at)
				p.tab = tabTransfers
			}
			if row.FileDragOver() {
				over = at
				row.Background(t.Accent.Alpha(0.12)).Border(2, t.Accent)
			}
		}
		switch {
		case over == at:
		case p.chosen == at:
			row.Background(t.Accent.Alpha(0.12))
		case row.Hovered():
			row.Background(t.SurfaceHover)
		}
		row.ContextMenu(func(m *ui.Menu) { c.entryMenu(m, r, at, e.dir) })
		row.Children(func() {
			ui.Box(ctx).Width(14).Center().Shrink(0).Children(func() {
				switch {
				case open:
					ui.Icon(ctx, aiChevron).FontSize(12).TextColor(t.TextMuted)
				case e.dir:
					ui.Icon(ctx, aiChevronRight).FontSize(12).TextColor(t.TextMuted)
				}
			})
			if e.dir {
				ui.Icon(ctx, rowFolder).FontSize(navIconSize).TextColor(t.Accent)
			} else {
				ui.Icon(ctx, filesFile).FontSize(navIconSize).TextColor(t.TextMuted)
			}
			if p.renaming == at {
				in := ui.TextInput(ctx.Key("ssh-rename-"+key), &p.name).Label(L.FilesRename()).Grow(1).Basis(0).MinWidth(0).AutoFocus()
				switch {
				case in.Submitted():
					c.rename(r, at, p.name)
				case in.Shortcut(0, ui.KeyEscape):
					p.renaming = ""
				}
				return
			}
			ui.Box(ctx).Grow(1).Basis(0).MinWidth(0).Clip().Children(func() {
				ui.Text(ctx, e.name).SingleLine()
			})
			if !e.dir {
				ui.Text(ctx, sizeLabel(e.size)).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
			}
			if m := marks[at]; m != "" {
				ink := textTone(t, t.Accent)
				switch m {
				case "↓":
					ink = textTone(t, t.Success)
				case "!":
					ink = textTone(t, t.Danger)
				}
				ui.Text(ctx, m).FontSize(sizeSmall).Bold().TextColor(ink).Tooltip(markLabel(m))
			}
		})
		if row.Tooltip(entryTip(e)).Clicked() {
			p.chosen = at
			if e.dir {
				p.expanded[at] = !open
				if !open {
					r.read(at)
				}
			}
		}
		if e.dir && row.DoubleClicked() {
			p.root = at
		}
		if open {
			if o := c.treeRows(ctx, key, r, at, depth+1, hidden, marks); o != "" {
				over = o
			}
		}
	}
	return over
}

// entryTip is an entry's size and when it last changed.
func entryTip(e remoteEntry) string {
	when := e.mod.Local().Format(time.DateTime)
	if e.dir {
		return L.FilesChangedAt(when)
	}
	return L.FilesSizeAt(sizeLabel(e.size), when)
}

// transferMarks are the tree's marks by remote path: ↑ uploading, ↓
// downloaded, ! a transfer that failed; the latest transfer of a path
// marks it.
func transferMarks(xs []*fileXfer) map[string]string {
	m := map[string]string{}
	for _, x := range xs {
		at := x.src
		if x.up {
			at = path.Join(x.dst, x.name)
		}
		switch x.state {
		case xferRunning, xferWaiting:
			if x.up {
				m[at] = "↑"
			} else {
				m[at] = "↓"
			}
		case xferDone:
			if x.up {
				delete(m, at)
			} else {
				m[at] = "↓"
			}
		case xferFailed:
			m[at] = "!"
		default:
			delete(m, at)
		}
	}
	return m
}

// markLabel words a tree's mark.
func markLabel(m string) string {
	switch m {
	case "↑":
		return L.FilesMarkUp()
	case "!":
		return L.FilesMarkFailed()
	}
	return L.FilesMarkDown()
}

// entryMenu is an entry's right-click menu.
func (c *sshClient) entryMenu(m *ui.Menu, r *remoteFiles, at string, dir bool) {
	p := &c.panel
	p.chosen = at
	if m.Item(L.FilesDownload()).Chosen() {
		c.downloadTo(r, at, "")
	}
	if m.Item(L.FilesDownloadTo()).Chosen() {
		go func() {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: L.FilesDownloadTo(), Directory: true, CreateDirectories: true})
			if err == nil && len(paths) > 0 {
				c.s.update(func() { c.downloadTo(r, at, paths[0]) })
			}
		}()
	}
	if dir {
		m.Separator()
		if m.Item(L.FilesUploadHere()).Chosen() {
			c.chooseUpload(r, at)
		}
		if m.Item(L.FilesNewFolder()).Chosen() {
			c.newFolder(r, at)
		}
	}
	m.Separator()
	if m.Item(L.FilesRename()).Chosen() {
		p.renaming, p.name = at, path.Base(at)
	}
	if m.Item(L.FilesCopyPath()).Chosen() {
		clipboardWrite(at)
	}
	if m.Item(L.FilesOpenInTerminal()).Chosen() {
		folder := at
		if !dir {
			folder = path.Dir(at)
		}
		c.term.Send([]byte(cdCommand(folder, c.windows) + "\r"))
	}
	m.Separator()
	if m.Item(L.FilesDeleteAsk()).Chosen() {
		p.deleting = &remoteAt{at, dir}
	}
}

// cdCommand is the shell command that goes to a folder: on Windows, from
// SFTP's /C:/path, cd "C:\path" (cmd and PowerShell alike).
func cdCommand(folder string, windows bool) string {
	if windows {
		f := strings.TrimPrefix(folder, "/")
		return `cd "` + strings.ReplaceAll(f, "/", `\`) + `"`
	}
	return "cd " + scpQuote(folder, false)
}

// downloadTo downloads an entry into a local folder: Downloads for "".
func (c *sshClient) downloadTo(r *remoteFiles, at, local string) {
	if local == "" {
		d, err := downloadsDir()
		if err != nil {
			c.panel.note = fileError(err)
			return
		}
		local = d
	}
	r.download([]string{at}, local)
	c.panel.tab = tabTransfers
}

// chooseUpload asks for files to upload into a remote folder.
func (c *sshClient) chooseUpload(r *remoteFiles, to string) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: L.FilesUpload(), Multiple: true})
		if err == nil && len(paths) > 0 {
			c.s.update(func() {
				r.upload(paths, to)
				c.panel.tab = tabTransfers
			})
		}
	}()
}

// newFolder makes a folder in parent and starts naming it.
func (c *sshClient) newFolder(r *remoteFiles, parent string) {
	go func() {
		at, err := r.mkdir(parent, L.FilesNewFolderName())
		c.s.update(func() {
			p := &c.panel
			if err != nil {
				p.note = fileError(err)
				return
			}
			p.note = ""
			if parent != p.root {
				p.expanded[parent] = true
			}
			p.chosen, p.renaming, p.name = at, at, path.Base(at)
		})
	}()
}

// rename gives an entry the name typed.
func (c *sshClient) rename(r *remoteFiles, at, name string) {
	p := &c.panel
	p.renaming = ""
	name = strings.TrimSpace(name)
	if name == path.Base(at) {
		return
	}
	if !validName(name) {
		p.note = L.FilesBadName()
		return
	}
	go func() {
		err := r.rename(at, name)
		c.s.update(func() {
			p.note = ""
			if err != nil {
				p.note = fileError(err)
				return
			}
			p.chosen = path.Join(path.Dir(at), name)
		})
	}()
}

// scpView is the Files tab where the server has no SFTP: a path typed,
// downloaded, or files chosen and uploaded into its folder.
func (c *sshClient) scpView(ctx *ui.Context, key string, r *remoteFiles) {
	t := ctx.Theme()
	p := &c.panel
	ui.Column(ctx.Key("ssh-scp-"+key)).Padding(4, 12, 12, 12).Gap(12).FillWidth().Children(func() {
		ui.Column(ctx).Padding(12).Gap(4).Radius(chipRadius).Background(t.Surface).FillWidth().Children(func() {
			ui.Text(ctx, L.FilesNoSFTP()).Bold()
			ui.Text(ctx, L.FilesNoSFTPHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		in := ui.TextInput(ctx.Key("ssh-scp-path-"+key), &p.scpPath).Label(L.FilesPathOn(deviceShown(c.s.name))).Placeholder("~/").FillWidth()
		path := strings.TrimSpace(p.scpPath)
		ui.Row(ctx).Gap(8).FillWidth().Children(func() {
			if ui.PrimaryButton(ctx.Key("ssh-scp-get-"+key), L.FilesDownload()).Grow(1).Disabled(path == "").Clicked() || in.Submitted() && path != "" {
				c.downloadTo(r, path, "")
			}
			if ui.Button(ctx.Key("ssh-scp-put-"+key), L.FilesUploadHere()).Grow(1).Disabled(path == "").Clicked() {
				to := path
				if !strings.HasSuffix(to, "/") {
					to = pathDir(to)
				}
				c.chooseUpload(r, to)
			}
		})
		ui.Text(ctx, L.FilesSCPHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	})
}

// pathDir is the folder of a path typed for SCP, "~" for a bare name.
func pathDir(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return "~"
}

// transfersView lists the transfers, newest first, with Cancel, Show in
// folder or Try again, and Clear finished.
func (c *sshClient) transfersView(ctx *ui.Context, key string, r *remoteFiles) {
	t := ctx.Theme()
	xs := r.list()
	ui.Scroll(ctx.Key("ssh-xfers-"+key)).Grow(1).FillWidth().MinHeight(0).Padding(4, 12, 12, 12).Gap(6).Children(func() {
		if len(xs) == 0 {
			ui.Text(ctx, L.FilesNoTransfers()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for i := len(xs) - 1; i >= 0; i-- {
			c.transferRow(ctx, key, r, xs[i], i)
		}
	})
	ui.Row(ctx).Padding(0, 12, 12, 12).Children(func() {
		if ui.Button(ctx.Key("ssh-xfers-clear-"+key), L.FilesClearFinished()).Clicked() {
			r.clearFinished()
		}
	})
}

// transferRow is one transfer: its direction and name, where it goes, its
// progress, and what can be done with it.
func (c *sshClient) transferRow(ctx *ui.Context, key string, r *remoteFiles, x *fileXfer, i int) {
	t := ctx.Theme()
	r.mu.Lock()
	state, why, at := x.state, x.err, x.at
	r.mu.Unlock()
	size, done := x.size.Load(), x.done.Load()
	arrow, where, ink := filesDown, L.FilesTo(x.dst), textTone(t, t.Success)
	if x.up {
		arrow, ink = filesUpload, textTone(t, t.Accent)
	}
	id := key + "-" + strconv.Itoa(i)
	ui.Column(ctx.Key("ssh-xfer-"+id)).Padding(10, 12).Gap(6).Radius(chipRadius).Background(t.Surface).FillWidth().Children(func() {
		ui.Row(ctx).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Icon(ctx, arrow).FontSize(navIconSize).TextColor(ink)
			ui.Box(ctx).Grow(1).Basis(0).MinWidth(0).Clip().Children(func() { ui.Text(ctx, x.name).Bold().SingleLine() })
			switch state {
			case xferWaiting, xferRunning:
				if ui.Button(ctx.Key("ssh-xfer-stop-"+id), L.ActionCancel()).Clicked() {
					x.cancel()
				}
			case xferDone:
				if at != "" && !x.up && ui.Button(ctx.Key("ssh-xfer-show-"+id), L.FilesShowInFolder()).Clicked() {
					ctx.OpenURL(fileURL(filepath.Dir(at)))
				}
			default:
				if ui.Button(ctx.Key("ssh-xfer-again-"+id), L.FilesTryAgain()).Clicked() {
					r.again(x)
				}
			}
		})
		ui.Text(ctx, where).FontSize(sizeSmall).TextColor(t.TextMuted).SingleLine()
		frac := float32(0)
		if size > 0 {
			frac = min(float32(done)/float32(size), 1)
		}
		if state == xferDone {
			frac = 1
		}
		ui.Progress(ctx, float64(frac)).FillWidth()
		word, amount := L.FilesWaiting(), sizeLabel(size)
		switch state {
		case xferRunning:
			if x.up {
				word = L.FilesUploading()
			} else {
				word = L.FilesDownloading()
			}
			amount = L.FilesOf(sizeLabel(done), sizeLabel(size))
			if secs := time.Since(x.started).Seconds(); secs >= 1 {
				amount += " · " + sizeLabel(int64(float64(done)/secs)) + "/s"
			}
		case xferDone:
			word = L.FilesDone()
		case xferCanceled:
			word = L.FilesCanceled()
		case xferFailed:
			word = why
		}
		ui.Row(ctx).FillWidth().Children(func() {
			w := ui.Text(ctx, word).FontSize(sizeSmall).Grow(1)
			if state == xferFailed {
				w.TextColor(textTone(t, t.Danger))
			}
			ui.Text(ctx, amount).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
	})
}

// questionView asks over the pane what a transfer waits on (a name taken),
// or whether to delete what was chosen.
func (c *sshClient) questionView(ctx *ui.Context, key string, r *remoteFiles) {
	p := &c.panel
	q := r.question()
	if q == nil && p.deleting == nil {
		return
	}
	t := ctx.Theme()
	ui.Box(ctx.Key("ssh-ask-" + key)).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(paletteScrim).Center().Padding(16).Children(func() {
		ui.Column(ctx.Key("ssh-ask-card-"+key)).FillWidth().MaxWidth(connectPanelW).Padding(20).Gap(12).Radius(paneRadius).Background(t.Background).
			Border(1, t.Border).Shadow(0, 16, 40, 0, sheetShadow).Children(func() {
			if q != nil {
				c.conflictView(ctx, key, r, q)
				return
			}
			d := p.deleting
			ui.Text(ctx, L.FilesDeleteTitle(path.Base(d.path), deviceShown(c.s.name))).FontSize(sizeHeading).Bold()
			ui.Text(ctx, L.FilesDeleteHint()).TextColor(t.TextMuted)
			ui.Row(ctx).Gap(8).FillWidth().Children(func() {
				ui.Row(ctx).Grow(1)
				if ui.Button(ctx.Key("ssh-del-no-"+key), L.ActionCancel()).Clicked() {
					p.deleting = nil
				}
				if ui.Button(ctx.Key("ssh-del-yes-"+key), L.FilesDelete()).Clicked() {
					p.deleting = nil
					go func() {
						err := r.remove(d.path, d.dir)
						c.s.update(func() {
							p.note = ""
							if err != nil {
								p.note = fileError(err)
							}
						})
					}()
				}
			})
		})
	})
}

// conflictView asks what to do with a name taken: Replace, Keep both or
// Skip, for this one or for the rest of what it came with.
func (c *sshClient) conflictView(ctx *ui.Context, key string, r *remoteFiles, q *fileQuestion) {
	t := ctx.Theme()
	where := q.where
	if where == "" {
		where = "/"
	}
	ui.Text(ctx, L.FilesTaken(q.name, path.Base(filepath.ToSlash(where)))).FontSize(sizeHeading).Bold()
	ui.Text(ctx, L.FilesTakenHint(sizeWhen(q.there), sizeWhen(q.coming))).TextColor(t.TextMuted)
	labelled(ctx, func() {
		ui.Switch(ctx.Key("ssh-ask-rest-"+key), &c.panel.restToo).Label(L.FilesForTheRest())
	}, L.FilesForTheRest())
	ui.Row(ctx).Gap(8).FillWidth().Children(func() {
		ui.Row(ctx).Grow(1)
		answer := func(policy string) {
			r.answer(fileAnswer{policy: policy, rest: c.panel.restToo})
			c.panel.restToo = false
		}
		if ui.Button(ctx.Key("ssh-ask-skip-"+key), L.FilesSkip()).Clicked() {
			answer(policySkip)
		}
		if ui.Button(ctx.Key("ssh-ask-both-"+key), L.FilesKeepBoth()).Clicked() {
			answer(policyBoth)
		}
		if ui.PrimaryButton(ctx.Key("ssh-ask-replace-"+key), L.FilesReplace()).Clicked() {
			answer(policyReplace)
		}
	})
}

// sizeWhen words an entry by its size and when it changed.
func sizeWhen(e remoteEntry) string {
	when := ""
	if !e.mod.IsZero() {
		when = e.mod.Local().Format(time.DateTime)
	}
	switch {
	case e.dir && when == "":
		return L.FilesFolder()
	case e.dir:
		return L.FilesFolderAt(when)
	case when == "":
		return sizeLabel(e.size)
	}
	return L.FilesSizeAt(sizeLabel(e.size), when)
}

// deviceShown is a device's name without its Device ID.
func deviceShown(name string) string {
	shown, _ := splitTitle(name)
	return shown
}

// fileURL is a local folder as a file: URL, which the system opens.
func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// panelTabs are the files panel's tabs: a segmented choice across its
// whole width, each tab as wide as the others.
func panelTabs(ctx *ui.Context, key string, tab *int, labels ...string) {
	t := ctx.Theme()
	parts := ui.SegmentedBase(ctx.Key(key), tab, len(labels))
	parts.Track.FillWidth().Padding(3).Gap(2).Radius(chipRadius).Background(t.Surface).Border(1, t.Border).Label(L.FilesPanel()).Children(func() {
		for i, label := range labels {
			s := parts.Segment(i).Grow(1).Basis(0).MinWidth(0).Height(filesTabH).Center().Radius(chipRadius - 2)
			switch {
			case i == *tab:
				s.Background(t.Background).Shadow(0, 1, 2, 0, paneShadow)
			case s.Hovered():
				s.Background(t.SurfaceHover)
			}
			s.Children(func() { ui.Text(ctx, label).SingleLine() })
		}
	})
}
