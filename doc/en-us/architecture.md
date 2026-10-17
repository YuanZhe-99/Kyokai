# Architecture

## Processes

| Process | Binary | Runs as | Role |
|---|---|---|---|
| GUI | `Kyokai` (MyGo) | the user, never elevated | Window, tray, settings. Holds no identity; shows what the engine pushes. |
| Engine | `kyokai-svc run --host user` | the user, a child of the GUI | Loads the Rust core, which owns the device identity, the passwords, the embedded EasyTier node ([networking.md](networking.md)) and both ends of sessions ([protocol.md](protocol.md)); serves the local API. |
| Control CLI | `kyokai-svc ctl …` | the user | Scriptable client of the local API (development, smoke tests). |

The engine lives exactly as long as the GUI: on macOS it stops when the lifeline pipe the GUI holds
closes, on Windows when the GUI's process is gone (`--parent-pid`); quitting from the tray also asks
it to shut down. A second engine on the same socket refuses to start before touching the identity.
`KYOKAI_PROFILE` gives a development instance its own app name (and so its own single-instance
lock), socket and state directory.

## Run modes

| Mode | Requirement | Engine | Status |
|---|---|---|---|
| No privileges | none | user-level child of the GUI | implemented |
| Follow GUI | privileged helper installed once | in the helper while the GUI runs | the helper's install, service and authorization (Windows); the mode itself not yet |
| Background service | the same helper | from boot | not implemented |

Administrator rights are never requested when the GUI starts. The helper and the modes are
described in [run-modes.md](run-modes.md).

## Packages and crates

| Package / crate | Role |
|---|---|
| `cmd/kyokai` | GUI entry point |
| `cmd/kyokai-svc` | Engine entry point (`run`, `ctl`, `version`) |
| `internal/gui` | GUI state, views, tray, app updates, the engine child |
| `internal/engine` | Engine host: lifeline, local API handlers over the core |
| `internal/localapi` | Versioned NDJSON protocol over AF_UNIX, peer-credential authorization |
| `internal/core` | purego binding to the Rust core |
| `internal/fsutil` | Atomic writes and JSON documents |
| `internal/version` | The single product version |
| `core/kyokai-core` | Rust core library: C ABI, secrets store, device identity, passwords and OPAQUE (`auth`), access decisions (`access`), sessions (`session`), EasyTier node (`net`), screen capture and H.264 (`video`, `mf`), the input service (`input`), the viewer's keyboard grab (`keyboard`), AI tasks (`ai`: the providers, the task runtime, the desktop it drives) |
| `core/third_party/rustdesk-dxgi` | RustDesk's DXGI and GDI screen capture, vendored ([computer-use.md](computer-use.md)) |
| `core/third_party/rustdesk-qos` | RustDesk's video QoS, vendored |
| `core/third_party/thunk-noop` | Stands in for EasyTier's `thunk-rs` build step, so Windows builds keep the static C runtime |

## FFI

The core is a C-ABI library (`kyokai_core.dll`, `libkyokai_core.dylib`) built with
`crate-type = ["cdylib", "rlib"]`, loaded with purego (`CGO_ENABLED=0`). Exports take and return
integers and pointers only:

| Export | Does |
|---|---|
| `kyokai_abi_version() -> u32` | ABI version (3); hosts refuse a mismatch |
| `kyokai_start(json, len, *out, *out_len) -> i32` | Starts the core with `{version, state_dir, profile}` |
| `kyokai_call(json, len, *out, *out_len) -> i32` | Answers `{method, params}` |
| `kyokai_free(ptr, len)` | Frees a response buffer |
| `kyokai_stop()` | Drops the core's state, including its in-memory secrets |
| `kyokai_media_read(session, buf, cap, timeout_ms) -> i64` | Waits for a controller session's next media packet and copies it into the caller's buffer; -1 timeout, -2 ended, -3 too large |
| `kyokai_media_write(session, ptr, len) -> i32` | Passes a control packet from the GUI (input, key-frame request) to the session |
| `kyokai_decoder_open(json, len) -> u64` | Opens a viewer decoder for a stream (`{codec, width, height}`); 0 when the codec is not available. Needs no started core |
| `kyokai_decoder_decode(id, frame, len, out, cap, *w, *h) -> i32` | Decodes one frame into the caller's RGBA buffer: 1 picture, 0 needs more input, -1 error, -3 too large |
| `kyokai_decoder_close(id)` | Releases a decoder |
| `kyokai_keyboard_grab(window, mode, len) -> i32` | Grabs the keyboard for a viewer window (its native handle) in mode `map` or `translate`, or ends its grab with `off`: 0, -1 unknown mode or no window, -2 not available here (Windows only so far). Needs no started core |
| `kyokai_keyboard_read(*event, timeout_ms) -> i32` | Waits for the next key the grab took and writes it into the caller's `{window: usize, code: u32, down, caps, num: u8, pad: u8}`: 1 a key, 0 timeout, -2 not available |

