package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// Media packet kinds between the engine and the GUI (the core's
// media::packet).
const (
	packetVideoStart byte = 1
	packetFrame      byte = 2
	packetControl    byte = 3
	packetStats      byte = 4
	packetCursor     byte = 7
	packetDisplays   byte = 8
	packetActions    byte = 9
	packetCodecs     byte = 11
)

// keyFrame flags a frame that decodes on its own (media::KEY_FRAME).
const keyFrame byte = 1

// hostActions are the session actions the host offers (media ACTIONS),
// sent only to a session that may control input.
type hostActions struct {
	CtrlAltDel bool `json:"ctrl_alt_del"`
	Blocked    bool `json:"blocked"`
	Privacy    bool `json:"privacy"`
}

// hostStats is what the host's QoS measured and chose (media STATS).
type hostStats struct {
	DelayMS int `json:"delay_ms"`
	FPS     int `json:"fps"`
	Bitrate int `json:"bitrate"`
	// EncodeMS is how long the host takes from a picture to its frame.
	EncodeMS int `json:"encode_ms"`
}

// hostPointer is where the host's pointer is (media CURSOR), as fractions
// of the picture.
type hostPointer struct {
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Visible bool    `json:"visible"`
}

// hostDisplays are the host's displays and the one shown (media DISPLAYS).
type hostDisplays struct {
	List []struct {
		Width   int  `json:"width"`
		Height  int  `json:"height"`
		Primary bool `json:"primary"`
	} `json:"list"`
	Current int `json:"current"`
}

// videoStart describes the stream that follows (media::VideoStart).
type videoStart struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	I444   bool   `json:"i444"`
	// Encoder is the host's encoder and Hardware whether it runs in
	// hardware, as the encoder reports.
	Encoder  string `json:"encoder"`
	Hardware bool   `json:"hardware"`
}

// codecImpl is which encoder or decoder handles a stream ("" unknown) and
// whether it runs in hardware.
type codecImpl struct {
	name     string
	hardware bool
}

// coreLib loads the core library into the GUI once, for the viewers'
// decoders: next to kyokai-svc, or KYOKAI_CORE_LIB in development.
var coreLib = sync.OnceValues(func() (*core.Lib, error) {
	if p := os.Getenv("KYOKAI_CORE_LIB"); p != "" {
		return core.Load(p)
	}
	svc, err := engineBinary()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(core.LibraryPath(filepath.Dir(svc)))
	if err != nil {
		return nil, err
	}
	return core.Load(abs)
})

