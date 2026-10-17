// Package core loads the Rust core library (kyokai_core) with purego and
// calls its C ABI (doc/en-us/architecture.md, "FFI"): JSON requests in,
// JSON responses out, integers and pointers only.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"
)

// ABIVersion is the C ABI this package speaks; the library must report the
// same one.
const ABIVersion = 1

// LibraryName is the file name of the core library on this platform.
func LibraryName() string {
	switch runtime.GOOS {
	case "windows":
		return "kyokai_core.dll"
	case "darwin":
		return "libkyokai_core.dylib"
	}
	return "libkyokai_core.so"
}

// Lib is a loaded core library.
type Lib struct {
	abiVersion, start, call, stop, free uintptr
	mu                                  sync.Mutex // the core is one process-wide instance
	// out receives a response's buffer. It lives in the Lib (on the heap,
	// which never moves) rather than on a goroutine stack, which may move
	// between converting its address to a uintptr and the C call. Guarded
	// by mu.
	out struct {
		ptr *byte
		len uintptr
	}
}

// Config is what the core starts with.
type Config struct {
	// Version is the product version (internal/version).
	Version string `json:"version"`
	// StateDir holds this installation's secrets and identity.
	StateDir string `json:"state_dir"`
	// Profile keys the OS credential store entry of a development instance.
	Profile string `json:"profile,omitempty"`
}

// Status is the core's state as hosts show it; it never holds a secret.
type Status struct {
	Version     string `json:"version"`
	ABI         int    `json:"abi"`
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint"`
	StateDir    string `json:"state_dir"`
	// Persistence is "os-store", "memory-only" or "unreadable".
	Persistence    string `json:"persistence"`
	PasswordSerial uint64 `json:"password_serial"`
	// Network is the EasyTier node's status, decoded by the host.
	Network json.RawMessage `json:"network"`
}

// Load opens the library at path (absolute) and checks its ABI version.
// Side effects: maps the library into the process for its lifetime.
func Load(path string) (*Lib, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("core library path must be absolute: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("core library: %w", err)
	}
	h, err := open(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", filepath.Base(path), err)
	}
	l := &Lib{}
	for name, dst := range map[string]*uintptr{
		"kyokai_abi_version": &l.abiVersion, "kyokai_start": &l.start, "kyokai_call": &l.call,
		"kyokai_stop": &l.stop, "kyokai_free": &l.free,
	} {
		if *dst, err = symbol(h, name); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if v := uint32(syscallN(l.abiVersion)); v != ABIVersion {
		return nil, fmt.Errorf("core library ABI %d, expected %d", v, ABIVersion)
	}
	return l, nil
}

// Start starts the core and returns its first status.
// Side effects: creates the state directory and the device identity.
func (l *Lib) Start(cfg Config) (Status, error) {
	var st Status
	return st, l.exchange(l.start, cfg, &st)
}

// Call runs a core method and decodes its result into out (nil discards it).
func (l *Lib) Call(method string, params, out any) error {
	return l.exchange(l.call, struct {
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{method, params}, out)
}

// Stop stops the core, dropping its in-memory secrets.
func (l *Lib) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	syscallN(l.stop)
}

// exchange marshals req, calls fn (kyokai_start or kyokai_call), frees the
// response buffer and decodes its result into out.
func (l *Lib) exchange(fn uintptr, req, out any) error {
	in, err := json.Marshal(req)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.out.ptr, l.out.len = nil, 0
	rc := int32(syscallN(fn, uintptr(unsafe.Pointer(unsafe.SliceData(in))), uintptr(len(in)),
		uintptr(unsafe.Pointer(&l.out.ptr)), uintptr(unsafe.Pointer(&l.out.len))))
	runtime.KeepAlive(in)
	outPtr, outLen := l.out.ptr, l.out.len
	l.mu.Unlock()
	if rc != 0 || outPtr == nil {
		return fmt.Errorf("core call failed (code %d, response %t)", rc, outPtr != nil)
	}
	data := string(unsafe.Slice(outPtr, outLen)) // copy before freeing
	syscallN(l.free, uintptr(unsafe.Pointer(outPtr)), outLen)
	var resp struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(data), &resp); err != nil {
		return fmt.Errorf("core response: %w", err)
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}
