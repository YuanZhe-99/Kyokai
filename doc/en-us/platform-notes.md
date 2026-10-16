# Platform notes

| Platform | Status |
|---|---|
| Windows (x64, ARM64) | 0.1.0 foundation implemented and verified on Windows 11 ARM64, x64 under emulation (below). |
| macOS (Apple silicon, Intel) | Implemented, cross-checked (`go vet`, `cargo check`); not built or run on a Mac yet — CI and a real machine come with the first push. |
| Linux | Not supported yet. |

## Windows

Verified on Windows 11 ARM64 (Snapdragon dev kit, 2026-10-16; Go 1.27.2, Rust 1.99.1, Visual
Studio 2026 without its Clang component):

| Check | Result |
|---|---|
| Local gate | gofmt, `go vet` (Windows and macOS), `go test` (also `CGO_ENABLED=0`), `mygo vet`, `mygo i18n check`, `checkdocs`, `cargo fmt`, `cargo clippy -D warnings`, `cargo test` pass |
| ABI smoke test | `internal/core` loads the release `kyokai_core.dll`, starts it, calls and stops it |
| Core library | imports only system DLLs (`KERNEL32`, `ntdll`, `api-ms-win-core-synch`): the C runtime is static |
| Secrets | DPAPI-protected master key; the Device ID is the same after restarts of the engine and of the app |
| Engine | `scripts/smoke.sh user` passes: status, password, regenerate, a second engine refused, shutdown, restart |
| App (ARM64 package) | starts its engine as a child without any prompt; the temporary password differs on every start; killing the GUI ends the engine; nothing registered under `Run` |
| App (x64 package) | runs under emulation with the same identity; window rendered with the system's dark theme |
| Packages | `scripts/build.sh windows/arm64 windows/amd64` builds `Kyokai.exe` and the per-user installer for both |
| S2 viewer spike | 1920×1080 RGBA frames from Rust shown through `ui.Bitmap` next to a 500-row list at 59–60 fps; the viewer costs about 21–25 % of one core |
| S3-lite capture spike | RustDesk's DXGI capturer, vendored and decoupled from `hbb_common`, captures the 4K display at about 60 fps; converting to RGBA and scaling to 1080p costs about 15 % of one core |
| `cargo-deny` | does not build here yet: `ring` needs clang on ARM64 (CI runs it) |

## Known limitations

- AF_UNIX socket files created in folders under `AppData\Local` or `AppData\Roaming` stayed
  behind as inaccessible entries after the listener closed on the development machine, so a second
  bind failed; `%TEMP%` and the profile folder behaved. The engine's socket therefore lives in
  `%TEMP%\kyokai`. Leftover entries (`AppData\Local\kyokai`, test folders) cannot be deleted from
  the session that created them.
- Until the Clang component is installed, nothing that depends on `ring` (EasyTier, TLS clients,
  `cargo-deny`) builds on Windows ARM64.
- Windows packages are not code signed; SmartScreen warns on first launch.
