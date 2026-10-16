// kyokai-svc is Kyokai's headless engine (doc/en-us/architecture.md):
//
//	kyokai-svc run --host user [--socket P] [--state-dir D] [--core LIB]
//	               [--parent-pid N | --lifeline-fd N]
//	kyokai-svc ctl status | password | regenerate | shutdown [--socket P]
//
// The GUI starts `run` as its child; `ctl` is for scripts and development.
// KYOKAI_PROFILE selects a separate development instance.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/YuanZhe-99/Kyokai/internal/core"
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
		err = ctl(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: kyokai-svc run --host user [flags] | ctl status|password|regenerate|shutdown | version")
	os.Exit(2)
}

// run starts the engine in the foreground until a signal, the lifeline,
// the parent process or a shutdown request ends it.
func run(args []string) error {
	profile := os.Getenv("KYOKAI_PROFILE")
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	host := fs.String("host", "user", `host kind: "user" (No privileges)`)
	socket := fs.String("socket", localapi.UserSocketPath(profile), "local API socket path")
	stateDir := fs.String("state-dir", "", "state directory (default: the per-user Kyokai folder)")
	lib := fs.String("core", "", "core library (default: next to this executable)")
	parent := fs.Int("parent-pid", 0, "stop when this process exits")
	lifeline := fs.Int("lifeline-fd", -1, "file descriptor whose EOF stops the engine")
	fs.Parse(args)
	if *host != "user" {
		return fmt.Errorf("host %q is not available yet", *host)
	}
	opts := engine.Options{Version: version.Version, Socket: *socket, StateDir: *stateDir, Profile: profile, CoreLib: *lib, ParentPID: *parent}
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
		opts.CoreLib = filepath.Join(filepath.Dir(exe), core.LibraryName())
	}
	if *lifeline >= 0 {
		opts.Lifeline = os.NewFile(uintptr(*lifeline), "lifeline")
	}
	opts.Logf = engine.Logger(filepath.Join(opts.StateDir, "logs", "engine.log"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return engine.Run(ctx, opts)
}

// ctl sends one request to a running engine and prints the answer.
func ctl(args []string) error {
	fs := flag.NewFlagSet("ctl", flag.ExitOnError)
	socket := fs.String("socket", localapi.UserSocketPath(os.Getenv("KYOKAI_PROFILE")), "local API socket path")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	req := map[string]localapi.RequestType{
		"status": localapi.RequestStatus, "password": localapi.RequestPasswordReveal,
		"regenerate": localapi.RequestPasswordRegenerate, "shutdown": localapi.RequestShutdown,
	}[fs.Arg(0)]
	if req == "" {
		usage()
	}
	resp, err := localapi.NewClient(*socket).Do(context.Background(), localapi.Request{Type: req})
	if errors.Is(err, localapi.ErrNotRunning) {
		return errors.New("the engine is not running")
	}
	if err != nil {
		return err
	}
	switch {
	case resp.Status != nil:
		s := resp.Status
		fmt.Printf("version %s (ABI %d), run mode %s\ndevice ID %s\nfingerprint %s\nstate %s (secrets: %s)\n",
			s.Version, s.ABI, s.RunMode, s.DeviceID, s.Fingerprint, s.StateDir, s.Persistence)
	case resp.Password != "":
		fmt.Println(resp.Password)
	default:
		fmt.Println("ok")
	}
	return nil
}
