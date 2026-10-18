package gui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// okEngine answers every request with OK.
type okEngine struct{}

func (okEngine) Handle(context.Context, localapi.Request) localapi.Response {
	return localapi.Response{Version: localapi.ProtocolVersion, OK: true}
}
func (okEngine) Stream(context.Context, localapi.Request, func(localapi.Response) error) {}
func (okEngine) Media(context.Context, localapi.Request, func() io.ReadWriter) localapi.Response {
	return localapi.Response{}
}

// A saved access setting with no follow-up must not crash the GUI
// (choosing "Permanent" under Accepted passwords once did).
func TestAccessCommandWithoutFollowUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go localapi.ServeWith(ctx, ln, okEngine{}, localapi.Authorizer{OwnerUID: uint32(os.Getuid())})

	a := New("", t.TempDir(), path)
	a.accessCommand(localapi.Request{Type: localapi.RequestAccessSettings, PasswordMode: "permanent"}, nil)
	for deadline := time.Now().Add(5 * time.Second); ; {
		a.mu.Lock()
		busy, toast := a.busy["access"], a.toast
		a.mu.Unlock()
		if !busy {
			if toast != "" {
				t.Fatalf("the request failed: %s", toast)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
