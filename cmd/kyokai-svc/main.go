// kyokai-svc is Kyokai's headless engine (doc/en-us/architecture.md):
//
//	kyokai-svc run --host user [--socket P] [--state-dir D] [--core LIB]
//	               [--parent-pid N | --lifeline-fd N]
//	kyokai-svc run --host helper      (the KyokaiHelper service; doc/en-us/run-modes.md)
//	kyokai-svc helper install [--from DIR] [--operator-sid SID] | uninstall [--purge] | status
//	kyokai-svc agent --down PIPE --up PIPE --helper-pid N   (started by the helper)
//	kyokai-svc ctl status | password | regenerate | shutdown | network | trusted [--socket P]
//	kyokai-svc ctl network-set --server URL [--server URL…] --name N
//	kyokai-svc ctl name-set --name N     (an empty N follows the computer's name)
//	kyokai-svc ctl permanent [--remove]
//	kyokai-svc ctl connect --device ID [--want P,P…]
//	kyokai-svc ctl close --id N | watch --id N [--seconds S] [--snapshot F.png] [--quality P] [--fps N] [--text T]
//	kyokai-svc ctl allow --id N [--grant P,P…] | deny --id N
//	kyokai-svc ctl untrust --key K | forget --device ID | unattended --key K [--days N]
//	kyokai-svc ctl background [--on]     (the helper: Background service or Follow GUI)
//	kyokai-svc ctl move --to SOCKET       (moves the whole store to another engine of this device, which stops this one)
//	kyokai-svc ctl backup --file F | restore --file F | reset-identity --yes
//	kyokai-svc ctl ai | ai-tasks | ai-clear | ai-stop
//	kyokai-svc ctl ai-set [--provider P] [--openai-model M] [--claude-model M] [--approval A]
//	kyokai-svc ctl ai-key --provider P [--remove]
//	kyokai-svc ctl ai-start --text T | ai-reply --id N --text T
//	kyokai-svc ctl ai-pause | ai-resume | ai-cancel | ai-approve | ai-decline --id N
//
// Secrets come from the environment, never the command line: network-set
// reads KYOKAI_NETWORK_SECRET (unset keeps the stored one), permanent reads
// KYOKAI_PERMANENT_PASSWORD, connect reads KYOKAI_PASSWORD, backup and restore read
// KYOKAI_BACKUP_PASSPHRASE, ai-key reads KYOKAI_AI_KEY.
//
// The GUI starts `run` as its child; `ctl` is for scripts and development.
// KYOKAI_PROFILE selects a separate development instance.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

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
		opts.CoreLib, opts.ParentPID, opts.Profile = filepath.Join(filepath.Dir(exe), core.LibraryName()), 0, ""
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
	permanentEnv  = "KYOKAI_PERMANENT_PASSWORD"
	passwordEnv   = "KYOKAI_PASSWORD"
	passphraseEnv = "KYOKAI_BACKUP_PASSPHRASE"
	aiKeyEnv      = "KYOKAI_AI_KEY"
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
	name := fs.String("name", "", "network-set: the network name; name-set: this device's friendly name")
	device := fs.String("device", "", "connect, forget: the Device ID")
	want := fs.String("want", localapi.PermViewScreen+","+localapi.PermControlInput, "connect: the permissions to ask for")
	grant := fs.String("grant", "", "allow: the permissions to grant (default: all asked for)")
	id := fs.Uint64("id", 0, "close, allow, deny: the session or request id")
	key := fs.String("key", "", "untrust: the device key")
	remove := fs.Bool("remove", false, "permanent: remove the permanent password")
	file := fs.String("file", "", "backup, restore: the backup file")
	yes := fs.Bool("yes", false, "reset-identity: confirm a new Device ID")
	to := fs.String("to", "", "move: the socket of the engine that takes over this device's store")
	days := fs.Int("days", 30, "unattended: how long the device may connect without confirmation (0 stops it)")
	on := fs.Bool("on", false, "background: serve from boot (else only while a GUI is attached)")
	seconds := fs.Int("seconds", 5, "watch: how long to count frames")
	snapshot := fs.String("snapshot", "", "watch: decode the stream and save the last picture as PNG")
	quality := fs.String("quality", "", "watch: ask for a preset first (speed, balanced, best)")
	fpsCap := fs.Int("fps", 0, "watch: ask for a frame-rate cap first")
	provider := fs.String("provider", "", "ai-set, ai-key: claude or openai")
	openaiModel := fs.String("openai-model", "", "ai-set: the OpenAI model")
	claudeModel := fs.String("claude-model", "", "ai-set: the Claude model")
	approval := fs.String("approval", "", "ai-set: flagged or every-action")
	text := fs.String("text", "", "ai-start, ai-reply, watch: what the AI should do (watch: on the host)")
	// The command comes first, its flags after it.
	if len(args) == 0 {
		usage()
	}
	fs.Parse(args[1:])
	if fs.NArg() != 0 {
		usage()
	}
	cmd := args[0]
	if cmd == "move" {
		id, err := localapi.MoveStore(context.Background(), localapi.NewClient(*socket), localapi.NewClient(*to))
		if err == nil {
			fmt.Println("moved: device", id, "is now served by", *to)
		}
		return err
	}
	if cmd == "watch" {
		return watch(*socket, *id, time.Duration(*seconds)*time.Second, *snapshot, *quality, *fpsCap, *text)
	}
	req := localapi.Request{Type: map[string]localapi.RequestType{
		"status": localapi.RequestStatus, "password": localapi.RequestPasswordReveal,
		"regenerate": localapi.RequestPasswordRegenerate, "shutdown": localapi.RequestShutdown,
		"network": localapi.RequestNetworkGet, "network-set": localapi.RequestNetworkSet, "name-set": localapi.RequestNameSet, "unattended": localapi.RequestUnattended, "background": localapi.RequestBackground,
		"permanent": localapi.RequestPermanentSet, "connect": localapi.RequestConnect,
		"close": localapi.RequestClose, "allow": localapi.RequestDecide, "deny": localapi.RequestDecide,
		"trusted": localapi.RequestTrustedList, "untrust": localapi.RequestTrustedRemove,
		"forget": localapi.RequestHostForget, "backup": localapi.RequestIdentityExport,
		"restore": localapi.RequestIdentityImport, "reset-identity": localapi.RequestIdentityReset,
		"ai": localapi.RequestAISettingsGet, "ai-set": localapi.RequestAISettingsSet, "ai-key": localapi.RequestAIKeySet,
		"ai-start": localapi.RequestAITaskStart, "ai-reply": localapi.RequestAITaskReply, "ai-tasks": localapi.RequestAITasks,
		"ai-pause": localapi.RequestAITaskPause, "ai-resume": localapi.RequestAITaskResume, "ai-cancel": localapi.RequestAITaskCancel,
		"ai-approve": localapi.RequestAITaskDecide, "ai-decline": localapi.RequestAITaskDecide,
		"ai-clear": localapi.RequestAIHistoryClear, "ai-stop": localapi.RequestAIStop,
	}[cmd], ID: *id, Key: *key, DeviceID: *device}
	if req.Type == "" {
		usage()
	}
	client := localapi.NewClient(*socket)
	// The secrets come from the environment, never from the command line,
	// where other processes could read them.
	switch cmd {
	case "name-set":
		req.Name = name
	case "unattended":
		req.Days = *days
	case "background":
		req.Background = on
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
		req.AISettings = &localapi.AISettings{Provider: *provider, OpenAIModel: *openaiModel, ClaudeModel: *claudeModel, Approval: *approval}
	case "ai-key":
		req.AIKey = &localapi.AIKey{Provider: *provider}
		if !*remove {
			k, ok := os.LookupEnv(aiKeyEnv)
			if !ok {
				return fmt.Errorf("set %s (or pass --remove)", aiKeyEnv)
			}
			req.AIKey.Key = &k
		}
	case "ai-start":
		req.AITask = &localapi.AITaskRequest{Key: strconv.FormatInt(time.Now().UnixNano(), 36), Instruction: *text}
	case "ai-reply":
		req.AITask = &localapi.AITaskRequest{Text: *text}
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
	case resp.Document != "":
		// Owner-only: the file is encrypted, but it is still the identity.
		if err := os.WriteFile(*file, []byte(resp.Document), 0o600); err != nil {
			return err
		}
		fmt.Println("backup written to", *file)
	case resp.ID != 0 && cmd == "ai-start":
		fmt.Println("task", resp.ID)
	case resp.ID != 0:
		fmt.Println("session", resp.ID)
	case resp.AISettings != nil:
		s := resp.AISettings
		fmt.Printf("provider %s\nopenai model %s (key: %v)\nclaude model %s (key: %v)\napproval %s\n",
			s.Provider, s.OpenAIModel, s.Keys != nil && s.Keys.OpenAI, s.ClaudeModel, s.Keys != nil && s.Keys.Claude, s.Approval)
	case req.Type == localapi.RequestAITasks:
		for _, t := range resp.AITasks {
			printTask(t)
		}
	case req.Type == localapi.RequestTrustedList:
		for _, t := range resp.Trusted {
			until := "asks"
			if t.UnattendedUntil != nil {
				until = "unattended until " + time.Unix(*t.UnattendedUntil, 0).Format(time.DateTime)
			}
			fmt.Printf("%s %q  %s  %s  %s\n", t.DeviceID, t.About.Name, strings.Join(t.Granted, ","), until, t.Key)
		}
	default:
		fmt.Println("ok")
	}
	return nil
}

