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

- **Processes:** GUI (`Kyokai`, MyGo; decoding, display and input capture in-process from Phase 3) · engine
  (`kyokai-svc run --host user`, child of the GUI; EasyTier, device identity, host role, local API)
  · helper (`--host helper`, SYSTEM / root, Phase 6) · session agents (Phase 6). The GUI never
  runs elevated; privileged processes never decode remote media.
- **FFI:** C ABI with integers and pointers only; JSON calls and a blocking event poll; zero-copy
  RGBA frame slots in the GUI; encoded media and input events pass engine ↔ GUI over a streaming
  local API connection (Phase 3 breakdown).
- **Identity:** Ed25519 device key; 9-digit Device ID from its hash (locates, never authenticates);
  keys pinned after the first authentication; "new key claiming this ID" shown differently.
- **Authentication:** Noise XX + OPAQUE (temporary and permanent password slots in one round trip,
  bound to the Noise handshake); rate limits per overlay address and a global backoff that exempts
  pinned keys; host confirmation by default; unattended access only with a pinned key, the permanent
  password and an expiring grant.
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
| S4 providers | Claude: done (live two-turn round trip, fixtures saved). OpenAI: offline tests pass; the live run is deferred until the user has a key (decided 2026-10-17) | adapter fixes before Phase 4 |
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
| 4 Host AI runtime | Done on Windows (2026-10-17): OpenAI and Claude adapters, HTTPS client, task runtime and state machine, screenshots and coordinates, executor over the input service, input lease with local-input pause, approval policy, task history, AI page, tray stop, Kyokai windows excluded from capture; a live Claude task on synthetic screens passed. Done on the VM's real desktop (2026-10-17). Left: the OpenAI live run (deferred: no key yet), macOS (Mac pass) | §26 AI Computer Use and the host side of Authorization and Safety |
| 5 AI workspace | Done in code (2026-10-17): the viewer's AI panel, tasks a controller starts and steers (protocol 2), progress pushed to the controller, pause / resume / cancel / reply, take-over by remote input, controller approvals, the host's banner with Stop, emergency stop (tray, Ctrl+Alt+Shift+Esc), no replay after a reconnect; session tests pass. Left: the run between this machine and the VM's interactive desktop | spec §27 steps 4–5 on two machines |
| 6 Run modes & helper | Done in code on Windows (2026-10-17): helper install / uninstall / status with operator and group authorization, the session agent (SYSTEM in the console session, following the input desktop), unattended grants and whom the helper serves, the run-mode switch with the store move (run between two user engines), signed read-once self-update, self-disable. Left: running all of it as a service (administrator install on this machine and the VM), the `KYOKAI_SIGNING_KEY` secret, the macOS helper (Mac pass) | UAC and lock screen operable in Follow GUI; Background admits only unattended grants; uninstall leaves nothing |
| 7 Security & reliability | Done so far (2026-10-17): threat model (security.md), provider-answer fuzzing, revocation (ends live sessions and resume tokens), lease cleanup (a controller's task pauses when its session ends), log audit, bounded queues, caps on incoming connections (16) and connected host sessions (4). Capture reopens the display after a mode change, the lock screen or a UAC prompt (not yet seen happen). Sleep (awake while connected, AI paused at sleep) and privacy mode done in code. Left: verifying the display reopening on the VM, a real sleep and wake, helper and agents in the threat model (Phase 6) | §26 Authorization and Safety complete |
| 8 Release path | tagged release, packages, checksums, app and helper updates from the previous version | §26 Build and Release |
| 9 Docs & acceptance | User guide, README, the third-party review and the acceptance record (doc/en-us/acceptance.md) done (2026-10-17). Left: the README at release, the open rows of the acceptance record | every §26 group recorded |

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

## Phase 3 breakdown (decided 2026-10-17)

Architecture change from the original design: the controller's **session stays in the engine**
(network, Noise, OPAQUE, keys, as built in Phase 2). The GUI loads the core only to **decode,
display and capture input**: the engine forwards encoded video to the GUI over a streaming local
API connection and takes input events back. Decoded frames never cross processes, no delegated
certificates are needed, and privileged processes (Phase 6) relay encoded bytes only.

| Step | Scope | Done when |
|---|---|---|
| 3.1 Video channel (done) | binary session messages (chunked frames, key-frame requests, latest-frame-wins), engine ↔ GUI media stream over the local API, the GUI-side core | a synthetic test pattern from the host shows in the controller's viewer |
| 3.2 Capture (done; the GDI fallback is still to be seen on a machine without desktop duplication) | RustDesk DXGI + GDI fallback (from S3-lite) in the core, capture thread, monitor choice; the GUI excludes its own windows (`WDA_EXCLUDEFROMCAPTURE`) | the host's screen at 1080p / 30+ fps in the viewer |
| 3.3 First codec (software and hardware H.264 done; the VM measurement left) | Media Foundation H.264: a hardware encoder when present, Microsoft's software MFT otherwise; decode in the GUI | 1080p60 over the VM path; latency and bitrate shown |
| 3.4 Viewer | its own window, fit / original size / full screen, stats, Disconnect | §26 viewing items on Windows |
| 3.5 Input (done; the real run on the VM's desktop is left) | mouse and keyboard to the host only with `control-input`, virtual-desktop coordinates, nothing into Kyokai's own windows, held keys released | typing and clicking into apps on the VM |
| 3.6 QoS (done) | RustDesk's `video_qos.rs`, delay probes, bitrate / frame-rate adaptation, presets | adapts on the VM path; unchanged frames skipped |
| 3.7 Codecs (negotiation and Media Foundation's HEVC and VP9 done; H.264 preferred because the GPU's HEVC did not decode at 720p and above; libvpx / libaom and 4:4:4 left) | VP8 / VP9 (libvpx), AV1 (libaom), HEVC through Media Foundation, negotiation, 4:4:4 | every codec the plan lists, chosen by capability |
| 3.8 Manual control (clipboard text, images and files, the remote pointer and display choice done; keyboard modes left) | RustDesk keyboard modes with rdev, clipboard (arboard), multi-monitor and DPI | §26 Manual Computer Use on Windows |

Testing: this machine as controller, the remote VM as host. Capture needs the host engine in the
VM's interactive desktop session, not the SSH session.

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
- **Session actions** (done in code, protocol 3: lock, Ctrl+Alt+Del in the helper modes, block the
  host's input; the remote cursor was done before). Left: running lock, block and Ctrl+Alt+Del on
  a real host (they act on that desktop), and the keyboard mode choice with the keyboard modes.
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

## Phase 6 breakdown (approved by the user 2026-10-17)

The helper comes from Easytier-MyGo's `internal/helper` and `internal/bundlesig` (install,
uninstall, status, protected copies, read-once verified self-update); the session agents from
RustDesk's Windows service and portable service (SYSTEM token in the console session) and its
macOS LaunchDaemon / LaunchAgent scripts.

Implementation design (Windows now; macOS in the Mac pass, where the helper returns "not available
yet" until then):

- **Helper (6.1):** `internal/helper` from Easytier-MyGo. `kyokai-svc helper install` runs elevated
  (the GUI starts it with a UAC prompt), copies `kyokai-svc.exe` and `kyokai_core.dll` into
  `%ProgramFiles%\Kyokai\helper` (administrator-owned), creates `%ProgramData%\Kyokai\state`
  (SYSTEM and Administrators only, Users may read the folder holding the socket), records the
  operator SID and registers the automatic service `KyokaiHelper` running
  `kyokai-svc run --host helper`. `uninstall` stops and deletes it and its files; `status` needs no
  rights. The helper's socket admits the operator and members of a local `kyokai` group (when an
  administrator created it); everyone else is refused. Its secrets use DPAPI as SYSTEM.
- **Session agent (6.2):** the helper starts `kyokai-svc agent` in the active console session with a
  copy of its SYSTEM token (`WTSGetActiveConsoleSessionId`, `DuplicateTokenEx`,
  `SetTokenInformation(TokenSessionId)`, `CreateProcessAsUserW` on `winsta0\default`), restarts it on
  session changes, and talks to it over a socket only SYSTEM may open. The agent follows the input
  desktop (`OpenInputDesktop`/`SetThreadDesktop`, so the UAC secure desktop and the lock screen),
  captures and encodes, injects input, reads the clipboard and the pointer, and answers the AI's
  desktop calls; only encoded video, input events and screenshots for the AI cross. The core's host
  side gets one desktop interface with two implementations: in-process (No privileges) and the
  agent (helper modes).
- **Run modes (6.3):** Settings, Run mode: No privileges / Follow GUI / Background service. The GUI
  connects to the helper's socket in helper modes and to its child engine otherwise; Follow GUI
  serves sessions only while a GUI is attached, Background always (unattended grants only before
  login). Switching moves the secrets store with one operator-only call and deletes the source after
  the destination confirms; only one engine holds an identity at a time.
- **Updates and self-disable (6.4, 6.5):** release bundles carry `bundle.json` and `bundle.sig`
  (`internal/bundlesig`, Ed25519, `release.pub`); the helper reads each file once, verifies and writes
  those bytes; unsigned builds ask for administrator rights. The helper stops and removes its service
  when `%LOCALAPPDATA%\Programs\Kyokai` no longer holds the app.

| Step | Scope | Done when |
|---|---|---|
| 6.1 Helper | `kyokai-svc helper install / uninstall / status`: protected copies, a SYSTEM service (Windows) or root LaunchDaemon (macOS), the helper's local API socket for the operator and the `kyokai` group | one administrator prompt installs it; status shows it; uninstall leaves nothing |
| 6.2 Session agent | `kyokai-svc agent`, started by the helper in the active console session with a SYSTEM token: captures the input desktop (also the UAC secure desktop and the lock screen) and injects there; talks to the helper over a local pipe; encoded video only crosses | a UAC prompt and the lock screen show in the viewer and take input |
| 6.3 Run modes | Settings: No privileges / Follow GUI / Background service; the engine runs in the helper; the secrets store moves by one operator-only call; Background admits only unattended grants | switching modes keeps the Device ID and trusted devices; Background serves before login |
| 6.4 Updates | the helper verifies a signed bundle read once and installs those bytes; macOS by code-signing requirement | an update from the previous build installs without a new prompt (signed) |
| 6.5 Self-disable | the helper stops itself when the app is removed | uninstalling the app leaves no running helper |

Needs from the user: approval to install the helper (an administrator prompt) on this machine and
on the VM for each test, and to uninstall it afterwards; the VM's interactive session for 6.2.

## Decided (2026-10-16)

- No EasyTier listeners by default. Hole punching turned out to make Windows Firewall ask (its UDP
  sockets wait for unsolicited packets), so by user decision (2026-10-17) direct paths are off in No
  privileges unless "Try direct connections" is on, and always on in the helper, whose installer
  adds a firewall rule.
- Hole punching through real NATs is tested with the two machines on different networks — Phase 2.
- Privilege separation of the network-facing host role in helper modes — decided in Phase 6.
- macOS timestamping with the self-signed identity — tested in S5; if Apple's service refuses,
  MyGo gains an option to skip the timestamp.
- The `easytier-go` spike is dropped; Windows 7 is not supported.
- Video inside the EasyTier tunnel: `data_plane_tcp` (smoltcp, no congestion control) first; if
  real-link latency is poor, `data_plane_udp` with KCP like RustDesk's direct path — Phase 2.
