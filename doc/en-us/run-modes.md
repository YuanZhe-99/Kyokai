# Run modes

Kyokai follows the model Easytier-MyGo uses: administrator authorization happens **once**, when a
privileged helper is installed, and never when the GUI starts. The GUI never runs elevated.

## The three modes

| Mode | Requirement | Engine | Adds |
|---|---|---|---|
| No privileges (default) | none | user-level child of the GUI | the logged-in user's normal desktop |
| Follow GUI | the helper, installed once | in the helper, serving sessions while a GUI is attached | Windows: elevated windows, the UAC secure desktop, the lock screen |
| Background service | the same helper | in the helper from boot | reachable before login, with the permanent password only |

The helper keeps its mode in `state\mode.json` (`helper.background`). While a GUI is attached (an
open status stream) it serves everyone; without one, Background service serves only the
permanent password (while Connect without asking is on) and Follow GUI no one, ending its host sessions ([identity-and-access.md](identity-and-access.md),
"Unattended access").

## Switching

General, Run mode, switches between the three. Leaving No privileges installs the helper when it
is missing (the one administrator prompt), waits for it, and **moves the store**
(`localapi.MoveStore`): the user engine seals its identity, trusted devices, known hosts,
permanent password, network secret, AI keys, friendly name and AI settings under a one-time
passphrase (`store.export`), the helper takes them over (`store.import`), and only once the helper
reports the same Device ID does the user engine forget them and stop (`store.clear`), so one
identity is never served twice and never lost. The GUI then uses the helper's socket (`gui.json`
`helper`, saved per profile, so a development profile that switched follows the helper too), also
when it starts again. Follow GUI and Background service switch with `helper.background`. Going back moves the
store the other way and removes the helper and its state: on Windows with a second prompt; on
macOS the helper, root already, removes itself when its operator asks (`helper.remove`, retried
while launchd restarts the engine that gave up the store), with the administrator prompt only as
the fallback. `ctl move --to SOCKET`
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

## The helper (macOS)

The app ships `Kyokai Helper.app` in `Contents/Resources/bin`, beside its own `kyokai-svc`: a
bundle (`com.yuanzhe.kyokai.helper`, no Dock icon) holding the core library and the helper's own
`kyokai-svc`, so that macOS lists the agent's permissions as "Kyokai Helper" while the app's
engine counts as Kyokai. `kyokai-svc helper install --from BIN --operator-uid N` (root, through
the administrator prompt `osascript … with administrator privileges`, as EasyTier MyGo does)
copies the bundle with `ditto` into `/Library/Application Support/Kyokai`, owned by root and
writable by root only, creates `state` (root only), records the operator's uid in
`state/helper.json`, and loads two launchd jobs: the daemon `com.yuanzhe.kyokai.helper`
(`run --host helper` as root, from boot, restarted by launchd) and the agent
`com.yuanzhe.kyokai.agent` in `/Library/LaunchAgents` (`agent --socket …`, in every graphical
session as its user and at the login window). `helper uninstall` unloads both and deletes the
bundle and sockets (`--purge`: also the state and the helper's key in the System keychain).

The helper keeps its identity in `state` with the store's key in the System keychain. Its
socket `ctl.sock` can be opened by every local user; the helper serves only root, the operator
and members of a local `kyokai` group, by uid and gid ([security.md](security.md)).

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

On macOS launchd starts the agent, not the helper: it connects to `agent.sock` in the helper's
folder (root-owned, so only root can hold it; the agent checks that the peer is root), and the
helper takes a connection only from its own `kyokai-svc` (`LOCAL_PEERPID`, `proc_pidpath`) run by
the user at the screen or by root at the login window; the newest agent replaces an older one.
The link's messages are the same. The agent injects with its own permissions; blocking the
user's input, privacy mode and locking are not offered there. The status's `permissions` are the
agent's in the helper modes, and a permission asked for there is asked by the agent; macOS counts
a new grant only in an agent started after it, so the permissions card offers **Restart Kyokai
Helper** (`helper.agent_restart`: the agent exits and launchd starts it again).

## Updates

Release builds sign the helper's files: `scripts/build.sh` writes `bundle.json` (the version and the
SHA-256 of `kyokai-svc.exe` and `kyokai_core.dll`) and `bundle.sig` (Ed25519, `KYOKAI_SIGNING_KEY`)
next to them. When the helper is older than the app, General, Run mode, offers **Update the
helper** (`helper.update`): the helper reads the bundle **once**, checks the signature against the
key embedded in it (`internal/bundlesig/release.pub`), every hash, that it holds both files and is
not older, writes exactly those verified bytes into its folder (renaming the running files aside)
and exits; the service restarts it. It never reads the user-writable app folder again, so a file
swapped there after the check is not installed. An unsigned build (development, or a release
without the key) is installed with one administrator prompt instead.

An app update replaces the app's files, not the helper's, and a helper of another version cannot
reach devices that run the new one. So the GUI, the first time it attaches to a helper behind it while
no session is open (an older version, or the same version from another build: the status's
`build`, so a test build's helper follows too), asks for this signed update itself, without a prompt; it never falls back to
the administrator prompt on its own. A connection refused for a version mismatch says which side
differs: this device's older helper, or the other device's version.

On macOS the code signature does this: the helper copies the app's `Kyokai Helper.app` into a
root-owned folder first, checks that copy with `codesign --verify --deep --strict` against the
installed bundle's designated requirement (the same identifier and certificate) and that its
version is not older, then puts it in place and exits; launchd restarts the daemon and the helper
restarts the agent. An ad hoc signed build never satisfies the requirement and needs the prompt.

## Self-disable

The installer records the app's folder (`app_dir` in `helper.json`). When the app's engine is
missing there at two checks a minute apart (an app update replacing its files is not a removal),
the helper deletes its service, its state and its socket, schedules its own files for deletion at
the next restart of Windows and exits, so removing the app leaves no running helper. On macOS it
unloads the agent, deletes its plists, folder and key and unloads itself last.

## Current implementation

The helper's install, uninstall and status commands, the service, its authorization and the
session agent (its link, start in the console session and desktop following) are implemented on
Windows; the link is tested in-process, the service and the agent not yet on a machine. On macOS
the helper ran on a MacBook Pro with macOS 27 ([platform-notes.md](platform-notes.md), "macOS"):
install with one prompt, the store move both ways, the agent's video and a click, the signed
self-update (an ad hoc bundle refused) and removal without a prompt; Background service, the login
window and self-disable have not run there yet.
The helper ran as a service on Windows ARM64 ([platform-notes.md](platform-notes.md), "The
helper"): install, authorization, the agent with capture of and a click on the secure desktop,
the store move both ways, unattended access, uninstall and self-disable. The run-mode switch in
the GUI and the signed update have not run there yet.
The signed self-update and the self-disable are implemented and their rules tested (a file
swapped after the check is not installed); none of this has run as a service yet. A helper
installed with `kyokai-svc helper install` alone runs an engine of its own with a separate Device ID
until a store is moved into it.
