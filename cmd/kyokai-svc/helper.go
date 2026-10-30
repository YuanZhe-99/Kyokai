package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/engine"
	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// helperCmd installs, removes or reports the privileged helper
// (doc/en-us/run-modes.md).
//
//	kyokai-svc helper install [--from DIR] [--app-dir DIR] [--operator-sid SID | --operator-uid N]   (administrator)
//	kyokai-svc helper uninstall [--purge]                          (administrator)
//	kyokai-svc helper status
func helperCmd(args []string) error {
	if len(args) == 0 {
		usage()
	}
	fs := flag.NewFlagSet("helper", flag.ExitOnError)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	from := fs.String("from", filepath.Dir(exe), "install: the folder holding kyokai-svc and the core library")
	operator := fs.String("operator-sid", localapi.CurrentSID(), "install: the user who may use the helper (Windows)")
	operatorUID := fs.Uint("operator-uid", uint(os.Getuid()), "install: the user who may use the helper (macOS, Linux)")
	appDir := fs.String("app-dir", "", "install: the app's folder the helper removes itself without (default: --from; empty for an AppImage, which has none)")
	purge := fs.Bool("purge", false, "uninstall: also delete the helper's identity and trusted devices")
	fs.Parse(args[1:])
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["app-dir"] {
		*appDir = *from
	}
	switch args[0] {
	case "install":
		if err := helper.Install(helper.InstallOptions{From: *from, AppDir: *appDir, OperatorSID: *operator, OperatorUID: uint32(*operatorUID)}); err != nil {
			return err
		}
		fmt.Println("helper installed")
	case "uninstall":
		if err := helper.Uninstall(*purge); err != nil {
			return err
		}
		fmt.Println("helper removed")
	case "status":
		out, _ := json.Marshal(helper.Inspect())
		fmt.Println(string(out))
	default:
		usage()
	}
	return nil
}

// runHelper runs the engine as the privileged helper until ctx ends: the
// helper's state and socket, serving its operator and the kyokai group.
func runHelper(ctx context.Context, opts engine.Options) error {
	s, err := helper.ReadSettings()
	if err != nil {
		return err
	}
	opts.Socket, opts.StateDir = helper.SocketPath, helper.StateDir
	opts.Helper = &localapi.Authorizer{
		OwnerUID:    uint32(os.Getuid()),
		OwnerSID:    localapi.CurrentSID(),
		OperatorSID: s.OperatorSID,
		GroupSID:    helper.GroupSID(),
		OperatorUID: s.OperatorUID,
	}
	if gid, ok := helper.GroupGID(); ok {
		opts.Helper.GroupGID = &gid
	}
	opts.Logf = engine.Logger(filepath.Join(opts.StateDir, "logs", "engine.log"))
	opts.Update = func(from string) error { return helper.SelfUpdate(from, version.Version) }
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		opts.AgentSocket = helper.AgentSocket
	}
	// Removing privileges needs no administrator: the helper, SYSTEM or
	// root already, removes itself when its operator asks.
	opts.Remove = func() error {
		go func() {
			time.Sleep(time.Second) // the reply goes out first
			if err := helper.Retire(); err != nil {
				opts.Logf("removing the helper: %v", err)
			}
			os.Exit(0)
		}()
		return nil
	}
	go watchApp(ctx, opts.Logf)
	return engine.Run(ctx, opts)
}

// watchApp removes the helper once the app it came from is gone: missing at
// two checks a minute apart, so an app update replacing its files is not
// taken for a removal (doc/en-us/run-modes.md, "Self-disable").
// Side effects: deletes the service and the helper's state, then exits.
func watchApp(ctx context.Context, logf func(string, ...any)) {
	missing := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		s, err := helper.ReadSettings()
		if err != nil || !s.AppGone() {
			missing = 0
			continue
		}
		if missing++; missing < 2 {
			continue
		}
		logf("the app is gone from %s: removing the helper", s.AppDir)
		if err := helper.Retire(); err != nil {
			logf("removing the helper: %v", err)
			return
		}
		os.Exit(0)
	}
}
