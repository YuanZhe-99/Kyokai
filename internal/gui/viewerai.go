package gui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// Media packet kinds for the host's AI (the core's media::packet).
const (
	packetAITask  byte = 5
	packetAIError byte = 6
	packetAIDraft byte = 10
)

// draftQuiet is how long after typing a draft typed elsewhere is not shown
// over what is being typed.
const draftQuiet = time.Second

// viewerAI is the viewer's AI panel state (guarded by viewer.mu): the task
// on the host — one record for every controller, whoever started it —
// as the host last reported it, and the shared draft of the next one.
type viewerAI struct {
	// run and approve are the session's run-ai-task and approve-ai-action.
	run, approve bool
	// hidden collapses the panel; one waiting for approval shows anyway.
	hidden bool
	task   *localapi.AITask
	err    string
	draft  string
	// draftAt is when this viewer's user last typed the draft.
	draftAt time.Time
	reply   string
}

// aiShown reports whether the panel shows: the session may run AI tasks,
// and the person has not collapsed it unless a step waits for approval,
// which is never hidden.
func (st viewerAI) aiShown() bool {
	return st.run && (!st.hidden || st.task != nil && st.task.State == "approval")
}

// aiPacket takes an AI packet from the host into the panel.
func (v *viewer) aiPacket(kind byte, body []byte) {
	var task localapi.AITask
	var refused struct{ Error string }
	switch {
	case kind == packetAITask && json.Unmarshal(body, &task) == nil:
		v.update(func() {
			v.mu.Lock()
			v.ai.task, v.ai.err = &task, ""
			v.mu.Unlock()
		})
	case kind == packetAIDraft:
		var d struct{ Text string }
		if json.Unmarshal(body, &d) == nil {
			v.update(func() {
				v.mu.Lock()
				if time.Since(v.ai.draftAt) > draftQuiet {
					v.ai.draft = d.Text
				}
				v.mu.Unlock()
			})
		}
	case kind == packetAIError && json.Unmarshal(body, &refused) == nil:
		v.update(func() {
			v.mu.Lock()
			v.ai.err = refused.Error
			v.mu.Unlock()
		})
	}
}

// sendControl queues a control message (an AI request, a display choice)
// for the host.
func (v *viewer) sendControl(msg map[string]any) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case v.out <- append([]byte{packetControl}, body...):
	default:
	}
}

