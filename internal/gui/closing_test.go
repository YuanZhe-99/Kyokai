package gui

import (
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestDecideClose: quitting never asks; the main window with Quit chosen
// asks while anything is live, then quits; otherwise a window asks for two
// live connections or more, or one it does not show, then disconnects them
// and hides (main) or closes.
func TestDecideClose(t *testing.T) {
	for _, c := range []struct {
		name string
		f    closeFacts
		want closeDecision
	}{
		{"quitting with connections", closeFacts{quitting: true, main: true, quits: true, liveHere: 2, liveAll: 3}, closeDecision{closeNow, 0}},
		{"quitting from a viewer window", closeFacts{quitting: true, liveHere: 2, liveAll: 2}, closeDecision{closeNow, 0}},
		{"main, Quit chosen, nothing live", closeFacts{main: true, quits: true}, closeDecision{closeQuit, 0}},
		{"main, Quit chosen, live elsewhere", closeFacts{main: true, quits: true, liveAll: 1}, closeDecision{closeQuit, 1}},
		{"main hides, nothing live", closeFacts{main: true, noneShown: true}, closeDecision{closeHide, 0}},
		{"main hides, one shown", closeFacts{main: true, liveHere: 1, liveAll: 1}, closeDecision{closeHide, 0}},
		{"main hides, one not shown", closeFacts{main: true, noneShown: true, liveHere: 1, liveAll: 1}, closeDecision{closeHide, 1}},
		{"main hides, two", closeFacts{main: true, liveHere: 2, liveAll: 2}, closeDecision{closeHide, 2}},
		{"main hides, live only elsewhere", closeFacts{main: true, noneShown: true, liveAll: 2}, closeDecision{closeHide, 0}},
		{"window, one", closeFacts{liveHere: 1, liveAll: 1}, closeDecision{closeWindow, 0}},
		{"window, two", closeFacts{liveHere: 2, liveAll: 3}, closeDecision{closeWindow, 2}},
		{"window, none live", closeFacts{liveAll: 1}, closeDecision{closeWindow, 0}},
	} {
		if got := decideClose(c.f); got != c.want {
			t.Errorf("%s: decideClose = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Closing the main window counts its live tabs, desktops and services
// alike, and whether it shows any of them.
func TestCloseFactsCountEveryKind(t *testing.T) {
	a := &App{viewers: map[string]*viewer{}, services: map[string]*serviceTab{}, tray: &Tray{}}
	a.home = &homeTab{app: a}
	a.main = &viewerWindow{app: a, main: true}
	desk := &viewer{app: a, device: "111111111", phase: localapi.SessionInfo{State: "connected"}}
	ended := &viewer{app: a, device: "222222222", phase: localapi.SessionInfo{State: "failed"}}
	ssh := &serviceTab{app: a, device: "111111111", service: serviceSSH, key: serviceKey("111111111", serviceSSH), phase: svcRunning}
	form := &serviceTab{app: a, device: "333333333", service: serviceVNC, key: serviceKey("333333333", serviceVNC), phase: svcForm}
	a.viewers[desk.device], a.viewers[ended.device] = desk, ended
	a.services[ssh.tabKey()], a.services[form.tabKey()] = ssh, form
	for _, v := range []tab{desk, ended, ssh, form} {
		a.main.tabs = append(a.main.tabs, v)
		a.main.placeLocked(v, nil, zoneMiddle)
	}
	a.main.homeGroupLocked()
	f := a.closeFactsFor(a.main)
	want := closeFacts{main: true, liveHere: 2, liveAll: 2, noneShown: true}
	if f != want {
		t.Fatalf("closeFactsFor = %+v, want %+v", f, want)
	}
	if d := decideClose(f); d != (closeDecision{closeHide, 2}) {
		t.Fatalf("decideClose = %+v", d)
	}
	// A live connection on screen is one the person sees going.
	a.main.placeLocked(desk, nil, zoneMiddle)
	if f := a.closeFactsFor(a.main); f.noneShown {
		t.Fatalf("the desktop shows: %+v", f)
	}
}
