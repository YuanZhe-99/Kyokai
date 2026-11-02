package ctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommandsAreServed checks that help lists exactly the commands the
// command line serves.
func TestCommandsAreServed(t *testing.T) {
	own := map[string]bool{"help": true, "version": true, "move": true, "watch": true, "screenshot": true, "devices": true, "ai-follow": true}
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

// TestLoopbackOnly: a tunnel listens on this computer's loopback only.
func TestLoopbackOnly(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"2222", "127.0.0.1:2222", true},
		{"127.0.0.1:3390", "127.0.0.1:3390", true},
		{"[::1]:5901", "[::1]:5901", true},
		{"localhost:22", "localhost:22", true},
		{"0.0.0.0:2222", "", false},
		{"192.168.1.5:22", "", false},
		{"example.com:22", "", false},
		{"ssh", "", false},
		{"70000", "", false},
	} {
		got, err := loopback(c.in)
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("loopback(%q) = %q, %v; want %q, ok %v", c.in, got, err, c.want, c.ok)
		}
	}
}

// TestSecretFromStdin: --secret-stdin reads the first line once, without
// its line ending; otherwise the environment gives the secret.
func TestSecretFromStdin(t *testing.T) {
	t.Setenv("KYOKAI_TEST_SECRET", "from-env")
	defer func() { secretStdin, stdin, stdinLine = false, os.Stdin, nil }()
	secretStdin, stdinLine = false, nil
	if s, ok := secret("KYOKAI_TEST_SECRET"); s != "from-env" || !ok {
		t.Errorf("env: %q %v", s, ok)
	}
	secretStdin, stdin = true, strings.NewReader("pa ss\r\nsecond\n")
	for range 2 {
		if s, ok := secret("KYOKAI_TEST_SECRET"); s != "pa ss" || !ok {
			t.Errorf("stdin: %q %v", s, ok)
		}
	}
	secretStdin, stdin, stdinLine = true, strings.NewReader(""), nil
	if _, ok := secret("KYOKAI_TEST_SECRET"); ok {
		t.Error("an empty standard input gives a secret")
	}
}
