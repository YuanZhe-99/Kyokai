package gui

import (
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestControlledHere: someone controls this computer while a connected
// host session may control its input or its AI runs a task; watching,
// reconnecting, controlling another computer and a paused task do not
// count, and nothing counts without the engine.
func TestControlledHere(t *testing.T) {
	session := func(role, state string, granted ...string) localapi.SessionInfo {
		return localapi.SessionInfo{Role: role, State: state, Granted: granted}
	}
	for _, tc := range []struct {
		name      string
		sessions  []localapi.SessionInfo
		ai        *localapi.AIActive
		connected bool
		want      bool
	}{
		{"nobody", nil, nil, true, false},
		{"controlled", []localapi.SessionInfo{session("host", "connected", localapi.PermViewScreen, localapi.PermControlInput)}, nil, true, true},
		{"watched only", []localapi.SessionInfo{session("host", "connected", localapi.PermViewScreen)}, nil, true, false},
		{"reconnecting", []localapi.SessionInfo{session("host", "reconnecting", localapi.PermControlInput)}, nil, true, false},
		{"controlling another", []localapi.SessionInfo{session("controller", "connected", localapi.PermControlInput)}, nil, true, false},
		{"AI running", nil, &localapi.AIActive{ID: 1, State: "running"}, true, true},
		{"AI paused", nil, &localapi.AIActive{ID: 1, State: "paused"}, true, false},
		{"no engine", []localapi.SessionInfo{session("host", "connected", localapi.PermControlInput)}, nil, false, false},
	} {
		a := demoApp(t)
		a.connected = tc.connected
		a.status.Sessions, a.status.AI = tc.sessions, tc.ai
		if got := a.controlledHere(); got != tc.want {
			t.Errorf("%s: controlledHere() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
