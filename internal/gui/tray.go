package gui

import (
	_ "embed"

	"github.com/egoist/mygo"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

//go:embed assets/tray.png
var trayGlyph []byte

// Tray owns the status icon: the Device ID, Show and Quit. It never shows a
// password.
type Tray struct {
	app  *App
	tray *mygo.Tray
	last string
}

// NewTray creates the status icon (main thread). It returns nil when the
// desktop has no tray; closing the window then quits.
func (a *App) NewTray() *Tray {
	t, err := mygo.NewTray(mygo.TrayOptions{Icon: trayGlyph, IconIsTemplate: true, ToolTip: "Kyokai"})
	if err != nil {
		return nil
	}
	tr := &Tray{app: a, tray: t, last: "-"}
	tr.Refresh()
	return tr
}

// Refresh rebuilds the menu when the Device ID or the language changed
// (main thread).
func (t *Tray) Refresh() {
	if t == nil {
		return
	}
	a := t.app
	a.mu.Lock()
	id := ""
	if a.status != nil && a.connected {
		id = a.status.DeviceID
	}
	a.mu.Unlock()
	status := L.EngineStarting()
	if id != "" {
		status = L.TrayDeviceID(groupDigits(id))
	}
	key := status + L.TrayShow()
	if key == t.last {
		return
	}
	t.last = key
	t.tray.SetToolTip("Kyokai — " + status)
	t.tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: status, Disabled: true},
		mygo.Separator(),
		{Label: L.TrayShow(), Click: func(*mygo.MenuItem, *mygo.Window) { a.Focus() }},
		{Label: L.TrayQuit(), Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() }},
	}))
}
