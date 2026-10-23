package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// aiPoll is how often the tasks are re-read while one is in progress; the
// status push says only that a task runs, not what it did.
const aiPoll = 700 * time.Millisecond

// aiState is this computer's AI as the GUI knows it: its settings and
// tasks, and the shared draft of its next task.
type aiState struct {
	// settings is nil until loaded.
	settings *localapi.AISettings
	// edit is the source being added or changed (aisources.go); template
	// is the one picked to add.
	edit     *aiEdit
	template string
	draft    string // the next task's instruction, shared through the engine
	// draftAt is when this user last typed in it, so that a draft typed
	// elsewhere does not replace what is being typed here.
	draftAt time.Time
	tasks   []localapi.AITask
	loaded  bool
	polling bool
}

// aiView is Settings › AI: the providers and the defaults for new tasks
// (aiProviders), then how AI sessions go — what sends a prompt and the
// recent tasks kept. The tasks themselves are AI sessions' panes
// (aisession.go).
func (a *App) aiView(c *ui.Context) {
	settingsPage(c, L.AISettingsTitle(), L.SettingsAboutAI(), func() {
		if !a.engineState(c) {
			return
		}
		a.ensureAILoaded()
		a.aiProviders(c)
		a.aiSessionsSection(c)
	})
}

// aiSessionsSection is how AI sessions go: what sends a prompt
// (Prefs.SendKey), and the recent tasks — this computer's history and the
// sessions kept here — with Clear history.
func (a *App) aiSessionsSection(c *ui.Context) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	keys := []string{"", "shift-enter", "button"}
	names := []string{L.AISendEnter(), L.AISendShiftEnter(), L.AISendButton()}
	cur := names[max(slices.Index(keys, p.SendKey), 0)]
	a.section(c, "ai-sessions", L.SectionAISessions(), nil, func(r *rows) {
		r.row(c, "ai-send-key", tile(aiSend), setting{title: L.AISendKey(), about: L.AISendKeyAbout()}, func() {
			if ui.Select(c.Key("ai-send-key-sel"), &cur, names).Width(280).Changed() {
				p.SendKey = keys[slices.Index(names, cur)]
				_ = a.setPrefs(p)
			}
		})
		r.row(c, "ai-recent", tile(rowArchive), setting{title: L.AIRecentTitle(), about: L.AIRecentAbout()}, func() {
			if ui.Button(c.Key("ai-recent-clear"), L.AIHistoryClear()).Clicked() {
				a.clearAIHistory()
			}
		})
	})
}

// clearAIHistory forgets the recent tasks: this computer's history and the
// sessions kept here, but those open in a pane.
// Notes: call on the main thread.
func (a *App) clearAIHistory() {
	a.mu.Lock()
	open := map[string]bool{}
	for _, t := range a.aiTabs {
		open[t.id] = true
	}
	a.mu.Unlock()
	a.changeAIRecords(func(rs []AIRecord) []AIRecord {
		return slices.DeleteFunc(rs, func(r AIRecord) bool { return !open[r.ID] })
	})
	go func() {
		_, _ = a.request(localapi.RequestAIHistoryClear)
		a.refreshAITasks()
	}()
}

// aiBanner tells the user, as a card above the panes (noticeCard), that
// this computer's AI task waits for their approval — in the accent, Review
// showing its session — or that a controller's AI task is working on this
// computer — in the warning colour, with the emergency stop at hand —,
// unless a session following that task shows.
func (a *App) aiBanner(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var active *localapi.AIActive
	if a.connected && a.status != nil {
		active = a.status.AI
	}
	a.mu.Unlock()
	if active == nil || (active.State != "approval" && active.Origin == "") || a.aiTaskShown(active.ID) {
		return
	}
	text, tone := L.AIApprovalBanner(), t.Accent
	if active.Origin != "" && active.State != "approval" {
		text, tone = L.AIRemoteBanner(a.deviceName(active.Origin)), t.Warning
	}
	noticeCard(c, "ai-banner", tone, func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			noticeIcon(c, navAI, tone)
			ui.Text(c, text).FontWeight(500).Grow(1).MinWidth(0)
			var review bool
			if active.State == "approval" {
				review = ui.PrimaryButton(c.Key("ai-review"), L.AIReview()).Clicked()
			} else {
				review = ui.Button(c.Key("ai-review"), L.AIReview()).Clicked()
			}
			if review {
				a.openAITask("", active.ID, a.aiInstruction(active.ID))
			}
			if active.Origin != "" && ui.Button(c.Key("ai-banner-stop"), L.AIActionStop()).Clicked() {
				a.stopAI()
			}
		})
	})
}

