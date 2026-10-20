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