// aiPanel is the side panel where the person at this controller gives the
// host's AI a task and follows it.
func (v *viewer) aiPanel(c *ui.Context) {
	t := c.Theme()
	v.mu.Lock()
	st := v.ai
	v.mu.Unlock()
	ui.Scroll(c).Width(aiPanelW).FillHeight().Background(t.Surface).Padding(cardPad).Gap(gap).Children(func() {
		ui.Text(c, L.ViewerAITitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.ViewerAIHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if st.err != "" {
			ui.Text(c, st.err).FontSize(sizeSmall).TextColor(t.Danger)
		}
		task := st.task
		if task != nil {
			v.aiTask(c, task, st.approve)
		}
		if task != nil && !aiFinished(task.State) {
			return
		}
		if task != nil && task.State == "done" {
			ui.Row(c).Gap(8).FillWidth().Children(func() {
				reply := st.reply
				if ui.TextInput(c.Key("v-ai-reply"), &reply).Placeholder(L.AIReplyHint()).Grow(1).Changed() {
					v.mu.Lock()
					v.ai.reply = reply
					v.mu.Unlock()
				}
				if ui.Button(c.Key("v-ai-send"), L.AIActionSend()).Disabled(strings.TrimSpace(reply) == "").Clicked() {
					v.sendControl(map[string]any{"type": "ai-command", "id": task.ID, "command": "reply", "text": reply})
					v.mu.Lock()
					v.ai.reply = ""
					v.mu.Unlock()
				}
			})
			ui.Divider(c)
		}
		ui.Text(c, L.AINewTask()).Bold()
		draft := st.draft
		if ui.TextArea(c.Key("v-ai-draft"), &draft).FillWidth().MinHeight(inputAreaH).Changed() {
			v.mu.Lock()
			v.ai.draft, v.ai.draftAt = draft, time.Now()
			v.mu.Unlock()
			// Shared through the host with its user and other controllers.
			v.sendControl(map[string]any{"type": "ai-draft", "text": draft})
		}
		ui.Row(c).Children(func() {
			if ui.Button(c.Key("v-ai-start"), L.AIActionStart()).Disabled(strings.TrimSpace(draft) == "").Clicked() {
				// The key makes a start that is sent again after a
				// reconnect start one task on the host.
				key := strconv.FormatInt(time.Now().UnixNano(), 36)
				v.sendControl(map[string]any{"type": "ai-start", "key": key, "instruction": draft})
				v.mu.Lock()
				v.ai.draft, v.ai.err = "", ""
				v.mu.Unlock()
			}
		})
	})
}

// aiTask shows the host's task: its state, what the model said, its latest
// actions, the approval and the controls.
func (v *viewer) aiTask(c *ui.Context, task *localapi.AITask, approve bool) {
	t := c.Theme()
	key := "v-ai-task-" + itoa(task.ID)
	ui.Column(c.Key(key)).Gap(8).FillWidth().Children(func() {
		ui.Text(c, task.Instruction).Bold().Selectable()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if task.State == "running" {
				ui.Spinner(c)
			}
			ui.Text(c, aiStateLabel(task)).FontSize(sizeSmall).TextColor(t.TextMuted)
		})
		if task.Error != "" {
			ui.Text(c, task.Error).FontSize(sizeSmall).TextColor(t.TextMuted).Selectable()
		}
		if task.Text != "" {
			ui.Text(c, task.Text).Selectable()
		}
		if n := len(task.Progress); n > 0 && !aiFinished(task.State) {
			ui.Text(c, task.Progress[n-1]).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		if a := task.Approval; a != nil {
			ui.Column(c.Key(key+"-approval")).Gap(8).FillWidth().Padding(10).Radius(8).Border(2, t.Accent).Children(func() {
				ui.Text(c, L.AIApprovalTitle()).Bold()
				ui.Text(c, describeAction(a.Action))
				for i, f := range a.Flagged {
					ui.Text(c.Key(key+"-f"+strconv.Itoa(i)), f).FontSize(sizeSmall).TextColor(t.Warning)
				}
				if !approve {
					ui.Text(c, L.ViewerAIHostApproves()).FontSize(sizeSmall).TextColor(t.TextMuted)
					return
				}
				ui.Row(c).Gap(gap).Children(func() {
					if ui.Button(c.Key(key+"-allow"), L.ActionAllow()).Clicked() {
						v.sendControl(map[string]any{"type": "ai-decide", "id": task.ID, "approve": true})
					}
					if ui.Button(c.Key(key+"-deny"), L.ActionDeny()).Clicked() {
						v.sendControl(map[string]any{"type": "ai-decide", "id": task.ID, "approve": false})
					}
				})
			})
		}
		for i, s := range task.Steps[max(len(task.Steps)-aiStepsShown, 0):] {
			ui.Row(c.Key(key + "-step-" + strconv.Itoa(i))).Gap(8).Children(func() {
				ui.Text(c, describeAction(s.Action)).FontSize(sizeSmall).Grow(1)
				if o := outcomeLabel(s.Outcome); o != "" {
					ui.Text(c, o).FontSize(sizeSmall).TextColor(t.Warning)
				}
			})
		}
		if aiFinished(task.State) {
			return
		}
		ui.Row(c).Gap(gap).Children(func() {
			if task.State == "paused" {
				if ui.Button(c.Key(key+"-resume"), L.AIActionResume()).Clicked() {
					v.sendControl(map[string]any{"type": "ai-command", "id": task.ID, "command": "resume"})
				}
			} else if ui.Button(c.Key(key+"-pause"), L.AIActionPause()).Clicked() {
				v.sendControl(map[string]any{"type": "ai-command", "id": task.ID, "command": "pause"})
			}
			if ui.Button(c.Key(key+"-cancel"), L.AIActionStop()).Clicked() {
				v.sendControl(map[string]any{"type": "ai-command", "id": task.ID, "command": "cancel"})
			}
		})
	})
}
