package engine

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestProtectedModeRefuses: in protected mode the engine refuses
// everything but the AI page, the host user's safety controls and what
// the GUI sends as it attaches, before anything reaches the core.
func TestProtectedModeRefuses(t *testing.T) {
	h := &handler{}
	h.protected.Store(true)
	for _, typ := range []localapi.RequestType{
		localapi.RequestNetworkGet, localapi.RequestNetworkSet, localapi.RequestPasswordReveal,
		localapi.RequestNetworkProfiles, localapi.RequestNetworkProfileSet, localapi.RequestNetworkProfileRemove,
		localapi.RequestNetworkDirect,
		localapi.RequestPermanentSet, localapi.RequestAccessSettings, localapi.RequestConnect,
		localapi.RequestIdentityExport, localapi.RequestIdentityReset, localapi.RequestTrustedRemove,
		localapi.RequestBackground, localapi.RequestStoreExport, localapi.RequestNameSet,
	} {
		if resp := h.Handle(context.Background(), localapi.Request{Type: typ}); resp.OK || resp.Code != localapi.CodeInvalid {
			t.Errorf("%s served in protected mode", typ)
		}
	}
	for _, typ := range []localapi.RequestType{
		localapi.RequestStatus, localapi.RequestProtect, localapi.RequestDecide, localapi.RequestClose,
		localapi.RequestCaptureShow, localapi.RequestInputKyokai, localapi.RequestSoundSource, localapi.RequestLogs,
		localapi.RequestAISettingsSet, localapi.RequestAITaskStart, localapi.RequestAIStop,
	} {
		if !protectedAllows[typ] {
			t.Errorf("%s refused in protected mode", typ)
		}
	}
}

// Protected mode covers the streams too (regression: session.media and
// tunnel.open went to Media, which skipped the check), and an engine
// carries a bounded number of them.
func TestMediaRefusedWhenProtectedOrFull(t *testing.T) {
	open := func() io.ReadWriter {
		t.Fatal("a refused stream was opened")
		return nil
	}
	h := &handler{}
	h.protected.Store(true)
	for _, typ := range []localapi.RequestType{localapi.RequestMedia, localapi.RequestTunnelOpen} {
		if resp := h.Media(context.Background(), localapi.Request{Type: typ}, open); resp.OK || resp.Code != localapi.CodeInvalid {
			t.Errorf("%s served in protected mode", typ)
		}
	}
	h = &handler{}
	h.streams.Store(maxStreams)
	if h.Media(context.Background(), localapi.Request{Type: localapi.RequestTunnelOpen}, open).OK {
		t.Error("a tunnel beyond maxStreams was opened")
	}
	h = &handler{}
	h.videos.Store(maxVideoStreams)
	if h.Media(context.Background(), localapi.Request{Type: localapi.RequestMedia}, open).OK {
		t.Error("a media stream beyond maxVideoStreams was opened")
	}
	if h.streams.Load() != 0 || h.videos.Load() != maxVideoStreams {
		t.Errorf("a refusal kept its count: streams %d, videos %d", h.streams.Load(), h.videos.Load())
	}
}

// TestOwnerTierRequests: the kyokai group may use the helper but never carry its identity out or
// in, replace it, remove the helper or stop it: those are the operator's
// (regression: only keys.sign, credentials.get and logs.read were).
func TestOwnerTierRequests(t *testing.T) {
	group := localapi.WithOwner(context.Background(), false)
	types := []localapi.RequestType{
		localapi.RequestIdentityExport, localapi.RequestStoreExport, localapi.RequestIdentityImport,
		localapi.RequestStoreImport, localapi.RequestStoreClear, localapi.RequestIdentityReset,
		localapi.RequestHelperRemove, localapi.RequestShutdown,
	}
	stopped := false
	p := &parked{moved: "helper", stop: func() { stopped = true }, start: func() (*handler, error) {
		t.Fatal("a refused request started the core")
		return nil, nil
	}}
	for _, typ := range types {
		if resp := (&handler{}).Handle(group, localapi.Request{Type: typ}); resp.OK || !strings.Contains(resp.Error, "owner") {
			t.Errorf("%s served to the group: %+v", typ, resp)
		}
		if resp := p.Handle(group, localapi.Request{Type: typ}); resp.OK || !strings.Contains(resp.Error, "owner") {
			t.Errorf("%s served to the group by a parked engine: %+v", typ, resp)
		}
		if _, refused := refuseTier(localapi.WithOwner(context.Background(), true), localapi.Request{Type: typ}); refused {
			t.Errorf("%s refused to the owner", typ)
		}
	}
	if stopped {
		t.Fatal("the group stopped a parked engine")
	}
	// The owner still stops a parked engine.
	if !p.Handle(localapi.WithOwner(context.Background(), true), localapi.Request{Type: localapi.RequestShutdown}).OK || !stopped {
		t.Fatal("the owner could not stop a parked engine")
	}
}

// clipboard.mine is served in protected mode and takes only a SHA-256 in
// lowercase hex.
func TestClipboardMine(t *testing.T) {
	if !protectedAllows[localapi.RequestClipboardMine] {
		t.Error("clipboard.mine refused in protected mode")
	}
	if ownerOnly[localapi.RequestClipboardMine] {
		t.Error("clipboard.mine is not for every tier")
	}
	for _, d := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("0", 63), strings.Repeat("0", 65)} {
		if (&handler{}).Handle(context.Background(), localapi.Request{Type: localapi.RequestClipboardMine, Digest: d}).OK {
			t.Errorf("digest %q taken", d)
		}
	}
	if !isDigest(strings.Repeat("0a", 32)) {
		t.Error("a lowercase hex SHA-256 refused")
	}
}
