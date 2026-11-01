//go:build !darwin && !linux && !windows

package localapi

import (
	"fmt"
	"net"
	"time"
)

// peerCredentials is unavailable on this platform; other Unices could
// add their own file (FreeBSD uses LOCAL_PEERCRED too).
func peerCredentials(net.Conn, time.Time) (Peer, error) {
	return Peer{}, fmt.Errorf("peer credentials are not implemented on this platform")
}

// serverPeer is unavailable on this platform.
func serverPeer(conn net.Conn) (Peer, error) { return peerCredentials(conn, time.Time{}) }
