package gui

import (
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

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

// aiWant is what an AI link asks a host for: running AI tasks and
// answering their approvals, nothing that sends a picture.
var aiWant = []string{localapi.PermRunAITask, localapi.PermApproveAIAction}

// aiLink is this computer's connection to a host for its AI sessions
// (doc/en-us/gui.md, "AI sessions"): a session of its own asking only for
// aiWant, so the host sends no picture; its media connection brings the
// host's task (the one record every controller sees), the shared draft and
// refusals, and takes the AI requests. One per host, shared by the host's
// AI panes and closed with the last of them. Fields after mu are guarded
// by it.
type aiLink struct {
	app    *App
	device string
	// address is where to dial the host, empty over the network.
	address string
	sid     atomic.Uint64
	// panes is how many AI panes use the link (main thread).
	panes int
	// out queues the AI requests for the media connection's writer.
	out chan []byte

	mu      sync.Mutex
	phase   localapi.SessionInfo
	pumping bool
	conn    net.Conn
	// run and approve are the session's run-ai-task and approve-ai-action.
	run, approve bool
	task         *localapi.AITask
	err          string
	draft        string
	// draftAt is when this computer's user last typed the draft.
	draftAt time.Time
}

// aiLinkView is what an AI pane shows of its host's link.
type aiLinkView struct {
	phase        localapi.SessionInfo
	run, approve bool
	task         *localapi.AITask
	err, draft   string
}

// snapshot is the link as its panes show it now.
func (l *aiLink) snapshot() aiLinkView {
	l.mu.Lock()
	defer l.mu.Unlock()
	return aiLinkView{phase: l.phase, run: l.run, approve: l.approve, task: l.task, err: l.err, draft: l.draft}
}

// useAILink is the link to a host for one more AI pane: the open one, or a
// new one dialing it (at address when set).
// Notes: call on the main thread; releaseAILink gives it back.
func (a *App) useAILink(device, address string) *aiLink {
	a.mu.Lock()
	l := a.aiLinks[device]
	fresh := l == nil
	if fresh {
		l = &aiLink{app: a, device: device, address: address, out: make(chan []byte, 16)}
		a.aiLinks[device] = l
	}
	a.mu.Unlock()
	l.panes++
	if fresh {
		l.dial()
	}
	return l
}

// releaseAILink gives back an AI pane's use of its host's link; the last
// one ends the session (which pauses the host's task it started, as any
// controller's leaving does).
// Notes: call on the main thread.
func (a *App) releaseAILink(device string) {
	a.mu.Lock()
	l := a.aiLinks[device]
	a.mu.Unlock()
	if l == nil {
		return
	}
	if l.panes--; l.panes > 0 {
		return
	}
	a.mu.Lock()
	delete(a.aiLinks, device)
	a.mu.Unlock()
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if id := l.sid.Load(); id != 0 {
		a.dropSession(id)
		go a.closeSession(id)
	}
}

// dial asks the host for a session anew (after a refusal or a failure,
// Reconnect), giving up the one before.
func (l *aiLink) dial() {
	old := l.sid.Swap(0)
	l.mu.Lock()
	l.phase = localapi.SessionInfo{State: "connecting", DeviceID: l.device}
	l.err = ""
	address := l.address
	l.mu.Unlock()
	go func() {
		if old != 0 {
			l.app.closeSession(old)
		}
		resp, err := l.app.requestWith(localapi.Request{Type: localapi.RequestConnect, Connect: &localapi.ConnectRequest{
			DeviceID: l.device, Want: aiWant, Address: address,
		}})
		l.mu.Lock()
		if err != nil {
			msg := L.EngineDown()
			var refused *localapi.RequestError
			if errors.As(err, &refused) {
				msg = refused.Message
			}
			l.phase = localapi.SessionInfo{State: "failed", DeviceID: l.device}
			l.err = msg
		} else {
			l.sid.Store(resp.ID)
		}
		l.mu.Unlock()
		l.app.redrawAI(l.device)
	}()
}

// syncAILinks follows the engine's sessions in the AI links: each takes
// its session's progress and starts its media once connected.
// Returns: the sessions the links own, and the hosts a link dials without
// knowing its session yet, which syncViewers leaves alone (no desktop
// opens for them).
// Notes: call on the main thread.
func (a *App) syncAILinks(sessions []localapi.SessionInfo) (owned map[uint64]bool, dialing map[string]bool) {
	a.mu.Lock()
	links := make([]*aiLink, 0, len(a.aiLinks))
	for _, l := range a.aiLinks {
		links = append(links, l)
	}
	a.mu.Unlock()
	owned, dialing = map[uint64]bool{}, map[string]bool{}
	for _, l := range links {
		id := l.sid.Load()
		if id == 0 {
			l.mu.Lock()
			if l.phase.State == "connecting" {
				dialing[l.device] = true
			}
			l.mu.Unlock()
			continue
		}
		owned[id] = true
		i := slices.IndexFunc(sessions, func(s localapi.SessionInfo) bool { return s.ID == id })
		if i < 0 {
			continue
		}
		s := sessions[i]
		l.mu.Lock()
		changed := l.phase.State != s.State
		l.phase = s
		start := s.State == "connected" && !l.pumping
		if start {
			l.pumping = true
			l.run = slices.Contains(s.Granted, localapi.PermRunAITask)
			l.approve = slices.Contains(s.Granted, localapi.PermApproveAIAction)
		}
		l.mu.Unlock()
		if start {
			go l.pump(id)
		}
		if changed || start {
			a.redrawAI(l.device)
		}
	}
	return owned, dialing
}

// pump reads the session's media for the host's AI until it ends: the
// task, the draft and refusals; a writer sends the AI requests meanwhile.
// Requests queued for a session before are dropped, never sent again.
func (l *aiLink) pump(id uint64) {
	defer func() {
		l.mu.Lock()
		l.pumping, l.conn = false, nil
		l.mu.Unlock()
	}()
	conn, err := localapi.NewClient(l.app.socket).Media(l.app.ctx, id)
	if err != nil {
		return
	}
	defer conn.Close()
	for len(l.out) > 0 {
		<-l.out
	}
	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case p := <-l.out:
				if localapi.WritePacket(conn, p) != nil {
					return
				}
			}
		}
	}()
	for {
		p, err := localapi.ReadPacket(conn, nil)
		if err != nil {
			return
		}
		if len(p) > 0 {
			l.packet(p[0], p[1:])
		}
	}
}

