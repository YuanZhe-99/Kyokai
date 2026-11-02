package gui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// paneSource is what a pane offers this computer's AI when the host user
// lets it work there (doc/en-us/computer-use.md, "AI on a pane"): its
// picture and its input.
type paneSource interface {
	// paneSize is the picture's size, zero while there is none.
	paneSize() (int, int)
	// panePicture is a copy of the picture.
	panePicture() (*image.RGBA, error)
	// paneInput sends one of the AI's input events (the core's
	// input::Event, as the viewer sends a person's) to the pane's computer.
	paneInput(e inputEvent) error
	// paneRelease releases every key and button the AI holds there.
	paneRelease()
}

// paneServe is a pane served to this computer's AI: the engine's requests
// answered from its source until stopped.
type paneServe struct {
	cancel context.CancelFunc
	mu     sync.Mutex // writes to conn
	conn   net.Conn
	moved  time.Time
}

// paneMoveEvery is how often the host user's pointer moves on a served pane
// are told (keys, buttons and the wheel always are).
const paneMoveEvery = 200 * time.Millisecond

// servePane serves src to this computer's AI as p until stop is called or
// the engine ends it.
// Returns: the serving, whose stop ends it.
// Side effects: a local API connection of its own, on a goroutine.
func (a *App) servePane(p localapi.AIPane, src paneSource) *paneServe {
	ctx, cancel := context.WithCancel(a.ctx)
	s := &paneServe{cancel: cancel}
	go func() {
		defer cancel()
		conn, err := localapi.NewClient(a.sock()).ServePane(ctx, p)
		if err != nil {
			return
		}
		// The AI's chips list the pane while it is served.
		a.loadAI()
		defer func() {
			if a.ctx.Err() == nil {
				a.loadAI()
			}
		}()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()
		dec := json.NewDecoder(conn)
		for {
			var m localapi.AIPaneMessage
			if err := dec.Decode(&m); err != nil {
				return
			}
			answer := answerPane(src, m.Request)
			s.write(localapi.AIPaneMessage{Seq: m.Seq, Answer: answer})
		}
	}()
	return s
}

// stop ends serving the pane: a task on it fails at its next look.
func (s *paneServe) stop() { s.cancel() }

// local tells the engine the host user used the pane: a key, button or the
// wheel at once, a pointer move at most every paneMoveEvery.
func (s *paneServe) local(moved bool) {
	s.mu.Lock()
	if moved && time.Since(s.moved) < paneMoveEvery {
		s.mu.Unlock()
		return
	}
	if moved {
		s.moved = time.Now()
	}
	s.mu.Unlock()
	what := "press"
	if moved {
		what = "move"
	}
	s.write(localapi.AIPaneMessage{Local: what})
}

// write sends one message, dropped while the connection is not open.
func (s *paneServe) write(m localapi.AIPaneMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(s.conn).Encode(m)
}

// answerPane answers one engine request from src: the picture's size, the
// picture as a PNG, an input event sent, or everything released.
func answerPane(src paneSource, request json.RawMessage) json.RawMessage {
	var r struct {
		Op    string     `json:"op"`
		Event inputEvent `json:"event"`
	}
	fail := func(err error) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return b
	}
	if err := json.Unmarshal(request, &r); err != nil {
		return fail(err)
	}
	var out any = struct{}{}
	switch r.Op {
	case "size":
		w, h := src.paneSize()
		out = map[string]int{"width": w, "height": h}
	case "capture":
		pic, err := src.panePicture()
		if err != nil {
			return fail(err)
		}
		var buf bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, pic); err != nil {
			return fail(err)
		}
		out = map[string]string{"png": base64.StdEncoding.EncodeToString(buf.Bytes())}
	case "input":
		if err := src.paneInput(r.Event); err != nil {
			return fail(err)
		}
	case "release":
		src.paneRelease()
	default:
		return fail(errors.New("unknown request"))
	}
	b, _ := json.Marshal(out)
	return b
}

// paneKey is the remote key an executor's key name presses, the modifiers
// included.
func paneKey(name string) (remoteKey, bool) {
	switch name {
	case "shift":
		return keyShift, true
	case "ctrl":
		return keyCtrl, true
	case "alt":
		return keyAlt, true
	case "meta":
		return keyMeta, true
	}
	k, ok := remoteKeysByName[name]
	return k, ok
}

// paneSize is the Remote Desktop or VNC picture's size.
func (sc *screenClient) paneSize() (int, int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.fb == nil {
		return 0, 0
	}
	return sc.fb.Width, sc.fb.Height
}

// panePicture copies the Remote Desktop or VNC framebuffer.
func (sc *screenClient) panePicture() (*image.RGBA, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.fb == nil {
		return nil, errors.New("the pane shows no picture yet")
	}
	w, h := sc.fb.Width, sc.fb.Height
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := sc.fb.Pix[y*sc.fb.Stride : y*sc.fb.Stride+w*4]
		dst := out.Pix[y*out.Stride : y*out.Stride+w*4]
		for x := 0; x < len(row); x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = row[x+2], row[x+1], row[x], 255
		}
	}
	return out, nil
}

