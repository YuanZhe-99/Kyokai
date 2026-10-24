package gui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// The keyboard grab holds while the pointer is anywhere in a pane, its
// header and toolbar too, not only over the picture; over another pane, it
// is that pane's; outside the panes, none's.
func TestGrabFollowsThePane(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	a := &App{viewers: map[string]*viewer{}, services: map[string]*serviceTab{}, busy: map[string]bool{}}
	a.connected = true
	a.status = &localapi.Status{DeviceID: "482916735"}
	a.home = &homeTab{app: a}
	vw := &viewerWindow{app: a, main: true}
	a.main = vw
	vw.homeGroupLocked()
	var vs []*viewer
	for i, device := range []string{"213559601", "402118905"} {
		v := &viewer{app: a, device: device, title: device, preset: 1, fpsCap: "30 fps", control: true,
			start: videoStart{Codec: "vp9-sw", Width: 320, Height: 180}, bm: ui.NewBitmap(pic),
			phase: localapi.SessionInfo{ID: uint64(i + 1), DeviceID: device, State: "connected"}, pumping: true}
		v.home.Store(vw)
		a.viewers[device] = v
		vw.tabs = append(vw.tabs, v)
		vw.placeLocked(v, nil, zoneMiddle)
		vs = append(vs, v)
	}
	vw.placeLocked(vs[1], vs[0], zoneRight)
	tt := ui.NewTester(vw.view, 1600, 800)
	tt.Frame()
	tt.Frame()
	under := func(x, y float32) tab {
		tt.Move(x, y)
		tt.Frame()
		tt.Frame()
		vw.mu.Lock()
		defer vw.mu.Unlock()
		return vw.pointerIn
	}
	box := func(v *viewer) ui.Rect {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.box
	}
	left, right := box(vs[0]), box(vs[1])
	if left.W == 0 || right.X <= left.X {
		t.Fatalf("the panes did not lay out side by side: %+v %+v", left, right)
	}
	for _, c := range []struct {
		name string
		x, y float32
		want tab
	}{
		{"the left picture", left.X + left.W/2, left.Y + left.H/2, vs[0]},
		{"the left toolbar", left.X + left.W/2, left.Y + barTop + 12, vs[0]},
		{"the left header", left.X + 40, left.Y - paneHeaderH/2, vs[0]},
		{"the right picture", right.X + right.W/2, right.Y + right.H/2, vs[1]},
		{"the sidebar", 20, 400, nil},
	} {
		if got := under(c.x, c.y); got != c.want {
			t.Errorf("pointer over %s: the grab is %v's, want %v's", c.name, got, c.want)
		}
	}
}

// A pane's menu or split list takes the keyboard while open: no pane grabs
// it (covered), until a choice or the pointer moving over a picture.
func TestPaneMenuTakesTheKeyboard(t *testing.T) {
	a := &App{viewers: map[string]*viewer{}, services: map[string]*serviceTab{}}
	v := &viewer{app: a, device: "213559601"}
	a.viewers[v.device] = v
	a.menuOpened()
	if !a.covered() {
		t.Fatal("an open menu leaves the panes grabbing")
	}
	a.menuClosed()
	if a.covered() {
		t.Fatal("a chosen item leaves the panes without the keyboard")
	}
	a.menuOpened()
	v.sendInput(ui.InputEvent{Kind: ui.InputPointerMove, X: 10, Y: 10})
	if a.covered() {
		t.Fatal("the pointer back over a picture leaves the panes without the keyboard")
	}
	var none *App
	none.menuOpened() // view tests without an app
	none.menuClosed()
}

func TestGrabCall(t *testing.T) {
	for _, c := range []struct {
		want, have        string
		refused, renewing bool
		call              bool
	}{
		{"", "", false, false, false},
		{"map", "", false, false, true},
		{"map", "map", false, false, false},
		{"map", "map", false, true, true}, // renewed
		{"translate", "map", false, false, true},
		{"", "map", false, false, true}, // ended
		{"map", "", true, false, false}, // refused: Legacy
		{"", "", true, false, false},
	} {
		if got := grabCall(c.want, c.have, c.refused, c.renewing); got != c.call {
			t.Errorf("grabCall(%q, %q, refused %v, renewing %v) = %v", c.want, c.have, c.refused, c.renewing, got)
		}
	}
}

