package gui

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// aiRecordsMax is how many AI sessions this computer keeps, the newest.
const aiRecordsMax = 50

// aiRecentShown is how many recent tasks a new session lists.
const aiRecentShown = 6

// aiStepsListed is how many of a task's latest steps the conversation
// shows (a host sends its latest 20).
const aiStepsListed = 20

// aiKey is an AI session pane's tabKey.
func aiKey(id string) string { return "ai-" + id }

// aiTab is an AI session as a pane (doc/en-us/gui.md, "AI sessions"): the
// conversation with this computer's AI or a host's — the instruction, the
// model's text, its steps and an approval waiting, inline — and the prompt
// box under it. A device may have several; its tasks run one at a time.
// Fields after win are the main thread's.
type aiTab struct {
	app *App
	id  string // its AIRecord's
	// device is "" for this computer, else the host's Device ID; a new
	// session may move (moveTo, under a.mu).
	device string
	win    atomic.Pointer[viewerWindow]

	// reply is what is typed in the box while it continues a task; sent
	// are the replies sent from the pane, which the conversation shows.
	reply string
	sent  []aiSent
	// starting is the first instruction sent to a host while its task is
	// not seen yet, prev the host's task when it was sent (aiBinds).
	starting string
	prev     uint64
	// renaming is the title being typed in the header (nil: not renaming).
	renaming *string
	// seen is what the conversation showed last (steps and state), to
	// follow its end as it grows; folded are its groups of steps folded
	// to their count, by place.
	seen   string
	folded map[int]bool
}

// aiSent is a reply sent from a pane, after the step that was the newest
// then (its time; 0 before any).
type aiSent struct {
	text  string
	after int64
}

// aiBinds reports whether a host's task is the one a pane's first
// instruction started: another than the task it had when sent, with that
// instruction.
func aiBinds(task *localapi.AITask, sent string, prev uint64) bool {
	return task != nil && sent != "" && task.ID != prev && task.Instruction == sent
}

// sendsOnEnter reports whether Enter with mods sends the prompt under the
// send-key preference (Prefs.SendKey); otherwise it breaks the line.
func sendsOnEnter(pref string, mods ui.Modifiers) bool {
	switch pref {
	case "shift-enter":
		return mods == ui.Shift
	case "button":
		return false
	}
	return mods == 0
}

// trimAIRecords are the sessions kept: the newest aiRecordsMax, newest
// first.
func trimAIRecords(rs []AIRecord) []AIRecord {
	rs = slices.Clone(rs)
	slices.SortStableFunc(rs, func(x, y AIRecord) int { return cmp.Compare(y.Updated, x.Updated) })
	return rs[:min(len(rs), aiRecordsMax)]
}

// aiRecent is a task a new session lists: its session ("" for a task of
// this computer's that none follows), device, task, title, state and when
// it last changed (Unix seconds).
type aiRecent struct {
	id, device, title, state string
	task                     uint64
	at                       int64
}

// recentAI are the tasks a new session lists, newest first, at most n: the
// sessions kept here that started one, and this computer's tasks that no
// session follows (started by a controller, by ctl, by an older Kyokai).
func recentAI(records []AIRecord, local []localapi.AITask, n int) []aiRecent {
	var out []aiRecent
	for _, r := range records {
		if r.Task != 0 {
			out = append(out, aiRecent{id: r.ID, device: r.Device, title: r.Title, state: r.State, task: r.Task, at: r.Updated})
		}
	}
	for _, t := range local {
		if !slices.ContainsFunc(records, func(r AIRecord) bool { return r.Device == "" && r.Task == t.ID }) {
			out = append(out, aiRecent{title: t.Instruction, state: t.State, task: t.ID, at: t.Updated})
		}
	}
	slices.SortStableFunc(out, func(x, y aiRecent) int { return cmp.Compare(y.at, x.at) })
	return out[:min(len(out), n)]
}

// aiRecord is a session's record, and whether it is kept.
func (a *App) aiRecord(id string) (AIRecord, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.prefs.AISessions, func(r AIRecord) bool { return r.ID == id })
	if i < 0 {
		return AIRecord{}, false
	}
	return a.prefs.AISessions[i], true
}

