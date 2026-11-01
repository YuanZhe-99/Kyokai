package gui

import (
	"context"
	"errors"
	"image"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp"
	"github.com/YuanZhe-99/Kyokai/internal/rfb"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func init() {
	serviceDials[serviceRDP] = dialRDP
	serviceDials[serviceVNC] = dialVNC
}

// The size Remote Desktop opens the other desktop at, and how long a
// sign-in may take.
const (
	rdpWidth, rdpHeight = 1600, 900
	remoteTimeout       = 30 * time.Second
)

// dialRDP is the Remote Desktop tab's serviceDial, with the method chosen
// or remembered: Network Level Authentication with a user name and
// password, or no sign-in (TLS alone: the Windows sign-in screen asks).
// Returns: errNoSecureSignIn when a server met with a password offers no
// Network Level Authentication (the password is never sent over TLS alone).
// Notes: the server's certificate is not pinned: the tunnel reaches the
// device's own port inside an authenticated Kyokai session; NLA's CredSSP
// is what proves the server before a password reaches it.
func dialRDP(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	plan, ok := s.ask(typed, s.savedCredential(ctx))
	if !ok {
		return nil, errNeedSignIn
	}
	opts := rdp.Options{Width: rdpWidth, Height: rdpHeight, Clipboard: true, DialTimeout: remoteTimeout,
		VerifyCertificate: func(string) error { return nil }}
	if plan.method == methodAccount {
		opts.Username, opts.Domain, opts.Password = plan.user, plan.domain, plan.password
	}
	conn, err := s.app.link.Open(ctx, s.device, s.address, serviceRDP)
	if err != nil {
		return nil, err
	}
	opts.Conn = conn
	sc := newScreen(s)
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	c, err := rdp.Dial(ctx, opts, rdpEvents{sc})
	switch {
	case errors.Is(err, rdp.ErrNLAUnavailable):
		return nil, errNoSecureSignIn
	case errors.Is(err, rdp.ErrUnsupportedSecurity) && plan.method == methodNone:
		// The server allows only Network Level Authentication.
		return nil, errSignInRequired
	case errors.Is(err, rdp.ErrAuthenticationFailed), errors.Is(err, rdp.ErrRemoteLogonDenied):
		return nil, errSignInFailed
	case err != nil:
		return nil, err
	}
	sc.desk = rdpDesk{c}
	s.keep(ctx, plan, typed != nil)
	go func() {
		err := guard(c.Run)
		note := L.ServiceEnded()
		switch {
		case errors.Is(err, errUnreadable):
			note = L.ServiceUnreadable()
		case errors.Is(err, rdp.ErrSessionReplaced):
			note = L.ServiceReplaced()
		}
		s.ended(sc, note)
	}()
	return sc, nil
}

// rdpDesk is a Remote Desktop connection as its tab drives it.
type rdpDesk struct{ c *rdp.Client }

func (d rdpDesk) pointer(x, y int, buttons uint8) error { return d.c.SendPointer(x, y, buttons) }
func (d rdpDesk) scroll(x, y int, dx, dy float64) error { return d.c.SendScroll(x, y, dx, dy) }
func (d rdpDesk) key(k remoteKey, down bool) error {
	return d.c.SendScancode(uint8(k.scan), k.scan&0xE000 != 0, down)
}
func (d rdpDesk) text(s string) error         { return d.c.SendText(s) }
func (d rdpDesk) clipboard(text string) error { return d.c.SetClipboard(text) }
func (d rdpDesk) close()                      { _ = d.c.Close() }

// rdpEvents are the Remote Desktop client's events for a picture tab.
type rdpEvents struct{ sc *screenClient }

func (rdpEvents) StateChanged(rdp.State) {}
func (rdpEvents) InfoChanged(rdp.Info)   {}
func (e rdpEvents) ResizeFramebuffer(w, h int) (*bgra.Image, error) {
	return e.sc.resize(w, h), nil
}
func (e rdpEvents) FramebufferUpdated(u rdp.Update)  { e.sc.updated(u.Rectangle) }
func (rdpEvents) CursorChanged(*bgra.Cursor)         {}
func (rdpEvents) CursorMoved(int, int)               {}
func (e rdpEvents) ClipboardReceived(text string)    { e.sc.remoteClipboard(text) }
func (rdpEvents) Bell()                              {}
func (rdpEvents) AudioFormatChanged(rdp.AudioFormat) {}
func (rdpEvents) AudioPacketReceived([]byte, uint64) {}
func (rdpEvents) MicrophoneOpened(rdp.AudioFormat)   {}
func (rdpEvents) MicrophoneClosed()                  {}

// vncAccountTypes are the only VNC security types a user name and password
// go to: Apple's SRP (36), alone or behind its RSA envelope (33 with 36,
// which rfb accepts only alongside 36). Never Apple's anonymous
// Diffie-Hellman (30), the VNC password's DES response (2) or none (1).
var vncAccountTypes = []rfb.SecurityType{rfb.SecurityAppleRSA, rfb.SecurityAppleSRP}

// dialVNC is the VNC tab's serviceDial, with the method chosen or
// remembered: a VNC password alone, a Mac's user name and password (Apple's
// SRP sign-ins alone), or no sign-in.
// Returns: errNoSecureSignIn when a user name and password meet a server
// without Apple's SRP; nothing is sent to it.
func dialVNC(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	plan, ok := s.ask(typed, s.savedCredential(ctx))
	if !ok {
		return nil, errNeedSignIn
	}
	opts := rfb.Options{Mode: rfb.ModeControl, Clipboard: true, AutomaticUpdates: true}
	switch plan.method {
	case methodNone:
		opts.SecurityTypes = []rfb.SecurityType{rfb.SecurityNone}
	case methodPassword:
		opts.Password = plan.password
		opts.SecurityTypes = []rfb.SecurityType{rfb.SecurityVNCPassword}
	default:
		opts.Username, opts.Password = plan.user, plan.password
		opts.SecurityTypes = vncAccountTypes
	}
	conn, err := s.app.link.Open(ctx, s.device, s.address, serviceVNC)
	if err != nil {
		return nil, err
	}
	opts.Conn = conn
	sc := newScreen(s)
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	c, err := rfb.Dial(ctx, opts, vncEvents{sc})
	switch {
	case plan.method == methodAccount && (errors.Is(err, rfb.ErrUnverifiedServer) || errors.Is(err, rfb.ErrUnsupportedServer)):
		return nil, errNoSecureSignIn
	case err != nil:
		return nil, vncSignInError(err, plan.method == methodNone)
	}
	sc.desk = vncDesk{c}
	s.keep(ctx, plan, typed != nil)
	go func() {
		note := L.ServiceEnded()
		if errors.Is(guard(c.Run), errUnreadable) {
			note = L.ServiceUnreadable()
		}
		s.ended(sc, note)
	}()
	return sc, nil
}

// vncSignInError is what a VNC dial's error means to its tab.
// Inputs: none, the method was "no sign-in".
// Returns: errSignInRequired when a server that needs a sign-in got none;
// errSignInFailed when the server refused the sign-in, or closed the
// connection during it (a Mac drops repeated failures instead of
// answering); else err (a server offering no sign-in Kyokai has stays
// rfb.ErrUnsupportedServer).
func vncSignInError(err error, none bool) error {
	switch {
	case none && (errors.Is(err, rfb.ErrUnsupportedServer) || errors.Is(err, rfb.ErrAuthenticationFailed)):
		return errSignInRequired
	case errors.Is(err, rfb.ErrAuthenticationFailed), errors.Is(err, rfb.ErrPermissionDenied),
		errors.Is(err, rfb.ErrSignInDropped):
		return errSignInFailed
	}
	return err
}

// vncDesk is a VNC connection as its tab drives it.
type vncDesk struct{ c *rfb.Client }

func (d vncDesk) pointer(x, y int, buttons uint8) error { return d.c.SendPointer(x, y, buttons, 0) }
func (d vncDesk) scroll(x, y int, dx, dy float64) error { return d.c.SendScroll(x, y, dx, dy) }
func (d vncDesk) key(k remoteKey, down bool) error {
	if k.sym == 0 {
		return nil
	}
	return d.c.SendKey(k.sym, down)
}
func (d vncDesk) text(s string) error { return d.c.SendText(s) }
func (d vncDesk) clipboard(text string) error {
	return d.c.SetClipboard(rfb.NewTextClipboard(text))
}
func (d vncDesk) close() { _ = d.c.Close() }

// vncEvents are the VNC client's events for a picture tab.
type vncEvents struct{ sc *screenClient }

func (vncEvents) StateChanged(rfb.State) {}
func (e vncEvents) ResizeFramebuffer(w, h int) (*bgra.Image, error) {
	return e.sc.resize(w, h), nil
}
func (e vncEvents) FramebufferUpdated(u rfb.Update) {
	d := u.Dirty
	e.sc.updated(image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height))
}
func (vncEvents) CursorChanged(*rfb.Cursor)          {}
func (vncEvents) CursorMoved(int, int)               {}
func (vncEvents) DisplaysChanged(*rfb.DisplayLayout) {}
func (e vncEvents) ClipboardReceived(cb *rfb.Clipboard) {
	if text, ok := cb.Text(); ok {
		e.sc.remoteClipboard(text)
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
	// A Mac signed in by someone else: share its screen rather than open
	// a session of one's own, as Screen Sharing offers first.
	return rfb.SessionShareConsole, nil
}
