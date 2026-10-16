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
	}
	for _, c := range cases {
		if got := c.a.Allows(c.p); got != c.want {
			t.Errorf("%s: Allows = %v, want %v", c.name, got, c.want)
		}
	}
}
