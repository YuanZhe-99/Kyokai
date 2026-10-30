package gui

import (
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/egoist/mygo"
)

// waylandSession tells whether the GUI runs on a Wayland desktop, where
// Kyokai's windows can be neither left out of the picture nor kept from
// remote input (doc/en-us/computer-use.md, "Linux").
func waylandSession() bool {
	return runtime.GOOS == "linux" &&
		(os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland")
}

// How often the Wayland guard looks, and how often it tells the engine
// again what it last told (an engine started again knows nothing).
const (
	waylandEvery  = 500 * time.Millisecond
	waylandResend = 10 * time.Second
)

// guardWayland, on a Wayland desktop only, minimizes Kyokai's windows when
// this computer starts being controlled (a connected session with
// control-input) or its AI starts a task, and tells the engine whether one
// of them shows anyway (`kyokai.shown`): the engine then holds the picture
// and injects no remote or AI input until none does.
// Notes: windows are minimized, not closed: closing ends their
// connections, and the main window can hold viewer tabs.
func (a *App) guardWayland() {
	if !waylandSession() {
		return
	}
	var controlled, sent bool
	var sentAt time.Time
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(waylandEvery):
		}
		now := a.controlledHere()
		if now && !controlled {
			mygo.RunOnMain(a.minimizeWindows)
		}
		controlled = now
		shown := make(chan bool, 1)
		mygo.RunOnMain(func() { shown <- a.windowShown() })
		var s bool
		select {
		case s = <-shown:
		case <-a.ctx.Done():
			return
		}
		if s == sent && time.Since(sentAt) < waylandResend {
			continue
		}
		if _, err := a.requestWith(localapi.Request{Type: localapi.RequestKyokaiShown, Shown: &s}); reached(err) {
			sent, sentAt = s, time.Now()
		}
	}
}

// controlledHere tells whether someone controls this computer now: a
// connected host session granted control-input, or an AI task running.
func (a *App) controlledHere() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected || a.status == nil {
		return false
	}
	if ai := a.status.AI; ai != nil && ai.State == "running" {
		return true
	}
	for _, s := range a.status.Sessions {
		if s.Role == "host" && s.State == "connected" && slices.Contains(s.Granted, localapi.PermControlInput) {
			return true
		}
	}
	return false
}

// windowShown tells whether one of Kyokai's windows is on the screen
// (shown and not minimized). Call on the main thread.
func (a *App) windowShown() bool {
	a.mu.Lock()
	wins := make([]*mygo.Window, 0, len(a.viewerWins)+1)
	if a.window != nil {
		wins = append(wins, a.window)
	}
	for _, vw := range a.viewerWins {
		wins = append(wins, vw.win)
	}
	a.mu.Unlock()
	return slices.ContainsFunc(wins, func(w *mygo.Window) bool { return w.IsVisible() && !w.IsMinimized() })
}

// minimizeWindows minimizes the main window and every window of its own.
// Call on the main thread.
func (a *App) minimizeWindows() {
	a.mu.Lock()
	wins := make([]*mygo.Window, 0, len(a.viewerWins)+1)
	if a.window != nil {
		wins = append(wins, a.window)
	}
	for _, vw := range a.viewerWins {
		wins = append(wins, vw.win)
	}
	a.mu.Unlock()
	for _, w := range wins {
		w.Minimize()
	}
}
