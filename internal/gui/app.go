// Package gui is Kyokai's window, tray and settings (doc/en-us/gui.md). It
// holds no identity of its own: the engine (`kyokai-svc`, started as a
// child) owns it, and the GUI shows what the engine's session pushes.
package gui

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

type page int

const (
	pageHome page = iota
	pageNetwork
	pageAI
	pageSettings
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
	// soundDevices are the engine's input devices for Sound to share,
	// asked once Settings shows.
	soundDevices       []localapi.SoundDevice
	soundDevicesLoaded bool

	// Network page: the settings form (network.go).
	net netForm
	// Sessions (sessions.go): the connect form, the permissions chosen per
	// open host confirmation, the access card.
	connect connectForm
	// rename is the name being given to a recent device on Home.
	rename renameForm
	// unlock is the permanent password typed to leave protected mode.
	unlock string
	// name is the friendly-name field in Settings while it is edited.
	name   nameForm
	grants map[uint64][]string
	access accessForm
	// Identity backup passphrase (identity.go), write-only.
	passphrase string
	// Open viewer windows by session id (viewer.go).
	viewers map[string]*viewer
	// The viewer windows, oldest first; a new viewer opens as a tab of the
	// newest (viewerwin.go).
	viewerWins []*viewerWindow
	// main is the main window as a holder of tabs, Home first.
	main *viewerWindow
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
	}
	a.main = &viewerWindow{app: a, main: true, home: true}
	a.socket = startSocket(a.prefs.Helper, helper.Inspect().Installed, socket)
	return a
}

// startSocket chooses the engine the GUI attaches to as it starts: the
// helper when this profile switched to Follow GUI or Background service
// and the helper is installed, else its own engine on `own`.
// Notes: a development profile follows its own choice too; ignoring it
// started an empty engine beside the helper, which then held the identity.
func startSocket(helperMode, installed bool, own string) string {
	if helperMode && installed {
		return helper.SocketPath
	}
	return own
}

// SetWindow hands the GUI its main window and keeps it out of capture
// unless the person chose otherwise.
func (a *App) SetWindow(w *mygo.Window) {
	a.mu.Lock()
	a.window = w
	a.main.win = w
	show := a.prefs.ShowInCapture
	a.mu.Unlock()
	w.SetContentProtection(!show)
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

// sendCapture tells the engine whether its captures show Kyokai's windows,
// as the preferences say: macOS's capture filter follows it (on Windows the
// windows' display affinity does the job). An engine of an older version
// refuses the request, which changes nothing.
func (a *App) sendCapture() {
	a.captureMu.Lock()
	defer a.captureMu.Unlock()
	a.mu.Lock()
	show := a.prefs.ShowInCapture
	a.mu.Unlock()
	_, _ = a.requestWith(localapi.Request{Type: localapi.RequestCaptureShow, Show: &show})
}

// sendLogs tells the engine whether to keep logs and connection history,
// as the preferences say. An engine of an older version refuses the
// request, which changes nothing.
func (a *App) sendLogs() {
	a.mu.Lock()
	off := a.prefs.NoLogs
	a.mu.Unlock()
	_, _ = a.requestWith(localapi.Request{Type: localapi.RequestLogs, LogsOff: &off})
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
func (a *App) sendSoundSource() {
	a.mu.Lock()
	source := a.prefs.SoundSource
	a.mu.Unlock()
	_, _ = a.requestWith(localapi.Request{Type: localapi.RequestSoundSource, SoundSource: &source})
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

// Start connects to the engine (starting it when needed) and begins the
// daily update check.
func (a *App) Start() {
	a.mu.Lock()
	a.openAtLogin = mygo.App.OpenAtLogin()
	a.mu.Unlock()
	go a.connectLoop()
	go a.appUpdateLoop()
}

// Stop ends the session and the engine.
func (a *App) Stop() {
	a.cancel()
	// Only the GUI's own engine stops; the helper keeps running.
	a.engine.stop(a.userSocket)
}

// Focus shows and raises the window.
func (a *App) Focus() {
	a.mu.Lock()
	w := a.window
	a.mu.Unlock()
	if w == nil {
		return
	}
	// The Dock icon first: an accessory app's window does not come forward.
	a.SetVisible(true)
	w.Show()
	w.Focus()
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

// HandleClose hides the window into the tray unless the user chose that
// closing quits (or there is no tray).
func (a *App) HandleClose(e *mygo.CloseEvent) {
	a.mu.Lock()
	quit := a.quitting || a.prefs.CloseQuits || a.tray == nil
	w := a.window
	first := !a.prefs.TrayHintShown
	a.prefs.TrayHintShown = true
	p := a.prefs
	a.mu.Unlock()
	if quit {
		return
	}
	e.PreventDefault()
	w.Hide()
	a.SetVisible(false)
	if first {
		_ = savePrefs(a.stateDir, p)
		a.notify("tray-hint", L.NotifyStillRunningTitle(), L.NotifyStillRunningBody())
	}
}

// update changes state under the lock and redraws the window and the tray.
func (a *App) update(fn func()) {
	a.mu.Lock()
	fn()
	w, t := a.window, a.tray
	a.mu.Unlock()
	if w != nil {
		w.Update(func() { t.Refresh() })
	}
}

func (a *App) notify(id, title, body string) {
	if !mygo.NotificationsSupported() {
		return
	}
	_ = mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body}).Show()
}

// setToast shows a short message at the bottom for four seconds (caller holds
// a.mu or runs on the main thread).
func (a *App) setToast(msg string, isErr bool) {
	a.toast, a.toastErr, a.toastAt = msg, isErr, time.Now()
	time.AfterFunc(4*time.Second+100*time.Millisecond, func() { a.update(func() {}) })
}

// WatchMainFullScreen keeps the main window's full-screen state, which
// hides its tabs. Notes: call once, after SetWindow.
func (a *App) WatchMainFullScreen() { a.main.watchFullScreen() }

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
				case pageSettings:
					a.settingsView(c)
				default:
					a.homeView(c)
				}
			})
		})
		a.toastView(c)
	})
}
