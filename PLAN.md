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

### M0 — Scaffold

Conventions, docs skeleton, tools (`checkdocs`, `deps`, `sign`), Go module, `mygo.json`, version,
Rust workspace with `kyokai-core`, signing material generated and recorded offline, secret scan.

### Phase 0 — Spikes (`spikes/`, results recorded in `doc/en-us/upstream.md` / `platform-notes.md`)

| Spike | Done when | Fallback |
|---|---|---|
| S2 viewer | 1920×1080 RGBA frames from Rust shown through purego + `ui.Bitmap` beside a 500-item list at ≥ 55 fps, GUI ≤ 25 % of one core on the Windows ARM64 dev machine; input events mapped to HID usages; no VC++ runtime dependency | separate viewer window, 30 fps cap, or the shader path |
| S1 EasyTier | two embedded nodes + an official core: routes carry `inst_id`/hostname, data-plane both ways, whitelist holds, duplicate `inst_id` behavior known, clean restart, minimal features build on ARM64 and x64 | sidecar core with `no_tun` + whitelist |
| S3-lite capture | the DXGI capturer feeds the S2 viewer at 1080p; CPU and latency measured | `windows-capture` |
| S4 providers | OpenAI and Claude two-turn round trips accepted with synthetic screenshots; fixtures saved | adapter fixes before Phase 4 |
| S5 macOS | self-signed signing through `mygo build` (hardened runtime, timestamp), dylib loads, privacy grant survives an update, engine child attributed to Kyokai, Windows update with the DLL loaded | timestamp shim, upstream MyGo option, or ad hoc |

### Phase 1 — Foundation (0.1.0)

Rust core (state dir, secrets store, identity, temporary password, scrubbed logs, events), the
engine with lifeline and local API v1 (+ `ctl`), the GUI shell (single instance with a dev profile
override, tray, close-to-tray, notifications, theme, four languages), Home (Device ID, temporary
password), Settings (General with Open at login off by default, Storage, Diagnostics, About with
updates and source link, Open Source Licenses), CI and release workflows, local packages.

Done when: the gate is green on Windows ARM64, the x64 build runs under emulation, the engine starts
without a prompt, the Device ID survives restarts, a new temporary password appears each start,
quitting or killing the GUI ends the engine, nothing is registered for autostart, and the installer
and portable zip build.

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

## Open decisions

- Video codec (OS-native H.264 vs libvpx VP9) — start of Phase 3.
- EasyTier listeners vs. hole punching only (firewall and Local Network prompts) — S1 / Phase 2.
- Video over smoltcp TCP vs. a UDP data plane with Kyokai congestion control — Phase 2 measurements.
- Privilege separation of the network-facing host role in helper modes — Phase 6.
- macOS timestamping with the self-signed identity — S5.
