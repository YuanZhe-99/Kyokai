package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestPickRows: the + picker lists what opens the kind chosen — a device
// whose status says otherwise greyed with the reason, one without status
// not; This computer first for an AI task; the devices of my networks only
// for the desktop — and what is typed: a name, a Device ID with or without
// spaces, a network, a Device ID nobody has, an address (doc/en-us/gui.md,
// "Windows").
func TestPickRows(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	rows := []deviceRow{
		{id: "657365333", about: localapi.About{Name: "Studio Mac"}, features: []string{"tunnel"}, routes: []localapi.Route{{Address: "192.168.1.20"}}},
		{id: "402118905", about: localapi.About{Name: "NUC"}, features: []string{"tunnel"}},
		{id: "647222040"},
	}
	watched := map[string]*localapi.SystemSample{
		"657365333": {Services: []string{serviceSSH, serviceRDP}, Offers: []string{localapi.PermUseSSH, localapi.PermRunAITask}, AI: true},
		"402118905": {Services: []string{serviceSSH}, AI: true},
	}
	others := []networkOther{{id: "100000002", network: "lab", path: localapi.NetworkDevice{Path: "relay", LatencyMS: 40}}}
	no := "!" // a row greyed: it does not open the kind
	for _, c := range []struct {
		kind, query string
		want        []string // keys, no+key for a row greyed
	}{
		{"", "", []string{"657365333", "402118905", "647222040", "100000002"}},
		{serviceSSH, "", []string{"657365333", no + "402118905", "647222040"}},
		{serviceRDP, "", []string{no + "657365333", no + "402118905", "647222040"}},
		{serviceVNC, "", []string{no + "657365333", no + "402118905", "647222040"}},
		{actionAI, "", []string{selfID, "657365333", no + "402118905", "647222040"}},
		{actionAI, "this", []string{selfID}},
		{"", "studio", []string{"657365333"}},
		{"", "657 365", []string{"657365333"}},
		{"", "657365333", []string{"657365333"}},
		{"", "lab", []string{"100000002"}},
		{serviceSSH, "100 000 002", []string{"100000002"}}, // not a peer here: typed
		{"", "123456789", []string{"123456789"}},
		{"", "192.168.1.20", []string{"192.168.1.20", "657365333"}},
		{"", "nuc.local", []string{"nuc.local"}},
		{"", "nuc", []string{"402118905"}},
		{"", "kitchen", []string{"kitchen"}},
		{"", "living room", nil},
	} {
		var got []string
		for _, r := range pickRows(c.kind, c.query, false, rows, watched, nil, others, ui.Color{}, ui.Color{}, ui.Color{}) {
			k := r.key
			if !r.ok {
				k = no + k
				if want := L.PickNotOffered(kindName(c.kind)); r.note != want {
					t.Errorf("%q %q: %s says %q, want %q", c.kind, c.query, r.key, r.note, want)
				}
			}
			got = append(got, k)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q %q: %v, want %v", c.kind, c.query, got, c.want)
		}
	}
	typed := pickRows("", "nuc.local", false, nil, nil, nil, nil, ui.Color{}, ui.Color{}, ui.Color{})
	if typed[0].address != "nuc.local" || typed[0].device != "" || typed[0].note != L.PickByAddress() {
		t.Errorf("an address typed: %+v", typed[0])
	}
}

// TestNextRow: ↓ and ↑ choose the next device that opens the kind, around
// the ends, from none chosen too.
func TestNextRow(t *testing.T) {
	rows := []pickRow{{ok: false}, {ok: true}, {ok: false}, {ok: true}}
	for _, c := range []struct{ from, step, want int }{
		{-1, 1, 1}, {1, 1, 3}, {3, 1, 1}, {-1, -1, 3}, {1, -1, 3}, {3, -1, 1},
	} {
		if got := nextRow(rows, c.from, c.step); got != c.want {
			t.Errorf("from %d by %d: %d, want %d", c.from, c.step, got, c.want)
		}
	}
	if got := nextRow([]pickRow{{}, {}}, -1, 1); got != -1 {
		t.Errorf("none opens the kind: %d", got)
	}
	if got := nextRow(nil, -1, -1); got != -1 {
		t.Errorf("no rows: %d", got)
	}
}

// TestPickerByKeyboard: the picker opens from search, takes the keys typed
// at once, ↓ chooses a device, Escape closes it; for an AI task ↓ chooses
// This computer first.
func TestPickerByKeyboard(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	a.status.Recent = []localapi.RecentDevice{
		{DeviceID: "657365333", Last: 1797400000, About: localapi.About{Name: "Studio Mac"}},
		{DeviceID: "402118905", Last: 1797300000, About: localapi.About{Name: "NUC"}},
	}
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Type(strings.ToLower(L.SidebarNew()))
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if a.palette.open || a.picker.at != "sidebar" {
		t.Fatalf("palette %+v, picker %+v", a.palette, a.picker)
	}
	if !tt.HasText("Studio Mac") || !tt.HasText("NUC") {
		t.Fatalf("the devices are not listed: %q", tt.Texts())
	}
	tt.Type("nu")
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.picker.query != "nu" || a.picker.chosen != "402118905" {
		t.Fatalf("picker %+v", a.picker)
	}
	if !a.covered() {
		t.Fatal("a picture would take the keys under the picker")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.picker.at != "" {
		t.Fatalf("Escape left the picker open: %+v", a.picker)
	}
	a.openPicker("sidebar", false, "", zoneMiddle)
	a.picker.kind = actionAI
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.picker.chosen != selfID || !tt.HasText(L.PickStartAI()) {
		t.Fatalf("for an AI task ↓ chose %q", a.picker.chosen)
	}
}

// TestPickerKindKeepsTheKeys: after a kind is clicked, what is typed still
// goes to the search, and ↓ still chooses a device.
func TestPickerKindKeepsTheKeys(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	a.status.Recent = []localapi.RecentDevice{{DeviceID: "402118905", Last: 1797300000, About: localapi.About{Name: "NUC"}}}
	a.openPicker("sidebar", false, "", zoneMiddle)
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	tt.Frame()
	search, ok := tt.Find(L.PickSearch())
	if !ok {
		t.Fatalf("no search: %q", tt.Texts())
	}
	// The kinds stand in a row of five above the search's row, as wide as it.
	tt.ClickAt(search.X+search.W/5, search.Y-40)
	tt.Frame()
	tt.Frame()
	if a.picker.at != "sidebar" || a.picker.kind == "" {
		t.Fatalf("the click chose no kind: %+v", a.picker)
	}
	tt.Type("nu")
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.picker.query != "nu" || a.picker.chosen != "402118905" {
		t.Fatalf("the keys did not reach the search: %+v", a.picker)
	}
}

// A NanoKVM pane is offered where the host offers its NanoKVM USB and the
// saved sign-in grants it, tunnels or not.
func TestKindOfferedNanokvm(t *testing.T) {
	for _, c := range []struct {
		sample *localapi.SystemSample
		want   bool
	}{
		{&localapi.SystemSample{Nanokvm: true, Offers: []string{localapi.PermUseNanokvm}}, true},
		{&localapi.SystemSample{Nanokvm: true, Offers: []string{localapi.PermViewScreen}}, false},
		{&localapi.SystemSample{Offers: []string{localapi.PermUseNanokvm}}, false},
		{nil, true},
	} {
		if got := kindOffered(serviceNanokvm, c.sample, false); got != c.want {
			t.Errorf("%+v: %v, want %v", c.sample, got, c.want)
		}
	}
}
