package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeTab is a tab that only has a name.
type fakeTab string

func (f fakeTab) tabKey() string             { return string(f) }
func (f fakeTab) tabTitle() string           { return string(f) }
func (f fakeTab) head() paneHead             { return paneHead{title: string(f)} }
func (f fakeTab) tabWindow() *viewerWindow   { return nil }
func (f fakeTab) setTabWindow(*viewerWindow) {}
func (f fakeTab) view(*ui.Context)           {}
func (f fakeTab) live() bool                 { return false }
func (f fakeTab) disconnect()                {}
func (f fakeTab) syncGrab(bool)              {}
func (f fakeTab) endGrab()                   {}
func (f fakeTab) setFront(bool)              {}

// layout writes a split as text: a tab's name, (x|y) side by side, (x/y)
// one above the other; the tab in focus after a colon.
func layout(s split) string {
	var w func(p *pane) string
	w = func(p *pane) string {
		switch {
		case p == nil:
			return "-"
		case p.first == nil:
			return p.tab.tabKey()
		case p.across:
			return "(" + w(p.first) + "|" + w(p.second) + ")"
		default:
			return "(" + w(p.first) + "/" + w(p.second) + ")"
		}
	}
	focus := "-"
	if s.focus != nil {
		focus = s.focus.tabKey()
	}
	return w(s.root) + ":" + focus
}

// TestSplitPlaces: a tab dropped on a pane's edge splits that pane, so one
// side may hold one tab and the other two; a tab moves rather than shows
// twice; a fifth tab takes a place instead of splitting; closing tabs
// gives their room to their neighbours.
func TestSplitPlaces(t *testing.T) {
	a, b, c, d, e := fakeTab("a"), fakeTab("b"), fakeTab("c"), fakeTab("d"), fakeTab("e")
	var s split
	for _, st := range []struct {
		name string
		do   func()
		want string
	}{
		{"one tab", func() { s.single(a) }, "a:a"},
		{"b right of a", func() { s.place(b, a, zoneRight) }, "(a|b):b"},
		{"c below b: a stays full height", func() { s.place(c, b, zoneBottom) }, "(a|(b/c)):c"},
		{"d above a", func() { s.place(d, a, zoneTop) }, "((d/a)|(b/c)):d"},
		{"a fifth tab takes a place", func() { s.place(e, b, zoneLeft) }, "((d/a)|(e/c)):e"},
		{"a moves left of c", func() { s.place(a, c, zoneLeft) }, "(d|(e/(a|c))):a"},
		{"b chosen replaces the tab in focus", func() { s.show(b) }, "(d|(e/(b|c))):b"},
		{"c chosen takes the focus", func() { s.show(c) }, "(d|(e/(b|c))):c"},
		{"closing c gives b its room", func() { s.remove(c) }, "(d|(e/b)):b"},
		{"closing d gives the right side the window", func() { s.remove(d) }, "(e/b):b"},
		{"dropped on itself only focuses", func() { s.place(e, e, zoneLeft) }, "(e/b):e"},
		{"closing e leaves b", func() { s.remove(e) }, "b:b"},
		{"closing the last leaves none", func() { s.remove(b) }, "-:-"},
	} {
		st.do()
		if got := layout(s); got != st.want {
			t.Fatalf("%s: %s, want %s", st.name, got, st.want)
		}
	}
	if s.remove(a) {
		t.Error("a tab not shown was removed")
	}
}

// TestGroups: the main window's sidebar entries (doc/en-us/gui.md,
// "Windows"). Each tab added gets an entry of its own; one dropped on
// another's edge joins its group, and one dropped in another's place takes
// it while that one keeps an entry of its own; a pane leaving its group
// takes its room along, a group left empty goes, and with nothing left Home
// shows.
func TestGroups(t *testing.T) {
	app := &App{}
	app.home = &homeTab{app: app}
	vw := &viewerWindow{app: app, main: true}
	app.main = vw
	vw.homeGroupLocked()
	a, b, c := fakeTab("a"), fakeTab("b"), fakeTab("c")
	entries := func() string {
		var out []string
		for _, g := range vw.groups {
			l := layout(g.split)
			if g == vw.cur {
				l = "*" + l
			}
			out = append(out, l)
		}
		return strings.Join(out, " ")
	}
	leave := func(v tab) { vw.leaveGroupLocked(vw.groupOf(v), v) }
	for _, st := range []struct {
		name string
		do   func()
		want string
	}{
		{"Home first", func() {}, "*home:home"},
		{"a, b and c get entries of their own", func() {
			for _, v := range []tab{a, b, c} {
				vw.tabs = append(vw.tabs, v)
				vw.placeLocked(v, nil, zoneMiddle)
			}
		}, "home:home a:a b:b *c:c"},
		{"b right of a joins a's group", func() { vw.placeLocked(b, a, zoneRight) }, "home:home *(a|b):b c:c"},
		{"c in b's place: b keeps an entry", func() { vw.placeLocked(c, b, zoneMiddle) }, "home:home *(a|c):c b:b"},
		{"choosing a shows its group with the focus on it", func() { vw.placeLocked(a, nil, zoneMiddle) }, "home:home *(a|c):a b:b"},
		{"c leaves the group for an entry of its own", func() { leave(c); vw.placeLocked(c, nil, zoneMiddle) }, "home:home a:a b:b *c:c"},
		{"an emptied entry goes; the one before shows", func() { leave(c) }, "home:home a:a *b:b"},
		{"Home leaves too", func() { leave(app.home); leave(a) }, "*b:b"},
		{"with nothing left, Home shows", func() {
			leave(b)
			if vw.cur == nil {
				vw.homeGroupLocked()
			}
		}, "*home:home"},
	} {
		st.do()
		if got := entries(); got != st.want {
			t.Fatalf("%s: %s, want %s", st.name, got, st.want)
		}
	}
}

