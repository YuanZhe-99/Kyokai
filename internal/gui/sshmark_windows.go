package gui

import "os"

// markDownloaded marks a file copied from another computer as coming from
// the internet zone (its Zone.Identifier stream, Mark of the Web), so
// Windows asks before running it, as for a browser's download.
func markDownloaded(path string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}
