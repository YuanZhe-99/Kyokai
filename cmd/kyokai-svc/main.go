// kyokai-svc is Kyokai's headless engine (doc/en-us/architecture.md):
//
//	kyokai-svc run --host user [--socket P] [--state-dir D] [--core LIB]
//	               [--parent-pid N | --lifeline-fd N]
//	kyokai-svc ctl status | password | regenerate | shutdown | network [--socket P]
//	kyokai-svc ctl network-set --server URL [--server URL…] --name N
//
// network-set takes the network secret from KYOKAI_NETWORK_SECRET (unset
// keeps the stored one).| password | regenerate | shutdown [--socket P]
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
	"strings"
	"syscall"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// networkSecretEnv carries the network secret to `ctl network-set`.
const networkSecretEnv = "KYOKAI_NETWORK_SECRET"

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
	fmt.Fprintln(os.Stderr, "usage: kyokai-svc run --host user [flags] | ctl status|password|regenerate|shutdown|network|network-set | version")
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
	var servers []string
	fs.Func("server", "network-set: an EasyTier server URL (repeatable)", func(s string) error {
		servers = append(servers, s)
		return nil
	})
	name := fs.String("name", "", "network-set: the network name")
	// The command comes first, its flags after it.
	if len(args) == 0 {
		usage()
	}
	fs.Parse(args[1:])
	if fs.NArg() != 0 {
		usage()
	}
	req := localapi.Request{Type: map[string]localapi.RequestType{
		"status": localapi.RequestStatus, "password": localapi.RequestPasswordReveal,
		"regenerate": localapi.RequestPasswordRegenerate, "shutdown": localapi.RequestShutdown,
		"network": localapi.RequestNetworkGet, "network-set": localapi.RequestNetworkSet,
	}[args[0]]}
	if req.Type == "" {
		usage()
	}
	if req.Type == localapi.RequestNetworkSet {
		// The secret comes from the environment, never from the command line,
		// where other processes could read it.
		req.Network = &localapi.NetworkSettings{Servers: servers, Name: *name}
		if s, ok := os.LookupEnv(networkSecretEnv); ok {
			req.Network.Secret = &s
		}
	}
	resp, err := localapi.NewClient(*socket).Do(context.Background(), req)
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
		n := s.Network
		fmt.Printf("network %s", n.State)
		if n.Error != "" {
			fmt.Printf(": %s", n.Error)
		}
		if n.Address != "" {
			fmt.Printf(", address %s, NAT %s", n.Address, n.NAT)
		}
		fmt.Println()
		for _, d := range n.Devices {
			fmt.Printf("  device %s  %s  %d ms\n", d.DeviceID, d.Path, d.LatencyMS)
		}
	case resp.Network != nil:
		n := resp.Network
		fmt.Printf("servers %s\nname %q\nsecret set: %v (weak: %v)\n",
			strings.Join(n.Servers, ", "), n.Name, n.HasSecret, n.WeakSecret)
	case resp.Password != "":
		fmt.Println(resp.Password)
	default:
		fmt.Println("ok")
	}
	return nil
}
