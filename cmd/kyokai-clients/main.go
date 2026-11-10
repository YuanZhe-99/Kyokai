//go:build cgo

// Command kyokai-clients is Kyokai's SSH, Remote Desktop and VNC clients as
// a C library (libkyokai_clients.so) for the Android app, Kyokai-Mobile,
// which has no engine and no Go GUI: the app drives the clients through the
// same kind of interface it drives the Rust core with — a JSON call, and
// per client a stream of packets it reads on a thread of its own.
//
// The clients reach the host's services through Kyokai tunnels (doc/en-us/
// remote-services.md). The core is the app's: it is already loaded and
// started when the app calls kyokai_clients_init with the addresses of the
// core's kyokai_call, kyokai_free, kyokai_media_read and kyokai_media_write,
// and this library never starts or stops it.
//
// Exports:
//
//	uint32_t kyokai_clients_abi(void)
//	void     kyokai_clients_init(void *call, void *free, void *read, void *write)
//	int32_t  kyokai_clients_call(const uint8_t *req, size_t len, uint8_t **out, size_t *out_len)
//	void     kyokai_clients_free(uint8_t *p, size_t len)
//	int64_t  kyokai_clients_read(uint64_t id, uint8_t *buf, size_t cap, uint32_t timeout_ms)
//
// kyokai_clients_call takes {"method", "params"} and answers {"ok": true,
// "result"} or {"ok": false, "error"} in memory the caller frees with
// kyokai_clients_free. Methods (calls.go): ssh.open, sftp.open, rdp.open,
// vnc.open (each answers {"id"} at once and connects on its own), write,
// resize, pointer, scroll, scancode, key, text, clipboard, display, close;
// on an sftp.open client the files calls of sftp.go: sftp.list, sftp.mkdir,
// sftp.remove, sftp.rename, sftp.download, sftp.upload, sftp.cancel; and
// key.import (keys.go).
//
// kyokai_clients_read returns the next packet of client id (packets.go):
// its length; -1 when none came within timeout_ms; -2 when the client ended
// and has nothing more; or, when cap is too small, minus the length it
// needs (always below -3), the packet kept for the next read.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef int32_t (*kc_call_fn)(const uint8_t *, size_t, uint8_t **, size_t *);
typedef void (*kc_free_fn)(uint8_t *, size_t);
typedef int64_t (*kc_read_fn)(uint64_t, uint8_t *, size_t, uint32_t);
typedef int32_t (*kc_write_fn)(uint64_t, const uint8_t *, size_t);

static void *core_call, *core_free, *core_read, *core_write;

static void kc_init(void *c, void *f, void *r, void *w) {
	core_call = c; core_free = f; core_read = r; core_write = w;
}
static int kc_ready(void) { return core_call && core_free && core_read && core_write; }
static int32_t kc_call(const uint8_t *p, size_t n, uint8_t **out, size_t *out_len) {
	return ((kc_call_fn)core_call)(p, n, out, out_len);
}
static void kc_free(uint8_t *p, size_t n) { ((kc_free_fn)core_free)(p, n); }
static int64_t kc_read(uint64_t id, uint8_t *buf, size_t cap, uint32_t ms) {
	return ((kc_read_fn)core_read)(id, buf, cap, ms);
}
static int32_t kc_write(uint64_t id, const uint8_t *p, size_t n) {
	return ((kc_write_fn)core_write)(id, p, n);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"time"
	"unsafe"
)

// abi is this library's interface version: the app refuses another.
const abi = 1

func main() {}

//export kyokai_clients_abi
func kyokai_clients_abi() C.uint32_t { return abi }

// kyokai_clients_init keeps the core's exports this library calls.
// Inputs: the addresses of the app's loaded core's kyokai_call, kyokai_free,
// kyokai_media_read and kyokai_media_write.
//
//export kyokai_clients_init
func kyokai_clients_init(call, free, read, write unsafe.Pointer) {
	C.kc_init(call, free, read, write)
}

// kyokai_clients_call runs one method (calls.go) and answers it.
// Inputs: req/len, the request's JSON, borrowed for the call.
// Returns: 0 with *out/*out_len the answer, which the caller frees with
// kyokai_clients_free; -1 for a null pointer.
//
//export kyokai_clients_call
func kyokai_clients_call(req *C.uint8_t, n C.size_t, out **C.uint8_t, outLen *C.size_t) C.int32_t {
	if req == nil || out == nil || outLen == nil {
		return -1
	}
	body := C.GoBytes(unsafe.Pointer(req), C.int(n))
	answer := handle(body)
	p := C.CBytes(answer)
	*out = (*C.uint8_t)(p)
	*outLen = C.size_t(len(answer))
	return 0
}

// kyokai_clients_free frees an answer of kyokai_clients_call.
//
//export kyokai_clients_free
func kyokai_clients_free(p *C.uint8_t, _ C.size_t) { C.free(unsafe.Pointer(p)) }

// kyokai_clients_read takes client id's next packet into buf.
// Returns: its length; -1 after timeout_ms without one; -2 when the client
// is gone; below -3, minus the length a too small buf needs.
//
//export kyokai_clients_read
func kyokai_clients_read(id C.uint64_t, buf *C.uint8_t, cap C.size_t, ms C.uint32_t) C.int64_t {
	c := clients.get(uint64(id))
	if c == nil {
		return -2
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(cap))
	return C.int64_t(c.read(dst, time.Duration(ms)*time.Millisecond))
}

// handle decodes a request, runs it and encodes its answer.
func handle(body []byte) []byte {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var result any
	err := json.Unmarshal(body, &req)
	if err == nil {
		result, err = run(req.Method, req.Params)
	}
	var answer []byte
	if err != nil {
		answer, _ = json.Marshal(map[string]any{"ok": false, "error": err.Error()})
	} else {
		answer, _ = json.Marshal(map[string]any{"ok": true, "result": result})
	}
	return answer
}

// coreCall calls one of the core's methods and decodes its result into out
// (nil to drop it).
func coreCall(method string, params any, out any) error {
	if C.kc_ready() == 0 {
		return fmt.Errorf("the core is not given")
	}
	body, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		return err
	}
	p := C.CBytes(body)
	defer C.free(p)
	var answer *C.uint8_t
	var n C.size_t
	if C.kc_call((*C.uint8_t)(p), C.size_t(len(body)), &answer, &n) != 0 {
		return fmt.Errorf("the core refused the call")
	}
	raw := C.GoBytes(unsafe.Pointer(answer), C.int(n))
	C.kc_free(answer, n)
	var doc struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if !doc.OK {
		return fmt.Errorf("%s", doc.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(doc.Result, out)
}

// mediaRead reads one of the core's packets for session id into buf.
func mediaRead(id uint64, buf []byte, timeout time.Duration) int64 {
	return int64(C.kc_read(C.uint64_t(id), (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)), C.uint32_t(timeout/time.Millisecond)))
}

// mediaWrite writes one packet to the core for session id; it blocks until
// the core has room.
func mediaWrite(id uint64, packet []byte) int32 {
	p := C.CBytes(packet)
	defer C.free(p)
	return int32(C.kc_write(C.uint64_t(id), (*C.uint8_t)(p), C.size_t(len(packet))))
}
