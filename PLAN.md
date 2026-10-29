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

## 0.6.6

The official EasyTier core as an option beside Kyokai Network (decided 2026-10-28: no TUN, the
secret only in a runtime file, every run mode). Built and documented
([networking.md](doc/en-us/networking.md#official-easytier-core)); what is left:

| Item | Scope | Done when |
|---|---|---|
| A whole session through official cores | the dial reached the other core's loopback hand-over, but this computer's installed helper holds port 47310 (platform-notes) | a session runs between two devices on official 2.6.4 and 2.7.0 cores, on Windows and macOS, each on a computer whose port 47310 is free; recorded in platform-notes |
| Seen on the Mac | the cores download and run there (no Gatekeeper prompt for a binary fetched by the engine), the Runs on menu and the TOML field; the keyboard grab with the window tiled to half the screen (frontmost application) | on the maintainer's Mac, recorded in platform-notes |
| Options checked before they apply | the TOML is parsed and Kyokai's own keys refused as it is saved, but the core's `--check-config` runs only as the network restarts, so options EasyTier itself refuses leave the network failed with its reason | saving runs `--check-config` first and keeps the running network on a refusal |
| A form for common options | listeners, MTU and the common `[flags]` as fields over the TOML, as EasyTier MyGo has (`configdoc`, same author, GPL-3.0: relicensed for Kyokai) | form ⇄ TOML round trips in table tests |
| Run-mode switches | a store moved into or out of the helper keeps `easytier` but not the downloaded cores, so its networks fail until the core is chosen again | the core moves with the store, or the GUI downloads it again on its own |

The spike (2026-10-28, two cores on one Windows computer, no TUN): with 2.6.4 and 2.7.0, a SOCKS5
connect through one core's portal to the other's virtual IP on a whitelisted port reached a plain
listener on the other's `127.0.0.1` (EasyTier rewrites its own virtual IP to loopback in
`tcp_proxy_service`); a port outside `tcp_whitelist` was dropped; the route list carries the
`Kyokai-<id>` hostnames. Peer links refuse loopback addresses (error 10049), so the integration
test listens on a LAN address. **easytier-mini 2.7.0 cannot carry Kyokai sessions without TUN**: it
has no SOCKS5 portal and does not forward a connection to its own virtual IP, so it is left out
until TUN is decided.

## 0.6.7: Linux

Linux as a third platform, as host and as controller (planned 2026-10-29). Starting point: the
Rust core and the Go engine already build and test on Linux with stubs (`cfg(not(any(windows,
target_os = "macos")))`, `*_other.go`): no capture, no input, no keyboard grab, no credential store
(so nothing secret persists), no peer credentials on the local API, no helper, no service. MyGo
has a Linux backend (GTK 3, WebKitGTK 4.1, AppIndicator tray, Debian packages and an install
script), but its `SetContentProtection` does nothing there (still so in v0.7.3).

Targets: x86_64 and arm64; glibc distributions from Debian
13 / Ubuntu 24.04; GNOME and KDE Plasma on Wayland, and X11 sessions.

| Item | Scope | Done when |
|---|---|---|
| Engine and local API | `SO_PEERCRED` in `peercred_linux.go`; the socket and state paths under `$XDG_RUNTIME_DIR` and `$XDG_STATE_HOME` (paths_unix already shared with macOS); the engine's parent watch; `kyokai-svc` under `systemd --user` for Background service | the local API tests pass on Linux, a peer of another uid is refused; a session runs between the GUI and the engine on Linux |
| Credential store | the master key in the Secret Service (D-Bus, `org.freedesktop.secrets`: GNOME Keyring, KWallet), through a maintained crate checked with `cargo deny`; without a reachable Secret Service nothing is persisted, as today, and the interface says so | the store's tests against a real Secret Service on the dev machine; a profile survives a restart; with the service stopped, no secret file appears |
| Capture | X11: XShm (XCB) per output, damage-driven; Wayland: the ScreenCast portal over PipeWire (DMA-BUF or shm frames), its restore token kept (persist mode "until revoked") so the host user is asked once, not per session, unattended hosts relying on it and the docs naming where a compositor drops it; the cursor as metadata; multi-monitor and scaling mapped to Kyokai's display model | a controller sees a Linux host's displays on GNOME Wayland, KDE Wayland and an X11 session, recorded in platform-notes |
| Input | X11: XTest; Wayland: the RemoteDesktop portal (libei where the portal offers it, the portal's notify calls otherwise), sharing the ScreenCast session so one consent covers both; uinput only in the helper modes (it needs `/dev/uinput`); the local-input watch that revokes the AI lease (XInput2 raw events; on Wayland, input reaching Kyokai's own surface plus the portal session's state, the gap written down) | the input tests' two-machine run with a Linux host; local input takes over the AI lease on X11 and Wayland, recorded in platform-notes |
| Hide Kyokai from capture | MyGo cannot exclude a window on Linux, so the capture path does it: on X11, Kyokai's own windows (by its `_NET_WM_PID` and the window IDs MyGo reports) blacked out in each frame and remote input inside them dropped; on Wayland, where Kyokai cannot learn where its windows are, they hide to the tray while a remote session controls the host, and requests and approvals come as notifications plus a small window that remote input never reaches (the session is paused while it shows) | the exclusion's table tests (window rectangles to masks); on each session type a live check that no Kyokai window is in the picture or reachable while controlled |
| Codecs | VP9 and AV1 in software (libvpx, libaom: the vcpkg triplets gain `x64-linux` and `arm64-linux`); H.264 / HEVC through VA-API where present, software decode otherwise; nothing allocates per frame | encode and decode times in the viewer's tooltip on the dev machine; a session Linux ↔ Windows and Linux ↔ macOS in each codec |
| Keyboard grab, clipboard, audio, power | the grab: X11 `XGrabKeyboard`, Wayland the keyboard-shortcuts-inhibit protocol (what MyGo exposes decides; GNOME asks the user); clipboard text and files through MyGo; audio from the default sink's monitor (PipeWire / PulseAudio); keeping awake through logind's `Inhibit` | each as host and controller on the dev machine, recorded in platform-notes |
| AI on a Linux host | the desktop facts the executor reads (the foreground window and its process: EWMH on X11; on Wayland only what the portals give, so the AI sees less there and the docs say so); screenshots from the same capture | an AI task with approvals and take-over on a Linux host, on X11 and Wayland |
| Privileged helper | a systemd system service installed through `pkexec` (one administrator prompt, polkit), the `kyokai` group, protected copies under `/usr/lib/kyokai` or `/opt/kyokai`, the socket under `/run/kyokai`, verifying the bytes it installs (signature over the package, as the update channel signs); uinput for input, no greeter or lock-screen capture (GDM and SDDM on Wayland do not allow it) | install, update and uninstall on the dev machine with the prompt only then; the run-modes tests on Linux |
| GUI | the tray (AppIndicator, which GNOME shows only with its extension: the first-run tour says so), notifications, autostart for Follow Kyokai (`~/.config/autostart`), the Privacy step's Linux permissions (the portal consent instead of system settings), the views in each language | the native app inspected on GNOME Wayland and KDE (screenshots), `go tool mygo vet` and the view tests in CI on Linux |
| Official EasyTier core | the Linux release assets of 2.6.4 and 2.7.0 in the core picker; Kyokai Network in-process as on the other systems | a network on each core on Linux |
| Packaging and CI | `.deb` for amd64 and arm64 through MyGo's packager, the install script for other distributions, and an AppImage per architecture (updated in place by the signed update channel, the helper installed from it like from the others); signed app updates on Linux; CI's Rust and Go jobs gain `ubuntu-24.04` and `ubuntu-24.04-arm`; release.yml builds and attaches the packages; scripts/build.sh and smoke.sh learn Linux | a tagged build installs and updates on Debian 13 and Ubuntu 24.04, as `.deb`, through the script and as AppImage |
| Docs | platform-notes gains a Linux section; architecture, run-modes, computer-use, security, packaging, ci-cd, development, user-guide, gui and third-party (new libraries and their licenses) in en-us and zh-cn; README and the "Decisions in force" platform line | `go run ./tools/checkdocs` passes; each doc names Linux where it names the other two |

Order: the engine, the local API and the credential store; capture and input on
X11 (the quickest to verify), then Wayland; codecs; the GUI; the helper; packaging and CI. The
version moves to 0.6.7 when the user releases it.

Decided 2026-10-29 (also under Decided): Wayland hides Kyokai to the tray while controlled; the
portal's restore token is accepted for unattended Wayland hosts; the helper ships in 0.6.7;
`.deb`, the install script and AppImage.

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
| Element list (UIA / AX) | an optional observation beside the screenshot, which stays the only one required (Kyokai's own SSH, Remote Desktop and VNC windows are pixels to UIA and AX): the foreground window's visible elements on the main display, Kyokai's own process left out, password fields reduced to their role, as a compact numbered table (role, name, text, bounds in the screenshot's coordinates), bound to that screenshot's capture id and refused once stale; actions stay coordinates (an element number resolves to its centre, then today's bounds check, guards and injection). On by default only for `openai-chat` sources, an experiment for Claude and OpenAI. A second layer for surfaces without a tree and weak models: the system OCR (Windows.Media.Ocr, Apple Vision) before OmniParser or Set-of-Mark | success, tokens and latency with and without it on Kyokai's own task set |
| Kyokai's own task set | the public benchmarks are Ubuntu-first and saturated (OSWorld-Verified), WindowsAgentArena has no maintained board and macOSWorld is small, so Kyokai measures itself: a fixed set of tasks on Windows and macOS through its two-machine test bench, also used for the token and coordinate rows and for the Auto mode's classes (including injection attempts on screen) | the set runs on both platforms with each provider, results recorded in platform-notes |

### Research behind 0.7.0 (2026-10-23)

Two research rounds (agent backend and computer-use options; approvals, tokens and window scope)
lead to the rows above. What they found that the rows rely on:

- **The boundary matters more than the backend.** As long as Kyokai owns the action schema, the
  sidecar protocol and the task record, Pi is a replaceable loop and the trust boundary (the Rust
  executor, the lease, approvals, take-over) does not move; Pi having no permission system is an
  advantage, while OpenCode's approvals, resumption and configuration would each have to be torn
  out. Neither speaks the providers' native computer-use dialects (Pi's `Tool` is name,
  description and parameters; its Responses parser ignores `computer_call`; no
  `context_management` for Claude), so the spike's question is whether Pi can carry the native
  protocols, not whether it runs. Without them OpenAI's Flagged policy would keep only the
  Windows/Command shortcut.
