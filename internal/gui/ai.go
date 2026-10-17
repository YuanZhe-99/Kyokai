package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// aiPoll is how often the tasks are re-read while one is in progress; the
// status push says only that a task runs, not what it did.
const aiPoll = 700 * time.Millisecond

// aiStepsShown is how many of a task's latest actions the page lists.
const aiStepsShown = 8

// aiState is the AI page's state.
type aiState struct {
	// settings is nil until loaded.
	settings *localapi.AISettings
	// edit is the source being added or changed (aisources.go); template
	// is the one picked to add.
	edit     *aiEdit
	template string
	draft    string // the next task's instruction
	reply    string // a reply to the latest finished task
	tasks    []localapi.AITask
	loaded   bool
	polling  bool
}

// aiView is the AI page: the task in progress (or a new one), the provider
// and the history.
func (a *App) aiView(c *ui.Context) {
	if !a.engineState(c) {
		return
	}
	a.mu.Lock()
	loaded, busy := a.ai.loaded, a.busy["ai"]
	a.mu.Unlock()
	if !loaded && !busy {
		a.loadAI()
	}
	a.aiTaskCard(c)
	a.aiProviderCard(c)
	a.aiHistoryCard(c)
}

// aiTaskCard shows the task in progress with its controls, or the latest
// finished task and the form for a new one.
func (a *App) aiTaskCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	s, draft, reply := a.ai.settings, a.ai.draft, a.ai.reply
	var latest *localapi.AITask
	if len(a.ai.tasks) > 0 {
		latest = &a.ai.tasks[0]
	}
	busy := a.busy["ai-task"]
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.AITaskTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.AITaskHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		active := latest != nil && !aiFinished(latest.State)
		if latest != nil {
			a.aiTaskDetail(c, latest)
		}
		if active {
			return
		}
		if latest != nil && latest.State == "done" {
			ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
				if ui.TextInput(c.Key("ai-reply"), &reply).Placeholder(L.AIReplyHint()).Grow(1).Disabled(busy).Changed() {
					a.mu.Lock()
					a.ai.reply = reply
					a.mu.Unlock()
				}
				if ui.Button(c.Key("ai-send"), L.AIActionSend()).Disabled(busy || strings.TrimSpace(reply) == "").Clicked() {
					a.aiReply(latest.ID, reply)
				}
			})
			ui.Divider(c)
		}
		ui.Text(c, L.AINewTask()).Bold()
		if ui.TextArea(c.Key("ai-draft"), &draft).FillWidth().MinHeight(inputAreaH).Disabled(busy).Changed() {
			a.mu.Lock()
			a.ai.draft = draft
			a.mu.Unlock()
		}
		ready := aiReady(s)
		if s != nil && !ready && s.Model != nil && aiSource(s, s.Model.Source) != nil {
			ui.Text(c, L.AINeedKey(aiSource(s, s.Model.Source).Name)).FontSize(sizeSmall).TextColor(t.Warning)
		}
		ui.Row(c).Children(func() {
			if ui.Button(c.Key("ai-start"), L.AIActionStart()).Disabled(busy || !ready || strings.TrimSpace(draft) == "").Clicked() {
				a.aiStart(draft)
			}
		})
	})
}

// aiTaskDetail shows one task: its state, what the model said, its latest
// actions, an approval waiting for the user, and the controls.
func (a *App) aiTaskDetail(c *ui.Context, task *localapi.AITask) {
	t := c.Theme()
	key := "ai-task-" + itoa(task.ID)
	ui.Column(c.Key(key)).Gap(8).FillWidth().Children(func() {
		ui.Text(c, task.Instruction).Bold().Selectable()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if task.State == "running" {
				ui.Spinner(c)
			}
			col := t.TextMuted
			switch task.State {
			case "approval":
				col = t.Accent
			case "failed":
				col = t.Danger
			case "done":
				col = t.Success
			case "paused":
				col = t.Warning
			}
			ui.Text(c, aiStateLabel(task)).TextColor(col)
			a.mu.Lock()
			s := a.ai.settings
			a.mu.Unlock()
			ui.Text(c, aiTaskModel(s, task)).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		if task.Origin != "" {
			ui.Text(c, L.AITaskFrom(groupDigits(task.Origin))).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		if task.Error != "" {
			ui.Text(c, task.Error).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
		}
		if task.Text != "" {
			ui.Text(c, task.Text).Selectable()
		}
		if n := len(task.Progress); n > 0 && !aiFinished(task.State) {
			ui.Text(c, task.Progress[n-1]).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		if task.Approval != nil {
			a.aiApproval(c, task)
		}
		if len(task.Steps) > 0 {
			ui.Text(c, L.AIStepsTitle()).FontSize(sizeSmall).Bold()
			steps := task.Steps[max(len(task.Steps)-aiStepsShown, 0):]
			for i, s := range steps {
				ui.Row(c.Key(key + "-step-" + strconv.Itoa(i))).Gap(8).Children(func() {
					ui.Text(c, describeAction(s.Action)).FontSize(sizeSmall).Grow(1)
					if o := outcomeLabel(s.Outcome); o != "" {
						ui.Text(c, o).FontSize(sizeSmall).TextColor(t.Warning)
					}
				})
			}
		}
		if aiFinished(task.State) {
			return
		}
		ui.Row(c).Gap(gap).Children(func() {
			switch task.State {
			case "paused":
				if ui.Button(c.Key(key+"-resume"), L.AIActionResume()).Clicked() {
					a.aiCommand(localapi.RequestAITaskResume, task.ID)
				}
			default:
				if ui.Button(c.Key(key+"-pause"), L.AIActionPause()).Clicked() {
					a.aiCommand(localapi.RequestAITaskPause, task.ID)
				}
			}
			if ui.Button(c.Key(key+"-cancel"), L.AIActionStop()).Clicked() {
				a.aiCommand(localapi.RequestAITaskCancel, task.ID)
			}
		})
	})
}

// aiApproval asks the host user about the next action.
func (a *App) aiApproval(c *ui.Context, task *localapi.AITask) {
	t := c.Theme()
	key := "ai-approve-" + itoa(task.ID)
	ui.Column(c.Key(key)).Gap(8).FillWidth().Padding(12).Radius(8).Border(2, t.Accent).Children(func() {
		ui.Text(c, L.AIApprovalTitle()).Bold()
		ui.Text(c, describeAction(task.Approval.Action))
		if len(task.Approval.Flagged) > 0 {
			ui.Text(c, L.AIApprovalFlagged()).FontSize(sizeSmall).TextColor(t.Warning)
			for i, f := range task.Approval.Flagged {
				ui.Text(c.Key(key+"-f"+strconv.Itoa(i)), f).FontSize(sizeSmall).Selectable()
			}
		}
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key(key+"-allow"), L.ActionAllow()).Clicked() {
				a.aiDecide(task.ID, true)
			}
			if ui.Button(c.Key(key+"-deny"), L.ActionDeny()).Clicked() {
				a.aiDecide(task.ID, false)
			}
		})
	})
}

