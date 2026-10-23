package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeTab is a tab that only has a name.
type fakeTab string

func (f fakeTab) tabKey() string             { return string(f) }
func (f fakeTab) tabTitle() string           { return string(f) }
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
