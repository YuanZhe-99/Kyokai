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
	// Frames is how many frames the host sent the last second (0 from a
	// 0.4.0 host, which does not say).
	Frames int `json:"frames"`
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
	// folded: the toolbar is folded to its handle; guarded by mu.
	folded bool
	// The toolbar on the main thread: its offset from the middle of the
	// picture's top edge, its width in the last frame, and whether the
	// pointer was dragging it then. pic is the picture, which takes the
	// keyboard again after a choice in the toolbar.
	barOff      ui.Point
	barW        float32
	barDragging bool
	pic         ui.Handle
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
	// The stream's health (watchdog): when the last picture was decoded
	// (the stream's start until the first), when a frame or the host's
	// stats last came, whether the picture is being recovered (none for a
	// while although the host sends frames), and whether to offer waking
	// the host's screen (nothing new for wakeAfter).
	pictureAt  time.Time
	heardAt    time.Time
	recovering bool
	asleep     bool
	// gate leaves out the frames the decoder cannot use (its own lock).
	gate videoGate
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
		v.preset, v.full, v.swap, v.mute, v.folded = min(max(vp.Preset, 0), 2), vp.Full, vp.Swap, vp.Mute, vp.Folded
		v.original, v.clip.off = vp.Original, vp.NoClipboard
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
	v.pictureAt, v.heardAt = time.Now(), time.Now()
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
	go v.watchdog(done, keyRequest)
	pool := make(packetPool, videoWaiting+soundWaiting+4)
	video := make(chan []byte, videoWaiting)
	sound := make(chan []byte, soundWaiting)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); v.decodeVideo(lib, video, pool, keyRequest) }()
	go func() { defer wg.Done(); v.playSound(lib, sound, pool) }()
	defer wg.Wait()
	defer close(sound)
	defer close(video)
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
		if p[0] == packetFrame || p[0] == packetStats {
			v.mu.Lock()
			v.heardAt = time.Now()
			v.mu.Unlock()
		}
		switch p[0] {
		case packetVideoStart:
			// A new stream always reaches the decoder (which starts it at
			// its key frame; only a key frame clears the gate).
			video <- p
			continue
		case packetFrame:
			sent, ask := v.gate.pass(p, video, time.Now())
			if !sent {
				pool.put(p)
			}
			if ask {
				v.askKey(keyRequest)
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

// videoGate passes frames to the video goroutine's queue. Once a frame was
// left out (the queue was full) or the decoder failed on one, the frames
// after it refer to a picture the decoder does not have: they are left out
// too, until a key frame, which alone opens the gate again; the host is
// asked for one at once and again every keyAgain until it comes
// (regression: it was asked once, and a key frame lost too stopped the
// picture for good).
// Notes: the pump, the decoder and the watchdog share it, under mu.
type videoGate struct {
	mu    sync.Mutex
	lost  bool
	asked time.Time
}

// Picture health (viewer.watchdog): how often the viewer asks the host for
// a key frame at most, how long without a picture while the host sends
// frames counts as a lost picture, and after how long without a picture
// the viewer offers to wake the host's screen.
const (
	keyAgain  = time.Second
	noPicture = 2 * time.Second
	wakeAfter = 3 * time.Second
)

// pass queues frame p unless it must be left out.
// Returns: whether it was queued (the caller keeps it otherwise), and
// whether to ask the host for a key frame now.
func (g *videoGate) pass(p []byte, video chan<- []byte, now time.Time) (sent, ask bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := len(p) > 1 && p[1]&keyFrame != 0
	if g.lost && !key {
		return false, false
	}
	select {
	case video <- p:
		g.lost = false
		return true, false
	default:
		g.lost = true
		return false, g.ask(now)
	}
}

// broke closes the gate after the decoder failed on a frame.
// Returns: whether to ask the host for a key frame now.
func (g *videoGate) broke(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lost = true
	return g.ask(now)
}

// again is the watchdog's check: whether to ask for a key frame now, while
// the gate waits for one or the picture needs one (viewerNeedsKey).
func (g *videoGate) again(need bool, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return (g.lost || need) && g.ask(now)
}

// ask reports whether the host may be asked again now, and if so counts
// the request (the caller holds g.mu).
func (g *videoGate) ask(now time.Time) bool {
	if now.Sub(g.asked) < keyAgain {
		return false
	}
	g.asked = now
	return true
}

// viewerNeedsKey reports whether the picture needs a key frame although
// the gate is open: no picture came for noPicture while the host says it
// sent frames (a key frame lost on the way, a decoder that gave up). A
// still screen sends none, and a 0.4.0 host does not say.
func viewerNeedsKey(sincePicture time.Duration, hostFrames int) bool {
	return sincePicture >= noPicture && hostFrames > 0
}

// askKey asks the host for a key frame; a full queue leaves the request
// to the watchdog's next look.
func (v *viewer) askKey(keyRequest []byte) {
	select {
	case v.out <- keyRequest:
	default:
	}
}

// watchdog looks at the stream each second until done closes: it asks for
// a key frame again while the gate waits for one or the picture needs one,
// and has the window show "Recovering the picture…" and offer to wake the
// host's screen when that changes.
func (v *viewer) watchdog(done <-chan struct{}, keyRequest []byte) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			v.mu.Lock()
			since := now.Sub(v.pictureAt)
			need := viewerNeedsKey(since, v.host.Frames)
			recovering, asleep := need && v.bm != nil, since >= wakeAfter && v.control
			changed := recovering != v.recovering || asleep != v.asleep
			v.recovering, v.asleep = recovering, asleep
			v.mu.Unlock()
			if v.gate.again(need, now) {
				v.askKey(keyRequest)
			}
			if changed {
				v.update(func() {})
			}
		}
	}
}

