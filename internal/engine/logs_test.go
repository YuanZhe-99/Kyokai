package engine

import (
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
	dir := t.TempDir()
	// The log is open again at the end: close it, or Windows cannot
	// delete the folder.
	t.Cleanup(func() {
		logFile.Lock()
		defer logFile.Unlock()
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		if logFile.f != nil {
			_ = logFile.f.Close()
			logFile.f = nil
		}
	})
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
