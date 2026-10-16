//go:build !windows

package gui

import "os/exec"

func hideWindow(*exec.Cmd) {}
