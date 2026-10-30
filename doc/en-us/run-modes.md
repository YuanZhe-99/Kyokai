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
is missing (the one administrator prompt), waits for it, brings it to the app's build when it is
behind (the signed update, [Updates](#updates)), and **moves the store** (`localapi.MoveStore`):
the user engine seals its whole store — every secret (the identity, trusted devices and known
hosts, the permanent password, networks with their secrets, access settings, saved passwords and
sign-ins, keys, AI keys) and the state files `device.json`, `ai.json` and `ai-tasks.json`, plus the
connection history held in memory under No logs — under a one-time passphrase (`store.export`,
document version 2); the helper replaces its own store with it in one write (`store.import`); and
only once the helper reports the same Device ID does the user engine forget everything, leave
`moved.json` in its state folder saying where its identity went, and stop (`store.clear`). So one
identity is never served twice and never lost, and nothing of the other run mode is left behind.
Both engines must be the same build: a 0.4.0 engine refuses the version 2 document without
writing anything.

A move never replaces, unasked, another identity that has history (trusted devices, known hosts,
networks, a permanent password, AI keys, saved sign-ins or keys): the GUI asks the person, who may
keep each run mode's own instead. An engine that finds `moved.json` in its state folder is
**parked**: it starts no core and makes no identity of its own, serves only its status (`moved`),
taking a store back (`store.import`) and starting over with a new identity after the person
confirms (`store.forget_move`); before 0.4.1 such an engine made a fresh identity, which a later
switch could move over the real one. The GUI uses the helper's socket when `gui.json` says so
(`helper`, per profile) or when its own engine's marker says the identity moved to the helper; if
the helper is gone, Home offers to install it again or to start over here. Follow GUI and
Background service switch with `helper.background`, after the GUI already uses the helper.

Going back moves the store the other way, then the helper removes itself without a prompt
(`helper.remove`: on Windows through the same path as its self-disable, on macOS as before; retried
while the service manager restarts the engine that gave up the store), with the administrator
prompt only as the fallback. `ctl move --to SOCKET` does the same move from the command line. When
the GUI quits, only its own engine stops; the helper keeps running and ends the GUI's controller
sessions 15 seconds after its last GUI detached (a GUI that quits asks for that at once,
`sessions.close_controllers`).

**Cleanup after updating.** The first start of each version from 0.4.1 on (once per version, `gui.json`
`stores_unified`) removes the store of the run mode not in use, which earlier switches left
behind: attached to the helper, the GUI deletes the user engine's `secrets.bin`, `master.key`,
`device.json`, `ai.json` and `ai-tasks.json` and leaves the marker (on macOS the Keychain item
stays, opening nothing); attached to its own engine with the helper installed, it brings the
helper to its build with the signed update (never a prompt; an unsigned build tries again at the
next start) and asks it to remove itself. When the helper holds devices, networks or a permanent
password and this run mode none (a lost choice of run mode), the person chooses which set stays.
The helper belongs to the installed app: a development profile (`KYOKAI_PROFILE`) never removes
it.

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
also deletes the helper's identity and trusted devices); `helper status` needs no rights. The GUI
learns whether the helper is installed from the service manager alone and starts no process for
it (its version comes from the helper's status): the engine is a console program, and run from
the GUI for its version it opened a console window, left blank on the screen at login (0.3.2).

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

## The helper (Linux)

`kyokai-svc helper install --from BIN --operator-uid N` (root, through `pkexec`, which asks through
the desktop's polkit agent: the one administrator prompt) copies the app's `kyokai-svc` and
`libkyokai_core.so` into `/usr/local/lib/kyokai/helper`, writable by root only, records the
operator's uid and the app's folder in `/var/lib/kyokai/helper.json` (root only), and writes two
systemd units: the system service `kyokai-helper.service` (`run --host helper` as root, from
boot, started again when it ends; systemd makes `/run/kyokai` for its sockets, which every local
user may enter, and `/var/lib/kyokai`, root only) and the user service `kyokai-agent.service` in
`/etc/systemd/user` (`agent --socket /run/kyokai/agent.sock`, part of `graphical-session.target`,
enabled for every user with `systemctl --global` and started at once in every running user
manager), which captures and injects in the user's graphical session as the macOS agent does. The
agent's link is the macOS one: the helper takes a connection only from its own `kyokai-svc`
(`SO_PEERCRED`, `/proc/<pid>/exe`) run by the user of the active session on the first seat
(logind's `/run/systemd/seats/seat0`), and the agent connects only to a socket held by root. There
is no agent at the sign-in screen: GDM and SDDM let no program capture it. `helper uninstall`
stops and disables both units and deletes the files and sockets (`--purge`: also the state).

The helper keeps its identity in `/var/lib/kyokai` with the store's key sealed by `systemd-creds`
(`master.cred`: the host's credential key, made by the first seal when the computer has none, and
the TPM where there is one), since root has no Secret Service. Its socket `/run/kyokai/ctl.sock`
can be opened by every local user; the helper serves only root, the operator and members of a
local `kyokai` group, by uid and gid ([security.md](security.md)). Updates use the signed bundle
Windows uses (`bundle.json` and `bundle.sig` beside the app's `kyokai-svc`), and self-disable
stops the units and deletes everything, the service last.

## The session agent

The helper runs as SYSTEM in session 0, which has no desktop. It keeps a copy of itself running as
SYSTEM in the active console session (`kyokai-svc agent`, started with its own token moved to that
session on `winsta0\default`), checks the console session every 2 seconds and replaces the agent
when it changes. The agent follows the desktop that receives input — the user's, the UAC secure
desktop or the lock screen — on every request and whenever its capture loses the display, and does
the desktop's work there: capture and encoding, input, the pointer, the clipboard and the AI's
desktop calls. The helper's video, input, pointer, clipboard and AI desktop send their work to it
(`agent::link`); the session and AI code are the same in every mode.

A capture compares the input desktop with its own every half second, whatever it captured, and
follows a change as a lost display ([computer-use.md](computer-use.md), "Video", has the lock
screen's details). A thread that cannot move to the input desktop (Windows refuses
`SetThreadDesktop` to one that owns a window or a hook) logs it once per desktop; a capture thread
that cannot then ends, the agent tells the helper (`{"ended": stream}`), the session's frames end
and it opens the video again on a fresh thread ([computer-use.md](computer-use.md), "Video"). The
helper passes the session's room for frames to the agent with the frame rate and bitrate
(`video.targets`), so the agent captures only what the session can send; its channel from the agent
holds as many frames as the host keeps ahead. The agent writes its log — the core's lines and
any crash trace — to `logs\agent.log` in the helper's state folder unless No logs is on
([security.md](security.md), "Logging"); changing No logs restarts the agent, which follows the
choice (a GUI that attaches sends its choice again, which changes nothing and restarts nothing).
The macOS agent, whose user cannot write the helper's folder, sends its log lines over the link
and the helper writes them to its own `helper.log`. The helper's operator reads these logs
without administrator rights through `logs.read` (Diagnostics, **Save logs…**; `ctl logs`).

They talk over two one-way named pipes (synchronous I/O on one pipe handle is serialized, so a
waiting read would block every write), each with a random name, created as the first instance
with a SYSTEM-only DACL; the helper checks that the client is the agent process it started, and
the agent that the server is the helper named on its command line. Messages are a u32 length, a
kind (JSON request or reply, a video frame, a screenshot) and the body. A person's input goes to
the agent without waiting for an answer, and the pointer is asked for on a thread of the session's
own, so neither holds up the network's threads. Only encoded video, input
events, clipboard text, clipboard images this computer copied (as PNG, encoded by the agent), the
paths of copied files and the AI's screenshots cross. Both processes are privileged and never decode remote media, so in the
helper modes clipboard images from a controller are not taken. Files on the clipboard are read
and written by the helper while it impersonates the console's signed-in user
([computer-use.md](computer-use.md), "Files"). Without an agent, video shows the
placeholder (Kyokai Helper getting ready) while the session tries again ([computer-use.md](computer-use.md),
"Video"), input is refused, and an AI task reads it as the host user taking over. An open that
the agent answers too late is closed there again, a wake asked for while no agent is attached
reaches the next one, and only the newest agent's connection ending takes the link down.

On macOS launchd starts the agent, not the helper: it connects to `agent.sock` in the helper's
folder (root-owned, so only root can hold it; the agent checks that the peer is root), and the
helper takes a connection only from its own `kyokai-svc` (`LOCAL_PEERPID`, `proc_pidpath`) run by
the user at the screen or by root at the login window; the newest agent replaces an older one,
and the older one's connection ending later leaves the newer one's streams alone.
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
In the helper modes the helper holds the only copy of the identity, so removing the app removes
it too. A helper installed again before Windows restarts first drops the deletions an earlier
removal scheduled under its folders, which would otherwise take its new files at that restart.

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
until a store is moved into it. On Linux the helper, its units, the agent's link and the sealed key
are implemented and the link's peer checks tested; it has not been installed on a machine yet.
