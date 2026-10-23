package gui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestSendsOnEnter: Enter sends and Shift+Enter breaks the line by
// default, the reverse when chosen, and neither sends with the button only
// (doc/en-us/gui.md, "AI sessions").
func TestSendsOnEnter(t *testing.T) {
	for _, c := range []struct {
		pref string
		mods ui.Modifiers
		want bool
	}{
		{"", 0, true}, {"", ui.Shift, false}, {"", ui.Ctrl, false},
		{"shift-enter", 0, false}, {"shift-enter", ui.Shift, true},
		{"button", 0, false}, {"button", ui.Shift, false},
	} {
		if got := sendsOnEnter(c.pref, c.mods); got != c.want {
			t.Errorf("%q with %v: %v, want %v", c.pref, c.mods, got, c.want)
		}
	}
}

// TestAISendHint: the box says what sends it, as the preference has it;
// nothing while the task works or when only Send sends.
func TestAISendHint(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	for _, c := range []struct {
		pref string
		mode boxMode
		want string
	}{
		{"", boxStart, L.AIHintStart()}, {"", boxReply, L.AIHintSend()},
		{"shift-enter", boxStart, L.AIHintStartShift()}, {"shift-enter", boxReply, L.AIHintSendShift()},
		{"button", boxStart, ""}, {"", boxWorking, ""},
	} {
		if got := aiSendHint(c.pref, c.mode); got != c.want {
			t.Errorf("%q in %d: %q, want %q", c.pref, c.mode, got, c.want)
		}
	}
}

// TestAIWhen: a recent task's time is the time today, "yesterday", else
// the date.
func TestAIWhen(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	now := time.Date(2026, 10, 23, 15, 4, 0, 0, time.Local)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 23, 9, 30, 0, 0, time.Local), "09:30"},
		{time.Date(2026, 10, 23, 0, 0, 0, 0, time.Local), "00:00"},
		{time.Date(2026, 10, 22, 23, 59, 0, 0, time.Local), L.AIYesterday()},
		{time.Date(2026, 10, 22, 0, 0, 0, 0, time.Local), L.AIYesterday()},
		{time.Date(2026, 10, 21, 23, 59, 0, 0, time.Local), "10-21"},
	} {
		if got := aiWhen(c.at.Unix(), now); got != c.want {
			t.Errorf("%v: %q, want %q", c.at, got, c.want)
		}
	}
}

