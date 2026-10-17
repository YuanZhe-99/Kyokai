# PLAN.md

The active plan: milestones, done-when criteria and open decisions. Long-lived contracts live in
`doc/`. Delete items as they are done; delete this file when the plan is complete.

## Decisions in force

- Platforms: Windows x64 + ARM64, macOS arm64 + x64 (macOS 14+). Linux is not supported yet.
- License AGPL-3.0-only. UI languages en, zh-Hans, zh-Hant, ja; docs en-us + zh-cn.
- Run modes like Easytier-MyGo: No privileges (default: GUI + user-level engine child, no service,
  no prompt); Follow GUI and Background service through a privileged helper installed once.
- AI providers: OpenAI (Responses API `computer` tool) and Claude (`computer_toolset_20260801`),
  both from the start, behind one provider trait; AI runs only on the host.
- EasyTier v2.7 line (in-process data plane), pinned by commit, minimal feature subset (no TUN,
  SOCKS5, KCP, faketcp, magic DNS, CLI, web), following every upstream release.
- macOS builds are signed with a self-signed "Kyokai Code Signing" certificate so that privacy
  grants survive updates; app updates are signed for the MyGo updater; the privileged helper
  verifies its own updates (bundle signature on Windows, code signature on macOS).

## Design for the coming phases

- **Processes:** GUI (`Kyokai`, MyGo; controller role in-process from Phase 2) · engine
  (`kyokai-svc run --host user`, child of the GUI; EasyTier, device identity, host role, local API)
  · helper (`--host helper`, SYSTEM / root, Phase 6) · session agents (Phase 6). The GUI never
  runs elevated; privileged processes never decode remote media.
- **FFI:** C ABI with integers and pointers only; JSON calls and a blocking event poll; zero-copy
  RGBA frame slots; controller byte streams pumped by Go over a CONNECT-style local API upgrade.
- **Identity:** Ed25519 device key; 9-digit Device ID from its hash (locates, never authenticates);
  keys pinned after the first authentication; "new key claiming this ID" shown differently.
- **Authentication:** Noise XX + OPAQUE (temporary and permanent password slots in one round trip,
  bound to the Noise handshake); rate limits per overlay address and a global backoff that exempts
  pinned keys; host confirmation by default; unattended access only with a pinned key, the permanent
  password and an expiring grant; delegated, audience-bound controller certificates.
- **Networking:** `instance_id = UUIDv5(device id)`, data-plane bind/connect, loopback whitelists,
  EasyTier treated as an untrusted pipe.
- **Desktop:** RustDesk's platform layers on both systems (see "RustDesk reuse per platform"),
  decoupled from `hbb_common`; macOS capture is new ScreenCaptureKit code (no CGDisplayStream);
  arboard for the clipboard.
- **AI:** append-only Claude history (server-side clearing, never client pruning), per-provider
  screenshot scaling, ordered batches with an approval check before each action.
- **Secrets:** OS-protected master key + encrypted store; helper stores in SYSTEM / root scope;
  operator-only migration between stores; one active host per identity.

## Test setup and order (user decision 2026-10-17)

All of Phases 2–9 are finished and tested on Windows first, then macOS gets one pass at the end.

- **This machine (Windows ARM64):** two instances with their own profiles (host and controller)
  for authentication, sessions, capture → viewer, input into other apps, the AI runtime and
  workspace, packaging.
- **A remote Windows 11 x64 VM** on another network, reached over SSH with a dedicated key (commands)
  and watched through its console or RustDesk (screens): native x64, hole punching across real
  NATs, clipboard and display differences, lock screen, UAC and Background service (console
  session, not RDP).
- **macOS pass:** ScreenCaptureKit, privacy permissions, VideoToolbox, LaunchDaemon, signing and
  updates, Mac ↔ Windows throughput.

## Milestones

### Phase 0 — Spikes (`spikes/`, results recorded in `doc/en-us/upstream.md` / `platform-notes.md`)

| Spike | Done when | Fallback |
|---|---|---|
| S4 providers | Claude: done (live two-turn round trip, fixtures saved). OpenAI: offline tests pass, live run waits for a key | adapter fixes before Phase 4 |
| S5 macOS | self-signed signing through `mygo build` (hardened runtime, timestamp), dylib loads, privacy grant survives an update, engine child attributed to Kyokai, Windows update with the DLL loaded | timestamp shim, upstream MyGo option, or ad hoc |

