package engine

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// services answers the requests about sign-in keys
// (doc/en-us/identity-and-access.md, "Keys") and the saved sign-ins of
// devices' services (doc/en-us/remote-services.md).
// Returns: the answer, and whether req was one of them.
// Notes: keys.sign and credentials.get are served only to the engine's
// owner or the helper's operator (localapi.IsOwner), never to the kyokai
// group.
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
	case localapi.RequestCredentialsGet:
		if !localapi.IsOwner(ctx) {
			return localapi.Fail(localapi.CodeInvalid, "only this computer's owner reads saved passwords"), true
		}
		var creds localapi.Credentials
		params := map[string]string{"device_id": req.DeviceID, "service": req.Service}
		if err := h.lib.Call("credentials.get", params, &creds); err != nil {
			return localapi.Fail(localapi.CodeInvalid, "%v", err), true
		}
		return localapi.Response{OK: true, Credentials: &creds}, true
	case localapi.RequestCredentialsSet:
		if req.Credentials == nil {
			return localapi.Fail(localapi.CodeInvalid, "credentials missing"), true
		}
		params := map[string]any{"device_id": req.DeviceID, "service": req.Service, "credentials": req.Credentials}
		return h.command("credentials.set", params), true
	case localapi.RequestCredentialsForget:
		return h.command("credentials.forget", map[string]string{"device_id": req.DeviceID, "service": req.Service}), true
	case localapi.RequestWatchSet:
		devices := []string{}
		if req.Watch != nil {
			devices = append(devices, *req.Watch...)
		}
		return h.command("watch.set", map[string][]string{"devices": devices}), true
	}
	return localapi.Response{}, false
}

// tunnel opens a tunnel to a device's own service and carries its bytes
// until the client or the tunnel ends (doc/en-us/remote-services.md).
// Returns: a refusal naming why there is no tunnel (the session's failure:
// "service-down", "denied", "unsupported", "unreachable", … or "timeout"),
// else OK once the tunnel ended.
// Side effects: the tunnel's session ends when this returns.
func (h *handler) tunnel(ctx context.Context, req localapi.Request, open func() io.ReadWriter) localapi.Response {
	t := req.Tunnel
	if t == nil || t.DeviceID == "" || t.Service == "" {
		return localapi.Fail(localapi.CodeInvalid, "tunnel request missing")
	}
	var opened struct{ ID uint64 }
	if err := h.lib.Call("tunnel.open", t, &opened); err != nil {
		return localapi.Fail(localapi.CodeInvalid, "%v", err)
	}
	h.changed()
	defer func() {
		_ = h.lib.Call("session.close", map[string]uint64{"id": opened.ID}, nil)
		h.changed()
	}()
	// Waits a second at a time, so a client that leaves ends the attempt.
	var waited struct {
		State   string          `json:"state"`
		Failure json.RawMessage `json:"failure"`
	}
	for deadline := time.Now().Add(localapi.TunnelWait); ; {
		params := map[string]uint64{"id": opened.ID, "timeout_ms": 1000}
		if err := h.lib.Call("tunnel.wait", params, &waited); err != nil {
			return localapi.Fail(localapi.CodeInternal, "%v", err)
		}
		pending := waited.State == "connecting" || waited.State == "confirming"
		if !pending || ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
	}
	if waited.State != "connected" {
		return localapi.Fail(localapi.CodeInvalid, "%s", tunnelFailure(waited.State, waited.Failure))
	}
	h.pump(ctx, opened.ID, open())
	return localapi.Response{OK: true}
}

// tunnelFailure names why a tunnel's session did not connect: the host's
// refusal ("service-down", "denied", …), the session's failure
// ("unsupported", "unreachable", …), or "timeout" when it was still
// waiting.
func tunnelFailure(state string, failure json.RawMessage) string {
	var name string
	if json.Unmarshal(failure, &name) == nil && name != "" {
		return name
	}
	var refused struct {
		Refused string `json:"refused"`
	}
	if json.Unmarshal(failure, &refused) == nil && refused.Refused != "" {
		return refused.Refused
	}
	if state == "closed" {
		return "closed"
	}
	return "timeout"
}
