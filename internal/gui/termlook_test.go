package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestTermLookOver: a device's choices win field by field, the default
// fills the rest, and a device with none looks like the default.
func TestTermLookOver(t *testing.T) {
	base := TermLook{Light: "Builtin Solarized Light", Font: "Cascadia Mono", Size: 15, Cursor: "bar"}
	own := TermLook{Light: termKyokai, Size: 12, Blink: "off"}
	got := own.over(base)
	want := TermLook{Light: termKyokai, Font: "Cascadia Mono", Size: 12, Cursor: "bar", Blink: "off"}
	if got != want {
		t.Fatalf("over: %+v, want %+v", got, want)
	}
	if (TermLook{}).over(base) != base {
		t.Fatal("a device with no choices of its own")
	}
	shape, still := got.cursor()
	if shape != terminal.CursorBar || !still {
		t.Fatalf("cursor %v, still %v", shape, still)
	}
	if f := (TermLook{}).font(); f.Size != termSize || f.Family != "" {
		t.Fatalf("default font %+v", f)
	}
}

// TestTermLookPerDevice: a device's look is saved apart from the default,
// and clearing it leaves the device following the default again.
func TestTermLookPerDevice(t *testing.T) {
	a := demoApp(t)
	a.setTermLook("", TermLook{Size: 16})
	a.setTermLook("657365333", TermLook{Dark: "Dracula"})
	if got := a.termLook("657365333"); got.Dark != "Dracula" || got.Size != 16 {
		t.Fatalf("the device's look %+v", got)
	}
	if got := a.termLook("402118905"); got.Dark != "" || got.Size != 16 {
		t.Fatalf("another device's %+v", got)
	}
	a.setTermLook("657365333", TermLook{})
	if _, ok := loadPrefs(a.stateDir).Terminals["657365333"]; ok {
		t.Fatal("a cleared look stayed saved")
	}
}

// TestTermTheme: Kyokai's colors for none and for a name Ghostty does not
// have; a Ghostty theme by its name.
func TestTermTheme(t *testing.T) {
	kyokai := termTheme("", true)
	if kyokai.Cursor != ui.DarkTheme().Accent {
		t.Fatalf("Kyokai's cursor %v", kyokai.Cursor)
	}
	if *termTheme("No Such Theme", true) != *kyokai || *termTheme(termKyokai, true) != *kyokai {
		t.Fatal("an unknown theme is not Kyokai's")
	}
	names := termNames()
	if len(names) < 100 {
		t.Fatalf("%d themes", len(names))
	}
	if th := termTheme(names[0], false); *th == *termTheme("", false) {
		t.Fatalf("%s reads as Kyokai's", names[0])
	}
}

// TestSSHEnvAndKeys: the shell starts in the interface's language; the
// find bar opens with ⌘F or Ctrl+Shift+F, never Ctrl+F (the shell's); a
// program's title loses control characters and is cut.
func TestSSHEnvAndKeys(t *testing.T) {
	for loc, want := range map[string]string{"en": "en_US.UTF-8", "zh-Hans": "zh_CN.UTF-8", "zh-Hant": "zh_TW.UTF-8", "ja": "ja_JP.UTF-8"} {
		i18n.SetLocaleFunc(func() string { return loc })
		if env := sshEnv(); env[0] != [2]string{"LANG", want} {
			t.Errorf("%s: %v", loc, env)
		}
	}
	i18n.SetLocaleFunc(nil)
	find := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		find = ui.Super
	}
	if !findKey(find, ui.KeyF) || findKey(ui.Ctrl, ui.KeyF) || findKey(find, ui.KeyG) {
		t.Fatal("find keys")
	}
	if got := cleanTitle(" vim\x1b]0;x‮ a.txt ", 80); got != "vim]0;x a.txt" {
		t.Fatalf("title %q", got)
	}
	if got := cleanTitle("abcdef", 3); got != "abc…" {
		t.Fatalf("cut %q", got)
	}
}

// TestLookView: a device's look offers the default first, worded by its
// value from Settings, and Use the defaults for all clears the device's
// own choices.
func TestLookView(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := demoApp(t)
	a.setTermLook("", TermLook{Size: 15, Dark: "Dracula"})
	a.setTermLook("657365333", TermLook{Cursor: "bar"})
	s := &serviceTab{app: a, device: "657365333", name: "Studio Mac · 657 365 333", key: "657365333/ssh"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(termLookW).Padding(16).Gap(10).Children(func() { s.lookView(c) })
	}, 420, 1000)
	tt.Frame()
	tt.Frame()
	for _, want := range []string{L.TermLookFor("Studio Mac"), L.TermUseDefault("15"), L.TermCursorBar()} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if dir := os.Getenv("SCREENSHOT_DIR"); dir != "" {
		if f, err := os.Create(filepath.Join(dir, "term-look.png")); err == nil {
			_ = png.Encode(f, tt.Image())
			_ = f.Close()
		}
	}
	// A family with a space is typed whole and saved on Enter, not per key
	// (regression: the saved value was trimmed and the field took it back).
	// The field sits under its title.
	title, ok := tt.Find(L.TermFont())
	if !ok {
		t.Fatal("no font row")
	}
	tt.Press(title.X+20, title.Y+title.H+16)
	tt.Frame()
	if !tt.Focused(L.TermFont()) {
		t.Fatal("the font field did not take the focus")
	}
	tt.Type("Fira Code")
	tt.Frame()
	if got := a.termLook("657365333").Font; got != "" {
		t.Fatalf("saved %q while typing", got)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if got := a.termLook("657365333").Font; got != "Fira Code" {
		t.Fatalf("font after Enter: %q", got)
	}

	if err := tt.Click(L.TermUseDefaults()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.termLook("657365333"); got.Cursor != "" || got.Size != 15 {
		t.Fatalf("after Use the defaults: %+v", got)
	}
}
