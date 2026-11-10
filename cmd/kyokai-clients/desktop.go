package main

// Remote Desktop and VNC, as the desktop's internal/gui/desktops.go dials
// them: the picture goes to the client's frame packets, input comes from
// calls.

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp"
	"github.com/YuanZhe-99/Kyokai/internal/rfb"
)

// remoteTimeout bounds a Remote Desktop or VNC handshake and sign-in.
const remoteTimeout = 30 * time.Second

var (
	errNoSecureSignIn = errors.New("no-secure-sign-in")
	errSignInRequired = errors.New("sign-in-required")
)

// desk is a connected Remote Desktop or VNC client as calls drive it.
type desk interface {
	pointer(x, y int, buttons uint8) error
	scroll(x, y int, dx, dy float64) error
	scancode(code int, down bool) error
	key(keysym uint32, down bool) error
	text(s string) error
	clipboard(text string) error
	close()
}

// framebuffer is the buffer a client decodes into, shared with its events.
type framebuffer struct {
	mu sync.Mutex
	fb *bgra.Image
}

func (f *framebuffer) resize(c *client, w, h int) *bgra.Image {
	img := bgra.New(w, h)
	f.mu.Lock()
	f.fb = img
	f.mu.Unlock()
	c.resized(w, h)
	c.state(map[string]any{"state": "connected", "width": w, "height": h})
	return img
}

func (f *framebuffer) updated(c *client, r image.Rectangle) {
	f.mu.Lock()
	fb := f.fb
	f.mu.Unlock()
	c.updated(fb, r)
}

// runRDP connects client c to a device's Remote Desktop.
func runRDP(ctx context.Context, c *client, p openParams) {
	saved := savedCredential(p.DeviceID, "rdp")
	plan := p.plan(saved)
	if plan.Method == "" {
		c.end(map[string]any{"state": "failed", "error": reason(errNeedSignIn)})
		return
	}
	w, h := p.Width, p.Height
	if w < 640 || h < 480 {
		w, h = 1600, 900
	}
	opts := rdp.Options{Width: w, Height: h, Clipboard: true, DialTimeout: remoteTimeout,
		VerifyCertificate: func(string) error { return nil }}
	if plan.Method == "account" {
		opts.Username, opts.Domain, opts.Password = plan.User, plan.Domain, plan.Password
	}
	conn, err := openTunnel(ctx, p.DeviceID, p.Address, "rdp")
	if err != nil {
		c.end(map[string]any{"state": "failed", "error": reason(err)})
		return
	}
	opts.Conn = conn
	fb := &framebuffer{}
	dctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	rc, err := rdp.Dial(dctx, opts, rdpEvents{c, fb})
	cancel()
	switch {
	case errors.Is(err, rdp.ErrNLAUnavailable):
		err = errNoSecureSignIn
	case errors.Is(err, rdp.ErrUnsupportedSecurity) && plan.Method == "none":
		err = errSignInRequired
	case errors.Is(err, rdp.ErrAuthenticationFailed), errors.Is(err, rdp.ErrRemoteLogonDenied):
		err = errSignInFailed
	}
	if err != nil {
		_ = conn.Close()
		c.end(map[string]any{"state": "failed", "error": reason(err)})
		return
	}
	if cr := kept(plan, p.Remember, p.typed()); cr != nil {
		saveCredential(p.DeviceID, "rdp", *cr)
	}
	d := rdpDesk{rc}
	c.mu.Lock()
	c.desk = d
	c.closer = d.close
	c.mu.Unlock()
	err = guard(rc.Run)
	_ = conn.Close()
	why := "ended"
	switch {
	case errors.Is(err, rdp.ErrSessionReplaced):
		why = "replaced"
	case errors.Is(err, errUnreadable):
		why = "unreadable"
	}
	c.end(map[string]any{"state": "closed", "error": why})
}

type rdpDesk struct{ c *rdp.Client }

func (d rdpDesk) pointer(x, y int, b uint8) error       { return d.c.SendPointer(x, y, b) }
func (d rdpDesk) scroll(x, y int, dx, dy float64) error { return d.c.SendScroll(x, y, dx, dy) }
func (d rdpDesk) scancode(code int, down bool) error {
	return d.c.SendScancode(uint8(code), code&0xE000 != 0, down)
}
func (d rdpDesk) key(sym uint32, down bool) error { return d.c.SendKey(sym, down) }
func (d rdpDesk) text(s string) error             { return d.c.SendText(s) }
func (d rdpDesk) clipboard(s string) error        { return d.c.SetClipboard(s) }
func (d rdpDesk) close()                          { _ = d.c.Close() }

type rdpEvents struct {
	c  *client
	fb *framebuffer
}

func (rdpEvents) StateChanged(rdp.State) {}
func (rdpEvents) InfoChanged(rdp.Info)   {}
func (e rdpEvents) ResizeFramebuffer(w, h int) (*bgra.Image, error) {
	return e.fb.resize(e.c, w, h), nil
}
func (e rdpEvents) FramebufferUpdated(u rdp.Update)  { e.fb.updated(e.c, u.Rectangle) }
func (rdpEvents) CursorChanged(*bgra.Cursor)         {}
func (rdpEvents) CursorMoved(int, int)               {}
func (e rdpEvents) ClipboardReceived(text string)    { e.c.push(packetClipboard, []byte(text)) }
func (rdpEvents) Bell()                              {}
func (rdpEvents) AudioFormatChanged(rdp.AudioFormat) {}
func (rdpEvents) AudioPacketReceived([]byte, uint64) {}
func (rdpEvents) MicrophoneOpened(rdp.AudioFormat)   {}
func (rdpEvents) MicrophoneClosed()                  {}

