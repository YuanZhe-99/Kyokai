//go:build !windows

package helper

// The macOS helper (a root LaunchDaemon) comes with the Mac pass; until
// then these report ErrUnsupported.

// BinDir, StateDir and SocketPath are empty without a backend.
var BinDir, StateDir, SocketPath string

// Elevated reports false without a backend.
func Elevated() bool { return false }

// GroupSID is empty outside Windows.
func GroupSID() string { return "" }

// Install is not available yet.
func Install(InstallOptions) error { return ErrUnsupported }

// Uninstall is not available yet.
func Uninstall(bool) error { return ErrUnsupported }

// Inspect reports nothing installed.
func Inspect() Status { return Status{} }

// SelfUpdate is not available yet.
func SelfUpdate(string, string) error { return ErrUnsupported }

// Retire is not available yet.
func Retire() error { return ErrUnsupported }