// TestAIBinds: a pane waiting for its first instruction's task on a host
// takes a task other than the one the host had, with that instruction.
func TestAIBinds(t *testing.T) {
	task := &localapi.AITask{ID: 8, Instruction: "Open Notepad"}
	for _, c := range []struct {
		name string
		task *localapi.AITask
		sent string
		prev uint64
		want bool
	}{
		{"the new task", task, "Open Notepad", 7, true},
		{"the task it had", task, "Open Notepad", 8, false},
		{"another instruction", task, "Close Notepad", 7, false},
		{"nothing sent", task, "", 7, false},
		{"no task", nil, "Open Notepad", 7, false},
	} {
		if got := aiBinds(c.task, c.sent, c.prev); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// TestRecentAI: a new session lists the sessions kept that started a
// task and this computer's tasks no session follows, newest first, at
// most n.
func TestRecentAI(t *testing.T) {
	records := []AIRecord{
		{ID: "a", Device: "", Task: 3, Title: "Mine, renamed", Updated: 30},
		{ID: "b", Device: "657365333", Task: 3, Title: "On the Mac", Updated: 50},
		{ID: "c", Device: "657365333", Title: "never started", Updated: 60},
	}
	local := []localapi.AITask{
		{ID: 3, Instruction: "Mine", Updated: 31},
		{ID: 4, Instruction: "From a controller", Origin: "402118905", Updated: 40},
		{ID: 1, Instruction: "Old", Updated: 10},
	}
	var got []string
	for _, r := range recentAI(records, local, 3) {
		got = append(got, r.title)
	}
	if want := []string{"On the Mac", "From a controller", "Mine, renamed"}; !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

// TestTrimAIRecords: the newest fifty sessions are kept.
func TestTrimAIRecords(t *testing.T) {
	var rs []AIRecord
	for i := range aiRecordsMax + 10 {
		rs = append(rs, AIRecord{ID: fmt.Sprint(i), Updated: int64(i)})
	}
	kept := trimAIRecords(rs)
	if len(kept) != aiRecordsMax || kept[0].ID != fmt.Sprint(aiRecordsMax+9) || kept[len(kept)-1].ID != "10" {
		t.Fatalf("%d kept, from %s to %s", len(kept), kept[0].ID, kept[len(kept)-1].ID)
	}
}

// TestAISessionsKeptWithoutNoLogs: the sessions are written to gui.json,
// and never while No logs is on (doc/en-us/security.md).
func TestAISessionsKeptWithoutNoLogs(t *testing.T) {
	for _, noLogs := range []bool{false, true} {
		dir := t.TempDir()
		p := defaultPrefs()
		p.NoLogs, p.AISessions = noLogs, []AIRecord{{ID: "x", Title: "Open Notepad", Task: 2}}
		if err := savePrefs(dir, p); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(prefsPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if written := strings.Contains(string(raw), "Open Notepad"); written == noLogs {
			t.Errorf("No logs %v: written %v", noLogs, written)
		}
	}
}

// aiDemoApp is an App for the AI panes' tests: this computer's AI set up
// with a key, and the Mac at 657 365 333 among its recent devices.
func aiDemoApp(t *testing.T) *App {
	a := demoApp(t)
	a.ai.loaded, a.ai.settings = true, demoAISettings(true)
	a.status.Recent = []localapi.RecentDevice{{DeviceID: "657365333", Last: 1797400000, About: localapi.About{Name: "Studio Mac", OS: "macos"}}}
	return a
}

// addAITab puts an AI session's pane in the main window, shown.
func addAITab(a *App, device string, rec AIRecord) *aiTab {
	a.prefs.AISessions = append(a.prefs.AISessions, rec)
	tb := &aiTab{app: a, id: rec.ID, device: device}
	a.aiTabs[tb.tabKey()] = tb
	a.main.tabs = append(a.main.tabs, tb)
	a.main.placeLocked(tb, nil, zoneMiddle)
	tb.win.Store(a.main)
	return tb
}

// addAILink gives a host a link in a state.
func addAILink(a *App, device, state string, run, approve bool, task *localapi.AITask) *aiLink {
	l := &aiLink{app: a, device: device, out: make(chan []byte, 16), panes: 1,
		phase: localapi.SessionInfo{ID: 9, State: state, DeviceID: device}, run: run, approve: approve, task: task}
	l.sid.Store(9)
	a.aiLinks[device] = l
	return l
}

// aiSteps are a task's steps for the tests: a screenshot, a click, typing
// and a key; the click at time 2.
func aiSteps() []localapi.AIStep {
	return []localapi.AIStep{
		{At: 1, Action: []byte(`{"kind":"screenshot"}`), Outcome: "ok"},
		{At: 2, Action: []byte(`{"kind":"click","at":{"x":640,"y":425},"button":"left","count":1}`), Outcome: "ok"},
		{At: 3, Action: []byte(`{"kind":"type","text":"Q3 report"}`), Outcome: "ok"},
		{At: 4, Action: []byte(`{"kind":"key-combo","keys":["ctrl","s"]}`), Outcome: "declined"},
	}
}

// TestAIPromptByKeyboard: the box of a new session takes the keys as it
// shows; Enter sends — the session then has its title and waits for its
// task — unless the send-key preference says otherwise, when it breaks
// the line instead.
func TestAIPromptByKeyboard(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	for _, c := range []struct {
		pref string
		mods ui.Modifiers
		sent bool
	}{{"", 0, true}, {"", ui.Shift, false}, {"shift-enter", ui.Shift, true}, {"button", 0, false}} {
		a := aiDemoApp(t)
		a.prefs.SendKey = c.pref
		tb := addAITab(a, "", AIRecord{ID: "n", Created: 1, Updated: 1})
		tt := ui.NewTester(a.main.view, 1280, 800)
		tt.Frame()
		tt.Frame()
		tt.Type("Open Notepad")
		tt.Frame()
		tt.Key(c.mods, ui.KeyEnter)
		tt.Frame()
		rec, _ := a.aiRecord("n")
		if sent := tb.starting == "Open Notepad" && rec.Title == "Open Notepad"; sent != c.sent {
			t.Errorf("%q with %v: sent %v (starting %q, title %q, draft %q)", c.pref, c.mods, sent, tb.starting, rec.Title, a.ai.draft)
		}
	}
}

// TestAIApprovalInline: a host's step waiting for approval shows in the
// conversation, never hidden; Allow sends the decision on the link when
// the session may approve, and otherwise says the host's user decides.
func TestAIApprovalInline(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	for _, approve := range []bool{true, false} {
		a := aiDemoApp(t)
		task := &localapi.AITask{ID: 5, Instruction: "Rename the report", State: "approval", Steps: aiSteps(),
			Approval: &localapi.AIApproval{Action: []byte(`{"kind":"type","text":"Q3.docx"}`)}}
		l := addAILink(a, "657365333", "connected", true, approve, task)
		addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Task: 5, Title: "Rename the report", Updated: 1})
		tt := ui.NewTester(a.main.view, 1280, 800)
		tt.Frame()
		if !tt.HasText(L.AIApprovalTitle()) {
			t.Fatalf("approve %v: the approval does not show", approve)
		}
		if !approve {
			if !tt.HasText(L.ViewerAIHostApproves()) {
				t.Error("without approve-ai-action, no word that the host's user decides")
			}
			continue
		}
		if err := tt.Click(L.ActionAllow()); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		select {
		case p := <-l.out:
			var msg map[string]any
			if json.Unmarshal(p[1:], &msg) != nil || msg["type"] != "ai-decide" || msg["approve"] != true {
				t.Fatalf("sent %s", p[1:])
			}
		default:
			t.Fatal("Allow sent nothing")
		}
	}
}

// TestAINamesInOrder: what names a host's session puts its parts where
// they belong — the window's title, the model with whose key — though the
// catalog's functions take their parts in their names' order
// (regression: the chip read "NUC · Claude Opus 5.5's key").
func TestAINamesInOrder(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "en" })
	defer i18n.SetLocaleFunc(nil)
	a := aiDemoApp(t)
	task := &localapi.AITask{ID: 3, Instruction: "Rename the report", State: "running", Model: "claude-opus-5-5"}
	addAILink(a, "657365333", "connected", true, true, task)
	tb := addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Task: 3, Title: "Rename the report", Updated: 1})
	if got, want := tb.tabTitle(), "Rename the report — Studio Mac"; got != want {
		t.Errorf("window title %q, want %q", got, want)
	}
	tt := ui.NewTester(a.main.view, 1280, 800)
	tt.Frame()
	if want := friendlyModelName("claude-opus-5-5", "", "") + " · Studio Mac's key"; !tt.HasText(want) {
		t.Errorf("no %q among %q", want, tt.Texts())
	}
}

