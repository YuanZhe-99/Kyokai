package gui

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// view is an AI session's pane (doc/en-us/gui.md, "AI sessions"): a new
// session's box centred under what the AI should do there, with
// suggestions and the recent tasks; else the conversation, and the box
// under it.
func (t *aiTab) view(c *ui.Context) {
	th := c.Theme()
	rec, ok := t.app.aiRecord(t.id)
	if !ok {
		return
	}
	if t.device == "" {
		t.app.ensureAILoaded()
	}
	s := t.state(rec)
	ui.Column(c.Key("ai-" + t.id)).Fill().Background(th.Background).Children(func() {
		if rec.Task == 0 && t.starting == "" {
			t.newView(c, rec, s)
			return
		}
		ui.Scroll(c.Key("ai-conv-"+t.id)).Grow(1).FillWidth().MinHeight(0).Padding(20, 24, 0, 24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(aiColumnW).Gap(16).Padding(0, 0, 12, 0).Children(func() { t.conversation(c, rec, s) })
		})
		ui.Column(c.Key("ai-foot-"+t.id)).FillWidth().Shrink(0).Padding(12, 24, 16, 24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(aiColumnW).Gap(6).Children(func() { t.box(c, rec, s) })
		})
	})
}

// newView is a new session: the AI's mark, what it should do on the
// device and how it works there, the box, three suggestions that fill it,
// and the recent tasks — this computer's and those of the devices it
// reached (recentAI) — centred in the pane.
func (t *aiTab) newView(c *ui.Context, rec AIRecord, s aiPaneState) {
	th := c.Theme()
	a := t.app
	sc := ui.Scroll(c.Key("ai-new-"+t.id)).Fill().Padding(0, 24, 24, 24)
	h := max(sc.LayoutBounds().H-24, 0)
	sc.Children(func() {
		ui.Column(c).FillWidth().MinHeight(h).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(aiNewW).Children(func() {
				ask, about := L.AIAskHere(), L.AIAskHereAbout()
				if t.device != "" {
					ask, about = L.AIAskThere(t.where()), L.AIAskThereAbout(t.where())
				}
				ui.Column(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(aiMarkSize, aiMarkSize).Radius(aiMarkSize/2).Center().Margin(0, 0, 8, 0).Background(th.Accent.Alpha(0.12)).Children(func() {
						ui.Icon(c, navAI).FontSize(sizeAIAsk).TextColor(th.Accent)
					})
					ui.Text(c, ask).FontSize(sizeAIAsk).FontWeight(600).TextAlign(ui.Center)
					ui.Text(c, about).TextColor(th.TextMuted).TextAlign(ui.Center)
				})
				ui.Column(c).FillWidth().Gap(6).Margin(22, 0, 0, 0).Children(func() { t.box(c, rec, s) })
				ui.Row(c.Key("ai-suggest-"+t.id)).FillWidth().Gap(8).Wrap().Justify(ui.Center).Margin(18, 0, 0, 0).Children(func() {
					for i, sg := range []struct {
						icon *ui.SVG
						text string
					}{{rowFolder, L.AISuggestOne()}, {aiImage, L.AISuggestTwo()}, {aiPrinter, L.AISuggestThree()}} {
						if aiSuggestion(c, "ai-suggest-"+t.id+"-"+strconv.Itoa(i), sg.icon, sg.text) {
							t.setDraft(sg.text)
						}
					}
				})
				a.mu.Lock()
				recent := recentAI(a.prefs.AISessions, a.ai.tasks, aiRecentShown)
				a.mu.Unlock()
				if len(recent) == 0 {
					return
				}
				ui.Text(c, L.AIRecentTitle()).FontSize(sizeSmall).FontWeight(600).TextColor(th.TextMuted).Margin(30, 10, 6, 10)
				for _, r := range recent {
					if !t.recentRow(c, r) {
						continue
					}
					if r.id != "" {
						a.openAISession(r.device, "", r.id, nil, zoneMiddle)
					} else {
						a.openAITask(r.device, r.task, r.title)
					}
				}
			})
		})
	})
}

