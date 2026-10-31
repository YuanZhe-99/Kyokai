package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
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
	if runtime.GOOS == "darwin" {
		t.Cleanup(func() { forgetMasterKey(t, profile) })
	}
	st, err := l.Start(Config{Version: "0.0.0-test", StateDir: dir, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	if len(st.DeviceID) != 9 || st.Version != "0.0.0-test" || st.ABI != ABIVersion {
		t.Fatalf("status %+v", st)
	}
	// No viewer grabs the keyboard here: a window's grab stands at none. A
	// grab for a window that is never in front takes no key: it is held,
	// behind; asked again it is renewed; ended, none again. Without the
	// Accessibility permission (a Mac runner) the grab is refused.
	state := func(want ...string) {
		t.Helper()
		ks, err := l.KeyboardState(0x1234)
		if err != nil || !slices.Contains(want, ks.State) {
			t.Fatalf("keyboard state: %v %+v, want one of %v", err, ks, want)
		}
	}
	// A system without a grab (Linux) says so for its state and a grab
	// alike, and its viewers use Legacy mode.
	if _, err := l.KeyboardState(0x1234); errors.Is(err, ErrNoKeyboard) {
		if err := l.KeyboardGrab(0x1234, "map"); !errors.Is(err, ErrNoKeyboard) {
			t.Fatalf("grab without a grab here: %v", err)
		}
	} else {
		state("none")
	}
	switch err := l.KeyboardGrab(0x1234, "map"); {
	case err == nil:
		_, _, _ = l.Keyboard().Read(10 * time.Millisecond) // the GUI's reader asks
		state("behind", "secure-input")
		if err := l.KeyboardGrab(0x1234, "map"); err != nil {
			t.Fatalf("renew the grab: %v", err)
		}
		state("behind", "secure-input")
		if err := l.KeyboardGrab(0x1234, "off"); err != nil {
			t.Fatalf("end the grab: %v", err)
		}
		state("none")
	case !errors.Is(err, ErrNoKeyboard):
		t.Fatalf("grab: %v", err)
	}
	var pw struct{ Temporary string }
	if err := l.Call("password.reveal", nil, &pw); err != nil || len(pw.Temporary) != 8 {
		t.Fatalf("reveal: %v %q", err, pw.Temporary)
	}
	if err := l.Call("no.such.method", nil, nil); err == nil {
		t.Fatal("an unknown method must fail")
	}
	// The networks: one added is joined (its node starts, though its server
	// answers nowhere), is listed without its secret and in the status, and
	// is left, then removed.
	type networks struct {
		Profiles []struct {
			ID        string `json:"id"`
			Joined    bool   `json:"joined"`
			HasSecret bool   `json:"has_secret"`
			Secret    string `json:"secret"`
		} `json:"profiles"`
	}
	var nets networks
	add := map[string]any{"servers": []string{"tcp://127.0.0.1:9"}, "name": "smoke", "secret": "a-secret-of-16-chars"}
	if err := l.Call("network.profile.set", add, &nets); err != nil || len(nets.Profiles) != 1 ||
		!nets.Profiles[0].Joined || !nets.Profiles[0].HasSecret || nets.Profiles[0].Secret != "" {
		t.Fatalf("add a network: %v %+v", err, nets)
	}
	id := nets.Profiles[0].ID
	var status struct {
		Network struct {
			Networks []struct {
				ID     string `json:"id"`
				Joined bool   `json:"joined"`
			} `json:"networks"`
		} `json:"network"`
	}
	if err := l.Call("status", nil, &status); err != nil || len(status.Network.Networks) != 1 ||
		status.Network.Networks[0].ID != id || !status.Network.Networks[0].Joined {
		t.Fatalf("status: %v %+v", err, status.Network)
	}
	// Showing the name in the networks reaches the status.
	var shown Status
	if err := l.Call("device.set_name", map[string]any{"publish": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Call("status", nil, &shown); err != nil || !shown.PublishName {
		t.Fatalf("the name shown in the networks: %v %v", err, shown.PublishName)
	}
	if err := l.Call("network.profile.set", map[string]any{"id": id, "joined": false}, &nets); err != nil || nets.Profiles[0].Joined {
		t.Fatalf("leave: %v %+v", err, nets)
	}
	if err := l.Call("network.profile.remove", map[string]any{"id": id}, &nets); err != nil || len(nets.Profiles) != 0 {
		t.Fatalf("remove: %v %+v", err, nets)
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

// forgetMasterKey deletes the Keychain item the core made for profile, which
// outlives the test's state directory, and fails the test if it remains
// (regression: every run left a `master-key.test-…` item behind).
func forgetMasterKey(t *testing.T, profile string) {
	args := []string{"-s", "com.yuanzhe.kyokai", "-a", "master-key." + profile}
	_ = exec.Command("security", append([]string{"delete-generic-password"}, args...)...).Run()
	if exec.Command("security", append([]string{"find-generic-password"}, args...)...).Run() == nil {
		t.Errorf("the Keychain item for profile %s remains", profile)
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
