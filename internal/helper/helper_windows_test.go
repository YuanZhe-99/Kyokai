package helper

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestCheckTrusted: a folder a user made (as anyone may in ProgramData) or
// a junction is not the helper's to trust (regression: Install adopted an
// existing %ProgramData%\Kyokai, whose maker kept full control of it).
func TestCheckTrusted(t *testing.T) {
	if Elevated() {
		t.Skip("an administrator's folders belong to Administrators")
	}
	dir := t.TempDir()
	if checkTrusted(dir) == nil {
		t.Error("a folder of this user's is trusted")
	}
	junction := filepath.Join(dir, "j")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, os.Getenv("SystemRoot")).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	if checkTrusted(junction) == nil {
		t.Error("a junction is trusted")
	}
	if localPath(filepath.Join(junction, "System32")) {
		t.Error("a path through a junction reads as local")
	}
	if !localPath(dir) {
		t.Error("a temporary folder is not local")
	}
	for _, p := range []string{`\server\share\bin`, `\?\C:\bin`, `\.\pipe\x`} {
		if localPath(p) {
			t.Errorf("%s reads as local", p)
		}
	}
}

// TestPrepareDirMovesAside: an existing folder Install does not trust is
// moved aside rather than adopted. Needs administrator rights (creating a
// folder owned by Administrators).
func TestPrepareDirMovesAside(t *testing.T) {
	if !Elevated() {
		t.Skip("needs administrator rights")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "Kyokai")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", dir, base).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	if err := prepareDir(dir, stateSDDL); err != nil {
		t.Fatal(err)
	}
	if err := checkTrusted(dir); err != nil {
		t.Fatalf("the new folder: %v", err)
	}
	aside, _ := filepath.Glob(dir + ".untrusted-*")
	if len(aside) != 1 {
		t.Fatalf("the junction was not moved aside: %v", aside)
	}
}
