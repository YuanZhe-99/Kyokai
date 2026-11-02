# PLAN.md

The active plan: what is left, with done-when criteria and open decisions. Long-lived contracts
live in `doc/`; what is implemented and verified per platform is in
`doc/en-us/platform-notes.md` and `doc/en-us/acceptance.md`. Delete items as they are done; delete
this file when the plan is complete.

## Decisions in force

- Platforms: Windows x64 + ARM64, macOS arm64 + x64 (macOS 14+), Linux x64 + arm64 (since 0.6.7).
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

Linux as a third platform, as host and as controller (planned 2026-10-29). Done so far: the Rust
core, the engine and the local API build and pass the whole gate on Debian 13 x86_64 (the codecs
through vcpkg's `x64-linux` triplet, peer credentials, the socket under `$XDG_RUNTIME_DIR`, the
store's key in the Secret Service, the system sample from procfs), and `scripts/smoke.sh user`
passes there (platform-notes). Capture and input work on X11 (Xvfb) and capture on Wayland (headless sway), platform-notes. Sound and
keeping awake work. Still stubs: the keyboard grab, the helper and the service. MyGo
has a Linux backend (GTK 3, WebKitGTK 4.1, AppIndicator tray, Debian packages and an install
script), but its `SetContentProtection` does nothing there (still so in v0.7.3).

Targets: x86_64 and arm64; glibc distributions from Debian
13 / Ubuntu 24.04; GNOME and KDE Plasma on Wayland, and X11 sessions.

| Item | Scope | Done when |
|---|---|---|
| Engine and service | the state folder under `$XDG_STATE_HOME` (check what MyGo's `App.Path` gives); `kyokai-svc` under `systemd --user` for Background service; a refused peer of another uid seen live (two accounts); KWallet's Secret Service | a session runs between the GUI and the engine on Linux; Background service survives a logout |
| Capture | X11 done (MIT-SHM, computer-use.md "Linux"; left: a real two-display X11 desktop, HiDPI scaling); Wayland done in the engine (portal + PipeWire shm, restore token kept, seen on headless sway; left: GNOME and KDE, the consent dialog and the token on a real desktop, HiDPI); the cursor as metadata; multi-monitor and scaling mapped to Kyokai's display model | a controller sees a Linux host's displays on GNOME Wayland, KDE Wayland and an X11 session, recorded in platform-notes |
| Input | X11 done (XTest, XInput 2 raw events; left: a person's own input stopping an AI task, on a real X11 desktop); Wayland done in the engine (the RemoteDesktop portal's notify calls on the ScreenCast session; the local watch from `/dev/input`, so AI tasks need the helper or the `input` group; left: a run on GNOME and KDE); uinput only in the helper modes (it needs `/dev/uinput`) | the input tests' two-machine run with a Linux host; local input takes over the AI lease on X11 and Wayland, recorded in platform-notes |
| Hide Kyokai from capture | MyGo cannot exclude a window on Linux, so the capture path does it: on X11 done (Kyokai's windows blacked out and refused, seen on Xvfb; left: the real GUI's windows); on Wayland (engine done: `kyokai.shown` holds the picture and input; GUI left), where Kyokai cannot learn where its windows are, they hide to the tray while a remote session controls the host, and requests and approvals come as notifications plus a small window that remote input never reaches (the session is paused while it shows) | the exclusion's table tests (window rectangles to masks); on each session type a live check that no Kyokai window is in the picture or reachable while controlled |
| Codecs | VP9 and AV1 in software (libvpx, libaom: the `x64-linux` triplet builds and its tests pass; `arm64-linux` is written but not built, and `scripts/protoc.sh` has no `linux-aarch_64` yet); H.264 / HEVC: VA-API on hosts, FFmpeg in the viewer (0.6.10); nothing allocates per frame | encode and decode times in the viewer's tooltip on the dev machine; a session Linux ↔ Windows and Linux ↔ macOS in each codec |
| Keyboard grab, clipboard | audio and keeping awake done (PipeWire; logind and the screen saver's inhibitors; the sleep watch). The grab: X11 `XGrabKeyboard` (Translate must let typing keys reach GTK: re-sent to the viewer's window, or Translate falls back to Legacy on Linux), Wayland the keyboard-shortcuts-inhibit protocol (what MyGo exposes decides; GNOME asks the user); clipboard text and files through MyGo and arboard (X11 and Wayland data control) | each as host and controller on the dev machine, recorded in platform-notes |
| AI on a Linux host | the desktop facts the executor reads (the foreground window and its process: EWMH on X11; on Wayland only what the portals give, so the AI sees less there and the docs say so); screenshots from the same capture | an AI task with approvals and take-over on a Linux host, on X11 and Wayland |
| Privileged helper | implemented and run installed (platform-notes: install, authorization, sealed key, a session through the agent, removal). Left: the GUI's switch with pkexec and the store move, self-disable, the signed update, the evdev watch from the helper for AI on Wayland, uinput | install, update and uninstall on the dev machine with the prompt only then; the run-modes tests on Linux |
| GUI | runs (Xvfb: tour, Home, a viewer to an X11 host; window buttons; Linux tour wording; the Wayland guard). Left: the tray (AppIndicator, which GNOME shows only with its extension), notifications, Open at login (`~/.config/autostart` through MyGo), the run modes once the helper exists | the native app inspected on GNOME Wayland and KDE (screenshots), `go tool mygo vet` and the view tests in CI on Linux |
| Official EasyTier core | done: the Linux assets are listed and install (platform-notes); left: a network on each core on Linux | a network on each core on Linux |
| Packaging and CI | done in the scripts and workflows (`.deb`, `install.sh`, AppImage; CI and release jobs on Ubuntu 24.04 x64 and arm64); the `.deb` and archive built here. Left: the AppImage built and both installed, the jobs' first run | a tagged build installs and updates on Debian 13 and Ubuntu 24.04, as `.deb`, through the script and as AppImage |
| Docs | platform-notes gains a Linux section; architecture, run-modes, computer-use, security, packaging, ci-cd, development, user-guide, gui and third-party (new libraries and their licenses) in en-us and zh-cn; README and the "Decisions in force" platform line | `go run ./tools/checkdocs` passes; each doc names Linux where it names the other two |

Order: capture and input on
X11 (the quickest to verify), then Wayland; codecs; the GUI; the helper; packaging and CI. The
version moves to 0.6.7 when the user releases it.

Decided 2026-10-29 (also under Decided): Wayland hides Kyokai to the tray while controlled; the
portal's restore token is accepted for unattended Wayland hosts; the helper ships in 0.6.7;
`.deb`, the install script and AppImage.

## 0.6.10: H.264 and HEVC on Linux

Done: a Linux host encodes H.264 and HEVC through VA-API, a Linux viewer decodes them through the
system's FFmpeg ([computer-use.md](doc/en-us/computer-use.md), "Linux"). Left:

| Item | Scope | Done when |
|---|---|---|
| Seen across platforms | a Linux host's NanoKVM and screen in H.264 and HEVC to a Windows and a Mac controller; a Linux controller of a Windows and a Mac host; AMD (Mesa) and NVIDIA drivers | in platform-notes, with encode time and CPU against software VP9 |

## 0.6.15: left to see

Built 2026-11-01 (version-history; computer-use, networking, protocol, remote-services, gui).
What only real machines can show:

| Item | Done when |
|---|---|
| Video baseline, before and after | 10-minute sessions while dragging windows and typing, this computer with a LAN device (direct, relayed, by address) and the test VM over its relay, RustDesk side by side: `ctl watch` gaps, the tooltip's encode and decode ms, kbit/s and fps, the host's recovery lines, a capture of small text; recorded in platform-notes |
| Resolution | each choice's size on a 4K and a 2560×1600 host; encode and decode within a frame time at 4K in hardware |
| Paths on real machines | an address cut and a direct-link cut on two devices: the picture back within about 3 seconds; an SSH shell and an SFTP copy (its checksum) carry on; a healthy session moves once to a LAN address that starts answering; no session moves in 10 minutes of jitter (`netdelay -stall`) |
| LAN offer | on a LAN pair with default settings the offer shows, one click brings the path to Direct and the session carries on; none for the test VM |
| Corners | the real app's window corners on Windows 11 at 100 % and 200 %, light and dark, and on GNOME and KDE, shown to the user |
| Engine priority | encode and decode ms the same on battery; no Efficiency mode during a session |
| MyGo 0.7.4 keys | Ctrl+C and Ctrl+V in the picture, the terminal, Remote Desktop and VNC panes on Windows and macOS |

Not built as planned: a move hands the session to the new connection only in part (the host keeps
the controller's AI task; capture and the encoder start again with a key frame), since the Noise
state cannot be moved under a running session safely.

## 0.7.0: left to see

Released 2026-11-02 on its tests and the live runs on the test VM (platform-notes). What it still
needs to be seen doing:

| Item | Scope | Done when |
|---|---|---|
| AI on macOS | the sidecar's start-up and memory, the Auto classes' facts, AI scope's dimming, the element list (AX) and its OCR (Vision), the task set's three macOS tasks; macOS and Linux code compiled only by CI | on the maintainer's Mac, recorded in platform-notes |
| Auto classes live | done live on the VM: system shortcut, settings, terminal, process control, power and sign-out; left: deleting, sending, payments, consent, vaults, installing, Remote Desktop | each class asks in a live run, recorded in platform-notes |
| Measurements | the token use before and after the caching and pruning on a real task; the task set with and without the element list; more runs with stronger models (Claude, OpenAI) | the numbers in platform-notes |
| AI on a pane live | a Remote Desktop or VNC pane lent to the AI on the test VM (the host user types the sign-in) | a task done there, recorded in platform-notes |
| Viewer controls seen live | the custom quality and more frame-rate caps, the Hardware codec switch, Lock when the session ends; the wheel in notches | in a real session to the test VM: a custom quality changes the bitrate, Hardware codec off moves the stream to a software codec, the VM locks when the session ends, one notch scrolls as one on a host and in a Remote Desktop and a VNC pane; recorded in platform-notes |

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
| zip and zstd advisories | `core/deny.toml` ignores RUSTSEC-2026-0367/0368/0369/0429 (zip 8) and 0391/0398/0403 (zstd 0.13, zstd-safe 7): all reached only through `easytier-proto`'s Windows build dependency `zip = "8.6.0"` with default features, for a protoc download that never runs. No 8.x or 0.13.x release fixes them; only zip 9 / zstd 0.14.3 | at each EasyTier update, check its `zip` requirement; once it moves to zip 9 or drops default features, update `Cargo.lock` and remove these ignores, `cargo deny check` passing without them |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup; on the Windows computer: the pointer ring with no mouse attached, H.264 decoded on the GPU and Media Foundation's reused samples (the encode and decode times in the viewer's tooltip, both directions) | recorded in platform-notes |

## Decided

- The AI beside the host user (2026-11-02): best effort. With "Work beside me" on (apps granted),
  the AI sees and acts in its granted window without the host user's pointer or keyboard where the
  system lets it — the window captured on its own even when covered, actions on its elements through
  UI Automation patterns (invoke, toggle, select, expand, set value, scroll). The host user's input
  outside that window does not pause it; input in it still takes over at once. A step that cannot
  run that way pauses the task and says so; resuming brings the window to the front and runs that
  step with the pointer and keyboard. Windows first; elsewhere every acting step falls back.
- AI scope (2026-11-01): only the host user grants or widens the AI's apps; a controller cannot,
  whatever its permissions.
- Pi (2026-11-01, the spike's first answer): Pi's built-in Anthropic and OpenAI Responses
  adapters cannot carry the native computer-use protocols (the Anthropic one loses `toolset_name`,
  the Responses parser drops `computer_call` items), so Kyokai registers providers of its own
  (`registerProvider` with its own `streamSimple`) that own the requests and answers, while Pi's
  loop, abort, steering, follow-ups and compaction run on top; sessions, settings and credentials
  in memory, the model catalog, cache warming and attribution off, images not resized.
- The CLI (2026-11-01): 0.7.0 brings a console `kyokai` command in the app's `bin` folder, not
  added to PATH, with `kyokai-svc ctl` kept as another name for it; it shows a session's picture
  only as a screenshot taken when a command asks, saved to the path that command names.
- 0.6.15 (2026-11-01): one release for the picture, the paths and the corners. A session leaves a
  silent path within seconds and may also move, while healthy, to a clearly better route
  (replacing 0.5.0's "a healthy session never moves"); tunnels survive a move. Screens are sent at
  their native resolution where both sides work in hardware, and the controller chooses the
  resolution (Auto, Native, 4K, 1440p, 1080p, 720p). Direct connections stay off by default, and a
  relayed session on one LAN is offered a direct connection in one click. On Windows and Linux a
  pane's corner that meets the window's is 4 points, inside the system's own corner (never square;
  macOS unchanged).
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
- 0.5.0 (2026-10-21): a device has routes — the Kyokai network and any number of addresses; Auto picks by latency now and throughput remembered per route, at connect and on resume only (a healthy session never moves; superseded 2026-11-01, above); a route can be fixed by hand. Addresses are edited from the card (⋯ → Addresses…), its path line is a route picker, and the connect form adds addresses. KCP/QUIC switches stay out (EasyTier's proxies never carry Kyokai's sessions; `quic://` server URLs already work).
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
