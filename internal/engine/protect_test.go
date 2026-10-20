package engine

import (
	"context"
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
