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

## Current implementation

The helper's install, uninstall and status commands, the service and its authorization are
implemented on Windows; the macOS helper (a root LaunchDaemon) comes with the Mac pass and reports
"not available yet" until then. The run-mode choice in Settings, the session agent that captures
and injects on the console session's desktop, moving the identity into the helper, its signed
self-update and its self-disable follow (`PLAN.md`, Phase 6). Until then an installed helper runs
an engine of its own with a separate Device ID, which is for development only.
