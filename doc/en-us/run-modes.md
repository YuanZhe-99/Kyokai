# Run modes

Kyokai follows the model Easytier-MyGo uses: administrator authorization happens **once**, when a
privileged helper is installed, and never when the GUI starts. The GUI never runs elevated.

## The three modes

| Mode | Requirement | Engine | Adds |
|---|---|---|---|
| No privileges (default) | none | user-level child of the GUI | the logged-in user's normal desktop |
| Follow GUI | the helper, installed once | in the helper, serving sessions while a GUI is attached | Windows: elevated windows, the UAC secure desktop, the lock screen |
| Background service | the same helper | in the helper from boot | reachable before login, for unattended grants only |

The helper keeps its mode in `state\mode.json` (`helper.background`). While a GUI is attached (an
open status stream) it serves everyone; without one, Background service serves only unattended
grants and Follow GUI no one, ending its host sessions ([identity-and-access.md](identity-and-access.md),
"Unattended access").

## Switching

Settings, Run mode, switches between the three. Leaving No privileges installs the helper when it
is missing (the one administrator prompt), waits for it, and **moves the store**
(`localapi.MoveStore`): the user engine seals its identity, trusted devices, known hosts,
permanent password, network secret, AI keys, friendly name and AI settings under a one-time
passphrase (`store.export`), the helper takes them over (`store.import`), and only once the helper
reports the same Device ID does the user engine forget them and stop (`store.clear`), so one
identity is never served twice and never lost. The GUI then uses the helper's socket (`gui.json`
`helper`). Follow GUI and Background service switch with `helper.background`. Going back moves the
store the other way and removes the helper and its state (a second prompt). `ctl move --to SOCKET`
does the same move from the command line. When the GUI quits, only its own engine stops; the
helper keeps running.

## The helper (Windows)

`kyokai-svc helper install` (administrator) copies the app's `kyokai-svc.exe` and core library into
`%ProgramFiles%\Kyokai\helper`, which only administrators can change, so a user cannot swap what
the service runs. It creates `%ProgramData%\Kyokai` for SYSTEM and Administrators only (users may
open the folder itself, to reach the socket; the `state` folder below it is closed to them),
records the installing user as the **operator** in `state\helper.json`, adds an inbound firewall rule
for its own `kyokai-svc.exe` (`Kyokai helper`, so the helper's direct paths need no prompt;
[networking.md](networking.md)) and registers the automatic
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
events, clipboard text, clipboard images this computer copied (as PNG, encoded by the agent), the
paths of copied files and the AI's screenshots cross. Both processes are privileged and never decode remote media, so in the
helper modes clipboard images from a controller are not taken. Files on the clipboard are read
and written by the helper while it impersonates the console's signed-in user
([computer-use.md](computer-use.md), "Files"). Without an agent, video falls back
to the test pattern, input is refused, and an AI task reads it as the host user taking over.

## Updates

Release builds sign the helper's files: `scripts/build.sh` writes `bundle.json` (the version and the
SHA-256 of `kyokai-svc.exe` and `kyokai_core.dll`) and `bundle.sig` (Ed25519, `KYOKAI_SIGNING_KEY`)
next to them. When the helper is older than the app, Settings, Run mode, offers **Update the
helper** (`helper.update`): the helper reads the bundle **once**, checks the signature against the
key embedded in it (`internal/bundlesig/release.pub`), every hash, that it holds both files and is
not older, writes exactly those verified bytes into its folder (renaming the running files aside)
and exits; the service restarts it. It never reads the user-writable app folder again, so a file
swapped there after the check is not installed. An unsigned build (development, or a release
without the key) is installed with one administrator prompt instead.

## Self-disable

The installer records the app's folder (`app_dir` in `helper.json`). When the app's engine is
missing there at two checks a minute apart (an app update replacing its files is not a removal),
the helper deletes its service, its state and its socket, schedules its own files for deletion at
the next restart of Windows and exits, so removing the app leaves no running helper.

## Current implementation

The helper's install, uninstall and status commands, the service, its authorization and the
session agent (its link, start in the console session and desktop following) are implemented on
Windows; the link is tested in-process, the service and the agent not yet on a machine. The macOS
helper (a root LaunchDaemon) comes with the Mac pass and reports "not available yet" until then.
The helper ran as a service on Windows ARM64 ([platform-notes.md](platform-notes.md), "The
helper"): install, authorization, the agent with capture of and a click on the secure desktop,
the store move both ways, unattended access, uninstall and self-disable. The run-mode switch in
the GUI and the signed update have not run there yet.
The signed self-update and the self-disable are implemented and their rules tested (a file
swapped after the check is not installed); none of this has run as a service yet. A helper
installed with `kyokai-svc helper install` alone runs an engine of its own with a separate Device ID
until a store is moved into it.