// aiSuggestion is a suggestion under a new session's box: its icon and its
// words. Returns: whether it was clicked.
func aiSuggestion(c *ui.Context, key string, icon *ui.SVG, text string) bool {
	th := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Height(aiSuggestH).Padding(0, 12).Radius(10).Border(1, th.Border).Background(aiRaised(th)).Label(text)
	if b.Hovered() {
		b.Background(th.SurfaceHover)
	}
	b.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).FontSize(statusIconSize).TextColor(th.TextMuted)
			ui.Text(c, text).FontSize(sizeSmall).SingleLine()
		})
	})
	return b.Clicked()
}

// recentRow is a recent task: its title, where it ran, and its state —
// waiting for you as a pill, else in a word with when. Returns: whether it
// was clicked.
func (t *aiTab) recentRow(c *ui.Context, r aiRecent) bool {
	th := c.Theme()
	b := ui.ButtonBase(c.Key("ai-recent-"+t.id+"-"+r.device+"-"+strconv.FormatUint(r.task, 10))).Height(aiRecentH).FillWidth().Padding(0, 10).Radius(9).Label(r.title)
	if b.Hovered() {
		b.Background(th.SurfaceHover)
	}
	where := L.PickThisComputer()
	if r.device != "" {
		where = t.app.deviceName(r.device)
	}
	b.Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, navAI).FontSize(navIconSize).TextColor(th.TextMuted)
			ui.Text(c, r.title).SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, where).FontSize(sizeSmall).TextColor(th.TextMuted).SingleLine().Width(aiRecentWhereW)
			ui.Row(c).Width(aiRecentStateW).Justify(ui.End).Children(func() {
				if r.state == "approval" {
					statePill(c, L.AIWaitingForYou(), th.Warning)
					return
				}
				ui.Text(c, aiStateWord(r.state)+" · "+aiWhen(r.at, time.Now())).FontSize(sizeSmall).TextColor(th.TextMuted).SingleLine()
			})
		})
	})
	return b.Clicked()
}

// conversation is the session's task: your instruction, the model's steps
// in groups between the replies sent here — each group folding to its
// count —, the model's text, what it is doing, a step waiting for your OK,
// and how it ended. It follows its end as it grows.
func (t *aiTab) conversation(c *ui.Context, rec AIRecord, s aiPaneState) {
	th := c.Theme()
	task := s.task
	if task == nil {
		// Its first instruction is on its way, or the device moved on (a
		// host shows its newest task only; this computer forgot it on a
		// restart): what is kept of it.
		aiBubble(c, "ai-you-"+t.id, rec.Title)
		if t.starting != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				busyMark(c, L.AIStarting(), busySize)
				ui.Text(c, L.AIStarting()).FontSize(sizeSmall).TextColor(th.TextMuted)
			})
			return
		}
		ui.Text(c, aiStateWord(rec.State)+" · "+aiWhen(rec.Updated, time.Now())).FontSize(sizeSmall).TextColor(th.TextMuted)
		return
	}
	aiBubble(c, "ai-you-"+t.id, task.Instruction)
	if task.Origin != "" && t.device == "" {
		ui.Text(c, L.AITaskFrom(groupDigits(task.Origin))).FontSize(sizeSmall).TextColor(th.TextMuted)
	}
	steps := task.Steps[max(len(task.Steps)-aiStepsListed, 0):]
	start := 0
	// Each reply follows the answer it was sent to; the task's text is shown
	// last only once it is a new answer, not that one again.
	for i, r := range t.sent {
		end := start
		for end < len(steps) && steps[end].At <= r.after {
			end++
		}
		t.stepsGroup(c, i, steps[start:end])
		if r.answer != "" {
			ui.Text(c.Key("ai-answer-"+t.id+"-"+strconv.Itoa(i)), r.answer).Selectable().FillWidth()
		}
		aiBubble(c, "ai-reply-"+t.id+"-"+strconv.Itoa(i), r.text)
		start = end
	}
	t.stepsGroup(c, len(t.sent), steps[start:])
	if task.Text != "" && (len(t.sent) == 0 || task.Text != t.sent[len(t.sent)-1].answer) {
		ui.Text(c, task.Text).Selectable().FillWidth()
	}
	if n := len(task.Progress); n > 0 && task.State == "running" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			busyMark(c, task.Progress[n-1], busySize)
			ui.Text(c, task.Progress[n-1]).FontSize(sizeSmall).TextColor(th.TextMuted)
		})
	}
	if task.Approval != nil {
		t.approval(c, task, s.approve)
	}
	end := ui.Row(c.Key("ai-state-" + t.id)).Gap(8).AlignItems(ui.Center)
	end.Children(func() {
		word, col := aiStateLabel(task), th.TextMuted
		switch task.State {
		case "approval":
			return // the step waiting says it
		case "running":
			if len(task.Progress) > 0 {
				return // the note says it
			}
			busyMark(c, word, busySize)
		case "done":
			col = th.Success
		case "failed":
			col = th.Danger
		case "paused":
			col = th.Warning
		}
		ui.Text(c, word).FontSize(sizeSmall).FontWeight(600).TextColor(textTone(th, col))
		if task.Model != "" {
			model := friendlyModelName(task.Model, "", "")
			if t.device == "" {
				model = aiTaskModel(s.settings, task)
			}
			ui.Text(c, model).FontSize(sizeSmall).TextColor(th.TextMuted).SingleLine().MinWidth(0)
		}
	})
	if task.Error != "" {
		ui.Text(c, task.Error).FontSize(sizeSmall).TextColor(th.TextMuted).Selectable()
	}
	if seen := strconv.Itoa(len(task.Steps)) + task.State + strconv.Itoa(len(t.sent)); seen != t.seen {
		t.seen = seen
		end.ScrollIntoView()
	}
}

