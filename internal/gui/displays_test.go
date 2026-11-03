package gui

import (
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// displaysOf is a host's display list of n displays showing current (an
// index), as the host sends it.
func displaysOf(n, current int) hostDisplays {
	var d hostDisplays
	d.List = make([]struct {
		Width   int  `json:"width"`
		Height  int  `json:"height"`
		Primary bool `json:"primary"`
	}, n)
	d.Current = current
	return d
}

// A pane keeps to its display when the host opens another (a new or resumed
// session starts on the primary), and a pane on a display another pane of
// the device shows moves to the first free one.
func TestKeepScreen(t *testing.T) {
	cases := []struct {
		name           string
		screen         int
		n, current     int
		taken          map[int]bool
		ask, keptAfter int
	}{
		{"one display", 2, 1, 0, nil, 0, 2},
		{"no choice, free", 0, 3, 0, nil, 0, 0},
		{"kept, shown", 2, 3, 1, nil, 0, 2},
		{"kept, host on the primary", 2, 3, 0, nil, 2, 2},
		{"kept display gone", 3, 2, 0, nil, 0, 3},
		{"no choice, primary taken", 0, 3, 0, map[int]bool{1: true}, 2, 2},
		{"no choice, first two taken", 0, 3, 0, map[int]bool{1: true, 2: true}, 3, 3},
		{"no choice, all taken", 0, 2, 0, map[int]bool{1: true, 2: true}, 0, 0},
	}
	for _, c := range cases {
		v := &viewer{screen: c.screen}
		if got := v.keepScreen(displaysOf(c.n, c.current), c.taken); got != c.ask || v.screen != c.keptAfter {
			t.Errorf("%s: asked %d kept %d, want %d and %d", c.name, got, v.screen, c.ask, c.keptAfter)
		}
	}
}

// A device's further desktop panes have keys and sessions of their own,
// share the first's settings, and count against the displays offered; a
// NanoKVM pane takes no display.
func TestDesktopPanesPerDisplay(t *testing.T) {
	a := &App{stateDir: t.TempDir(), viewers: map[string]*viewer{}}
	t.Cleanup(func() { _ = a.writePrefs() })
	first := a.newViewer("482916735", localapi.About{}, nil, false, 0)
	first.setCodec("av1")
	first.keepSettings()
	second := a.newViewer("482916735", localapi.About{}, nil, false, 2)
	second.screen = 3
	a.newViewer("482916735", localapi.About{}, []string{localapi.PermUseNanokvm}, true, 0)
	other := a.newViewer("123456789", localapi.About{}, nil, false, 0)
	other.screen = 2
	if first.tabKey() != "482916735" || second.tabKey() != "482916735/desktop/2" {
		t.Fatalf("keys %q %q", first.tabKey(), second.tabKey())
	}
	if second.codec != "av1" {
		t.Errorf("the second pane did not take the device's settings: %q", second.codec)
	}
	if len(a.desktops("482916735")) != 2 || a.viewerFor("482916735") != first {
		t.Fatal("the device's desktop panes")
	}
	first.displays = displaysOf(3, 0)
	if taken := a.takenScreens(second); len(taken) != 1 || !taken[1] {
		t.Errorf("taken for the second pane: %v", taken)
	}
	if taken := a.takenScreens(first); len(taken) != 1 || !taken[3] {
		t.Errorf("taken for the first pane: %v", taken)
	}
	first.sid.Store(7)
	second.sid.Store(9)
	if a.viewerOf(9) != second || a.viewerOf(7) != first || a.viewerOf(8) != nil {
		t.Error("each session's pane")
	}
	second.close()
	if len(a.desktops("482916735")) != 1 || a.viewerFor("482916735") != first {
		t.Error("closing the second pane leaves the first")
	}
}
