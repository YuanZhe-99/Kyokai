# Architecture

## Processes

| Process | Binary | Runs as | Role |
|---|---|---|---|
| GUI | `Kyokai` (MyGo) | the user, never elevated | Window, tray, settings. Holds no identity; shows what the engine pushes. |
| Engine | `kyokai-svc run --host user` | the user, a child of the GUI | Loads the Rust core, which owns the device identity and the temporary password; serves the local API. |
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
| Follow GUI | privileged helper installed once | in the helper while the GUI runs | not implemented |
| Background service | the same helper | from boot | not implemented |

Administrator rights are never requested when the GUI starts.

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
| `core/kyokai-core` | Rust core library: C ABI, secrets store, device identity, temporary password |

## FFI

The core is a C-ABI library (`kyokai_core.dll`, `libkyokai_core.dylib`) built with
`crate-type = ["cdylib", "rlib"]`, loaded with purego (`CGO_ENABLED=0`). Exports take and return
integers and pointers only:

| Export | Does |
|---|---|
| `kyokai_abi_version() -> u32` | ABI version (1); hosts refuse a mismatch |
| `kyokai_start(json, len, *out, *out_len) -> i32` | Starts the core with `{version, state_dir, profile}` |
| `kyokai_call(json, len, *out, *out_len) -> i32` | Answers `{method, params}` |
| `kyokai_free(ptr, len)` | Frees a response buffer |
| `kyokai_stop()` | Drops the core's state, including its in-memory secrets |

Responses are `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` in a buffer the core
allocates; panics are caught at the boundary and reported as errors. Methods: `status` (never a
secret), `password.reveal`, `password.regenerate`. On Windows the library is loaded by absolute path
with `LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` and links the
C runtime statically, so nothing is resolved through the search path.

## Local API

- Transport: an AF_UNIX socket (also on Windows) at `%TEMP%\kyokai\engine.sock` or
  `$TMPDIR/kyokai-<uid>/engine.sock` (`engine-<profile>.sock` for a profile; `KYOKAI_SOCKET`
  overrides); newline-delimited JSON with a `version` field (1). One request per connection,
  except `session.attach`, which keeps its connection and receives the status whenever it
  changes.
- Requests: `status`, `password.reveal`, `password.regenerate`, `session.attach`, `shutdown`.
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
| `logs/engine.log` | Engine log, without secrets |

## Cross-cutting rules

- **Secrets never travel** ([security.md](security.md)).
- **Never show stale data as current.** The GUI shows the engine's state only while its session is
  attached and says "starting" or why the engine is not running otherwise.
- **Versioned boundaries.** The ABI and the local API carry versions; enums cross as names.