// aiBubble is a message of yours in the conversation: on the right, on the
// pane's quiet face.
func aiBubble(c *ui.Context, key, text string) {
	th := c.Theme()
	ui.Row(c.Key(key)).FillWidth().Children(func() {
		ui.Box(c).Grow(1)
		ui.Text(c, text).Selectable().MaxWidth(aiBubbleMaxW).Padding(10, 14).Radius(aiBubbleRadius).Background(tileBackground(th))
	})
}

// stepsGroup is the steps between two of your messages: a header with
// their count, which folds them away, then each step (aiStepRow).
func (t *aiTab) stepsGroup(c *ui.Context, i int, steps []localapi.AIStep) {
	if len(steps) == 0 {
		return
	}
	th := c.Theme()
	key := "ai-steps-" + t.id + "-" + strconv.Itoa(i)
	folded := t.folded[i]
	ui.Column(c.Key(key)).FillWidth().Gap(2).Padding(4, 0, 0, 2).AlignItems(ui.Start).Children(func() {
		count := L.AIStepsCount(len(steps))
		head := ui.ButtonBase(c.Key(key+"-head")).Padding(2, 6, 2, 2).Radius(6).Margin(0, 0, 4, 0).Label(count)
		if head.Hovered() {
			head.Background(th.SurfaceHover)
		}
		icon := aiChevron
		if folded {
			icon = aiChevronRight
		}
		head.Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon).FontSize(statusIconSize).TextColor(th.TextMuted)
				ui.Text(c, count).FontSize(sizeSmall).FontWeight(500).TextColor(th.TextMuted)
			})
		})
		if head.Clicked() {
			if t.folded == nil {
				t.folded = map[int]bool{}
			}
			t.folded[i] = !folded
		}
		if folded {
			return
		}
		for j, st := range steps {
			aiStepRow(c, key+"-"+strconv.Itoa(j), st)
		}
	})
}