// aiHistoryCard lists the finished tasks.
func (a *App) aiHistoryCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var done []localapi.AITask
	for i, task := range a.ai.tasks {
		// The latest task is shown above.
		if i > 0 && aiFinished(task.State) {
			done = append(done, task)
		}
	}
	a.mu.Unlock()
	card(c).Children(func() {
		ui.Text(c, L.AIHistoryTitle()).FontSize(sizeHeading).Bold()
		if len(done) == 0 {
			ui.Text(c, L.AIHistoryEmpty()).FontSize(sizeSmall).TextColor(t.TextMuted)
			return
		}
		for _, task := range done {
			ui.Row(c.Key("ai-h-" + itoa(task.ID))).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, task.Instruction).Grow(1).MaxLines(1)
				ui.Text(c, aiStateLabel(&task)).FontSize(sizeSmall).TextColor(t.TextMuted)
				ui.Text(c, time.Unix(task.Created, 0).Local().Format(time.DateTime)).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			})
		}
		if ui.Button(c.Key("ai-clear"), L.AIHistoryClear()).Clicked() {
			go func() {
				_, _ = a.request(localapi.RequestAIHistoryClear)
				a.refreshAITasks()
			}()
		}
	})
}

// aiBanner tells the user on every other page that the AI task waits for
// them, or that a controller's AI task is working on this computer (with
// the emergency stop at hand).
func (a *App) aiBanner(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var active *localapi.AIActive
	if a.connected && a.status != nil && a.page != pageAI {
		active = a.status.AI
	}
	a.mu.Unlock()
	if active == nil || (active.State != "approval" && active.Origin == "") {
		return
	}
	text := L.AIApprovalBanner()
	if active.Origin != "" && active.State != "approval" {
		text = L.AIRemoteBanner(groupDigits(active.Origin))
	}
	card(c.Key("ai-banner")).Border(2, t.Accent).Children(func() {
		ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
			ui.Text(c, text).Grow(1)
			if ui.Button(c.Key("ai-review"), L.AIReview()).Clicked() {
				a.mu.Lock()
				a.page = pageAI
				a.mu.Unlock()
			}
			if active.Origin != "" && ui.Button(c.Key("ai-banner-stop"), L.AIActionStop()).Clicked() {
				a.stopAI()
			}
		})
	})
}

// aiStatusChanged reacts to a pushed status (caller holds a.mu): it keeps
// the task list current while a task runs and notifies when the task waits
// for approval.
func (a *App) aiStatusChanged(prev, st *localapi.Status) {
	if st.AI != nil && st.AI.State == "approval" && (prev == nil || prev.AI == nil || prev.AI.State != "approval") {
		a.notify("ai-approval", L.AINotifyApprovalTitle(), L.AINotifyApprovalBody())
	}
	if st.AI != nil && !a.ai.polling {
		a.ai.polling = true
		go a.pollAI()
	}
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

// refreshAITasks fetches the task list.
func (a *App) refreshAITasks() {
	resp, err := a.request(localapi.RequestAITasks)
	if err != nil {
		return
	}
	a.update(func() { a.ai.tasks = resp.AITasks })
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

// aiStart starts a task. The idempotency key makes a repeated click start
// one task.
func (a *App) aiStart(instruction string) {
	a.mu.Lock()
	a.busy["ai-task"] = true
	a.mu.Unlock()
	key := strconv.FormatInt(time.Now().UnixNano(), 36)
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskStart,
			AITask: &localapi.AITaskRequest{Key: key, Instruction: instruction}})
		a.update(func() {
			delete(a.busy, "ai-task")
			if a.aiFailed(err) {
				return
			}
			a.ai.draft = ""
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
			if !a.aiFailed(err) {
				a.ai.reply = ""
			}
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
