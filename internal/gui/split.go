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

// topLeft is the tab in the split's top-left corner, whose pane's header
// holds the sidebar's button while the sidebar is hidden: the first half
// all the way down.
func (s *split) topLeft() tab {
	p := s.root
	for p != nil && p.first != nil {
		p = p.first
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

// size is a width and a height in DIPs.
type size struct{ w, h float32 }

// paneRoom is the least room of a tab's pane in a group: paneMinW ×
// paneMinH inside its margin (doc/en-us/gui.md, "Windows").
var paneRoom = size{paneMinW + paneMargin, paneMinH + paneMargin}

// minRoom is the least room the pane takes: a tab's paneRoom; the halves of
// a split beside or above each other, with the divider's line between.
func (p *pane) minRoom() size {
	switch {
	case p == nil:
		return size{}
	case p.first == nil:
		return paneRoom
	}
	a, b := p.first.minRoom(), p.second.minRoom()
	if p.across {
		return size{a.w + b.w + 1, max(a.h, b.h)}
	}
	return size{max(a.w, b.w), a.h + b.h + 1}
}

// keepRoom is a split's ratio held where its first half keeps lo and its
// second hi of total, the room both share with the divider's line: MyGo's
// split keeps no minimum of its own beyond a few DIPs.
// Returns: ratio unchanged while total is not known yet or cannot hold
// both (the group then comes apart, fitFor).
func keepRoom(ratio, total, lo, hi float32) float32 {
	room := total - 1
	if room <= 0 || lo+hi > room {
		return ratio
	}
	return min(max(ratio/100*room, lo), room-hi) * 100 / room
}

// halves are the rooms of a split's two halves when it has room, as MyGo
// lays them out: the line takes one DIP, the halves share the rest by the
// ratio as keepRoom holds it.
func (p *pane) halves(room size) (a, b size) {
	am, bm := p.first.minRoom(), p.second.minRoom()
	if p.across {
		w := keepRoom(p.ratio, room.w, am.w, bm.w) / 100 * max(room.w-1, 0)
		return size{w, room.h}, size{max(room.w-1-w, 0), room.h}
	}
	h := keepRoom(p.ratio, room.h, am.h, bm.h) / 100 * max(room.h-1, 0)
	return size{room.w, h}, size{room.w, max(room.h-1-h, 0)}
}

// roomOf is the room of t's pane when the split has room, and whether t is
// in it.
func (s *split) roomOf(t tab, room size) (size, bool) {
	var find func(p *pane, room size) (size, bool)
	find = func(p *pane, room size) (size, bool) {
		switch {
		case p == nil:
			return size{}, false
		case p.first == nil:
			return room, p.tab == t
		}
		a, b := p.halves(room)
		if r, ok := find(p.first, a); ok {
			return r, true
		}
		return find(p.second, b)
	}
	return find(s.root, room)
}

// fits reports whether a pane with room may split on z's side, both halves
// keeping paneRoom; the middle always fits.
func fits(room size, z dropZone) bool {
	switch z {
	case zoneLeft, zoneRight:
		return room.w >= 2*paneRoom.w+1
	case zoneTop, zoneBottom:
		return room.h >= 2*paneRoom.h+1
	}
	return true
}

// zoneRect is a drop zone's place on a pane, in percent of the pane.
type zoneRect struct {
	zone       dropZone
	x, y, w, h float32
}

// dropZonesFor are where on a pane a dragged pane may go, by whether the
// pane has room to split side by side and above and below: beside it on
// each side that fits, else in its place, which takes the room of the
// sides that do not fit.
func dropZonesFor(across, down bool) []zoneRect {
	switch {
	case across && down:
		return []zoneRect{
			{zoneLeft, 0, 0, 25, 100}, {zoneRight, 75, 0, 25, 100},
			{zoneTop, 25, 0, 50, 25}, {zoneBottom, 25, 75, 50, 25}, {zoneMiddle, 25, 25, 50, 50},
		}
	case across:
		return []zoneRect{{zoneLeft, 0, 0, 25, 100}, {zoneRight, 75, 0, 25, 100}, {zoneMiddle, 25, 0, 50, 100}}
	case down:
		return []zoneRect{{zoneTop, 0, 0, 100, 25}, {zoneBottom, 0, 75, 100, 25}, {zoneMiddle, 0, 25, 100, 50}}
	}
	return []zoneRect{{zoneMiddle, 0, 0, 100, 100}}
}

// fit is what the main window does to hold the group it shows.
type fit int

const (
	// fitKeep: the window holds the group.
	fitKeep fit = iota
	// fitGrow: it grows on its display, which holds the group.
	fitGrow
	// fitApart: the display cannot hold it, so the group comes apart.
	fitApart
)

// fitFor is what a window of size win on a display whose work area is work
// does to hold need, the group's window minimum (windowNeed).
func fitFor(need, win, work size) fit {
	switch {
	case need.w > work.w || need.h > work.h:
		return fitApart
	case need.w > win.w || need.h > win.h:
		return fitGrow
	}
	return fitKeep
}
