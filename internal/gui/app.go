// Package gui is Kyokai's window, tray and settings (doc/en-us/gui.md). It
// holds no identity of its own: the engine (`kyokai-svc`, started as a
// child) owns it, and the GUI shows what the engine's session pushes.
package gui

import (
	"context"
	"errors"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

type page int

// The sidebar's pages: Home, then the areas of settings (doc/en-us/gui.md).
const (
	pageHome page = iota
	pageNetwork
	pageAI
	pagePermissions
	pageSecurity
	pageGeneral
	pageAbout
)

// App is the GUI's state. Fields after mu are guarded by it; views read
// them under the lock and never keep elements.
type App struct {
	profile  string
	stateDir string
	// socket is the engine the GUI uses now: userSocket (its child) or the
	// helper's; reattach makes the connection loop switch to it.
	socket     string
	userSocket string
	reattach   func()
	ctx        context.Context
	cancel     context.CancelFunc
	engine     engineChild
	// captureMu keeps the capture setting's sends in order (sendCapture).
	captureMu sync.Mutex

	mu       sync.Mutex
	window   *mygo.Window
	tray     *Tray
	prefs    Prefs
	visible  bool
	quitting bool

	// Engine connection: the last pushed status, or why there is none.
	status    *localapi.Status
	connected bool
	engineErr string

	// Home: the temporary password, fetched only when shown or copied.
	password     string
	passwordFor  uint64 // the serial the fetched password belongs to
	showPassword bool
	// permissionsAsked: this run already looked at the privacy
	// permissions (permissions.go); asked: the privacy permissions this run
	// asked for, which macOS counts only in an engine started afterwards.
	permissionsAsked bool
	asked            map[string]bool
	// helperUpdated: this run already updated an older helper (modes.go).
	helperUpdated bool
	// cleaning: the one-time cleanup of the other run mode's store runs
	// (cleanup.go).
	cleaning bool
	// soundDevices are the engine's input devices for Sound to share,
	// asked once Permissions shows.
	soundDevices       []localapi.SoundDevice
	soundDevicesLoaded bool

	// Network page: the networks kept and the form open (network.go).
	net netState
	// Sessions (sessions.go): the connect form, the permissions chosen per
	// open host confirmation, the access card.
	connect connectForm
	// statusOf is the host whose status Home shows in full, or "".
	statusOf string
	// routesEdit is the Addresses editor open on Home, and probedAt when
	// Home last asked to look at the shown devices' addresses.
	routesEdit routesForm
	probedAt   time.Time
	// rename is the name being given to a recent device on Home.
	rename renameForm
	// unlock is the permanent password typed to leave protected mode.
	unlock string
	// name is the friendly-name field in Security while it is edited.
	name   nameForm
	grants map[uint64][]string
	access accessForm
	// Identity backup passphrase (identity.go), write-only.
	passphrase string
	// Open viewer windows by session id (viewer.go).
	viewers map[string]*viewer
	// The services' tabs by serviceKey (servicetab.go), and how they reach
	// the services and their sign-ins (remote.go).
	services map[string]*serviceTab
	link     serviceLink
	// Home's status watch (home.go).
	watch watchState
	// The Security page's keys and saved sign-ins (keys.go).
	keys keysState
	// The viewer windows, oldest first; a new viewer opens as a tab of the
	// newest (viewerwin.go).
	viewerWins []*viewerWindow
	// main is the main window as a holder of tabs, Home first.
	main *viewerWindow
	// lastWin is the Kyokai window used last, whose shown tab is the one
	// in front, which alone exchanges the clipboard (viewerclip.go).
	lastWin *viewerWindow
	// keyReader starts the reader of the keys the viewers' keyboard grab
	// takes (viewerinput.go), once.
	keyReader sync.Once
	// The AI page (ai.go).
	ai aiState

	// View state.
	page         page
	openAtLogin  bool
	showLicenses bool
	busy         map[string]bool
	toast        string
	toastErr     bool
	toastAt      time.Time

	// App updates (update.go).
	appUpdate      *mygo.Update
	updating       bool
	updateProgress float64
}

// New creates the GUI for a profile ("" outside development).
func New(profile, stateDir, socket string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{
		profile: profile, stateDir: stateDir, socket: socket, userSocket: socket, ctx: ctx, cancel: cancel,
		prefs: loadPrefs(stateDir), busy: map[string]bool{}, grants: map[uint64][]string{}, viewers: map[string]*viewer{},
		services: map[string]*serviceTab{},
	}
	a.link = newServiceLink(a)
	a.main = &viewerWindow{app: a, main: true, home: true}
	a.lastWin = a.main
	a.socket = startSocket(a.prefs.Helper || movedToHelper(stateDir), helper.Installed(), socket)
	return a
}

// startSocket chooses the engine the GUI attaches to as it starts: the
// helper when this profile switched to Follow GUI or Background service
// and the helper is installed, else its own engine on `own`. A user
// engine whose store moved to the helper counts as that choice too, so a
// lost gui.json never starts an identity beside the helper's.
// Notes: a development profile follows its own choice too; ignoring it
// started an empty engine beside the helper, which then held the identity.
func startSocket(helperMode, installed bool, own string) string {
	if helperMode && installed {
		return helper.SocketPath
	}
	return own
}

// mainWindow returns the main window, made first when there is none: it
// exists only while shown (doc/en-us/gui.md, "Tray"). A hidden window
// kept for later came up at Windows sign-in as a white frame that Kyokai
// took for hidden: Windows' rearranging of windows when the displays
// changed made it visible, unpainted, behind MyGo's back.
// Side effects: a new hidden window at the place it had, kept out of
// capture unless the person chose otherwise; the caller shows it.
// Notes: call on the main thread.
func (a *App) mainWindow() *mygo.Window {
	a.mu.Lock()
	w := a.window
	show := a.prefs.ShowInCapture
	a.mu.Unlock()
	if w != nil {
		return w
	}
	// The tab strip is the title bar (TitleBarOptions).
	w = mygo.NewWindow(TitleBarOptions(mygo.WindowOptions{
		Title: "Kyokai", Width: 1000, Height: 700, MinWidth: 760, MinHeight: 520,
		StateKey: "main",
		// Shown once its display affinity is set: never captured for a
		// moment.
		Hidden:  true,
		Content: ui.View(a.MainView),
	}))
	w.SetContentProtection(!show)
	a.mu.Lock()
	a.window = w
	a.main.win = w
	a.mu.Unlock()
	a.watchFocus(a.main)
	a.main.watchFullScreen()
	w.OnClose(a.HandleClose)
	w.OnHide(func() { a.SetVisible(false) })
	w.OnShow(func() { a.SetVisible(true) })
	return w
}

// later runs fn on the main thread after the caller returns, and redraws
// the main window when there is one.
func (a *App) later(fn func()) {
	a.mu.Lock()
	w := a.window
	a.mu.Unlock()
	if w != nil {
		w.Update(fn)
		return
	}
	go mygo.RunOnMain(fn)
}

// setShowInCapture saves whether capture may see Kyokai's windows and
// applies it to the main window and every open viewer at once.
// Side effects: writes the preferences; changes the windows' display
// affinity; tells the engine (sendCapture). Call it on the main thread.
func (a *App) setShowInCapture(p Prefs, show bool) {
	p.ShowInCapture = show
	a.setPrefs(p)
	a.mu.Lock()
	wins := []*mygo.Window{a.window}
	for _, vw := range a.viewerWins {
		wins = append(wins, vw.win)
	}
	a.mu.Unlock()
	for _, w := range wins {
		if w != nil {
			w.SetContentProtection(!show)
		}
	}
	go a.sendCapture()
}

// sendCapture tells the engine whether its captures show Kyokai's windows and
// whether a person's remote input may reach them: both follow Hide Kyokai
// from capture. macOS's capture filter follows the first (on Windows the
// windows' display affinity does the job). An engine of an older version
// refuses what it does not know, which changes nothing: it keeps Kyokai's
// windows guarded.
// Returns: whether both requests reached the engine (see reached).
func (a *App) sendCapture() bool {
	a.captureMu.Lock()
	defer a.captureMu.Unlock()
	a.mu.Lock()
	show := a.prefs.ShowInCapture
	a.mu.Unlock()
	_, err := a.requestWith(localapi.Request{Type: localapi.RequestCaptureShow, Show: &show})
	_, err2 := a.requestWith(localapi.Request{Type: localapi.RequestInputKyokai, KyokaiInput: &show})
	return reached(err) && reached(err2)
}

// sendLogs tells the engine whether to keep logs and connection history,
// as the preferences say. An engine of an older version refuses the
// request, which changes nothing.
// Returns: whether the request reached the engine (see reached).
func (a *App) sendLogs() bool {
	a.mu.Lock()
	off := a.prefs.NoLogs
	a.mu.Unlock()
	_, err := a.requestWith(localapi.Request{Type: localapi.RequestLogs, LogsOff: &off})
	return reached(err)
}

// sendSettings tells the engine what this GUI's preferences choose for it:
// what its captures show, whether a person's remote input may reach
// Kyokai's windows, the sound to share and whether to keep logs.
// Returns: whether every request reached the engine.
func (a *App) sendSettings() bool {
	capture, sound, logs := a.sendCapture(), a.sendSoundSource(), a.sendLogs()
	return capture && sound && logs
}

// reached reports whether a request's answer came from the engine: it
// was served, or refused (an older engine keeps its own safe default), not
// lost on the way (no engine yet, or one too busy starting to answer).
func reached(err error) bool {
	var refused *localapi.RequestError
	return err == nil || errors.As(err, &refused)
}

// setNoLogs keeps no logs, crash traces or connection history from now
// on (deleting the logs written), or keeps them again: in this GUI's
// state folder and in its engine's.
// Side effects: writes gui.json and the marker; may delete the logs.
func (a *App) setNoLogs(p Prefs, off bool) {
	p.NoLogs = off
	a.setPrefs(p)
	_ = engine.SetLogsOff(a.stateDir, off)
	go a.sendLogs()
}

// sendSoundSource tells the engine which sound this device shares, as the
// preferences say. An engine of an older version refuses the request,
// which changes nothing.
// Returns: whether the request reached the engine (see reached).
func (a *App) sendSoundSource() bool {
	a.mu.Lock()
	source := a.prefs.SoundSource
	a.mu.Unlock()
	_, err := a.requestWith(localapi.Request{Type: localapi.RequestSoundSource, SoundSource: &source})
	return reached(err)
}

// setSoundSource keeps the sound to share and tells the engine; an input
// device on macOS asks for the Microphone permission (the engine's, or the
// helper's in its modes).
func (a *App) setSoundSource(p Prefs, source string) {
	p.SoundSource = source
	a.setPrefs(p)
	go a.sendSoundSource()
	if source != "" && runtime.GOOS == "darwin" {
		a.askPermission("microphone", false)
	}
}

// loadSoundDevices asks the engine for its input devices, once.
func (a *App) loadSoundDevices() {
	a.mu.Lock()
	if a.soundDevicesLoaded {
		a.mu.Unlock()
		return
	}
	a.soundDevicesLoaded = true
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestSoundDevices})
		if err != nil || resp == nil {
			return
		}
		a.update(func() { a.soundDevices = resp.SoundDevices })
	}()
}

