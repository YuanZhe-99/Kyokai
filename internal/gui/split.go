package gui

// maxPanes is how many tabs one window shows side by side at most.
const maxPanes = 4

// dropZone is where on a pane a tab was dropped: its middle (in that
// tab's place) or one of its edges (beside it, splitting the pane).
type dropZone int

const (
	zoneMiddle dropZone = iota
	zoneLeft
	zoneRight
	zoneTop
	zoneBottom
)

// pane is a node of a window's split (doc/en-us/gui.md, "Connections"): a
// tab, or two panes side by side (across) or one above the other, the
// first taking ratio percent of the room. Panes change on the main thread
// only; the dividers bind ratio.
type pane struct {
	tab    tab
	across bool
	ratio  float32
	first  *pane
	second *pane
}

// split is which of a window's tabs show where: a tree of panes, each
// split in two as tabs are dropped on their edges, so that one side may
// hold one tab and the other two, and the tab in focus, which takes the
// keyboard and is the window's shown tab. The window holds it under its
// lock.
type split struct {
	root  *pane
	focus tab
}

// shownTabs are the tabs shown, in reading order.
func (s *split) shownTabs() []tab {
	var out []tab
	var walk func(p *pane)
	walk = func(p *pane) {
		switch {
		case p == nil:
		case p.first == nil:
			out = append(out, p.tab)
		default:
			walk(p.first)
			walk(p.second)
		}
	}
	walk(s.root)
	return out
}

// focused is the tab in focus, or nil.
func (s *split) focused() tab { return s.focus }

// topRight is the tab in the split's top-right corner, whose pane's header
// keeps the room of the window's caption buttons: the right half of panes
// side by side, the upper of panes above each other.
func (s *split) topRight() tab {
	p := s.root
	for p != nil && p.first != nil {
		if p.across {
			p = p.second
		} else {
			p = p.first
		}
	}
	if p == nil {
		return nil
	}
	return p.tab
}

// leaf is the pane showing t, and its parent (nil for the root).
func (s *split) leaf(t tab) (p, parent *pane) {
	var find func(p, up *pane) (*pane, *pane)
	find = func(p, up *pane) (*pane, *pane) {
		switch {
		case p == nil:
			return nil, nil
		case p.first == nil:
			if p.tab == t {
				return p, up
			}
			return nil, nil
		}
		if l, u := find(p.first, p); l != nil {
			return l, u
		}
		return find(p.second, p)
	}
	return find(s.root, nil)
}

// place puts t where it was dropped on the pane showing target: the middle
// puts it in target's place; an edge splits that pane in two with t on
// that side. A tab shown elsewhere moves rather than shows twice; with
// maxPanes shown already an edge counts as the middle. t takes the focus.
func (s *split) place(t, target tab, z dropZone) {
	if s.root == nil {
		s.single(t)
		return
	}
	if t == target {
		s.focus = t
		return
	}
	shown := s.shownTabs()
	moving := false
	for _, o := range shown {
		moving = moving || o == t
	}
	if z != zoneMiddle && !moving && len(shown) >= maxPanes {
		z = zoneMiddle
	}
	if moving {
		s.remove(t)
	}
	p, _ := s.leaf(target)
	if p == nil {
		p, _ = s.leaf(s.focus)
	}
	if p == nil {
		s.single(t)
		return
	}
	s.focus = t
	if z == zoneMiddle {
		p.tab = t
		return
	}
	old, added := &pane{tab: p.tab}, &pane{tab: t}
	p.tab, p.ratio = nil, 50
	p.across = z == zoneLeft || z == zoneRight
	if z == zoneLeft || z == zoneTop {
		p.first, p.second = added, old
	} else {
		p.first, p.second = old, added
	}
}

// show brings t forward as a tab chosen in the strip: the focus moves to
// its pane, or it takes the place of the tab in focus.
func (s *split) show(t tab) {
	if p, _ := s.leaf(t); p != nil {
		s.focus = t
		return
	}
	s.place(t, s.focus, zoneMiddle)
}

// single shows t alone.
func (s *split) single(t tab) { *s = split{root: &pane{tab: t}, focus: t} }

// remove takes t's pane out; its sibling takes the room, and the focus
// moves into the sibling when it was on t. Returns: whether t was shown.
func (s *split) remove(t tab) bool {
	p, parent := s.leaf(t)
	if p == nil {
		return false
	}
	if parent == nil {
		*s = split{}
		return true
	}
	sibling := parent.first
	if sibling == p {
		sibling = parent.second
	}
	*parent = *sibling
	if s.focus == t {
		for parent.first != nil {
			parent = parent.first
		}
		s.focus = parent.tab
	}
	return true
}
