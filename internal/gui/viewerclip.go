package gui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// packetClipboard is what crossed a session's clipboard (the core's
// media::packet::CLIPBOARD).
const packetClipboard byte = 13

// The clipboard panel's history: entries kept per tab, and the largest
// thumbnail of an image.
const (
	clipKeep   = 30
	clipThumbW = 240
	clipThumbH = 160
	// clipPreview is how much of a text the panel shows.
	clipPreview = 600
)

// viewerClip is the viewer's clipboard state (guarded by viewer.mu): whether
// the session may share it, the person's switch, whether the tab is the one
// in front, the panel, and what crossed while it was in front.
type viewerClip struct {
	// may: the session was granted `clipboard` or `files`.
	may bool
	// off: the person stopped sharing it with this device (kept for it).
	off bool
	// front: the tab is shown in the Kyokai window used last, so it alone
	// exchanges the clipboard.
	front bool
	// shown pulls the panel out.
	shown   bool
	history []clipEntry
}

// clipEntry is one thing that crossed: text, an image (as a thumbnail) or
// files, from the host or from this computer, and when.
type clipEntry struct {
	host  bool
	at    time.Time
	text  string
	thumb *ui.Bitmap
	// The image's size, or the files' names, count and total size.
	width, height int
	names         []string
	files         int
	size          int64
}

// clipPacket is the body of a clipboard packet.
type clipPacket struct {
	From  string `json:"from"`
	At    int64  `json:"at"`
	Text  string `json:"text"`
	PNG   string `json:"png"`
	Files []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		Dir  bool   `json:"dir"`
	} `json:"files"`
}

// clipPacketIn takes a clipboard packet into the history; an image is
// decoded and shrunk off the reader's goroutine, so video goes on.
func (v *viewer) clipPacketIn(body []byte) {
	var p clipPacket
	if json.Unmarshal(body, &p) != nil {
		return
	}
	e := clipEntry{host: p.From == "host", at: time.UnixMilli(p.At), text: p.Text}
	for _, f := range p.Files {
		if !strings.Contains(f.Path, "/") {
			e.names = append(e.names, f.Path)
		}
		if !f.Dir {
			e.files++
			e.size += f.Size
		}
	}
	if p.PNG == "" {
		v.update(func() { v.addClip(e) })
		return
	}
	go func() {
		thumb, w, h, ok := thumbnail(p.PNG)
		if !ok {
			return
		}
		e.width, e.height = w, h
		v.update(func() {
			e.thumb = ui.NewBitmap(thumb)
			v.addClip(e)
		})
	}()
}

// addClip puts an entry in the history, newest first by when it crossed,
// keeping clipKeep. Notes: call on the main thread.
func (v *viewer) addClip(e clipEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	h := v.clip.history
	i := slices.IndexFunc(h, func(o clipEntry) bool { return o.at.Before(e.at) })
	if i < 0 {
		i = len(h)
	}
	h = slices.Insert(h, i, e)
	if len(h) > clipKeep {
		h = h[:clipKeep]
	}
	v.clip.history = h
}

// thumbnail decodes a base64 PNG and shrinks it to fit clipThumbW ×
// clipThumbH, each pixel the average of the ones it covers.
// Returns: the thumbnail and the image's own size.
func thumbnail(b64 string) (*image.RGBA, int, int, bool) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, 0, 0, false
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, false
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, false
	}
	scale := min(1, float64(clipThumbW)/float64(w), float64(clipThumbH)/float64(h))
	tw, th := max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		y0, y1 := y*h/th, max((y+1)*h/th, y*h/th+1)
		for x := range tw {
			x0, x1 := x*w/tw, max((x+1)*w/tw, x*w/tw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = byte(r/n>>8), byte(g/n>>8), byte(bl/n>>8), byte(a/n>>8)
		}
	}
	return dst, w, h, true
}

// clipUsePacket is the viewer's clipboard choice for its engine, which
// keeps it on this side: whether this session shares the clipboard, and
// whether its tab is in front.
// Notes: the caller holds v.mu.
func (v *viewer) clipUsePacket() []byte {
	body, _ := json.Marshal(map[string]any{"type": "clipboard-use", "share": !v.clip.off, "front": v.clip.front})
	return append([]byte{packetControl}, body...)
}

// setFront makes the tab the one in front, or not, and tells the engine.
func (v *viewer) setFront(on bool) {
	v.mu.Lock()
	if v.clip.front == on {
		v.mu.Unlock()
		return
	}
	v.clip.front = on
	p := v.clipUsePacket()
	v.mu.Unlock()
	select {
	case v.out <- p:
	default:
	}
}