// viewer is the connection to one device, shown as a tab of a viewer
// window (viewerwin.go), which it can move between: from Connecting…,
// through waiting for the device's user (with a password field), to its
// screen, and when it ends why, with Reconnect. Fields after mu are guarded
// by it; the bitmap changes only on the main thread.
type viewer struct {
	app    *App
	device string
	// sid is the session the connection is now; a reconnect makes another.
	sid atomic.Uint64
	// title names the device: its friendly name and Device ID.
	title string
	// want is what the connection asks the device for.
	want []string
	// home is the window the tab is in now; nil once the viewer closed.
	home atomic.Pointer[viewerWindow]
	conn net.Conn
	// out queues control packets (input) for the media connection's writer.
	out chan []byte
	// moved wakes the writer for a pending pointer move (viewerinput.go).
	moved chan struct{}

	mu    sync.Mutex
	start videoStart
	bm    *ui.Bitmap
	// The stream's pictures: the one the bitmap shows (-1: none of them,
	// as a new stream starts), the newest decoded one waiting for the main
	// thread (-1: none), and the third, which the next frame decodes into.
	imgs    [3]*image.RGBA
	shown   int
	ready   int
	readyAt time.Time
	state   string // "", or why nothing shows (ended, unsupported)
	// The last second's frame rate, bitrate, and decode and draw times;
	// the counts and sums of the second going on.
	fps, kbps, decodeMS, drawMS float64
	frames, drawn, bytes        int
	decoding, drawing           time.Duration
	counted                     time.Time
	host                        hostStats
	// The viewer's quality choice: preset index (speed, balanced, best),
	// frame-rate cap and full colour (4:4:4).
	preset int
	fpsCap string
	full   bool
	// The codecs the host offers ({codecs}, media CODECS) and the viewer's
	// choice among them ("auto" or a codec's name).
	offer []string
	codec string
	// decoder is this computer's decoder of the stream, as it reports
	// after its first picture.
	decoder codecImpl
	// swap exchanges Ctrl and Cmd (the Windows key) in what is sent; nil
	// follows swapDefault.
	swap *bool
	// Input: the picture element's box, whether this session may control
	// input, and the keys sent down.
	box     ui.Rect
	control bool
	held    map[ui.Key]bool
	// The newest pointer move the writer has not sent yet.
	move *inputEvent
	// The keyboard grab (viewerinput.go): the mode the core grabs keys in for
	// this window ("" for none), whether the core refused to, and the
	// window's native handle the grab is for.
	grabbed string
	noGrab  bool
	window  uintptr
	// The host's pointer, which the picture leaves out.
	pointer hostPointer
	// The host's displays, to choose which one shows.
	displays hostDisplays
	// The host's session actions; nil until it offers them.
	actions *hostActions
	// The AI panel (viewerai.go).
	ai viewerAI
	// The connection's progress as the engine reports it, and whether the
	// stream is running (pump).
	phase   localapi.SessionInfo
	pumping bool
	// The tab's password field while the device waits or refused, and
	// whether to remember what is typed there.
	password string
	remember bool
}

// session is the connection's current session id (0 before the first).
func (v *viewer) session() uint64 { return v.sid.Load() }

// viewerFor is the connection to a device, or nil.
func (a *App) viewerFor(device string) *viewer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.viewers[device]
}

// connectDevice opens the connection to a device as a tab of the main
// window, or shows its tab when it is open; a connection that ended
// connects again. want is what to ask for (nil: viewing, input, the
// clipboard and files).
// Notes: call on the main thread; one connection per device.
func (a *App) connectDevice(device string, about localapi.About, want []string) {
	if want == nil {
		want = connectWant(false)
	}
	if v := a.viewerFor(device); v != nil {
		if w := v.home.Load(); w != nil {
			w.show(v)
		}
		if !v.live() {
			v.redial("", false)
		}
		return
	}
	v := a.newViewer(device, about, want)
	a.main.add(v)
	v.redial("", false)
}

// newViewer makes a device's connection, not yet dialed, and registers it.
func (a *App) newViewer(device string, about localapi.About, want []string) *viewer {
	v := &viewer{app: a, device: device, title: deviceTitle(about, device), want: want,
		counted: time.Now(), out: make(chan []byte, 256), moved: make(chan struct{}, 1), preset: 1, fpsCap: "30 fps", codec: "auto"}
	// The settings chosen for this device last time.
	if vp, ok := a.viewerPrefs(device); ok {
		v.preset, v.full, v.swap = min(max(vp.Preset, 0), 2), vp.Full, vp.Swap
		if slices.Contains(fpsCaps, vp.FPS) {
			v.fpsCap = vp.FPS
		}
		if vp.Codec != "" {
			v.codec = vp.Codec
		}
	}
	a.mu.Lock()
	a.viewers[device] = v
	a.mu.Unlock()
	return v
}

// live reports whether the connection is under way or connected (not
// ended or failed).
func (v *viewer) live() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch v.phase.State {
	case "", "closed", "failed":
		return v.pumping
	}
	return true
}

