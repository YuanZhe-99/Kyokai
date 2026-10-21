package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrOtherIdentity reports that the destination holds another identity
// with its own settings, which a move replaces only when the person chose
// to (MoveOptions.Replace).
var ErrOtherIdentity = errors.New("the other engine holds another identity with its own settings")

// MoveOptions says where a move goes and whether it may replace the
// destination's own identity.
type MoveOptions struct {
	// To names the destination for the source's marker: "helper" or "user".
	To string
	// Replace lets the destination's identity go even when it has history.
	Replace bool
}

// MoveStore moves this device's whole store from one engine to another (a
// run-mode change: the user engine and the helper; doc/en-us/run-modes.md).
// The source seals everything under a one-time passphrase, the destination
// takes it over in one write, and only once the destination reports the
// same Device ID does the source forget it, leave a marker saying where it
// went and stop, so one identity is never held twice and never lost.
// Returns: the Device ID now served by the destination; ErrOtherIdentity
// when the destination's own identity has history and Replace is off.
// Side effects: replaces the destination's store; clears and stops the
// source.
// Notes: both engines must run the same version and build (the GUI updates
// the helper first): an older engine cannot read the whole-store document.
func MoveStore(ctx context.Context, from, to *Client, o MoveOptions) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pass := hex.EncodeToString(b)
	src, err := from.Do(ctx, Request{Type: RequestStatus})
	if err != nil {
		return "", err
	}
	before, err := to.Do(ctx, Request{Type: RequestStatus})
	if err != nil {
		return "", err
	}
	if src.Status == nil || before.Status == nil || src.Status.Version != before.Status.Version || src.Status.Build != before.Status.Build {
		return "", fmt.Errorf("the two engines run different builds: update Kyokai Helper first")
	}
	sealed, err := from.Do(ctx, Request{Type: RequestStoreExport, Backup: &IdentityBackup{Passphrase: pass}})
	if err != nil {
		return "", err
	}
	_, err = to.Do(ctx, Request{Type: RequestStoreImport, Backup: &IdentityBackup{Document: sealed.Document, Passphrase: pass}, Replace: o.Replace})
	var re *RequestError
	if errors.As(err, &re) && strings.Contains(re.Message, "another identity") {
		return "", ErrOtherIdentity
	}
	if err != nil {
		return "", err
	}
	dst, err := to.Do(ctx, Request{Type: RequestStatus})
	if err != nil {
		return "", err
	}
	if dst.Status == nil || dst.Status.DeviceID != src.Status.DeviceID {
		return "", fmt.Errorf("the other engine did not take over this device's identity")
	}
	if _, err := from.Do(ctx, Request{Type: RequestStoreClear, MovedTo: o.To}); err != nil {
		return "", err
	}
	return dst.Status.DeviceID, nil
}
