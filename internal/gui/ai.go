package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
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
	// apps are the programs with windows on this screen (ai.apps), for
	// the scope chip; appsAt is when they were last asked for.
	apps   []string
	appsAt time.Time
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
	a.noticeCard(c, "ai-banner", tone, func() {
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
// Inputs: model and thinking, another model of the task's source and how
// much it thinks from now on (nil, "": as they were).
func (a *App) aiReply(id uint64, text string, model *localapi.AIModelRef, thinking string) {
	a.mu.Lock()
	a.busy["ai-task"] = true
	a.mu.Unlock()
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskReply, ID: id,
			AITask: &localapi.AITaskRequest{Text: text, Model: model, Thinking: thinking}})
		a.update(func() {
			delete(a.busy, "ai-task")
			a.aiFailed(err)
		})
		a.refreshAITasks()
	}()
}

// aiRenameTask gives this computer's task the session's name.
func (a *App) aiRenameTask(id uint64, name string) {
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskRename, ID: id,
			AITask: &localapi.AITaskRequest{Name: name}})
		a.update(func() { a.aiFailed(err) })
	}()
}

// aiFork copies this computer's finished task, as it is (run nil) or as it
// was after one of its runs, and opens the copy in a session of its own
// titled as t.
func (a *App) aiFork(t *aiTab, id uint64, run *int) {
	title := t.title()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskFork, ID: id,
			AITask: &localapi.AITaskRequest{Run: run}})
		a.update(func() { a.aiFailed(err) })
		if err != nil {
			return
		}
		a.refreshAITasks()
		a.later(func() { a.openAITask("", resp.ID, title) })
	}()
}

// aiCompact compacts this computer's finished task, then follows it until
// the summary is in (no task holds the desktop, so the status poll does not
// run meanwhile).
func (a *App) aiCompact(id uint64) {
	go func() {
		_, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskCompact, ID: id})
		a.update(func() { a.aiFailed(err) })
		for deadline := time.Now().Add(6 * time.Minute); err == nil && time.Now().Before(deadline); {
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			a.refreshAITasks()
			a.mu.Lock()
			i := slices.IndexFunc(a.ai.tasks, func(t localapi.AITask) bool { return t.ID == id })
			busy := i >= 0 && a.ai.tasks[i].Compacting
			a.mu.Unlock()
			if !busy {
				return
			}
		}
	}()
}

// exportPath asks the user where to save a task as Markdown.
// Returns: the path, empty when the user cancelled.
// Notes: the save dialog waits for the user; call it off the main thread.
func (a *App) exportPath(title string) string {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent: a.window, Title: L.AIExport(), DefaultPath: exportName(title) + ".md",
		Filters: []mygo.FileFilter{{Name: L.MarkdownFileKind(), Extensions: []string{"md"}}},
	})
	if err != nil {
		return ""
	}
	return path
}

// hostExport asks a host for its task as Markdown, saved where the user
// chooses when it comes (aiLink.exportTo).
// Notes: call it off the main thread.
func (a *App) hostExport(l *aiLink, id uint64, title string) {
	if path := a.exportPath(title); path != "" {
		l.exportTo(id, path)
	}
}

// hostFork asks a host to copy its finished task, as it is (run nil) or
// as it was after one of its runs, and opens a session for the host that
// takes the copy when it comes (a new task with the same instruction:
// aiBinds), titled as t.
// Notes: call on the main thread.
func (a *App) hostFork(t *aiTab, id uint64, instruction string, run *int) {
	l := a.aiLinkOf(t.device)
	if l == nil {
		return
	}
	rec := a.newAIRecord(t.device)
	title := t.title()
	a.setAIRecord(rec, func(r *AIRecord) { r.Title = title })
	if n := a.openAISession(t.device, "", rec, nil, zoneMiddle); n != nil {
		n.starting, n.prev = instruction, id
	}
	msg := map[string]any{"type": "ai-command", "id": id, "command": "fork"}
	if run != nil {
		msg["run"] = *run
	}
	l.send(msg)
}

// aiExport saves this computer's task as Markdown where the user chooses.
// Notes: the save dialog waits for the user; call it off the main thread.
func (a *App) aiExport(id uint64, title string) {
	path := a.exportPath(title)
	if path == "" {
		return
	}
	resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskExport, ID: id})
	var werr error
	if err == nil {
		werr = os.WriteFile(path, []byte(resp.Document), 0o600)
	}
	a.update(func() {
		switch {
		case a.aiFailed(err):
		case werr != nil:
			a.setToast(werr.Error(), true)
		default:
			a.setToast(L.AIExported(), false)
		}
	})
}