// printTask writes one AI task: its state, its actions and the model's text.
func printTask(t localapi.AITask) {
	fmt.Printf("task %d  %s  %s/%s  %q\n", t.ID, t.State, t.Provider, t.Model, t.Instruction)
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
	fmt.Printf("version %s (ABI %d), run mode %s\ndevice ID %s\nfingerprint %s\nstate %s (secrets: %s)\npermanent password %v\n",
		s.Version, s.ABI, s.RunMode, s.DeviceID, s.Fingerprint, s.StateDir, s.Persistence, s.PermanentPassword)
	fmt.Printf("name %q (chosen %q), %s %s, %s\n", s.About.Name, s.Name, s.About.Maker, s.About.Model, s.About.OS)
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
		fmt.Printf("request %d  from %s %q  wants %s  known %v  new key %v\n",
			r.ID, r.DeviceID, r.About.Name, strings.Join(r.Want, ","), r.Known, r.NewKey)
	}
	for _, r := range s.Recent {
		fmt.Printf("recent %s %q  %s %s\n", r.DeviceID, r.About.Name, r.About.Maker, r.About.Model)
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
// the host's AI to do it (the viewer's AI panel).
func watch(socket string, id uint64, d time.Duration, snapshot, quality string, fpsCap int, aiText string) error {
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
		if lib, err = core.Load(filepath.Join(filepath.Dir(exe), core.LibraryName())); err != nil {
			return err
		}
	}
	// Like the viewer: ask for a key frame (and the stream description) first.
	if err := localapi.WritePacket(conn, append([]byte{3}, `{"type":"key-frame"}`...)); err != nil {
		return err
	}
	if quality != "" || fpsCap > 0 {
		body, _ := json.Marshal(map[string]any{"type": "quality", "preset": quality, "fps": fpsCap})
		if err := localapi.WritePacket(conn, append([]byte{3}, body...)); err != nil {
			return err
		}
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
	frames, bytes, pictures := 0, 0, 0
	start := time.Now()
	for {
		p, err := localapi.ReadPacket(conn, buf)
		if err != nil {
			break
		}
		buf = p
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
				}
			}
		case len(p) > 0 && p[0] == 4:
			fmt.Println("host", string(p[1:]))
		case len(p) > 0 && (p[0] == 5 || p[0] == 6):
			fmt.Println("ai", string(p[1:]))
		case len(p) >= 10 && p[0] == 2:
			frames++
			bytes += len(p) - 10
			if dec != nil {
				if _, _, ok, err := dec.Decode(p[10:], pic.Pix); err == nil && ok {
					pictures++
				}
			}
		}
	}
	el := time.Since(start).Seconds()
	fmt.Printf("%d frames in %.1f s: %.1f fps, %.0f kbit/s\n", frames, el, float64(frames)/el, float64(bytes)*8/1000/el)
	if dec != nil {
		dec.Close()
		fmt.Printf("%d pictures decoded\n", pictures)
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
