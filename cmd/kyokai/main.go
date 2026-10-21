// kyokai is the desktop GUI. It starts (or connects to) its engine,
// kyokai-svc, and shows the device's identity and settings; see
// doc/en-us/gui.md and doc/en-us/architecture.md.
//
// Closing the window hides it into the tray when the desktop has one;
// quitting from the tray stops the engine too. KYOKAI_PROFILE runs a
// separate development instance with its own name, socket and state.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/gui"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func main() {
	profile := os.Getenv("KYOKAI_PROFILE")
	name := "Kyokai"
	if profile != "" {
		name += "-" + profile
	}
	mygo.App.SetName(name)
	mygo.App.SetVersion(version.Version)
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	stateDir, err := engine.DefaultStateDir(profile)
	if err != nil {
		log.Fatal(err)
	}
	engine.CrashLog(filepath.Join(stateDir, "logs", "gui-crash.log"))
	a := gui.New(profile, stateDir, localapi.UserSocketPath(profile))
	gui.ApplyTheme(a.ThemePref())
	if lang := a.LanguagePref(); lang != "" {
		mygo.App.SetLocale(lang)
	}
	mygo.App.OnSecondInstance(func([]string, string) { a.Focus() })
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			a.Focus()
		}
	})
	mygo.App.WhenReady(func() {
		// Opened at login, Kyokai starts in the tray without a window. Asked
		// once ready: macOS tells a login item so only as the app finishes
		// launching (regression: asked before, it read false and the window
		// came up at every login).
		hidden := mygo.App.WasOpenedAtLogin()
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleWindowMenu},
		}))
		// The tab strip is the title bar (gui.TitleBarOptions).
		w := mygo.NewWindow(gui.TitleBarOptions(mygo.WindowOptions{
			Title:     "Kyokai",
			Width:     1000,
			Height:    700,
			MinWidth:  760,
			MinHeight: 520,
			StateKey:  "main",
			// Shown once its display affinity is set: never captured for a
			// moment.
			Hidden:  true,
			Content: ui.View(a.MainView),
		}))
		// Kyokai's windows stay out of every capture (the viewer's video and
		// the AI's screenshots) unless Security says otherwise.
		a.SetWindow(w)
		if !hidden {
			w.Show()
		}
		a.SetVisible(!hidden)
		tray := a.NewTray()
		a.SetTray(tray)
		w.OnClose(a.HandleClose)
		a.WatchMainFullScreen()
		// Quitting the app (its menu, the Dock, logging out or shutting
		// down) quits even while closing the window only hides it, and ends
		// this GUI's connections.
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.BeforeQuit() })
		w.OnHide(func() { a.SetVisible(false) })
		w.OnShow(func() { a.SetVisible(true) })
		mygo.App.OnLocaleChanged(func(string) { tray.Refresh() })
		a.Start()
	})
	_ = L.NavHome // the catalogs register the app's languages
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	a.Stop()
}
