package gui

import (
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
	a.main = &viewerWindow{app: a, main: true}
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
