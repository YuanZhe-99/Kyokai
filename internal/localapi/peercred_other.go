//go:build !darwin && !linux && !windows

package localapi

import (
	"fmt"
	"net"
)

// peerCredentials is unavailable on this platform; other Unices could
// add their own file (FreeBSD uses LOCAL_PEERCRED too).
func peerCredentials(conn net.Conn) (Peer, error) {
	return Peer{}, fmt.Errorf("peer credentials are not implemented on this platform")
}