// redial connects to the device anew: with a password typed in the tab,
// else the saved one (if any), else asking the device's user. A session
// still waiting is given up first, so the device has one at a time.
// Side effects: the tab follows the new session.
func (v *viewer) redial(password string, remember bool) {
	old := v.session()
	v.mu.Lock()
	v.phase = localapi.SessionInfo{State: "connecting", DeviceID: v.device}
	v.state, v.bm, v.start, v.password = "", nil, videoStart{}, ""
	want := v.want
	v.mu.Unlock()
	go func() {
		if old != 0 {
			_, _ = v.app.requestWith(localapi.Request{Type: localapi.RequestClose, ID: old})
		}
		resp, err := v.app.requestWith(localapi.Request{Type: localapi.RequestConnect, Connect: &localapi.ConnectRequest{
			DeviceID: v.device, Password: password, Want: want, Remember: remember,
		}})
		v.update(func() {
			if err != nil {
				var refused *localapi.RequestError
				msg := L.EngineDown()
				if errors.As(err, &refused) {
					msg = refused.Message
				}
				v.mu.Lock()
				v.phase = localapi.SessionInfo{State: "failed", DeviceID: v.device}
				v.state = msg
				v.mu.Unlock()
				return
			}
			v.sid.Store(resp.ID)
		})
	}()
}

// syncViewers follows the engine's sessions in the tabs: each connection
// takes its session's progress, starts its stream once connected, and a
// connection made elsewhere (another window, ctl) gets a tab of its own.
// Notes: call on the main thread.
func (a *App) syncViewers() {
	a.mu.Lock()
	var sessions []localapi.SessionInfo
	if a.status != nil {
		sessions = a.status.Sessions
	}
	a.mu.Unlock()
	for _, s := range sessions {
		if s.Role != "controller" {
			continue
		}
		v := a.viewerFor(s.DeviceID)
		if v == nil {
			if s.State != "connected" {
				continue
			}
			v = a.newViewer(s.DeviceID, s.Peer, s.Granted)
			v.sid.Store(s.ID)
			a.main.add(v)
		}
		if v.session() != s.ID {
			// An older session of the device, or another made elsewhere
			// while this connection is not live: follow the live one.
			if v.live() || s.State != "connected" {
				continue
			}
			v.sid.Store(s.ID)
		}
		v.mu.Lock()
		v.phase = s
		if s.Peer.Name != "" {
			v.title = deviceTitle(s.Peer, s.DeviceID)
		}
		start := s.State == "connected" && !v.pumping
		if start {
			v.pumping = true
			v.control = slices.Contains(s.Granted, localapi.PermControlInput)
			v.ai.run = slices.Contains(s.Granted, localapi.PermRunAITask)
			v.ai.approve = slices.Contains(s.Granted, localapi.PermApproveAIAction)
			v.state = ""
			if !slices.Contains(s.Granted, localapi.PermViewScreen) {
				v.state = L.ViewerNoPicture()
			}
		}
		v.mu.Unlock()
		if start {
			if w := v.home.Load(); w != nil {
				w.retitle()
			}
			go v.pump()
		}
	}
}

