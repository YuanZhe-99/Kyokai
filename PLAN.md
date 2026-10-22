# PLAN.md

The active plan: what is left, with done-when criteria and open decisions. Long-lived contracts
live in `doc/`; what is implemented and verified per platform is in
`doc/en-us/platform-notes.md` and `doc/en-us/acceptance.md`. Delete items as they are done; delete
this file when the plan is complete.

## Decisions in force

- Platforms: Windows x64 + ARM64, macOS arm64 + x64 (macOS 14+). Linux is not supported yet.
- AI runs only on the host; providers from templates (Claude, OpenAI and OpenAI-compatible).
- EasyTier v2.7 line (in-process data plane), pinned by commit, minimal feature subset, following
  every upstream release; direct paths off in No privileges unless the person turns them on.
- macOS builds are signed with the maintainer's Apple Development certificate (Team ID); the helper
  verifies its own updates (bundle signature on Windows, code signature on macOS).

## Left

| Item | Scope | Done when |
|---|---|---|
| Shared AI record and draft | the host keeps one AI record that every controller sees and may steer with its permissions (done in the core); the AI prompt draft is shared live: what one controller or the host's user types reaches the host, which passes it to every other controller and its own AI page | two controllers and the host see the same task and the same draft, recorded in platform-notes |
| macOS crash | the Mac app "crashed" when a connection request came in: most likely the GUI froze, announcing the request under its lock while macOS asked about notifications (fixed in 0.4.0, with a watchdog that writes the stacks of a frozen GUI to `gui-crash.log`) | the first connection to a Mac host no longer freezes on the user's Macs, or the new logs name another cause |
| macOS pass | done on one Mac (platform-notes): capture, permissions, input and keyboard as host, Follow Kyokai. Left: the clipboard, Background service and the login-window agent, the helper's self-disable, sleep notifications, a Mac as controller of another machine through the viewer window, a Mac host from another machine | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | Custom quality (quality and frame rate); a hardware-codec switch; lock after the session ends | each control in the viewer, carried by the protocol where the host decides |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup; on the Windows computer: the pointer ring with no mouse attached, H.264 decoded on the GPU and Media Foundation's reused samples (the encode and decode times in the viewer's tooltip, both directions) | recorded in platform-notes |

## Decided

- 0.5.0 (2026-10-21): a device has routes — the Kyokai network and any number of addresses; Auto picks by latency now and throughput remembered per route, at connect and on resume only (a healthy session never moves); a route can be fixed by hand. Addresses are edited from the card (⋯ → Addresses…), its path line is a route picker, and the connect form adds addresses. KCP/QUIC switches stay out (EasyTier's proxies never carry Kyokai's sessions; `quic://` server URLs already work).
- 0.4.1 (2026-10-21): SSH, RDP and VNC run in built-in clients through tunnels inside Kyokai sessions to the host's own ports, each a permission like the others, off by default; saved service passwords stay in the engine's store and reach the GUI's client only at sign-in, private keys never leave the engine. A controller with a saved password or key watches a host's status quietly. KCP/QUIC proxies are not used (they never carry Kyokai's sessions). Home is a grid of rich device cards. Remote control is a stream: stale frames and input are skipped, key state stays in step, a stalled session never traps the controller's keyboard.
- Kyokai's windows follow Hide Kyokai from capture: with it off, a person controlling this device
  may use them too (never the host's AI, nobody while a connection request or an AI approval
  waits); with it on, as before, neither sees nor reaches them (2026-10-19).
- The viewer's controls are one floating toolbar over the picture, as RustDesk's; folded, its handle
  drags along the top edge and keeps its place (2026-10-19).
- Breaking wire changes are allowed only until 0.2.0 is released; from then on a new version keeps talking
  to the previous ones (see Protocol compatibility) (2026-10-18).

- A correct password admits without asking within the password grants chosen in Settings (default: view, control, clipboard, files); a controller may also ask without a password, which the host's user allows or denies, as RustDesk does (2026-10-17).
- Hole punching makes Windows Firewall ask, so direct paths are off by default in No privileges
  and always on in the helper, whose installer adds a firewall rule (2026-10-17).
- The services policy for Ctrl+Alt+Del is switched on only for the call.
- The macOS helper is installed like EasyTier MyGo's (one `osascript` administrator prompt) and
  removes itself when switched off (no prompt); its agent is the bundle Kyokai Helper, whose own
  privacy grants count (2026-10-17).
- The macOS Keychain stays the secrets' key store; the per-build Keychain prompt is removed by
  signing with a Team ID (the maintainer's free Apple Development certificate), not by storing
  the key elsewhere (2026-10-17).
- HEVC decodes on the GPU only (the software decoder never outputs the uneven-tile streams of one
  hardware encoder); a host with a free hardware HEVC encoder prefers HEVC.
- macOS timestamping with the self-signed identity works (the CI image check, 2026-10-17); the
  apps are not notarized, so a downloaded copy needs its quarantine removed (the user guide says how).
