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
| `core/kyokai-core` | Rust core library: C ABI, secrets store, device identity, passwords and OPAQUE (`auth`), access decisions (`access`), sessions (`session`), EasyTier node (`net`), screen capture and codecs (`video`, `mf` for Media Foundation, `vt` for VideoToolbox, `soft` for libvpx and libaom, `annexb` for the H.264 and HEVC framing), the host's sound (`audio`, `opus`), the input service (`input`), the viewer's keyboard grab (`keyboard`), AI tasks (`ai`: the providers, the task runtime, the desktop it drives), sign-in keys (`keys`), tunnels to the host's own services (`tunnel`), the host's status (`system`) and its watchers (`watch`) |
| `core/third_party/rustdesk-codec` | RustDesk's libvpx and libaom codecs, vendored, with the static libraries vcpkg builds ([computer-use.md](computer-use.md)) |
| `core/third_party/rustdesk-dxgi` | RustDesk's DXGI and GDI screen capture, vendored ([computer-use.md](computer-use.md)) |
| `core/third_party/rustdesk-qos` | RustDesk's video QoS, vendored |
| `core/third_party/thunk-noop` | Stands in for EasyTier's `thunk-rs` build step, so Windows builds keep the static C runtime |

## FFI

The core is a C-ABI library (`kyokai_core.dll`, `libkyokai_core.dylib`) built with
`crate-type = ["cdylib", "rlib"]`, loaded with purego (`CGO_ENABLED=0`). Exports take and return
integers and pointers only:

| Export | Does |
|---|---|
| `kyokai_abi_version() -> u32` | ABI version (5); hosts refuse a mismatch |
| `kyokai_start(json, len, *out, *out_len) -> i32` | Starts the core with `{version, state_dir, profile}` |
| `kyokai_call(json, len, *out, *out_len) -> i32` | Answers `{method, params}` |
| `kyokai_free(ptr, len)` | Frees a response buffer |
| `kyokai_stop()` | Ends every session (each connected one with a goodbye, waiting up to a second), then drops the core's state, including its in-memory secrets |
| `kyokai_media_read(session, buf, cap, timeout_ms) -> i64` | Waits for a controller session's next media packet (a tunnel's session: its bytes, packet 14) and copies it into the caller's buffer; -1 timeout, -2 ended, -3 too large |
| `kyokai_media_write(session, ptr, len) -> i32` | Passes a control packet from the GUI (input, key-frame request) to the session: 0, -1 malformed, -2 ended, -3 refused as behind; input never waits — moves, presses, clicks and text are refused while the session cannot take input, releases never are (`session::InputGate`) — and other messages wait 200 ms at most; a tunnel's bytes (packet 14) always wait for room |
| `kyokai_decoder_open(json, len) -> u64` | Opens a viewer decoder for a stream (`{codec, width, height}`); 0 when the codec is not available. Needs no started core |
| `kyokai_decoder_decode(id, frame, len, out, cap, *w, *h) -> i32` | Decodes one frame into the caller's RGBA buffer: 1 picture, 0 needs more input, -1 error, -3 too large |
| `kyokai_decoder_info(id, out, cap) -> i32` | Writes `{decoder, hardware}` JSON into the caller's buffer: which decoder it is (`videotoolbox`, `media-foundation`, `libvpx`, `libaom`) and whether it decodes in hardware, as it reports; the length, -1 unknown handle, -3 too large |
| `kyokai_decoder_close(id)` | Releases a decoder |
| `kyokai_audio_open() -> u64` | Opens a decoder of the host's sound (Opus); 0 when it cannot. Needs no started core |
| `kyokai_audio_decode(id, packet, len, out, cap) -> i32` | Decodes one Opus packet into the caller's float buffer as interleaved stereo at 48 kHz: the frames written, -1 error, -3 too large |
| `kyokai_audio_close(id)` | Releases a sound decoder |
| `kyokai_keyboard_grab(window, mode, len) -> i32` | Grabs the keyboard for a viewer window (its native handle) in mode `map` or `translate`, or ends its grab with `off`: 0, -1 unknown mode or no window, -2 not available here (no hook; on macOS, an event tap needs the app's Accessibility permission). Needs no started core |
| `kyokai_keyboard_read(*event, timeout_ms) -> i32` | Waits for the next key the grab took and writes it into the caller's `{window: usize, code: u32, down, caps, num: u8, pad: u8}`: 1 a key, 0 timeout, -2 not available |
| `kyokai_privacy_request(name, len) -> i32` | Asks macOS, from the calling process, for `screen`, `accessibility` or `input-monitoring` (the app calls it, so a prompt is the app's own): 1 granted, 0 not yet, -1 unknown name or no such permission here. Needs no started core |

Responses are `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` in a buffer the core
allocates; panics are caught at the boundary and reported as errors. Methods: `status` (never a
secret; includes the network status), `password.reveal`, `password.regenerate`, `network.get`
(the first network's settings without the secret, for an older GUI and `ctl`), `network.set` (`{servers, name, secret?, direct?}`: the first network; a missing secret keeps
the stored one, an empty one clears it, no servers and no name remove it), `network.profiles` (→ `{direct, profiles}`: every
network kept, `{id, label, servers, name, has_secret, weak_secret, joined}`), `network.profile.set` (`{id?, label?,
servers?, name?, secret?, joined?}`: no id adds a network, joined unless it says otherwise; with one, what it leaves out
keeps its value, the secret too; answers the networks), `network.profile.remove` (`{id}`), `network.direct`
(`{direct}`: try direct paths in every network, [networking.md](networking.md)), `password.set_permanent` (`{password}`, null removes it), `session.connect`
(`{device_id, password, want, remember, address?}`; an empty password uses the one saved for the host;
`address` dials that IP address or domain name instead of the network; returns the session id at
once, progress shows in the status), `session.identify` (`{address}` → `{device_id}` or
`{failure}`: `unreachable`, `incompatible`, `broken` or `self`; dials without holding the core's
lock), `device.remove` (`{device_id}`: ends this device's sessions with that host, leaves them out of
the list, forgets its pinned key and everything saved for it), `protect.set` (`{on, password?}`: protected mode, on with a permanent password set, off with it, [identity-and-access.md](identity-and-access.md)), `access.history` (`{keep}`: the connection
history in the secrets store, or in memory only for No logs, [security.md](security.md)), `password.forget_saved`
(`{device_id}`: its password and everything else saved for it), `session.close`, `request.decide` (`{id, allow, permissions}`), `trusted.list`, `trusted.remove`,
`host.forget`, the sign-in keys ([identity-and-access.md](identity-and-access.md), "Keys"): `keys.list`
(public parts only), `keys.add` (`{name, private}`), `keys.remove` (`{id}`), `keys.sign`
(`{key_id, algorithm, data}` → `{signature}`), `authorized.list`, `authorized.add`
(`{name, public}`), `authorized.remove` (`{id}`), the tunnels and saved service sign-ins
([remote-services.md](remote-services.md)): `tunnel.open` (`{device_id, address?, service}` →
`{id}`), `tunnel.wait` (`{id, timeout_ms}` → `{state, failure}`, waiting without the core's lock),
`credentials.get`, `credentials.set`, `credentials.forget`, `watch.set` (`{devices}`: watch these hosts'
status for 30 seconds, [computer-use.md](computer-use.md), "Host status"), `identity.export` (`{passphrase}` → `{document}`), `identity.import`
(`{document, passphrase}`), `identity.reset`, `device.set_name` (`{name}`; "" follows the computer's
name; the status carries `name` and this device's `about`), `access.settings` (`{password_mode?: temporary|permanent|both, without_asking?, by_address?: {on, ranges}}`; `by_address` starts or stops the listener at once), `permission.request` (`{name}`: `screen`, `accessibility`, `input-monitoring` or `keychain` on macOS → `{granted}`; `keychain` reopens the store and rebuilds the core when it opens; in the helper the agent asks), `store.start_over` (the saved store cannot be unlocked: it is moved aside and the core rebuilt on a new one → `{kept}`), `agent.restart` (the helper's session agent exits and its supervisor starts it again), `store.export` (`{passphrase}` → `{document}`), `store.import` (`{document,
passphrase, replace?}`: the whole store, version 2; another identity with history only with `replace`), `store.clear` (`{to}`: a run-mode change; leaves `moved.json`, [run-modes.md](run-modes.md)), `sessions.close_controllers` (ends this engine's controller sessions), `host.serve` (`{serve}`: `all`, `unattended` or `none`, set by the engine from its run
mode), `capture.show` (`{show}`: whether captures show Kyokai's windows, as Security's Hide Kyokai
from capture says; macOS's capture follows it, live; the helper passes it to its agent; hidden
until a GUI says), `input.kyokai` (`{allow}`: whether a person's remote input may reach Kyokai's
windows, as the same switch says, which the GUI sends with `capture.show`; each session decides per
event, never for the AI nor while a confirmation or an approval waits, and tells the helper's agent
with the event; refused until a GUI says; an older engine refuses the request and stays guarded),
`sound.source` (`{source}`: the sound to share, "" for what the computer plays or
an input device's id; running streams switch; the helper passes it to its agent), `sound.devices`
(→ `[{id, name}]`, the input devices; the agent's in the helper), and for AI tasks ([computer-use.md](computer-use.md)):
`ai.settings.get`, `ai.settings.set` (`{sources?, model?: {source, id}, approval?}`; the answer adds
`keys` per source), `ai.key.set` (`{source, key}`, null removes it; a key is never returned),
`ai.models.list` (`{source}` → `{models}`, asks the source), `ai.task.start`
(`{key, instruction}` → `{id}`; the key makes it idempotent), `ai.task.reply` (`{id, text}`),
`ai.task.pause`, `ai.task.resume`, `ai.task.cancel` (`{id}`), `ai.task.decide` (`{id, approve}`),
`ai.tasks` (newest first), `ai.draft.set` (`{text}`: the shared draft of the next instruction), `ai.history.clear`, `ai.stop` (cancels every task). The status also
carries `permanent_password`, `permissions` (macOS: `{screen, accessibility, input_monitoring, keychain}`, the agent's in the helper; null elsewhere),
`password_mode` and `without_asking` (the access settings), `by_address` (`{on, ranges, port, error}`: connections by address, [networking.md](networking.md)), `keeps_history`, `protected`, `ai_draft` (the shared draft),
`password_grants` (what a correct password grants without asking;
set with `access.password_grants` `{grants}`), `requests` (open host confirmations, each with
`password`: whether the controller gave a password or a key, and `key`: the key's name), `sessions` (a tunnel's with its `service`), `recent` (hosts
connected to, newest first, each with the `address` its last session dialed, the `features` the
host announced and `saved`, a flag per kind of sign-in kept for it), `watching` and `watchers` (the
status watches from and of this device) and `ai` (the task in progress with its state and origin, or null). The Go binding keeps the response slots it passes to the core in heap memory,
never on a goroutine stack, which may move between taking an address and the call. On Windows the
library is loaded by absolute path with
`LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` and links the C
runtime statically, so nothing is resolved through the search path.

## Local API

- Transport: an AF_UNIX socket (also on Windows) at `%TEMP%\kyokai\engine.sock` or
  `$TMPDIR/kyokai-<uid>/engine.sock` (`engine-<profile>.sock` for a profile; `KYOKAI_SOCKET`
  overrides); newline-delimited JSON with a `version` field (2). One request per connection,
  except `session.attach`, which keeps its connection and receives the status whenever it
  changes.
- Requests: `status` (with the network status and `build`, the commit the engine was built from,
  which tells two builds of one version apart), `password.reveal`, `password.regenerate`,
  `network.get`, `network.set`, `network.profiles`, `network.profile.set`, `network.profile.remove`, `network.direct`
  (refused settings answer `invalid` with the reason; the status's `network` adds `networks`, each network's own, to all
  of them together, so an older GUI reads it as before),
  `password.set_permanent`, `session.connect`, `session.identify`, `device.remove`, `protect.set` (while protected mode is on the engine answers only the requests in `protectedAllows` and refuses the rest as `invalid`; the status says `protected`), `logs.set` (`{logs_off}`: No logs; the engine writes or removes its state directory's `no-logs` marker, closes or reopens its log, deletes the logs and calls `access.history`; the status says `logs_off`; the GUI sends it as it attaches and when the setting changes), `password.forget_saved`, `session.close`, `request.decide`, `trusted.list`,
  `trusted.remove`, `host.forget`, `keys.list`, `keys.add`, `keys.remove`, `keys.sign`,
  `authorized.list`, `authorized.add`, `authorized.remove`, `credentials.get` (owner or operator
  only), `credentials.set`, `credentials.forget`, `watch.set`, `tunnel.open` (streaming, below), `identity.export`, `identity.import`, `identity.reset`,
  `device.set_name`, `access.settings`, `system.permission` (`{permission}`, macOS), `capture.show` (`{show}`; the GUI sends it as it attaches and when the setting changes), `input.kyokai` (`{kyokai_input}`; sent with `capture.show`; an engine that does not know it keeps Kyokai's windows guarded), `sound.source` (`{sound_source}`, likewise), `sound.devices` (→ `sound_devices`), `secrets.start_over`, `helper.background`, `helper.agent_restart` and `helper.remove` (the helper only; after the store moved out), `store.forget_move` (a parked engine starts over), `sessions.close_controllers`, `store.export`,
  `store.import`, `store.clear` (stops the engine after), `session.attach`,
  `session.media`, `shutdown`, and the AI requests named after the core's
  methods (`ai.settings.get` … `ai.stop`, `ai.draft.set`). `session.media` (`{id}` of a live controller
  session) answers OK and then carries length-prefixed packets both ways: video, stats, the
  host's codec offer, its AI task, what crossed the clipboard (packet 13) and whether input is
  paused (packet 15, `{paused}`) from the engine, control
  and AI messages from the GUI, among them `clipboard-use`, which the engine keeps for the session
  and never forwards; the GUI asks for a key frame first. `tunnel.open` (`{tunnel: {device_id,
  address?, service}}`) answers once the host admitted the tunnel (after up to 90 seconds) or names
  why not, then carries the tunnel's bytes as packets 14 both ways
  ([remote-services.md](remote-services.md)). These additions change no request's
  meaning, so the local API stays at version 2: an older engine ignores `clipboard-use` and an
  older GUI ignores packets 13 and 15. The status includes the open host
  confirmations and the sessions; secrets travel only towards the engine (the network secret, the
  permanent password, a typed password, a provider's API key). A session receives the status after every command that changes it
  and whenever the network's status moves.
- Authorization comes from the operating system's peer credentials (`LOCAL_PEERCRED` on macOS;
  `SIO_AF_UNIX_GETPEERPID` and the process token's SID on Windows), never from the request: only
  the engine's own user is served; an unreadable identity is refused. The server marks each
  connection's tier in the request's context (`localapi.IsOwner`): the owner, or the helper's
  operator, may also sign with the engine's keys (`keys.sign`) and read a saved service password
  (`credentials.get`); the helper's `kyokai` group may not.

## State

| Path | Holds |
|---|---|
| `%APPDATA%\Kyokai\` / `~/Library/Application Support/Kyokai/` | The state directory (`Kyokai-<profile>` for a profile) |
| `secrets.bin` | The secrets store, encrypted ([security.md](security.md)) |
| `moved.json` | Where this engine's store went in a run-mode switch: while it is there the engine is parked and makes no identity ([run-modes.md](run-modes.md)) |
| `master.key` (Windows) | The store's key as a DPAPI blob; on macOS the key is in the login Keychain |
| `gui.json` | GUI preferences |
| `ai.json` | AI settings: provider, models, approval policy (no key) |
| `ai-tasks.json` | Finished AI tasks, without screenshots |
| `logs/engine.log` | Engine log, without secrets: the engine's lines, the core's (its stderr) and a crash trace |
| `logs/gui-crash.log` | The GUI's stderr: its crash trace, a panic in its copy of the core, an engine that ended with an error |

## Cross-cutting rules

- **Secrets never travel** ([security.md](security.md)).
- **Never show stale data as current.** The GUI shows the engine's state only while its session is
  attached and says "starting" or why the engine is not running otherwise.
- **Versioned boundaries.** The ABI and the local API carry versions; enums cross as names.
- **Nothing waits for the main thread under the app lock.** MyGo's notifications, clipboard and
  window calls wait for the main thread, which takes the GUI's lock to draw: they are made after
  the lock is let go (`statusPushed`, `revealPassword`); a watchdog writes every goroutine's stack
  to `gui-crash.log` when the main thread has not answered for 10 seconds (`watchMain`).