// close ends the viewer's stream as its tab closes: the keyboard grab, the
// media connection and its place among the open viewers. The session goes on.
func (v *viewer) close() {
	v.endGrab()
	v.home.Store(nil)
	v.app.mu.Lock()
	if v.app.viewers[v.device] == v {
		delete(v.app.viewers, v.device)
	}
	v.app.mu.Unlock()
	v.mu.Lock()
	conn := v.conn
	v.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// update runs fn on the main thread for the window the tab is in now, and
// redraws it; nothing once the viewer closed.
func (v *viewer) update(fn func()) {
	if w := v.home.Load(); w != nil {
		w.win.Update(fn)
	}
}

// pump reads the session's media until the stream or the window ends,
// decoding each frame with a decoder of the GUI's core.
// Side effects: asks the host for a key frame first, so a (re)opened viewer
// starts with a complete picture.
// Notes: runs on one locked OS thread, which the decoder is tied to.
func (v *viewer) pump() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lib, err := coreLib()
	if err != nil {
		v.setState(L.ViewerNoDecoder())
		return
	}
	var dec *core.Decoder
	// fresh: the decoder has not had a key frame yet.
	fresh := false
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()
	defer func() {
		v.mu.Lock()
		v.pumping = false
		v.mu.Unlock()
	}()
	conn, err := localapi.NewClient(v.app.socket).Media(v.app.ctx, v.session())
	if err != nil {
		v.setState(L.ViewerEnded())
		return
	}
	v.mu.Lock()
	v.conn = conn
	v.mu.Unlock()
	defer conn.Close()
	_ = localapi.WritePacket(conn, append([]byte{packetControl}, `{"type":"key-frame"}`...))
	// The host follows what the bar shows, also for a new session.
	for _, p := range v.choicePackets() {
		_ = localapi.WritePacket(conn, p)
	}
	// The writer stops with the pump; v.out stays open, so a late input
	// event never sends on a closed channel.
	done := make(chan struct{})
	defer close(done)
	go v.writer(conn, done)
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
			// The same stream described again (a key frame was asked
			// for): a new group of pictures, which the decoder takes as
			// it is, without a blank picture between.
			v.mu.Lock()
			same := dec != nil && v.start == s
			v.mu.Unlock()
			if same {
				continue
			}
			if dec != nil {
				dec.Close()
				dec = nil
			}
			d, err := lib.OpenDecoder(p[1:])
			if err != nil {
				v.setState(L.ViewerUnsupported(codecLabel(s.Codec)))
				continue
			}
			dec, fresh = d, true
			v.begin(s)
		case packetDisplays:
			var d hostDisplays
			if json.Unmarshal(p[1:], &d) == nil {
				v.update(func() {
					v.mu.Lock()
					v.displays = d
					v.mu.Unlock()
				})
			}
		case packetCodecs:
			var o struct {
				Codecs []string `json:"codecs"`
			}
			if json.Unmarshal(p[1:], &o) == nil {
				v.update(func() {
					v.mu.Lock()
					v.offer = o.Codecs
					v.mu.Unlock()
				})
			}
		case packetActions:
			var a hostActions
			if json.Unmarshal(p[1:], &a) == nil {
				v.update(func() {
					v.mu.Lock()
					v.actions = &a
					v.mu.Unlock()
				})
			}
		case packetCursor:
			var ptr hostPointer
			if json.Unmarshal(p[1:], &ptr) == nil {
				v.update(func() {
					v.mu.Lock()
					v.pointer = ptr
					v.mu.Unlock()
				})
			}
		case packetAITask, packetAIError, packetAIDraft:
			v.aiPacket(p[0], p[1:])
		case packetStats:
			var s hostStats
			if json.Unmarshal(p[1:], &s) == nil {
				v.mu.Lock()
				v.host = s
				v.mu.Unlock()
			}
		case packetFrame:
			if len(p) < 10 {
				continue
			}
			// A new decoder starts at a key frame: the frames encoded before
			// the host's (they may still come after its stream description)
			// refer to pictures it never had.
			if fresh && p[1]&keyFrame == 0 {
				continue
			}
			first := fresh
			fresh = false
			if dec != nil {
				v.show(dec, p[10:])
				// The decoder knows how it decodes once it has a key frame.
				if first {
					name, hw, _ := dec.Info()
					v.update(func() {
						v.mu.Lock()
						v.decoder = codecImpl{name, hw}
						v.mu.Unlock()
					})
				}
			}
		}
	}
}

// begin prepares the pictures for a new stream, at once on the pump's
// goroutine, so the stream's first frame (its key frame) finds them; the
// bitmap keeps the old picture until the first new one is shown.
func (v *viewer) begin(s videoStart) {
	r := image.Rect(0, 0, s.Width, s.Height)
	imgs := [3]*image.RGBA{image.NewRGBA(r), image.NewRGBA(r), image.NewRGBA(r)}
	v.mu.Lock()
	v.start, v.imgs, v.shown, v.ready, v.decoder = s, imgs, -1, -1, codecImpl{}
	v.mu.Unlock()
	v.setState("")
}

