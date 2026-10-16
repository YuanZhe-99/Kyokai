// Package gui is Kyokai's window, tray and settings (doc/en-us/gui.md). It
// holds no identity of its own: the engine (`kyokai-svc`, started as a
// child) owns it, and the GUI shows what the engine's session pushes.
package gui

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

type page int

const (
	pageHome page = iota
	pageSettings
)

// App is the GUI's state. Fields after mu are guarded by it; views read
// them under the lock and never keep elements.
type App struct {
	profile  string
	stateDir string
	socket   string
	ctx      context.Context
	cancel   context.CancelFunc
	engine   engineChild

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
	return &App{
		profile: profile, stateDir: stateDir, socket: socket, ctx: ctx, cancel: cancel,
		prefs: loadPrefs(stateDir), busy: map[string]bool{},
	}
}

// SetWindow hands the GUI its main window.
func (a *App) SetWindow(w *mygo.Window) {
	a.mu.Lock()
	a.window = w
	a.mu.Unlock()
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
	a.engine.stop(a.socket)
}

// Focus shows and raises the window.
func (a *App) Focus() {
	a.mu.Lock()
	w := a.window
	a.mu.Unlock()
	if w == nil {
		return
	}
	w.Show()
	w.Focus()
	a.SetVisible(true)
}

// Quit ends the app; the engine stops with it.
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	mygo.App.Quit()
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

// View is the whole window.
func (a *App) View(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	pg := a.page
	a.mu.Unlock()
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Fill().Grow(1).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c)
			ui.Scroll(c).Grow(1).Fill().Padding(24).Gap(gap).Children(func() {
				switch pg {
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
