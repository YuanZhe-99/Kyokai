//go:build !windows && !darwin && !linux

package helper

import "github.com/YuanZhe-99/Kyokai/internal/localapi"

// Other systems have no helper backend yet; these report ErrUnsupported.

// BinDir, StateDir, SocketPath and AgentSocket are empty without a backend.
var BinDir, StateDir, SocketPath, AgentSocket string

// Elevated reports false without a backend.
func Elevated() bool { return false }

// GroupSID is empty outside Windows.
func GroupSID() string { return "" }

// GroupGID reports no group without a backend.
func GroupGID() (uint32, bool) { return 0, false }

// Install is not available yet.
func Install(InstallOptions) error { return ErrUnsupported }

// Uninstall is not available yet.
func Uninstall(bool) error { return ErrUnsupported }

// Installed reports false without a backend.
func Installed() bool { return false }

// Inspect reports nothing installed.
func Inspect() Status { return Status{} }

// SelfUpdate is not available yet.
func SelfUpdate(string, string) error { return ErrUnsupported }

// Retire is not available yet.
func Retire() error { return ErrUnsupported }

// CheckState is not available yet.
func CheckState() error { return ErrUnsupported }

// IsHelper reports false without a backend.
func IsHelper(localapi.Peer) bool { return false }

// localPath reports false without a backend.
func localPath(string) bool { return false }