// show decodes one frame into the picture neither shown nor waiting, which
// becomes the waiting one, and has the main thread show the newest one
// waiting: decoding never waits for drawing, and a picture not drawn yet
// gives way to a newer one. Frames the decoder holds (or cannot decode)
// show nothing.
// Notes: the bitmap takes a picture without a copy, so the one shown is
// never decoded into.
func (v *viewer) show(dec *core.Decoder, data []byte) {
	v.mu.Lock()
	free := 0
	for free == v.shown || free == v.ready {
		free++
	}
	pic := v.imgs[free]
	v.bytes += len(data)
	v.mu.Unlock()
	if pic == nil {
		return
	}
	began := time.Now()
	w, h, ok, err := dec.Decode(data, pic.Pix)
	if err != nil || !ok || w != pic.Rect.Dx() || h != pic.Rect.Dy() {
		return
	}
	decoded := time.Now()
	v.mu.Lock()
	v.frames++
	v.decoding += decoded.Sub(began)
	v.ready, v.readyAt = free, decoded
	if el := decoded.Sub(v.counted); el >= time.Second {
		v.fps = float64(v.frames) / el.Seconds()
		v.kbps = float64(v.bytes) * 8 / 1000 / el.Seconds()
		v.decodeMS = v.decoding.Seconds() * 1000 / float64(max(v.frames, 1))
		v.drawMS = v.drawing.Seconds() * 1000 / float64(max(v.drawn, 1))
		v.frames, v.drawn, v.bytes, v.counted = 0, 0, 0, decoded
		v.decoding, v.drawing = 0, 0
	}
	v.mu.Unlock()
	v.update(func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.ready < 0 {
			return // a newer update showed it already
		}
		img := v.imgs[v.ready]
		if v.bm == nil {
			v.bm = ui.NewBitmap(img)
		} else {
			v.bm.Update(img)
		}
		v.drawing += time.Since(v.readyAt)
		v.drawn++
		v.shown, v.ready = v.ready, -1
	})
}

func (v *viewer) setState(s string) {
	v.update(func() {
		v.mu.Lock()
		v.state = s
		v.mu.Unlock()
	})
}

// view is the viewer window: the picture scaled to fit, the AI panel when
// the session may run AI tasks, and a status line.
func (v *viewer) view(c *ui.Context) {
	v.mu.Lock()
	phase, pumping := v.phase, v.pumping
	v.mu.Unlock()
	if !pumping && phase.State != "connected" {
		// No picture, no keys: the grab ends with the connection.
		v.syncGrab(false)
		v.connectPanel(c, phase)
		return
	}
	v.mu.Lock()
	bm, state, start := v.bm, v.state, v.start
	withAI, ptr, actions := v.ai.aiShown(), v.pointer, v.actions
	b := bar{start: start, fps: v.fps, kbps: v.kbps, decodeMS: v.decodeMS, drawMS: v.drawMS, host: v.host,
		preset: v.preset, fpsCap: v.fpsCap, full: v.full, displays: v.displays, offer: v.offer, codec: v.codec, decoder: v.decoder}
	v.mu.Unlock()
	b.showPtr = v.app == nil || v.app.showPointer()
	if !b.showPtr {
		ptr.Visible = false
	}
	if v.app != nil {
		b.path = v.app.pathTo(v.device)
	}
	ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
		if actions != nil {
			v.actionBar(c, *actions)
		}
		ui.Row(c).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
			v.picture(c, bm, state, start, ptr)
			if withAI {
				v.aiPanel(c)
			}
		})
		v.statusBar(c, b)
	})
}

