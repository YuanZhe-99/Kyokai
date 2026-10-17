package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// MoveStore moves this device's whole store from one engine to another (a
// run-mode change: the user engine and the helper; doc/en-us/run-modes.md).
// The source seals it under a one-time passphrase, the destination takes
// it over, and only once the destination reports the same Device ID does
// the source forget it and stop, so one identity is never held twice and
// never lost.
// Returns: the Device ID now served by the destination.
// Side effects: replaces the destination's store; clears and stops the
// source.
func MoveStore(ctx context.Context, from, to *Client) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pass := hex.EncodeToString(b)
	src, err := from.Do(ctx, Request{Type: RequestStatus})
	if err != nil {
		return "", err
	}
	sealed, err := from.Do(ctx, Request{Type: RequestStoreExport, Backup: &IdentityBackup{Passphrase: pass}})
	if err != nil {
		return "", err
	}
	if _, err := to.Do(ctx, Request{Type: RequestStoreImport, Backup: &IdentityBackup{Document: sealed.Document, Passphrase: pass}}); err != nil {
		return "", err
	}
	dst, err := to.Do(ctx, Request{Type: RequestStatus})
	if err != nil {
		return "", err
	}
	if dst.Status == nil || src.Status == nil || dst.Status.DeviceID != src.Status.DeviceID {
		return "", fmt.Errorf("the other engine did not take over this device's identity")
	}
	if _, err := from.Do(ctx, Request{Type: RequestStoreClear}); err != nil {
		return "", err
	}
	return dst.Status.DeviceID, nil
}