- **Coordinate modes first.** Answers on a 0–999 grid (Qwen3-VL's reference agent, Gemini) used
  as pixels land at about 73 % horizontally and 130 % vertically of the real point and pass the
  bounds check unless the target is near the bottom: the cheapest fix, before the Pi move.
- **amontlabs/lcu: not adopted.** It wraps the closed computer-use runtime shipped inside the
  ChatGPT/Codex desktop app (that app must be installed) for other harnesses; the model writes
  JavaScript in a REPL; Linux X11 and Apple-silicon macOS only (Windows a candidate, Intel Mac not
  supported); one main author and breakage on ChatGPT updates. Adopting it would move execution
  out of Kyokai's executor, run model-written code and miss Kyokai's platforms. Ideas taken:
  per-app policy (terminals refused, the agent's own host app never approved, browsers and
  password managers high risk) — in the Auto classes; the owner present (Touch ID / Windows Hello)
  for anything that would persist, should such a grant ever exist; product messages never enter
  the model's context; an input test matrix (typing by keyboard layout drops Korean syllables,
  emoji and AZERTY `ñ` — Kyokai's `type` gets those cases).
- **Other options and what Kyokai takes:** the providers' native tools stay first choice (Claude
  `computer_toolset_20260801`, OpenAI's GA `computer` tool on general models; Gemini
  `computer_use` gets a native adapter once GA); open weights (Qwen3.8-27B, Holo3-35B-A3B, both
  Apache-2.0) through `openai-chat` once coordinate modes exist (Holo4 is non-commercial); UFO²'s
  control allow-list and the merging of UIA with visual detection, not its picture-in-picture
  session or COM/API actions; Cua's capture id, UIA techniques and read-back checks, Lume for test
  infrastructure, not private SPIs or background delivery; Agent S3's planner/grounder split and
  behaviour narrative, not `exec` or best-of-N rollouts; Windows-MCP's and Peekaboo's snapshot
  schema and "act on the latest snapshot" rule, not embedding them; no code-executing harness.
