package engine

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// TestNoLogs: with No logs on nothing is written and the logs written are
// gone; a process started afterwards writes nothing either; turned off,
// the log is written again.
func TestNoLogs(t *testing.T) {
	t.Cleanup(keepStderr())
	dir := t.TempDir()
	t.Cleanup(closeLog)
	path := filepath.Join(dir, "logs", "engine.log")
	helperLog := filepath.Join(dir, "helper.log")
	if err := os.WriteFile(helperLog, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	logf := Logger(path)
	logf("before")
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "before") {
		t.Fatalf("log = %q, want the line", b)
	}
	if err := SetLogsOff(dir, true); err != nil {
		t.Fatal(err)
	}
	logf("while off")
	for _, p := range []string{filepath.Dir(path), helperLog} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there (%v)", p, err)
		}
	}
	if !LogsOff(dir) {
		t.Fatal("the choice is not kept for the next start")
	}
	if CrashLog(path) != nil {
		t.Error("a process started with no logs opened its log")
	}
	if err := SetLogsOff(dir, false); err != nil {
		t.Fatal(err)
	}
	logf("after")
	b, _ := os.ReadFile(path)
	if got := string(b); !strings.Contains(got, "after") || strings.Contains(got, "while off") {
		t.Errorf("log = %q", got)
	}
}

// TestStderrInLog: what the core writes to stderr (its panics and log
// lines) lands in the log while logs are on, and nowhere while they are off.
func TestStderrInLog(t *testing.T) {
	t.Cleanup(keepStderr())
	dir := t.TempDir()
	t.Cleanup(closeLog)
	path := filepath.Join(dir, "logs", "engine.log")
	if CrashLog(path) == nil {
		t.Fatal("no log opened")
	}
	writeStderr([]byte("core: before\n"))
	if err := SetLogsOff(dir, true); err != nil {
		t.Fatal(err)
	}
	writeStderr([]byte("core: while off\n"))
	if err := SetLogsOff(dir, false); err != nil {
		t.Fatal(err)
	}
	writeStderr([]byte("core: after\n"))
	b, _ := os.ReadFile(path)
	if got := string(b); !strings.Contains(got, "core: after") || strings.Contains(got, "while off") {
		t.Errorf("log = %q", got)
	}
}

// closeLog closes the log a test left open (Windows cannot delete a folder
// with an open file) and sends the crash trace and the standard logger back
// to stderr.
func closeLog() {
	logFile.Lock()
	defer logFile.Unlock()
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	log.SetOutput(os.Stderr)
	if logFile.f != nil {
		_ = logFile.f.Close()
		logFile.f = nil
	}
}

// TestReadLogs: the logs come back under their names, each cut to its
// newest part; with No logs on, nothing.
func TestReadLogs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", logTail) + "newest"
	for name, text := range map[string]string{"logs/engine.log": "engine line", "logs/agent.log": long, "helper.log": "helper line"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := ReadLogs(dir)
	for _, want := range []string{"==== engine.log ====\nengine line", "==== helper.log ====\nhelper line", "newest"} {
		if !strings.Contains(got, want) {
			t.Errorf("logs miss %q", want)
		}
	}
	if len(got) > 2*logTail {
		t.Errorf("logs are %d bytes, want each cut to %d", len(got), logTail)
	}
	if err := os.WriteFile(filepath.Join(dir, noLogs), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadLogs(dir); got != "" {
		t.Errorf("logs read with No logs on: %q", got)
	}
}
