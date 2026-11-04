package gui

import (
	"image"
	"slices"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"

	"github.com/egoist/mygo/ui"
)

// TestPaneDropZones: a pane of the group only swaps places; a pane from
// elsewhere goes beside on each side that fits, or only in a pane's place
// while the group shows maxPanes; a maximized group's other panes still
// count as its own.
func TestPaneDropZones(t *testing.T) {
	a, b, c, d, out := fakeTab("a"), fakeTab("b"), fakeTab("c"), fakeTab("d"), fakeTab("out")
	group2 := func() group {
		var g group
		g.split.single(a)
		g.split.place(b, a, zoneRight)
		return g
	}
	full := group2()
	full.split.place(c, a, zoneBottom)
	full.split.place(d, b, zoneBottom)
	zoomed := group2()
	zoomed.zoom = a
	for _, tc := range []struct {
		name  string
		g     group
		from  tab
		zones []dropZone
		here  string
	}{
		{"own group", group2(), b, []dropZone{zoneMiddle}, L.DropSwap()},
		{"maximized own group", zoomed, b, []dropZone{zoneMiddle}, L.DropSwap()},
		{"from elsewhere", group2(), out, []dropZone{zoneLeft, zoneRight, zoneTop, zoneBottom, zoneMiddle}, L.DropHere()},
		{"full group", full, out, []dropZone{zoneMiddle}, L.DropHere()},
	} {
		zones, here, _ := paneDraw{g: tc.g, from: tc.from}.zones(a)
		var got []dropZone
		for _, z := range zones {
			got = append(got, z.zone)
		}
		if !slices.Equal(got, tc.zones) || here != tc.here {
			t.Errorf("%s: zones %v named %q, want %v named %q", tc.name, got, here, tc.zones, tc.here)
		}
	}
}

// TestPaneDropInTheWindow: in a real frame, a pane of the group dropped on
// the pane beside it swaps the two (regression: the drop changed the panes
// halfway through the frame, which drew a desktop twice and crashed), and a
// pane from another entry goes beside a pane on the side it was dropped.
func TestPaneDropInTheWindow(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	a := &App{viewers: map[string]*viewer{}, services: map[string]*serviceTab{}, busy: map[string]bool{}}
	a.connected = true
	a.status = &localapi.Status{DeviceID: "482916735"}
	a.home = &homeTab{app: a}
	vw := &viewerWindow{app: a, main: true}
	a.main = vw
	vw.homeGroupLocked()
	var tabs []tab
	for i, d := range []struct{ id, title string }{{"213559601", "Alpha"}, {"657365333", "Beta"}, {"402118905", "Gamma"}} {
		v := &viewer{app: a, device: d.id, title: d.title, preset: 1, fpsCap: "30 fps",
			start: videoStart{Codec: "vp9-sw", Width: 320, Height: 180}, bm: ui.NewBitmap(pic), pumping: true,
			phase: localapi.SessionInfo{ID: uint64(i + 1), DeviceID: d.id, State: "connected"}}
		a.viewers[d.id] = v
		tabs = append(tabs, v)
		vw.tabs = append(vw.tabs, v)
		vw.placeLocked(v, nil, zoneMiddle)
		v.setTabWindow(vw)
	}
	alpha, beta, gamma := tabs[0], tabs[1], tabs[2]
	vw.placeLocked(beta, alpha, zoneRight)
	tt := ui.NewTester(vw.view, 2600, 900)
	tt.Frame()
	tt.Frame()
	// drag drags a pane's sidebar row over the panes and drops it on the
	// first zone named zone.
	drag := func(title, zone string) {
		t.Helper()
		r, ok := tt.Find(title)
		if !ok {
			t.Fatalf("no row %q", title)
		}
		tt.Move(r.X+10, r.Y+r.H/2)
		tt.Press(r.X+10, r.Y+r.H/2)
		for _, x := range []float32{r.X + 40, 500, 700} {
			tt.Move(x, 450)
			tt.Frame()
		}
		z, ok := tt.Find(zone)
		if !ok {
			tt.Release(700, 450)
			t.Fatalf("dragging %s: no zone %q in %v", title, zone, tt.Texts())
		}
		tt.Move(z.X+z.W/2, z.Y+z.H/2)
		tt.Frame()
		tt.Release(z.X+z.W/2, z.Y+z.H/2)
		tt.Frame()
		tt.Frame()
	}
	drag("Beta", L.DropSwap())
	if got := vw.cur.split.shownTabs(); !slices.Equal(got, []tab{beta, alpha}) || len(vw.groups) != 3 {
		t.Fatalf("Beta dropped on Alpha: shows %v in %d entries, want Beta, Alpha in 3", got, len(vw.groups))
	}
	if tt.HasText(L.DropLeft()) {
		t.Errorf("a pane of the group offers to go beside another")
	}
	drag("Gamma", L.DropLeft())
	if got := vw.cur.split.shownTabs(); !slices.Equal(got, []tab{gamma, beta, alpha}) {
		t.Fatalf("Gamma dropped left of Beta: shows %v", got)
	}
}
