package main

import (
	"encoding/binary"
	"encoding/json"
	"image"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

// Packet kinds kyokai_clients_read returns, byte 0 of each.
const (
	// packetState is JSON {"state": "connecting"|"connected"|"failed"|
	// "closed", "error"?: a reason (calls.go), "host_key"?: an SSH server's
	// key line for the person to trust, "width"?, "height"?}.
	packetState = 1
	// packetData is what an SSH shell wrote, raw.
	packetData = 2
	// packetFrame is part of a remote picture: x, y, width, height of the
	// part and the whole picture's width and height, u32 big-endian each,
	// then the part's pixels, R,G,B,A rows without padding, alpha 255.
	packetFrame = 3
	// packetClipboard is text the host copied, UTF-8.
	packetClipboard = 4
)

// client is one connection the app opened: its packets wait here until the
// app reads them. A picture is not queued: its latest pixels and the part
// changed since the last read make one frame packet at read time, so a slow
// reader never falls behind.
type client struct {
	id     uint64
	mu     sync.Mutex
	wake   chan struct{} // one token: something to read
	queue  [][]byte
	held   []byte // a packet a read had no room for
	ended  bool   // nothing more will be queued
	closer func()
	stop   func() // cancels the dial

	// The picture: its pixels as RGBA, and the part not read yet.
	pic   *image.RGBA
	dirty image.Rectangle

	// The connection's own input (desktop.go, ssh.go), set once connected.
	desk desk
	term *sshTerm
}

func newClient(id uint64) *client {
	return &client{id: id, wake: make(chan struct{}, 1)}
}

func (c *client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// push queues a packet of kind with body.
func (c *client) push(kind byte, body []byte) {
	p := make([]byte, 1+len(body))
	p[0] = kind
	copy(p[1:], body)
	c.mu.Lock()
	if !c.ended {
		c.queue = append(c.queue, p)
	}
	c.mu.Unlock()
	c.signal()
}

// state queues a state packet.
func (c *client) state(fields map[string]any) {
	body, _ := json.Marshal(fields)
	c.push(packetState, body)
}

// end queues the client's last state; reads then drain the queue and end.
func (c *client) end(fields map[string]any) {
	c.state(fields)
	c.mu.Lock()
	c.ended = true
	c.mu.Unlock()
	c.signal()
}

// resized starts a picture of w by h, all of it to be read.
func (c *client) resized(w, h int) {
	c.mu.Lock()
	c.pic = image.NewRGBA(image.Rect(0, 0, w, h))
	c.dirty = c.pic.Rect
	c.mu.Unlock()
}

// updated copies the changed part r of the client's framebuffer into the
// picture, opaque, and marks it to be read.
func (c *client) updated(fb *bgra.Image, r image.Rectangle) {
	c.mu.Lock()
	pic := c.pic
	if pic == nil || fb == nil || fb.Width != pic.Rect.Dx() || fb.Height != pic.Rect.Dy() {
		c.mu.Unlock()
		return
	}
	r = r.Intersect(pic.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := fb.Pix[y*fb.Stride+r.Min.X*4 : y*fb.Stride+r.Max.X*4]
		dst := pic.Pix[y*pic.Stride+r.Min.X*4 : y*pic.Stride+r.Max.X*4]
		for i := 0; i+3 < len(src); i += 4 {
			dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 255
		}
	}
	c.dirty = c.dirty.Union(r)
	c.mu.Unlock()
	c.signal()
}

// read takes the next packet into dst, waiting up to timeout.
// Returns: its length; -1 none; -2 ended; below -3, minus the length needed.
func (c *client) read(dst []byte, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		p := c.next()
		ended := c.ended && p == nil
		c.mu.Unlock()
		if p != nil {
			if len(p) > len(dst) {
				c.mu.Lock()
				c.held = p
				c.mu.Unlock()
				return -int64(len(p))
			}
			return int64(copy(dst, p))
		}
		if ended {
			clients.remove(c.id)
			return -2
		}
		left := time.Until(deadline)
		if left <= 0 {
			return -1
		}
		select {
		case <-c.wake:
		case <-time.After(left):
		}
	}
}

// next is the packet to read now: one held back, a queued one, else a frame
// of what changed. Called with mu held.
func (c *client) next() []byte {
	if p := c.held; p != nil {
		c.held = nil
		return p
	}
	if len(c.queue) > 0 {
		p := c.queue[0]
		c.queue = c.queue[1:]
		return p
	}
	if c.pic == nil || c.dirty.Empty() {
		return nil
	}
	r := c.dirty
	c.dirty = image.Rectangle{}
	w, h := r.Dx(), r.Dy()
	p := make([]byte, 1+24+w*h*4)
	p[0] = packetFrame
	for i, v := range []int{r.Min.X, r.Min.Y, w, h, c.pic.Rect.Dx(), c.pic.Rect.Dy()} {
		binary.BigEndian.PutUint32(p[1+i*4:], uint32(v))
	}
	at := 25
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := c.pic.Pix[y*c.pic.Stride+r.Min.X*4 : y*c.pic.Stride+r.Max.X*4]
		at += copy(p[at:], row)
	}
	return p
}

// registry is the open clients by id.
type registry struct {
	mu   sync.Mutex
	next uint64
	all  map[uint64]*client
}

var clients = registry{all: map[uint64]*client{}}

func (r *registry) add() *client {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	c := newClient(r.next)
	r.all[c.id] = c
	return c
}

func (r *registry) get(id uint64) *client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.all[id]
}

func (r *registry) remove(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.all, id)
}

// pushData queues a shell's output, waiting while the app has a backlog to
// read, so a fast shell slows down instead of filling memory.
func (c *client) pushData(b []byte) {
	for {
		c.mu.Lock()
		backlog := len(c.queue)
		ended := c.ended
		c.mu.Unlock()
		if ended || backlog < 256 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.push(packetData, b)
}
