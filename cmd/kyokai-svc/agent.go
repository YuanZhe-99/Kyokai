package main

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/engine"
	helperpkg "github.com/YuanZhe-99/Kyokai/internal/helper"
)

// agentCmd is the session agent's life (doc/en-us/run-modes.md): on
// Windows started by the helper as SYSTEM in the console session, opening
// the helper's two pipes; on macOS started by launchd in the user's session
// (or at the login window), connecting to the helper's socket. It does the
// desktop's work until the helper goes.
//
//	kyokai-svc agent --down PIPE --up PIPE --helper-pid N   (Windows)
//	kyokai-svc agent --socket PATH                          (macOS)
//
// Side effects: on Windows its stderr, with the core's log lines and any
// crash trace, goes to the helper's logs\agent.log unless No logs is on
// (macOS: launchd's own log for the agent).
func agentCmd(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	down := fs.String("down", "", "the pipe the helper writes requests to (Windows)")
	up := fs.String("up", "", "the pipe the agent writes replies and video to (Windows)")
	helper := fs.Uint("helper-pid", 0, "the helper that started this agent (Windows)")
	socket := fs.String("socket", "", "the helper's socket for its agent (macOS)")
	fs.Parse(args)
	if runtime.GOOS == "windows" {
		engine.CrashLog(filepath.Join(helperpkg.StateDir, "logs", "agent.log"))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	lib, err := core.Load(core.LibraryPath(filepath.Dir(exe)))
	if err != nil {
		return err
	}
	if *socket != "" {
		return lib.Call("agent.serve", map[string]any{"socket": *socket}, nil)
	}
	return lib.Call("agent.serve", map[string]any{"down": *down, "up": *up, "helper_pid": *helper}, nil)
}