// connectPanel is the tab before and after the picture: connecting,
// waiting for the device's user (or its password), why it ended, and
// Reconnect.
func (v *viewer) connectPanel(c *ui.Context, phase localapi.SessionInfo) {
	t := c.Theme()
	v.mu.Lock()
	pw, remember, note := v.password, v.remember, v.state
	v.mu.Unlock()
	var behind helperGap
	if v.app != nil {
		behind = v.app.outdatedHelper()
	}
	edit := func(e ui.Element) {
		if e.Changed() {
			v.mu.Lock()
			v.password, v.remember = pw, remember
			v.mu.Unlock()
		}
	}
	ui.Column(c).Fill().Background(t.Background).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		card(c).Width(connectPanelW).Children(func() {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, phase.Peer)
				ui.Text(c, v.title).FontSize(sizeHeading).Bold()
			})
			waiting, ended := false, false
			switch phase.State {
			case "", "connecting", "reconnecting":
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, sessionState(phase, behind)).TextColor(t.TextMuted)
				})
			case "confirming":
				waiting = true
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, L.SessionConfirmingThere()).TextColor(t.TextMuted)
				})
			default:
				ended = true
				text := note
				if text == "" {
					text = sessionState(phase, behind)
				}
				ui.Text(c, text).TextColor(t.Danger)
			}
			if !waiting && !ended {
				return
			}
			label := L.ViewerPasswordHint()
			if ended {
				label = L.ViewerPasswordAgain()
			}
			ui.Text(c, label).FontSize(sizeSmall).TextColor(t.TextMuted)
			submit := false
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("tab-pw"), &pw).Password().Grow(1)
				edit(in)
				if in.Submitted() && pw != "" {
					submit = true
				}
				label := L.ActionConnect()
				if ended && pw == "" {
					label = L.ViewerReconnect()
				}
				if ui.PrimaryButton(c.Key("tab-go"), label).Disabled(waiting && pw == "").Clicked() {
					submit = true
				}
			})
			labelled(c, func() {
				edit(ui.Switch(c.Key("tab-remember"), &remember).Label(L.ConnectRemember()))
			}, L.ConnectRemember())
			if submit {
				v.redial(pw, remember)
			}
		})
	})
}

// picture is the host's screen scaled to fit, with the host's pointer
// marked on it, or why it does not show.
func (v *viewer) picture(c *ui.Context, bm *ui.Bitmap, state string, start videoStart, ptr hostPointer) {
	area := ui.Column(c.Key("picture-area")).Grow(1).FillHeight().AlignItems(ui.Center).Justify(ui.Center)
	at := area.LayoutBounds()
	focused := false
	defer func() { v.syncGrab(focused) }()
	area.Children(func() {
		switch {
		case state != "":
			ui.Text(c, state).TextColor(ui.Hex("#ffffff"))
		case bm == nil:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, L.ViewerWaiting()).TextColor(ui.Hex("#ffffff"))
			})
		default:
			// The picture takes text: macOS turns keys into text (and input
			// methods compose) only for an element that does, and Legacy
			// mode sends letters as that text (regression: no key reached a
			// Mac's host from a Mac). Candidate windows open at its corner.
			img := ui.Image(c.Key("picture"), bm).Fit(ui.Contain).Grow(1).FillWidth().Focusable().
				TextCaret(ui.Rect{W: 1, H: 16}).HandleInput(v.sendInput)
			focused = img.Focused()
			box := img.LayoutBounds()
			v.mu.Lock()
			v.box = box
			v.mu.Unlock()
			// The marker lets the pointer through, so it never takes a click.
			if ox, oy, w, h := pictureBox(box, start.Width, start.Height); ptr.Visible && w > 0 {
				x := box.X - at.X + ox + ptr.X*w
				y := box.Y - at.Y + oy + ptr.Y*h
				ui.Column(c.Key("pointer")).Absolute().Left(x-pointerR).Top(y-pointerR).
					Width(2*pointerR).Height(2*pointerR).Radius(pointerR).
					Background(ui.Hex("#ffffff").Alpha(0.85)).Border(2, ui.Hex("#000000")).PassThrough()
			}
		}
	})
}

