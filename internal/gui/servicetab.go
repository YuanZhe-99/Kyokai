package gui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/rfb"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// A service tab's phases: connecting, asking for a sign-in, showing its
// client, or ended (why is in serviceTab.note).
const (
	svcConnecting = "connecting"
	svcForm       = "form"
	svcRunning    = "running"
	svcEnded      = "ended"
)

// How a service client's dial fails, beyond the connection's own errors.
var (
	// errNeedSignIn: nothing usable is saved; the tab asks for a sign-in.
	errNeedSignIn = errors.New("a sign-in is needed")
	// errSignInFailed: the service refused the sign-in.
	errSignInFailed = errors.New("the sign-in was refused")
	// errSignInRequired: "no sign-in" met a server that needs one; the tab
	// asks how to sign in.
	errSignInRequired = errors.New("the server needs a sign-in")
	// errHostKeyChanged: the SSH server's key is not the one pinned; no
	// password was sent.
	errHostKeyChanged = errors.New("the host key changed")
	// errNoAnswer: the service went quiet during the handshake or sign-in
	// (an SSH server holding failed or penalized sign-ins).
	errNoAnswer = errors.New("the service did not answer")
)

// credForm is a service tab's sign-in form while it shows. The password is
// handed to the client as the form is sent and cleared here.
type credForm struct {
	method                 string
	user, domain, password string
	remember               bool
}

// How a service tab signs in (localapi.Credentials.Method), chosen in its
// form and remembered with Remember.
const (
	// methodKey: SSH with a user name and this device's keys.
	methodKey = "key"
	// methodPassword: SSH with a user name and password; VNC with its
	// password alone.
	methodPassword = "password"
	// methodAccount: a user name and password — a Mac's for VNC, a
	// Windows account for Remote Desktop.
	methodAccount = "account"
	// methodNone: no sign-in — a VNC server without one, or Remote
	// Desktop's own sign-in screen.
	methodNone = "none"
)

// signInMethods are the methods each service offers, in the form's order.
var signInMethods = map[string][]string{
	serviceSSH: {methodKey, methodPassword},
	serviceVNC: {methodPassword, methodAccount, methodNone},
	serviceRDP: {methodAccount, methodNone},
}

// signInPlan is the sign-in a dial uses: what the form sent, else the
// method remembered for the device's service with its sign-in.
// Returns: false when the tab asks first — nothing was remembered (a first
// connection, or Remember was off), or a method the service does not offer.
// Notes: nothing is tried before the person chose (no key, no blank
// password); a remembered method that fails asks again.
func signInPlan(service string, typed *credForm, saved *localapi.Credentials) (credForm, bool) {
	if typed != nil {
		return *typed, true
	}
	if saved == nil || !slices.Contains(signInMethods[service], saved.Method) {
		return credForm{}, false
	}
	f := credForm{method: saved.Method, user: saved.User, domain: saved.Domain, remember: true}
	if saved.Password != nil {
		f.password = *saved.Password
	}
	return f, true
}

// askedFor is the form a tab shows for a sign-in: plan's method, user and
// domain (never its password), else what is saved, else the service's
// first method.
func askedFor(service string, plan credForm, saved *localapi.Credentials) credForm {
	f := credForm{method: plan.method, user: plan.user, domain: plan.domain, remember: plan.remember}
	if f.method == "" && saved != nil {
		f.method, f.user, f.domain = saved.Method, saved.User, saved.Domain
	}
	if !slices.Contains(signInMethods[service], f.method) {
		f.method = signInMethods[service][0]
	}
	return f
}

// kept is what a sign-in that worked leaves saved for the service: with
// Remember, its method and password (none for a method without one), so
// the next connection uses it; without, the user name and domain alone, and
// the method and password remembered before are forgotten.
// Returns: nil when nothing changes (the sign-in was the remembered one).
func kept(plan credForm, typed bool) *localapi.Credentials {
	if !typed {
		return nil
	}
	none := ""
	cr := localapi.Credentials{User: plan.user, Domain: plan.domain, Password: &none}
	if plan.remember {
		cr.Method = plan.method
		if plan.method != methodKey && plan.method != methodNone {
			password := plan.password
			cr.Password = &password
		}
	}
	return &cr
}

