// kyokai-svc is Kyokai's headless engine (doc/en-us/architecture.md):
//
//	kyokai-svc run --host user [--socket P] [--state-dir D] [--core LIB]
//	               [--parent-pid N | --lifeline-fd N]
//	kyokai-svc run --host helper      (the KyokaiHelper service; doc/en-us/run-modes.md)
//	kyokai-svc helper install [--from DIR] [--operator-sid SID | --operator-uid N] | uninstall [--purge] | status
//	kyokai-svc agent --down PIPE --up PIPE --helper-pid N | --socket PATH   (started for the helper)
//	kyokai-svc ctl COMMAND [flags]   (the command line, as the console `kyokai`: internal/ctl, doc/en-us/cli.md)
//
// The GUI starts `run` as its child.
// KYOKAI_PROFILE selects a separate development instance.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/ctl"
	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "ctl":
		os.Exit(ctl.Main("kyokai-svc ctl", os.Args[2:]))
	case "helper":
		err = helperCmd(os.Args[2:])
	case "agent":
		err = agentCmd(os.Args[2:])
	case "version":
		fmt.Println(version.Version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kyokai-svc:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kyokai-svc run --host user|helper [flags] | ctl <command> [flags] | helper install|uninstall|status (see the package doc) | version")
	os.Exit(2)
}

// run starts the engine in the foreground until a signal, the lifeline,
// the parent process or a shutdown request ends it.
func run(args []string) error {
	profile := os.Getenv("KYOKAI_PROFILE")
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	host := fs.String("host", "user", `host kind: "user" (No privileges) or "helper" (the privileged helper)`)
	socket := fs.String("socket", localapi.UserSocketPath(profile), "local API socket path")
	stateDir := fs.String("state-dir", "", "state directory (default: the per-user Kyokai folder)")
	lib := fs.String("core", "", "core library (default: next to this executable)")
	parent := fs.Int("parent-pid", 0, "stop when this process exits")
	lifeline := fs.Int("lifeline-fd", -1, "file descriptor whose EOF stops the engine")
	fs.Parse(args)
	if *host != "user" && *host != "helper" {
		return fmt.Errorf("unknown host %q", *host)
	}
	opts := engine.Options{Version: version.Version, Socket: *socket, StateDir: *stateDir, Profile: profile, CoreLib: *lib, ParentPID: *parent}
	if *host == "helper" {
		// The helper runs its own installed copies, with its own state.
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		opts.CoreLib, opts.ParentPID, opts.Profile = core.LibraryPath(filepath.Dir(exe)), 0, ""
		run := func(ctx context.Context) error { return runHelper(ctx, opts) }
		if handled, err := asService(run); handled || err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return run(ctx)
	}
	if opts.StateDir == "" {
		dir, err := engine.DefaultStateDir(profile)
		if err != nil {
			return err
		}
		opts.StateDir = dir
	}
	if opts.CoreLib == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		opts.CoreLib = core.LibraryPath(filepath.Dir(exe))
	}
	if *lifeline >= 0 {
		opts.Lifeline = os.NewFile(uintptr(*lifeline), "lifeline")
	}
	opts.Logf = engine.Logger(filepath.Join(opts.StateDir, "logs", "engine.log"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return engine.Run(ctx, opts)
}
