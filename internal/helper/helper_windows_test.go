package helper

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestVersionHasNoWindow: asking the installed engine, a console program,
// for its version opens no console window (regression: the GUI asked as it
// started, and Windows left a blank window on the screen at login).
func TestVersionHasNoWindow(t *testing.T) {
	a := versionCmd().SysProcAttr
	if a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("version query opens a window: %+v", a)
	}
}
