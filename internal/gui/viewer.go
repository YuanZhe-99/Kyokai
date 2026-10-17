package gui

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"net"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// Media packet kinds between the engine and the GUI (the core's
// media::packet).
const (
	packetVideoStart byte = 1
	packetFrame      byte = 2
	packetControl    byte = 3
)

// videoStart describes the stream that follows (media::VideoStart).
type videoStart struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// viewer shows one controller session's screen in its own window. Fields
// after mu are guarded by it; the bitmap changes only on the main thread.
type viewer struct {
	app    *App
	id     uint64
	device string
	win    *mygo.Window
	conn   net.Conn

	mu      sync.Mutex
	start   videoStart
	bm      *ui.Bitmap
	imgs    [2]*image.RGBA // the one on screen and the one being filled
	front   int
	state   string // "", or why nothing shows (ended, unsupported)
	fps     float64
	kbps    float64
	frames  int
	bytes   int
	counted time.Time
}

// openViewer shows the viewer of a session, opening it the first time.
func (a *App) openViewer(s localapi.SessionInfo) {
	a.mu.Lock()
	v, open := a.viewers[s.ID]
	a.mu.Unlock()
	if open {
		v.win.Focus()
		return
	}
	v = &viewer{app: a, id: s.ID, device: s.DeviceID, counted: time.Now()}
	v.win = mygo.NewWindow(mygo.WindowOptions{
		Title: L.ViewerTitle(groupDigits(s.DeviceID)), Width: 1280, Height: 760, MinWidth: 480, MinHeight: 320,
		Content: ui.View(v.view),
	})
	v.win.OnClosed(func() {
		a.mu.Lock()
		delete(a.viewers, s.ID)
		a.mu.Unlock()
		v.mu.Lock()
		conn := v.conn
		v.mu.Unlock()
		if conn != nil {
			conn.Close()
		}
	})
	a.mu.Lock()
	a.viewers[s.ID] = v
	a.mu.Unlock()
	go v.pump()
}

// pump reads the session's media until the stream or the window ends.
// Side effects: asks the host for a key frame first, so a (re)opened viewer
// starts with a complete picture.
func (v *viewer) pump() {
	conn, err := localapi.NewClient(v.app.socket).Media(v.app.ctx, v.id)
	if err != nil {
		v.setState(L.ViewerEnded())
		return
	}
	v.mu.Lock()
	v.conn = conn
	v.mu.Unlock()
	defer conn.Close()
	_ = localapi.WritePacket(conn, append([]byte{packetControl}, `{"type":"key-frame"}`...))
	var buf []byte
	for {
		p, err := localapi.ReadPacket(conn, buf)
		if err != nil {
			v.setState(L.ViewerEnded())
			return
		}
		buf = p
		if len(p) == 0 {
			continue
		}
		switch p[0] {
		case packetVideoStart:
			var s videoStart
			if json.Unmarshal(p[1:], &s) != nil || s.Width <= 0 || s.Height <= 0 || s.Width > 16384 || s.Height > 16384 {
				continue
			}
			v.begin(s)
		case packetFrame:
			if len(p) < 10 {
				continue
			}
			v.show(p[10:], binary.BigEndian.Uint64(p[2:10]))
		}
	}
}

// begin prepares the pictures for a new stream.
func (v *viewer) begin(s videoStart) {
	v.win.Update(func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.start = s
		v.state = ""
		if s.Codec != "raw-rgba" {
			v.state = L.ViewerUnsupported(s.Codec)
			return
		}
		r := image.Rect(0, 0, s.Width, s.Height)
		v.imgs = [2]*image.RGBA{image.NewRGBA(r), image.NewRGBA(r)}
		v.bm = ui.NewBitmap(v.imgs[0])
		v.front = 0
	})
}

// show puts one frame on screen: it fills the hidden picture, then swaps
// it in on the main thread.
func (v *viewer) show(data []byte, _ uint64) {
	v.mu.Lock()
	if v.bm == nil || v.start.Codec != "raw-rgba" || len(data) != v.start.Width*v.start.Height*4 {
		v.mu.Unlock()
		return
	}
	back := v.imgs[1-v.front]
	copy(back.Pix, data)
	v.frames++
	v.bytes += len(data)
	if el := time.Since(v.counted); el >= time.Second {
		v.fps = float64(v.frames) / el.Seconds()
		v.kbps = float64(v.bytes) * 8 / 1000 / el.Seconds()
		v.frames, v.bytes, v.counted = 0, 0, time.Now()
	}
	v.mu.Unlock()
	done := make(chan struct{})
	v.win.Update(func() {
		defer close(done)
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.bm == nil {
			return
		}
		v.bm.Update(back)
		v.front = 1 - v.front
	})
	// The hidden picture is reused only after the bitmap took this one.
	<-done
}

func (v *viewer) setState(s string) {
	v.win.Update(func() {
		v.mu.Lock()
		v.state = s
		v.mu.Unlock()
	})
}

// view is the viewer window: the picture scaled to fit, and a status line.
func (v *viewer) view(c *ui.Context) {
	t := c.Theme()
	v.mu.Lock()
	bm, state, start, fps, kbps := v.bm, v.state, v.start, v.fps, v.kbps
	v.mu.Unlock()
	ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
		ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			switch {
			case state != "":
				ui.Text(c, state).TextColor(ui.Hex("#ffffff"))
			case bm == nil:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, L.ViewerWaiting()).TextColor(ui.Hex("#ffffff"))
				})
			default:
				ui.Image(c, bm).Fit(ui.Contain).Grow(1).FillWidth()
			}
		})
		ui.Row(c).FillWidth().Padding(6, 12).Gap(16).Background(t.Surface).Children(func() {
			ui.Text(c, groupDigits(v.device)).FontSize(sizeSmall).Grow(1)
			if start.Width > 0 {
				ui.Text(c, fmt.Sprintf("%d×%d · %s · %.0f fps · %.0f kbit/s", start.Width, start.Height, start.Codec, fps, kbps)).
					FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})
	})
}
