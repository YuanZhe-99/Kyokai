package gui

import (
	"log"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
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

// TraceShown has the log note every top-level window of this process that
// turns visible, whoever showed it: Kyokai's own shows and any other (a
// window Windows turned visible behind Kyokai's back came up white at
// sign-in; doc/en-us/gui.md, "Tray").
// Side effects: a WinEvent hook for this process, kept for its life;
// call on the main thread, whose message loop delivers the events.
func TraceShown() {
	const (
		eventObjectShow      = 0x8002
		winEventOutOfContext = 0
		gaRoot               = 2
	)
	user32 := windows.NewLazySystemDLL("user32.dll")
	setHook := user32.NewProc("SetWinEventHook")
	getAncestor := user32.NewProc("GetAncestor")
	cb := windows.NewCallback(func(_ windows.Handle, _ uint32, hwnd windows.HWND, object, child int32, _, _ uint32) uintptr {
		if object != 0 || child != 0 {
			return 0
		}
		if root, _, _ := getAncestor.Call(uintptr(hwnd), gaRoot); root != uintptr(hwnd) {
			return 0
		}
		class := make([]uint16, 128)
		n, _ := windows.GetClassName(hwnd, &class[0], int32(len(class)))
		log.Printf("gui: a window turned visible (%s)", windows.UTF16ToString(class[:n]))
		return 0
	})
	setHook.Call(eventObjectShow, eventObjectShow, 0, cb, uintptr(windows.GetCurrentProcessId()), 0, winEventOutOfContext)
}
