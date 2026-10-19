package core

import (
	"fmt"
	"os"
	"runtime"
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
	// The keyboard grab: ending one that never began and a bad request
	// install nothing; no key is waiting.
	if err := l.KeyboardGrab(1, "off"); err != nil {
		t.Fatal(err)
	}
	if l.KeyboardGrab(1, "legacy") == nil || l.KeyboardGrab(0, "map") == nil {
		t.Fatal("a bad grab must fail")
	}
	// Read answers on Windows and macOS (a Mac viewer grabs keys too) and
	// fails elsewhere.
	if _, ok, err := l.Keyboard().Read(time.Millisecond); ok || (err != nil) != (runtime.GOOS != "windows" && runtime.GOOS != "darwin") {
		t.Fatalf("read: %v %v", ok, err)
	}
	// The viewer's decoder: raw test-pattern frames pass through.
	dec, err := l.OpenDecoder([]byte(`{"codec":"raw-rgba","width":2,"height":1}`))
	if err != nil {
		t.Fatal(err)
	}
	pic := make([]byte, 8)
	if w, h, ok, err := dec.Decode([]byte{1, 2, 3, 4, 5, 6, 7, 8}, pic); !ok || err != nil || w != 2 || h != 1 || pic[7] != 8 {
		t.Fatalf("decode: %d×%d %v %v %v", w, h, ok, err, pic)
	}
	dec.Close()
	if _, err := l.OpenDecoder([]byte(`{"codec":"none","width":2,"height":1}`)); err == nil {
		t.Fatal("an unknown codec must be refused")
	}
	// Regression: the response slots once lived on the goroutine stack, and
	// a stack that grew on the way into the C call left the response behind
	// at the old address. Calling at many depths makes the stack grow at
	// the call in some of them.
	for depth := range 300 {
		if err := atDepth(depth, func() error { return l.Call("status", nil, nil) }); err != nil {
			t.Fatalf("status at depth %d: %v", depth, err)
		}
	}
}

// atDepth runs f below depth stack frames of 256 bytes each.
func atDepth(depth int, f func() error) error {
	var pad [256]byte
	if depth == 0 {
		return f()
	}
	err := atDepth(depth-1, f)
	runtime.KeepAlive(&pad)
	return err
}

func TestLoadRefusesRelativePaths(t *testing.T) {
	if _, err := Load(LibraryName()); err == nil {
		t.Fatal("a relative path must be refused")
	}
}
