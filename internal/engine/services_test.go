package engine

import (
	"context"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestOwnerOnlyRequests: signing with the engine's keys is refused to a
// connection that is not the owner's or the operator's (the kyokai group),
// before anything reaches the core; protected mode refuses the key
// requests too.
func TestOwnerOnlyRequests(t *testing.T) {
	h := &handler{} // no core: a request that reached it would panic
	for _, req := range []localapi.Request{
		{Type: localapi.RequestKeysSign, Sign: &localapi.SignRequest{KeyID: "k", Algorithm: "ssh-ed25519", Data: "AA=="}},
	} {
		if resp := h.Handle(context.Background(), req); resp.OK || resp.Code != localapi.CodeInvalid {
			t.Errorf("%s served to the group", req.Type)
		}
	}
	h.protected.Store(true)
	for _, typ := range []localapi.RequestType{
		localapi.RequestKeysList, localapi.RequestKeysAdd, localapi.RequestKeysRemove, localapi.RequestKeysSign,
		localapi.RequestAuthorizedList, localapi.RequestAuthorizedAdd, localapi.RequestAuthorizedRemove,
	} {
		if resp := h.Handle(context.Background(), localapi.Request{Type: typ}); resp.OK || resp.Code != localapi.CodeInvalid {
			t.Errorf("%s served in protected mode", typ)
		}
	}
}
