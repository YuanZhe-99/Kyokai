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
- **Desktop:** RustDesk's DXGI capturer (decoupled from `hbb_common`) on Windows, ScreenCaptureKit
  on macOS; enigo + RustDesk's virtual-desktop logic for input; arboard for the clipboard.
- **AI:** append-only Claude history (server-side clearing, never client pruning), per-provider
  screenshot scaling, ordered batches with an approval check before each action.
- **Secrets:** OS-protected master key + encrypted store; helper stores in SYSTEM / root scope;
  operator-only migration between stores; one active host per identity.

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
| 2 Identity & networking | EasyTier in the engine, Server / Network page, resolver, Noise + OPAQUE, host confirmation, trusted devices page, identity backup / restore / reset, recent devices, rate limits, session control and resume, hello with the source URL, decoder fuzzing, GUI-side binding | spec §26 Authentication, EasyTier Networking and Device Identity pass between Windows ARM64 and a Mac through a standard server; throughput measured |
| 3 Manual Computer Use | codec decision, capture, video channel, viewer, input with focused capture, DPI / multi-monitor, clipboard, macOS permission guidance | §26 Manual Computer Use on two machines |
| 4 Host AI runtime | provider trait + OpenAI and Claude adapters and settings, task manager, state machine, observation, coordinates, executor, leases, policy, approvals, task history | §26 AI Computer Use and the host side of Authorization and Safety |
| 5 AI workspace | chat panel, remote tasks, progress, pause / resume / cancel / take over, controller approvals, host status, emergency stop, reconnect without replay | spec §27 steps 4–5 on two machines |
| 6 Run modes & helper | helper install / uninstall / status, session agents, Follow GUI, Background service, store migration, verified self-update, self-disable when the app is removed | UAC and lock screen operable in Follow GUI; Background admits only unattended grants; uninstall leaves nothing |
| 7 Security & reliability | threat model, fuzzing, revocation, lease cleanup, sleep / lock / display changes, privacy masking, resource limits, log audit | §26 Authorization and Safety complete |
| 8 Release path | tagged release, packages, checksums, app and helper updates from the previous version | §26 Build and Release |
| 9 Docs & acceptance | user guide, final README, third-party inventory, §27 acceptance with evidence | every §26 group recorded |

## Phase 3 quality, bandwidth and latency controls (RustDesk parity)

RustDesk's own controls, all to be offered; the host-side adaptation reuses RustDesk's QoS logic
(`src/server/video_qos.rs`, AGPL) where it fits:

- Image quality presets: Best quality, Balanced, Optimize reaction time, and Custom (quality and
  frame rate); a frame-rate cap (RustDesk: 30 by default, up to 120).
- Automatic adaptation: bitrate and frame rate follow measured delay and loss; key frame on loss;
  unchanged frames are skipped.
- Codec preference (Auto / the codecs Kyokai ships) and a hardware-codec switch; full-colour 4:4:4
  where the codec supports it.
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

## Open decisions

- EasyTier on x64 Windows relinks against `msvcrt.dll` through `thunk-rs` (VC-LTL), against the
  plan's static CRT; pin `PROTOC` to a vetted binary — before Phase 2.
- Hole punching through real NATs (S1 could not test it) — Phase 2 with the Mac.
- Which EasyTier feature brings back TXT DNS resolution for STUN (missing in the minimal set) —
  before Phase 2.
- Worth a spike: EasyTier v2.7's `easytier-go` (the wasm core inside a pure-Go process).
- Video codec (OS-native H.264 vs libvpx VP9) — start of Phase 3.
- EasyTier listeners vs. hole punching only (firewall and Local Network prompts) — S1 / Phase 2.
- Video over smoltcp TCP vs. a UDP data plane with Kyokai congestion control — Phase 2 measurements.
- Privilege separation of the network-facing host role in helper modes — Phase 6.
- macOS timestamping with the self-signed identity — S5.