// changeAIRecords changes the sessions kept and saves them (trimAIRecords;
// only in memory with No logs on).
// Notes: call on the main thread.
func (a *App) changeAIRecords(change func(rs []AIRecord) []AIRecord) {
	a.mu.Lock()
	p := a.prefs
	a.mu.Unlock()
	p.AISessions = trimAIRecords(change(slices.Clone(p.AISessions)))
	_ = a.setPrefs(p)
}

// setAIRecord changes one session's record.
// Notes: call on the main thread.
func (a *App) setAIRecord(id string, change func(r *AIRecord)) {
	a.changeAIRecords(func(rs []AIRecord) []AIRecord {
		if i := slices.IndexFunc(rs, func(r AIRecord) bool { return r.ID == id }); i >= 0 {
			change(&rs[i])
		}
		return rs
	})
}

// newAIRecord keeps a new session on a device ("" this computer).
// Returns: its id.
// Notes: call on the main thread.
func (a *App) newAIRecord(device string) string {
	now := time.Now()
	id := strconv.FormatInt(now.UnixNano(), 36)
	a.changeAIRecords(func(rs []AIRecord) []AIRecord {
		return append(rs, AIRecord{ID: id, Device: device, Created: now.Unix(), Updated: now.Unix()})
	})
	return id
}

// bindAI makes a session follow the task its first instruction started,
// titled by that instruction unless renamed.
// Notes: call on the main thread.
func (a *App) bindAI(id string, task uint64, instruction string) {
	a.setAIRecord(id, func(r *AIRecord) {
		r.Task, r.State, r.Updated = task, "running", time.Now().Unix()
		if r.Title == "" {
			r.Title = instruction
		}
	})
}

// aiTaskSeen follows a device's task in its sessions: a pane waiting for
// its first instruction's task takes it (aiBinds), and the sessions
// following the task keep its state.
// Notes: call on the main thread.
func (a *App) aiTaskSeen(device string, task *localapi.AITask) {
	// This computer's panes take their task from its start's answer.
	for _, t := range a.aiTabsOf(device) {
		if device != "" && aiBinds(task, t.starting, t.prev) {
			t.starting = ""
			a.bindAI(t.id, task.ID, task.Instruction)
		}
	}
	a.mu.Lock()
	stale := slices.ContainsFunc(a.prefs.AISessions, func(r AIRecord) bool {
		return r.Device == device && r.Task == task.ID && (r.State != task.State || r.Title == "")
	})
	a.mu.Unlock()
	if !stale {
		return
	}
	a.changeAIRecords(func(rs []AIRecord) []AIRecord {
		for i := range rs {
			if rs[i].Device == device && rs[i].Task == task.ID {
				rs[i].State, rs[i].Updated = task.State, time.Now().Unix()
				if rs[i].Title == "" {
					rs[i].Title = task.Instruction
				}
			}
		}
		return rs
	})
}

// aiTabsOf are a device's AI panes open ("" this computer).
func (a *App) aiTabsOf(device string) []*aiTab {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*aiTab
	for _, t := range a.aiTabs {
		if t.device == device {
			out = append(out, t)
		}
	}
	return out
}

// aiLinkOf is the link to a host, or nil.
func (a *App) aiLinkOf(device string) *aiLink {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.aiLinks[device]
}

// redrawAI redraws the windows that show a device's AI panes and names
// them again: a session's title, or its host's name once its link learns
// it, may have changed.
func (a *App) redrawAI(device string) {
	var wins []*viewerWindow
	for _, t := range a.aiTabsOf(device) {
		if w := t.win.Load(); w != nil && w.win != nil && !slices.Contains(wins, w) {
			wins = append(wins, w)
		}
	}
	for _, w := range wins {
		w.win.Update(w.retitle)
	}
}

// ensureAILoaded loads this computer's AI settings and tasks once.
func (a *App) ensureAILoaded() {
	a.mu.Lock()
	load := !a.ai.loaded && !a.busy["ai"] && a.connected
	a.mu.Unlock()
	if load {
		a.loadAI()
	}
}

// deviceName is a device's name as this computer shows it — the name its
// user gave it, else its own — or else its Device ID in groups.
func (a *App) deviceName(device string) string {
	about := a.recentAbout(device)
	a.mu.Lock()
	about = a.prefs.named(device, about)
	a.mu.Unlock()
	if about.Name != "" {
		return about.Name
	}
	return groupDigits(device)
}