// serviceClient is what a running service tab shows: SSH's terminal, or a
// Remote Desktop or VNC picture.
type serviceClient interface {
	// view draws the client in the tab.
	view(c *ui.Context, s *serviceTab)
	// close ends the client's connection (any goroutine).
	close()
	// grabs reports whether the client takes the keyboard through the
	// core's grab (Map and Translate), rather than from the toolkit.
	grabs() bool
	// scan passes it a key the grab took.
	scan(ev core.KeyEvent)
	// keys is what takes the keyboard for the other computer: the
	// picture or the terminal (main thread).
	keys() *ui.Handle
	// setFront tells it whether its tab is the one in front, which alone
	// exchanges the clipboard.
	setFront(on bool)
}

// serviceDial connects a service's client: typed is the sign-in the form
// sent, nil to use what is saved. Returns errNeedSignIn, errSignInFailed or
// errHostKeyChanged as the tab should react.
type serviceDial func(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error)

// serviceDials are the clients by service; ssh.go and desktops.go
// register theirs.
var serviceDials = map[string]serviceDial{}

// serviceTab is a connection to one of a device's own services (SSH,
// Remote Desktop, VNC) through a tunnel inside a Kyokai session, shown as a
// tab like a desktop's (doc/en-us/remote-services.md, "Clients"): a loading
// indicator, the sign-in form when nothing saved works, then the client.
// Fields after mu are guarded by it; credentials are never kept here.
type serviceTab struct {
	app     *App
	device  string
	service string
	address string
	// key is its tabKey (serviceKey, with its number for a second SSH pane
	// and after); kind the service in words, numbered from the second.
	key  string
	kind string
	// name is the device's name and Device ID (deviceTitle); title the
	// tab's, with kind.
	name  string
	title string
	home  atomic.Pointer[viewerWindow]

	mu    sync.Mutex
	phase string
	note  string
	form  credForm
	// hostKey is a changed SSH key the person chose to trust (a public key
	// line), accepted by the next dial.
	hostKey string
	changed string // the changed SSH key last seen, to trust on request
	client  serviceClient
	cancel  context.CancelFunc
	// try counts dials: the result of an older one is dropped.
	try int
	// The keyboard grab, as the viewer's (viewerinput.go).
	grabbed string
	noGrab  bool
	win     uintptr
	front   bool
}

// serviceKey is a service tab's tabKey: the device's first pane of a
// service, and with n from 2 its further SSH panes.
func serviceKey(device, service string, n ...int) string {
	k := device + "/" + service
	if len(n) > 0 && n[0] > 1 {
		k += "/" + strconv.Itoa(n[0])
	}
	return k
}

// serviceName words a service.
func serviceName(service string) string {
	switch service {
	case serviceSSH:
		return L.ServiceSSH()
	case serviceRDP:
		return L.ServiceRDP()
	}
	return L.ServiceVNC()
}

// openService opens a device's service in a tab of the main window: SSH in
// a new one each time, numbered from the second ("SSH 2"), each its own
// connection; Remote Desktop and VNC, a session of the remote screen each,
// show their tab when it is open, and one that ended connects again.
// Returns: the tab's tabKey.
// Notes: call on the main thread.
func (a *App) openService(device, address, service string) string {
	n := 1
	if service == serviceSSH {
		a.mu.Lock()
		for a.services[serviceKey(device, service, n)] != nil {
			n++
		}
		a.mu.Unlock()
	} else if t := a.tabFor(serviceKey(device, service)); t != nil {
		s := t.(*serviceTab)
		if w := s.tabWindow(); w != nil {
			w.show(s)
		}
		s.mu.Lock()
		ended := s.phase == svcEnded
		s.mu.Unlock()
		if ended {
			s.connect(nil)
		}
		return s.key
	}
	a.mu.Lock()
	about := a.prefs.named(device, localapi.About{})
	for _, r := range a.statusRecent() {
		if r.DeviceID == device {
			about = a.prefs.named(device, r.About)
		}
	}
	name := deviceTitle(about, device)
	kind := serviceName(service)
	if n > 1 {
		kind = L.ServiceNumbered(kind, n)
	}
	s := &serviceTab{app: a, device: device, service: service, address: address, name: name,
		key: serviceKey(device, service, n), kind: kind, title: L.ServiceTabTitle(name, kind)}
	a.services[s.key] = s
	a.mu.Unlock()
	a.main.add(s)
	s.connect(nil)
	return s.key
}

