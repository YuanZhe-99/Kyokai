package engine

import (
	"context"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// services answers the requests about sign-in keys (doc/en-us/identity-and-access.md, "Keys").
// Returns: the answer, and whether req was one of them.
// Notes: keys.sign is served only to the engine's owner or the helper's
// operator (localapi.IsOwner), never to the kyokai group.
func (h *handler) services(ctx context.Context, req localapi.Request) (localapi.Response, bool) {
	switch req.Type {
	case localapi.RequestKeysList:
		var keys []localapi.LoginKey
		if err := h.lib.Call("keys.list", nil, &keys); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err), true
		}
		return localapi.Response{OK: true, Keys: keys}, true
	case localapi.RequestKeysAdd:
		if req.LoginKey == nil || req.LoginKey.Private == nil {
			return localapi.Fail(localapi.CodeInvalid, "key missing"), true
		}
		var key localapi.LoginKey
		params := map[string]string{"name": req.LoginKey.Name, "private": *req.LoginKey.Private}
		if err := h.lib.Call("keys.add", params, &key); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err), true
		}
		h.changed()
		return localapi.Response{OK: true, Keys: []localapi.LoginKey{key}}, true
	case localapi.RequestKeysRemove:
		if req.LoginKey == nil || req.LoginKey.ID == "" {
			return localapi.Fail(localapi.CodeInvalid, "key missing"), true
		}
		return h.command("keys.remove", map[string]string{"id": req.LoginKey.ID}), true
	case localapi.RequestKeysSign:
		if !localapi.IsOwner(ctx) {
			return localapi.Fail(localapi.CodeInvalid, "only this computer's owner signs with its keys"), true
		}
		if req.Sign == nil {
			return localapi.Fail(localapi.CodeInvalid, "signing request missing"), true
		}
		var out struct{ Signature string }
		if err := h.lib.Call("keys.sign", req.Sign, &out); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err), true
		}
		return localapi.Response{OK: true, Signature: out.Signature}, true
	case localapi.RequestAuthorizedList:
		var keys []localapi.AuthorizedKey
		if err := h.lib.Call("authorized.list", nil, &keys); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err), true
		}
		return localapi.Response{OK: true, Authorized: keys}, true
	case localapi.RequestAuthorizedAdd:
		if req.Authorized == nil || req.Authorized.Public == "" {
			return localapi.Fail(localapi.CodeInvalid, "public key missing"), true
		}
		var key localapi.AuthorizedKey
		params := map[string]string{"name": req.Authorized.Name, "public": req.Authorized.Public}
		if err := h.lib.Call("authorized.add", params, &key); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err), true
		}
		h.changed()
		return localapi.Response{OK: true, Authorized: []localapi.AuthorizedKey{key}}, true
	case localapi.RequestAuthorizedRemove:
		if req.Authorized == nil || req.Authorized.ID == "" {
			return localapi.Fail(localapi.CodeInvalid, "key missing"), true
		}
		return h.command("authorized.remove", map[string]string{"id": req.Authorized.ID}), true
	}
	return localapi.Response{}, false
}