// aiStepRow is one of a task's steps: a round mark of what became of it
// (done, did not run as asked, left out), what it did in words, what became
// of it when not done as asked, and when.
func aiStepRow(c *ui.Context, key string, st localapi.AIStep) {
	th := c.Theme()
	mark, col := rowCheck, th.Success
	switch st.Outcome {
	case "ok":
	case "failed", "declined":
		mark, col = paneClose, th.Warning
	default:
		mark, col = aiDash, th.TextMuted
	}
	ui.Row(c.Key(key)).FillWidth().MinHeight(aiStepRowH).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(aiStepMark, aiStepMark).Shrink(0).Radius(aiStepMark / 2).Center().Background(col.Alpha(0.15)).Children(func() {
			ui.Icon(c, mark).FontSize(aiStepGlyph).TextColor(col)
		})
		ui.Text(c, describeAction(st.Action)).Grow(1).MinWidth(0)
		if o := outcomeLabel(st.Outcome); o != "" {
			ui.Text(c, o).FontSize(sizeSmall).TextColor(textTone(th, th.Warning)).Shrink(0)
		}
		if st.At > 0 {
			ui.Text(c, time.Unix(st.At, 0).Local().Format("15:04")).FontSize(sizeSmall).FontFeatures("tnum").TextColor(th.TextMuted).Shrink(0)
		}
	})
}

// approval is the step waiting for an OK, inline: what it would do, the
// provider's notes, Allow and Deny and that moving the mouse there pauses
// the AI at once — or, where approvals are the host user's, that they
// decide.
func (t *aiTab) approval(c *ui.Context, task *localapi.AITask, may bool) {
	th := c.Theme()
	key := "ai-approve-" + t.id
	ui.Column(c.Key(key)).FillWidth().Gap(6).Padding(14, 16).Radius(aiCardRadius).Border(1, th.Accent).Background(th.Accent.Alpha(0.10)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, aiShield).FontSize(navIconSize).TextColor(th.Accent)
			ui.Text(c, L.AIApprovalTitle()).FontWeight(600)
		})
		ui.Text(c, describeAction(task.Approval.Action))
		if len(task.Approval.Flagged) > 0 {
			ui.Text(c, L.AIApprovalFlagged()).FontSize(sizeSmall).TextColor(textTone(th, th.Warning))
			for i, f := range task.Approval.Flagged {
				ui.Text(c.Key(key+"-f"+strconv.Itoa(i)), f).FontSize(sizeSmall).Selectable()
			}
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Margin(6, 0, 0, 0).Children(func() {
			if !may {
				ui.Text(c, L.ViewerAIHostApproves()).FontSize(sizeSmall).TextColor(th.TextMuted)
				return
			}
			if ui.PrimaryButton(c.Key(key+"-allow"), L.ActionAllow()).Clicked() {
				t.decide(task.ID, true)
			}
			if ui.Button(c.Key(key+"-deny"), L.ActionDeny()).Clicked() {
				t.decide(task.ID, false)
			}
			note := L.AIApprovalNoteHere()
			if t.device != "" {
				note = L.AIApprovalNoteThere(t.where())
			}
			ui.Text(c, note).FontSize(sizeSmall).TextColor(th.TextMuted).Margin(0, 0, 0, 6).MinWidth(0)
		})
	})
}

// boxMode is what the prompt box does: start a task, continue the one
// that is done, or nothing while the task works.
type boxMode int

const (
	boxStart boxMode = iota
	boxReply
	boxWorking
)