### Phase 1 — Foundation (0.1.0)

Implemented and verified on Windows ARM64 and x64 (doc/en-us/platform-notes.md). Remaining: the
first CI run on GitHub (after the push is approved), the macOS build and a real-Mac run (S5), and
the input-event mapping check of S2 with real keys.

### Later phases

| Phase | Scope | Done when |
|---|---|---|
| 2 Identity & networking | Done (2026-10-17): EasyTier in the engine, Network page, resolver, Noise + OPAQUE, host confirmation, trusted devices, permanent password, rate limits, hello with the source URL, sessions with heartbeat and close. Also done: identity backup / restore / reset, recent devices, session resume, decoder fuzz tests, a session to the remote x64 VM across networks. Left: the controller role in the GUI process and throughput (both with Phase 3's video channel); hole punching needs a non-symmetric NAT on one side (this machine and the VM are both symmetric, so traffic is relayed) | §26 Authentication, EasyTier Networking and Device Identity pass between this machine and the remote x64 VM through a standard server; throughput measured; the Mac pass later |
| 3 Manual Computer Use | codec decision, capture, video channel, viewer, input with focused capture, DPI / multi-monitor, clipboard, macOS permission guidance | §26 Manual Computer Use on two machines |
| 4 Host AI runtime | provider trait + OpenAI and Claude adapters and settings, task manager, state machine, observation, coordinates, executor, leases, policy, approvals, task history | §26 AI Computer Use and the host side of Authorization and Safety |
| 5 AI workspace | chat panel, remote tasks, progress, pause / resume / cancel / take over, controller approvals, host status, emergency stop, reconnect without replay | spec §27 steps 4–5 on two machines |
| 6 Run modes & helper | helper install / uninstall / status, session agents, Follow GUI, Background service, store migration, verified self-update, self-disable when the app is removed | UAC and lock screen operable in Follow GUI; Background admits only unattended grants; uninstall leaves nothing |
| 7 Security & reliability | threat model, fuzzing, revocation, lease cleanup, sleep / lock / display changes, privacy masking, resource limits, log audit | §26 Authorization and Safety complete |
| 8 Release path | tagged release, packages, checksums, app and helper updates from the previous version | §26 Build and Release |
| 9 Docs & acceptance | user guide, final README, third-party inventory, §27 acceptance with evidence | every §26 group recorded |

## RustDesk reuse per platform

User decision (2026-10-16): RustDesk's per-OS code is reused on macOS as well as Windows, not only
the DXGI capturer. Each module is vendored with its header and a "Modified by Kyokai" note.

| Area | Windows | macOS |
|---|---|---|
| Capture | `libs/scrap/src/dxgi/{mod,gdi,hdr,mag}.rs` (S3-lite) | New: ScreenCaptureKit only (`SCStream` for video, `SCScreenshotManager` for AI screenshots, `SCContentFilter` excludes Kyokai's windows). RustDesk's `quartz` capturer is not used: CGDisplayStream is obsoleted in the macOS 15 SDK, gives black frames on 15 / 26 and cannot exclude windows |
| Displays | `src/server/display_service.rs`, resolutions | same, plus `platform/macos.rs` resolutions, focused display, backing scale |
| Cursor | `platform/windows.rs` cursor shape / position | `platform/macos.rs` `get_cursor`, `get_cursor_data`, `set_cursor_pos`, `clip_cursor` |
| Input | `src/server/input_service.rs`, `libs/enigo` changes, `src/keyboard.rs` + rdev | same modules (macOS branches) |
| Permissions | — | `platform/macos.rs` / `macos.mm`: screen recording, accessibility, input monitoring checks and prompts |
| Session state | `platform/windows.rs` locked / logon desktop, `lock_screen` | `is_locked`, `is_prelogin`, `lock_screen`, active user |
| Privacy mode | `src/privacy_mode/win_*` (exclude from capture, input block) | `src/privacy_mode/macos.rs` |
| Helper (Phase 6) | `portable_service.rs`, SYSTEM service → session process, `windows/acl.rs` | LaunchDaemon / LaunchAgent plists and install / uninstall scripts in `platform/privileges_scripts` |

RustDesk's own update paths (`update_me`, DMG / MSI handling) are not used: MyGo's updater and the
helper's verified self-update replace them.

## Phase 3 quality, bandwidth and latency controls (RustDesk parity)

RustDesk's own controls, all to be offered; the host-side adaptation reuses RustDesk's QoS logic
(`src/server/video_qos.rs`, AGPL) where it fits:

- Image quality presets: Best quality, Balanced, Optimize reaction time, and Custom (quality and
  frame rate); a frame-rate cap (RustDesk: 30 by default, up to 120).
- Automatic adaptation: bitrate and frame rate follow measured delay and loss; key frame on loss;
  unchanged frames are skipped.
- Codec preference (Auto / each codec below) and a hardware-codec switch; full-colour 4:4:4 where
  the codec supports it.
- View: original size, scale to fit, full screen; remote cursor shown or not; monitor selection.
- Per-session: clipboard on/off and lock after the session ends (audio and file transfer later).
- Shown to the controller: latency, frame rate, bitrate, codec and direct-vs-relay path.

## Phase 3 manual control: reuse RustDesk's input stack

User decision (2026-10-16): RustDesk's manual-control systems are reused, decoupled from
`hbb_common` the way S3-lite decoupled the capturer (wire types become Kyokai's own protobuf):

- **Controller keyboard:** `src/keyboard.rs` — the grab loop on top of `rustdesk-org/rdev` (MIT;
  low-level hook while the viewer has focus), and the Map / Translate / Legacy keyboard modes with
  their numpad, lock-key and layout handling. This replaces MyGo's key events for the viewer (no
  scancodes, consumed shortcuts) instead of new hook code.
- **Host input:** `src/server/input_service.rs` — key events per mode, lock-mode (CapsLock /
  NumLock) sync, mouse with virtual-desktop coordinates, release of held keys and buttons, cursor
  shape and position services (the remote cursor), window-focus tracking; injection through
  RustDesk's `libs/enigo` changes where upstream `enigo` 0.6.1 lacks them.
- **Session actions** from `src/client.rs` / `src/ui_session_interface.rs`: send Ctrl+Alt+Del (works
  only with the helper, Phase 6), lock the remote screen, block the host's local input (only while a
  human controls, never against the host user's take-over), show the remote cursor, keyboard mode
  choice.
- Each vendored file keeps its header, gains a "Modified by Kyokai" note and an entry in
  `doc/en-us/third-party.md`.

## Phase 3 codecs: every mainstream codec, hardware where available

User decision (2026-10-16). RustDesk's `hwcodec` carries no license, so the hardware path uses the
OS APIs (whose vendors license H.264 / H.265):

| Codec | Implementation |
|---|---|
| VP8 / VP9 | software libvpx, RustDesk's `vpxcodec.rs` / `vpx.rs` |
| AV1 | software libaom (RustDesk's `aom.rs`); hardware where the device has it |
| H.264 / H.265 | hardware through Media Foundation (Windows; NVIDIA / AMD / Intel / Qualcomm MFTs) and VideoToolbox (macOS) |

Host and controller exchange capability lists and use the best common codec; without hardware
(e.g. Windows N editions) they fall back to VP9.

RustDesk's QoS stays codec-independent: frame rate is paced by the capture loop and unchanged frames
are skipped before encoding; each encoder implements RustDesk's `EncoderApi` contract (`set_quality`
ratio → bitrate while running, `bitrate`, forced key frame, low-latency mode). Media Foundation
(`CODECAPI_AVEncCommonMeanBitRate`, `AVEncVideoForceKeyFrame`, `AVLowLatencyMode`) and
VideoToolbox (`AverageBitRate`, `ForceKeyFrame`, `RealTime`) provide these; an encoder that ignores
a live bitrate change is recreated. Full-colour 4:4:4 stays a software VP9 / AV1 option, as in
RustDesk. Done when each encoder follows the QoS on two machines.

## Decided (2026-10-16)

- No EasyTier listeners by default (outbound + hole punching, no firewall or Local Network
  prompt); a listener option only if the two-machine test shows LAN paths need it — Phase 2.
- Hole punching through real NATs is tested with the two machines on different networks — Phase 2.
- Privilege separation of the network-facing host role in helper modes — decided in Phase 6.
- macOS timestamping with the self-signed identity — tested in S5; if Apple's service refuses,
  MyGo gains an option to skip the timestamp.
- The `easytier-go` spike is dropped; Windows 7 is not supported.
- Video inside the EasyTier tunnel: `data_plane_tcp` (smoltcp, no congestion control) first; if
  real-link latency is poor, `data_plane_udp` with KCP like RustDesk's direct path — Phase 2.
