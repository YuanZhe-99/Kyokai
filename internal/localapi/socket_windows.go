package localapi

import (
	"fmt"
	"os"
	"os/exec"
)

// secureSocket lets local users connect to a socket whose mode allows
// others (the helper's): Windows checks write access on the socket file
// for AF_UNIX connections. The daemon still authorizes each caller by SID.
func secureSocket(path string, mode os.FileMode, _ int) error {
	if mode&0o006 == 0 {
		return nil
	}
	// *S-1-5-32-545 is BUILTIN\Users, named by SID so any language works.
	if out, err := exec.Command("icacls", path, "/grant", "*S-1-5-32-545:(R,W)").CombinedOutput(); err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	return nil
}
