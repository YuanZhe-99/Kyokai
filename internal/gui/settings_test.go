package gui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestSettingsHitsShow: every setting search finds shows on its page, so
// that a hit always leads somewhere (doc/en-us/gui.md, "Settings").
func TestSettingsHitsShow(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	pages := map[page]*ui.Tester{}
	for _, h := range settingsHits() {
		tt, ok := pages[h.page]
		if !ok {
			a := demoApp(t)
			a.page = h.page
			// What only shows once loaded, or switched on — and macOS's
			// privacy permissions, which its core always reports (search
			// finds them there only; regression: never set, Access hid them
			// on a Mac).
			a.status.WithoutAsking = true
			a.status.Permissions = &localapi.Permissions{}
			a.net.loaded = true
			a.ai.loaded, a.ai.settings = true, demoAISettings(true)
			tt = ui.NewTester(a.View, 1000, 4000)
			tt.Frame()
			pages[h.page] = tt
		}
		if !tt.HasText(h.title) {
			t.Errorf("%s: %q is not on its page", pageName(h.page), h.title)
		}
	}
}

// TestSettingsSearchLeadsThere: words in the sheet's search list the
// settings they find in place of its sections; choosing one opens its page,
// scrolled to it once and tinted.
func TestSettingsSearchLeadsThere(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	a.net.loaded = true
	a.settingsOpen, a.page, a.settingsQuery = true, pageGeneral, "latency"
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	if tt.HasText(L.NavAccess()) {
		t.Fatal("the sections show beside the settings found")
	}
	if err := tt.Click(L.NetworkLatencyFirst()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.page != pageNetwork || a.settingsFocus.title != L.NetworkLatencyFirst() || !a.settingsFocus.shown {
		t.Fatalf("page %v, focus %+v", a.page, a.settingsFocus)
	}
	if hit, scroll := a.focused(L.NetworkLatencyFirst()); !hit || scroll {
		t.Fatalf("tinted %v, scrolled again %v", hit, scroll)
	}
}

// TestSearchSettings: every word typed must be in a setting's title or its
// words, case aside; titles that match come first.
func TestSearchSettings(t *testing.T) {
	all := []settingsHit{
		{pageGeneral, "Language", "Follows the system"},
		{pageAccess, "Temporary password", "Shown on Home"},
		{pageAccess, "Permanent password", ""},
		{pageNetwork, "Networks", "Add a network"},
		{pageGeneral, "Home screen", ""},
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"password", []string{"Temporary password", "Permanent password"}},
		{"PERM  pass", []string{"Permanent password"}},
		{"add", []string{"Networks"}},
		{"system", []string{"Language"}},
		{"home", []string{"Home screen", "Temporary password"}},
		{"password home", []string{"Temporary password"}},
		{"nothing", nil},
	} {
		var got []string
		for _, h := range searchSettings(all, tc.q) {
			got = append(got, h.title)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.q, got, tc.want)
		}
	}
}