// box is the prompt box: what the device is busy with or why a task
// cannot start, then the text (Enter and Shift+Enter as Prefs.SendKey
// says, which its hint repeats) with Send — or Pause or Resume and Stop
// while the task works —, and the chips under it.
func (t *aiTab) box(c *ui.Context, rec AIRecord, s aiPaneState) {
	th := c.Theme()
	a := t.app
	mode := boxStart
	switch {
	case s.task != nil && !aiFinished(s.task.State):
		mode = boxWorking
	case s.task != nil && s.task.State == "done":
		mode = boxReply
	}
	if t.starting != "" {
		mode = boxWorking
	}
	busy := mode == boxStart && s.other != nil
	if busy {
		ui.Row(c.Key("ai-busy-" + t.id)).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			text := L.AIBusyHere()
			if t.device != "" {
				text = L.AIBusyThere(t.where())
			}
			ui.Text(c, text).FontSize(sizeSmall).TextColor(textTone(th, th.Warning)).Grow(1)
			if ui.Button(c.Key("ai-busy-show-"+t.id), L.AIShowTask()).Clicked() {
				a.openAITask(t.device, s.other.ID, s.other.Instruction)
			}
		})
	}
	if !s.ready && s.why != "" && mode != boxWorking {
		ui.Row(c.Key("ai-why-" + t.id)).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.why).FontSize(sizeSmall).TextColor(th.TextMuted).Grow(1)
			switch {
			case t.device == "" && s.settings != nil:
				if ui.Button(c.Key("ai-setup-"+t.id), L.NavSettings()).Clicked() {
					a.openSettings(pageAI)
				}
			case s.link != nil && (s.link.phase.State == "failed" || s.link.phase.State == "closed" || s.link.phase.State == "connected"):
				if ui.Button(c.Key("ai-redial-"+t.id), L.ViewerReconnect()).Clicked() {
					if l := a.aiLinkOf(t.device); l != nil {
						l.dial()
					}
				}
			}
		})
	}
	text, hint := s.draft, L.AIPromptHint()
	switch {
	case mode == boxReply:
		text, hint = t.reply, L.AIReplyHint()
	case mode == boxWorking:
		text, hint = "", L.AIWorkingHint()
	case s.task != nil:
		hint = L.AIRestartHint()
	}
	canSend := mode != boxWorking && !busy && s.ready
	a.mu.Lock()
	pref := a.prefs.SendKey
	a.mu.Unlock()
	frame := ui.Column(c.Key("ai-box-"+t.id)).FillWidth().Gap(6).Padding(12, 12, 10, 16).Radius(aiBoxRadius).Background(aiRaised(th)).Border(1, th.Border).Shadow(0, 2, 12, 0, paneShadow)
	frame.Children(func() {
		in := ui.TextAreaBase(c.Key("ai-box-in-"+t.id), &text).FillWidth().MinHeight(aiBoxMinH).MaxHeight(aiBoxMaxH).Placeholder(hint).Label(hint).Disabled(mode == boxWorking)
		if mode != boxWorking {
			in.AutoFocus()
		}
		if in.Changed() {
			t.typed(mode, text)
		}
		in.OnTextKey(func(k ui.TextKey) bool {
			if k.Key != ui.KeyEnter || !sendsOnEnter(pref, k.Mods) {
				return false
			}
			if canSend && t.submit(mode, s, k.Text()) {
				k.SetText("")
			}
			return true
		})
		ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
			if h := aiSendHint(pref, mode); h != "" {
				ui.Text(c, h).FontSize(sizeSmall).TextColor(th.TextMuted).SingleLine().MinWidth(0)
			}
			ui.Box(c).Grow(1)
			if mode == boxWorking && s.task != nil {
				icon, label, command := aiPause, L.AIActionPause(), "pause"
				if s.task.State == "paused" {
					icon, label, command = aiResume, L.AIActionResume(), "resume"
				}
				if iconButton(c, "ai-"+command+"-"+t.id, icon, label) {
					t.command(s.task.ID, command)
				}
				if aiRoundButton(c, "ai-stop-"+t.id, aiStop, L.AIActionStop(), th.Danger, true) {
					t.command(s.task.ID, "cancel")
				}
				return
			}
			if aiRoundButton(c, "ai-send-"+t.id, aiSend, L.AIActionSend(), th.Accent, canSend && strings.TrimSpace(text) != "") {
				t.submit(mode, s, text)
			}
		})
	})
	t.chips(c, rec, s)
}

// aiSendHint is what the box says sends it, as the send-key preference
// has it; nothing while the task works or when only Send sends.
func aiSendHint(pref string, mode boxMode) string {
	if mode == boxWorking {
		return ""
	}
	start := mode == boxStart
	switch {
	case pref == "button":
		return ""
	case pref == "shift-enter" && start:
		return L.AIHintStartShift()
	case pref == "shift-enter":
		return L.AIHintSendShift()
	case start:
		return L.AIHintStart()
	}
	return L.AIHintSend()
}

