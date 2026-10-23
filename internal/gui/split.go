package gui

import "slices"

// paneLayout is how a window splits its picture area among its tabs
// (doc/en-us/gui.md, "Connections"): one tab, two side by side, two one
// above the other, or four in the corners.
type paneLayout int

const (
	layoutOne paneLayout = iota
	layoutColumns
	layoutRows
	layoutGrid
)

// panes counts the places of a layout.
func (l paneLayout) panes() int {
	switch l {
	case layoutColumns, layoutRows:
		return 2
	case layoutGrid:
		return 4
	default:
		return 1
	}
}

// dropZone is where in a window's picture area a tab was dropped: its
// middle (the pane in focus), one of its edges or one of its corners.
type dropZone int

const (
	zoneMiddle dropZone = iota
	zoneLeft
	zoneRight
	zoneTop
	zoneBottom
	zoneTopLeft
	zoneTopRight
	zoneBottomLeft
	zoneBottomRight
)

// split is which of a window's tabs show where: a layout, the tab in each
// of its places (nil for an empty one) and the place in focus, whose tab
// takes the keyboard and is the window's shown tab. Its methods are pure;
// the window holds it under its lock.
type split struct {
	layout paneLayout
	panes  [4]tab
	focus  int
}

// shownTabs are the tabs in the layout's places, in order, without the
// empty ones.
func (s *split) shownTabs() []tab {
	var out []tab
	for _, t := range s.panes[:s.layout.panes()] {
		if t != nil {
			out = append(out, t)
		}
	}
	return out
}

// focused is the tab in focus, or nil.
func (s *split) focused() tab { return s.panes[s.focus] }

// place puts t where it was dropped: the middle replaces the tab in focus;
// an edge splits the area in two with t on that side; a corner splits it in
// four with t in that corner. The other tabs shown keep their order in the
// remaining places, as many as fit. t takes the focus.
func (s *split) place(t tab, z dropZone) {
	others := slices.DeleteFunc(s.shownTabs(), func(o tab) bool { return o == t })
	if z == zoneMiddle {
		if i := slices.Index(s.panes[:s.layout.panes()], t); i >= 0 {
			s.focus = i
			return
		}
		s.panes[s.focus] = t
		return
	}
	layout, at := layoutGrid, int(z-zoneTopLeft)
	switch z {
	case zoneLeft, zoneRight:
		layout, at = layoutColumns, int(z-zoneLeft)
	case zoneTop, zoneBottom:
		layout, at = layoutRows, int(z-zoneTop)
	}
	s.layout, s.focus, s.panes = layout, at, [4]tab{}
	s.panes[at] = t
	for i := range layout.panes() {
		if i != at && len(others) > 0 {
			s.panes[i], others = others[0], others[1:]
		}
	}
}

// show brings t forward as a tab chosen in the strip: the focus moves to
// its place, or it replaces the tab in focus.
func (s *split) show(t tab) { s.place(t, zoneMiddle) }

// single shows t alone.
func (s *split) single(t tab) {
	*s = split{layout: layoutOne}
	s.panes[0] = t
}

// remove takes t out of its place; the layout shrinks to fit the tabs left
// (four places stay while three tabs show, one empty), and the focus stays
// on a tab. Returns: whether t was shown.
func (s *split) remove(t tab) bool {
	i := slices.Index(s.panes[:s.layout.panes()], t)
	if i < 0 {
		return false
	}
	s.panes[i] = nil
	left := s.shownTabs()
	switch {
	case len(left) <= 1:
		s.layout, s.panes, s.focus = layoutOne, [4]tab{}, 0
		if len(left) == 1 {
			s.panes[0] = left[0]
		}
		return true
	case len(left) == 2 && s.layout == layoutGrid:
		s.layout, s.panes = layoutColumns, [4]tab{left[0], left[1]}
	}
	if s.panes[s.focus] == nil || s.focus >= s.layout.panes() {
		s.focus = slices.IndexFunc(s.panes[:s.layout.panes()], func(o tab) bool { return o != nil })
	}
	return true
}
