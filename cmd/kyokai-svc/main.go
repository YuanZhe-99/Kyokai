// kyokai-svc is Kyokai's headless engine (doc/en-us/architecture.md):
//
//	kyokai-svc run --host user [--socket P] [--state-dir D] [--core LIB]
//	               [--parent-pid N | --lifeline-fd N]
//	kyokai-svc ctl status | password | regenerate | shutdown | network | trusted [--socket P]
//	kyokai-svc ctl network-set --server URL [--server URL…] --name N
//	kyokai-svc ctl permanent [--remove]
//	kyokai-svc ctl connect --device ID [--want P,P…]
//	kyokai-svc ctl close --id N
//	kyokai-svc ctl allow --id N [--grant P,P…] | deny --id N
//	kyokai-svc ctl untrust --key K | forget --device ID
//
// Secrets come from the environment, never the command line: network-set
// reads KYOKAI_NETWORK_SECRET (unset keeps the stored one), permanent reads
// KYOKAI_PERMANENT_PASSWORD, connect reads KYOKAI_PASSWORD.
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
	fmt.Fprintln(os.Stderr, "usage: kyokai-svc run --host user [flags] | ctl <command> [flags] (see the package doc) | version")
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

// Environment variables that carry secrets to `ctl`.
const (
	permanentEnv = "KYOKAI_PERMANENT_PASSWORD"
	passwordEnv  = "KYOKAI_PASSWORD"
)

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
	device := fs.String("device", "", "connect, forget: the Device ID")
	want := fs.String("want", localapi.PermViewScreen+","+localapi.PermControlInput, "connect: the permissions to ask for")
	grant := fs.String("grant", "", "allow: the permissions to grant (default: all asked for)")
	id := fs.Uint64("id", 0, "close, allow, deny: the session or request id")
	key := fs.String("key", "", "untrust: the device key")
	remove := fs.Bool("remove", false, "permanent: remove the permanent password")
	// The command comes first, its flags after it.
	if len(args) == 0 {
		usage()
	}
	fs.Parse(args[1:])
	if fs.NArg() != 0 {
		usage()
	}
	cmd := args[0]
	req := localapi.Request{Type: map[string]localapi.RequestType{
		"status": localapi.RequestStatus, "password": localapi.RequestPasswordReveal,
		"regenerate": localapi.RequestPasswordRegenerate, "shutdown": localapi.RequestShutdown,
		"network": localapi.RequestNetworkGet, "network-set": localapi.RequestNetworkSet,
		"permanent": localapi.RequestPermanentSet, "connect": localapi.RequestConnect,
		"close": localapi.RequestClose, "allow": localapi.RequestDecide, "deny": localapi.RequestDecide,
		"trusted": localapi.RequestTrustedList, "untrust": localapi.RequestTrustedRemove,
		"forget": localapi.RequestHostForget,
	}[cmd], ID: *id, Key: *key, DeviceID: *device}
	if req.Type == "" {
		usage()
	}
	client := localapi.NewClient(*socket)
	// The secrets come from the environment, never from the command line,
	// where other processes could read them.
	switch cmd {
	case "network-set":
		req.Network = &localapi.NetworkSettings{Servers: servers, Name: *name}
		if s, ok := os.LookupEnv(networkSecretEnv); ok {
			req.Network.Secret = &s
		}
	case "permanent":
		req.Permanent = &localapi.PermanentPassword{}
		if !*remove {
			p, ok := os.LookupEnv(permanentEnv)
			if !ok {
				return fmt.Errorf("set %s (or pass --remove)", permanentEnv)
			}
			req.Permanent.Password = &p
		}
	case "connect":
		p, ok := os.LookupEnv(passwordEnv)
		if !ok {
			return fmt.Errorf("set %s to the host's password", passwordEnv)
		}
		req.Connect = &localapi.ConnectRequest{DeviceID: *device, Password: p, Want: list(*want)}
	case "allow", "deny":
		req.Decide = &localapi.Decision{ID: *id, Allow: cmd == "allow", Permissions: list(*grant)}
		if cmd == "allow" && *grant == "" {
			// Grant everything that was asked for.
			st, err := client.Do(context.Background(), localapi.Request{Type: localapi.RequestStatus})
			if err != nil {
				return err
			}
			for _, r := range st.Status.Requests {
				if r.ID == *id {
					req.Decide.Permissions = r.Want
				}
			}
		}
	}
	resp, err := client.Do(context.Background(), req)
	if errors.Is(err, localapi.ErrNotRunning) {
		return errors.New("the engine is not running")
	}
	if err != nil {
		return err
	}
	switch {
	case resp.Status != nil:
		printStatus(resp.Status)
	case resp.Network != nil:
		n := resp.Network
		fmt.Printf("servers %s\nname %q\nsecret set: %v (weak: %v)\n",
			strings.Join(n.Servers, ", "), n.Name, n.HasSecret, n.WeakSecret)
	case resp.Password != "":
		fmt.Println(resp.Password)
	case resp.ID != 0:
		fmt.Println("session", resp.ID)
	case req.Type == localapi.RequestTrustedList:
		for _, t := range resp.Trusted {
			fmt.Printf("%s  %s  %s\n", t.DeviceID, strings.Join(t.Granted, ","), t.Key)
		}
	default:
		fmt.Println("ok")
	}
	return nil
}

// list splits a comma-separated flag value, dropping empty parts.
func list(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// printStatus writes the engine's status for people and scripts.
func printStatus(s *localapi.Status) {
	fmt.Printf("version %s (ABI %d), run mode %s\ndevice ID %s\nfingerprint %s\nstate %s (secrets: %s)\npermanent password %v\n",
		s.Version, s.ABI, s.RunMode, s.DeviceID, s.Fingerprint, s.StateDir, s.Persistence, s.PermanentPassword)
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
	for _, r := range s.Requests {
		fmt.Printf("request %d  from %s  wants %s  known %v  new key %v\n",
			r.ID, r.DeviceID, strings.Join(r.Want, ","), r.Known, r.NewKey)
	}
	for _, x := range s.Sessions {
		fmt.Printf("session %d  %s  %s  %s", x.ID, x.Role, x.DeviceID, x.State)
		if len(x.Failure) > 0 && string(x.Failure) != "null" {
			fmt.Printf("  %s", x.Failure)
		}
		if len(x.Granted) > 0 {
			fmt.Printf("  granted %s", strings.Join(x.Granted, ","))
		}
		fmt.Println()
	}
}
