package core

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestRealLibrary is the ABI smoke test: it loads a built core library
// (KYOKAI_CORE_LIB, an absolute path) and exercises start, calls and stop.
// Without the variable it is skipped; CI sets it after building the core.
func TestRealLibrary(t *testing.T) {
	path := os.Getenv("KYOKAI_CORE_LIB")
	if path == "" {
		t.Skip("KYOKAI_CORE_LIB not set")
	}
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A fresh profile per run: t.TempDir always ends in "001", and a
	// Keychain item made by another test binary raises an access prompt that
	// nobody answers on CI.
	profile := fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano())
	st, err := l.Start(Config{Version: "0.0.0-test", StateDir: dir, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	if len(st.DeviceID) != 9 || st.Version != "0.0.0-test" || st.ABI != ABIVersion {
		t.Fatalf("status %+v", st)
	}
	var pw struct{ Temporary string }
	if err := l.Call("password.reveal", nil, &pw); err != nil || len(pw.Temporary) != 8 {
		t.Fatalf("reveal: %v %q", err, pw.Temporary)
	}
	if err := l.Call("no.such.method", nil, nil); err == nil {
		t.Fatal("an unknown method must fail")
	}
}

func TestLoadRefusesRelativePaths(t *testing.T) {
	if _, err := Load(LibraryName()); err == nil {
		t.Fatal("a relative path must be refused")
	}
}
