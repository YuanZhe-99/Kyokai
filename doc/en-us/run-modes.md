# Run modes

Kyokai follows the model Easytier-MyGo uses: administrator authorization happens **once**, when a
privileged helper is installed, and never when the GUI starts. The GUI never runs elevated.

## The three modes

| Mode | Requirement | Engine | Adds |
|---|---|---|---|
| No privileges (default) | none | user-level child of the GUI | the logged-in user's normal desktop |
| Follow GUI | the helper, installed once | in the helper, serving sessions while a GUI is attached | Windows: elevated windows, the UAC secure desktop, the lock screen |
| Background service | the same helper | in the helper from boot | reachable before login, for unattended grants only |

## The helper (Windows)

`kyokai-svc helper install` (administrator) copies the app's `kyokai-svc.exe` and core library into
`%ProgramFiles%\Kyokai\helper`, which only administrators can change, so a user cannot swap what
the service runs. It creates `%ProgramData%\Kyokai` for SYSTEM and Administrators only (users may
open the folder itself, to reach the socket; the `state` folder below it is closed to them),
records the installing user as the **operator** in `state\helper.json` and registers the automatic
service `KyokaiHelper`, which runs `kyokai-svc run --host helper` as SYSTEM and restarts after a
failure. `helper uninstall` stops and deletes the service, its files and the socket (`--purge`
also deletes the helper's identity and trusted devices); `helper status` needs no rights.

The helper's engine keeps its identity and secrets in `%ProgramData%\Kyokai\state` (DPAPI as
SYSTEM). Its socket `%ProgramData%\Kyokai\ctl.sock` can be opened by every local user, and the
helper serves only SYSTEM, the operator and members of a local `kyokai` group (when an
administrator created it); anyone else, an administrator included, is refused
([security.md](security.md)).

## The session agent

The helper runs as SYSTEM in session 0, which has no desktop. It keeps a copy of itself running as
SYSTEM in the active console session (`kyokai-svc agent`, started with its own token moved to that
session on `winsta0\default`), checks the console session every 2 seconds and replaces the agent
when it changes. The agent follows the desktop that receives input — the user's, the UAC secure
desktop or the lock screen — on every request and whenever its capture loses the display, and does
the desktop's work there: capture and encoding, input, the pointer, the clipboard and the AI's
desktop calls. The helper's video, input, pointer, clipboard and AI desktop send their work to it
(`agent::link`); the session and AI code are the same in every mode.

They talk over two one-way named pipes (synchronous I/O on one pipe handle is serialized, so a
waiting read would block every write), each with a random name, created as the first instance
with a SYSTEM-only DACL; the helper checks that the client is the agent process it started, and
the agent that the server is the helper named on its command line. Messages are a u32 length, a
kind (JSON request or reply, a video frame, a screenshot) and the body. Only encoded video, input
events, clipboard text, clipboard images this computer copied (as PNG, encoded by the agent) and
the AI's screenshots cross. Both processes are privileged and never decode remote media, so in the
helper modes clipboard images from a controller are not taken. Without an agent, video falls back
to the test pattern, input is refused, and an AI task reads it as the host user taking over.

## Current implementation

The helper's install, uninstall and status commands, the service, its authorization and the
session agent (its link, start in the console session and desktop following) are implemented on
Windows; the link is tested in-process, the service and the agent not yet on a machine. The macOS
helper (a root LaunchDaemon) comes with the Mac pass and reports "not available yet" until then.
The run-mode choice in Settings, moving the identity into the helper, its signed self-update and
its self-disable follow (`PLAN.md`, Phase 6). Until then an installed helper runs an engine of its
own with a separate Device ID, which is for development only.
