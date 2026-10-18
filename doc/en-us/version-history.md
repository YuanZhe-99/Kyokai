# Version history

## 0.1.1 (2026-10-17)

The macOS release (Apple silicon, Intel), and connections reworked after RustDesk.

- macOS: ScreenCaptureKit capture, CGEvent input with the keyboard modes, the privacy permissions
  (Screen Recording, Accessibility, Input Monitoring, Keychain) asked for and shown on Home; the
  run modes through a LaunchDaemon helper and a LaunchAgent (Kyokai Helper), installed with one
  administrator prompt, updated by code signature, removed without one; signed with an Apple
  Development certificate so grants and Keychain access survive updates.
- Connections: devices only, no sessions in the interface. Home shows This device, Control another
  device and Recent devices as cards (menu: Connect, Connect with AI, Forget password, Remove).
  Each connection is a tab beside Home that can be dragged into its own window and back, with
  full screen, a password field with Remember password while the host is asked, and Reconnect.
- Access: Accepted passwords (temporary, permanent or both) and a global Connect without asking,
  on by default, replace per-device unattended grants; with nobody at the computer only the
  permanent password admits.
- AI: the host keeps one AI record for every controller, and the draft of the next instruction is
  shared live between the host and every controller. Wire protocol 6, FFI ABI 4, local API 2.
- Fixes: a stopping engine no longer removes its successor's socket; quitting at logout works;
  Reopen no longer starts a second copy; a store that cannot be unlocked offers Start over; crash
  traces are written to the logs folder.
- Release check (2026-10-17): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0,
  vcpkg 2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.

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
