package localapi

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID: the PID of the process on
// the other end of an AF_UNIX socket (Windows 10 1803 and later).
const sioAFUnixGetPeerPID = 0x58000100

// peerCredentials identifies the caller by its process token: the user's
// SID, and whether the token belongs to the Administrators group.
func peerCredentials(conn net.Conn) (Peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("not a unix connection: %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var pid uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var ret uint32
		ioErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &ret, nil, 0)
	}); err != nil {
		return Peer{}, err
	}
	if ioErr != nil || pid == 0 {
		return Peer{}, fmt.Errorf("peer process unknown: %v", ioErr)
	}
	return processIdentity(pid)
}

// processIdentity reads a process's user SID and Administrators membership.
func processIdentity(pid uint32) (Peer, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return Peer{}, err
	}
	defer windows.CloseHandle(h)
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
	return Peer{PID: pid, SID: user.User.Sid.String(), Admin: isAdmin, Known: true}, nil
}

// CurrentSID is this process's user SID (the owner of a user daemon).
func CurrentSID() string {
	p, err := processIdentity(windows.GetCurrentProcessId())
	if err != nil {
		return ""
	}
	return p.SID
}
