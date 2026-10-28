package gui

import (
	"testing"
	"time"
)

// TestAutoInstallNow: an automatic install starts only with a found update,
// nothing live, and the chosen moment come.
func TestAutoInstallNow(t *testing.T) {
	at3 := time.Date(2026, 10, 28, 3, 40, 0, 0, time.Local)
	at4 := at3.Add(time.Hour)
	cases := []struct {
		name string
		f    autoFacts
		want bool
	}{
		{"off", autoFacts{found: true, idle: time.Hour, now: at3, hour: 3}, false},
		{"idle long enough", autoFacts{mode: autoIdle, found: true, idle: autoIdleAfter}, true},
		{"idle too short", autoFacts{mode: autoIdle, found: true, idle: autoIdleAfter - time.Second}, false},
		{"idle but live", autoFacts{mode: autoIdle, found: true, idle: time.Hour, live: true}, false},
		{"idle, nothing found", autoFacts{mode: autoIdle, idle: time.Hour}, false},
		{"daily in the hour", autoFacts{mode: autoDaily, hour: 3, now: at3, found: true}, true},
		{"daily after the hour", autoFacts{mode: autoDaily, hour: 3, now: at4, found: true}, false},
		{"daily at midnight", autoFacts{mode: autoDaily, hour: 0, now: at3.Add(-3*time.Hour - 40*time.Minute), found: true}, true},
		{"daily but live", autoFacts{mode: autoDaily, hour: 3, now: at3, found: true, live: true}, false},
		{"daily, failed before", autoFacts{mode: autoDaily, hour: 3, now: at3}, false},
	}
	for _, c := range cases {
		if got := autoInstallNow(c.f); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAutoInstallHourKept: midnight (hour 0) survives a save and load, and
// a missing hour means 03:00.
func TestAutoInstallHourKept(t *testing.T) {
	dir := t.TempDir()
	if p := loadPrefs(dir); p.AutoInstallHour != 3 {
		t.Fatalf("default hour %d, want 3", p.AutoInstallHour)
	}
	p := defaultPrefs()
	p.AutoInstall, p.AutoInstallHour = autoDaily, 0
	if err := savePrefs(dir, p); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(dir); got.AutoInstall != autoDaily || got.AutoInstallHour != 0 {
		t.Fatalf("loaded %q at %d, want daily at 0", got.AutoInstall, got.AutoInstallHour)
	}
}