// openAISession shows an AI session in a pane: the open pane of session
// id, else a pane for it — a new session when id is "" — on a device (""
// this computer; a host by its Device ID, dialed at address when set),
// beside a pane on side z when one is given.
// Returns: the pane.
// Notes: call on the main thread.
func (a *App) openAISession(device, address, id string, beside tab, z dropZone) *aiTab {
	if t, ok := a.tabFor(aiKey(id)).(*aiTab); ok && id != "" {
		if beside != nil && beside != tab(t) {
			a.dropTab(t.tabKey(), a.main, beside, z)
		} else if w := t.tabWindow(); w != nil {
			w.show(t)
		}
		return t
	}
	if id == "" {
		id = a.newAIRecord(device)
	}
	t := &aiTab{app: a, id: id, device: device}
	a.mu.Lock()
	a.aiTabs[t.tabKey()] = t
	a.mu.Unlock()
	if device == "" {
		a.ensureAILoaded()
	} else {
		a.useAILink(device, address)
	}
	a.main.addAt(t, beside, z)
	return t
}

// protectedSession is the AI session protected mode shows in place of the
// panes: one with this computer's AI, kept for the app's run.
// Notes: call on the main thread.
func (a *App) protectedSession() *aiTab {
	if a.protectedAI == nil {
		a.protectedAI = &aiTab{app: a, id: a.newAIRecord("")}
		a.ensureAILoaded()
	}
	return a.protectedAI
}

// openAIBeside shows a desktop's AI at its right: the device's newest
// session pane open, else a new session.
// Notes: call on the main thread.
func (a *App) openAIBeside(v *viewer) {
	v.mu.Lock()
	address := v.address
	v.mu.Unlock()
	id := ""
	for _, t := range a.aiTabsOf(v.device) {
		id = max(id, t.id) // ids grow with time
	}
	a.openAISession(v.device, address, id, v, zoneRight)
}

// openAITask shows the session following one of a device's tasks: the one
// kept, else a new one titled by its instruction.
// Notes: call on the main thread.
func (a *App) openAITask(device string, task uint64, instruction string) {
	a.mu.Lock()
	i := slices.IndexFunc(a.prefs.AISessions, func(r AIRecord) bool { return r.Device == device && r.Task == task })
	id := ""
	if i >= 0 {
		id = a.prefs.AISessions[i].ID
	}
	a.mu.Unlock()
	if id == "" {
		id = a.newAIRecord(device)
		a.bindAI(id, task, instruction)
	}
	a.openAISession(device, "", id, nil, zoneMiddle)
}

// tabKey is the pane's identity: its session's.
func (t *aiTab) tabKey() string { return aiKey(t.id) }

// title is the session's name: its title, else New AI task.
func (t *aiTab) title() string {
	if r, ok := t.app.aiRecord(t.id); ok && r.Title != "" {
		return r.Title
	}
	return L.AINewSession()
}

// where names the device the session runs on.
func (t *aiTab) where() string {
	if t.device == "" {
		return L.PickThisComputer()
	}
	return t.app.deviceName(t.device)
}

// tabTitle names the window while the pane is in focus.
func (t *aiTab) tabTitle() string { return L.AIPaneTitle(t.where(), t.title()) }

// head is the pane's header as any pane's: the AI icon and the device's
// name, then the session's title where other panes say their kind.
func (t *aiTab) head() paneHead {
	title := t.title()
	return paneHead{icon: navAI, title: t.where(), sub: title, kind: title}
}

// moveTo makes a new session run on another device ("" this computer),
// its link going with it.
// Notes: call on the main thread, before the session's first task.
func (t *aiTab) moveTo(device string) {
	a := t.app
	if device == t.device {
		return
	}
	if t.device != "" {
		a.releaseAILink(t.device)
	}
	a.mu.Lock()
	t.device = device // under a.mu: aiTabsOf reads it from other goroutines
	a.mu.Unlock()
	a.setAIRecord(t.id, func(r *AIRecord) { r.Device = device })
	if device == "" {
		a.ensureAILoaded()
	} else {
		a.useAILink(device, "")
	}
	if w := t.tabWindow(); w != nil {
		w.retitle()
	}
}

// tabWindow is the window the pane is in, or nil.
func (t *aiTab) tabWindow() *viewerWindow { return t.win.Load() }

// setTabWindow notes the window the pane moved to.
func (t *aiTab) setTabWindow(w *viewerWindow) { t.win.Store(w) }

