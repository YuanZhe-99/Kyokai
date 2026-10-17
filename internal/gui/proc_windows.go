package gui

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// hideWindow keeps a console window from flashing up for the engine.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// lightTaskbar reports whether Windows draws the taskbar and tray light
// (Personalize's SystemUsesLightTheme), where a white tray glyph vanishes.
func lightTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}