// SetTray hands the GUI its status icon (nil when the desktop has none).
func (a *App) SetTray(t *Tray) {
	a.mu.Lock()
	a.tray = t
	a.mu.Unlock()
}

// SetVisible records whether the window is shown.
func (a *App) SetVisible(v bool) {
	a.mu.Lock()
	a.visible = v
	a.mu.Unlock()
	a.syncDock()
	a.homeVisibilityChanged()
}

// syncDock shows the Dock icon while a Kyokai window is open and hides it
// while only the tray is left (macOS; elsewhere it does nothing).
func (a *App) syncDock() {
	a.mu.Lock()
	open := a.visible || len(a.viewerWins) > 0
	a.mu.Unlock()
	policy := mygo.ActivationPolicyAccessory
	if open {
		policy = mygo.ActivationPolicyRegular
	}
	mygo.App.SetActivationPolicy(policy)
}

// Start connects to the engine (starting it when needed), begins the daily
// update check and watches the main thread (watchMain).
func (a *App) Start() {
	// Asked without the lock: MyGo answers it on the main thread.
	openAtLogin := mygo.App.OpenAtLogin()
	a.mu.Lock()
	a.openAtLogin = openAtLogin
	a.mu.Unlock()
	go a.connectLoop()
	go a.appUpdateLoop()
	go a.watchMain()
	go a.watchLoop()
}

