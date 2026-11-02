package ctl

import (
	"path/filepath"
	"testing"
)

// TestCommandsAreServed checks that help lists exactly the commands the
// command line serves.
func TestCommandsAreServed(t *testing.T) {
	own := map[string]bool{"help": true, "version": true, "move": true, "watch": true, "screenshot": true}
	listed := map[string]bool{}
	for _, c := range commands {
		if c.name == "" {
			continue
		}
		listed[c.name] = true
		_, req := requests[c.name]
		_, svc := ctlServices[c.name]
		if !req && !svc && !own[c.name] {
			t.Errorf("help lists %q, which nothing serves", c.name)
		}
	}
	for name := range requests {
		if !listed[name] {
			t.Errorf("%q is served but help does not list it", name)
		}
	}
	for name := range ctlServices {
		if !listed[name] {
			t.Errorf("%q is served but help does not list it", name)
		}
	}
}

// TestFlagFor checks which commands' help shows a flag, and with what text.
func TestFlagFor(t *testing.T) {
	for _, c := range []struct {
		usage, cmd, want string
		ok               bool
	}{
		{"the engine's local API socket", "status", "the engine's local API socket", true},
		{"ai-key, ai-models: the AI source's id", "ai-models", "the AI source's id", true},
		{"ai-key, ai-models: the AI source's id", "ai-set", "", false},
		{"allow: the permissions; password-grants: what a password grants", "password-grants", "what a password grants", true},
		{"background: serve from boot; capture-show: show windows; no-logs: keep none", "capture-show", "show windows", true},
		// A colon inside the text does not name commands.
		{"ssh, rdp or vnc (credentials-forget: also key)", "credentials-get", "ssh, rdp or vnc (credentials-forget: also key)", true},
		{"ai-start, ai-reply, watch: what the AI should do (watch: on the host)", "watch", "what the AI should do (watch: on the host)", true},
	} {
		got, ok := flagFor(c.usage, c.cmd)
		if got != c.want || ok != c.ok {
			t.Errorf("flagFor(%q, %q) = %q, %v; want %q, %v", c.usage, c.cmd, got, ok, c.want, c.ok)
		}
	}
}

// TestExitCodes checks the codes scripts react to.
func TestExitCodes(t *testing.T) {
	none := filepath.Join(t.TempDir(), "none.sock")
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"help"}, ExitOK},
		{[]string{"help", "ai-scope"}, ExitOK},
		{[]string{"ai-scope", "--help"}, ExitOK},
		{[]string{"tunnel", "--help"}, ExitOK},
		{nil, ExitUsage},
		{[]string{"bogus"}, ExitUsage},
		{[]string{"status", "--bogus"}, ExitUsage},
		{[]string{"status", "extra"}, ExitUsage},
		{[]string{"status", "--socket", none}, ExitNotRunning},
		{[]string{"screenshot", "--socket", none}, ExitFailed},
	} {
		if got := Main("kyokai", c.args); got != c.want {
			t.Errorf("kyokai %v: exit %d, want %d", c.args, got, c.want)
		}
	}
}
