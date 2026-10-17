//go:build !windows

package gui

import "os/exec"

func hideWindow(*exec.Cmd) {}

// lightTaskbar is false off Windows: macOS tints the template glyph itself.
func lightTaskbar() bool { return false }