// Which keys that reached the window had to get past the grab: every key
// in Map; in Translate the keys that never type and shortcuts, Cmd or Ctrl
// on a Mac, Win, or Ctrl or Alt alone elsewhere (Ctrl+Alt is AltGr).
func TestGrabLetPass(t *testing.T) {
	down := func(k ui.Key, m ui.Modifiers) ui.InputEvent {
		return ui.InputEvent{Kind: ui.InputKeyDown, Key: k, Mods: m}
	}
	for _, c := range []struct {
		mode, goos string
		ev         ui.InputEvent
		passed     bool
	}{
		{"map", "darwin", down(ui.KeyEnter, 0), true},
		{"map", "darwin", down(ui.KeyA, 0), true},
		{"map", "windows", down(ui.KeyBackspace, 0), true},
		{"map", "darwin", ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyEnter}, false},
		{"map", "darwin", ui.InputEvent{Kind: ui.InputText, Text: "a"}, false},
		{"translate", "darwin", down(ui.KeyEnter, 0), true},
		{"translate", "darwin", down(ui.KeyTab, 0), true},
		{"translate", "darwin", down(ui.KeyF5, 0), true},
		{"translate", "darwin", down(ui.KeyLeft, ui.Shift), true},
		{"translate", "darwin", down(ui.KeyA, 0), false},
		{"translate", "darwin", down(ui.KeyA, ui.Shift), false},
		{"translate", "darwin", down(ui.KeySpace, 0), false},
		{"translate", "darwin", down(ui.KeyMinus, 0), false},
		{"translate", "darwin", down(ui.KeyC, ui.Super), true},
		{"translate", "darwin", down(ui.KeyC, ui.Ctrl), true},
		{"translate", "darwin", down(ui.KeyE, ui.Alt), false}, // Option types
		{"translate", "windows", down(ui.KeyC, ui.Ctrl), true},
		{"translate", "windows", down(ui.KeyF, ui.Alt), true},
		{"translate", "windows", down(ui.KeyQ, ui.Ctrl|ui.Alt), false}, // AltGr
		{"translate", "windows", down(ui.KeyD, ui.Super), true},
	} {
		if got := grabLetPass(c.mode, c.ev, c.goos); got != c.passed {
			t.Errorf("%s on %s, %+v: passed %v, want %v", c.mode, c.goos, c.ev, got, c.passed)
		}
	}
}

// The keyboard dots: the first while the viewer grabs, the second while
// the core says the grab takes the keys and none got past it lately.
func TestGrabDots(t *testing.T) {
	now := time.Now()
	st := func(state string, missedAgo time.Duration) grabStatus {
		s := grabStatus{KeyboardState: core.KeyboardState{State: state}}
		if missedAgo > 0 {
			s.missed = now.Add(-missedAgo)
		}
		return s
	}
	for _, c := range []struct {
		name          string
		grabbing      bool
		s             grabStatus
		first, second dot
	}{
		{"no grab", false, st("taking", 0), dotOff, dotOff},
		{"not asked yet", true, st("", 0), dotOn, dotOff},
		{"taking", true, st("taking", 0), dotOn, dotOn},
		{"a key got past", true, st("taking", time.Second), dotOn, dotWarn},
		{"long ago", true, st("taking", 5*time.Second), dotOn, dotOn},
		{"secure input", true, st("secure-input", 0), dotOn, dotWarn},
		{"tap off", true, st("off", 0), dotOn, dotWarn},
	} {
		first, second := grabDots(c.grabbing, c.s, now)
		if first != c.first || second != c.second {
			t.Errorf("%s: dots %v %v, want %v %v", c.name, first, second, c.first, c.second)
		}
	}
	secure := grabStatus{KeyboardState: core.KeyboardState{State: "secure-input", Holder: "Terminal"}}
	if h := grabHint(true, secure, now); !strings.Contains(h, "Terminal") {
		t.Errorf("the tooltip does not name the program holding secure input: %q", h)
	}
}

// What the core says of the grab asks for a renewal where one helps: the
// tap or hook off, the grab gone, another window's; not where only the
// system can help.
func TestGrabStateRenews(t *testing.T) {
	for state, renew := range map[string]bool{
		"taking": false, "off": true, "none": true, "other-window": true,
		"secure-input": false, "behind": false, "no-reader": false,
	} {
		v := &viewer{device: "213559601"}
		v.noteGrabState(core.KeyboardState{State: state}, time.Now())
		if v.renew != renew {
			t.Errorf("%s: renew %v, want %v", state, v.renew, renew)
		}
	}
}

// A key the grab should have taken that reached the window goes to the host
// as in Legacy mode, and the grab is renewed; one typed just before the grab
// was taken, or one Translate leaves to the controller's layout, does not.
func TestKeysPastTheGrabStillGo(t *testing.T) {
	for _, c := range []struct {
		name    string
		mode    string
		since   time.Duration
		key     ui.Key
		sent    bool
		renewed bool
	}{
		{"Enter past Map", "map", time.Second, ui.KeyEnter, true, true},
		{"Enter typed as the grab came", "map", 10 * time.Millisecond, ui.KeyEnter, false, false},
		{"Delete past Translate", "translate", time.Second, ui.KeyBackspace, true, true},
		{"a letter Translate leaves to the layout", "translate", time.Second, ui.KeyA, false, false},
	} {
		v := &viewer{device: "213559601", control: true, grabbed: c.mode, grabbedAt: time.Now().Add(-c.since),
			out: make(chan []byte, 4), moved: make(chan struct{}, 1)}
		v.start = videoStart{Width: 320, Height: 180}
		v.sendInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: c.key})
		sent := len(v.out) > 0
		if sent != c.sent || v.renew != c.renewed {
			t.Errorf("%s: sent %v renewed %v, want %v %v", c.name, sent, v.renew, c.sent, c.renewed)
		}
		if sent {
			if p := string(<-v.out); !strings.Contains(p, `"kind":"key"`) {
				t.Errorf("%s: sent %s, want a named key", c.name, p)
			}
		}
	}
}
