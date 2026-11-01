//go:build darwin || linux

package localapi

import (
	"fmt"
	"net"
	"time"
)

// peerCredentials reads the identity of the process on the other end of a
// Unix socket connection. The kernel recorded it at connect time, so the
// peer cannot forge it (unlike anything in the request payload).
// Inputs: accepted is unused here (Windows checks it).
func peerCredentials(conn net.Conn, _ time.Time) (Peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("not a unix connection: %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var peer Peer
	var credErr error
	if err := raw.Control(func(fd uintptr) { peer, credErr = socketCredentials(int(fd)) }); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, credErr
	}
	if !peer.Known {
		return Peer{}, fmt.Errorf("peer credentials unavailable")
	}
	return peer, nil
}

// serverPeer identifies the engine at the far end of a client's
// connection: the kernel recorded its credentials when it listened.
func serverPeer(conn net.Conn) (Peer, error) { return peerCredentials(conn, time.Time{}) }