// live reports whether the pane holds a host's link that is under way or
// connected: closing it ends the link with the last pane, which pauses the
// host's task it started. A session on this computer holds no connection.
func (t *aiTab) live() bool {
	if t.device == "" {
		return false
	}
	l := t.app.aiLinkOf(t.device)
	if l == nil {
		return false
	}
	switch l.snapshot().phase.State {
	case "connecting", "confirming", "connected", "reconnecting":
		return true
	}
	return false
}

// disconnect closes the pane: it gives back its host's link, and a session
// that never started a task is not kept.
func (t *aiTab) disconnect() {
	t.win.Store(nil)
	a := t.app
	a.mu.Lock()
	delete(a.aiTabs, t.tabKey())
	a.mu.Unlock()
	if t.device != "" {
		a.releaseAILink(t.device)
	}
	if r, ok := a.aiRecord(t.id); ok && r.Task == 0 {
		a.changeAIRecords(func(rs []AIRecord) []AIRecord {
			return slices.DeleteFunc(rs, func(r AIRecord) bool { return r.ID == t.id })
		})
	}
}

// syncGrab does nothing: the pane sends no keys to another computer.
func (t *aiTab) syncGrab(bool) {}

// endGrab does nothing.
func (t *aiTab) endGrab() {}

// setFront does nothing: the pane exchanges no clipboard.
func (t *aiTab) setFront(bool) {}

// aiPaneState is what a session's pane shows, gathered once a frame: the
// task it follows (nil until seen, or once a host moved on), the device's
// other task while one works, whether a task can start or be continued
// and why not in words, whether approvals may be answered here, the shared
// draft of the device's next task, this computer's AI settings, and its
// host's link (nil for this computer).
type aiPaneState struct {
	task, other *localapi.AITask
	ready       bool
	why         string
	approve     bool
	draft       string
	settings    *localapi.AISettings
	link        *aiLinkView
}

// state gathers the pane's aiPaneState.
func (t *aiTab) state(rec AIRecord) aiPaneState {
	a := t.app
	var s aiPaneState
	active := func(task *localapi.AITask) bool { return task != nil && !aiFinished(task.State) }
	if t.device != "" {
		l := a.aiLinkOf(t.device)
		if l == nil {
			return s
		}
		v := l.snapshot()
		s.link, s.approve, s.draft = &v, v.approve, v.draft
		if v.task != nil && v.task.ID == rec.Task && rec.Task != 0 {
			s.task = v.task
		} else if active(v.task) {
			s.other = v.task
		}
		name := t.where()
		switch {
		case v.phase.State == "connecting" || v.phase.State == "":
			s.why = L.AILinkConnecting(name)
		case v.phase.State == "confirming":
			s.why = L.AILinkConfirming(name)
		case v.phase.State == "reconnecting":
			s.why = L.AILinkConnecting(name)
		case v.phase.State == "connected" && !v.run:
			s.why = L.AILinkNoAI(name)
		case v.phase.State != "connected":
			s.why = v.err
			if s.why == "" {
				s.why = sessionState(v.phase, helperGap{})
			}
		default:
			s.ready = true
		}
		return s
	}
	a.mu.Lock()
	connected, settings, draft := a.connected, a.ai.settings, a.ai.draft
	var activeID uint64
	if a.status != nil && a.status.AI != nil {
		activeID = a.status.AI.ID
	}
	for i := range a.ai.tasks {
		task := &a.ai.tasks[i]
		switch {
		case task.ID == rec.Task && rec.Task != 0:
			s.task = task
		case task.ID == activeID:
			s.other = task
		}
	}
	a.mu.Unlock()
	s.approve, s.draft, s.settings = true, draft, settings
	switch {
	case !connected:
		s.why = L.EngineDown()
	case settings == nil:
		// Loading.
	case !aiReady(settings):
		s.why = L.AISetUpFirst()
	default:
		s.ready = true
	}
	return s
}

// typed follows what is typed in the box: the shared draft of the
// device's next task, or the reply.
func (t *aiTab) typed(mode boxMode, text string) {
	a := t.app
	if mode == boxReply {
		t.reply = text
		return
	}
	if t.device != "" {
		if l := a.aiLinkOf(t.device); l != nil {
			l.setDraft(text)
		}
		return
	}
	a.mu.Lock()
	a.ai.draft, a.ai.draftAt = text, time.Now()
	a.mu.Unlock()
	go func() {
		_, _ = a.requestWith(localapi.Request{Type: localapi.RequestAIDraft, AITask: &localapi.AITaskRequest{Text: text}})
	}()
}

