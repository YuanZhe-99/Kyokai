# Platform notes

| Platform | Status |
|---|---|
| Windows (x64, ARM64) | 0.1.0 foundation implemented and verified on Windows 11 ARM64, x64 under emulation (below). |
| macOS (Apple silicon, Intel) | Built and tested on GitHub's macOS runners (Apple silicon: full gate including `scripts/smoke.sh user` and the unsigned package; Intel: build and Go tests); not run on a real Mac yet. |
| Linux | Not supported yet. |

## Windows

Verified on Windows 11 ARM64 (Snapdragon dev kit, 2026-10-16; Go 1.27.2, Rust 1.99.1, Visual
Studio 2026 without its Clang component):

| Check | Result |
|---|---|
| Local gate | gofmt, `go vet` (Windows and macOS), `go test` (also `CGO_ENABLED=0`), `mygo vet`, `mygo i18n check`, `checkdocs`, `cargo fmt`, `cargo clippy -D warnings`, `cargo test` pass |
| ABI smoke test | `internal/core` loads the release `kyokai_core.dll`, starts it, calls and stops it |
| Core library | imports only system DLLs (`KERNEL32`, `ntdll`, `api-ms-win-core-synch`, and with EasyTier `ws2_32`, `iphlpapi`, `bcrypt`, `crypt32` and the like): the C runtime is static on ARM64 and x64 — no `msvcrt.dll` |
| Secrets | DPAPI-protected master key; the Device ID is the same after restarts of the engine and of the app |
| Engine | `scripts/smoke.sh user` passes: status, password, regenerate, a second engine refused, shutdown, restart |
| App (ARM64 package) | starts its engine as a child without any prompt; the temporary password differs on every start; killing the GUI ends the engine; nothing registered under `Run` |
| App (x64 package) | runs under emulation with the same identity; window rendered with the system's dark theme |
| Packages | `scripts/build.sh windows/arm64 windows/amd64` builds `Kyokai.exe` and the per-user installer for both |
| S2 viewer spike | 1920×1080 RGBA frames from Rust shown through `ui.Bitmap` next to a 500-row list at 59–60 fps; the viewer costs about 21–25 % of one core |
| S3-lite capture spike | RustDesk's DXGI capturer, vendored and decoupled from `hbb_common`, captures the 4K display at about 60 fps; converting to RGBA and scaling to 1080p costs about 15 % of one core |
| S1 EasyTier spike | v2.7.0 embedded in-process, no TUN, no admin: data plane both ways through a stock server, whitelist holds, clean restarts; hole punching untested (symmetric NAT) |
| EasyTier in the engine (2026-10-17) | through a standard public server: online with a DHCP address within 10 s; the NAT type is found through STUN (symmetric on this network); two engines with their own profiles list each other's Device ID, relayed, about 36 ms |
| Sessions (2026-10-17) | two engines through a standard server: a wrong password is refused as such on both sides; the right temporary password raises the host's confirmation, which granted view-screen only, and the session connected with exactly that; the controller then became a trusted device ("known"); a permanent password works the same; closing from the controller ends both sides |
| Identity backup (2026-10-17) | backup → reset (new Device ID) → restore with a wrong passphrase refused, with the right one the old Device ID, permanent password and trusted device are back and the network reconnects |
| Remote x64 VM (2026-10-17) | the CI-built x64 installer installs per user without a prompt; its engine joins the network through the standard server (symmetric NAT on both sides, so relayed, about 99 ms); a session from this machine to the VM raised the VM's confirmation and connected with the granted subset; after the VM's node restarted, the session resumed within 7 s with the same permissions and no new confirmation |
| Video channel (2026-10-17) | the test pattern (320×180 raw RGBA) through two engines and the public server's relay: 4 fps at about 7.6 Mbit/s, the relay's limit for this path; the stream description arrives and is repeated on a key-frame request |
| Screen video (2026-10-17) | this machine's 1920×1080 screen through DXGI, Media Foundation H.264 (software encoder; first output after the first frame), two engines and the public relay, decoded by the viewer's decoder: 15.5 fps on a mostly still screen at about 1 Mbit/s, the picture correct |
| `cargo deny` | passes (advisories, bans, licenses, sources) once VS's clang is on `PATH` |

## macOS

| Check | Result |
|---|---|
| CI (`macos-latest`, `macos-15-intel`) | gate, ABI smoke test, engine smoke test and unsigned package pass |
| Keychain | a test binary that reads a Keychain item another (differently signed) binary created raises an access prompt, which hangs a headless run; tests therefore use a fresh profile per run, and the shipped app is always signed with the same certificate |

## Known limitations

- AF_UNIX socket files created in folders under `AppData\Local` or `AppData\Roaming` stayed
  behind as inaccessible entries after the listener closed on the development machine, so a second
  bind failed; `%TEMP%` and the profile folder behaved. The engine's socket therefore lives in
  `%TEMP%\kyokai`. Leftover entries (`AppData\Local\kyokai`, test folders) cannot be deleted from
  the session that created them.
- `ring` (EasyTier, TLS clients, `cargo-deny`) needs clang on Windows ARM64: the VS component
  installs it under `VC\Tools\Llvm\ARM64\bin` but does not put it on `PATH`.
- Windows packages are not code signed; SmartScreen warns on first launch.
