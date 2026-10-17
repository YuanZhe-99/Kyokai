// Package helper installs, removes and inspects the privileged helper: the
// engine registered once with the operating system so that Kyokai can run
// in the Follow GUI and Background service modes without asking for
// administrator rights again (doc/en-us/run-modes.md).
//
// Install and Uninstall need administrator rights (the GUI starts them with
// one prompt); Inspect works for anyone.
//
// Adapted from EasyTier MyGo's internal/helper (same author, GPL-3.0,
// relicensed under AGPL-3.0 here).
package helper

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
)

// Group may use the helper in addition to the operator, when an
// administrator created it.
const Group = "kyokai"

// ErrUnsupported reports a platform without a helper backend yet.
var ErrUnsupported = errors.New("the privileged helper is not available on this platform yet")

// Status describes the helper as installed on this machine.
type Status struct {
	Installed bool `json:"installed"`
	// Active means the service is running.
	Active bool `json:"active"`
	// Version is the installed helper's version, when readable.
	Version string `json:"version,omitempty"`
}

// InstallOptions describes an installation.
type InstallOptions struct {
	// From is the directory holding the app's kyokai-svc and core library.
	From string
	// OperatorSID is the user who installs it (Windows), who may then use
	// the helper.
	OperatorSID string
}

// Settings is what the installer records for the helper (helper.json in
// the state directory, administrator-only).
type Settings struct {
	OperatorSID string `json:"operator_sid"`
}

// binaries are the files the helper runs, copied from the app.
func binaries() []string {
	return []string{exeName("kyokai-svc"), core.LibraryName()}
}

// svcPath is the installed engine.
func svcPath() string { return filepath.Join(BinDir, exeName("kyokai-svc")) }

// ReadSettings reads what the installer recorded.
func ReadSettings() (Settings, error) {
	var s Settings
	ok, err := fsutil.ReadJSON(filepath.Join(StateDir, "helper.json"), &s)
	if err == nil && !ok {
		err = errors.New("the helper is not installed")
	}
	return s, err
}

// fileExists reports whether p exists.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// copyFile copies src to dst atomically.
// Notes: reads src once; used by Install, where an administrator approved
// the source.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(dst, data, 0o755)
}

// exeName adds .exe on Windows.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// removeWithParent deletes dir and everything below it, then its parent
// when that is left empty.
// Inputs: dir, a directory that may be absent.
// Side effects: removes files; the parent stays when anything else is in it.
// Notes: a missing dir or parent is not an error, so a repeated uninstall
// succeeds.
func removeWithParent(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || len(entries) > 0 {
		return err
	}
	if err := os.Remove(parent); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
