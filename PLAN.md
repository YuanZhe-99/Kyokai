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
| macOS pass | ScreenCaptureKit capture (Kyokai's windows left out), privacy permissions, VideoToolbox H.264 / HEVC, input, the clipboard, the LaunchDaemon helper and LaunchAgent session agent, signing (S5: timestamping with the self-signed identity, TCC grants surviving an update), sleep notifications | the acceptance record's rows hold on macOS, both directions Mac ↔ Windows |
| OpenAI live run | the Responses adapter against the real API (fixtures and fakes only so far) | a task on a real desktop, recorded in platform-notes |
| Viewer controls (RustDesk parity) | original size and full screen; Custom quality (quality and frame rate); codec preference (Auto / each codec) and a hardware-codec switch, which also lets a session exercise libvpx and libaom between two machines; remote cursor shown or not; per-session clipboard on/off; lock after the session ends; direct-vs-relay shown in the viewer | each control in the viewer, carried by the protocol where the host decides |
| Runs still to see | the keyboard modes on a real keyboard; the GDI fallback on a machine without desktop duplication; the agent's display power request in the helper modes; a real sleep and wake (the test VM has no sleep state); a mixed-DPI setup | recorded in platform-notes |

## Decided

- Hole punching makes Windows Firewall ask, so direct paths are off by default in No privileges
  and always on in the helper, whose installer adds a firewall rule (2026-10-17).
- The helper and its session agent are Windows-only until the macOS pass; the services policy for
  Ctrl+Alt+Del is switched on only for the call.
- HEVC decodes on the GPU only (the software decoder never outputs the uneven-tile streams of one
  hardware encoder); a host with a free hardware HEVC encoder prefers HEVC.
- macOS timestamping with the self-signed identity is tested in S5; if Apple's service refuses,
  MyGo gains an option to skip the timestamp.
