// Package ctl is Kyokai's command line (doc/en-us/cli.md): one request to the
// user's own engine over the local API per command, its answer printed. It
// runs as the console `kyokai` in the app's bin folder and as
// `kyokai-svc ctl`; it may do nothing the GUI may not.
package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/helper"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	"github.com/YuanZhe-99/Kyokai/internal/version"
)

// networkSecretEnv carries the network secret to `ctl network-set` and
// `ctl network-profile`.
const networkSecretEnv = "KYOKAI_NETWORK_SECRET"

// Environment variables that carry secrets to `ctl`.
const (
	permanentEnv  = "KYOKAI_PERMANENT_PASSWORD"
	passwordEnv   = "KYOKAI_PASSWORD"
	passphraseEnv = "KYOKAI_BACKUP_PASSPHRASE"
	aiKeyEnv      = "KYOKAI_AI_KEY"
)

// Exit codes the command line ends with.
const (
	ExitOK         = 0 // done (also: help shown)
	ExitFailed     = 1 // anything else went wrong
	ExitUsage      = 2 // an unknown command or a wrong flag
	ExitNotRunning = 3 // no engine listens on the socket
	ExitRefused    = 4 // the engine refused the request (the message says why)
)

// errUsage reports a command line already explained on standard error.
var errUsage = errors.New("usage")

// prog is what the program is called in messages; jsonOut is --json.
var (
	prog    = "kyokai"
	jsonOut bool
)

// Main runs one command line and returns its exit code.
// Inputs: name, the program as messages name it ("kyokai", "kyokai-svc
// ctl"); args, the command and its flags.
// Side effects: talks to the user's engine; prints to stdout and stderr.
func Main(name string, args []string) int {
	prog, jsonOut = name, false
	err := run(args)
	var refused *localapi.RequestError
	code := ExitFailed
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.Is(err, errUsage):
		return ExitUsage
	case errors.As(err, &refused):
		code = ExitRefused
	case errors.Is(err, localapi.ErrNotRunning):
		code = ExitNotRunning
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", prog, err)
	return code
}

// newFlags makes a command's flag set with the flags every command takes:
// --socket, and --json unless the command streams.
// Returns: the flag set and the socket flag.
// Notes: a wrong flag prints the command's help and makes parse fail.
func newFlags(cmd string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(prog+" "+cmd, flag.ContinueOnError)
	fs.Usage = func() { commandHelp(fs, cmd) }
	socket := fs.String("socket", localapi.UserSocketPath(os.Getenv("KYOKAI_PROFILE")), "the engine's local API socket")
	if !streams[cmd] {
		fs.BoolVar(&jsonOut, "json", false, "print the engine's answer as JSON")
	}
	return fs, socket
}

// parse parses a command's flags, which come after it.
// Returns: nil, flag.ErrHelp for --help, or errUsage (already explained).
func parse(fs *flag.FlagSet, args []string) error {
	if slices.ContainsFunc(args, func(a string) bool { return a == "-h" || strings.TrimLeft(a, "-") == "help" }) {
		fs.SetOutput(os.Stdout)
	}
	err := fs.Parse(args)
	switch {
	case err == nil && fs.NArg() > 0:
		fmt.Fprintf(os.Stderr, "%s: unexpected %q\n", fs.Name(), fs.Arg(0))
		return errUsage
	case err == nil, errors.Is(err, flag.ErrHelp):
		return err
	}
	return errUsage
}

// printJSON writes v as indented JSON, for --json.
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

// requests are the commands that make one request of their own type.
var requests = map[string]localapi.RequestType{
	"status": localapi.RequestStatus, "password": localapi.RequestPasswordReveal,
	"regenerate": localapi.RequestPasswordRegenerate, "shutdown": localapi.RequestShutdown,
	"network": localapi.RequestNetworkGet, "network-set": localapi.RequestNetworkSet,
	"networks": localapi.RequestNetworkProfiles, "network-profile": localapi.RequestNetworkProfileSet, "network-remove": localapi.RequestNetworkProfileRemove, "network-direct": localapi.RequestNetworkDirect, "name-set": localapi.RequestNameSet, "access": localapi.RequestAccessSettings, "password-grants": localapi.RequestPasswordGrants, "background": localapi.RequestBackground, "capture-show": localapi.RequestCaptureShow, "no-logs": localapi.RequestLogs, "protect": localapi.RequestProtect, "helper-update": localapi.RequestHelperUpdate, "clipboard-mine": localapi.RequestClipboardMine, "network-backend": localapi.RequestNetworkBackend, "cores": localapi.RequestCores, "core-install": localapi.RequestCoreInstall,
	"permanent": localapi.RequestPermanentSet, "connect": localapi.RequestConnect,
	"close": localapi.RequestClose, "allow": localapi.RequestDecide, "deny": localapi.RequestDecide,
	"trusted": localapi.RequestTrustedList, "untrust": localapi.RequestTrustedRemove,
	"forget": localapi.RequestHostForget, "remove": localapi.RequestDeviceRemove, "identify": localapi.RequestIdentify, "backup": localapi.RequestIdentityExport,
	"restore": localapi.RequestIdentityImport, "reset-identity": localapi.RequestIdentityReset,
	"ai": localapi.RequestAISettingsGet, "ai-set": localapi.RequestAISettingsSet, "ai-key": localapi.RequestAIKeySet, "ai-models": localapi.RequestAIModels,
	"ai-start": localapi.RequestAITaskStart, "ai-reply": localapi.RequestAITaskReply, "ai-tasks": localapi.RequestAITasks,
	"ai-pause": localapi.RequestAITaskPause, "ai-resume": localapi.RequestAITaskResume, "ai-cancel": localapi.RequestAITaskCancel,
	"ai-approve": localapi.RequestAITaskDecide, "ai-decline": localapi.RequestAITaskDecide,
	"ai-clear": localapi.RequestAIHistoryClear, "ai-stop": localapi.RequestAIStop,
	"ai-rename": localapi.RequestAITaskRename, "ai-export": localapi.RequestAITaskExport, "ai-fork": localapi.RequestAITaskFork, "ai-compact": localapi.RequestAITaskCompact,
	"ai-scope": localapi.RequestAIScopeSet, "ai-apps": localapi.RequestAIApps,
}

