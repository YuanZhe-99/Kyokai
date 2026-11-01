package localapi

import (
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID: the PID of the process on
// the other end of an AF_UNIX socket (Windows 10 1803 and later).
const sioAFUnixGetPeerPID = 0x58000100

// peerCredentials identifies the caller by its process token: the user's
// SID, and whether the token belongs to the Administrators group.
// Inputs: accepted, when the server accepted conn.
// Notes: Windows reports the peer by PID alone. A process created after the
// connection was accepted cannot have made it: its PID was reused after the
// client exited, and it is refused rather than taken for the client.
func peerCredentials(conn net.Conn, accepted time.Time) (Peer, error) {
	pid, err := peerPID(conn)
	if err != nil {
		return Peer{}, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return Peer{}, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return Peer{}, err
	}
	if madeAfter(created, accepted) {
		return Peer{}, fmt.Errorf("peer process %d started after the connection: its PID was reused", pid)
	}
	return tokenIdentity(h, pid)
}

// madeAfter reports whether a process created at created started after t.
func madeAfter(created windows.Filetime, t time.Time) bool {
	return time.Unix(0, created.Nanoseconds()).After(t)
}

// serverPeer identifies the engine at the far end of a client's
// connection: always its PID, and its SID when its token can be read (a
// user cannot read a SYSTEM service's token; the helper's check then
// compares the PID with its service's).
func serverPeer(conn net.Conn) (Peer, error) {
	pid, err := peerPID(conn)
	if err != nil {
		return Peer{}, err
	}
	if p, err := processIdentity(pid); err == nil {
		return p, nil
	}
	return Peer{PID: pid}, nil
}

// peerPID asks the socket for the PID of the process at its other end.
func peerPID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix connection: %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var ret uint32
		ioErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &ret, nil, 0)
	}); err != nil {
		return 0, err
	}
	if ioErr != nil || pid == 0 {
		return 0, fmt.Errorf("peer process unknown: %v", ioErr)
	}
	return pid, nil
}

// processIdentity reads a process's user SID, Administrators membership and
// enabled groups.
func processIdentity(pid uint32) (Peer, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return Peer{}, err
	}
	defer windows.CloseHandle(h)
	return tokenIdentity(h, pid)
}

// tokenIdentity reads the identity of the open process h (whose PID is pid)
// from its token.
func tokenIdentity(h windows.Handle, pid uint32) (Peer, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return Peer{}, err
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return Peer{}, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return Peer{}, err
	}
	isAdmin, _ := tok.IsMember(admins)
	var groups []string
	if g, err := tok.GetTokenGroups(); err == nil {
		for _, x := range g.AllGroups() {
			if x.Attributes&windows.SE_GROUP_ENABLED != 0 {
				groups = append(groups, x.Sid.String())
			}
		}
	}
	return Peer{PID: pid, SID: user.User.Sid.String(), Admin: isAdmin, GroupSIDs: groups, Known: true}, nil
}

// CurrentSID is this process's user SID (the owner of a user daemon).
func CurrentSID() string {
	p, err := processIdentity(windows.GetCurrentProcessId())
	if err != nil {
		return ""
	}
	return p.SID
}