// statusRecent is the engine's recent devices (caller holds a.mu).
func (a *App) statusRecent() []localapi.RecentDevice {
	if a.status == nil {
		return nil
	}
	return a.status.Recent
}

// connect dials the service anew with the sign-in typed (nil: what is
// saved), ending a dial under way.
// Side effects: a goroutine dials; the tab follows it on the main thread.
func (s *serviceTab) connect(typed *credForm) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.client != nil {
		go s.client.close()
		s.client = nil
	}
	s.try++
	try := s.try
	s.phase, s.note = svcConnecting, ""
	ctx, cancel := context.WithCancel(s.app.ctx)
	s.cancel = cancel
	dial := serviceDials[s.service]
	s.mu.Unlock()
	go func() {
		client, err := dial(ctx, s, typed)
		s.update(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if try != s.try {
				if client != nil {
					go client.close()
				}
				return
			}
			s.settle(client, err, typed)
		})
	}()
}

// settle takes a dial's result. Notes: the caller holds s.mu.
func (s *serviceTab) settle(client serviceClient, err error, typed *credForm) {
	switch {
	case err == nil:
		s.phase, s.client = svcRunning, client
		client.setFront(s.front)
	case errors.Is(err, errNeedSignIn):
		s.phase = svcForm
	case errors.Is(err, errSignInFailed):
		s.phase, s.note = svcForm, L.ServiceWrongSignIn()
	case errors.Is(err, errSignInRequired):
		s.phase, s.note = svcForm, L.ServiceNeedsSignIn()
	case errors.Is(err, errHostKeyChanged):
		s.phase, s.note = svcEnded, L.ServiceHostKeyChanged()
	case errors.Is(err, errNoAnswer):
		s.phase, s.note = svcEnded, L.ServiceNoAnswer()
	case errors.Is(err, context.Canceled):
		s.phase = svcEnded
	default:
		s.phase, s.note = svcEnded, serviceFailure(err)
	}
}

// serviceFailure words why a service could not be reached.
func serviceFailure(err error) string {
	// What a VNC server said or did (internal/rfb).
	var said *rfb.RefusedError
	switch {
	case errors.Is(err, rfb.ErrClosedBeforeGreeting):
		return L.ServiceVNCLoopback()
	case errors.As(err, &said):
		return L.ServiceVNCRefused(said.Reason)
	case errors.Is(err, rfb.ErrUnsupportedServer):
		return L.ServiceVNCUnsupported()
	}
	var refused *localapi.RequestError
	if !errors.As(err, &refused) {
		return L.ServiceFailed()
	}
	// The engine names why (localapi.Client.Tunnel): never shown as such.
	switch name := refused.Message; name {
	case "unsupported":
		return L.ServiceNoTunnel()
	case "service-down":
		return L.ServiceDown()
	case "password", "denied", "timeout", "busy", "rate-limited", "absent":
		return failureText(localapi.SessionInfo{Failure: json.RawMessage(`{"refused":"` + name + `"}`)}, helperGap{})
	case "unreachable", "wrong-device", "key-changed", "incompatible":
		return failureText(localapi.SessionInfo{Failure: json.RawMessage(`"` + name + `"`)}, helperGap{})
	}
	return L.ServiceFailed()
}

