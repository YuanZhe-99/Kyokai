package gui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/fsutil"
	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// cleanup is what the first start of this version does about the store of
// the run mode not in use (doc/en-us/run-modes.md, "Cleanup after
// updating"): before 0.4.1 a switch left a second set behind.
type cleanup int

const (
	// cleanupNone: nothing is left over.
	cleanupNone cleanup = iota
	// cleanupUserSet: attached to the helper, the No privileges engine's
	// store is left over in this profile's state folder.
	cleanupUserSet
	// cleanupHelper: attached to this profile's own engine, Kyokai Helper
	// is still installed with a store of its own.
	cleanupHelper
)

// userSetFiles are the No privileges engine's store and state files.
var userSetFiles = []string{"secrets.bin", "master.key", "device.json", "ai.json", "ai-tasks.json"}

// cleanupAction decides the cleanup from the engine the GUI attached to.
// Inputs: st — its status; installed — whether Kyokai Helper is installed
// for this profile (only the installed app's, never a development one);
// userSet — whether this profile's state folder still holds a store.
func cleanupAction(st *localapi.Status, installed, userSet bool) cleanup {
	switch {
	case st == nil || st.Moved != "":
		return cleanupNone
	case st.RunMode == "helper" && st.DeviceID != "" && userSet:
		return cleanupUserSet
	case st.RunMode == "user" && installed:
		return cleanupHelper
	}
	return cleanupNone
}

// hasHistory is whether an engine's status shows anything set up or
// learned: known devices or networks.
func hasHistory(st *localapi.Status) bool {
	return st != nil && (len(st.Recent) > 0 || len(st.Network.Networks) > 0 || st.PermanentPassword)
}

// storesChanged runs the cleanup once, the first time this version attaches
// to an engine. Notes: the caller holds a.mu.
func (a *App) storesChanged(st *localapi.Status) {
	if a.cleaning || a.prefs.StoresUnified == version.Version || st == nil || st.Moved != "" || helperBehind(st, version.Build()) {
		return
	}
	_, err := os.Stat(filepath.Join(a.stateDir, "secrets.bin"))
	// Kyokai Helper serves the installed app's profile: a development
	// profile (KYOKAI_PROFILE) never removes it (regression: one in No
	// privileges offered to).
	act := cleanupAction(st, a.profile == "" && helper.Installed(), err == nil)
	a.cleaning = true
	st2 := *st
	go func() {
		done := a.cleanUp(act, &st2)
		a.update(func() {
			a.cleaning = false
			if done {
				p := a.prefs
				p.StoresUnified = version.Version
				go a.setPrefs(p)
			}
		})
	}()
}

// cleanUp carries a cleanup out.
// Returns: whether it is finished (else it is tried at the next start).
func (a *App) cleanUp(act cleanup, st *localapi.Status) bool {
	switch act {
	case cleanupUserSet:
		if err := a.dropUserSet(st.DeviceID); err != nil {
			return false
		}
		a.update(func() { a.setToast(L.CleanupUserSet(), false) })
		return true
	case cleanupHelper:
		ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
		defer cancel()
		hlp := localapi.NewClient(helper.SocketPath)
		resp, err := hlp.Do(ctx, localapi.Request{Type: localapi.RequestStatus})
		if err != nil {
			// Another operator's helper, or not running: nothing to do here.
			var re *localapi.RequestError
			return errors.As(err, &re)
		}
		// The helper holds what this run mode lacks (a lost choice of run
		// mode): the person decides which set stays.
		if hasHistory(resp.Status) && !hasHistory(st) && a.confirm(L.CleanupHelperHasMore(), L.CleanupUseHelper()) {
			if err := a.useSocket(helper.SocketPath, true); err != nil {
				return false
			}
			_ = a.dropUserSet(resp.Status.DeviceID)
			return true
		}
		if helperBehind(resp.Status, version.Build()) {
			// Only a signed update, never a prompt at start; an unsigned
			// build tries again next time.
			if a.signedHelperUpdate() != nil || waitForBuild(ctx, hlp) != nil {
				return false
			}
		}
		if !removeHelper(ctx, hlp) {
			return false
		}
		a.update(func() { a.setToast(L.CleanupHelper(), false) })
		return true
	}
	return true
}

// dropUserSet deletes the No privileges engine's left-over store and
// leaves the marker that keeps an engine started there from making an
// identity of its own.
// Notes: on macOS the store's Keychain item stays (it opens nothing
// without the file).
func (a *App) dropUserSet(deviceID string) error {
	marker, _ := json.Marshal(map[string]any{"to": "helper", "device_id": deviceID, "at": time.Now().Unix()})
	if err := fsutil.WriteFileAtomic(filepath.Join(a.stateDir, "moved.json"), marker, 0o600); err != nil {
		return err
	}
	for _, f := range userSetFiles {
		if err := os.Remove(filepath.Join(a.stateDir, f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
