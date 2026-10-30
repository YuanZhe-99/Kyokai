package localapi

import (
	"context"
	"slices"
)

// Authorization for the local API.
//
// The socket's filesystem permissions decide who may connect at all; the
// kernel-reported peer identity decides whether the connection is served.
// The No privileges engine serves only its own user: a per-user socket that
// someone else reached anyway is a configuration problem, and the caller
// is refused. A connection whose identity cannot be read is refused too.
// The privileged helper also serves its operator and the members of the
// `kyokai` group (doc/en-us/run-modes.md); nobody else, not even an
// administrator.

// Peer is the identity of a connected client, as reported by the kernel.
type Peer struct {
	UID uint32
	GID uint32
	PID uint32
	// Groups are the peer's groups (macOS: from LOCAL_PEERCRED; Linux:
	// the primary gid and SO_PEERGROUPS).
	Groups []uint32
	// SID and Admin identify the caller on Windows (its token's user and
	// membership of Administrators); uids are unused there.
	SID   string
	Admin bool
	// GroupSIDs are the enabled groups of the peer's token (Windows).
	GroupSIDs []string
	// Known is false when the platform could not report an identity.
	Known bool
}

// Authorizer decides whether a connection is served.
type Authorizer struct {
	// OwnerUID (macOS) and OwnerSID (Windows) are the socket's owner.
	OwnerUID uint32
	OwnerSID string
	// OperatorSID is the user who installed the helper (Windows).
	OperatorSID string
	// GroupSID is the `kyokai` group whose members may use the helper
	// (Windows; empty when no administrator created the group).
	GroupSID string
	// OperatorUID and GroupGID are the operator and the `kyokai` group on
	// macOS; nil when not recorded or no such group.
	OperatorUID *uint32
	GroupGID    *uint32
}

// Allows reports whether p is the owner, the operator or in the group. It
// never fails open: an unknown identity, or a SID-less peer on Windows, is
// refused.
func (a Authorizer) Allows(p Peer) bool {
	if a.Owner(p) {
		return true
	}
	if !p.Known {
		return false
	}
	if p.SID != "" || a.OwnerSID != "" {
		return p.SID != "" && a.GroupSID != "" && slices.Contains(p.GroupSIDs, a.GroupSID)
	}
	return a.GroupGID != nil && slices.Contains(p.Groups, *a.GroupGID)
}

// Owner reports whether p is the socket's owner or the helper's operator:
// the tier served the engine's sign-in keys and saved service passwords
// (keys.sign, credentials.get), which the kyokai group is not.
func (a Authorizer) Owner(p Peer) bool {
	if !p.Known {
		return false
	}
	if p.SID != "" || a.OwnerSID != "" {
		return p.SID != "" && (p.SID == a.OwnerSID || a.OperatorSID != "" && p.SID == a.OperatorSID)
	}
	return p.UID == a.OwnerUID || a.OperatorUID != nil && p.UID == *a.OperatorUID
}

// ownerKey marks a request context whose connection is the owner tier.
type ownerKey struct{}

// IsOwner reports whether a request came from the engine's owner or the
// helper's operator (Authorizer.Owner), as the server put it in the
// request's context.
func IsOwner(ctx context.Context) bool {
	owner, _ := ctx.Value(ownerKey{}).(bool)
	return owner
}