// aiTaskShown reports whether the main window shows a session following
// one of this computer's tasks.
func (a *App) aiTaskShown(id uint64) bool {
	a.main.mu.Lock()
	var shown []tab
	if a.main.cur != nil {
		shown = a.main.cur.shownTabs()
	}
	a.main.mu.Unlock()
	for _, v := range shown {
		if t, ok := v.(*aiTab); ok && t.device == "" {
			if r, ok := a.aiRecord(t.id); ok && r.Task == id {
				return true
			}
		}
	}
	return false
}

// aiInstruction is one of this computer's tasks' instruction, "" while
// unknown.
func (a *App) aiInstruction(id uint64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.ai.tasks {
		if t.ID == id {
			return t.Instruction
		}
	}
	return ""
}

// aiStatusChanged reacts to a pushed status (caller holds a.mu): it keeps
// the task list current while a task runs.
// Returns: whether the task now waits for approval, which the caller
// announces once it let go of the lock (statusPushed).
func (a *App) aiStatusChanged(prev, st *localapi.Status) (approval bool) {
	if st.AI != nil && !a.ai.polling {
		a.ai.polling = true
		go a.pollAI()
	}
	return st.AI != nil && st.AI.State == "approval" && (prev == nil || prev.AI == nil || prev.AI.State != "approval")
}

// pollAI re-reads the tasks while one is in progress, and once after it
// ends.
func (a *App) pollAI() {
	for a.ctx.Err() == nil {
		a.refreshAITasks()
		a.mu.Lock()
		active := a.status != nil && a.status.AI != nil
		if !active {
			a.ai.polling = false
		}
		a.mu.Unlock()
		if !active {
			a.refreshAITasks()
			return
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(aiPoll):
		}
	}
}

// refreshAITasks fetches the task list, which the sessions on this
// computer follow (aiTaskSeen).
func (a *App) refreshAITasks() {
	resp, err := a.request(localapi.RequestAITasks)
	if err != nil {
		return
	}
	a.update(func() { a.ai.tasks = resp.AITasks })
	a.later(func() {
		for i := range resp.AITasks {
			a.aiTaskSeen("", &resp.AITasks[i])
		}
		a.redrawAI("")
	})
}

// loadAI fetches the settings and the tasks.
func (a *App) loadAI() {
	a.mu.Lock()
	a.busy["ai"] = true
	a.mu.Unlock()
	go func() {
		resp, err := a.request(localapi.RequestAISettingsGet)
		a.update(func() {
			delete(a.busy, "ai")
			a.ai.loaded = true
			if err != nil || resp.AISettings == nil {
				a.setToast(L.EngineDown(), true)
				return
			}
			a.ai.settings = resp.AISettings
		})
		a.refreshAITasks()
	}()
}

// aiReply continues a finished task.
func (a *App) aiReply(id uint64, text string) {
	a.mu.Lock()
	a.busy["ai-task"] = true
	a.mu.Unlock()
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskReply, ID: id,
			AITask: &localapi.AITaskRequest{Text: text}})
		a.update(func() {
			delete(a.busy, "ai-task")
			a.aiFailed(err)
		})
		a.refreshAITasks()
	}()
}

// aiCommand pauses, resumes or cancels a task.
func (a *App) aiCommand(t localapi.RequestType, id uint64) {
	go func() {
		_, err := a.requestWith(localapi.Request{Type: t, ID: id})
		a.update(func() { a.aiFailed(err) })
		a.refreshAITasks()
	}()
}

// aiDecide answers the task's approval.
func (a *App) aiDecide(id uint64, approve bool) {
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskDecide, ID: id,
			AITask: &localapi.AITaskRequest{Approve: approve}})
		a.update(func() { a.aiFailed(err) })
		a.refreshAITasks()
	}()
}

// aiFailed shows a request's error (caller holds a.mu).
// Returns: whether there was one.
func (a *App) aiFailed(err error) bool {
	var refused *localapi.RequestError
	switch {
	case errors.As(err, &refused):
		a.setToast(refused.Message, true)
	case err != nil:
		a.setToast(L.EngineDown(), true)
	default:
		return false
	}
	return true
}

// stopAI is the emergency stop: every task is cancelled.
func (a *App) stopAI() {
	go func() {
		_, _ = a.request(localapi.RequestAIStop)
		a.refreshAITasks()
	}()
}

func aiFinished(state string) bool {
	return state == "done" || state == "failed" || state == "cancelled"
}

func aiStateLabel(t *localapi.AITask) string {
	switch t.State {
	case "running":
		return L.AIStateRunning()
	case "approval":
		return L.AIStateApproval()
	case "paused":
		switch t.Paused {
		case "local-input":
			return L.AIStatePausedLocal()
		case "controller":
			return L.AIStatePausedController()
		case "take-over":
			return L.AIStatePausedTakeOver()
		case "disconnected":
			return L.AIStatePausedDisconnected()
		case "sleep":
			return L.AIStatePausedSleep()
		}
		return L.AIStatePaused()
	case "done":
		return L.AIStateDone()
	case "failed":
		return L.AIStateFailed()
	}
	return L.AIStateCancelled()
}

