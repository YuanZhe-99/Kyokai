# Packaging

`scripts/build.sh [goos/goarch …]` builds, per target, the Rust core (`cargo build --release`)
and the engine (`kyokai-svc`, `CGO_ENABLED=0`) into `resources/<goos>-<goarch>/bin`, copies
`LICENSE` and `THIRD_PARTY_NOTICES.md` into `resources/licenses`, and runs `go tool mygo build`.
Windows targets need a Windows host (the core links with MSVC; an ARM64 machine also builds x64);
macOS targets need a Mac. `CORE_PREBUILT=1` uses core libraries already placed in the resources
(the release workflow), and `MYGO_SIGN` passes a signing identity to `mygo build -sign`.

## Artifacts

| Platform | Files |
|---|---|
| Windows (x64, ARM64) | `Kyokai.exe` with `bin/kyokai-svc.exe`, `bin/kyokai_core.dll` and `licenses/`; a per-user NSIS installer (`%LOCALAPPDATA%\Programs\Kyokai`, no administrator rights); a portable zip |
| macOS (Apple silicon, Intel) | `Kyokai.app` (the engine and the core in `Contents/Resources/bin`) and a `.dmg` |
| Both | with `MYGO_UPDATER_PRIVATE_KEY`: the signed update archive, deltas and `update-<target>.json` |

Built files under `resources/<goos>-<goarch>/` and `resources/licenses/` are not committed.

## Signing

- **Updates:** Ed25519 (MyGo updater); the archive covers the whole app, the core library
  included.
- **macOS code signing:** release builds use the self-signed "Kyokai Code Signing" certificate,
  imported into a temporary keychain and trusted for code signing on the runner. A stable identity
  keeps Screen Recording, Accessibility and Keychain grants across updates; Gatekeeper still asks
  "Open Anyway" on first launch. The hardened runtime that a real identity turns on would refuse
  the Team-ID-less core library, so `packaging/macos/app.entitlements` and
  `packaging/macos/engine.entitlements` (`macos.helperEntitlements` for `bin/kyokai-svc`) disable
  library validation. Without the certificate the app is ad hoc signed.
- **Windows:** not code signed; SmartScreen warns on first launch.

## Licenses

Kyokai is AGPL-3.0-only. Every package carries `licenses/LICENSE` and
`licenses/THIRD_PARTY_NOTICES.md`, which the Settings page shows; the About page links the source.
