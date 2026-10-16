//go:build darwin

package localapi

import "golang.org/x/sys/unix"

// socketCredentials is macOS's LOCAL_PEERCRED: an xucred with the uid and
// the group list (no pid).
func socketCredentials(fd int) (Peer, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return Peer{}, err
	}
	p := Peer{UID: cred.Uid, Known: true}
	for i := 0; i < int(cred.Ngroups) && i < len(cred.Groups); i++ {
		p.Groups = append(p.Groups, cred.Groups[i])
	}
	if len(p.Groups) > 0 {
		p.GID = p.Groups[0]
	}
	return p, nil
}