// setClipShare shares the clipboard with this device, or stops, kept for
// the device, and tells the engine.
func (v *viewer) setClipShare(on bool) {
	v.mu.Lock()
	v.clip.off = !on
	p := v.clipUsePacket()
	v.mu.Unlock()
	v.keepSettings()
	select {
	case v.out <- p:
	default:
	}
}

// syncFront makes the tab shown in the Kyokai window used last the one in
// front, which alone exchanges the clipboard (none while that window shows
// the Kyokai tab). Notes: runs whenever a window gets the focus, a tab is
// chosen, added, moved or closed.
func (a *App) syncFront() {
	a.mu.Lock()
	w := a.lastWin
	a.mu.Unlock()
	var front tab
	if w != nil {
		w.mu.Lock()
		front = w.shown()
		w.mu.Unlock()
	}
	for _, v := range a.allTabs() {
		v.setFront(v == front)
	}
}

// watchFocus records a window as the one used last when it gets the focus.
func (a *App) watchFocus(vw *viewerWindow) {
	vw.win.OnFocus(func() {
		a.mu.Lock()
		a.lastWin = vw
		a.mu.Unlock()
		a.syncFront()
	})
}

// clipPanel is the side panel of what crossed the clipboard with this
// device while its tab was in front, with the switch that shares it.
func (v *viewer) clipPanel(c *ui.Context) {
	t := c.Theme()
	v.mu.Lock()
	st, name := v.clip, v.title
	st.history = slices.Clone(st.history)
	v.mu.Unlock()
	// The device by its name alone; its Device ID is in the tab and the bar.
	name, _, _ = strings.Cut(name, " · ")
	ui.Scroll(c).Width(clipPanelW).FillHeight().Background(t.Surface).Padding(cardPad).Gap(gap).Children(func() {
		ui.Text(c, L.ViewerClipboard()).FontSize(sizeHeading).Bold()
		share := !st.off
		labelled(c, func() {
			if ui.Switch(c.Key("clip-share"), &share).Label(L.ViewerClipShare()).Changed() {
				v.setClipShare(share)
			}
		}, L.ViewerClipShare())
		ui.Text(c, L.ViewerClipHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if share && !st.front {
			ui.Text(c, L.ViewerClipBehind()).FontSize(sizeSmall).TextColor(textTone(t, t.Warning))
		}
		if len(st.history) == 0 {
			ui.Text(c, L.ViewerClipEmpty()).FontSize(sizeSmall).TextColor(t.TextMuted)
			return
		}
		if ui.Button(c.Key("clip-clear"), L.ViewerClipClear()).Clicked() {
			v.mu.Lock()
			v.clip.history = nil
			v.mu.Unlock()
		}
		for i, e := range st.history {
			key := "clip-" + strconv.FormatInt(e.at.UnixNano(), 36) + "-" + strconv.Itoa(i)
			ui.Column(c.Key(key)).Gap(6).FillWidth().Padding(10).Radius(8).Border(1, t.Border).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
					from := "↑ " + L.ViewerClipFromHere()
					if e.host {
						from = "↓ " + L.ViewerClipFromHost(name)
					}
					ui.Text(c, from).FontSize(sizeSmall).Bold().Grow(1)
					ui.Text(c, e.at.Local().Format("15:04:05")).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				})
				switch {
				case e.thumb != nil:
					w, h := e.thumb.Size()
					ui.Image(c, e.thumb).Width(float32(w)).Height(float32(h)).Radius(4)
					ui.Text(c, fmt.Sprintf("%d × %d", e.width, e.height)).FontSize(sizeSmall).TextColor(t.TextMuted)
				case e.files > 0 || len(e.names) > 0:
					ui.Text(c, strings.Join(e.names, ", ")).FontSize(sizeSmall).Selectable()
					ui.Text(c, L.ViewerClipFiles(e.files, sizeLabel(e.size))).FontSize(sizeSmall).TextColor(t.TextMuted)
				default:
					preview := e.text
					if len(preview) > clipPreview {
						preview = strings.ToValidUTF8(preview[:clipPreview], "") + "…"
					}
					ui.Text(c, preview).FontSize(sizeSmall).Selectable()
					if ui.Button(c.Key(key+"-copy"), L.ActionCopy()).Clicked() {
						mygo.Clipboard.WriteText(e.text)
					}
				}
			})
		}
	})
}

// sizeLabel writes a number of bytes as people read it: "820 B", "2.1 MB".
func sizeLabel(n int64) string {
	const unit = 1000
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	f, i := float64(n), 0
	for f >= unit && i < 4 {
		f /= unit
		i++
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + []string{"B", "kB", "MB", "GB", "TB"}[i]
}