// vncAccountTypes: a user name and password go only to Apple's SRP (alone
// or behind its RSA envelope).
var vncAccountTypes = []rfb.SecurityType{rfb.SecurityAppleRSA, rfb.SecurityAppleSRP}

// runVNC connects client c to a device's VNC server.
func runVNC(ctx context.Context, c *client, p openParams) {
	saved := savedCredential(p.DeviceID, "vnc")
	plan := p.plan(saved)
	opts := rfb.Options{Mode: rfb.ModeControl, Clipboard: true, AutomaticUpdates: true}
	switch plan.Method {
	case "none":
		opts.SecurityTypes = []rfb.SecurityType{rfb.SecurityNone}
	case "password":
		opts.Password = plan.Password
		opts.SecurityTypes = []rfb.SecurityType{rfb.SecurityVNCPassword}
	case "account":
		opts.Username, opts.Password = plan.User, plan.Password
		opts.SecurityTypes = vncAccountTypes
	default:
		c.end(map[string]any{"state": "failed", "error": reason(errNeedSignIn)})
		return
	}
	conn, err := openTunnel(ctx, p.DeviceID, p.Address, "vnc")
	if err != nil {
		c.end(map[string]any{"state": "failed", "error": reason(err)})
		return
	}
	opts.Conn = conn
	fb := &framebuffer{}
	dctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	vc, err := rfb.Dial(dctx, opts, vncEvents{c, fb})
	cancel()
	switch {
	case err == nil:
	case plan.Method == "account" && (errors.Is(err, rfb.ErrUnverifiedServer) || errors.Is(err, rfb.ErrUnsupportedServer)):
		err = errNoSecureSignIn
	case plan.Method == "none" && (errors.Is(err, rfb.ErrUnsupportedServer) || errors.Is(err, rfb.ErrAuthenticationFailed)):
		err = errSignInRequired
	case errors.Is(err, rfb.ErrAuthenticationFailed), errors.Is(err, rfb.ErrPermissionDenied), errors.Is(err, rfb.ErrSignInDropped):
		err = errSignInFailed
	}
	if err != nil {
		_ = conn.Close()
		c.end(map[string]any{"state": "failed", "error": reason(err)})
		return
	}
	if cr := kept(plan, p.Remember, p.typed()); cr != nil {
		saveCredential(p.DeviceID, "vnc", *cr)
	}
	d := vncDesk{vc}
	c.mu.Lock()
	c.desk = d
	c.closer = d.close
	c.mu.Unlock()
	err = guard(vc.Run)
	_ = conn.Close()
	why := "ended"
	if errors.Is(err, errUnreadable) {
		why = "unreadable"
	}
	c.end(map[string]any{"state": "closed", "error": why})
}

type vncDesk struct{ c *rfb.Client }

func (d vncDesk) pointer(x, y int, b uint8) error       { return d.c.SendPointer(x, y, b, 0) }
func (d vncDesk) scroll(x, y int, dx, dy float64) error { return d.c.SendScroll(x, y, dx, dy) }
func (d vncDesk) scancode(int, bool) error              { return nil } // VNC takes keysyms
func (d vncDesk) key(sym uint32, down bool) error       { return d.c.SendKey(sym, down) }
func (d vncDesk) text(s string) error                   { return d.c.SendText(s) }
func (d vncDesk) clipboard(s string) error {
	return d.c.SetClipboard(rfb.NewTextClipboard(s))
}
func (d vncDesk) close() { _ = d.c.Close() }

type vncEvents struct {
	c  *client
	fb *framebuffer
}

func (vncEvents) StateChanged(rfb.State) {}
func (e vncEvents) ResizeFramebuffer(w, h int) (*bgra.Image, error) {
	return e.fb.resize(e.c, w, h), nil
}
func (e vncEvents) FramebufferUpdated(u rfb.Update) {
	d := u.Dirty
	e.fb.updated(e.c, image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height))
}
func (vncEvents) CursorChanged(*rfb.Cursor)          {}
func (vncEvents) CursorMoved(int, int)               {}
func (vncEvents) DisplaysChanged(*rfb.DisplayLayout) {}
func (e vncEvents) ClipboardReceived(cb *rfb.Clipboard) {
	if text, ok := cb.Text(); ok {
		e.c.push(packetClipboard, []byte(text))
	}
}
func (vncEvents) ClipboardFailed(rfb.ClipboardProblem, error) {}
func (vncEvents) Bell()                                       {}
func (vncEvents) KeyboardSourceChanged(rfb.KeyboardSource)    {}
func (vncEvents) StatusReceived(rfb.Status)                   {}
func (vncEvents) DeviceInfoReceived(rfb.DeviceInfo)           {}
func (vncEvents) RemoteDragChanged(*rfb.RemoteDrag)           {}
func (vncEvents) DropDestinationReceived(uint32, string)      {}
func (vncEvents) SystemCommandsChanged([]rfb.SystemCommand)   {}
func (vncEvents) ChooseSession(rfb.SessionRequest) (rfb.SessionChoice, error) {
	// A Mac signed in by someone else: share its screen.
	return rfb.SessionShareConsole, nil
}

// errUnreadable is a client that panicked on what the server sent.
var errUnreadable = errors.New("unreadable")

// guard runs f, a client's protocol loop, turning a panic into errUnreadable.
func guard(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errUnreadable
		}
	}()
	return f()
}