// run sends one request to the user's engine and prints the answer.
func run(args []string) error {
	// Nothing goes to the helper's socket unless the helper listens there.
	localapi.RequireServer(helper.SocketPath, helper.IsHelper)
	if len(args) == 0 {
		overview(os.Stderr)
		return errUsage
	}
	cmd := args[0]
	switch cmd {
	case "help", "-h", "-help", "--help":
		return help(args[1:])
	case "version":
		fmt.Println(version.Version)
		return nil
	}
	if summary(cmd) == "" {
		fmt.Fprintf(os.Stderr, "%s: unknown command %q (%s help lists them)\n", prog, cmd, prog)
		return errUsage
	}
	if f, ok := ctlServices[cmd]; ok {
		return f(args)
	}
	fs, socket := newFlags(cmd)
	var servers []string
	fs.Func("server", "network-set, network-profile: an EasyTier server URL (repeatable)", func(s string) error {
		servers = append(servers, s)
		return nil
	})
	address := fs.String("address", "", "identify, connect: the host's IP address or domain name (connect: instead of the network)")
	byAddress := fs.String("by-address", "", "access: accept connections by address (true, false)")
	ranges := fs.String("ranges", "", "access: the address ranges connections by address are accepted from (comma-separated; with --by-address)")
	direct := fs.Bool("direct", false, "network-set: try direct (hole-punched) paths; omitted keeps the current choice")
	name := fs.String("name", "", "network-set, network-profile: the network name; name-set: this device's friendly name; ai-rename: the task's name (empty: its instruction names it)")
	network := fs.String("network", "", "network-profile, network-remove: the network's id (network-profile: none adds one)")
	label := fs.String("label", "", "network-profile: the name the network is shown by (empty: its network name)")
	joined := fs.String("joined", "", "network-profile: join the network or leave it (true, false)")
	tomlFile := fs.String("toml-file", "", "network-profile: a file with the official core's own options for the network (EasyTier chosen)")
	easytier := fs.String("easytier", "", "network-backend: the official EasyTier core's tag (empty: Kyokai Network)")
	tag := fs.String("tag", "", "core-install: the EasyTier release to download")
	device := fs.String("device", "", "connect, forget, remove: the Device ID")
	want := fs.String("want", localapi.PermViewScreen+","+localapi.PermControlInput, "connect: the permissions to ask for")
	grant := fs.String("grant", "", "allow: the permissions to grant (default: all asked for); password-grants: what a correct password grants without asking (empty: none)")
	id := fs.Uint64("id", 0, "close, allow, deny, screenshot, watch: the session or request id; ai-reply, ai-pause, ai-resume, ai-cancel, ai-approve, ai-decline, ai-rename, ai-export, ai-fork, ai-compact: the task")
	key := fs.String("key", "", "untrust: the device key")
	remove := fs.Bool("remove", false, "permanent: remove the permanent password")
	file := fs.String("file", "", "backup, restore: the backup file")
	yes := fs.Bool("yes", false, "reset-identity: confirm a new Device ID")
	from := fs.String("from", "", "helper-update: the app folder holding the signed bundle (must be the one the helper was installed from)")
	digest := fs.String("digest", "", "clipboard-mine: the SHA-256 of the clipboard's content, lowercase hex")
	to := fs.String("to", "", "move: the socket of the engine that takes over this device's store")
	mode := fs.String("mode", "", "access: which passwords open this host (temporary, permanent, both)")
	withoutAsking := fs.String("without-asking", "", "access: whether an accepted password admits without asking (true, false)")
	publish := fs.String("publish", "", "name-set: show the friendly name in the networks before any login (true, false)")
	on := fs.Bool("on", false, "background: serve from boot (else only while a GUI is attached); capture-show: captures show Kyokai's windows; no-logs: keep no logs or connection history; protect: protected mode; network-direct: try direct paths in every network")
	seconds := fs.Int("seconds", 5, "watch: how long to count frames")
	snapshot := fs.String("snapshot", "", "watch: decode the stream and save the last picture as PNG")
	quality := fs.String("quality", "", "watch: ask for a preset first (speed, balanced, best)")
	fpsCap := fs.Int("fps", 0, "watch: ask for a frame-rate cap first")
	codec := fs.String("codec", "", "watch: ask for a codec first (auto, or one the host offers: h264, hevc, vp9, vp9-sw, av1, vp8)")
	click := fs.String("click", "", "watch: click at X,Y (fractions of the picture) after 2 s")
	typed := fs.String("type", "", "watch: type this text after 3 s, as the viewer's text input")
	keys := fs.String("keys", "", "watch: press these keys by name after 3 s, after --type (e.g. meta+a,backspace,enter)")
	keysEvery := fs.Duration("keys-every", 0, "watch: press the --keys again at this interval until the watch ends (0: once)")
	display := fs.Int("display", -1, "watch: show the host's display N (0 first) from the start")
	actions := fs.String("action", "", "watch: session actions to send, 4 s apart from 2 s on (lock, ctrl-alt-del, block, unblock, privacy-on, privacy-off)")
	source := fs.String("source", "", "ai-key, ai-models: the AI source's id")
	model := fs.String("model", "", "ai-set: the model tasks use, as SOURCE:MODEL; ai-start: this task's model (none: the settings'); ai-reply: another model of the task's source")
	thinking := fs.String("thinking", "", "ai-start, ai-reply: how much the model thinks: low, medium, high or max (none: the usual)")
	sourcesFile := fs.String("sources-file", "", "ai-set: a JSON list of AI sources replacing the current ones")
	approval := fs.String("approval", "", "ai-set: auto or every-action")
	text := fs.String("text", "", "ai-start, ai-reply, watch: what the AI should do (watch: on the host)")
	apps := fs.String("apps", "", "ai-scope: the programs the AI may work in, as ai-apps names them (comma-separated; empty: the whole desktop)")
	run := fs.Int("run", 0, "ai-fork: the run to begin after, 1 first as ai-tasks lists them (none: the latest, a clone)")
	out := fs.String("out", "", "screenshot: the PNG file to write; ai-export: the Markdown file to write (none: standard output)")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	switch cmd {
	case "screenshot":
		return screenshot(*socket, *id, *out, *display)
	case "move":
		dest := "user"
		if *to == helper.SocketPath {
			dest = "helper"
		}
		id, err := localapi.MoveStore(context.Background(), localapi.NewClient(*socket), localapi.NewClient(*to), localapi.MoveOptions{To: dest})
		switch {
		case err != nil:
			return err
		case jsonOut:
			return printJSON(map[string]string{"device_id": id, "socket": *to})
		}
		fmt.Println("moved: device", id, "is now served by", *to)
		return nil
	case "watch":
		return watch(*socket, *id, time.Duration(*seconds)*time.Second, *snapshot, *quality, *fpsCap, *codec, *text, *click, *typed, *keys, *keysEvery, *actions, *display)
	}
	req := localapi.Request{Type: requests[cmd], ID: *id, Key: *key, DeviceID: *device, Address: *address}
	client := localapi.NewClient(*socket)
	// The secrets come from the environment, never from the command line,
	// where other processes could read them.
	switch cmd {
	case "name-set":
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "name" {
				req.Name = name
			}
		})
		if *publish != "" {
			p := *publish == "true"
			req.PublishName = &p
		}
	case "access":
		req.PasswordMode = *mode
		if *withoutAsking != "" {
			on := *withoutAsking == "true"
			req.WithoutAsking = &on
		}
		if *byAddress != "" {
			req.ByAddress = &localapi.ByAddress{On: *byAddress == "true", Ranges: append([]string{}, list(*ranges)...)}
		}
	case "password-grants":
		g := append([]string{}, list(*grant)...) // never null: empty grants nothing
		req.Grants = &g
	case "background":
		req.Background = on
	case "capture-show":
		req.Show = on
	case "network-direct":
		req.Direct = on
	case "network-remove":
		req.Profile = &localapi.NetworkChange{ID: *network}
	case "network-profile":
		req.Profile = &localapi.NetworkChange{ID: *network, Servers: servers}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "label":
				req.Profile.Label = label
			case "name":
				req.Profile.Name = name
			}
		})
		if *joined != "" {
			j := *joined == "true"
			req.Profile.Joined = &j
		}
		if s, ok := os.LookupEnv(networkSecretEnv); ok {
			req.Profile.Secret = &s
		}
		if *tomlFile != "" {
			b, err := os.ReadFile(*tomlFile)
			if err != nil {
				return err
			}
			t := string(b)
			req.Profile.TOML = &t
		}
	case "network-backend":
		req.EasyTier = easytier
	case "core-install":
		req.Tag = *tag
	case "no-logs":
		req.LogsOff = on
	case "protect":
		// Off needs the permanent password, from the environment.
		req.Protect = &localapi.ProtectRequest{On: *on, Password: os.Getenv(permanentEnv)}
	case "helper-update":
		req.From = *from
	case "clipboard-mine":
		req.Digest = *digest
	case "network-set":
		req.Network = &localapi.NetworkSettings{Servers: servers, Name: *name}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "direct" {
				req.Network.Direct = direct
			}
		})
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
			return fmt.Errorf("set %s to the host's password (empty asks without one)", passwordEnv)
		}
		req.Connect = &localapi.ConnectRequest{DeviceID: *device, Password: p, Want: list(*want), Address: *address}
	case "backup", "restore":
		p, ok := os.LookupEnv(passphraseEnv)
		if !ok || *file == "" {
			return fmt.Errorf("set %s and pass --file", passphraseEnv)
		}
		req.Backup = &localapi.IdentityBackup{Passphrase: p}
		if cmd == "restore" {
			doc, err := os.ReadFile(*file)
			if err != nil {
				return err
			}
			req.Backup.Document = string(doc)
		}
	case "ai-set":
		req.AISettings = &localapi.AISettings{Approval: *approval}
		if *model != "" {
			s, id, ok := strings.Cut(*model, ":")
			if !ok {
				return errors.New("--model wants SOURCE:MODEL")
			}
			req.AISettings.Model = &localapi.AIModelRef{Source: s, ID: id}
		}
		if *sourcesFile != "" {
			data, err := os.ReadFile(*sourcesFile)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &req.AISettings.Sources); err != nil {
				return fmt.Errorf("%s: %w", *sourcesFile, err)
			}
		}
	case "ai-models":
		req.Source = *source
	case "ai-scope":
		if a := list(*apps); len(a) > 0 {
			req.Apps = &a
		}
	case "ai-key":
		req.AIKey = &localapi.AIKey{Source: *source}
		if !*remove {
			k, ok := os.LookupEnv(aiKeyEnv)
			if !ok {
				return fmt.Errorf("set %s (or pass --remove)", aiKeyEnv)
			}
			req.AIKey.Key = &k
		}
	case "ai-start", "ai-reply":
		req.AITask = &localapi.AITaskRequest{Thinking: *thinking}
		if cmd == "ai-start" {
			req.AITask.Key, req.AITask.Instruction = strconv.FormatInt(time.Now().UnixNano(), 36), *text
		} else {
			req.AITask.Text = *text
		}
		if *model != "" {
			s, id, ok := strings.Cut(*model, ":")
			if !ok {
				return errors.New("--model wants SOURCE:MODEL")
			}
			req.AITask.Model = &localapi.AIModelRef{Source: s, ID: id}
		}
	case "ai-rename":
		req.AITask = &localapi.AITaskRequest{Name: *name}
	case "ai-fork":
		req.AITask = &localapi.AITaskRequest{}
		if *run > 0 {
			r := *run - 1
			req.AITask.Run = &r
		}
	case "ai-approve", "ai-decline":
		req.AITask = &localapi.AITaskRequest{Approve: cmd == "ai-approve"}
	case "reset-identity":
		if !*yes {
			return errors.New("this gives the device a new Device ID; pass --yes")
		}
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
	if err != nil {
		return err
	}
	if jsonOut {
		if cmd == "backup" {
			// The backup goes to its file, never to the terminal.
			if err := os.WriteFile(*file, []byte(resp.Document), 0o600); err != nil {
				return err
			}
			resp.Document = ""
		}
		return printJSON(resp)
	}
	switch {
	case resp.Status != nil:
		printStatus(resp.Status)
	case resp.Network != nil:
		n := resp.Network
		fmt.Printf("servers %s\nname %q\nsecret set: %v (weak: %v)\ndirect paths: %v\n",
			strings.Join(n.Servers, ", "), n.Name, n.HasSecret, n.WeakSecret, n.Direct != nil && *n.Direct)
	case resp.Networks != nil:
		backend := "Kyokai Network"
		if resp.Networks.EasyTier != "" {
			backend = "EasyTier " + resp.Networks.EasyTier
		}
		fmt.Printf("direct paths: %v\nrunning on: %s\n", resp.Networks.Direct, backend)
		for _, p := range resp.Networks.Profiles {
			fmt.Printf("network %s %q: name %q, servers %s, secret set: %v (weak: %v), joined: %v\n",
				p.ID, p.Label, p.Name, strings.Join(p.Servers, ", "), p.HasSecret, p.WeakSecret, p.Joined)
			if len(p.Options) > 0 {
				fmt.Printf("  EasyTier options: %s\n", strings.Join(p.Options, ", "))
			}
		}
	case resp.Cores != nil:
		fmt.Printf("downloaded: %s\n", strings.Join(resp.Cores.Installed, ", "))
		for _, r := range resp.Cores.Releases {
			fmt.Printf("available: %s (prerelease: %v)\n", r.Tag, r.Prerelease)
		}
		if resp.Cores.Error != "" {
			fmt.Println("releases not listed:", resp.Cores.Error)
		}
	case resp.DeviceID != "":
		fmt.Println("device", resp.DeviceID)
	case resp.Failure != "":
		return fmt.Errorf("no device there: %s", resp.Failure)
	case resp.Password != "":
		fmt.Println(resp.Password)
	case resp.Document != "" && cmd == "ai-export":
		if *out == "" {
			fmt.Print(resp.Document)
			return nil
		}
		if err := os.WriteFile(*out, []byte(resp.Document), 0o600); err != nil {
			return err
		}
		fmt.Println("written to", *out)
	case resp.Document != "":
		// Owner-only: the file is encrypted, but it is still the identity.
		if err := os.WriteFile(*file, []byte(resp.Document), 0o600); err != nil {
			return err
		}
		fmt.Println("backup written to", *file)
	case resp.ID != 0 && (cmd == "ai-start" || cmd == "ai-fork"):
		fmt.Println("task", resp.ID)
	case resp.ID != 0:
		fmt.Println("session", resp.ID)
	case resp.AISettings != nil:
		s := resp.AISettings
		if s.Model != nil {
			fmt.Printf("model %s:%s\n", s.Model.Source, s.Model.ID)
		}
		fmt.Printf("approval %s\n", s.Approval)
		for _, src := range s.Sources {
			ids := make([]string, len(src.Models))
			for i, m := range src.Models {
				ids[i] = m.ID
			}
			fmt.Printf("source %s %q  %s  %s  key %v  models %s\n", src.ID, src.Name, src.Protocol, src.URL, s.Keys[src.ID], strings.Join(ids, ","))
		}
	case req.Type == localapi.RequestAIApps:
		for _, a := range resp.AIApps {
			fmt.Println(a)
		}
	case req.Type == localapi.RequestAIModels:
		for _, m := range resp.AIModels {
			fmt.Printf("%s  %s\n", m.ID, m.Name)
		}
	case req.Type == localapi.RequestAITasks:
		for _, t := range resp.AITasks {
			printTask(t)
		}
	case req.Type == localapi.RequestTrustedList:
		for _, t := range resp.Trusted {
			fmt.Printf("%s %q  %s  %s\n", t.DeviceID, t.About.Name, strings.Join(t.Granted, ","), t.Key)
		}
	default:
		fmt.Println("ok")
	}
	return nil
}