// decodeVideo opens a decoder for each stream the host describes and shows
// its frames, until video closes. A frame the decoder fails on closes the
// gate and asks for a key frame (at most each keyAgain); the decoder takes
// nothing but a key frame next.
// Notes: runs on one locked OS thread, which the decoder is tied to.
func (v *viewer) decodeVideo(lib *core.Lib, video <-chan []byte, pool packetPool, keyRequest []byte) {
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
			if v.show(dec, p[10:]) != nil {
				fresh = true
				if v.gate.broke(time.Now()) {
					v.askKey(keyRequest)
				}
			}
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
// gives way to a newer one. Frames the decoder holds show nothing.
// Returns: the decoder's error (a picture of another size is one too).
// Notes: the bitmap takes a picture without a copy, so the one shown is
// never decoded into.
func (v *viewer) show(dec *core.Decoder, data []byte) error {
	v.mu.Lock()
	free := 0
	for free == v.shown || free == v.ready {
		free++
	}
	pic := v.imgs[free]
	v.bytes += len(data)
	v.mu.Unlock()
	if pic == nil {
		return nil
	}
	began := time.Now()
	w, h, ok, err := dec.Decode(data, pic.Pix)
	switch {
	case err != nil:
		return err
	case !ok:
		return nil
	case w != pic.Rect.Dx() || h != pic.Rect.Dy():
		return fmt.Errorf("a %d×%d picture in a %d×%d stream", w, h, pic.Rect.Dx(), pic.Rect.Dy())
	}
	decoded := time.Now()
	v.mu.Lock()
	v.frames++
	v.pictureAt = decoded
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
	return nil
}

func (v *viewer) setState(s string) {
	v.update(func() {
		v.mu.Lock()
		v.state = s
		v.mu.Unlock()
	})
}

// view is the viewer window: the picture (fitted, or at its original size)
// with the toolbar over its top edge, and beside it the clipboard panel and
// the AI panel when the session may use them.
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
	ui.Row(c).Fill().Background(ui.Hex("#000000")).AlignItems(ui.Stretch).Children(func() {
		// The toolbar floats over the picture, the panels beside it.
		stage := ui.Row(c.Key("stage")).Grow(1).MinWidth(0).AlignItems(ui.Stretch)
		stageW := stage.LayoutBounds().W
		stage.Children(func() {
			if original && state == "" && bm != nil {
				v.originalPicture(c, bm, start, ptr)
			} else {
				v.picture(c, bm, state, start, ptr)
			}
			v.toolbar(c, b, actions, control, stageW)
		})
		if withClip {
			v.clipPanel(c)
		}
		if withAI {
			v.aiPanel(c)
		}
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
					busyMark(c, sessionState(phase, behind), busySize)
					ui.Text(c, sessionState(phase, behind)).TextColor(t.TextMuted)
				})
			case "confirming":
				waiting = true
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					busyMark(c, L.SessionConfirmingThere(), busySize)
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
	v.mu.Lock()
	recovering, asleep := v.recovering, v.asleep
	v.mu.Unlock()
	area.Children(func() {
		switch {
		case state != "":
			ui.Text(c, state).TextColor(ui.Hex("#ffffff"))
		case bm == nil:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				busyMarkIn(c, L.ViewerWaiting(), busySize, ui.Hex("#ffffff"))
				ui.Text(c, L.ViewerWaiting()).TextColor(ui.Hex("#ffffff"))
			})
			if asleep {
				v.wakeOffer(c)
			}
		default:
			// The picture takes text: macOS turns keys into text (and input
			// methods compose) only for an element that does, and Legacy
			// mode sends letters as that text (regression: no key reached a
			// Mac's host from a Mac). Candidate windows open at its corner.
			img := ui.Image(c.Key("picture"), bm).Fit(ui.Contain).Grow(1).FillWidth().Focusable().
				TextCaret(ui.Rect{W: 1, H: 16}).HandleInput(v.sendInput).Bind(&v.pic)
			focused = img.Focused()
			box := img.LayoutBounds()
			v.mu.Lock()
			v.box = box
			v.mu.Unlock()
			if ox, oy, w, h := pictureBox(box, start.Width, start.Height); ptr.Visible && w > 0 {
				pointerRing(c, box.X-at.X+ox+ptr.X*w, box.Y-at.Y+oy+ptr.Y*h)
			}
			// The picture shown is old while the host sends frames that
			// do not reach it: said over it until a new one comes.
			if recovering {
				ui.Column(c.Key("recovering")).Absolute().Bottom(noticeBottom).Left(0).Right(0).AlignItems(ui.Center).PassThrough().Children(func() {
					notice(c, func() {
						busyMarkIn(c, L.ViewerRecovering(), busySize, ui.Hex("#ffffff"))
						ui.Text(c, L.ViewerRecovering()).TextColor(ui.Hex("#ffffff"))
						if asleep {
							v.wakeOffer(c)
						}
					})
				})
			}
		}
	})
}

