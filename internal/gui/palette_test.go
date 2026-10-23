package gui

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestSearchPalette: every word typed must be in a result's title, the line
// under it or its more words, case aside; titles that match come first in
// their group, groups keep their order and show six at most; with nothing
// typed, everything but the settings (doc/en-us/gui.md, "Windows").
func TestSearchPalette(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	set := L.NavSettings()
	all := []paletteItem{
		{group: "This device", title: "Temporary password", sub: "••••••••"},
		{group: "This device", title: "Device ID", sub: "482 916 735"},
		{group: "Devices", title: "Office", sub: "Mac · 657 365 333"},
		{group: "Devices", title: "Mac mini", sub: "402 118 905 · Connected"},
		{group: "Actions", title: "Settings"},
		{group: set, title: "Permanent password", sub: "Settings › Access"},
		{group: set, title: "Language", sub: "Settings › General", words: "Follows the system"},
	}
	for i := range 8 {
		all = append(all, paletteItem{group: "Devices", title: fmt.Sprintf("PC %d", i)})
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"", []string{"Temporary password", "Device ID", "Office", "Mac mini", "PC 0", "PC 1", "PC 2", "PC 3", "Settings"}},
		{"password", []string{"Temporary password", "Permanent password"}},
		{"PASS temp", []string{"Temporary password"}},
		{"mac", []string{"Mac mini", "Office"}},
		{"connected", []string{"Mac mini"}},
		{"system", []string{"Language"}},
		{"settings", []string{"Settings", "Permanent password", "Language"}},
		{"pc", []string{"PC 0", "PC 1", "PC 2", "PC 3", "PC 4", "PC 5"}},
		{"nothing like it", nil},
	} {
		var got []string
		for _, it := range searchPalette(all, c.q) {
			got = append(got, it.title)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.q, got, c.want)
		}
	}
}

// TestPaletteByKeyboard: Ctrl+K (⌘K) opens search with its field taking
// the keys, and closes it; ↑ and ↓ choose around the ends; Enter opens what
// is chosen, a setting on its page; Escape closes it, and a picture under
// it takes no keys meanwhile.
func TestPaletteByKeyboard(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if !a.palette.open || !a.covered() {
		t.Fatalf("Ctrl+K: %+v", a.palette)
	}
	n := len(searchPalette(a.paletteItems(), ""))
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if a.palette.sel != n-1 {
		t.Fatalf("↑ from the first: %d, want %d", a.palette.sel, n-1)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.palette.sel != 0 {
		t.Fatalf("↓ from the last: %d", a.palette.sel)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if a.palette.open {
		t.Fatal("Ctrl+K again left it open")
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.palette.open {
		t.Fatal("Escape left it open")
	}
	a.net.loaded = true
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Type("latency")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.palette.open || !a.settingsOpen || a.page != pageNetwork || a.settingsFocus.title != L.NetworkLatencyFirst() {
		t.Fatalf("palette %+v; Settings %v at %v, focus %+v", a.palette, a.settingsOpen, a.page, a.settingsFocus)
	}
}

// TestPaletteLeavesKeysAway: while a desktop's picture has the keyboard,
// Ctrl+K goes to the other computer, not to search (regression: a window
// shortcut takes a key before the picture, and Ctrl+K in a shell there
// would have opened the palette).
func TestPaletteLeavesKeysAway(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	v := &viewer{app: a, device: "657365333", title: "Studio Mac", preset: 1, fpsCap: "30 fps",
		start: videoStart{Codec: "vp9-sw", Width: 320, Height: 180}, bm: ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 320, 180))),
		phase: localapi.SessionInfo{ID: 1, DeviceID: "657365333", State: "connected"}, pumping: true}
	a.viewers[v.device] = v
	a.main.tabs = append(a.main.tabs, v)
	a.main.placeLocked(v, nil, zoneMiddle)
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	v.pic.Focus()
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if a.palette.open {
		t.Fatal("Ctrl+K on the picture opened search")
	}
}