func outcomeLabel(o string) string {
	switch o {
	case "failed":
		return L.AIOutcomeFailed()
	case "declined":
		return L.AIOutcomeDeclined()
	case "not-executed":
		return L.AIOutcomeSkipped()
	case "paused":
		return L.AIOutcomePaused()
	}
	return ""
}

// aiAction is an action as the core writes it (doc/en-us/computer-use.md).
type aiAction struct {
	Kind      string    `json:"kind"`
	At        *aiPoint  `json:"at"`
	To        *aiPoint  `json:"to"`
	Path      []aiPoint `json:"path"`
	Button    string    `json:"button"`
	Count     int       `json:"count"`
	Modifiers []string  `json:"modifiers"`
	Held      []string  `json:"held"`
	Keys      []string  `json:"keys"`
	Text      string    `json:"text"`
	DX        int       `json:"dx"`
	DY        int       `json:"dy"`
	Duration  int       `json:"duration_ms"`
	Region    *[4]int   `json:"region"`
}

type aiPoint struct{ X, Y int }

// describeAction words an action for the host user, as the approval
// prompt and the task's list show it.
func describeAction(raw json.RawMessage) string {
	var act aiAction
	if len(raw) == 0 || json.Unmarshal(raw, &act) != nil {
		return L.AIDoUnknown()
	}
	var s string
	switch act.Kind {
	case "screenshot":
		s = L.AIDoScreenshot()
	case "zoom":
		s = L.AIDoZoom()
	case "cursor-position":
		s = L.AIDoCursor()
	case "move":
		if act.To == nil {
			return L.AIDoUnknown()
		}
		s = withKeys(L.AIDoMove(act.To.X, act.To.Y), act.Held)
	case "click":
		s = describeClick(act)
	case "mouse-down":
		s = L.AIDoMouseDown()
	case "mouse-up":
		s = L.AIDoMouseUp()
	case "drag":
		if len(act.Path) < 2 {
			return L.AIDoUnknown()
		}
		from, to := act.Path[0], act.Path[len(act.Path)-1]
		s = withKeys(L.AIDoDrag(from.X, from.Y, to.X, to.Y), act.Modifiers)
	case "scroll":
		switch {
		case act.DY > 0:
			s = L.AIDoScrollDown()
		case act.DY < 0:
			s = L.AIDoScrollUp()
		case act.DX < 0:
			s = L.AIDoScrollLeft()
		default:
			s = L.AIDoScrollRight()
		}
		s = withKeys(s, act.Modifiers)
	case "type":
		text := act.Text
		if r := []rune(text); len(r) > 80 {
			text = string(r[:80]) + "…"
		}
		s = L.AIDoType(text)
	case "key-combo":
		s = L.AIDoKey(keyLabel(act.Keys))
	case "hold-key":
		s = L.AIDoHoldKey(keyLabel(act.Keys), seconds(act.Duration))
	case "wait":
		s = L.AIDoWait(seconds(act.Duration))
	default:
		return L.AIDoUnknown()
	}
	return s
}

func describeClick(act aiAction) string {
	if act.At == nil {
		return withKeys(L.AIDoClickHere(), act.Modifiers)
	}
	x, y := act.At.X, act.At.Y
	var s string
	switch {
	case act.Button == "right":
		s = L.AIDoRightClick(x, y)
	case act.Button == "middle":
		s = L.AIDoMiddleClick(x, y)
	case act.Count == 2:
		s = L.AIDoDoubleClick(x, y)
	case act.Count >= 3:
		s = L.AIDoTripleClick(x, y)
	default:
		s = L.AIDoClick(x, y)
	}
	return withKeys(s, act.Modifiers)
}

func withKeys(action string, keys []string) string {
	if len(keys) == 0 {
		return action
	}
	return L.AIDoWithKeys(action, keyLabel(keys))
}

// keyLabel writes a combination as keyboards print it: "Ctrl+Shift+T".
func keyLabel(keys []string) string {
	out := make([]string, len(keys))
	for i, k := range keys {
		switch k {
		case "ctrl":
			k = "Ctrl"
		case "shift":
			k = "Shift"
		case "alt":
			k = "Alt"
			if runtime.GOOS == "darwin" {
				k = "Option"
			}
		case "meta":
			k = "Win"
			if runtime.GOOS == "darwin" {
				k = "Command"
			}
		default:
			if len(k) == 1 {
				k = strings.ToUpper(k)
			} else {
				parts := strings.Split(k, "-")
				for j, p := range parts {
					parts[j] = strings.ToUpper(p[:1]) + p[1:]
				}
				k = strings.Join(parts, " ")
			}
		}
		out[i] = k
	}
	return strings.Join(out, "+")
}

// seconds writes a duration in milliseconds as seconds, without a needless
// fraction.
func seconds(ms int) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", float64(ms)/1000), "0"), ".")
}