// notice is a dark rounded strip of text over the viewer's picture.
func notice(c *ui.Context, children func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(6, 12).Radius(barRadius).Background(ui.RGBA(0, 0, 0, 0.7)).Children(children)
}

// wakeOffer says the host's screen may be asleep and offers to wake it,
// for a session that may control input: a Shift press and release, which
// wakes a display as a key does (a 0.4.0 host takes it too).
func (v *viewer) wakeOffer(c *ui.Context) {
	ui.Row(c.Key("wake")).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, L.ViewerAsleep()).TextColor(ui.Hex("#ffffff"))
		if ui.Button(c.Key("wake-it"), L.ViewerWake()).Clicked() {
			for _, down := range []bool{true, false} {
				v.queue(inputEvent{Kind: "scan", Code: 0x2A, Down: down})
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
					TextCaret(ui.Rect{W: 1, H: 16}).HandleInput(v.sendInput).Bind(&v.pic)
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

// bar is what the toolbar shows: the stream and its numbers, the
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

// toolbar is the viewer's controls, floating over the picture's top edge
// as RustDesk's: menus for the display, the quality, the keyboard and the
// host's session actions, toggles for the sound, the pointer ring, the
// clipboard and AI panels and full screen, and the path to the device. Its
// grip, or any gap between its controls, drags it along the top edge, to a
// place kept for every viewer (Prefs.ToolbarAt); ⌃ folds it to a small
// handle at that place, kept for the device. It never covers more than the
// picture's width less barMargin a side: what does not fit goes into the
// toolbar's overflow menu.
// Notes: call on the main thread (barOff, barW and barDragging are its own).
func (v *viewer) toolbar(c *ui.Context, b bar, a *hostActions, control bool, stageW float32) {
	t := c.Theme()
	v.mu.Lock()
	folded := v.folded
	v.mu.Unlock()
	at := float32(0)
	if v.app != nil {
		at = v.app.toolbarAt()
	}
	free := max(stageW-v.barW-2*barMargin, 0)
	if !v.barDragging {
		v.barOff = ui.Point{X: at * free}
	}
	ui.Row(c.Key("bar-lane")).Absolute().Top(barTop).Left(0).Right(0).Justify(ui.Center).PassThrough().Children(func() {
		bar := ui.Row(c.Key("bar")).AlignItems(ui.Center).Gap(2).Padding(2, 6).Radius(barRadius).
			Background(t.Surface).Border(1, t.Border).Shadow(0, 2, 10, 0, ui.RGBA(0, 0, 0, 0.3)).
			MaxWidth(max(stageW-2*barMargin, 0)).
			Draggable(&v.barOff, ui.DraggableOptions{Axis: ui.Horizontal, Bounds: &ui.Rect{X: -free / 2, W: free}, Rigid: true})
		bar.Children(func() {
			ui.Icon(c, barGrip).Size(barIconSize, barIconSize).TextColor(t.TextMuted).Cursor(ui.CursorGrab).Tooltip(L.ViewerMoveBar())
			if !folded {
				ui.Toolbar(c, func() { v.barControls(c, b, a, control) }).Label(L.ViewerToolbar()).Shrink(1).MinWidth(0).Padding(0)
			}
			// The path and the fold button never go into the overflow menu.
			if folded {
				if flatButton(c, "bar-fold", barUnfold, L.ViewerShowBar()).Clicked() {
					v.fold(false)
				}
				return
			}
			pathChip(c, b)
			if flatButton(c, "bar-fold", barFold, L.ViewerHideBar()).Clicked() {
				v.fold(true)
			}
		})
		if w := bar.LayoutBounds().W; w != v.barW {
			// Its place depends on its width: once more with the new one.
			v.barW = w
			c.Invalidate()
		}
		dragging := bar.Dragging()
		if v.barDragging && !dragging && free > 0 && v.app != nil {
			v.app.setToolbarAt(min(max(v.barOff.X/free, -0.5), 0.5))
		}
		v.barDragging = dragging
	})
}

// barButton is an icon button of the toolbar, named by label.
func barButton(c *ui.Context, key string, icon *ui.SVG, label string) ui.Element {
	return ui.Button(c.Key(key), "").Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icon).Size(barIconSize, barIconSize)
	})
}

