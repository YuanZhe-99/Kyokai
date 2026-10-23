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

## Next version: the interface

Decided with the maintainer on 2026-10-23 from a design draft of ten screens (light and dark,
Windows and macOS chrome; a private canvas the maintainer keeps). Kyokai's own look, not a copy of
another app's: other apps were references for behaviour only.

- **Shell.** The sidebar is drawn on the window's material (Mica on Windows, the sidebar material
  on macOS; `WindowOptions.Vibrancy`); the content floats on it as rounded panes 8 px from the
  edges. The tab strip goes. The sidebar lists Home and every open pane; a split shows as one group,
  its panes indented under the group's name, the group a card while it is in front; a pane dragged
  out of a group leaves it. Top: hide the sidebar, and + (the picker below). Bottom: search,
  light/dark (a sun, a moon while dark), the settings gear, the network state. Hidden, the sidebar
  folds to an icon rail with tooltips.
- **Panes.** Home, Desktop, SSH, Remote Desktop, VNC and AI task panes share one header: icon,
  device name, a second line (Device ID, "SSH · user", "AI task"), route and delay, then split
  right, split down, maximize or restore, and close; a narrow pane folds the first three into ⋯.
  Windows' caption buttons end the top-right pane's header; macOS' traffic lights sit at the top of
  the sidebar or rail. The pane in focus has an accent edge. Splits use the existing pane tree
  (`split`, four panes at most). A single pane dragged out gets a window of its own without a
  sidebar: the pane alone, its header holding the window's controls (the traffic lights at its
  start on macOS); closing that window disconnects it. The viewer's floating toolbar stays as it
  is (RustDesk parity).
- **+ picker.** A popover under +: the kind first (Desktop, SSH, Remote Desktop, VNC, AI task),
  then the device (a search by name, Device ID or address). Devices that do not offer the kind are
  greyed out with the reason; a device without status is not; AI task adds This computer; devices
  the joined networks route to and that were never connected come last with a dashed icon. Last,
  where it opens: New window (its own sidebar entry) or Beside current (a split). A pane header's
  split buttons open the same picker.
- **Search (Ctrl+K).** A palette over the window: devices, open panes, settings rows and actions,
  matched as typed and grouped; Enter opens the first (or copies, for the password).
- **Settings.** The top layer: a sheet over the whole window, the sidebar too, opened only by the
  gear or search, never part of a split, closed with × or Esc. Grouped navigation with a search
  field: Preferences (General), This computer (Access, Network, Security & privacy, Run mode), AI
  (Providers), Kyokai (About & updates). A row is a title, one short sentence and its control on
  the right; longer explanations move behind "How … works" links; the permissions a password
  grants are tiles that toggle; a problem says what to do next (Fix…).
- **Home.** This device (Device ID; the password with show, copy and new) and Connect (an ID or
  address, Open as Desktop, SSH, Remote Desktop, VNC or AI task). Devices as cards of equal height
  with one row of compact metrics, Open while connected, Connect otherwise, services as icon
  buttons. "Others in my networks" (off by default, kept in `gui.json`) adds the Kyokai devices
  the joined networks route to and that were never connected, as dashed cards with Connect.
- **AI.** An AI task is a pane per device (this computer or a host): the conversation (the
  instruction, the model's text, its steps — a thumbnail where it took a screenshot — and
  approvals inline with Allow and Deny) and the prompt box at the bottom; under the box the
  approval mode, where it runs and the model with whose key ("NUC's key"). A new task's pane
  centres the box, with suggestions and recent tasks; Watch its screen opens the device's Desktop
  beside it. Providers, keys, the default model and the default approval mode move to Settings ›
  AI; the AI page goes. A send-key preference: Enter sends and Shift+Enter breaks the line (the
  default), the reverse, or both break and only the button sends.

| Milestone | Scope | Done when |
|---|---|---|
| M0 Spikes | the material behind a transparent page with floating panes (light, dark; Windows 11, macOS); MyGo's Windows caption buttons (46 × `TitleBarHeight`, snap layouts) at the end of the top-right pane's header — placed, or hidden (`HideWindowButtons`) for our own, or a MyGo change; a live picture clipped to a rounded pane without a cost on the video path; the keyboard grab stepping aside while a sheet, popover or palette is open | each answered on both platforms, recorded in platform-notes |
| M1 Shell | the sidebar (Home, open panes and groups, the bottom row, the rail), pane headers and focus, no tab strip, groups on the split tree; a single pane dragged out into a window of its own without a sidebar; tray, close and quit rules unchanged | view tests render 0, 1, 3 and 4 panes in both themes and four languages; the native app seen on Windows and macOS; gui.md rewritten |
| M2 Settings | the sheet, grouped navigation, settings search, the row style and permission tiles; every current setting moved (General; Permissions as Access; Network; Security as Security & privacy; Run mode; AI providers; About), the copy shortened | every setting reachable in the sheet and by search; no settings page left in the sidebar |
| M3 Home | This device, Connect with Open as, cards of equal height, "Others in my networks", the status details as a pane | view tests with the network devices on and off |
| M4 + and search | the picker (kinds, what each device offers, where it opens) and the Ctrl+K palette | both usable by keyboard alone; tests for the matching and the offers |
| M5 AI | the AI task pane (this computer and a host), inline approvals, the prompt box and send-key preference, recent tasks; the AI page removed. Protocol: `ai-start` takes an optional model and approval mode, and a host lists its usable models to `run-ai-task` controllers, behind a feature token (older hosts: the pickers hidden, the host's choice used) | a task started, steered, approved and finished from both kinds of pane on two machines; protocol tests with an older peer |
| M6 The rest | connection requests and "Connected here", the AI approval banners, protected mode, Starting… and the engine down, macOS privacy permissions, the address and rename editors — in the new shell | each state in view tests and seen once natively |
| M7 Finish | contrast and keyboard navigation; gui.md, user-guide.md and computer-use.md in both languages; version-history and platform-notes | the full gate; native screenshots on both platforms; the maintainer's review |

Open decisions:

- The version: one release (0.6.0?) when M7 is done, the milestones landing on `main` unreleased?
- Connection requests: a card above every pane, as today, or the top layer like Settings?
- Light/dark in the sidebar: switch between Light and Dark, System staying in Settings?
- Recent tasks: this computer's history only, or also tasks it started or watched elsewhere?
- Sessions: one AI task per host, as a host keeps one AI record today, or several per host (a
  protocol change)?

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

- The interface (2026-10-23): everything is a pane the sidebar holds; Settings is the top layer
  over the whole window and never part of a split; AI tasks are panes of their own, apart from the
  providers in Settings › AI; a single pane dragged out becomes a window of its own without a
  sidebar; the sidebar keeps search, light/dark, the gear and the network state at its bottom
  (Kyokai has no user accounts, so nothing account-like goes there).
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