- **Five ideas for approvals and scope** (from the open-source survey): classify each action's
  concrete target deterministically; fuse risk signals by the worst case and fail closed; policy
  tiers per app (allow / ask / hand off / block) the model cannot change and approvals only
  tighten; prune screenshots in a cache-friendly way with compaction and batching; hold the scope
  on both capture and input, re-checked whenever a new window appears.
- **The AI in one window while the user works in another** (the open question in AI scope) is
  only partly possible with public APIs: UIA patterns and AX actions operate many standard
  controls without moving the pointer; posted messages and `CGEventPostToPid` are unreliable;
  full coverage needs private APIs (Cua's SkyLight SPIs on macOS) or a foreground swap on Windows.
  It is best effort and macOS-leaning, and it redefines "local input takes over at once". Real
  separation (Windows' agent workspace, session containers, Windows Sandbox, RDP loopback, VMs)
  moves the AI off the user's live desktop — the only way folder limits could be enforced, but
  not a way to operate the user's open windows.

## Left

| Item | Scope | Done when |
|---|---|---|
| 0.6.3 seen on the Mac | done on Windows (platform-notes): the codec wait's regression test against a real host, Reconnect's view test, the lease and pace tests and a watcher changing pace on one session | on the maintainer's Mac against the Mac mini: after the display slept or the host recovered, the picture comes back in the chosen codec and never stays on "can't show the picture"; Reconnect over a 0.6.2 host stuck there; Home's figures current again on coming back from a pane or Settings, without "Getting its status…"; recorded in platform-notes |
| 0.6.4 seen on the Mac | the first-run tour, done on Windows (platform-notes): its view tests on both systems and in each language, and a fresh profile in the native app | on the maintainer's Mac with a fresh test profile: the traffic lights over the illustration, the Privacy step's Allow… and Reopen Kyokai, Open power settings landing on Energy (Battery on a MacBook), Install and finish for Follow Kyokai; recorded in platform-notes, the profile deleted after |
| 0.6.2 seen on the Mac | released on its tests, a grab, its renewal and its state through the real core on Windows, and the Mac code type-checked from Windows (not run): closing to the tray, the grab in the whole pane, keys that got past it, renewing it, the macOS tap's start and stop, the two dots and the log line | on the maintainer's Mac: closing with two connections leaves Kyokai in the tray; Map and Translate send Enter, Delete and Command after switching between two connections, with the pointer over the toolbar, and after Kyokai went to the background and came back; the dots green, amber with the reason while another app holds secure keyboard entry; the log line when keys pass; the toolbar's » menu in a narrow pane (MyGo opens it itself, so the grab may not step aside); recorded in platform-notes |
| 0.6.1 seen in the native app | released on its tests and view renders: the room rules, the window minimum and coming apart, several SSH panes, the macOS traffic lights | on Windows: a group at its minimum, the sidebar coming back, a move to a smaller display, two SSH shells side by side; on the maintainer's Mac: the lights with the sidebar, the rail, a window of its own, protected mode and full screen; recorded in platform-notes |
| 0.6.0 on macOS | the new interface built and run on a Mac: the sidebar material behind floating panes, the traffic lights over the sidebar's top row and on a window of its own's pane, the Settings pages and the keyboard grab stepping aside under the sheet, the + picker and ⌘K search, a session with the Mac's own AI and one with a host's, a request and a device connected to the Mac, Home's privacy permissions while one is missing, contrast and the keyboard (Windows done: platform-notes, "0.6.0 shell, M1" to "0.6.0 contrast and keyboard, M7") | seen on the maintainer's Mac, recorded in platform-notes |
| Shared AI record and draft | the host keeps one AI record that every controller sees and may steer with its permissions (done in the core); the AI prompt draft is shared live: what one controller or the host's user types reaches the host, which passes it to every other controller and its own AI sessions | two controllers and the host see the same task and the same draft, recorded in platform-notes |
| macOS crash | the Mac app "crashed" when a connection request came in: most likely the GUI froze, announcing the request under its lock while macOS asked about notifications (fixed in 0.4.0, with a watchdog that writes the stacks of a frozen GUI to `gui-crash.log`) | the first connection to a Mac host no longer freezes on the user's Macs, or the new logs name another cause |
| macOS pass | done on one Mac (platform-notes): capture, permissions, input and keyboard as host, Follow Kyokai. Left: the clipboard, Background service and the login-window agent, the helper's self-disable, sleep notifications, a Mac as controller of another machine through the viewer window, a Mac host from another machine | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | Custom quality (quality and frame rate); a hardware-codec switch; lock after the session ends | each control in the viewer, carried by the protocol where the host decides |
| zip and zstd advisories | `core/deny.toml` ignores RUSTSEC-2026-0367/0368/0369 (zip 8) and 0391/0398/0403 (zstd 0.13, zstd-safe 7): all reached only through `easytier-proto`'s Windows build dependency `zip = "8.6.0"` with default features, for a protoc download that never runs. No 8.x or 0.13.x release fixes them; only zip 9 / zstd 0.14.3 | at each EasyTier update, check its `zip` requirement; once it moves to zip 9 or drops default features, update `Cargo.lock` and remove these ignores, `cargo deny check` passing without them |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup; on the Windows computer: the pointer ring with no mouse attached, H.264 decoded on the GPU and Media Foundation's reused samples (the encode and decode times in the viewer's tooltip, both directions) | recorded in platform-notes |

## Decided

- Linux (2026-10-29): 0.6.7 brings Linux as host and controller with all three run modes (the
  privileged helper included); on Wayland Kyokai's windows hide to the tray while a remote session
  controls the host, requests and approvals as notifications plus a small window remote input never
  reaches; unattended Wayland hosts rely on the portal's restore token; packages are `.deb`, the
  install script and AppImage (no RPM, no Flatpak).
- The first-run tour (2026-10-25): approved as the design canvas draws it — nine steps on macOS, eight on Windows (Keep it awake added, with the Windows display note); the computers as drawn there (front left, no perspective, the mark square on the top); Skip to No privileges; shown again from General; README credits Arion's onboarding (no code). Version 0.6.4.

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