// ended shows that the client's connection ended, unless a newer dial
// replaced it. Safe from any goroutine.
func (s *serviceTab) ended(client serviceClient, note string) {
	s.update(func() {
		s.mu.Lock()
		if s.client != client {
			s.mu.Unlock()
			return
		}
		s.phase, s.client, s.note = svcEnded, nil, note
		s.mu.Unlock()
		s.endGrab()
		go client.close()
	})
}

// update runs fn on the main thread of the tab's window and redraws it;
// nothing once the tab closed.
func (s *serviceTab) update(fn func()) {
	if w := s.home.Load(); w != nil {
		w.win.Update(fn)
	}
}

// redraw draws the tab's window anew (a client's picture changed).
func (s *serviceTab) redraw() { s.update(func() {}) }

// The tab interface (viewerwin.go).

func (s *serviceTab) tabKey() string   { return s.key }
func (s *serviceTab) tabTitle() string { return s.title }

// head is the service's pane header: its kind's icon, the device's name,
// and the service.
func (s *serviceTab) head() paneHead {
	title, _ := splitTitle(s.name)
	return paneHead{icon: serviceIcon(s.service), title: title, sub: s.kind, kind: s.kind}
}
func (s *serviceTab) tabWindow() *viewerWindow     { return s.home.Load() }
func (s *serviceTab) setTabWindow(w *viewerWindow) { s.home.Store(w) }
func (s *serviceTab) running() serviceClient       { s.mu.Lock(); defer s.mu.Unlock(); return s.client }
func (s *serviceTab) formShown() credForm {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.form
}

// ask sets the form the tab shows if the dial needs a sign-in (see
// askedFor), and returns the sign-in to try: false asks at once.
// Side effects: s.form, without any password.
func (s *serviceTab) ask(typed *credForm, saved *localapi.Credentials) (credForm, bool) {
	plan, ok := signInPlan(s.service, typed, saved)
	s.mu.Lock()
	s.form = askedFor(s.service, plan, saved)
	s.mu.Unlock()
	return plan, ok
}

// keep saves what a sign-in that worked leaves (see kept), with extra
// fields a client adds (SSH's pinned key).
func (s *serviceTab) keep(ctx context.Context, plan credForm, typed bool) {
	if cr := kept(plan, typed); cr != nil {
		_ = s.app.link.SaveCredential(ctx, s.device, s.service, *cr)
	}
}

// live reports whether the tab is connecting or connected.
func (s *serviceTab) live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase == svcConnecting || s.phase == svcRunning
}

// disconnect ends the service's connection as its tab closes.
func (s *serviceTab) disconnect() {
	s.endGrab()
	s.home.Store(nil)
	s.app.mu.Lock()
	if s.app.services[s.tabKey()] == s {
		delete(s.app.services, s.tabKey())
	}
	s.app.mu.Unlock()
	s.mu.Lock()
	s.try++
	cancel, client := s.cancel, s.client
	s.client, s.phase = nil, svcEnded
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		go client.close()
	}
}

// setFront tells the client whether its tab is in front.
func (s *serviceTab) setFront(on bool) {
	s.mu.Lock()
	s.front = on
	client := s.client
	s.mu.Unlock()
	if client != nil {
		client.setFront(on)
	}
}

// syncGrab has the core grab the keyboard for the tab's window while the
// pointer is anywhere in its pane (inPane) in Map or Translate mode, for a
// client that takes keys that way, and ends the grab otherwise.
// Side effects: starts the app's key reader with the first grab.
func (s *serviceTab) syncGrab(inPane bool) {
	client := s.running()
	want := ""
	if inPane && client != nil && client.grabs() {
		if mode := s.app.keyboardMode(); mode != "legacy" {
			want = mode
		}
	}
	s.mu.Lock()
	have, refused := s.grabbed, s.noGrab
	s.mu.Unlock()
	if want == have || refused && want != "" {
		return
	}
	lib, err := coreLib()
	if err != nil {
		return
	}
	w := s.home.Load()
	if w == nil {
		return
	}
	window := w.win.NativeHandle()
	mode := want
	if mode == "" {
		mode = "off"
	}
	err = s.app.grabKeyboard(lib, window, s, mode)
	s.mu.Lock()
	s.win = window
	if s.noGrab = err != nil; s.noGrab {
		want = ""
	}
	s.grabbed = want
	s.mu.Unlock()
	if want != "" {
		s.app.keyReader.Do(func() { go s.app.readKeys(lib) })
	}
}

