package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestTourSteps: the privacy permissions are a step on macOS only; every
// system has the run modes, staying awake and the end, in that order.
func TestTourSteps(t *testing.T) {
	for _, c := range []struct {
		goos string
		want []tourStep
	}{
		{"windows", []tourStep{tourWelcome, tourNetwork, tourConnect, tourLetIn, tourAI, tourAway, tourAwake, tourReady}},
		{"darwin", []tourStep{tourWelcome, tourNetwork, tourConnect, tourLetIn, tourAI, tourPrivacy, tourAway, tourAwake, tourReady}},
	} {
		if got := tourSteps(c.goos); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.goos, got, c.want)
		}
	}
}

// TestFirstRunTour: only a fresh profile gets the tour, until it is
// finished or skipped; a profile from before the tour (gui.json, the
// engine's store or the helper marker there) never does.
func TestFirstRunTour(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"fresh", nil, true},
		{"gui.json from before", map[string]string{"gui.json": `{"theme":"dark"}`}, false},
		{"the engine's store", map[string]string{"secrets.bin": "x"}, false},
		{"moved to the helper", map[string]string{"moved.json": `{"to":"helper"}`}, false},
		{"pending", map[string]string{"gui.json": `{"tour":"pending"}`}, true},
		{"done", map[string]string{"gui.json": `{"tour":"done"}`}, false},
	} {
		dir := t.TempDir()
		for name, body := range c.files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		a := New("", dir, "")
		if got := a.tour != nil; got != c.want {
			t.Errorf("%s: tour %v, want %v", c.name, got, c.want)
		}
		// A fresh profile keeps its pending tour across a restart.
		if c.want && loadPrefs(dir).Tour != tourPending {
			t.Errorf("%s: gui.json says %q", c.name, loadPrefs(dir).Tour)
		}
	}
}

// tourApp is a fresh profile's App on its first run, its engine attached.
func tourApp(t *testing.T) *App {
	a := New("", t.TempDir(), filepath.Join(t.TempDir(), "none.sock"))
	if a.tour == nil {
		t.Fatal("a fresh profile shows no tour")
	}
	a.connected = true
	a.status = &localapi.Status{DeviceID: "482916735", RunMode: "user", Persistence: "os-store", PasswordSerial: 1}
	a.net = netState{loaded: true}
	return a
}

// TestSkipAsksThenStartsWithTheDefaults: Skip asks first, Keep going stays;
// Skip then ends the tour in No privileges with nothing else changed, and
// the tour does not come back.
func TestSkipAsksThenStartsWithTheDefaults(t *testing.T) {
	a := tourApp(t)
	before := a.prefs
	tt := ui.NewTester(a.MainView, 1200, 760)
	tt.Frame()
	if err := tt.Click(L.TourSkip()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(L.TourSkipTitle()) {
		t.Fatal("Skip does not ask first")
	}
	if err := tt.Click(L.TourSkipKeep()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText(L.TourSkipTitle()) || a.tour == nil {
		t.Fatal("Keep going did not keep the tour")
	}
	if err := tt.Click(L.TourSkip()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	keep, _ := tt.Find(L.TourSkipKeep())
	tt.ClickAt(keep.X+keep.W+30, keep.Y+keep.H/2) // the card's Skip, beside Keep going
	tt.Frame()
	if a.tour != nil {
		t.Fatal("Skip did not end the tour")
	}
	if a.currentMode() != modeUser || a.busy["mode"] {
		t.Error("Skip changed the run mode")
	}
	want := before
	want.Tour = tourDone
	if !reflect.DeepEqual(a.prefs, want) {
		t.Errorf("Skip changed the preferences: %+v, want %+v", a.prefs, want)
	}
	if b := New("", a.stateDir, ""); b.tour != nil {
		t.Error("the tour came back after Skip")
	}
}

// TestReplayClosesWithoutChanges: the tour shown again from Settings looks
// at other choices and closes with every setting as it was, back on
// Settings › General.
func TestReplayClosesWithoutChanges(t *testing.T) {
	a := demoApp(t)
	a.net = netState{loaded: true}
	before := a.prefs
	a.openTour()
	tt := ui.NewTester(a.MainView, 1280, 800)
	tt.Frame()
	for i := range len(tourSteps(artPlatform)) - 1 {
		a.tourEdit(func(tr *tour) { tr.direct, tr.ask, tr.mode, tr.kind = true, true, modeBackground, 3 })
		next := L.TourNext()
		if i == 0 {
			next = L.TourStart()
		}
		if err := tt.Click(next); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if !tt.HasText(L.TourSwitchTo(modeTitle(modeBackground))) {
		t.Error("the last step offers no switch to the run mode looked at")
	}
	if err := tt.Click(L.ActionClose()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.tour != nil || !a.settingsOpen || a.page != pageGeneral {
		t.Errorf("Close: tour %v, settings %v at %v", a.tour != nil, a.settingsOpen, a.page)
	}
	if !reflect.DeepEqual(a.prefs, before) || a.busy["mode"] || a.currentMode() != modeUser {
		t.Error("closing the tour shown again changed a setting")
	}
}

// TestTourStillUnderReduceMotion: with Reduce Motion on, a step's
// illustration stops asking for frames once its entrance is over.
func TestTourStillUnderReduceMotion(t *testing.T) {
	for _, still := range []bool{false, true} {
		a := tourApp(t)
		tt := ui.NewTester(a.MainView, 1200, 760)
		tt.SetPreferences(ui.Preferences{ReduceMotion: still})
		for range 5 {
			tt.Advance(time.Second)
			tt.Frame()
		}
		if got := tt.FramePending(); got == still {
			t.Errorf("Reduce Motion %v: a frame pending %v", still, got)
		}
	}
}

// TestTourRenders draws every step of the first run on both systems, each
// language and the dark appearance, and the tour shown again; with
// SCREENSHOT_DIR set it writes PNGs for review.
func TestTourRenders(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	defer func(goos string, own bool) { artPlatform, ownWindowButtons = goos, own }(artPlatform, ownWindowButtons)
	shot := func(tt *ui.Tester, name string) {
		if dir == "" {
			return
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, goos := range []string{"windows", "darwin", "linux"} {
			artPlatform, ownWindowButtons = goos, goos != "darwin"
			for _, dark := range []bool{false, true} {
				if dark && lang != "en" {
					continue
				}
				for i, step := range tourSteps(goos) {
					a := tourApp(t)
					a.status.Permissions = &localapi.Permissions{Screen: true, Keychain: true}
					a.tour.at = i
					switch step {
					case tourNetwork:
						a.net.form = &netForm{}
					case tourAway:
						a.tour.mode = modeFollow
					}
					tt := ui.NewTester(a.MainView, 1200, 760)
					tt.SetDark(dark)
					tt.SetPreferences(ui.Preferences{ReduceMotion: true})
					if goos == "darwin" {
						tt.SetTitleBar(ui.TitleBar{Height: 28, Left: trafficMainX + 54})
					}
					tt.Frame()
					tt.Advance(time.Second)
					tt.Frame()
					suffix := ""
					if dark {
						suffix = "-dark"
					}
					shot(tt, lang+"-tour-"+goos+"-"+strconv.Itoa(i+1)+suffix)
				}
			}
		}
		if lang == "en" {
			a := demoApp(t)
			a.openTour()
			a.tour.at = len(tourSteps(artPlatform)) - 1
			a.tour.mode = modeBackground
			tt := ui.NewTester(a.MainView, 1280, 800)
			tt.Frame()
			shot(tt, "en-tour-replay")
		}
	}
	i18n.SetLocaleFunc(nil)
}
