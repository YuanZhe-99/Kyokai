package gui

import (
	"path/filepath"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
)

// Prefs are the GUI's own settings, kept in the state directory.
type Prefs struct {
	// Theme is "system", "light" or "dark".
	Theme string `json:"theme,omitempty"`
	// Language is the chosen interface language ("" follows the system).
	Language string `json:"language,omitempty"`
	// CloseQuits makes the close button quit instead of hiding to the tray.
	CloseQuits bool `json:"close_quits,omitempty"`
	// ShowInCapture lets screenshots, screen sharing, other remote-desktop
	// tools, Kyokai's own video and the AI's screenshots see Kyokai's
	// windows. Off (the zero value) keeps them out of every capture.
	ShowInCapture bool `json:"show_in_capture,omitempty"`
	// Helper: this device lives in the privileged helper (Follow GUI or
	// Background service), so the GUI uses the helper's socket.
	Helper bool `json:"helper,omitempty"`
	// TrayHintShown records that the hide-to-tray notice was shown once.
	TrayHintShown bool `json:"tray_hint_shown,omitempty"`
	// CheckAppUpdates checks GitHub for a newer signed app once a day;
	// LastAppCheck and AnnouncedApp keep it to one notification a version.
	CheckAppUpdates bool      `json:"check_app_updates"`
	LastAppCheck    time.Time `json:"last_app_check,omitzero"`
	AnnouncedApp    string    `json:"announced_app,omitempty"`
	// KeyboardMode is the viewers' keyboard mode: "map" (also when empty),
	// "translate" or "legacy".
	KeyboardMode string `json:"keyboard_mode,omitempty"`
	// HidePointer hides the ring that marks the host's pointer in the
	// viewers (for hosts whose picture shows their cursor already).
	HidePointer bool `json:"hide_pointer,omitempty"`
	// AskedPermissions is the app version that last asked macOS for the
	// privacy permissions on its own.
	AskedPermissions string `json:"asked_permissions,omitempty"`
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

// LanguagePref is the remembered interface language ("" follows the system).
func (a *App) LanguagePref() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs.Language
}

// setPrefs stores new prefs (caller does not hold a.mu).
func (a *App) setPrefs(p Prefs) {
	a.update(func() { a.prefs = p })
	_ = savePrefs(a.stateDir, p)
}