// How often the GUI makes sure its main thread still answers, and how long
// it waits before it writes down where every goroutine is (watchMain).
const (
	watchEvery = 5 * time.Second
	hangAfter  = 10 * time.Second
)

// watchMain notes a main thread that stopped answering: every watchEvery it
// asks the main thread to run nothing, and when that has not happened
// within hangAfter it writes every goroutine's stack to the GUI's crash log
// (through the standard logger: nowhere while No logs is on), once per
// hang.
// Notes: a frozen app is what a person calls a crash, and it leaves no
// report; the stacks say what waits for what.
func (a *App) watchMain() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(watchEvery):
		}
		ran := make(chan struct{})
		go mygo.RunOnMain(func() { close(ran) })
		select {
		case <-ran:
			continue
		case <-a.ctx.Done():
			return
		case <-time.After(hangAfter):
		}
		buf := make([]byte, 1<<20)
		log.Printf("the main thread has not answered for %v:\n%s", hangAfter, buf[:runtime.Stack(buf, true)])
		// Once per hang: the next look waits until it answers again.
		select {
		case <-ran:
		case <-a.ctx.Done():
			return
		}
	}
}

// Stop ends the session and the engine.
func (a *App) Stop() {
	a.cancel()
	// Only the GUI's own engine stops; the helper keeps running.
	a.engine.stop(a.userSocket)
}

