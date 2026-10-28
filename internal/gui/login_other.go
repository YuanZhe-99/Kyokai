//go:build !darwin

package gui

// LaunchedAsLoginItem is false off macOS: Windows starts Kyokai at login
// with MyGo's login argument, which App.WasOpenedAtLogin reads.
func LaunchedAsLoginItem() bool { return false }
