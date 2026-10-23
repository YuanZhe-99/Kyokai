package gui

import (
	"maps"
	"path/filepath"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// Prefs are the GUI's own settings, kept in the state directory.
type Prefs struct {
	// Theme is "system", "light" or "dark".
	Theme string `json:"theme,omitempty"`
	// Language is the chosen interface language ("" follows the system).
	Language string `json:"language,omitempty"`
	// CloseQuits makes the close button quit instead of hiding to the tray.
	CloseQuits bool `json:"close_quits,omitempty"`
	// ShowAtLogin opens the window when Kyokai starts at login; off (the zero
	// value) starts it in the tray.
	ShowAtLogin bool `json:"show_at_login,omitempty"`
	// ShowInCapture lets screenshots, screen sharing, other remote-desktop
	// tools, Kyokai's own video and the AI's screenshots see Kyokai's
	// windows. Off (the zero value) keeps them out of every capture.
	ShowInCapture bool `json:"show_in_capture,omitempty"`
	// Helper: this device lives in the privileged helper (Follow GUI or
	// Background service), so the GUI uses the helper's socket.
	Helper bool `json:"helper,omitempty"`
	// TrayHintShown records that the hide-to-tray notice was shown once.
	TrayHintShown bool `json:"tray_hint_shown,omitempty"`
	// SidebarRail keeps the main window's sidebar folded to its icons.
	SidebarRail bool `json:"sidebar_rail,omitempty"`
	// CheckAppUpdates checks GitHub for a newer signed app once a day;
	// LastAppCheck and AnnouncedApp keep it to one notification a version.
	CheckAppUpdates bool      `json:"check_app_updates"`
	LastAppCheck    time.Time `json:"last_app_check,omitzero"`
	AnnouncedApp    string    `json:"announced_app,omitempty"`
	// KeyboardMode is the viewers' keyboard mode: "map" (also when empty),
	// "translate" or "legacy".
	KeyboardMode string `json:"keyboard_mode,omitempty"`
	// SoundSource is the sound this device shares: "" for what it plays,
	// else an input device's id (Permissions, Sound to share).
	SoundSource string `json:"sound_source,omitempty"`
	// HidePointer hides the ring that marks the host's pointer in the
	// viewers (for hosts whose picture shows their cursor already).
	HidePointer bool `json:"hide_pointer,omitempty"`
	// ToolbarAt is where the viewers' toolbar sits along the picture's top
	// edge, as a share of the room it has: -0.5 at the left, 0 (the zero
	// value) in the middle, 0.5 at the right.
	ToolbarAt float32 `json:"toolbar_at,omitempty"`
	// StoresUnified is the app version whose first start removed the store
	// of the run mode not in use (cleanup.go).
	StoresUnified string `json:"stores_unified,omitempty"`
	// AskedPermissions is the app version that last asked macOS for the
	// privacy permissions on its own.
	AskedPermissions string `json:"asked_permissions,omitempty"`
	// Viewers are the viewer settings chosen for each device, by Device ID.
	Viewers map[string]ViewerPrefs `json:"viewers,omitempty"`
	// NoLogs keeps no logs, crash traces or connection history on this
	// computer, in the GUI and its engine (Security, No logs).
	NoLogs bool `json:"no_logs,omitempty"`
	// Aliases are the names this device's user gave other devices, by
	// Device ID: shown here instead of the names the devices give
	// themselves, and never sent anywhere.
	Aliases map[string]string `json:"aliases,omitempty"`
	// Uses count how often each of a device card's actions ("rdp", "ssh",
	// "vnc", "ai") was used, by Device ID: the card shows the most used first.
	Uses map[string]map[string]int `json:"uses,omitempty"`
}

// named is what a device says about itself, with the name this device's
// user gave it, if any, in place of its own.
func (p Prefs) named(device string, about localapi.About) localapi.About {
	if alias := p.Aliases[device]; alias != "" {
		about.Name = alias
	}
	return about
}

// ViewerPrefs are the settings a device's viewer opens with: the quality
// preset (0 Faster, 1 Balanced, 2 Sharper), the frame-rate cap ("30 fps"),
// full colour, the codec ("auto" or a codec's name), whether the host's
// sound is muted, and whether Ctrl and
// Cmd (the Windows key) are swapped (nil: the default, swapped between a
// Mac and Windows).
type ViewerPrefs struct {
	Preset int    `json:"preset"`
	FPS    string `json:"fps,omitempty"`
	Full   bool   `json:"full,omitempty"`
	Codec  string `json:"codec,omitempty"`
	Swap   *bool  `json:"swap,omitempty"`
	// Mute: the host's sound is not played.
	Mute bool `json:"mute,omitempty"`
	// Folded: the toolbar is folded to its handle.
	Folded bool `json:"folded,omitempty"`
	// Original: the picture shows at one point per pixel, scrolled,
	// instead of fitted to the window.
	Original bool `json:"original,omitempty"`
	// NoClipboard: the clipboard is not shared with the device, even with
	// its tab in front.
	NoClipboard bool `json:"no_clipboard,omitempty"`
}

func defaultPrefs() Prefs { return Prefs{Theme: "system", CheckAppUpdates: true} }

func prefsPath(stateDir string) string { return filepath.Join(stateDir, "gui.json") }

func loadPrefs(stateDir string) Prefs {
	p := defaultPrefs()
	_, _ = fsutil.ReadJSON(prefsPath(stateDir), &p)
	return p
}

func savePrefs(stateDir string, p Prefs) error { return fsutil.WriteJSON(prefsPath(stateDir), p) }

// ThemePref is the remembered appearance.
func (a *App) ThemePref() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs.Theme
}