// packet takes one of the host's AI packets into the link.
func (l *aiLink) packet(kind byte, body []byte) {
	switch kind {
	case packetAITask:
		var task localapi.AITask
		if json.Unmarshal(body, &task) != nil {
			return
		}
		l.mu.Lock()
		l.task, l.err = &task, ""
		l.mu.Unlock()
		// Redrawn once the sessions follow the task: their titles may change.
		l.app.later(func() {
			l.app.aiTaskSeen(l.device, &task)
			l.app.redrawAI(l.device)
		})
		return
	case packetAIDraft:
		var d struct{ Text string }
		if json.Unmarshal(body, &d) != nil {
			return
		}
		l.mu.Lock()
		if time.Since(l.draftAt) > draftQuiet {
			l.draft = d.Text
		}
		l.mu.Unlock()
	case packetAIError:
		var refused struct{ Error string }
		if json.Unmarshal(body, &refused) != nil {
			return
		}
		l.mu.Lock()
		l.err = refused.Error
		l.mu.Unlock()
	default:
		return
	}
	l.app.redrawAI(l.device)
}

// send queues an AI request for the host (dropped when the queue is full
// or the session is not connected: never sent later).
func (l *aiLink) send(msg map[string]any) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case l.out <- append([]byte{packetControl}, body...):
	default:
	}
}

// setDraft shares the draft of the host's next task as this computer's
// user types it.
func (l *aiLink) setDraft(text string) {
	l.mu.Lock()
	l.draft, l.draftAt = text, time.Now()
	l.mu.Unlock()
	l.send(map[string]any{"type": "ai-draft", "text": text})
}
