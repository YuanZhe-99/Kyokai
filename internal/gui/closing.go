package gui

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// closeFacts are what decides how closing a window goes (decideClose).
type closeFacts struct {
	// quitting: the app quits (the tray's Quit, its menu, the Dock, the
	// system, an update's relaunch).
	quitting bool
	// main: the window is the main one; quits: closing it quits the app
	// (Quit chosen for the close button, or there is no tray).
	main, quits bool
	// liveHere are the live connections in the window, liveAll those in
	// every window (tab.live).
	liveHere, liveAll int
	// homeShown: the window shows Home, so none of its connections is in
	// view (only the main window has Home).
	homeShown bool
}

// closeAction is what closing a window does.
type closeAction int

const (
	// closeNow lets the window close as the app quits: nothing is asked.
	closeNow closeAction = iota
	// closeQuit quits the app.
	closeQuit
	// closeHide disconnects the main window's tabs and hides it.
	closeHide
	// closeWindow closes a window of its own, which disconnects its tabs.
	closeWindow
)

// closeDecision is decideClose's answer: what to do, and how many
// connections to ask about first (0: ask nothing).
type closeDecision struct {
	action closeAction
	ask    int
}

// decideClose decides how closing a window goes (doc/en-us/gui.md, "Tray"):
// quitting never asks; the main window with Quit chosen asks when any
// connection is live anywhere, then quits; otherwise the window asks when it
// holds two live connections or more, or one it does not show (the main
// window showing Home), then disconnects its tabs and hides (the main
// window) or closes.
func decideClose(f closeFacts) closeDecision {
	switch {
	case f.quitting:
		return closeDecision{action: closeNow}
	case f.main && f.quits:
		return closeDecision{action: closeQuit, ask: f.liveAll}
	}
	d := closeDecision{action: closeWindow}
	if f.main {
		d.action = closeHide
	}
	if f.liveHere >= 2 || f.liveHere == 1 && f.homeShown {
		d.ask = f.liveHere
	}
	return d
}

// closeFactsFor gathers decideClose's facts for a window.
func (a *App) closeFactsFor(vw *viewerWindow) closeFacts {
	a.mu.Lock()
	f := closeFacts{quitting: a.quitting, main: vw.main, quits: a.prefs.CloseQuits || a.tray == nil}
	a.mu.Unlock()
	for _, t := range a.allTabs() {
		if t.live() {
			f.liveAll++
		}
	}
	vw.mu.Lock()
	tabs := vw.tabs
	f.homeShown = vw.main && (vw.home || len(vw.tabs) == 0)
	vw.mu.Unlock()
	for _, t := range tabs {
		if t.live() {
			f.liveHere++
		}
	}
	return f
}

// HandleClose decides what closing the main window does (decideClose):
// let it close as the app quits, quit, or disconnect its tabs and hide it
// into the tray, after asking when that ends connections not in view.
func (a *App) HandleClose(e *mygo.CloseEvent) { a.handleClose(a.main, e) }

// closeViewerWindow decides what closing a window of its own does: it
// closes (disconnecting its tabs), after asking when it holds several live
// connections.
func (a *App) closeViewerWindow(vw *viewerWindow, e *mygo.CloseEvent) { a.handleClose(vw, e) }

// handleClose applies decideClose to a window being closed.
// Side effects: may prevent the close and ask in a dialog from a goroutine
// (never on the main thread or under a.mu), then close again.
func (a *App) handleClose(vw *viewerWindow, e *mygo.CloseEvent) {
	d := decideClose(a.closeFactsFor(vw))
	vw.mu.Lock()
	agreed := vw.agreed
	vw.agreed = false
	vw.mu.Unlock()
	if d.action == closeNow || d.action == closeWindow && (d.ask == 0 || agreed) {
		return
	}
	e.PreventDefault()
	if d.ask == 0 || agreed {
		a.applyClose(vw, d.action)
		return
	}
	names := a.liveNames(vw, d.action == closeQuit)
	go func() {
		if !a.confirmClose(vw, d.action == closeQuit, names) {
			return
		}
		vw.win.Update(func() {
			vw.mu.Lock()
			vw.agreed = true
			vw.mu.Unlock()
			if d.action == closeWindow {
				vw.win.Close()
				return
			}
			a.applyClose(vw, d.action)
		})
	}()
}

// applyClose quits, or disconnects the main window's tabs and hides it.
// Notes: call on the main thread.
func (a *App) applyClose(vw *viewerWindow, action closeAction) {
	if action == closeQuit {
		a.Quit()
		return
	}
	vw.mu.Lock()
	tabs := vw.tabs
	vw.mu.Unlock()
	for _, t := range tabs {
		vw.closeTab(t)
	}
	vw.mu.Lock()
	vw.agreed = false
	vw.mu.Unlock()
	a.hideMain()
}

// hideMain hides the main window into the tray and says so the first time.
func (a *App) hideMain() {
	a.mu.Lock()
	w := a.window
	first := !a.prefs.TrayHintShown
	a.prefs.TrayHintShown = true
	p := a.prefs
	a.mu.Unlock()
	if w != nil {
		w.Hide()
	}
	a.SetVisible(false)
	if first {
		_ = savePrefs(a.stateDir, p)
		a.notify("tray-hint", L.NotifyStillRunningTitle(), L.NotifyStillRunningBody())
	}
}

// liveNames names the live connections closing a window ends: its own, or
// every window's when the app quits.
func (a *App) liveNames(vw *viewerWindow, all bool) []string {
	var tabs []tab
	if all {
		tabs = a.allTabs()
	} else {
		vw.mu.Lock()
		tabs = vw.tabs
		vw.mu.Unlock()
	}
	var names []string
	for _, t := range tabs {
		if t.live() {
			names = append(names, t.tabTitle())
		}
	}
	return names
}

// confirmClose asks whether to end the connections named, before a window
// closes or the app quits.
// Notes: call from a goroutine, never holding a.mu: the dialog waits for
// the main thread.
func (a *App) confirmClose(vw *viewerWindow, quit bool, names []string) bool {
	action := L.CloseDisconnect()
	if quit {
		action = L.CloseDisconnectQuit()
	}
	res, err := mygo.Dialog.Message(mygo.MessageOptions{
		Parent: vw.win, Type: mygo.MessageWarning, Title: action,
		Message: L.CloseAskTitle(), Detail: L.CloseAskDetail(strings.Join(names, "\n")),
		Buttons: []string{action, L.ActionCancel()}, DefaultButton: 1, CancelButton: 1,
	})
	return err == nil && res.Button == 0
}

// closeControllersWait bounds how long quitting waits for the engine to end
// this GUI's connections.
const closeControllersWait = time.Second

// BeforeQuit runs as the app quits: it no longer hides windows into the
// tray, and asks the engine to end this GUI's connections (in the helper
// modes the helper outlives the GUI), waiting closeControllersWait at most.
func (a *App) BeforeQuit() {
	a.MarkQuitting()
	ctx, cancel := context.WithTimeout(context.Background(), closeControllersWait)
	defer cancel()
	_, _ = localapi.NewClient(a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestCloseControllers})
}
