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
- macOS builds are signed with the self-signed "Kyokai Code Signing" certificate; the helper
  verifies its own updates (bundle signature on Windows, code signature on macOS).

## Left

| Item | Scope | Done when |
|---|---|---|
| macOS crash | the Mac app crashed once when a connection request came in; on 2026-10-17 requests on this Mac raised the confirmation without a crash, and no crash report was found | cause found from the crashing Mac's logs and fixed, or not seen again over the macOS pass |
| macOS signing with a Team ID | sign with the maintainer's free Apple Development certificate (Personal Team) instead of the self-signed one, locally and in the release workflow (the two `KYOKAI_MACOS_CERT_*` secrets and the identity name taken from the certificate); the offline copy in the maintainer's key store updated | a rebuild and an update keep Keychain access without asking, and the privacy grants hold; the one-year renewal written down |
| macOS pass | done on one Mac (platform-notes): capture, permissions, input and keyboard as host, Follow Kyokai. Left: VideoToolbox H.264 / HEVC (hosts send software VP9 now), the clipboard, Background service and the login-window agent, the helper's self-disable, sleep notifications, a Mac as controller of another machine through the viewer window, a Mac host from another machine | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | original size and full screen; Custom quality (quality and frame rate); codec preference (Auto / each codec) and a hardware-codec switch, which also lets a session exercise libvpx and libaom between two machines; remote cursor shown or not; per-session clipboard on/off; lock after the session ends; direct-vs-relay shown in the viewer | each control in the viewer, carried by the protocol where the host decides |
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