// endGrab ends the tab's keyboard grab, which releases the keys it held.
func (s *serviceTab) endGrab() {
	s.mu.Lock()
	window, grabbed := s.win, s.grabbed
	s.grabbed = ""
	s.mu.Unlock()
	if lib, err := coreLib(); err == nil && grabbed != "" {
		_ = s.app.grabKeyboard(lib, window, s, "off")
	}
}

// takeKey passes a key the grab took to the service tab holding the grab
// for its window. Returns: whether one took it.
// Notes: readKeys calls it for keys no desktop's viewer took.
func (a *App) takeKey(ev core.KeyEvent) bool {
	a.mu.Lock()
	tabs := make([]*serviceTab, 0, len(a.services))
	for _, s := range a.services {
		tabs = append(tabs, s)
	}
	a.mu.Unlock()
	for _, s := range tabs {
		s.mu.Lock()
		mine, client := s.win == ev.Window && s.grabbed != "", s.client
		s.mu.Unlock()
		if mine && client != nil {
			client.scan(ev)
			return true
		}
	}
	return false
}

// view is the tab: the client, or connecting, the sign-in form, or why it
// ended with Reconnect.
func (s *serviceTab) view(c *ui.Context) {
	s.mu.Lock()
	phase, note, client := s.phase, s.note, s.client
	s.mu.Unlock()
	if phase == svcRunning && client != nil {
		client.view(c, s)
		return
	}
	s.syncGrab(false)
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Background).AlignItems(ui.Center).Justify(ui.Center).Padding(0, paneMargin*2).Children(func() {
		homeCard(c, "connect-panel-"+s.tabKey()).FillWidth().MaxWidth(connectPanelW).Children(func() {
			ui.Text(c, s.title).FontSize(sizeHeading).Bold()
			switch phase {
			case svcForm:
				s.signInForm(c, note)
			case svcEnded:
				s.endedPanel(c, note)
			default:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					text := L.ServiceConnecting(serviceName(s.service))
					busyMark(c, text, busySize)
					ui.Text(c, text).TextColor(t.TextMuted)
				})
			}
		})
	})
}

