package localapi

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// socketCredentials is Linux's SO_PEERCRED (uid, gid, pid) with
// SO_PEERGROUPS for the supplementary groups, both recorded at connect time.
// Notes: the primary gid is added to Groups, as macOS's list carries it.
func socketCredentials(fd int) (Peer, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{UID: cred.Uid, GID: cred.Gid, PID: uint32(cred.Pid), Groups: []uint32{cred.Gid}, Known: true}
	groups, err := peerGroups(fd)
	if err != nil {
		return Peer{}, err
	}
	p.Groups = append(p.Groups, groups...)
	return p, nil
}

// peerGroups reads SO_PEERGROUPS, growing the buffer once when the kernel
// reports more groups than it holds (ERANGE with the needed length).
func peerGroups(fd int) ([]uint32, error) {
	buf := make([]uint32, 64)
	for {
		n := uint32(len(buf) * 4)
		// SAFETY-equivalent: buf outlives the call and n is its size in bytes.
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), unix.SOL_SOCKET, unix.SO_PEERGROUPS,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0)
		switch errno {
		case 0:
			return buf[:n/4], nil
		case unix.ERANGE:
			if int(n/4) <= len(buf) {
				return nil, errno
			}
			buf = make([]uint32, n/4)
		default:
			return nil, errno
		}
	}
}
