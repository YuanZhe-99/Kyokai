package gui

import (
	"context"
	"errors"
	"image"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
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

// signIn is the user, domain and password a dial uses: what the form sent,
// else what is saved.
func signIn(typed *credForm, saved *localapi.Credentials) (user, domain, password string) {
	if typed != nil {
		return typed.user, typed.domain, typed.password
	}
	if saved != nil {
		user, domain = saved.User, saved.Domain
		if saved.Password != nil {
			password = *saved.Password
		}
	}
	return user, domain, password
}

// keepSignIn saves what the form sent once it worked: with the password
// when Remember is on, else the user name alone.
func (s *serviceTab) keepSignIn(ctx context.Context, typed *credForm) {
	if typed == nil || typed.user == "" && !typed.remember {
		return
	}
	cr := localapi.Credentials{User: typed.user, Domain: typed.domain}
	if typed.remember {
		password := typed.password
		cr.Password = &password
	}
	_ = s.app.link.SaveCredential(ctx, s.device, s.service, cr)
}

// dialRDP is the Remote Desktop tab's serviceDial: Network Level
// Authentication with a user name, TLS alone without one (the Windows
// sign-in screen then asks).
// Notes: the server's certificate is not pinned: the tunnel reaches the
// device's own port inside an authenticated Kyokai session.
func dialRDP(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	saved := s.savedCredential(ctx)
	user, domain, password := signIn(typed, saved)
	s.mu.Lock()
	if typed == nil && saved != nil {
		s.form.user, s.form.domain = saved.User, saved.Domain
	}
	s.mu.Unlock()
	conn, err := s.app.link.Open(ctx, s.device, s.address, serviceRDP)
	if err != nil {
		return nil, err
	}
	sc := newScreen(s)
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	c, err := rdp.Dial(ctx, rdp.Options{Conn: conn, Username: user, Domain: domain, Password: password,
		Width: rdpWidth, Height: rdpHeight, Clipboard: true, DialTimeout: remoteTimeout,
		VerifyCertificate: func(string) error { return nil }}, rdpEvents{sc})
	switch {
	case errors.Is(err, rdp.ErrAuthenticationFailed) && typed == nil && user == "",
		errors.Is(err, rdp.ErrUnsupportedSecurity) && user == "":
		return nil, errNeedSignIn
	case errors.Is(err, rdp.ErrAuthenticationFailed), errors.Is(err, rdp.ErrRemoteLogonDenied):
		return nil, errSignInFailed
	case err != nil:
		return nil, err
	}
	sc.desk = rdpDesk{c}
	s.keepSignIn(ctx, typed)
	go func() {
		err := c.Run()
		note := L.ServiceEnded()
		if errors.Is(err, rdp.ErrSessionReplaced) {
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

// dialVNC is the VNC tab's serviceDial: no security, a VNC password, or a
// Mac's user name and password (Apple's sign-ins), whichever the server
// offers first.
func dialVNC(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	saved := s.savedCredential(ctx)
	user, _, password := signIn(typed, saved)
	s.mu.Lock()
	s.formUser = true
	if typed == nil && saved != nil {
		s.form.user = saved.User
	}
	s.mu.Unlock()
	conn, err := s.app.link.Open(ctx, s.device, s.address, serviceVNC)
	if err != nil {
		return nil, err
	}
	sc := newScreen(s)
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	// Nothing to sign in with: only a server that needs no sign-in is
	// taken; any other asks for one in the form, without a failed attempt
	// (a Mac slows down and then drops repeated ones).
	var only []rfb.SecurityType
	if user == "" && password == "" {
		only = []rfb.SecurityType{rfb.SecurityNone}
	}
	c, err := rfb.Dial(ctx, rfb.Options{Conn: conn, Username: user, Password: password, Mode: rfb.ModeControl,
		SecurityTypes: only, Clipboard: true, AllowUnverifiedLogin: true, AutomaticUpdates: true}, vncEvents{sc})
	if err != nil {
		return nil, vncSignInError(err, only != nil)
	}
	sc.desk = vncDesk{c}
	s.keepSignIn(ctx, typed)
	go func() {
		_ = c.Run()
		s.ended(sc, L.ServiceEnded())
	}()
	return sc, nil
}

// vncSignInError is what a VNC dial's error means to its tab.
// Inputs: blank, the dial had nothing to sign in with (only "None" asked).
// Returns: errNeedSignIn when a server that needs a sign-in got none;
// errSignInFailed when the server refused one, or closed the connection
// during it (a Mac drops repeated failures instead of answering); else
// err.
func vncSignInError(err error, blank bool) error {
	switch {
	case blank && (errors.Is(err, rfb.ErrUnsupportedServer) || errors.Is(err, rfb.ErrAuthenticationFailed)):
		return errNeedSignIn
	case errors.Is(err, rfb.ErrAuthenticationFailed), errors.Is(err, rfb.ErrPermissionDenied):
		return errSignInFailed
	case errors.Is(err, rfb.ErrSignInDropped):
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
