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
| QoS (2026-10-17) | over the public relay the host started at 15 fps, rose to 30 as the probes came back, and chose the Balanced bitrate (1.39 Mbit/s at 1080p); probe delays of 100–280 ms (relay path plus the video queue); the Faster preset with a 15 fps cap brought the bitrate to 1.04 Mbit/s and held 15 fps |
| Listener rebinding (2026-10-17) | a host whose DHCP address changed after its listener bound stopped accepting sessions; the listener now binds again when the node's address changes |
| AI runtime (2026-10-17) | a live Claude task (`claude-opus-5-5`) through the core's HTTPS client on the synthetic screens (a fake desktop, nothing injected): it clicked SAVE, waited, took a screenshot, confirmed the result and finished; the toolset, tool-result clearing and the three beta headers were accepted. On this machine the GDI capture returned the 1920×1080 primary display (sent as 1365×768) and the input hooks installed; no task has run on a real desktop yet |
| AI from a controller (2026-10-17) | in the session tests two engines' cores (in-memory link, the host's AI on the fake desktop and a local provider stand-in) ran a task the controller started, approved from the controller, to the end; a session without `run-ai-task` was refused, and so was steering a task the host's user started. Between two engines on this machine over the public relay (protocol 2), `ctl watch --text` reached the host's runtime, which refused it for want of a key, and a session without `run-ai-task` was refused. Not yet run between two machines |
| Clipboard (2026-10-17) | session tests with in-memory clipboards: text copied on either side after the session began reached the other, what was there before did not, a session without `clipboard` carried nothing, a 210 KB text crossed in parts (it used to end the session), and an image crossed as PNG; the system clipboard (arboard) is not yet exercised between two machines |
| Hardware H.264 (2026-10-17) | this machine's "QCOM Hardware Encoder - H264" (asynchronous) is chosen over Microsoft's software encoder; its first output follows the first frame, its stream decodes with Microsoft's decoder, and the screen source streams 1080p through it. Several open at once (parallel tests) crashed the process; now one is open per process and each is shut down when done, and 12 parallel test runs passed; CPU use not yet compared |
| Displays (2026-10-17) | this machine has one display, so no list is sent and the viewer offers no choice; switching between displays and input on a second display wait for a machine with two |
| AI on the local API (2026-10-17) | a real engine stored and reported the AI settings, refused an unknown policy and a task without a key, and kept the provider key out of every file but the secrets store |
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