// chips are the line under the box: when to ask, where the task runs (a
// new session's to choose), and its model — on this computer menus that
// change its AI settings; on a host, what is set there.
func (t *aiTab) chips(c *ui.Context, rec AIRecord, s aiPaneState) {
	th := c.Theme()
	a := t.app
	var where func(m *ui.Menu)
	if rec.Task == 0 && t.starting == "" {
		where = t.whereMenu
	}
	ui.Row(c.Key("ai-chips-" + t.id)).FillWidth().Gap(2).AlignItems(ui.Center).Children(func() {
		if t.device != "" {
			dot := th.TextMuted
			if s.link != nil && s.link.phase.State == "connected" {
				dot = th.Success
			}
			aiChip(c, "ai-chip-ask-"+t.id, aiShield, ui.Transparent, L.AIApprovalOnHost(t.where()), nil)
			aiChip(c, "ai-chip-where-"+t.id, nil, dot, L.AIRunsOn(t.where()), where)
			ui.Box(c).Grow(1)
			text := L.AIKeyOf(t.where())
			if s.task != nil && s.task.Model != "" {
				text = L.AIModelKeyOf(t.where(), friendlyModelName(s.task.Model, "", ""))
			}
			aiChip(c, "ai-chip-model-"+t.id, nil, ui.Transparent, text, nil)
			return
		}
		set := s.settings
		if set != nil {
			aiChip(c, "ai-chip-ask-"+t.id, aiShield, ui.Transparent, L.AIApproval(), func(m *ui.Menu) {
				for _, p := range []struct{ id, label string }{{"flagged", L.AIApprovalFlaggedOption()}, {"every-action", L.AIApprovalEveryOption()}} {
					on := set.Approval == p.id || set.Approval == "" && p.id == "flagged"
					if m.Item(p.label).Checked(on).Chosen() && !on {
						a.saveAISettings(&localapi.AISettings{Approval: p.id}, "")
					}
				}
			})
		}
		aiChip(c, "ai-chip-where-"+t.id, kindDesktop, ui.Transparent, L.PickThisComputer(), where)
		ui.Box(c).Grow(1)
		if set == nil {
			return
		}
		labels, refs, cur := aiModelChoices(set)
		if len(labels) == 0 {
			return
		}
		if cur == "" {
			cur = L.AIModel()
		}
		aiChip(c, "ai-chip-model-"+t.id, nil, ui.Transparent, cur, func(m *ui.Menu) {
			for i, label := range labels {
				if m.Item(label).Checked(label == cur).Chosen() && label != cur {
					ref := refs[i]
					a.saveAISettings(&localapi.AISettings{Model: &ref}, "")
				}
			}
		})
	})
}

// whereMenu chooses the device a new session runs on: this computer, or
// one this computer connected to (moveTo).
func (t *aiTab) whereMenu(m *ui.Menu) {
	a := t.app
	if m.Item(L.PickThisComputer()).Checked(t.device == "").Chosen() {
		t.moveTo("")
	}
	a.mu.Lock()
	var rows []deviceRow
	if a.status != nil {
		rows = deviceRows(a.status, a.prefs)
	}
	a.mu.Unlock()
	if len(rows) > 0 {
		m.Separator()
	}
	for _, r := range rows {
		if m.Item(r.name()).Checked(t.device == r.id).Chosen() {
			t.moveTo(r.id)
		}
	}
}

// aiChip is a chip under an AI session's box: an icon or a dot, its words
// and, when build fills a menu, a chevron (a menu button); words only
// without one.
func aiChip(c *ui.Context, key string, icon *ui.SVG, dot ui.Color, text string, build func(m *ui.Menu)) {
	th := c.Theme()
	var chip ui.Element
	if build != nil {
		chip = ui.ButtonBase(c.Key(key)).Label(text).Menu(build)
		if chip.Hovered() {
			chip.Background(th.SurfaceHover)
		}
	} else {
		chip = ui.Row(c.Key(key))
	}
	chip.Height(aiChipH).Padding(0, 10).Radius(8).MinWidth(0).Tooltip(text).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			switch {
			case icon != nil:
				ui.Icon(c, icon).FontSize(statusIconSize).TextColor(th.TextMuted)
			case dot.A > 0:
				ui.Box(c).Size(onlineDot, onlineDot).Radius(onlineDot / 2).Background(dot)
			}
			ui.Text(c, text).FontSize(sizeSmall).FontWeight(500).TextColor(th.TextMuted).SingleLine()
			if build != nil {
				ui.Icon(c, aiChevron).FontSize(aiChevronSize).TextColor(th.TextMuted)
			}
		})
	})
}

