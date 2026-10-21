package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"

	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// A GUI that switched to the helper attaches to it again when it starts,
// development profiles included; without the helper it keeps its own engine.
func TestStartSocketFollowsTheRunModeChoice(t *testing.T) {
	const own = "own.sock"
	for _, c := range []struct {
		helperMode, installed bool
		want                  string
	}{
		{false, false, own},
		{false, true, own},
		{true, false, own},
		{true, true, helper.SocketPath},
	} {
		if got := startSocket(c.helperMode, c.installed, own); got != c.want {
			t.Errorf("startSocket(%v, %v) = %q, want %q", c.helperMode, c.installed, got, c.want)
		}
	}
}

// Quitting the app (its menu, the Dock, a logout) is not vetoed by the
// window's close-to-tray: macOS would otherwise see the quit cancelled.
func TestQuitIsNotHiddenIntoTheTray(t *testing.T) {
	a := &App{tray: &Tray{}}
	a.MarkQuitting()
	e := &mygo.CloseEvent{}
	a.HandleClose(e)
	if e.DefaultPrevented() {
		t.Fatal("closing during a quit was prevented")
	}
}

// The viewer's AI panel can be collapsed, but a step waiting for approval
// always shows it.
func TestAIPanelNeverHidesAnApproval(t *testing.T) {
	for _, c := range []struct {
		name string
		st   viewerAI
		want bool
	}{
		{"no AI permission", viewerAI{}, false},
		{"shown", viewerAI{run: true}, true},
		{"collapsed", viewerAI{run: true, hidden: true}, false},
		{"collapsed while working", viewerAI{run: true, hidden: true, task: &localapi.AITask{State: "working"}}, false},
		{"collapsed while waiting for approval", viewerAI{run: true, hidden: true, task: &localapi.AITask{State: "approval"}}, true},
	} {
		if got := c.st.aiShown(); got != c.want {
			t.Errorf("%s: shown = %v, want %v", c.name, got, c.want)
		}
	}
}

// A user engine whose store moved to the helper makes the GUI start on the
// helper even when gui.json lost the choice (regression: a fresh identity
// started beside the helper's and a later switch moved it over the real one).
func TestMovedToHelper(t *testing.T) {
	dir := t.TempDir()
	if movedToHelper(dir) {
		t.Fatal("no marker, no move")
	}
	for _, c := range []struct {
		marker string
		want   bool
	}{
		{`{"to":"helper","device_id":"123456789"}`, true},
		{`{"to":"user"}`, false},
		{`not json`, false},
	} {
		if err := os.WriteFile(filepath.Join(dir, "moved.json"), []byte(c.marker), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := movedToHelper(dir); got != c.want {
			t.Errorf("%s: got %v", c.marker, got)
		}
	}
}

// The first start of a version removes the store of the run mode not in
// use: the user set while attached to the helper, the helper while
// attached to this profile's own engine.
func TestCleanupAction(t *testing.T) {
	helperSt := &localapi.Status{RunMode: "helper", DeviceID: "123456789"}
	userSt := &localapi.Status{RunMode: "user", DeviceID: "123456789"}
	parked := &localapi.Status{RunMode: "user", Moved: "helper"}
	for _, c := range []struct {
		name             string
		st               *localapi.Status
		installed, users bool
		want             cleanup
	}{
		{"no status", nil, true, true, cleanupNone},
		{"helper with a user set left", helperSt, true, true, cleanupUserSet},
		{"helper, nothing left", helperSt, true, false, cleanupNone},
		{"user engine, helper installed", userSt, true, true, cleanupHelper},
		{"user engine, no helper", userSt, false, true, cleanupNone},
		{"parked engine", parked, true, false, cleanupNone},
	} {
		if got := cleanupAction(c.st, c.installed, c.users); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
