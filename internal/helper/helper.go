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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/YuanZhe-99/Kyokai/internal/bundlesig"
	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/version"
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
	// OperatorSID (Windows) and OperatorUID (macOS) are the user who
	// installs it, who may then use the helper.
	OperatorSID string
	OperatorUID uint32
}

// Settings is what the installer records for the helper (helper.json in
// the state directory, administrator-only).
type Settings struct {
	OperatorSID string `json:"operator_sid,omitempty"`
	// OperatorUID is the operator on macOS.
	OperatorUID *uint32 `json:"operator_uid,omitempty"`
	// AppDir is the app's bin folder the helper was installed from; when
	// the app is removed from it, the helper removes itself.
	AppDir string `json:"app_dir,omitempty"`
}

// AppGone reports whether the app the helper was installed from is no
// longer there (its engine is missing).
func (s Settings) AppGone() bool {
	return s.AppDir != "" && !fileExists(filepath.Join(s.AppDir, exeName("kyokai-svc")))
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

// verifiedBundle reads the signed bundle in from (the app's bin folder)
// and checks that it holds every file the helper runs and is not older
// than the running helper.
// Returns: the verified bundle, whose Data is what SelfUpdate installs.
func verifiedBundle(from, running string) (bundlesig.Bundle, error) {
	key, err := bundlesig.PublicKey()
	if err != nil {
		return bundlesig.Bundle{}, err
	}
	b, err := bundlesig.Read(from, key)
	if err != nil {
		return b, err
	}
	for _, name := range binaries() {
		if _, ok := b.Data[name]; !ok {
			return b, fmt.Errorf("the signed bundle lacks %s", name)
		}
	}
	if version.Older(b.Version, running) {
		return b, fmt.Errorf("bundle %s is older than the helper %s", b.Version, running)
	}
	return b, nil
}

// installBundle writes a verified bundle's files into dir.
// Side effects: replaces files in dir. A running executable cannot be
// overwritten on Windows, so each old file is renamed aside to <name>.old
// first and put back when its write fails.
// Notes: writes exactly b.Data, never re-reading the app folder, so a file
// swapped there after verification is not installed.
func installBundle(dir string, b bundlesig.Bundle) error {
	for _, name := range b.Names() {
		dst := filepath.Join(dir, name)
		old := dst + ".old"
		_ = os.Remove(old)
		_ = os.Rename(dst, old)
		if err := fsutil.WriteFileAtomic(dst, b.Data[name], 0o755); err != nil {
			_ = os.Rename(old, dst)
			return err
		}
	}
	return nil
}

// deepestFirst lists dir and everything below it, each entry before the
// folder that holds it, so deleting in this order empties folders first;
// nothing for a missing dir.
func deepestFirst(dir string) []string {
	var all []string
	_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			all = append(all, p)
		}
		return nil
	})
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all
}

// dropPending leaves out of Windows' pending file operations
// (PendingFileRenameOperations: source and destination in pairs, an empty
// destination deleting the source) the deletions of anything under dirs,
// which a retired helper scheduled for the next restart: a helper installed
// again before that restart would otherwise lose its files then.
// Returns: the pairs to keep, and whether any were left out.
func dropPending(ops []string, dirs []string) ([]string, bool) {
	under := func(p string) bool {
		p = strings.ToLower(strings.TrimPrefix(p, `\??\`))
		for _, d := range dirs {
			d = strings.ToLower(strings.TrimRight(d, `\`))
			if p == d || strings.HasPrefix(p, d+`\`) {
				return true
			}
		}
		return false
	}
	var keep []string
	dropped := false
	for i := 0; i+1 < len(ops); i += 2 {
		if ops[i+1] == "" && under(ops[i]) {
			dropped = true
			continue
		}
		keep = append(keep, ops[i], ops[i+1])
	}
	return keep, dropped
}