// paneInput sends one of the AI's events to the Remote Desktop or VNC
// computer: the pointer in the framebuffer's pixels with the AI's buttons,
// the wheel where the pointer is, keys by name (the modifiers around them),
// text as text.
func (sc *screenClient) paneInput(e inputEvent) error {
	w, h := sc.paneSize()
	if w == 0 {
		return errors.New("the pane shows no picture yet")
	}
	sc.mu.Lock()
	switch e.Kind {
	case "pointer":
		sc.aiAt = image.Pt(min(int(e.X*float32(w)), w-1), min(int(e.Y*float32(h)), h-1))
		// Primary, secondary, middle as the protocols number them.
		bit := map[int]uint8{0: 1, 1: 4, 2: 2}[e.Button]
		switch e.Action {
		case "down":
			sc.aiButtons |= bit
		case "up":
			sc.aiButtons &^= bit
		}
	}
	at, buttons := sc.aiAt, sc.aiButtons
	sc.mu.Unlock()
	switch e.Kind {
	case "pointer":
		return sc.desk.pointer(at.X, at.Y, buttons)
	case "scroll":
		// Lines, about 40 pixels each, as a person's wheel on the pane.
		return sc.desk.scroll(at.X, at.Y, float64(e.DX)*40, float64(e.DY)*40)
	case "text":
		return sc.desk.text(e.Text)
	case "key":
		k, ok := paneKey(e.Key)
		if !ok {
			return errors.New("the pane has no key " + e.Key)
		}
		var m inputMods
		if e.Mods != nil {
			m = *e.Mods
		}
		mods := []struct {
			on bool
			k  remoteKey
		}{{m.Shift, keyShift}, {m.Ctrl, keyCtrl}, {m.Alt, keyAlt}, {m.Meta, keyMeta}}
		if e.Down {
			for _, m := range mods {
				if m.on {
					_ = sc.desk.key(m.k, true)
				}
			}
		}
		err := sc.desk.key(k, e.Down)
		if !e.Down {
			for _, m := range mods {
				if m.on {
					_ = sc.desk.key(m.k, false)
				}
			}
		}
		return err
	}
	return errors.New("the pane cannot send " + e.Kind)
}

// paneRelease lifts the AI's buttons and the modifiers on the Remote Desktop
// or VNC computer.
func (sc *screenClient) paneRelease() {
	sc.mu.Lock()
	at := sc.aiAt
	held := sc.aiButtons
	sc.aiButtons = 0
	sc.mu.Unlock()
	if held != 0 {
		_ = sc.desk.pointer(at.X, at.Y, 0)
	}
	for _, k := range []remoteKey{keyShift, keyCtrl, keyAlt, keyMeta} {
		_ = sc.desk.key(k, false)
	}
}

// serveAI lends this computer's own NanoKVM pane to its AI, or stops.
// Notes: call on the main thread.
func (v *viewer) serveAI(on bool) {
	if v.ai != nil {
		v.ai.stop()
		v.ai = nil
	}
	if on {
		v.ai = v.app.servePane(localapi.AIPane{ID: v.tabKey(), Name: v.title, Kind: "nanokvm"}, v)
	}
}

// newest is the NanoKVM pane's newest picture (under v.mu).
func (v *viewer) newest() *image.RGBA {
	i := v.shown
	if v.ready >= 0 {
		i = v.ready
	}
	if i < 0 {
		return nil
	}
	return v.imgs[i]
}

// paneSize is the NanoKVM picture's size as this pane has it.
func (v *viewer) paneSize() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p := v.newest(); p != nil {
		return p.Rect.Dx(), p.Rect.Dy()
	}
	return 0, 0
}

// panePicture copies the NanoKVM pane's picture.
func (v *viewer) panePicture() (*image.RGBA, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.newest()
	if p == nil {
		return nil, errors.New("the pane shows no picture yet")
	}
	out := image.NewRGBA(image.Rect(0, 0, p.Rect.Dx(), p.Rect.Dy()))
	for y := 0; y < p.Rect.Dy(); y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+p.Rect.Dx()*4], p.Pix[y*p.Stride:])
	}
	return out, nil
}

// paneInput sends one of the AI's events to the computer wired to the
// NanoKVM, as a person's at the pane go.
func (v *viewer) paneInput(e inputEvent) error {
	if e.Kind == "pointer" {
		v.mu.Lock()
		v.aiX, v.aiY = e.X, e.Y
		v.mu.Unlock()
	}
	v.queue(e)
	return nil
}

// paneRelease lifts the AI's buttons and modifiers on the wired computer.
func (v *viewer) paneRelease() {
	v.mu.Lock()
	x, y := v.aiX, v.aiY
	v.mu.Unlock()
	for b := range 3 {
		v.queue(inputEvent{Kind: "pointer", Action: "up", Button: b, X: x, Y: y})
	}
	for _, k := range []string{"shift", "ctrl", "alt", "meta"} {
		v.queue(inputEvent{Kind: "key", Key: k})
	}
}
