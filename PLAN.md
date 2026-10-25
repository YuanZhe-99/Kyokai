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

## 0.7.0

The AI update.

| Item | Scope | Done when |
|---|---|---|
| Pi as the agent backend | AI sessions run as Kyokai's providers → Pi → computer use. Pi (now `earendil-works/pi`, npm `@earendil-works/*`, MIT) is the agent loop on the host, in a sidecar Kyokai builds and signs per platform (`bun build --compile`), on `pi-coding-agent`'s SDK in its full-control setup: settings in memory, Kyokai's own resource loader, only Kyokai's actions as tools (one at a time), Pi's coding tools, code mode and MCP off. Not `pi-agent-core` alone (compaction, the session tree and the commands live in `pi-coding-agent`) and not the `pi` RPC mode (unversioned, one session per process, no tool results). The engine and the sidecar speak Kyokai's own versioned protocol (start, steer, follow-up, abort, action ⇄ result, key requests); keys answer `getApiKey` over that pipe, never through the environment, arguments or files; no model catalog fetch, attribution off, cache warming off, image resizing off. It brings several sessions per host (fork, clone, resume, name, export), compact (by hand and automatic) and model and thinking choices to the GUI, for the host user and for controllers under their permissions; the AI task history is kept, screenshots stripped, unless the session is empty, and only in memory under No logs. The providers' native computer-use tools must survive (Claude's `computer_toolset_20260801` with server-side clearing, OpenAI's `computer` tool with `pending_safety_checks`): a provider moves to Pi only when the spike keeps its native path. Pi and Bun are pinned exactly, in `tools/deps`. With it, a host's model and approval mode chosen per task from the prompt box (`ai-start` with an optional model and approval mode, a host listing its usable models to `run-ai-task` controllers, behind a feature token) | a spike answers each point — the native tools through the payload hook or a custom stream, screenshots reaching the provider byte for byte, abort during a tool and at the first failure, no file written and no connection but the provider's, start-up and memory on the four targets, nothing resuming after the engine is killed; then a task runs through Pi on a host with approvals and take-over intact, on both platforms |
| Auto approval mode | two approval modes, Every action and Auto; no global "ask nothing" and no "always allow": nothing an approval grants outlives the one action it was shown. Auto decides refuse / ask / run from facts the system gives about the target — the process and window under the action, the control's type and ID, password fields, key combinations, file dialogs — and on-screen words only within a known app; an AI reviewer, if one is ever added, may only add asks. Classes: 0 secure surfaces (Decided), power and session, network changes, deleting, uploading, password vaults, installing and elevation, security settings, terminals typed into, process control, sending, payments, consent dialogs, Remote Desktop and VNC windows (no element tree); elevation and typing credentials are rows that never loosen. Asks are rare, specific and worded by Kyokai, each tied to the exact action and screenshot; repeated refusals fall back to Every action. Open: who may widen a scope (the host user only, controllers with `approve-ai-action`, or a new permission) | table tests of the classes and the decision order; a live run of each class on Windows and macOS, recorded in platform-notes |
| AI scope: apps, windows and folders | the host user grants apps or windows and folders per session, neither implying the other; what the AI sees dims everything outside the grant (macOS: from ScreenCaptureKit's window list; Windows: from the z-order), and an action on a covered granted window is refused; each event's target (the window under the point, the focused window) is checked against the grant right before injection, app switching refused; dialogs of the granted app's own process follow it, other processes' pickers, notifications and UAC ask; folders are checked, not enforced — the current folder of Explorer, Finder and file dialogs, typed paths, and changes seen while the AI holds the lease — and the interface says so (enforcing needs a separate account, container or VM). The AI may work in a granted window while the host user works in another (decided); open: how — input that does not move the user's pointer or focus, and what "local input takes over at once" becomes then | each grant and refusal in table tests; the scope held on Windows and macOS in a live run, recorded in platform-notes |
| AI token efficiency | Claude: prompt caching (the 1-hour cache, which outlasts approvals and pauses); `openai-chat`: old screenshots dropped in batches with a one-line note per older step instead of a rolling newest 3, which changes the cached prefix every turn; OpenAI: `cached_tokens` checked, `truncation: "auto"` (which drops history without saying so) replaced by compaction or a loud failure | the tokens and cost of a fixed task before and after, recorded in platform-notes |
| Coordinate modes | per model or source: pixels, 0–999, 0–1000, smart-resize 28 or 32, with the matching prompt — an answer on a 0–999 grid passes today's bounds check and clicks elsewhere | a Qwen3-VL-family model and Gemini through `openai-chat` click where they mean, in fixtures and one live run |
| AI structure | the AI part kept clean: Kyokai owns the action schema, the executor (the only thing that acts), the policy engine (allow / ask / deny with a reason, from the action, its target's facts, the session's grants and the class table), observation (capture and the scope's blackout), the task and lease state, and the history (an event log projected into the task record); the agent loop is replaceable behind the versioned sidecar protocol (Rust today, Pi later); provider adapters only translate; no extensions and no placeholder abstractions. Steps, each shippable: the policy engine with class 0; Claude caching and batched pruning; app and window scope; folder checks; the Pi spike, then sessions and compact; the task history store | each step lands with its docs and tests; architecture.md describes the result |

## Left

| Item | Scope | Done when |
|---|---|---|
| 0.6.3 seen on the Mac | done on Windows (platform-notes): the codec wait's regression test against a real host, Reconnect's view test, the lease and pace tests and a watcher changing pace on one session | on the maintainer's Mac against the Mac mini: after the display slept or the host recovered, the picture comes back in the chosen codec and never stays on "can't show the picture"; Reconnect over a 0.6.2 host stuck there; Home's figures current again on coming back from a pane or Settings, without "Getting its status…"; recorded in platform-notes |
| 0.6.2 seen on the Mac | released on its tests, a grab, its renewal and its state through the real core on Windows, and the Mac code type-checked from Windows (not run): closing to the tray, the grab in the whole pane, keys that got past it, renewing it, the macOS tap's start and stop, the two dots and the log line | on the maintainer's Mac: closing with two connections leaves Kyokai in the tray; Map and Translate send Enter, Delete and Command after switching between two connections, with the pointer over the toolbar, and after Kyokai went to the background and came back; the dots green, amber with the reason while another app holds secure keyboard entry; the log line when keys pass; the toolbar's » menu in a narrow pane (MyGo opens it itself, so the grab may not step aside); recorded in platform-notes |
| 0.6.1 seen in the native app | released on its tests and view renders: the room rules, the window minimum and coming apart, several SSH panes, the macOS traffic lights | on Windows: a group at its minimum, the sidebar coming back, a move to a smaller display, two SSH shells side by side; on the maintainer's Mac: the lights with the sidebar, the rail, a window of its own, protected mode and full screen; recorded in platform-notes |
| 0.6.0 on macOS | the new interface built and run on a Mac: the sidebar material behind floating panes, the traffic lights over the sidebar's top row and on a window of its own's pane, the Settings pages and the keyboard grab stepping aside under the sheet, the + picker and ⌘K search, a session with the Mac's own AI and one with a host's, a request and a device connected to the Mac, Home's privacy permissions while one is missing, contrast and the keyboard (Windows done: platform-notes, "0.6.0 shell, M1" to "0.6.0 contrast and keyboard, M7") | seen on the maintainer's Mac, recorded in platform-notes |
| Shared AI record and draft | the host keeps one AI record that every controller sees and may steer with its permissions (done in the core); the AI prompt draft is shared live: what one controller or the host's user types reaches the host, which passes it to every other controller and its own AI sessions | two controllers and the host see the same task and the same draft, recorded in platform-notes |
| macOS crash | the Mac app "crashed" when a connection request came in: most likely the GUI froze, announcing the request under its lock while macOS asked about notifications (fixed in 0.4.0, with a watchdog that writes the stacks of a frozen GUI to `gui-crash.log`) | the first connection to a Mac host no longer freezes on the user's Macs, or the new logs name another cause |
| macOS pass | done on one Mac (platform-notes): capture, permissions, input and keyboard as host, Follow Kyokai. Left: the clipboard, Background service and the login-window agent, the helper's self-disable, sleep notifications, a Mac as controller of another machine through the viewer window, a Mac host from another machine | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | Custom quality (quality and frame rate); a hardware-codec switch; lock after the session ends | each control in the viewer, carried by the protocol where the host decides |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup; on the Windows computer: the pointer ring with no mouse attached, H.264 decoded on the GPU and Media Foundation's reused samples (the encode and decode times in the viewer's tooltip, both directions) | recorded in platform-notes |

## Decided

- The agent backend (2026-10-23): Pi, in a sidecar Kyokai builds on `pi-coding-agent`'s SDK —
  not OpenCode (everything on by default, some configuration discovery that cannot be closed, and
  interrupted turns resumed by itself after a restart) and not Pi's RPC mode; Kyokai's executor
  and approvals stay the only things that act.
- No extensions for now (2026-10-23): no user or third-party extensions, packages, skills, prompt
  templates or MCP servers; the sidecar uses Pi's extension API only as Kyokai's own built-in code,
  and nothing waits for extensions to come.
- Secure surfaces (2026-10-23): in the helper modes the AI may see the UAC secure desktop, the lock
  screen and the sign-in screen in every mode, told what they are; a UAC consent asks in every
  mode, never "always allow", and the host user or a controller with `approve-ai-action` approves
  it, shown the program, its publisher and whether it is signed; credential prompts and the lock
  and sign-in screens are refused, and a locked session pauses the task (the input desktop and the
  session's lock state decide). It comes with the Auto mode, as its class 0.
- Approval modes (2026-10-23): Every action or Auto, nothing else; no global "ask nothing" and no
  "always allow" (an approval covers the one action shown); elevation and typing credentials never
  loosen; approvals need nobody at the machine (controllers with `approve-ai-action`), so the AI
  never approves itself.
- AI scope (2026-10-23): apps outside the grant are dimmed in what the AI sees; the AI may work in
  a granted window while the host user works in another, how is still open.
- 0.6.1's design (2026-10-23): approved as the two 0.6.1 rows describe it; 0.6.1 is the interface
  only, the AI work is 0.7.0.
- The interface (2026-10-23): everything is a pane the sidebar holds; Settings is the top layer
  over the whole window and never part of a split; AI tasks are panes of their own, apart from the
  providers in Settings › AI; a single pane dragged out becomes a window of its own without a
  sidebar and is never split there; the sidebar keeps search, appearance (Follow system, Light,
  Dark), the gear and the network state at its bottom (Kyokai has no user accounts, so nothing
  account-like goes there); connection requests stay a card above the panes; several AI sessions
  per device in the interface, recent tasks including the devices connected before, Pi as the
  agent later; released as 0.6.0.
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
