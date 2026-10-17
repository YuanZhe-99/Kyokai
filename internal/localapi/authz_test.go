package localapi

import "testing"

func TestAuthorizerServesOnlyTheOwner(t *testing.T) {
	cases := []struct {
		name string
		a    Authorizer
		p    Peer
		want bool
	}{
		{"unknown identity", Authorizer{OwnerUID: 501}, Peer{UID: 501}, false},
		{"owner by uid", Authorizer{OwnerUID: 501}, Peer{UID: 501, Known: true}, true},
		{"other uid", Authorizer{OwnerUID: 501}, Peer{UID: 502, Known: true}, false},
		{"owner by SID", Authorizer{OwnerSID: "S-1-5-21-1"}, Peer{SID: "S-1-5-21-1", Known: true}, true},
		{"other SID, even an administrator", Authorizer{OwnerSID: "S-1-5-21-1"}, Peer{SID: "S-1-5-21-2", Admin: true, Known: true}, false},
		{"Windows peer without a SID", Authorizer{OwnerSID: "S-1-5-21-1"}, Peer{Known: true}, false},
		// The helper: SYSTEM owns the socket; the operator and the group may use it.
		{"helper: the operator", helperAuthz, Peer{SID: "S-1-5-21-op", Known: true}, true},
		{"helper: a group member", helperAuthz, Peer{SID: "S-1-5-21-m", GroupSIDs: []string{"S-1-5-21-g"}, Known: true}, true},
		{"helper: another user", helperAuthz, Peer{SID: "S-1-5-21-x", GroupSIDs: []string{"S-1-5-32-545"}, Known: true}, false},
		{"helper: an administrator outside the group", helperAuthz, Peer{SID: "S-1-5-21-a", Admin: true, Known: true}, false},
		{"helper without a group: a peer with no groups", Authorizer{OwnerSID: "S-1-5-18", OperatorSID: "S-1-5-21-op"}, Peer{SID: "S-1-5-21-x", GroupSIDs: []string{""}, Known: true}, false},
	}
	for _, c := range cases {
		if got := c.a.Allows(c.p); got != c.want {
			t.Errorf("%s: Allows = %v, want %v", c.name, got, c.want)
		}
	}
}

// helperAuthz is the helper's authorizer in the tests: SYSTEM, an operator
// and a group.
var helperAuthz = Authorizer{OwnerSID: "S-1-5-18", OperatorSID: "S-1-5-21-op", GroupSID: "S-1-5-21-g"}