Responses are `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` in a buffer the core
allocates; panics are caught at the boundary and reported as errors. Methods: `status` (never a
secret; includes the network status), `password.reveal`, `password.regenerate`, `network.get`
(settings without the secret), `network.set` (`{servers, name, secret?}`; a missing secret keeps
the stored one), `password.set_permanent` (`{password}`, null removes it), `session.connect`
(`{device_id, password, want}`; returns the session id at once, progress shows in the status),
`session.close`, `request.decide` (`{id, allow, permissions}`), `trusted.list`, `trusted.remove`,
`host.forget`, `identity.export` (`{passphrase}` → `{document}`), `identity.import`
(`{document, passphrase}`), `identity.reset`, `device.set_name` (`{name}`; "" follows the computer's
name; the status carries `name` and this device's `about`), `trusted.unattended` (`{key, days}`; 0
stops it), `store.export` (`{passphrase}` → `{document}`), `store.import` (`{document,
passphrase}`), `store.clear` (a run-mode change, [run-modes.md](run-modes.md)), `host.serve` (`{serve}`: `all`, `unattended` or `none`, set by the engine from its run
mode), and for AI tasks ([computer-use.md](computer-use.md)):
`ai.settings.get`, `ai.settings.set` (`{sources?, model?: {source, id}, approval?}`; the answer adds
`keys` per source), `ai.key.set` (`{source, key}`, null removes it; a key is never returned),
`ai.models.list` (`{source}` → `{models}`, asks the source), `ai.task.start`
(`{key, instruction}` → `{id}`; the key makes it idempotent), `ai.task.reply` (`{id, text}`),
`ai.task.pause`, `ai.task.resume`, `ai.task.cancel` (`{id}`), `ai.task.decide` (`{id, approve}`),
`ai.tasks` (newest first), `ai.history.clear`, `ai.stop` (cancels every task). The status also
carries `permanent_password`, `requests` (open host confirmations), `sessions`, `recent` (hosts
connected to, newest first) and `ai` (the task in progress with its state and origin, or null). The Go binding keeps the response slots it passes to the core in heap memory,
never on a goroutine stack, which may move between taking an address and the call. On Windows the
library is loaded by absolute path with
`LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` and links the C
runtime statically, so nothing is resolved through the search path.

## Local API

- Transport: an AF_UNIX socket (also on Windows) at `%TEMP%\kyokai\engine.sock` or
  `$TMPDIR/kyokai-<uid>/engine.sock` (`engine-<profile>.sock` for a profile; `KYOKAI_SOCKET`
  overrides); newline-delimited JSON with a `version` field (1). One request per connection,
  except `session.attach`, which keeps its connection and receives the status whenever it
  changes.
- Requests: `status` (with the network status), `password.reveal`, `password.regenerate`,
  `network.get`, `network.set` (refused settings answer `invalid` with the reason),
  `password.set_permanent`, `session.connect`, `session.close`, `request.decide`, `trusted.list`,
  `trusted.remove`, `host.forget`, `identity.export`, `identity.import`, `identity.reset`,
  `device.set_name`, `trusted.unattended`, `helper.background` (the helper only), `store.export`,
  `store.import`, `store.clear` (stops the engine after), `session.attach`,
  `session.media`, `shutdown`, and the AI requests named after the core's
  methods (`ai.settings.get` … `ai.stop`). `session.media` (`{id}` of a live controller
  session) answers OK and then carries length-prefixed packets both ways: video, stats and the
  host's AI task from the engine, control and AI messages from the GUI; the GUI asks for a key frame first. The status includes the open host
  confirmations and the sessions; secrets travel only towards the engine (the network secret, the
  permanent password, a typed password, a provider's API key). A session receives the status after every command that changes it
  and whenever the network's status moves.
- Authorization comes from the operating system's peer credentials (`LOCAL_PEERCRED` on macOS;
  `SIO_AF_UNIX_GETPEERPID` and the process token's SID on Windows), never from the request: only
  the engine's own user is served; an unreadable identity is refused.

## State

| Path | Holds |
|---|---|
| `%APPDATA%\Kyokai\` / `~/Library/Application Support/Kyokai/` | The state directory (`Kyokai-<profile>` for a profile) |
| `secrets.bin` | The secrets store, encrypted ([security.md](security.md)) |
| `master.key` (Windows) | The store's key as a DPAPI blob; on macOS the key is in the login Keychain |
| `gui.json` | GUI preferences |
| `ai.json` | AI settings: provider, models, approval policy (no key) |
| `ai-tasks.json` | Finished AI tasks, without screenshots |
| `logs/engine.log` | Engine log, without secrets |

## Cross-cutting rules

- **Secrets never travel** ([security.md](security.md)).
- **Never show stale data as current.** The GUI shows the engine's state only while its session is
  attached and says "starting" or why the engine is not running otherwise.
- **Versioned boundaries.** The ABI and the local API carry versions; enums cross as names.