// flatButton is an icon button with the toolbar's look outside a toolbar
// (which keeps room for its overflow button).
func flatButton(c *ui.Context, key string, icon *ui.SVG, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Padding(6, 10).Radius(t.Radius).AlignItems(ui.Center).Label(label).Tooltip(label)
	switch {
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	return b.Children(func() { ui.Icon(c, icon).Size(barIconSize, barIconSize) })
}

// barMenu is a menu button of the toolbar: its icon, named by label, and
// the menu that build fills.
func barMenu(c *ui.Context, key string, icon *ui.SVG, label string, build func(m *ui.Menu)) ui.Element {
	return barButton(c, key, icon, label).Menu(build)
}

// barToggle is an icon toggle of the toolbar, pressed while *on.
func barToggle(c *ui.Context, key string, on *bool, icon *ui.SVG, label, hint string) ui.Element {
	if hint == "" {
		hint = label
	}
	return ui.Toggle(c.Key(key), on, "").Label(label).Tooltip(hint).Children(func() {
		ui.Icon(c, icon).Size(barIconSize, barIconSize)
	})
}

// fold folds the toolbar to its handle, or opens it, and keeps that for
// the device.
func (v *viewer) fold(on bool) {
	v.mu.Lock()
	v.folded = on
	v.mu.Unlock()
	v.keepSettings()
}

// barControls are the toolbar's menus and toggles. After a choice the
// picture takes the keyboard again.
func (v *viewer) barControls(c *ui.Context, b bar, a *hostActions, control bool) {
	t := c.Theme()
	back := func() { v.pic.Focus(c) }
	preset, fpsCap, full := b.preset, b.fpsCap, b.full
	v.mu.Lock()
	original, soundOn, swap, peerOS, refused := v.original, !v.mute, v.swapping(), v.phase.Peer.OS, v.noGrab
	canAI, aiShown, canClip, clipShown := v.ai.run, !v.ai.hidden, v.clip.may, v.clip.shown
	v.mu.Unlock()

	barMenu(c, "bar-display", barDisplay, L.ViewerDisplay(), func(m *ui.Menu) {
		if n := len(b.displays.List); n > 1 {
			for i := range n {
				if m.Item(L.ViewerDisplayN(strconv.Itoa(i + 1))).Checked(i == b.displays.Current).Chosen() {
					v.sendControl(map[string]any{"type": "display", "index": i})
					back()
				}
			}
			m.Separator()
		}
		for i, label := range []string{L.ViewerFit(), L.ViewerOriginal()} {
			if m.Item(label).Checked(original == (i == 1)).Chosen() {
				v.mu.Lock()
				v.original = i == 1
				v.mu.Unlock()
				v.keepSettings()
				back()
			}
		}
	})

	barMenu(c, "bar-quality", barQuality, L.ViewerQuality(), func(m *ui.Menu) {
		for i, label := range []string{L.QualitySpeed(), L.QualityBalanced(), L.QualityBest()} {
			if m.Item(label).Checked(i == preset).Chosen() {
				v.setQuality(i, fpsCap, full)
				back()
			}
		}
		m.Separator()
		m.Submenu(L.ViewerFrameRate(), func(m *ui.Menu) {
			for _, rate := range fpsCaps {
				if m.Item(rate).Checked(rate == fpsCap).Chosen() {
					v.setQuality(preset, rate, full)
					back()
				}
			}
		})
		if names, labels := codecChoices(b.offer); len(names) > 2 {
			m.Submenu(L.ViewerCodec(), func(m *ui.Menu) {
				for i, label := range labels {
					if m.Item(label).Checked(names[i] == b.codec).Chosen() {
						v.setCodec(names[i])
						back()
					}
				}
			})
		}
		if m.Item(L.QualityFullColour()).Checked(full).Chosen() {
			v.setQuality(preset, fpsCap, !full)
			back()
		}
	})

	// Ctrl and Cmd matter where a Mac is on either side.
	swappable := runtime.GOOS == "darwin" || peerOS == "macos"
	if control && (grabsKeyboard || swappable) {
		// macOS refuses the keyboard grab without Kyokai's Accessibility
		// permission: the viewer works in Legacy mode meanwhile, and the
		// button says why.
		noGrab := refused && runtime.GOOS == "darwin"
		kb := barMenu(c, "bar-keyboard", barKeyboard, L.ViewerKeyboard(), func(m *ui.Menu) {
			if grabsKeyboard {
				mode := v.app.keyboardMode()
				for i, label := range []string{L.ViewerKeyboardMap(), L.ViewerKeyboardTranslate(), L.ViewerKeyboardLegacy()} {
					if m.Item(label).Checked(keyboardModes[i] == mode).Chosen() {
						v.app.setKeyboardMode(keyboardModes[i])
						back()
					}
				}
			}
			if swappable {
				if grabsKeyboard {
					m.Separator()
				}
				if m.Item(L.ViewerSwapKeys()).Checked(swap).Chosen() {
					v.setSwap(!swap)
					back()
				}
			}
		})
		if noGrab {
			kb.TextColor(t.Warning).Tooltip(L.ViewerKeyboardNoGrab())
		}
	}

	// The checks show what the host reports, not what was asked.
	if control && a != nil {
		action := func(name string) {
			v.sendControl(map[string]any{"type": "action", "action": name})
			back()
		}
		barMenu(c, "bar-actions", barActions, L.ViewerActions(), func(m *ui.Menu) {
			if m.Item(L.ViewerLock()).Chosen() {
				action("lock")
			}
			if a.CtrlAltDel && m.Item(L.ViewerCtrlAltDel()).Chosen() {
				action("ctrl-alt-del")
			}
			m.Separator()
			if m.Item(L.ViewerBlockInput()).Checked(a.Blocked).Disabled(a.Privacy).Chosen() {
				if a.Blocked {
					action("unblock")
				} else {
					action("block")
				}
			}
			if m.Item(L.ViewerPrivacy()).Checked(a.Privacy).Chosen() {
				if a.Privacy {
					action("privacy-off")
				} else {
					action("privacy-on")
				}
			}
		})
	}

	icon := barSound
	if !soundOn {
		icon = barSoundOff
	}
	if barToggle(c, "bar-sound", &soundOn, icon, L.ViewerSound(), L.ViewerSoundHint()).Changed() {
		v.setSound(soundOn)
		back()
	}
	if v.app != nil {
		showPtr := b.showPtr
		if barToggle(c, "bar-pointer", &showPtr, barPointer, L.ViewerPointer(), L.ViewerPointerHint()).Changed() {
			v.app.setShowPointer(showPtr)
			back()
		}
	}
	if canClip && barToggle(c, "bar-clipboard", &clipShown, barClipboard, L.ViewerClipboard(), "").Changed() {
		v.mu.Lock()
		v.clip.shown = clipShown
		v.mu.Unlock()
	}
	if canAI && barToggle(c, "bar-ai", &aiShown, navAI, L.ViewerAIPanel(), "").Changed() {
		v.mu.Lock()
		v.ai.hidden = !aiShown
		v.mu.Unlock()
	}
	if w := v.home.Load(); w != nil {
		full, icon, label := w.fullScreen(), barFullScreen, L.ViewerFullScreen()
		if full {
			icon, label = barExitFullScreen, L.ViewerExitFullScreen()
		}
		if barButton(c, "bar-full-screen", icon, label).Clicked() {
			w.win.SetFullScreen(!full)
			back()
		}
	}
}

// pathChip is how the stream reaches this computer: a dot and Direct or
// Relayed (in the warning colour, with what to do about it) with the
// latency, or a warning that the host sends its test pattern. Its tooltip
// has the stream's numbers and where the time goes.
func pathChip(c *ui.Context, b bar) {
	t := c.Theme()
	start := b.start
	if start.Codec == "raw-rgba" {
		// The host sends its test pattern: it may not record its screen.
		ui.Text(c.Key("bar-path"), "⚠").TextColor(t.Warning).Label(L.ViewerPlaceholder()).Tooltip(L.ViewerPlaceholder())
		return
	}
	if start.Width == 0 {
		return
	}
	codec := codecLabel(start.Codec)
	if start.I444 {
		codec += " 4:4:4"
	}
	tip := fmt.Sprintf("%d×%d · %s · %.0f fps · %.0f kbit/s", start.Width, start.Height, codec, b.fps, b.kbps) + "\n" +
		L.ViewerEncoding(codec, implLabel(codecImpl{start.Encoder, start.Hardware})) + "\n" +
		L.ViewerDecoding(codec, implLabel(b.decoder)) + "\n" +
		L.ViewerTimes(b.host.EncodeMS, b.host.DelayMS, int(b.decodeMS+0.5), int(b.drawMS+0.5), int(b.fps+0.5), b.host.FPS)
	dot, text := t.Success, fmt.Sprintf("%d ms", b.host.DelayMS)
	switch b.path.Path {
	case "direct":
		text = L.PathDirect() + " · " + text
	case "relay":
		dot, text = t.Warning, L.PathRelay()+" · "+text
		tip = L.ViewerRelayedHint() + "\n\n" + tip
	}
	ui.Row(c.Key("bar-path")).Gap(6).Padding(0, 6).AlignItems(ui.Center).Shrink(0).Tooltip(tip).Children(func() {
		ui.Column(c).Size(onlineDot, onlineDot).Radius(onlineDot / 2).Background(dot)
		ui.Text(c, text).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
	})
}
