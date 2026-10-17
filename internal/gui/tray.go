package gui

import (
	_ "embed"
	"strconv"

	"github.com/egoist/mygo"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

//go:embed assets/tray.png
var trayGlyph []byte

// trayGlyphBlack is the glyph for light taskbars on Windows, which shows
// tray icons as they are (macOS tints the template itself).
//
//go:embed assets/tray-black.png
var trayGlyphBlack []byte

// trayIcon is the glyph that shows on the current taskbar.
func trayIcon(light bool) []byte {
	if light {
		return trayGlyphBlack
	}
	return trayGlyph
}

// Tray owns the status icon: the Device ID, Stop AI task while one runs, Show
// and Quit. It never shows a password.
type Tray struct {
	app  *App
	tray *mygo.Tray
	last string
	// light is whether the black glyph is shown (a light taskbar).
	light bool
}

// NewTray creates the status icon (main thread). It returns nil when the
// desktop has no tray; closing the window then quits.
func (a *App) NewTray() *Tray {
	light := lightTaskbar()
	t, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(light), IconIsTemplate: true, ToolTip: "Kyokai"})
	if err != nil {
		return nil
	}
	tr := &Tray{app: a, tray: t, last: "-", light: light}
	tr.Refresh()
	return tr
}

// Refresh swaps the glyph when the taskbar turned light or dark, and
// rebuilds the menu when the Device ID or the language changed (main
// thread).
func (t *Tray) Refresh() {
	if t == nil {
		return
	}
	if light := lightTaskbar(); light != t.light {
		t.light = light
		_ = t.tray.SetIcon(trayIcon(light), true)
	}
	a := t.app
	a.mu.Lock()
	id, aiActive := "", false
	if a.status != nil && a.connected {
		id, aiActive = a.status.DeviceID, a.status.AI != nil
	}
	a.mu.Unlock()
	status := L.EngineStarting()
	if id != "" {
		status = L.TrayDeviceID(groupDigits(id))
	}
	key := status + L.TrayShow() + strconv.FormatBool(aiActive)
	if key == t.last {
		return
	}
	t.last = key
	t.tray.SetToolTip("Kyokai — " + status)
	items := []*mygo.MenuItem{{Label: status, Disabled: true}, mygo.Separator()}
	if aiActive {
		// The emergency stop, one click from anywhere.
		items = append(items, &mygo.MenuItem{Label: L.TrayStopAI(), Click: func(*mygo.MenuItem, *mygo.Window) { a.stopAI() }})
	}
	items = append(items,
		&mygo.MenuItem{Label: L.TrayShow(), Click: func(*mygo.MenuItem, *mygo.Window) { a.Focus() }},
		&mygo.MenuItem{Label: L.TrayQuit(), Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() }},
	)
	t.tray.SetMenu(mygo.NewMenu(items))
}
