# Version history

## 0.1.0 (2026-10-17)

The first release, for Windows (x64, ARM64); macOS follows.

- Connections: the embedded EasyTier node through standard servers (relayed unless direct paths
  are turned on), Device ID resolution, Noise XX with OPAQUE logins (temporary and permanent
  passwords), the host's confirmation, trusted devices, unattended grants, rate limits, resumed
  sessions, identity backup, friendly names with vendor logos. Wire protocol 4, FFI ABI 3, local
  API 2.
- Manual Computer Use: DXGI capture with a GDI fallback, monitors, Media Foundation H.264 / HEVC /
  VP9 (hardware where present), libvpx VP8 / VP9 and libaom AV1, full colour 4:4:4, RustDesk's
  QoS and quality presets, input with RustDesk's keyboard modes, the remote pointer, clipboard
  text, images and files, session actions (lock, Ctrl+Alt+Del, input block, privacy mode).
- AI Computer Use on the host with its own keys: Claude, OpenAI and OpenAI-compatible providers
  from templates, approvals, take-over, emergency stop, pause at sleep, tasks steered from a
  controller.
- Run modes: No privileges, Follow Kyokai and Background service through a signed, self-updating,
  self-disabling privileged helper with a session agent on the input desktop.

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
- Release check (2026-10-17): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0,
  vcpkg 2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.
