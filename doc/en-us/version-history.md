# Version history

## 0.1.0 (in progress)

- Repository scaffold: working conventions (`AGENTS.md`), mirrored documentation, the upstream pin
  check, the Rust workspace and the Go module; release signing material generated (app updates,
  helper bundles, macOS code signing).
- Foundation: the Rust core (secrets store under the OS credential store, persistent device
  identity and Device ID, temporary password) behind a versioned C ABI; the `kyokai-svc` engine as
  a user-level child of the GUI with the local API; the GUI (Home, Settings, tray, four languages,
  signed app updates); CI and release workflows; Windows packages.
- Phase 0: the S1 EasyTier, S2 viewer and S3-lite capture spikes pass on Windows ARM64.
- MyGo v0.5.5 / Rust 1.99.1; EasyTier v2.7.0 (prerelease, commit `8b7f1f0`) and RustDesk 1.5.0
  (commit `fada664`) pinned for the coming phases (checked 2026-10-16).
- vcpkg 2026.07.29 (commit `9e593bb`: libvpx 1.16.0, aom 3.13.3) pinned for the software codecs;
  MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 and Rust 1.99.1 unchanged (checked 2026-10-17).