// actionBar offers the host's session actions: lock its screen, send
// Ctrl+Alt+Del (helper modes only), block its keyboard and mouse, and
// privacy mode (which blocks them too). The switches show what the host
// reports, not what was asked.
func (v *viewer) actionBar(c *ui.Context, a hostActions) {
	t := c.Theme()
	action := func(name string) {
		v.sendControl(map[string]any{"type": "action", "action": name})
	}
	ui.Row(c).FillWidth().Padding(6, 12).Gap(12).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		if ui.Button(c.Key("act-lock"), L.ViewerLock()).Clicked() {
			action("lock")
		}
		if a.CtrlAltDel && ui.Button(c.Key("act-cad"), L.ViewerCtrlAltDel()).Clicked() {
			action("ctrl-alt-del")
		}
		blocked := a.Blocked
		labelled(c, func() {
			if ui.Switch(c.Key("act-block"), &blocked).Label(L.ViewerBlockInput()).Disabled(a.Privacy).Changed() {
				if blocked {
					action("block")
				} else {
					action("unblock")
				}
			}
		}, L.ViewerBlockInput())
		privacy := a.Privacy
		labelled(c, func() {
			if ui.Switch(c.Key("act-privacy"), &privacy).Label(L.ViewerPrivacy()).Changed() {
				if privacy {
					action("privacy-on")
				} else {
					action("privacy-off")
				}
			}
		}, L.ViewerPrivacy())
		if grabsKeyboard {
			ui.Row(c).Grow(1)
			ui.Text(c, L.ViewerKeyboard()).FontSize(sizeSmall).TextColor(t.TextMuted)
			mode := slices.Index(keyboardModes, v.app.keyboardMode())
			if ui.Segmented(c.Key("keyboard"), &mode, L.ViewerKeyboardMap(), L.ViewerKeyboardTranslate(), L.ViewerKeyboardLegacy()).
				Tooltip(L.ViewerKeyboardHint()).Changed() {
				v.app.setKeyboardMode(keyboardModes[mode])
			}
			// macOS refuses the tap without Kyokai's Accessibility
			// permission: the viewer works in Legacy mode meanwhile.
			v.mu.Lock()
			refused := v.noGrab
			v.mu.Unlock()
			if refused && runtime.GOOS == "darwin" {
				ui.Text(c, L.ViewerKeyboardNoGrab()).FontSize(sizeSmall).TextColor(t.Warning)
			}
		}
		// Ctrl and Cmd matter where a Mac is on either side.
		v.mu.Lock()
		swap, peerOS := v.swapping(), v.phase.Peer.OS
		v.mu.Unlock()
		if runtime.GOOS == "darwin" || peerOS == "macos" {
			labelled(c, func() {
				if ui.Switch(c.Key("swap-keys"), &swap).Label(L.ViewerSwapKeys()).Tooltip(L.ViewerSwapKeysHint()).Changed() {
					v.setSwap(swap)
				}
			}, L.ViewerSwapKeys())
		}
	})
}

// bar is what the status bar shows: the stream and its numbers, the
// viewer's choices, and the network's path to the device.
type bar struct {
	start                       videoStart
	fps, kbps, decodeMS, drawMS float64
	host                        hostStats
	preset                      int
	fpsCap                      string
	full, showPtr               bool
	displays                    hostDisplays
	offer                       []string
	codec                       string
	decoder                     codecImpl
	// path is the device's entry in the network status ("" Path when the
	// network lists no route to it).
	path localapi.NetworkDevice
}

// pathTo is how the network reaches a device now: direct or through the
// relay, with its latency.
func (a *App) pathTo(device string) localapi.NetworkDevice {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status == nil {
		return localapi.NetworkDevice{}
	}
	for _, d := range a.status.Network.Devices {
		if d.DeviceID == device {
			return d
		}
	}
	return localapi.NetworkDevice{}
}