// TestAIPanesRender renders AI sessions in their states, in every language
// and both themes: a new one with suggestions and recent tasks; this
// computer's task working with a reply sent, paused, done (the box then
// replies); a host's waiting for its user to allow AI tasks, waiting for
// an approval, busy with another task; one beside a host's desktop; a
// title being renamed; protected mode; Settings › AI. With SCREENSHOT_DIR
// set it writes PNGs.
func TestAIPanesRender(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	pic := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for i := range pic.Pix {
		pic.Pix[i] = byte(i * 3)
	}
	now := time.Now().Unix()
	working := localapi.AITask{ID: 3, Instruction: "Rename the Q3 report and save it", Source: "claude", Model: "claude-opus-5-5", State: "running",
		Text: "I opened the report; now saving it under its new name.", Progress: []string{"Saving the file"}, Steps: aiSteps(), Created: now - 60, Updated: now}
	shots := map[string]func(a *App){
		"ai-new": func(a *App) {
			a.prefs.AISessions = []AIRecord{{ID: "r1", Device: "657365333", Task: 2, Title: "Clean up the Downloads folder", State: "done", Updated: now - 3600}}
			a.ai.tasks = []localapi.AITask{{ID: 1, Instruction: "Check for updates", State: "done", Updated: now - 7200}}
			addAITab(a, "", AIRecord{ID: "n", Created: now, Updated: now})
		},
		"ai-working": func(a *App) {
			a.ai.tasks = []localapi.AITask{working}
			a.status.AI = &localapi.AIActive{ID: 3, State: "running"}
			tb := addAITab(a, "", AIRecord{ID: "w", Task: 3, Title: "Rename the Q3 report and save it", State: "running", Updated: now})
			tb.sent = []aiSent{{text: "Use the name Q3-final", after: 2}}
		},
		"ai-paused": func(a *App) {
			p := working
			p.State, p.Paused = "paused", "local-input"
			a.ai.tasks = []localapi.AITask{p}
			a.status.AI = &localapi.AIActive{ID: 3, State: "paused"}
			addAITab(a, "", AIRecord{ID: "w", Task: 3, Title: "Rename the Q3 report and save it", State: "paused", Updated: now})
		},
		"ai-done": func(a *App) {
			d := working
			d.State, d.Text, d.Progress = "done", "Saved as Q3-final.docx in Documents.", nil
			a.ai.tasks = []localapi.AITask{d}
			addAITab(a, "", AIRecord{ID: "w", Task: 3, Title: "Rename the Q3 report and save it", State: "done", Updated: now})
		},
		"ai-host-confirming": func(a *App) {
			addAILink(a, "657365333", "confirming", false, false, nil)
			addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Created: now, Updated: now})
		},
		"ai-host-approval": func(a *App) {
			task := working
			task.State, task.Progress = "approval", nil
			task.Approval = &localapi.AIApproval{Action: []byte(`{"kind":"key-combo","keys":["meta","r"]}`), Flagged: []string{"The step opens Run."}}
			addAILink(a, "657365333", "connected", true, true, &task)
			addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Task: 3, Title: "Rename the Q3 report and save it", Updated: now})
		},
		"ai-host-busy": func(a *App) {
			other := working
			other.ID, other.Origin = 9, "402118905"
			addAILink(a, "657365333", "connected", true, true, &other)
			addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Created: now, Updated: now})
		},
		"ai-beside-desktop": func(a *App) {
			task := working
			addAILink(a, "657365333", "connected", true, true, &task)
			tb := addAITab(a, "657365333", AIRecord{ID: "h", Device: "657365333", Task: 3, Title: "Rename the Q3 report and save it", Updated: now})
			v := &viewer{app: a, device: "657365333", title: "Studio Mac · 657 365 333", preset: 1, fpsCap: "30 fps",
				start: videoStart{Codec: "h264", Width: 320, Height: 180}, bm: ui.NewBitmap(pic),
				phase: localapi.SessionInfo{ID: 1, DeviceID: "657365333", State: "connected"}, pumping: true}
			a.viewers[v.device] = v
			a.main.tabs = append(a.main.tabs, v)
			a.main.placeLocked(v, tb, zoneLeft)
		},
		"ai-renaming": func(a *App) {
			a.ai.tasks = []localapi.AITask{working}
			tb := addAITab(a, "", AIRecord{ID: "w", Task: 3, Title: "Rename the Q3 report and save it", State: "running", Updated: now})
			name := "Q3 report"
			tb.renaming = &name
		},
		"ai-protected": func(a *App) {
			a.status.Protected, a.status.PermanentPassword = true, true
			a.ai.tasks = []localapi.AITask{working}
		},
		"settings-ai": func(a *App) { a.settingsOpen, a.page = true, pageAI },
	}
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ja"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, dark := range []bool{false, true} {
			if dark && lang != "en" {
				continue
			}
			for name, setup := range shots {
				a := aiDemoApp(t)
				setup(a)
				tt := ui.NewTester(a.main.view, 1280, 800)
				tt.SetDark(dark)
				tt.Frame()
				tt.Frame()
				if dir == "" {
					continue
				}
				suffix := ""
				if dark {
					suffix = "-dark"
				}
				f, err := os.Create(filepath.Join(dir, lang+"-"+name+suffix+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
		}
	}
	i18n.SetLocaleFunc(nil)
}