// setDraft puts a suggestion in the box of a new session.
func (t *aiTab) setDraft(text string) { t.typed(boxStart, text) }

// submit sends what the box holds: a new task, which the session follows,
// or a reply to the task that is done.
// Returns: whether it was sent (the box then empties).
// Notes: call on the main thread.
func (t *aiTab) submit(mode boxMode, s aiPaneState, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || mode == boxWorking {
		return false
	}
	a := t.app
	var after int64
	if s.task != nil && len(s.task.Steps) > 0 {
		after = s.task.Steps[len(s.task.Steps)-1].At
	}
	if mode == boxReply {
		t.sent = append(t.sent, aiSent{text: text, after: after})
		t.reply = ""
		if t.device == "" {
			a.aiReply(s.task.ID, text)
		} else if l := a.aiLinkOf(t.device); l != nil {
			l.send(map[string]any{"type": "ai-command", "id": s.task.ID, "command": "reply", "text": text})
		}
		return true
	}
	// A key makes a start that is sent again start one task.
	key := strconv.FormatInt(time.Now().UnixNano(), 36)
	t.sent = nil
	a.setAIRecord(t.id, func(r *AIRecord) {
		if r.Title == "" {
			r.Title = text
		}
		r.Task, r.State, r.Updated = 0, "", time.Now().Unix()
	})
	if t.device != "" {
		l := a.aiLinkOf(t.device)
		if l == nil {
			return false
		}
		t.starting, t.prev = text, 0
		if v := l.snapshot(); v.task != nil {
			t.prev = v.task.ID
		}
		l.setDraft("")
		l.send(map[string]any{"type": "ai-start", "key": key, "instruction": text})
		return true
	}
	t.starting = text
	a.mu.Lock()
	a.busy["ai-task"], a.ai.draft = true, ""
	a.mu.Unlock()
	id := t.id
	go func() {
		resp, err := a.requestWith(localapi.Request{Type: localapi.RequestAITaskStart,
			AITask: &localapi.AITaskRequest{Key: key, Instruction: text}})
		a.update(func() {
			delete(a.busy, "ai-task")
			if a.aiFailed(err) {
				a.ai.draft = text // kept to try again
			}
		})
		a.later(func() {
			t.starting = ""
			if err == nil {
				a.bindAI(id, resp.ID, text)
			}
			a.redrawAI("")
		})
		a.refreshAITasks()
	}()
	return true
}

// command pauses, resumes or cancels the session's task.
func (t *aiTab) command(id uint64, command string) {
	if t.device != "" {
		if l := t.app.aiLinkOf(t.device); l != nil {
			l.send(map[string]any{"type": "ai-command", "id": id, "command": command})
		}
		return
	}
	req := map[string]localapi.RequestType{"pause": localapi.RequestAITaskPause, "resume": localapi.RequestAITaskResume,
		"cancel": localapi.RequestAITaskCancel}[command]
	t.app.aiCommand(req, id)
}

// decide answers the task's approval.
func (t *aiTab) decide(id uint64, approve bool) {
	if t.device != "" {
		if l := t.app.aiLinkOf(t.device); l != nil {
			l.send(map[string]any{"type": "ai-decide", "id": id, "approve": approve})
		}
		return
	}
	t.app.aiDecide(id, approve)
}

// rename gives the session a title (an empty one keeps the title it has).
func (t *aiTab) rename(name string) {
	t.renaming = nil
	if name = strings.TrimSpace(name); name == "" {
		return
	}
	t.app.setAIRecord(t.id, func(r *AIRecord) { r.Title = name })
	if w := t.tabWindow(); w != nil {
		w.retitle()
	}
}

// watchScreen opens a host's desktop beside its AI pane (or brings it
// beside, when open).
// Notes: call on the main thread.
func (a *App) watchScreen(t *aiTab) {
	a.connectDevice(t.device, "", a.recentAbout(t.device))
	if v := a.tabFor(t.device); v != nil && v != tab(t) {
		a.dropTab(t.device, a.main, t, zoneLeft)
	}
}
