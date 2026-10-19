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
	packetAudio      byte = 12
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
	// collapsed: the bottom bar is folded away; topCollapsed: the top bar
	// (the host's actions and the keyboard); guarded by mu.
	collapsed, topCollapsed bool
	// address is where the connection dials the device (an IP address
	// or domain name), empty over the network; guarded by mu.
	address string
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
	// mute: the host's sound is not played (and not asked for).
	mute bool
	// original shows the picture at one point per pixel, scrolled, instead
	// of fitted to the window.
	original bool
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
	// The AI panel (viewerai.go) and the clipboard's (viewerclip.go).
	ai   viewerAI
	clip viewerClip
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
// connects again. address, when set, is where to dial the device instead
// of the network (an IP address or domain name). want is what to ask for
// (nil: viewing, input, the clipboard and files).
// Notes: call on the main thread; one connection per device.
func (a *App) connectDevice(device, address string, about localapi.About, want []string) {
	if want == nil {
		want = connectWant(false)
	}
	if v := a.viewerFor(device); v != nil {
		if address != "" {
			v.mu.Lock()
			v.address = address
			v.mu.Unlock()
		}
		if w := v.home.Load(); w != nil {
			w.show(v)
		}
		if !v.live() {
			v.redial("", false)
		}
		return
	}
	v := a.newViewer(device, about, want)
	v.address = address
	a.main.add(v)
	v.redial("", false)
}