// exportName is a file name from a session's title: its first 60
// characters with what file systems refuse left out.
func exportName(title string) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < ' ' {
			return -1
		}
		return r
	}, title)
	if r := []rune(strings.TrimSpace(name)); len(r) > 60 {
		name = string(r[:60])
	}
	if name = strings.Trim(name, " ."); name == "" {
		return "kyokai-ai-task"
	}
	return name
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

// aiWhy says why an action waits for approval, from the host's policy class
// (doc/en-us/computer-use.md, "Approvals"); "" where the card says enough
// already (every action, a flagged turn) or the host names no class.
func aiWhy(class string) string {
	switch class {
	case "secure-surface":
		return L.AIWhySecureSurface()
	case "credentials":
		return L.AIWhyCredentials()
	case "unknown":
		return L.AIWhyUnknown()
	case "system-shortcut":
		return L.AIWhyShortcut()
	case "power-session":
		return L.AIWhyPowerSession()
	case "settings":
		return L.AIWhySettings()
	case "deleting":
		return L.AIWhyDeleting()
	case "uploading":
		return L.AIWhyUploading()
	case "password-vault":
		return L.AIWhyPasswordVault()
	case "installing":
		return L.AIWhyInstalling()
	case "terminal":
		return L.AIWhyTerminal()
	case "process-control":
		return L.AIWhyProcessControl()
	case "sending":
		return L.AIWhySending()
	case "payments":
		return L.AIWhyPayments()
	case "consent":
		return L.AIWhyConsent()
	case "remote-desktop":
		return L.AIWhyRemoteDesktop()
	case "outside-scope":
		return L.AIWhyOutsideScope()
	case "outside-folders":
		return L.AIWhyOutsideFolders()
	}
	return ""
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
		case "locked":
			return L.AIStatePausedLocked()
		case "folders":
			return L.AIStatePausedFolders()
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

// refreshAIApps asks the engine again for the programs with windows on
// this screen (ai.apps), at most every few seconds; the scope chip shows
// them the next time it opens.
func (a *App) refreshAIApps() {
	a.mu.Lock()
	if time.Since(a.ai.appsAt) < 3*time.Second {
		a.mu.Unlock()
		return
	}
	a.ai.appsAt = time.Now()
	a.mu.Unlock()
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAIApps})
		if err != nil || !resp.OK {
			return
		}
		a.update(func() { a.ai.apps = resp.AIApps })
	}()
}

// setAIScope grants the AI only these programs' windows in this session,
// or the whole desktop with nil (doc/en-us/computer-use.md, "AI scope").
func (a *App) setAIScope(apps []string) {
	go func() {
		req := localapi.Request{Type: localapi.RequestAIScopeSet}
		if apps != nil {
			req.Apps = &apps
		}
		resp, err := a.requestWith(req)
		if err != nil || !resp.OK || resp.AISettings == nil {
			return
		}
		a.update(func() { a.ai.settings = resp.AISettings })
	}()
}

// setAIFolders grants the AI only these folders in this session, or every
// folder with nil: checked, not enforced (doc/en-us/computer-use.md, "AI
// scope").
func (a *App) setAIFolders(folders []string) {
	go func() {
		req := localapi.Request{Type: localapi.RequestAIFoldersSet}
		if folders != nil {
			req.Folders = &folders
		}
		resp, err := a.requestWith(req)
		if err != nil || !resp.OK || resp.AISettings == nil {
			return
		}
		a.update(func() { a.ai.settings = resp.AISettings })
	}()
}

// addAIFolder asks the user for a folder and grants it beside the granted
// ones.
// Notes: the dialog waits for the user on a goroutine of its own.
func (a *App) addAIFolder(granted []string) {
	go func() {
		picked, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: L.AIFoldersAdd(), Directory: true})
		if err != nil || len(picked) == 0 {
			return
		}
		a.setAIFolders(slices.Concat(granted, picked))
	}()
}

// scopeChoices are the programs the scope chip offers: those with windows
// now and those granted already, sorted.
func scopeChoices(apps, granted []string) []string {
	out := slices.Concat(apps, granted)
	slices.Sort(out)
	return slices.Compact(out)
}

// appLabel names a program for people: its executable without ".exe".
func appLabel(app string) string {
	return strings.TrimSuffix(app, ".exe")
}