// printTask writes one AI task: its state, its actions and the model's text.
func printTask(t localapi.AITask) {
	fmt.Printf("task %d  %s  %s:%s  %q\n", t.ID, t.State, t.Source, t.Model, t.Instruction)
	if t.Thinking != "" {
		fmt.Println("  thinking", t.Thinking)
	}
	if t.Name != "" {
		fmt.Printf("  name %q\n", t.Name)
	}
	if t.Resumable {
		fmt.Printf("  a reply continues it (%d tokens so far)\n", t.Context)
	}
	if t.Compacting {
		fmt.Println("  compacting")
	}
	for i, r := range t.Runs {
		fmt.Printf("  run %d  %q → %q\n", i+1, r.Message, r.Text)
	}
	if t.Paused != "" {
		fmt.Println("  paused:", t.Paused)
	}
	if t.Error != "" {
		fmt.Println("  error:", t.Error)
	}
	for _, s := range t.Steps {
		fmt.Printf("  %s  %s", s.Outcome, s.Action)
		if s.Detail != "" {
			fmt.Printf("  (%s)", s.Detail)
		}
		fmt.Println()
	}
	if t.Approval != nil {
		fmt.Printf("  waiting for approval: %s %v\n", t.Approval.Action, t.Approval.Flagged)
	}
	if t.Text != "" {
		fmt.Println("  text:", t.Text)
	}
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
	fmt.Printf("version %s (ABI %d), run mode %s\ndevice ID %s\nfingerprint %s\nstate %s (secrets: %s)\npermanent password %v\npassword grants %s\n",
		s.Version, s.ABI, s.RunMode, s.DeviceID, s.Fingerprint, s.StateDir, s.Persistence, s.PermanentPassword, strings.Join(s.PasswordGrants, ","))
	fmt.Printf("name %q (chosen %q), %s %s, %s\n", s.About.Name, s.Name, s.About.Maker, s.About.Model, s.About.OS)
	if p := s.Permissions; p != nil {
		fmt.Printf("permissions screen %v, accessibility %v, input monitoring %v, keychain %v\n", p.Screen, p.Accessibility, p.InputMonitoring, p.Keychain)
	}
	n := s.Network
	fmt.Printf("network %s", n.State)
	if n.Error != "" {
		fmt.Printf(": %s", n.Error)
	}
	if n.Address != "" {
		fmt.Printf(", address %s, NAT %s", n.Address, n.NAT)
	}
	if n.ListenPort != 0 {
		fmt.Printf(", listening on %d", n.ListenPort)
	}
	fmt.Println()
	for _, d := range n.Devices {
		fmt.Printf("  device %s  %s  %d ms\n", d.DeviceID, d.Path, d.LatencyMS)
	}
	for _, each := range n.Networks {
		fmt.Printf("  network %s %q: %s", each.ID, each.Label, each.State)
		if each.Error != "" {
			fmt.Printf(": %s", each.Error)
		}
		if each.Address != "" {
			fmt.Printf(", address %s", each.Address)
		}
		if each.ListenPort != 0 {
			fmt.Printf(", listening on %d", each.ListenPort)
		}
		fmt.Printf(", %d devices\n", len(each.Devices))
		if each.Unreachable != "" {
			fmt.Printf("    other devices cannot reach this one: %s\n", each.Unreachable)
		}
	}
	if s.LogsOff {
		fmt.Println("no logs")
	}
	if s.Protected {
		fmt.Println("protected mode")
	}
	if b := s.ByAddress; b.On {
		fmt.Printf("by address: from %s", strings.Join(b.Ranges, ", "))
		if b.Port != 0 {
			fmt.Printf(", listening on %d", b.Port)
		}
		if b.Error != "" {
			fmt.Printf(", not listening: %s", b.Error)
		}
		fmt.Println()
	}
	for _, r := range s.Requests {
		fmt.Printf("request %d  from %s %q  wants %s  known %v  new key %v  password %v  key %q  service %q\n",
			r.ID, r.DeviceID, r.About.Name, strings.Join(r.Want, ","), r.Known, r.NewKey, r.Password, r.Key, r.Service)
	}
	for _, r := range s.Recent {
		fmt.Printf("recent %s %q  %s %s  saved %+v  features %s\n", r.DeviceID, r.About.Name, r.About.Maker, r.About.Model, r.Saved, strings.Join(r.Features, ","))
		if r.Prefer != "" {
			fmt.Printf("  route chosen: %s\n", r.Prefer)
		}
		for _, rt := range r.Routes {
			probe := "not looked at"
			if p := rt.Probe; p != nil {
				probe = p.State
				if p.RTTMS != nil {
					probe += fmt.Sprintf(" %d ms", *p.RTTMS)
				}
			}
			fmt.Printf("  route %s  %s  last ok %d  rtt %d ms  %d kbit/s  fails %d\n", rt.Address, probe, rt.LastOK, rt.RTTMS, rt.KBPS, rt.Fails)
		}
	}
	for _, w := range s.Watchers {
		fmt.Printf("watcher %s %q  since %d\n", w.DeviceID, w.About.Name, w.Since)
	}
	for _, x := range s.Sessions {
		fmt.Printf("session %d  %s  %s %q  %s", x.ID, x.Role, x.DeviceID, x.Peer.Name, x.State)
		if len(x.Failure) > 0 && string(x.Failure) != "null" {
			fmt.Printf("  %s", x.Failure)
		}
		if len(x.Granted) > 0 {
			fmt.Printf("  granted %s", strings.Join(x.Granted, ","))
		}
		fmt.Println()
	}
}