// TestCorners: the pane that keeps the room of the window's caption
// buttons is the top-right one of its group, the one that holds the
// sidebar's button while the sidebar is hidden the top-left one.
func TestCorners(t *testing.T) {
	a, b, c := fakeTab("a"), fakeTab("b"), fakeTab("c")
	var s split
	s.single(a)
	if s.topRight() != a || s.topLeft() != a {
		t.Fatal("alone, a is in both corners")
	}
	s.place(b, a, zoneRight)
	s.place(c, b, zoneBottom)
	if got := s.topRight(); got != b {
		t.Fatalf("(a|(b/c)): %v, want b", got)
	}
	if got := s.topLeft(); got != a {
		t.Fatalf("(a|(b/c)) top-left: %v, want a", got)
	}
	s.place(c, a, zoneTop)
	if got := s.topRight(); got != b {
		t.Fatalf("((c/a)|b): %v, want b", got)
	}
	if got := s.topLeft(); got != c {
		t.Fatalf("((c/a)|b) top-left: %v, want c", got)
	}
}

// TestPaneRooms: a pane keeps at least 360 × 280 inside its margin, a split
// both halves and the divider's line; the divider stops where a half would
// go below that, and a split with no room for both keeps its ratio.
func TestPaneRooms(t *testing.T) {
	a, b, c := &pane{tab: fakeTab("a")}, &pane{tab: fakeTab("b")}, &pane{tab: fakeTab("c")}
	across := &pane{across: true, ratio: 50, first: a, second: b}
	nested := &pane{across: true, ratio: 50, first: a, second: &pane{ratio: 50, first: b, second: c}}
	for _, st := range []struct {
		name string
		p    *pane
		want size
	}{
		{"one pane", a, size{368, 288}},
		{"side by side", across, size{737, 288}},
		{"one beside two above each other", nested, size{737, 577}},
		{"nothing", nil, size{}},
	} {
		if got := st.p.minRoom(); got != st.want {
			t.Errorf("%s: %v, want %v", st.name, got, st.want)
		}
	}
	for _, st := range []struct {
		name                    string
		ratio, total, lo, hi, w float32
	}{
		{"dragged too far left", 10, 1000, 368, 368, 368 * 100 / 999.},
		{"dragged too far right", 95, 1000, 368, 368, (999 - 368) * 100 / 999.},
		{"in between", 50, 1000, 368, 368, 50},
		{"no room for both", 10, 600, 368, 368, 10},
		{"not laid out yet", 10, 0, 368, 368, 10},
	} {
		if got := keepRoom(st.ratio, st.total, st.lo, st.hi); got != st.w {
			t.Errorf("%s: %v, want %v", st.name, got, st.w)
		}
	}
	s := split{root: nested}
	if r, ok := s.roomOf(fakeTab("c"), size{1001, 801}); !ok || r != (size{500, 400}) {
		t.Errorf("c's room: %v %v, want {500 400}", r, ok)
	}
}

// TestSplitFits: a pane splits side by side from 737 wide and above and
// below from 577 tall; a drag shows only the zones that fit, the middle
// taking the room of the others.
func TestSplitFits(t *testing.T) {
	for _, st := range []struct {
		room size
		z    dropZone
		want bool
	}{
		{size{737, 300}, zoneRight, true}, {size{736, 900}, zoneLeft, false},
		{size{400, 577}, zoneBottom, true}, {size{900, 576}, zoneTop, false},
		{size{100, 100}, zoneMiddle, true},
	} {
		if got := fits(st.room, st.z); got != st.want {
			t.Errorf("fits(%v, %d) = %v", st.room, st.z, got)
		}
	}
	zones := func(across, down bool) string {
		var out []string
		for _, z := range dropZonesFor(across, down) {
			out = append(out, fmt.Sprint(z))
		}
		return strings.Join(out, " ")
	}
	for _, st := range []struct {
		across, down bool
		want         string
	}{
		{true, true, "{1 0 0 25 100} {2 75 0 25 100} {3 25 0 50 25} {4 25 75 50 25} {0 25 25 50 50}"},
		{true, false, "{1 0 0 25 100} {2 75 0 25 100} {0 25 0 50 100}"},
		{false, true, "{3 0 0 100 25} {4 0 75 100 25} {0 0 25 100 50}"},
		{false, false, "{0 0 0 100 100}"},
	} {
		if got := zones(st.across, st.down); got != st.want {
			t.Errorf("zones(%v, %v) = %s, want %s", st.across, st.down, got, st.want)
		}
	}
}