// Focus shows and raises the main window, made first when there is none.
// Safe from any goroutine.
func (a *App) Focus() {
	mygo.RunOnMain(func() {
		w := a.mainWindow()
		// The Dock icon first: an accessory app's window does not come
		// forward.
		a.SetVisible(true)
		w.Show()
		w.Focus()
	})
}

// Quit ends the app; the engine stops with it.
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	mygo.App.Quit()
}

// relaunch quits the app and starts it again.
// Side effects: ends the engine connection first, so that nothing starts
// the engine again, then stops the engine, so that the new app's engine
// takes its socket; only then does MyGo quit and start the app anew.
func (a *App) relaunch() {
	a.MarkQuitting()
	a.cancel()
	a.engine.stop(a.userSocket)
	mygo.App.Relaunch()
}

// MarkQuitting records that the app is quitting, so that closing its window
// no longer hides it into the tray.
func (a *App) MarkQuitting() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
}

// update changes state under the lock and redraws the window and the tray.
func (a *App) update(fn func()) {
	a.mu.Lock()
	fn()
	t := a.tray
	a.mu.Unlock()
	a.later(func() {
		if t != nil {
			t.Refresh()
		}
	})
}

// notify shows a desktop notification where the system has them.
// Notes: it waits for the main thread (on macOS the first notification of a
// run waits for the user's answer whether Kyokai may show them): never call
// it holding a.mu, which the main thread takes to draw.
func (a *App) notify(id, title, body string) {
	notifier(id, title, body)
}

// notifier shows a desktop notification; tests replace it.
var notifier = func(id, title, body string) {
	if notificationsSupported() {
		_ = mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body}).Show()
	}
}

// notificationsSupported asks the system once whether it shows
// notifications (a call on the main thread).
var notificationsSupported = sync.OnceValue(mygo.NotificationsSupported)

// clipboardWrite puts text on the clipboard; tests replace it.
// Notes: it waits for the main thread, as notify does.
var clipboardWrite = func(text string) { mygo.Clipboard.WriteText(text) }

// setToast shows a short message at the bottom for four seconds (caller holds
// a.mu or runs on the main thread).
func (a *App) setToast(msg string, isErr bool) {
	a.toast, a.toastErr, a.toastAt = msg, isErr, time.Now()
	time.AfterFunc(4*time.Second+100*time.Millisecond, func() { a.update(func() {}) })
}

// MainView is the main window: Home and the connections' tabs.
func (a *App) MainView(c *ui.Context) { a.main.view(c) }

// View is Home: the sidebar and the page chosen.
func (a *App) View(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	pg, protected := a.page, a.protected()
	a.mu.Unlock()
	if protected {
		pg = pageAI
	}
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Fill().Grow(1).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c)
			ui.Scroll(c).Grow(1).Fill().Padding(24).Gap(gap).Children(func() {
				a.requestsView(c)
				a.hostBanner(c)
				a.aiBanner(c)
				switch pg {
				case pageNetwork:
					a.networkView(c)
				case pageAI:
					if protected {
						a.protectedCard(c)
					}
					a.aiView(c)
				case pagePermissions:
					a.permissionsView(c)
				case pageSecurity:
					a.securityView(c)
				case pageGeneral:
					a.generalView(c)
				case pageAbout:
					a.aboutView(c)
				default:
					a.homeView(c)
				}
			})
		})
		a.toastView(c)
	})
}