// watch reads a controller session's media like the GUI's viewer does and
// prints what arrives: the stream, frames per second and bitrate, and the
// host's AI task. With a snapshot path it also decodes the frames (the
// viewer's decoder) and saves the last picture; with a text it first asks
// the host's AI to do it (the viewer's AI panel); with actions it sends them
// as the viewer's action bar does and prints the host's reports.
func watch(socket string, id uint64, d time.Duration, snapshot, quality string, fpsCap int, codec, aiText, click, typed, keys string, keysEvery time.Duration, actions string, display int) error {
	ctx, cancel := context.WithTimeout(context.Background(), d+10*time.Second)
	defer cancel()
	conn, err := localapi.NewClient(socket).Media(ctx, id)
	if err != nil {
		return err
	}
	defer conn.Close()
	var lib *core.Lib
	if snapshot != "" {
		// The decoder is tied to this thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if lib, err = core.Load(core.LibraryPath(filepath.Dir(exe))); err != nil {
			return err
		}
	}
	// Like the viewer: ask for a key frame (and the stream description) first.
	if err := localapi.WritePacket(conn, append([]byte{3}, `{"type":"key-frame"}`...)); err != nil {
		return err
	}
	// The host's sound too, counted below.
	if err := localapi.WritePacket(conn, append([]byte{3}, `{"type":"audio","on":true}`...)); err != nil {
		return err
	}
	if quality != "" || fpsCap > 0 {
		body, _ := json.Marshal(map[string]any{"type": "quality", "preset": quality, "fps": fpsCap})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
	}
	if codec != "" {
		body, _ := json.Marshal(map[string]any{"type": "codec", "codec": codec})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
	}
	if click != "" {
		// A press and release at a point of the picture, as the viewer sends
		// them (the session needs control-input).
		var x, y float64
		if _, err := fmt.Sscanf(click, "%f,%f", &x, &y); err != nil {
			return fmt.Errorf("--click wants X,Y as fractions: %w", err)
		}
		go func() {
			time.Sleep(2 * time.Second)
			for _, action := range []string{"move", "down", "up"} {
				body, _ := json.Marshal(map[string]any{"type": "input", "event": map[string]any{"kind": "pointer", "action": action, "button": 0, "x": x, "y": y}})
				_ = localapi.WritePacket(conn, append([]byte{3}, body...))
				time.Sleep(80 * time.Millisecond)
			}
		}()
	}
	if typed != "" || keys != "" {
		// Text, then named keys with their modifiers (ctrl, alt, shift,
		// meta), each pressed and released, as the viewer's Legacy keyboard
		// sends them.
		send := func(ev map[string]any) {
			body, _ := json.Marshal(map[string]any{"type": "input", "event": ev})
			_ = localapi.WritePacket(conn, append([]byte{3}, body...))
			time.Sleep(60 * time.Millisecond)
		}
		go func() {
			time.Sleep(3 * time.Second)
			if typed != "" {
				send(map[string]any{"kind": "text", "text": typed})
			}
			for {
				for k := range strings.SplitSeq(keys, ",") {
					if k == "" {
						continue
					}
					parts := strings.Split(k, "+")
					mods := map[string]bool{}
					for _, m := range parts[:len(parts)-1] {
						mods[m] = true
					}
					for _, down := range []bool{true, false} {
						send(map[string]any{"kind": "key", "key": parts[len(parts)-1], "down": down, "mods": mods})
					}
				}
				if keysEvery <= 0 || ctx.Err() != nil {
					return
				}
				time.Sleep(keysEvery)
			}
		}()
	}
	if display >= 0 {
		body, _ := json.Marshal(map[string]any{"type": "display", "index": display})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
	}
	if actions != "" {
		go func() {
			time.Sleep(2 * time.Second)
			for a := range strings.SplitSeq(actions, ",") {
				fmt.Printf("%s action %s\n", time.Now().Format("15:04:05"), a)
				body, _ := json.Marshal(map[string]any{"type": "action", "action": a})
				_ = localapi.WritePacket(conn, append([]byte{3}, body...))
				time.Sleep(4 * time.Second)
			}
		}()
	}
	if aiText != "" {
		body, _ := json.Marshal(map[string]any{"type": "ai-start", "key": strconv.FormatInt(time.Now().UnixNano(), 36), "instruction": aiText})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(d))
	var buf []byte
	var dec *core.Decoder
	var pic *image.RGBA
	// fresh: the decoder has not had a key frame yet.
	fresh := false
	frames, bytes, pictures, sounds, soundBytes := 0, 0, 0, 0, 0
	// When sound last came, its longest gap and the gaps over 30 ms: the
	// jitter the viewer's buffer has to cover.
	var soundAt time.Time
	var soundGap time.Duration
	soundLate := 0
	var decodeErr error
	printedDisplays := map[byte]bool{}
	start := time.Now()
	// Each second: the frames that came, the longest gap between two (or
	// since the last), and "stall" when that passed a second while the
	// host says it sent frames ("still" when it sent none: a still screen).
	var pace struct {
		sync.Mutex
		frames, hostFrames, delay int
		last                      time.Time
		gap, worst                time.Duration
	}
	pace.last = start
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				pace.Lock()
				gap := max(pace.gap, now.Sub(pace.last))
				n, host, delay := pace.frames, pace.hostFrames, pace.delay
				pace.frames, pace.gap, pace.worst = 0, 0, max(pace.worst, gap)
				pace.Unlock()
				note := ""
				switch {
				case gap > time.Second && host > 0:
					note = " stall"
				case n == 0 && host == 0:
					note = " still"
				}
				fmt.Printf("%s %d frames, longest gap %d ms, delay %d ms%s\n", now.Format("15:04:05"), n, gap.Milliseconds(), delay, note)
			}
		}
	}()
	for {
		p, err := localapi.ReadPacket(conn, buf)
		if err != nil {
			break
		}
		buf = p
		switch {
		case len(p) >= 10 && p[0] == 2:
			now := time.Now()
			pace.Lock()
			pace.frames++
			pace.gap = max(pace.gap, now.Sub(pace.last))
			pace.last = now
			pace.Unlock()
		case len(p) > 0 && p[0] == 4:
			var s struct {
				Frames int `json:"frames"`
				Delay  int `json:"delay_ms"`
			}
			if json.Unmarshal(p[1:], &s) == nil {
				pace.Lock()
				pace.hostFrames, pace.delay = s.Frames, s.Delay
				pace.Unlock()
			}
		}
		switch {
		case len(p) > 0 && p[0] == 1:
			fmt.Printf("stream %s\n", p[1:])
			if lib != nil {
				var s struct{ Width, Height int }
				if json.Unmarshal(p[1:], &s) == nil && s.Width > 0 && s.Height > 0 {
					if dec != nil {
						dec.Close()
					}
					if dec, err = lib.OpenDecoder(p[1:]); err != nil {
						return err
					}
					pic = image.NewRGBA(image.Rect(0, 0, s.Width, s.Height))
					fresh = true
				}
			}
		case len(p) > 0 && p[0] == 4:
			fmt.Println("host", string(p[1:]))
		case len(p) > 0 && (p[0] == 5 || p[0] == 6):
			fmt.Println("ai", string(p[1:]))
		case len(p) > 0 && (p[0] == 7 || p[0] == 8) && !printedDisplays[p[0]]:
			printedDisplays[p[0]] = true
			fmt.Printf("%s %s\n", map[byte]string{7: "pointer", 8: "displays"}[p[0]], p[1:])
		case len(p) > 0 && p[0] == 9:
			fmt.Printf("%s actions %s\n", time.Now().Format("15:04:05"), p[1:])
		case len(p) > 1 && p[0] == 12:
			sounds++
			soundBytes += len(p) - 1
			if now := time.Now(); !soundAt.IsZero() {
				gap := now.Sub(soundAt)
				soundGap = max(soundGap, gap)
				if gap > 30*time.Millisecond {
					soundLate++
				}
				soundAt = now
			} else {
				soundAt = now
			}
		case len(p) > 0 && p[0] == 11:
			fmt.Printf("codecs %s\n", p[1:])
		case len(p) >= 10 && p[0] == 2:
			frames++
			bytes += len(p) - 10
			// Like the viewer: a new decoder starts at a key frame.
			if fresh && p[1]&1 == 0 {
				continue
			}
			fresh = false
			if dec != nil {
				switch _, _, ok, err := dec.Decode(p[10:], pic.Pix, core.Full); {
				case err != nil && decodeErr == nil:
					decodeErr = err
				case err == nil && ok:
					pictures++
				}
			}
		}
	}
	el := time.Since(start).Seconds()
	fmt.Printf("%d frames in %.1f s: %.1f fps, %.0f kbit/s\n", frames, el, float64(frames)/el, float64(bytes)*8/1000/el)
	pace.Lock()
	fmt.Printf("longest gap between frames %d ms\n", pace.worst.Milliseconds())
	pace.Unlock()
	fmt.Printf("%d sound packets (%d ms, %.0f bytes each), longest gap %d ms, %d gaps over 30 ms\n", sounds, sounds*10, float64(soundBytes)/float64(max(sounds, 1)), soundGap.Milliseconds(), soundLate)
	if dec != nil {
		dec.Close()
		fmt.Printf("%d pictures decoded\n", pictures)
		if decodeErr != nil {
			fmt.Printf("first decoding error: %v\n", decodeErr)
		}
		if pictures > 0 {
			f, err := os.Create(snapshot)
			if err != nil {
				return err
			}
			defer f.Close()
			return png.Encode(f, pic)
		}
	}
	return nil
}

