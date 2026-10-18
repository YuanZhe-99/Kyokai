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
| Pointer follows the mouse (0.1.3) | hover moves reach the host, coalesced in the viewer; the host reports its pointer every 16 ms | the ring follows a hover in a real session |
| Direct LAN connections (0.1.3) | with direct connections on, the node listens on TCP and UDP 47311 and prefers UDP; with them off it sets `disable_p2p` | two devices on one LAN show Direct |
| macOS capture switch (0.1.3) | "Hide Kyokai from capture" reaches the engine (and the helper's agent); the ScreenCaptureKit filter follows it, live | off shows Kyokai's windows in this Mac's video, on hides them |
| VideoToolbox (0.1.3) | H.264 and HEVC encoding and decoding on macOS, interoperating with Media Foundation | Mac↔Mac and Mac↔Windows stream in hardware |
| Codec choice (0.1.3) | the host offers what it can send; the viewer picks Auto or one codec (protocol 7) | each offered codec chosen in a real session |
| Latency and frame rate (0.1.3) | faster pixel conversion; no stale or broken frames in the queues; the path and per-stage times in the viewer | on the LAN the frame-rate cap holds while a window is dragged; times recorded in platform-notes |
| Test-build findings (0.1.3) | fixed in the tree, to confirm on the devices: the helper is updated by the app itself (a 0.1.1 helper made a 0.1.3 Mac "incompatible"), and a version mismatch names the side; the host keeps at most 3 frames ahead of the controller's acknowledgements (protocol 8; a relay backlog showed 9.9 s at 1 fps); a device with direct connections off still dials a LAN peer that listens; the direct switch is in Settings, Run mode, and applies at once; the Network page's devices have their name, Copy and Connect; keys reach the host from a Mac viewer; the Codec choice names codecs only and the viewer's details say what encodes and decodes, in hardware or software (ABI 5) | each confirmed between the Macs and the Windows dev kit |
| Keyboard in every direction (0.1.3) | hosts release the modifiers a key brought (they stayed down after a shortcut); Swap Ctrl and Cmd in the viewer, on by default between a Mac and Windows; a Mac viewer grabs the keyboard (event tap) for Map and Translate as Windows does, and in Legacy turns the menu's Copy, Paste and the like back into Cmd shortcuts | typing and shortcuts tried by a person Mac↔Mac, Mac↔Windows and Windows↔Windows, recorded in platform-notes |
| Viewer settings per device (0.1.3) | quality, frame-rate cap, full colour, codec and the Ctrl/Cmd swap are remembered for each device | a reconnected device opens with its own settings |
| Audio | the host's sound in the viewer: capture (ScreenCaptureKit on macOS, WASAPI loopback on Windows), Opus, protocol messages, playback in the viewer, a mute switch | a person hears the other computer, Mac↔Windows both ways |
| Shared AI record and draft | the host keeps one AI record that every controller sees and may steer with its permissions (done in the core); the AI prompt draft is shared live: what one controller or the host's user types reaches the host, which passes it to every other controller and its own AI page | two controllers and the host see the same task and the same draft, recorded in platform-notes |
| macOS crash | the Mac app crashed once when a connection request came in; on 2026-10-17 requests on this Mac raised the confirmation without a crash, and no crash report was found | cause found from the crashing Mac's logs and fixed, or not seen again over the macOS pass |
| macOS pass | done on one Mac (platform-notes): capture, permissions, input and keyboard as host, Follow Kyokai. Left: the clipboard, Background service and the login-window agent, the helper's self-disable, sleep notifications, a Mac as controller of another machine through the viewer window, a Mac host from another machine | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | original size; Custom quality (quality and frame rate); a hardware-codec switch; per-session clipboard on/off; lock after the session ends | each control in the viewer, carried by the protocol where the host decides |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup | recorded in platform-notes |

## Decided

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
