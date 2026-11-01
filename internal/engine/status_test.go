package engine

import (
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// One status frame goes to every attached session without waiting for any:
// a session that has not taken the previous frame gets the newest instead,
// an unchanged frame goes only to a session still waiting for its first,
// and nobody blocks the producer.
func TestDeliverNeverWaits(t *testing.T) {
	h := &handler{sessions: map[chan localapi.Response]bool{}}
	slow, fast := make(chan localapi.Response, 1), make(chan localapi.Response, 1)
	h.sessions[slow], h.sessions[fast] = true, true
	frame := func(id string) localapi.Response {
		f, err := localapi.Encode(localapi.Response{OK: true, Status: &localapi.Status{DeviceID: id}})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	h.deliver(frame("a"))
	if got := (<-fast).Status.DeviceID; got != "a" {
		t.Fatalf("fast got %q", got)
	}
	// The slow session has not taken "a" when "b" comes.
	h.deliver(frame("b"))
	if got := (<-slow).Status.DeviceID; got != "b" {
		t.Fatalf("slow got %q, want the newest frame", got)
	}
	if got := (<-fast).Status.DeviceID; got != "b" {
		t.Fatalf("fast got %q", got)
	}
	// An unchanged frame is not sent again, except to a new session.
	late := make(chan localapi.Response, 1)
	h.sessions[late] = true
	h.deliver(frame("b"))
	if len(fast) != 0 || len(slow) != 0 {
		t.Fatal("an unchanged status was sent again")
	}
	if got := (<-late).Status.DeviceID; got != "b" {
		t.Fatalf("a new session got %q", got)
	}
}