// newViewer makes a device's connection, not yet dialed, and registers it.
func (a *App) newViewer(device string, about localapi.About, want []string) *viewer {
	a.mu.Lock()
	about = a.prefs.named(device, about)
	a.mu.Unlock()
	v := &viewer{app: a, device: device, title: deviceTitle(about, device), want: want,
		counted: time.Now(), out: make(chan []byte, 256), moved: make(chan struct{}, 1), preset: 1, fpsCap: "30 fps", codec: "auto"}
	// The settings chosen for this device last time.
	if vp, ok := a.viewerPrefs(device); ok {
		v.preset, v.full, v.swap, v.mute, v.collapsed = min(max(vp.Preset, 0), 2), vp.Full, vp.Swap, vp.Mute, vp.Collapsed
		v.original, v.clip.off, v.topCollapsed = vp.Original, vp.NoClipboard, vp.TopCollapsed
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
	want, address := v.want, v.address
	v.mu.Unlock()
	go func() {
		if old != 0 {
			_, _ = v.app.requestWith(localapi.Request{Type: localapi.RequestClose, ID: old})
		}
		resp, err := v.app.requestWith(localapi.Request{Type: localapi.RequestConnect, Connect: &localapi.ConnectRequest{
			DeviceID: v.device, Password: password, Want: want, Remember: remember, Address: address,
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
		a.mu.Lock()
		peer := a.prefs.named(s.DeviceID, s.Peer)
		a.mu.Unlock()
		v.mu.Lock()
		v.phase = s
		if peer.Name != "" {
			v.title = deviceTitle(peer, s.DeviceID)
		}
		start := s.State == "connected" && !v.pumping
		if start {
			v.pumping = true
			v.control = slices.Contains(s.Granted, localapi.PermControlInput)
			v.ai.run = slices.Contains(s.Granted, localapi.PermRunAITask)
			v.ai.approve = slices.Contains(s.Granted, localapi.PermApproveAIAction)
			v.clip.may = slices.Contains(s.Granted, localapi.PermClipboard) || slices.Contains(s.Granted, localapi.PermFiles)
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

// Packets waiting for the video and the sound goroutines: a few frames,
// and about half a second of sound.
const (
	videoWaiting = 4
	soundWaiting = 50
)

// packetPool reuses packet buffers between the reader and the goroutines
// it hands packets to, so nothing allocates per frame.
type packetPool chan []byte

// get returns a spare buffer, or nil (ReadPacket makes one).
func (p packetPool) get() []byte {
	select {
	case b := <-p:
		return b
	default:
		return nil
	}
}

// put gives a buffer back; spares beyond the pool's room are left to the
// collector.
func (p packetPool) put(b []byte) {
	select {
	case p <- b:
	default:
	}
}

// pump reads the session's media until the stream or the window ends. As
// RustDesk's client does, reading never waits on decoding: frames go to the
// video goroutine and sound to the sound goroutine, each with its own
// queue, so a slow picture never holds up sound, input or the host's
// reports.
// Side effects: asks the host for a key frame first, so a (re)opened viewer
// starts with a complete picture, and again when the video queue was full
// and frames were left out.
func (v *viewer) pump() {
	defer func() {
		v.mu.Lock()
		v.pumping = false
		v.mu.Unlock()
	}()
	lib, err := coreLib()
	if err != nil {
		v.setState(L.ViewerNoDecoder())
		return
	}
	conn, err := localapi.NewClient(v.app.socket).Media(v.app.ctx, v.session())
	if err != nil {
		v.setState(L.ViewerEnded())
		return
	}
	v.mu.Lock()
	v.conn = conn
	v.mu.Unlock()
	defer conn.Close()
	keyRequest := append([]byte{packetControl}, `{"type":"key-frame"}`...)
	_ = localapi.WritePacket(conn, keyRequest)
	// The host follows what the bar shows, also for a new session.
	for _, p := range v.choicePackets() {
		_ = localapi.WritePacket(conn, p)
	}
	// The writer stops with the pump; v.out stays open, so a late input
	// event never sends on a closed channel.
	done := make(chan struct{})
	defer close(done)
	go v.writer(conn, done)
	pool := make(packetPool, videoWaiting+soundWaiting+4)
	video := make(chan []byte, videoWaiting)
	sound := make(chan []byte, soundWaiting)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); v.decodeVideo(lib, video, pool) }()
	go func() { defer wg.Done(); v.playSound(lib, sound, pool) }()
	defer wg.Wait()
	defer close(sound)
	defer close(video)
	var gate videoGate
	for {
		p, err := localapi.ReadPacket(conn, pool.get())
		if err != nil {
			v.setState(L.ViewerEnded())
			return
		}
		if len(p) == 0 {
			pool.put(p)
			continue
		}
		switch p[0] {
		case packetVideoStart:
			// A new stream always reaches the decoder.
			gate.lost = false
			video <- p
			continue
		case packetFrame:
			sent, ask := gate.pass(p, video)
			if !sent {
				pool.put(p)
			}
			if ask {
				select {
				case v.out <- keyRequest:
				default:
				}
			}
			continue
		case packetAudio:
			select {
			case sound <- p:
			default:
				pool.put(p) // the sound goroutine is behind: left out
			}
			continue
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
		case packetClipboard:
			v.clipPacketIn(p[1:])
		case packetStats:
			var s hostStats
			if json.Unmarshal(p[1:], &s) == nil {
				v.mu.Lock()
				v.host = s
				v.mu.Unlock()
			}
		}
		pool.put(p)
	}
}

// videoGate passes frames to the video goroutine's queue. Once the queue
// was full and a frame was left out, the frames after it refer to a
// picture the decoder never had: they are left out too, until a key frame.
type videoGate struct {
	lost bool
}

// pass queues frame p unless it must be left out.
// Returns: whether it was queued (the caller keeps it otherwise), and
// whether to ask the host for a key frame (once per loss).
func (g *videoGate) pass(p []byte, video chan<- []byte) (sent, ask bool) {
	if g.lost && (len(p) < 2 || p[1]&keyFrame == 0) {
		return false, false
	}
	select {
	case video <- p:
		g.lost = false
		return true, false
	default:
		ask = !g.lost
		g.lost = true
		return false, ask
	}
}

// decodeVideo opens a decoder for each stream the host describes and shows
// its frames, until video closes.
// Notes: runs on one locked OS thread, which the decoder is tied to.
func (v *viewer) decodeVideo(lib *core.Lib, video <-chan []byte, pool packetPool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var dec *core.Decoder
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()
	// fresh: the decoder has not had a key frame yet.
	fresh := false
	for p := range video {
		switch p[0] {
		case packetVideoStart:
			var s videoStart
			if json.Unmarshal(p[1:], &s) != nil || s.Width <= 0 || s.Height <= 0 || s.Width > 16384 || s.Height > 16384 {
				break
			}
			// The same stream described again (a key frame was asked
			// for): a new group of pictures, which the decoder takes as
			// it is, without a blank picture between.
			v.mu.Lock()
			same := dec != nil && v.start == s
			v.mu.Unlock()
			if same {
				break
			}
			if dec != nil {
				dec.Close()
				dec = nil
			}
			d, err := lib.OpenDecoder(p[1:])
			if err != nil {
				v.setState(L.ViewerUnsupported(codecLabel(s.Codec)))
				break
			}
			dec, fresh = d, true
			v.begin(s)
		case packetFrame:
			// A new decoder starts at a key frame: the frames encoded before
			// the host's (they may still come after its stream description)
			// refer to pictures it never had.
			if len(p) < 10 || dec == nil || fresh && p[1]&keyFrame == 0 {
				break
			}
			first := fresh
			fresh = false
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
		pool.put(p)
	}
}

// playSound plays the host's sound packets while the viewer has sound on,
// until sound closes.
func (v *viewer) playSound(lib *core.Lib, sound <-chan []byte, pool packetPool) {
	var snd *viewerSound
	defer func() {
		if snd != nil {
			snd.close()
		}
	}()
	for p := range sound {
		v.mu.Lock()
		mute := v.mute
		v.mu.Unlock()
		switch {
		case mute && snd != nil:
			snd.close()
			snd = nil
		case !mute && snd == nil:
			snd, _ = openSound(lib)
		}
		if snd != nil && len(p) > 1 {
			snd.packet(p[1:])
		}
		pool.put(p)
	}
}

// begin prepares the pictures for a new stream, at once on the video
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

// view is the viewer window: the picture (fitted, or at its original size),
// the AI panel when the session may run AI tasks, and a status line.
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
	bm, state, start, original, control := v.bm, v.state, v.start, v.original, v.control
	withAI, withClip, ptr, actions := v.ai.aiShown(), v.clip.may && v.clip.shown, v.pointer, v.actions
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
		// Every host that takes this session's input gets the top bar (its
		// keyboard choices); only a Windows host offers actions in it.
		if control {
			v.actionBar(c, actions)
		}
		ui.Row(c).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
			if original && state == "" && bm != nil {
				v.originalPicture(c, bm, start, ptr)
			} else {
				v.picture(c, bm, state, start, ptr)
			}
			if withClip {
				v.clipPanel(c)
			}
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
			if ox, oy, w, h := pictureBox(box, start.Width, start.Height); ptr.Visible && w > 0 {
				pointerRing(c, box.X-at.X+ox+ptr.X*w, box.Y-at.Y+oy+ptr.Y*h)
			}
		}
	})
}

// originalPicture is the host's screen at one point per pixel: scrolled in
// both directions when it is larger than the area (the scroll bars' thumbs
// move it; the wheel goes to the host while this session controls it),
// centred on black when it is smaller. The pointer ring scrolls with it.
func (v *viewer) originalPicture(c *ui.Context, bm *ui.Bitmap, start videoStart, ptr hostPointer) {
	focused := false
	defer func() { v.syncGrab(focused) }()
	w, h := float32(start.Width), float32(start.Height)
	scroll := ui.ScrollBoth(c.Key("picture-scroll")).Grow(1).MinWidth(0).FillHeight()
	area := scroll.LayoutBounds()
	scroll.Children(func() {
		ui.Column(c).MinWidth(area.W).MinHeight(area.H).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Column(c.Key("picture-box")).Width(w).Height(h).Shrink(0).Children(func() {
				// It takes text and keys as the fitted picture does.
				img := ui.Image(c.Key("picture"), bm).Width(w).Height(h).Focusable().
					TextCaret(ui.Rect{W: 1, H: 16}).HandleInput(v.sendInput)
				focused = img.Focused()
				box := img.LayoutBounds()
				v.mu.Lock()
				v.box = box
				v.mu.Unlock()
				if ptr.Visible {
					pointerRing(c, ptr.X*w, ptr.Y*h)
				}
			})
		})
	})
}