// StartHidden reports whether a start at login stays in the tray.
func (a *App) StartHidden() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.prefs.ShowAtLogin
}

// LanguagePref is the remembered interface language ("" follows the system).
func (a *App) LanguagePref() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs.Language
}

// viewerPrefs are the settings remembered for a device's viewer, if any.
func (a *App) viewerPrefs(device string) (ViewerPrefs, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	vp, ok := a.prefs.Viewers[device]
	return vp, ok
}

// setViewerPrefs remembers a device's viewer settings.
// Side effects: writes gui.json.
func (a *App) setViewerPrefs(device string, vp ViewerPrefs) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	viewers := make(map[string]ViewerPrefs, len(p.Viewers)+1)
	for k, v := range p.Viewers {
		viewers[k] = v
	}
	viewers[device] = vp
	p.Viewers = viewers
	a.setPrefs(p)
}

// setAlias names a device on this device only; an empty name goes back
// to the one the device gives itself.
// Side effects: writes gui.json.
func (a *App) setAlias(device, name string) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	aliases := maps.Clone(p.Aliases)
	if aliases == nil {
		aliases = map[string]string{}
	}
	if name == "" {
		delete(aliases, device)
	} else {
		aliases[device] = name
	}
	p.Aliases = aliases
	a.setPrefs(p)
}

// setPrefs stores new prefs (caller does not hold a.mu).
// Returns: the error writing gui.json, if any (the change holds for this
// run).
func (a *App) setPrefs(p Prefs) error {
	a.update(func() { a.prefs = p })
	return savePrefs(a.stateDir, p)
}

// noteUse counts a use of a device card's action (Prefs.Uses).
// Side effects: writes gui.json.
func (a *App) noteUse(device, action string) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	uses := maps.Clone(p.Uses)
	if uses == nil {
		uses = map[string]map[string]int{}
	}
	counts := maps.Clone(uses[device])
	if counts == nil {
		counts = map[string]int{}
	}
	counts[action]++
	uses[device] = counts
	p.Uses = uses
	a.setPrefs(p)
}

// forgetUses drops what a removed device's card counted.
// Side effects: writes gui.json when there was something.
func (a *App) forgetUses(device string) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	if _, ok := p.Uses[device]; !ok {
		return
	}
	uses := maps.Clone(p.Uses)
	delete(uses, device)
	p.Uses = uses
	a.setPrefs(p)
}
