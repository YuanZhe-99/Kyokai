package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/YuanZhe-99/Kyokai/internal/core"
)

// agentCmd is the session agent's life (doc/en-us/run-modes.md): started
// by the helper as SYSTEM in the console session, it opens the helper's two
// pipes and does the desktop's work until the helper goes.
//
//	kyokai-svc agent --down PIPE --up PIPE --helper-pid N
func agentCmd(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	down := fs.String("down", "", "the pipe the helper writes requests to")
	up := fs.String("up", "", "the pipe the agent writes replies and video to")
	helper := fs.Uint("helper-pid", 0, "the helper that started this agent")
	fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	lib, err := core.Load(filepath.Join(filepath.Dir(exe), core.LibraryName()))
	if err != nil {
		return err
	}
	return lib.Call("agent.serve", map[string]any{"down": *down, "up": *up, "helper_pid": *helper}, nil)
}