// statusBar is the Device ID, the quality controls and the stream's
// numbers, with the path to the device and where the time goes.
func (v *viewer) statusBar(c *ui.Context, b bar) {
	t := c.Theme()
	start, fps, kbps, host, preset, fpsCap, full, displays, showPtr, offer, codec := b.start, b.fps, b.kbps, b.host, b.preset, b.fpsCap, b.full, b.displays, b.showPtr, b.offer, b.codec
	ui.Row(c).FillWidth().Padding(6, 12).Gap(16).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		ui.Text(c, groupDigits(v.device)).FontSize(sizeSmall).Grow(1)
		if n := len(displays.List); n > 1 {
			ui.Text(c, L.ViewerDisplay()).FontSize(sizeSmall).TextColor(t.TextMuted)
			labels := make([]string, n)
			for i := range labels {
				labels[i] = strconv.Itoa(i + 1)
			}
			current := displays.Current
			if ui.Segmented(c.Key("display"), &current, labels...).Changed() {
				v.sendControl(map[string]any{"type": "display", "index": current})
			}
		}
		if ui.Segmented(c.Key("quality"), &preset, L.QualitySpeed(), L.QualityBalanced(), L.QualityBest()).Changed() {
			v.setQuality(preset, fpsCap, full)
		}
		if ui.Select(c.Key("fps"), &fpsCap, fpsCaps).Width(100).Changed() {
			v.setQuality(preset, fpsCap, full)
		}
		if names, labels := codecChoices(offer); len(names) > 2 {
			picked := codecLabel(codec)
			if ui.Select(c.Key("codec"), &picked, labels).Width(150).Tooltip(L.ViewerCodecHint()).Changed() {
				if i := slices.Index(labels, picked); i >= 0 {
					v.setCodec(names[i])
				}
			}
		}
		labelled(c, func() {
			if ui.Switch(c.Key("full-colour"), &full).Label(L.QualityFullColour()).Changed() {
				v.setQuality(preset, fpsCap, full)
			}
		}, L.QualityFullColour())
		if start.Codec == "raw-rgba" {
			// The host sends its test pattern: it may not record its screen.
			ui.Text(c, L.ViewerPlaceholder()).FontSize(sizeSmall).TextColor(t.Warning)
		} else if start.Width > 0 {
			codec := codecLabel(start.Codec)
			if start.I444 {
				codec += " 4:4:4"
			}
			switch b.path.Path {
			case "direct":
				ui.Text(c, L.PathDirect()).FontSize(sizeSmall).TextColor(t.TextMuted)
			case "relay":
				ui.Text(c, L.PathRelay()).FontSize(sizeSmall).TextColor(t.Warning).Tooltip(L.ViewerRelayedHint())
			}
			ui.Text(c, fmt.Sprintf("%d×%d · %s · %.0f fps · %.0f kbit/s · %d ms", start.Width, start.Height, codec, fps, kbps, host.DelayMS)).
				FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted).
				Tooltip(L.ViewerEncoding(codec, implLabel(codecImpl{start.Encoder, start.Hardware})) + "\n" +
					L.ViewerDecoding(codec, implLabel(b.decoder)) + "\n" +
					L.ViewerTimes(host.EncodeMS, host.DelayMS, int(b.decodeMS+0.5), int(b.drawMS+0.5), int(fps+0.5), host.FPS))
		}
		if v.app != nil {
			labelled(c, func() {
				if ui.Switch(c.Key("pointer-marker"), &showPtr).Label(L.ViewerPointer()).Tooltip(L.ViewerPointerHint()).Changed() {
					v.app.setShowPointer(showPtr)
				}
			}, L.ViewerPointer())
		}
		v.mu.Lock()
		canAI, shown := v.ai.run, !v.ai.hidden
		v.mu.Unlock()
		if canAI {
			labelled(c, func() {
				if ui.Switch(c.Key("ai-panel"), &shown).Label(L.ViewerAIPanel()).Changed() {
					v.mu.Lock()
					v.ai.hidden = !shown
					v.mu.Unlock()
				}
			}, L.ViewerAIPanel())
		}
		if w := v.home.Load(); w != nil {
			full := w.fullScreen()
			label := L.ViewerFullScreen()
			if full {
				label = L.ViewerExitFullScreen()
			}
			if ui.Button(c.Key("full-screen"), label).Clicked() {
				w.win.SetFullScreen(!full)
			}
		}
	})
}
