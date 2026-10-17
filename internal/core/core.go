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
	"time"
	"unsafe"
)

// ABIVersion is the C ABI this package speaks; the library must report the
// same one.
const ABIVersion = 3

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
	mediaRead, mediaWrite               uintptr
	decOpen, decDecode, decClose        uintptr
	kbGrab, kbRead                      uintptr
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
	// Agent, set when the engine is the privileged helper, is the kyokai-svc
	// the core starts as the session agent in the console session.
	Agent string `json:"agent,omitempty"`
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
	// PermanentPassword says whether a permanent password is set.
	PermanentPassword bool `json:"permanent_password"`
	// PasswordGrants are the permissions a correct password grants without
	// asking.
	PasswordGrants []string `json:"password_grants"`
	// Requests and Sessions are decoded by the host (localapi types).
	Requests json.RawMessage `json:"requests"`
	Sessions json.RawMessage `json:"sessions"`
	Recent   json.RawMessage `json:"recent"`
	// AI is the task in progress or null.
	AI json.RawMessage `json:"ai"`
	// Name is the chosen friendly name; About is decoded by the host.
	Name  string          `json:"name"`
	About json.RawMessage `json:"about"`
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
		"kyokai_media_read": &l.mediaRead, "kyokai_media_write": &l.mediaWrite,
		"kyokai_decoder_open": &l.decOpen, "kyokai_decoder_decode": &l.decDecode, "kyokai_decoder_close": &l.decClose,
		"kyokai_keyboard_grab": &l.kbGrab, "kyokai_keyboard_read": &l.kbRead,
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

// Media errors from MediaRead and MediaWrite.
var (
	ErrMediaTimeout = errors.New("no media within the timeout")
	ErrMediaEnded   = errors.New("the session's media ended")
	ErrMediaTooBig  = errors.New("media packet larger than the buffer")
)

// MediaRead waits up to timeout for the next media packet of a controller
// session and copies it into buf.
// Returns: the packet's length, or ErrMediaTimeout, ErrMediaEnded or
// ErrMediaTooBig (that packet is dropped).
// Notes: blocks its goroutine's OS thread; it does not take the Lib's lock,
// so status calls go on meanwhile. buf must be heap memory (any slice from
// make is), which never moves during the call.
func (l *Lib) MediaRead(session uint64, buf []byte, timeout time.Duration) (int, error) {
	if len(buf) == 0 {
		return 0, ErrMediaTooBig
	}
	n := int64(syscallN(l.mediaRead, uintptr(session), uintptr(unsafe.Pointer(unsafe.SliceData(buf))),
		uintptr(len(buf)), uintptr(timeout.Milliseconds())))
	runtime.KeepAlive(buf)
	switch {
	case n >= 0:
		return int(n), nil
	case n == -1:
		return 0, ErrMediaTimeout
	case n == -3:
		return 0, ErrMediaTooBig
	}
	return 0, ErrMediaEnded
}

// MediaWrite passes one packet from the GUI (a control message) to a
// controller session.
func (l *Lib) MediaWrite(session uint64, packet []byte) error {
	if len(packet) == 0 {
		return errors.New("empty media packet")
	}
	rc := int32(syscallN(l.mediaWrite, uintptr(session), uintptr(unsafe.Pointer(unsafe.SliceData(packet))), uintptr(len(packet))))
	runtime.KeepAlive(packet)
	switch rc {
	case 0:
		return nil
	case -1:
		return errors.New("malformed media packet")
	}
	return ErrMediaEnded
}

// Decoder is a viewer decoder in the core. It is tied to the thread that
// opened it: use it from one goroutine with runtime.LockOSThread.
type Decoder struct {
	l  *Lib
	id uint64
	// size receives the picture's width and height. It lives in the Decoder
	// (a heap object, which never moves) rather than on a goroutine stack.
	size [2]uint32
}