// pointerRing marks the host's pointer at x, y in its parent. It lets the
// pointer through, so it never takes a click.
func pointerRing(c *ui.Context, x, y float32) {
	ui.Column(c.Key("pointer")).Absolute().Left(x-pointerR).Top(y-pointerR).
		Width(2*pointerR).Height(2*pointerR).Radius(pointerR).
		Background(ui.Hex("#ffffff").Alpha(0.85)).Border(2, ui.Hex("#000000")).PassThrough()
}

// actionBar is the top bar of a session that controls the host: the host's
// session actions when it offers them (a is nil otherwise) — lock its
// screen, send Ctrl+Alt+Del (helper modes only), block its keyboard and
// mouse, and privacy mode (which blocks them too) — then the keyboard mode
// and Swap Ctrl and Cmd. The switches show what the host reports, not what
// was asked. It folds away to a small tab over the picture's top edge,
// kept for the device.
func (v *viewer) actionBar(c *ui.Context, a *hostActions) {
	t := c.Theme()
	v.mu.Lock()
	folded := v.topCollapsed
	v.mu.Unlock()
	fold := func(on bool) {
		v.mu.Lock()
		v.topCollapsed = on
		v.mu.Unlock()
		v.keepSettings()
	}
	if folded {
		tab := ui.ButtonBase(c.Key("top-fold")).Absolute().Top(0).Right(barTabRight).Padding(2, 14).
			Radius(6).Background(t.Surface.Alpha(0.85)).Label(L.ViewerShowActions()).Tooltip(L.ViewerShowActions())
		tab.Children(func() { ui.Text(c, "⌄").TextColor(t.TextMuted) })
		if tab.Clicked() {
			fold(false)
		}
		return
	}
	action := func(name string) {
		v.sendControl(map[string]any{"type": "action", "action": name})
	}
	ui.Row(c).FillWidth().Wrap().Padding(6, 12).Gap(12).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		if ui.Button(c.Key("top-fold"), "⌃").Label(L.ViewerHideActions()).Tooltip(L.ViewerHideActions()).Clicked() {
			fold(true)
		}
		if a != nil {
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
		}
		if grabsKeyboard {
			ui.Row(c).Grow(1)
			// The label wraps with its choice, never apart from it.
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, L.ViewerKeyboard()).FontSize(sizeSmall).TextColor(t.TextMuted)
				mode := slices.Index(keyboardModes, v.app.keyboardMode())
				if ui.Segmented(c.Key("keyboard"), &mode, L.ViewerKeyboardMap(), L.ViewerKeyboardTranslate(), L.ViewerKeyboardLegacy()).
					Tooltip(L.ViewerKeyboardHint()).Changed() {
					v.app.setKeyboardMode(keyboardModes[mode])
				}
			})
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
// It wraps onto more lines in a narrow window, and folds down to the
// Device ID and Show controls.
func (v *viewer) statusBar(c *ui.Context, b bar) {
	t := c.Theme()
	start, fps, kbps, host, preset, fpsCap, full, displays, showPtr, offer, codec := b.start, b.fps, b.kbps, b.host, b.preset, b.fpsCap, b.full, b.displays, b.showPtr, b.offer, b.codec
	v.mu.Lock()
	collapsed := v.collapsed
	v.mu.Unlock()
	if collapsed {
		// Folded away entirely: a small tab over the picture's bottom edge
		// pulls the bar out again.
		tab := ui.ButtonBase(c.Key("bar-fold")).Absolute().Bottom(0).Right(barTabRight).Padding(2, 14).
			Radius(6).Background(t.Surface.Alpha(0.85)).Label(L.ViewerShowBar()).Tooltip(L.ViewerShowBar())
		tab.Children(func() { ui.Text(c, "⌃").TextColor(t.TextMuted) })
		if tab.Clicked() {
			v.mu.Lock()
			v.collapsed = false
			v.mu.Unlock()
			v.keepSettings()
		}
		return
	}
	ui.Row(c).FillWidth().Wrap().Padding(6, 12).Gap(16).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		if ui.Button(c.Key("bar-fold"), "⌄").Label(L.ViewerHideBar()).Tooltip(L.ViewerHideBar()).Clicked() {
			v.mu.Lock()
			v.collapsed = true
			v.mu.Unlock()
			v.keepSettings()
		}
		ui.Text(c, groupDigits(v.device)).FontSize(sizeSmall).SingleLine()
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
		v.mu.Lock()
		scale := 0
		if v.original {
			scale = 1
		}
		v.mu.Unlock()
		if ui.Segmented(c.Key("scale"), &scale, L.ViewerFit(), L.ViewerOriginal()).Tooltip(L.ViewerScaleHint()).Changed() {
			v.mu.Lock()
			v.original = scale == 1
			v.mu.Unlock()
			v.keepSettings()
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
		v.mu.Lock()
		soundOn := !v.mute
		v.mu.Unlock()
		labelled(c, func() {
			if ui.Switch(c.Key("sound"), &soundOn).Label(L.ViewerSound()).Tooltip(L.ViewerSoundHint()).Changed() {
				v.setSound(soundOn)
			}
		}, L.ViewerSound())
		if v.app != nil {
			labelled(c, func() {
				if ui.Switch(c.Key("pointer-marker"), &showPtr).Label(L.ViewerPointer()).Tooltip(L.ViewerPointerHint()).Changed() {
					v.app.setShowPointer(showPtr)
				}
			}, L.ViewerPointer())
		}
		v.mu.Lock()
		canAI, shown := v.ai.run, !v.ai.hidden
		canClip, clipShown := v.clip.may, v.clip.shown
		v.mu.Unlock()
		if canClip {
			labelled(c, func() {
				if ui.Switch(c.Key("clip-panel"), &clipShown).Label(L.ViewerClipboard()).Changed() {
					v.mu.Lock()
					v.clip.shown = clipShown
					v.mu.Unlock()
				}
			}, L.ViewerClipboard())
		}
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