// screenshot saves the first whole picture of a controller session's
// screen as a PNG file, decoded as the viewer decodes it.
// Inputs: id, the session (`connect` printed it); out, the file; display,
// the host's display to show first (-1: the one shown).
// Side effects: opens the session's media for a few seconds; writes out.
// Notes: the host sends a key frame on request, so a still screen still
// gives a picture; ten seconds without one is a failure.
func screenshot(socket string, id uint64, out string, display int) error {
	if id == 0 || out == "" {
		return errors.New("pass --id and --out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := localapi.NewClient(socket).Media(ctx, id)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The decoder is tied to this thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	lib, err := core.Load(core.LibraryPath(filepath.Dir(exe)))
	if err != nil {
		return err
	}
	if display >= 0 {
		body, _ := json.Marshal(map[string]any{"type": "display", "index": display})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
	}
	if err := localapi.WritePacket(conn, append([]byte{3}, `{"type":"key-frame"}`...)); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var (
		buf []byte
		dec *core.Decoder
		pic *image.RGBA
		// started: the decoder has had its key frame.
		started bool
	)
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()
	for {
		p, err := localapi.ReadPacket(conn, buf)
		if err != nil {
			return fmt.Errorf("no picture came: %w", err)
		}
		buf = p
		switch {
		case len(p) > 0 && p[0] == 1:
			// A stream description: a new decoder, which waits for a key frame.
			var s struct{ Width, Height int }
			if json.Unmarshal(p[1:], &s) != nil || s.Width <= 0 || s.Height <= 0 {
				continue
			}
			if dec != nil {
				dec.Close()
			}
			if dec, err = lib.OpenDecoder(p[1:]); err != nil {
				return err
			}
			pic, started = image.NewRGBA(image.Rect(0, 0, s.Width, s.Height)), false
		case len(p) >= 10 && p[0] == 2 && dec != nil && (started || p[1]&1 != 0):
			started = true
			_, _, ok, err := dec.Decode(p[10:], pic.Pix, core.Full)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			if err := png.Encode(f, pic); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(map[string]any{"file": out, "width": pic.Rect.Dx(), "height": pic.Rect.Dy()})
			}
			fmt.Printf("%s (%d×%d)\n", out, pic.Rect.Dx(), pic.Rect.Dy())
			return nil
		}
	}
}
