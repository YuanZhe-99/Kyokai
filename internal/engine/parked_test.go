package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// A parked engine (its store moved to the other run mode) starts no core —
// so no identity appears — and only says where its store went, until a
// store comes back or the person starts over.
func TestParkedEngineMakesNoIdentity(t *testing.T) {
	dir := t.TempDir()
	if movedTo(dir) != "" {
		t.Fatal("no marker, not parked")
	}
	if err := os.WriteFile(filepath.Join(dir, movedMarker), []byte(`{"to":"helper"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if movedTo(dir) != "helper" {
		t.Fatal("the marker names the helper")
	}
	started := 0
	p := &parked{moved: "helper", runMode: "user", stateDir: dir, stop: func() {}, start: func() (*handler, error) {
		started++
		return nil, errors.New("no core in this test")
	}}
	ctx := context.Background()
	r := p.Handle(ctx, localapi.Request{Type: localapi.RequestStatus})
	if !r.OK || r.Status.Moved != "helper" || r.Status.DeviceID != "" {
		t.Fatalf("status: %+v", r.Status)
	}
	for _, typ := range []localapi.RequestType{localapi.RequestConnect, localapi.RequestPasswordReveal, localapi.RequestStoreExport} {
		if p.Handle(ctx, localapi.Request{Type: typ}).OK {
			t.Errorf("%s served while parked", typ)
		}
	}
	if !p.Handle(ctx, localapi.Request{Type: localapi.RequestCaptureShow}).OK {
		t.Error("what a GUI sends as it attaches is taken")
	}
	if started != 0 {
		t.Fatal("a parked engine started its core")
	}
	// Starting over removes the marker first.
	_ = p.Handle(ctx, localapi.Request{Type: localapi.RequestStoreForgetMove})
	if started != 1 || movedTo(dir) != "" {
		t.Fatalf("start over: started %d, marker %q", started, movedTo(dir))
	}
}