// signInForm asks how to sign in to the service — the methods it offers
// (signInMethods) — and what that method needs: a user name (a domain for
// Remote Desktop), a password, and whether to remember the choice.
func (s *serviceTab) signInForm(c *ui.Context, note string) {
	t := c.Theme()
	f := s.formShown()
	edit := func(e ui.Element) {
		if e.Changed() {
			s.mu.Lock()
			s.form = f
			s.mu.Unlock()
		}
	}
	if note != "" {
		ui.Text(c, note).FontSize(sizeSmall).TextColor(textTone(t, t.Danger))
	}
	ui.Text(c, L.ServiceSignInHint(serviceName(s.service))).FontSize(sizeSmall).TextColor(t.TextMuted)
	methods := signInMethods[s.service]
	labels := make([]string, len(methods))
	for i, m := range methods {
		labels[i] = methodLabel(s.service, m)
	}
	idx := max(slices.Index(methods, f.method), 0)
	if ui.Segmented(c.Key("svc-method"), &idx, labels...).Changed() {
		f.method = methods[idx]
		s.mu.Lock()
		s.form = f
		s.mu.Unlock()
	}
	f.method = methods[idx]
	submit := false
	if s.service == serviceSSH || f.method == methodAccount {
		label := L.ServiceUser()
		if s.service == serviceVNC {
			label = L.ServiceUserMac()
		}
		edit(ui.TextInput(c.Key("svc-user"), &f.user).Placeholder(label).Label(label).FillWidth())
	}
	if s.service == serviceRDP && f.method == methodAccount {
		edit(ui.TextInput(c.Key("svc-domain"), &f.domain).Placeholder(L.ServiceDomain()).Label(L.ServiceDomain()).FillWidth())
	}
	switch f.method {
	case methodKey:
		ui.Text(c, L.ServiceKeyHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	case methodNone:
		hint := L.ServiceNoneHint()
		if s.service == serviceRDP {
			hint = L.ServiceScreenHint()
		}
		ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted)
	default:
		pw := ui.TextInput(c.Key("svc-password"), &f.password).Password().Placeholder(L.ServicePassword()).Label(L.ServicePassword()).FillWidth()
		edit(pw)
		if pw.Submitted() {
			submit = true
		}
	}
	labelled(c, func() {
		edit(ui.Switch(c.Key("svc-remember"), &f.remember).Label(L.ServiceRememberMethod()))
	}, L.ServiceRememberMethod())
	ui.Row(c).Gap(gap).Children(func() {
		ui.Row(c).Grow(1)
		if ui.Button(c.Key("svc-cancel"), L.ActionCancel()).Clicked() {
			if w := s.tabWindow(); w != nil {
				w.closeTab(s)
			}
		}
		if ui.PrimaryButton(c.Key("svc-go"), L.ServiceSignIn()).Disabled(!f.ready(s.service)).Clicked() {
			submit = true
		}
	})
	if submit && f.ready(s.service) {
		typed := f
		s.mu.Lock()
		s.form.password = ""
		s.mu.Unlock()
		s.connect(&typed)
	}
}

// ready reports whether the form holds what its method needs: a user name
// for SSH and for an account, a password where one is asked.
func (f credForm) ready(service string) bool {
	if (service == serviceSSH || f.method == methodAccount) && strings.TrimSpace(f.user) == "" {
		return false
	}
	return f.method == methodKey || f.method == methodNone || f.password != ""
}

// methodLabel words a sign-in method for a service's form.
func methodLabel(service, method string) string {
	switch method {
	case methodKey:
		return L.ServiceMethodKey()
	case methodAccount:
		return L.ServiceMethodAccount()
	case methodNone:
		if service == serviceRDP {
			return L.ServiceMethodScreen()
		}
		return L.ServiceMethodNone()
	}
	if service == serviceVNC {
		return L.ServiceMethodPasswordOnly()
	}
	return L.ServiceMethodPassword()
}

// endedPanel says why the connection ended, with Reconnect, and Trust the
// new key after an SSH server's key changed.
func (s *serviceTab) endedPanel(c *ui.Context, note string) {
	t := c.Theme()
	if note == "" {
		note = L.ServiceEnded()
	}
	ui.Text(c, note).TextColor(textTone(t, t.Danger))
	s.mu.Lock()
	changed := s.changed
	s.mu.Unlock()
	ui.Row(c).Gap(gap).Children(func() {
		ui.Row(c).Grow(1)
		if changed != "" && ui.Button(c.Key("svc-trust"), L.ServiceTrustKey()).Clicked() {
			s.mu.Lock()
			s.hostKey, s.changed = changed, ""
			s.mu.Unlock()
			s.connect(nil)
		}
		if ui.PrimaryButton(c.Key("svc-again"), L.ViewerReconnect()).Clicked() {
			s.connect(nil)
		}
	})
}

// savedCredential is the sign-in saved for the tab's service, nil when
// none is or the engine keeps none; asked right before a client signs in.
func (s *serviceTab) savedCredential(ctx context.Context) *localapi.Credentials {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cr, err := s.app.link.Credential(ctx, s.device, s.service)
	if err != nil {
		return nil
	}
	return cr
}
