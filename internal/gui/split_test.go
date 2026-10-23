package gui

import (
	"slices"
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

// names lists a split's places by tab name, "-" for an empty one, and the
// place in focus.
func names(s split) ([]string, int) {
	var out []string
	for _, t := range s.panes[:s.layout.panes()] {
		if t == nil {
			out = append(out, "-")
		} else {
			out = append(out, t.tabKey())
		}
	}
	return out, s.focus
}

// TestSplitPlaces: a tab dropped on an edge or corner splits the window
// there, the tabs shown fill the other places in order, and closing tabs
// shrinks the layout back without leaving the focus on an empty place.
func TestSplitPlaces(t *testing.T) {
	a, b, c, d, e := fakeTab("a"), fakeTab("b"), fakeTab("c"), fakeTab("d"), fakeTab("e")
	steps := []struct {
		name  string
		do    func(s *split)
		want  []string
		focus int
	}{
		{"one tab", func(s *split) { s.single(a) }, []string{"a"}, 0},
		{"b on the right", func(s *split) { s.place(b, zoneRight) }, []string{"a", "b"}, 1},
		{"c on the left keeps a", func(s *split) { s.place(c, zoneLeft) }, []string{"c", "a"}, 0},
		{"d in the bottom right corner", func(s *split) { s.place(d, zoneBottomRight) }, []string{"c", "a", "-", "d"}, 3},
		{"e in the middle replaces the focus", func(s *split) { s.place(e, zoneMiddle) }, []string{"c", "a", "-", "e"}, 3},
		{"a chosen in the strip takes the focus", func(s *split) { s.show(a) }, []string{"c", "a", "-", "e"}, 1},
		{"b chosen replaces the tab in focus", func(s *split) { s.show(b) }, []string{"c", "b", "-", "e"}, 1},
		{"closing b leaves two side by side", func(s *split) { s.remove(b) }, []string{"c", "e"}, 1},
		{"a on top moves to rows", func(s *split) { s.place(a, zoneTop) }, []string{"a", "c"}, 0},
		{"a dropped again elsewhere moves, not copies", func(s *split) { s.place(a, zoneBottom) }, []string{"c", "a"}, 1},
		{"closing c leaves one", func(s *split) { s.remove(c) }, []string{"a"}, 0},
		{"closing the last leaves none", func(s *split) { s.remove(a) }, []string{"-"}, 0},
	}
	var s split
	for _, st := range steps {
		st.do(&s)
		got, focus := names(s)
		if !slices.Equal(got, st.want) || focus != st.focus {
			t.Fatalf("%s: places %v focus %d, want %v focus %d", st.name, got, focus, st.want, st.focus)
		}
	}
	if s.remove(e) {
		t.Error("a tab not shown was removed")
	}
}