// TestWindowFits: the main window's minimum follows the group shown (about
// 990 for two side by side beside the sidebar, about 800 beside the rail);
// a smaller window grows on its display, and a display that cannot hold
// the group makes it come apart.
func TestWindowFits(t *testing.T) {
	a, b := fakeTab("a"), fakeTab("b")
	var one, two, stacked, zoomed group
	one.split.single(a)
	two.split.single(a)
	two.split.place(b, a, zoneRight)
	stacked.split.single(a)
	stacked.split.place(b, a, zoneBottom)
	zoomed = two
	zoomed.zoom = a
	for _, st := range []struct {
		name string
		g    group
		side float32
		want size
	}{
		{"one pane", one, shellSidebarW, size{mainMinW, mainMinH}},
		{"two side by side", two, shellSidebarW, size{989, mainMinH}},
		{"two side by side, the rail", two, railW, size{797, mainMinH}},
		{"two above each other", stacked, shellSidebarW, size{mainMinW, 585}},
		{"one maximized", zoomed, shellSidebarW, size{mainMinW, mainMinH}},
	} {
		if got := windowNeed(st.g, st.side); got != st.want {
			t.Errorf("%s: %v, want %v", st.name, got, st.want)
		}
	}
	need := size{989, 520}
	for _, st := range []struct {
		name      string
		win, work size
		want      fit
	}{
		{"holds it", size{1200, 760}, size{1920, 1040}, fitKeep},
		{"too narrow, room to grow", size{800, 760}, size{1920, 1040}, fitGrow},
		{"a display too small", size{800, 600}, size{960, 1040}, fitApart},
	} {
		if got := fitFor(need, st.win, st.work); got != st.want {
			t.Errorf("%s: %d, want %d", st.name, got, st.want)
		}
	}
}

// TestPlacesWhereItFits: a pane goes beside another only where both keep
// their minimum, else the other way, else in an entry of its own; a group
// that comes apart gives each pane an entry of its own in its place, the
// pane in focus shown.
func TestPlacesWhereItFits(t *testing.T) {
	app := &App{}
	app.home = &homeTab{app: app}
	vw := &viewerWindow{app: app, main: true, area: size{900, 600}}
	app.main = vw
	vw.homeGroupLocked()
	a, b, c, d := fakeTab("a"), fakeTab("b"), fakeTab("c"), fakeTab("d")
	for _, v := range []tab{a, b, c, d} {
		vw.tabs = append(vw.tabs, v)
		vw.placeLocked(v, nil, zoneMiddle)
	}
	entries := func() string {
		var out []string
		for _, g := range vw.groups {
			l := layout(g.split)
			if g == vw.cur {
				l = "*" + l
			}
			out = append(out, l)
		}
		return strings.Join(out, " ")
	}
	for _, st := range []struct {
		name string
		do   func()
		want string
	}{
		{"b right of a: room for it", func() { vw.placeLocked(b, a, zoneRight) }, "home:home *(a|b):b c:c d:d"},
		{"c right of b: no room, so below", func() { vw.placeLocked(c, b, zoneRight) }, "home:home *(a|(b/c)):c d:d"},
		{"d below c: no room either way", func() { vw.placeLocked(d, c, zoneBottom) }, "home:home (a|(b/c)):c *d:d"},
		{"the group comes apart", func() {
			g := vw.groupOf(c)
			vw.cur = g
			vw.comeApartLocked(g)
		}, "home:home a:a b:b *c:c d:d"},
	} {
		st.do()
		if got := entries(); got != st.want {
			t.Fatalf("%s: %s, want %s", st.name, got, st.want)
		}
	}
}

// TestBarInHead: the toolbar goes into a pane's header once the empty part
// has room for it and barDragRoom on each side to move the window, stays
// while each side keeps that, and does not come back at the width it left
// at (regression guard: it must never go back and forth frame by frame).
func TestBarInHead(t *testing.T) {
	v := &viewer{pumping: true, barW: 400}
	for _, st := range []struct {
		name  string
		headW float32 // as the header measured it there
		empty float32
		want  bool
	}{
		{"short of room", 0, 400 + 2*barDragRoom + barHeadSlack - 1, false},
		{"room for it floating", 0, 400 + 2*barDragRoom + barHeadSlack, true},
		{"each side keeps its room", 380, barDragRoom, true},
		{"a side runs short", 380, barDragRoom - 1, false},
		// The whole empty part as it left: the room it had, both sides and
		// the header's gaps.
		{"not back at once", 380, 380 + 2*(barDragRoom-1) + 8, false},
		{"back with room", 380, 380 + 2*barDragRoom + barHeadSlack, true},
	} {
		v.headW = st.headW
		if got := v.barInHead(st.empty); got != st.want {
			t.Errorf("%s: %v, want %v", st.name, got, st.want)
		}
	}
	v.pumping = false
	if v.barInHead(10000) {
		t.Error("no picture, no toolbar in the header")
	}
}
