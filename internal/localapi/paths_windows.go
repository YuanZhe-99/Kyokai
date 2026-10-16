package localapi

import "os"

// userRuntimeDir is the per-user temporary folder. AF_UNIX socket files
// under folders in AppData\Local or AppData\Roaming were seen to stay
// behind as inaccessible entries after the listener closed, which blocks
// the next bind (doc/en-us/platform-notes.md); %TEMP% does not.
func userRuntimeDir() string {
	return os.TempDir()
}