// aiRoundButton is the box's round send or stop button, in col; dimmed
// and inert while off. Returns: whether it was clicked.
func aiRoundButton(c *ui.Context, key string, icon *ui.SVG, label string, col ui.Color, on bool) bool {
	th := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(aiSendSize, aiSendSize).Radius(aiSendSize / 2).Center().Shrink(0).Label(label).Tooltip(label).Disabled(!on)
	face := col
	if !on {
		face = th.TextMuted.Alpha(0.35)
	}
	b.Background(face)
	b.Children(func() { ui.Icon(c, icon).FontSize(navIconSize).TextColor(closeHoverText) })
	return b.Clicked()
}

// aiRaised is the face of what stands on an AI pane — the prompt box, the
// suggestions —: the pane's own in light, a little lighter in dark.
func aiRaised(th *ui.Theme) ui.Color {
	if th.Dark {
		return ui.RGBA(255, 255, 255, 0.04)
	}
	return th.Background
}

// titleExtras follow the session's title in its pane's header: Rename,
// and its task's state when it waits for the user or is paused.
func (t *aiTab) titleExtras(c *ui.Context) {
	th := c.Theme()
	if iconButton(c, "head-rename-"+t.id, aiRename, L.AIRename()) {
		if t.renaming == nil {
			name := ""
			if r, ok := t.app.aiRecord(t.id); ok {
				name = r.Title
			}
			t.renaming = &name
		} else {
			t.rename(*t.renaming)
		}
	}
	rec, ok := t.app.aiRecord(t.id)
	if !ok {
		return
	}
	if task := t.state(rec).task; task != nil {
		switch task.State {
		case "approval":
			statePill(c, L.AIWaitingForYou(), th.Warning)
		case "paused":
			statePill(c, L.AIStatePaused(), th.Warning)
		}
	}
}

// headButtons are the header's own buttons on a host's session: Watch its
// screen, which opens its desktop beside the session (its icon alone in a
// narrow pane).
func (t *aiTab) headButtons(c *ui.Context, narrow bool) {
	if t.device == "" {
		return
	}
	if narrow {
		if iconButton(c, "head-watch-"+t.id, kindDesktop, L.AIWatchScreen()) {
			t.app.watchScreen(t)
		}
		return
	}
	b := ui.Button(c.Key("head-watch-"+t.id), "").Label(L.AIWatchScreen()).Height(aiChipH).Padding(0, 10).Margin(0, 6, 0, 0)
	b.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, kindDesktop).FontSize(statusIconSize)
			ui.Text(c, L.AIWatchScreen()).FontSize(sizeSmall).FontWeight(500).SingleLine()
		})
	})
	if b.Clicked() {
		t.app.watchScreen(t)
	}
}

// renameField is the session's title being typed in its header: Enter
// keeps it, Escape leaves it as it was.
func (t *aiTab) renameField(c *ui.Context) {
	name := *t.renaming
	in := ui.TextInput(c.Key("head-rename-in-"+t.id), &name).Width(aiRenameW).AutoFocus().Label(L.AIRename())
	if in.Changed() {
		*t.renaming = name
	}
	if in.Submitted() {
		t.rename(name)
	}
	if in.Shortcut(0, ui.KeyEscape) {
		t.renaming = nil
	}
}

// aiStateWord is a task's state in a word, as recent tasks show it.
func aiStateWord(state string) string {
	switch state {
	case "running":
		return L.AIStateRunning()
	case "approval":
		return L.AIWaitingForYou()
	case "paused":
		return L.AIStatePaused()
	case "done":
		return L.AIStateDone()
	case "failed":
		return L.AIStateFailed()
	case "cancelled":
		return L.AIStateCancelled()
	}
	return L.AIStarting()
}

// aiWhen is when a task last changed, as recent tasks say it: the time
// today, yesterday, else the date.
func aiWhen(at int64, now time.Time) string {
	t := time.Unix(at, 0).In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return t.Format("15:04")
	case !t.Before(today.AddDate(0, 0, -1)):
		return L.AIYesterday()
	}
	return t.Format("01-02")
}
