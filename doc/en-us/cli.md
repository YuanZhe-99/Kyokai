# Command line

`kyokai` is Kyokai's command line for people and scripts (package `internal/ctl`). Each command is
one request to the user's own engine over the local API ([architecture.md](architecture.md#local-api))
and prints its answer; it is a client like the GUI, with no trust of its own and nothing it may do
that the GUI may not.

## Running it

| Platform | Path |
|---|---|
| Windows | `bin\kyokai.exe` in the app's folder (`%LOCALAPPDATA%\Programs\Kyokai\bin\kyokai.exe` when installed) |
| macOS | `Kyokai.app/Contents/Resources/bin/kyokai` |
| Linux | `bin/kyokai` in the app's folder (`/opt/kyokai/bin/kyokai` from the Debian package) |

It is not put on `PATH`; people who want it there add the folder or a link themselves.
`kyokai-svc ctl COMMAND …` is another name for the same commands. A command talks to the engine
the GUI started (`--socket` names another; `KYOKAI_PROFILE` picks a development instance's), and
to the privileged helper's socket only when the helper listens there.

`kyokai help` lists the commands; `kyokai help COMMAND` and `kyokai COMMAND --help` explain one
and the flags it takes. Flags come after the command.

## Output and exit codes

Output is for people by default. `--json` prints the engine's answer as the local API's JSON
response instead (`screenshot`: the file and its size; `move`: the device and the socket); the
commands that print as things arrive (`watch`, `watch-status`, `tunnel`, `logs`) do not take it.
`backup --json` still writes the identity only to its file.

| Code | Meaning |
|---|---|
| 0 | done (also: help shown) |
| 1 | anything else went wrong |
| 2 | an unknown command or a wrong flag |
| 3 | no engine listens on the socket |
| 4 | the engine refused the request; the message says why |

## Secrets

Secrets come only from the environment or standard input, never from arguments, where other
processes could read them: `--secret-stdin` reads the command's secret from the first line of
standard input instead of its variable.

| Variable | Read by |
|---|---|
| `KYOKAI_PASSWORD` | `connect`: the host's password (empty asks without one) |
| `KYOKAI_PERMANENT_PASSWORD` | `permanent`; `protect` when turning protected mode off |
| `KYOKAI_NETWORK_SECRET` | `network-set`, `network-profile` (unset keeps the stored one) |
| `KYOKAI_BACKUP_PASSPHRASE` | `backup`, `restore` |
| `KYOKAI_AI_KEY` | `ai-key` |
| `KYOKAI_SERVICE_PASSWORD` | `credentials-set` |

## Commands

| Group | Commands |
|---|---|
| This device | `status`, `password`, `regenerate`, `permanent`, `name-set`, `access`, `password-grants`, `trusted`, `untrust`, `backup`, `restore`, `reset-identity` |
| Networks | `network`, `network-set`, `networks`, `network-profile`, `network-remove`, `network-direct`, `network-backend`, `cores`, `core-install` |
| Sessions and devices | `connect`, `identify`, `close`, `allow`, `deny`, `forget`, `remove`, `devices`, `route-add`, `route-remove`, `route-prefer`, `screenshot`, `watch`, `watch-status` |
| AI | `ai`, `ai-set`, `ai-key`, `ai-models`, `ai-start`, `ai-reply`, `ai-tasks`, `ai-follow`, `ai-rename`, `ai-fork`, `ai-compact`, `ai-export`, `ai-pause`, `ai-resume`, `ai-cancel`, `ai-approve`, `ai-decline`, `ai-apps`, `ai-scope`, `ai-clear`, `ai-stop` |
| Keys, tunnels and sign-ins | `keys`, `keys-generate`, `keys-remove`, `authorized`, `authorized-add`, `authorized-remove`, `tunnel`, `credentials-set`, `credentials-get`, `credentials-forget` |
| The engine | `background`, `helper-update`, `move`, `capture-show`, `no-logs`, `protect`, `clipboard-mine`, `logs`, `shutdown` |
| Itself | `help`, `version` |

`devices` lists the devices this one connected to with their kept addresses; the names the GUI
gives devices are the GUI's own and stay there. `tunnel --listen PORT` serves a host's SSH, Remote
Desktop or VNC on this computer's loopback (never another address), a tunnel per connection;
without it the tunnel is standard input and output, as OpenSSH's `ProxyCommand` wants, which also
carries files: `sftp` and `scp` with `-o ProxyCommand="kyokai tunnel --device ID --service ssh"`.
`ai-follow --id N` prints a task's steps and states as they come, until it ends.

`watch` is for testing the stream (frames per second, gaps, the host's reports, injected input); the
others are what the GUI does, one request each.

## Screenshots

`kyokai screenshot --id N --out FILE.png [--display D]` saves the first whole picture of
controller session N (the number `connect` printed) as a PNG file. It is the only way the command
line shows a host's screen: nothing is followed or written unless a command asks, and only to the
path it names. It needs the session's `view-screen` grant, as the viewer does; it asks the host
for a key frame and decodes it with the viewer's decoder, so a still screen still gives a picture,
and ten seconds without one is a failure.

## AI scope

`kyokai ai-apps` lists the programs with windows on this device's screen, by the names the AI scope
uses; `kyokai ai-scope --apps A,B` grants the AI only those programs' windows for this session, and
`kyokai ai-scope` with no apps gives it the whole desktop again ([computer-use.md](computer-use.md#ai-scope)).
The scope is the host user's to set, so these commands speak to this device's own engine only.
