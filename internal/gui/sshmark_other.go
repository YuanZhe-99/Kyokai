//go:build !windows && !darwin

package gui

// markDownloaded does nothing here: Linux has no mark for downloads.
func markDownloaded(string) {}
