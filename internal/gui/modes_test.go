package gui

import (
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/helper"
)

// A GUI that switched to the helper attaches to it again when it starts,
// development profiles included; without the helper it keeps its own engine.
func TestStartSocketFollowsTheRunModeChoice(t *testing.T) {
	const own = "own.sock"
	for _, c := range []struct {
		helperMode, installed bool
		want                  string
	}{
		{false, false, own},
		{false, true, own},
		{true, false, own},
		{true, true, helper.SocketPath},
	} {
		if got := startSocket(c.helperMode, c.installed, own); got != c.want {
			t.Errorf("startSocket(%v, %v) = %q, want %q", c.helperMode, c.installed, got, c.want)
		}
	}
}