// OpenDecoder opens a viewer decoder for a stream (the media VideoStart
// JSON). It needs no started core: the GUI loads the library only for this.
func (l *Lib) OpenDecoder(start []byte) (*Decoder, error) {
	if len(start) == 0 {
		return nil, errors.New("empty stream description")
	}
	id := uint64(syscallN(l.decOpen, uintptr(unsafe.Pointer(unsafe.SliceData(start))), uintptr(len(start))))
	runtime.KeepAlive(start)
	if id == 0 {
		return nil, errors.New("no decoder for this stream")
	}
	return &Decoder{l: l, id: id}, nil
}

// Decode decodes one frame into out (RGBA, at least width×height×4 bytes).
// Returns: the picture's size and true when one is ready; false while the
// decoder needs more input.
func (d *Decoder) Decode(frame, out []byte) (int, int, bool, error) {
	if len(frame) == 0 || len(out) == 0 {
		return 0, 0, false, errors.New("empty frame or picture buffer")
	}
	rc := int32(syscallN(d.l.decDecode, uintptr(d.id),
		uintptr(unsafe.Pointer(unsafe.SliceData(frame))), uintptr(len(frame)),
		uintptr(unsafe.Pointer(unsafe.SliceData(out))), uintptr(len(out)),
		uintptr(unsafe.Pointer(&d.size[0])), uintptr(unsafe.Pointer(&d.size[1]))))
	runtime.KeepAlive(frame)
	runtime.KeepAlive(out)
	runtime.KeepAlive(d)
	switch rc {
	case 1:
		return int(d.size[0]), int(d.size[1]), true, nil
	case 0:
		return 0, 0, false, nil
	case -3:
		return 0, 0, false, errors.New("picture larger than the buffer")
	}
	return 0, 0, false, errors.New("the frame could not be decoded")
}

// Close releases the decoder.
func (d *Decoder) Close() {
	syscallN(d.l.decClose, uintptr(d.id))
}

// ErrNoKeyboard reports that the core cannot grab the keyboard here (no
// hook, or not Windows yet).
var ErrNoKeyboard = errors.New("the keyboard cannot be grabbed here")

// KeyEvent is a key the core's keyboard grab took for a viewer
// (keyboard::KeyEvent): its scan code as Windows numbers it, and the
// controller's CapsLock and NumLock after it.
type KeyEvent struct {
	Window          uintptr
	Code            uint32
	Down, Caps, Num uint8
	_               uint8
}

// KeyboardGrab grabs the keyboard for a viewer window (its native handle)
// in mode "map" or "translate", or ends its grab with "off". Keys are taken
// only while that window is in front; they arrive through Keyboard.Read.
// Returns: ErrNoKeyboard where the hook is unavailable.
// Side effects: the core installs its keyboard hook with the first grab and
// removes it with the last; an ending grab releases the keys it held.
func (l *Lib) KeyboardGrab(window uintptr, mode string) error {
	b := []byte(mode)
	rc := int32(syscallN(l.kbGrab, window, uintptr(unsafe.Pointer(unsafe.SliceData(b))), uintptr(len(b))))
	runtime.KeepAlive(b)
	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("unknown keyboard mode %q", mode)
	}
	return ErrNoKeyboard
}

// Keyboard reads the keys the core's keyboard grab takes. Use one from one
// goroutine.
type Keyboard struct {
	l *Lib
	// ev receives a key. It lives in the Keyboard (a heap object, which
	// never moves) rather than on a goroutine stack.
	ev KeyEvent
}

// Keyboard returns a reader of grabbed keys.
func (l *Lib) Keyboard() *Keyboard { return &Keyboard{l: l} }

// Read waits up to timeout for the next grabbed key.
// Returns: the key and true; false when none came in time; ErrNoKeyboard
// where the hook is unavailable.
// Notes: blocks its goroutine's OS thread without the Lib's lock.
func (k *Keyboard) Read(timeout time.Duration) (KeyEvent, bool, error) {
	rc := int32(syscallN(k.l.kbRead, uintptr(unsafe.Pointer(&k.ev)), uintptr(timeout.Milliseconds())))
	runtime.KeepAlive(k)
	switch rc {
	case 1:
		return k.ev, true, nil
	case 0:
		return KeyEvent{}, false, nil
	}
	return KeyEvent{}, false, ErrNoKeyboard
}
