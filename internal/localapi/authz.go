package localapi

// Authorization for the local API.
//
// The socket's filesystem permissions decide who may connect at all; the
// kernel-reported peer identity decides whether the connection is served.
// The No privileges engine serves only its own user: a per-user socket that
// someone else reached anyway is a configuration problem, and the caller
// is refused. A connection whose identity cannot be read is refused too.
// The privileged helper's operator and group tiers arrive with the helper.

// Peer is the identity of a connected client, as reported by the kernel.
type Peer struct {
	UID uint32
	GID uint32
	PID uint32
	// Groups are the peer's supplementary groups (macOS: from
	// LOCAL_PEERCRED).
	Groups []uint32
	// SID and Admin identify the caller on Windows (its token's user and
	// membership of Administrators); uids are unused there.
	SID   string
	Admin bool
	// Known is false when the platform could not report an identity.
	Known bool
}

// Authorizer decides whether a connection is served.
type Authorizer struct {
	// OwnerUID (macOS) and OwnerSID (Windows) are the socket's owner.
	OwnerUID uint32
	OwnerSID string
}

// Allows reports whether p is the owner. It never fails open: an unknown
// identity, or a SID-less peer on Windows, is refused.
func (a Authorizer) Allows(p Peer) bool {
	if !p.Known {
		return false
	}
	if p.SID != "" || a.OwnerSID != "" {
		return p.SID != "" && p.SID == a.OwnerSID
	}
	return p.UID == a.OwnerUID
}
