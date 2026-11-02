package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

var update = flag.Bool("update", false, "rewrite the golden files from this run")

// fakeEngine answers each request type with a canned response and records
// what it was asked.
type fakeEngine struct {
	mu      sync.Mutex
	got     []localapi.Request
	answers map[localapi.RequestType]localapi.Response
}

func (f *fakeEngine) Handle(_ context.Context, req localapi.Request) localapi.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	if a, ok := f.answers[req.Type]; ok {
		return a
	}
	return localapi.Response{OK: true}
}

func (f *fakeEngine) Stream(context.Context, localapi.Request, func(localapi.Response) error) {}

func (f *fakeEngine) Media(context.Context, localapi.Request, func() io.ReadWriter) localapi.Response {
	return localapi.Response{}
}

// last is the newest request the engine got.
func (f *fakeEngine) last() localapi.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) == 0 {
		return localapi.Request{}
	}
	return f.got[len(f.got)-1]
}

// serveFake runs the fake engine on a fresh socket for the test.
func serveFake(t *testing.T, f *fakeEngine) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kyctl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = localapi.ServeWith(ctx, ln, f, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})
	}()
	t.Cleanup(func() { cancel(); <-done; os.RemoveAll(dir) })
	return path
}

// stdout runs fn with the process's standard output captured.
func stdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		out <- b.String()
	}()
	fn()
	os.Stdout = old
	w.Close()
	return <-out
}

// TestJSONGolden: each command's arguments become the local API request it
// names, and its --json output is the engine's answer, stable as the
// golden files hold it (go test -run JSONGolden -update rewrites them).
func TestJSONGolden(t *testing.T) {
	folders := strings.Join([]string{filepath.FromSlash("/work/report"), filepath.FromSlash("/shared")}, string(os.PathListSeparator))
	settings := &localapi.AISettings{
		Approval: "auto",
		Model:    &localapi.AIModelRef{Source: "claude", ID: "claude-opus-5-5"},
		Scope:    []string{"code.exe"},
		Folders:  []string{"/work/report"},
		Panes:    []localapi.AIPane{{ID: "rdp-1", Name: "Remote Desktop · NUC", Kind: "rdp"}},
		Keys:     map[string]bool{"claude": true},
	}
	f := &fakeEngine{answers: map[localapi.RequestType]localapi.Response{
		localapi.RequestAISettingsGet: {OK: true, AISettings: settings},
		localapi.RequestAIFoldersSet:  {OK: true, AISettings: settings},
		localapi.RequestAIScopeSet:    {OK: true, AISettings: settings},
		localapi.RequestAITaskStart:   {OK: true, ID: 7},
		localapi.RequestAIApps:        {OK: true, AIApps: []string{"code.exe", "explorer.exe"}},
		localapi.RequestAITasks: {OK: true, AITasks: []localapi.AITask{{
			ID: 7, Instruction: "Save the report", Source: "claude", Model: "claude-opus-5-5", State: "paused",
			Paused: "folders", Outside: []string{"/home/me/secret.txt"}, Pane: &localapi.AIPane{ID: "rdp-1", Name: "Remote Desktop · NUC", Kind: "rdp"},
			Steps: []localapi.AIStep{{At: 1, Action: json.RawMessage(`{"kind":"screenshot"}`), Outcome: "ok"}}, Created: 1, Updated: 2,
		}}},
	}}
	sock := serveFake(t, f)
	for _, c := range []struct {
		name string
		args []string
		// check looks at the request the command made.
		check func(localapi.Request) bool
	}{
		{"ai", []string{"ai"}, func(r localapi.Request) bool { return r.Type == localapi.RequestAISettingsGet }},
		{"ai-folders", []string{"ai-folders", "--folders", folders}, func(r localapi.Request) bool {
			return r.Type == localapi.RequestAIFoldersSet && r.Folders != nil && len(*r.Folders) == 2
		}},
		{"ai-folders-none", []string{"ai-folders"}, func(r localapi.Request) bool {
			return r.Type == localapi.RequestAIFoldersSet && r.Folders == nil
		}},
		{"ai-scope", []string{"ai-scope", "--apps", "code.exe,notepad.exe"}, func(r localapi.Request) bool {
			return r.Type == localapi.RequestAIScopeSet && r.Apps != nil && len(*r.Apps) == 2
		}},
		{"ai-apps", []string{"ai-apps"}, func(r localapi.Request) bool { return r.Type == localapi.RequestAIApps }},
		{"ai-start", []string{"ai-start", "--text", "Save the report", "--pane", "rdp-1", "--thinking", "high"}, func(r localapi.Request) bool {
			return r.Type == localapi.RequestAITaskStart && r.AITask.On == "rdp-1" && r.AITask.Thinking == "high" && r.AITask.Instruction == "Save the report"
		}},
		{"ai-tasks", []string{"ai-tasks"}, func(r localapi.Request) bool { return r.Type == localapi.RequestAITasks }},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := append(append([]string{}, c.args...), "--json", "--socket", sock)
			var code int
			got := stdout(t, func() { code = Main("kyokai", args) })
			if code != ExitOK {
				t.Fatalf("exit %d", code)
			}
			if !c.check(f.last()) {
				t.Errorf("request: %+v", f.last())
			}
			path := filepath.Join("testdata", c.name+".json")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
				t.Errorf("--json changed:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
