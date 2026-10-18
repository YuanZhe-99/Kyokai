# Security

## Secrets store

- The core keeps every secret in one JSON map encrypted with XChaCha20-Poly1305 (24-byte random
  nonce, associated data `kyokai/secrets/v1`) in `secrets.bin`, written atomically.
- The 32-byte master key comes from the operating system: on Windows a DPAPI blob for the current
  user (`master.key`), on macOS a generic password in the login Keychain (service
  `com.yuanzhe.kyokai`, account `master-key` or `master-key.<profile>`; the macOS helper, as root,
  in the System keychain, deleted with its state).
- Without a working OS store nothing is persisted: secrets live in memory for that run and the
  interface says the Device ID will change. A store that exists but cannot be decrypted is never
  overwritten; that run uses a temporary identity and says so, and Start over moves it aside
  (`secrets.bin.unreadable-<time>`, kept) for a new store and identity kept from then on. On macOS a new Keychain item is
  created only when there is none: when the user denies access to the existing one, nothing is
  replaced, the run keeps secrets in memory (persistence `denied`), and Allow on the Keychain row of
  the permissions card opens the store again — macOS asks again — and rebuilds the core on it.
- Secrets are zeroized when dropped; `kyokai_stop` drops them all.
- The store holds the device key, the network settings with the network secret
  ([networking.md](networking.md)), the OPAQUE server key and the permanent password's record, the
  trusted devices, the known hosts and the hosts' saved passwords
  ([identity-and-access.md](identity-and-access.md)) and the AI
  providers' API keys. The local API carries the network secret only from the GUI (or
  `ctl`) to the engine, never back; `ctl` reads it from the environment, not the command line. The same
  holds for the permanent password and the password typed to connect (`KYOKAI_PERMANENT_PASSWORD`,
  `KYOKAI_PASSWORD`); the core keeps a typed password in memory only until that session ends and then zeroizes it.
  A provider key travels the same way (`KYOKAI_AI_KEY` for `ctl ai-key`) and is never returned.

## Clipboard

Sharing needs the `clipboard` permission, starts with copies made after the session began and
never offers text that Kyokai itself copied ([computer-use.md](computer-use.md)).

Files need the separate `files` permission. The receiving side checks every offered path (no
absolute paths, `..`, reserved characters or device names, no duplicates) and the limits (1000
entries, 1 GiB) before anything is written, writes only inside a folder created for the session
with names it creates new (never overwriting), and deletes that folder when the session ends.
The sender never follows links or junctions. In the helper modes the files are read and written
while impersonating the console's signed-in user, so a path put on the clipboard grants nothing
that user could not read.

## AI tasks

- The model runs on the host's provider account: each source's key goes only to that source, over
  HTTPS (plain HTTP is allowed only to a server on this computer, which needs no key),
  and never into a log, an error, the status, the history or another device.
- Screenshots exist in memory for the task's conversation and go only to the chosen provider; the
  history (`ai-tasks.json`) keeps instructions, the model's text and the actions, never a picture.
  Kyokai's own windows are excluded from capture (`SetContentProtection`), so the AI page and
  approvals never reach the model. The host user can turn this off in Settings (General, "Hide
  Kyokai from screen capture", on by default) to see and change Kyokai through another
  remote-desktop tool on an unattended computer; the windows then appear in every capture,
  including Kyokai's own video and the AI's screenshots. Input still never reaches them.
- The model's proposals are only input: every action is parsed, bounds-checked against the latest
  screenshot and executed by the host's own executor; nothing runs commands or code, and nothing
  is injected into a Kyokai window (pointer input by the window under it, keys by the focused
  window).
- Local input, pause, cancel and the emergency stop revoke the task's input lease at once; a
  response that arrives afterwards never acts ([computer-use.md](computer-use.md)).
- A controller's task runs with the host's key under the host's approval policy; the controller
  needs `run-ai-task` to start or steer and `approve-ai-action` to approve, and only for the tasks
  it started. The host's user sees every task and can pause, stop or approve any of them.

## Device identity

- An Ed25519 signing key, created with the OS random source and kept in the secrets store.
- The Device ID is derived from the public key: SHA-256 over `kyokai/device-id/v1` and the key, its
  first 8 bytes as a number, reduced into 100 000 000–999 999 999. It locates a device; it never
  proves one. The fingerprint (SHA-256 of the public key) is the full identity.

## Temporary password

8 symbols from `abcdefghijkmnpqrstuvwxyz23456789` (no 0, 1, l, o), about 40 bits, from the OS
random source. It exists only in the core's memory, is new on every start and on request, and
reaches the GUI only when the user shows or copies it; a random serial tells the GUI it changed
without revealing it.

## Logging

Logs record versions, the Device ID and how secrets are kept — never a password, key or secret.
The engine writes `logs/engine.log` in the state directory. Audited on 2026-10-17: the engine logs
its start (version, Device ID, how secrets are kept) and why it stops (lifeline or parent gone);
the core writes no log; a provider's error carries its status and message, never the key.

## Local API

Only the engine's own user is served (peer credentials, never the request body); see
[architecture.md](architecture.md). The privileged helper's socket can be opened by every local
user, and the helper serves only SYSTEM, its operator and the `kyokai` group, judged by the
caller's token (its user and its enabled groups); an administrator outside them is refused too
([run-modes.md](run-modes.md)).

## Threat model

| Asset | Threat | Mitigation |
|---|---|---|
| A session with a host | a network member or relay impersonating a device, reading or changing traffic | Noise XX end to end over EasyTier, each static key signed by the device key; host keys pinned on first use; a changed key is refused |
| The host's passwords | guessing them online or offline | OPAQUE (nothing to guess offline from the wire), rate limits per address with a global backoff that spares pinned keys, the host's confirmation for anything beyond the password grants |
| Permissions | a controller doing more than it was granted | each request checked against the session's permissions on the host; no permission implies another; network membership grants none |
| Provider keys and secrets | leaking through logs, the status, sessions or files | only towards the engine on the local API, kept in the secrets store, never logged or returned |
| The host's desktop under AI | a model (or text on the screen steering it) acting against the user | proposals are only input, validated and executed by the host; approvals; local input and take-over pause at once; late responses never act; Kyokai's windows are neither captured nor injected into |
| Old messages | replaying input, commands or logins | Noise nonces; resume tokens single-use, bound to the controller's key and valid 2 minutes; commands never re-sent after a reconnect; idempotent task start |
| A revoked device | coming back | revoking ends its live sessions and drops its resume tokens; its task pauses when its session ends |
| The local API | another local user driving the engine | peer credentials, the engine's own user only |
| The helper | a local user driving the SYSTEM service, or swapping what it runs | peer credentials, the operator and the `kyokai` group only; its files are administrator-owned copies; the state folder is SYSTEM and Administrators only |
| Helper updates | a local user swapping the app's files to have SYSTEM run them | the release key's signature over every file's hash, read once and installed as verified; unsigned builds need an administrator prompt |
| The session agent | a local process posing as the helper or the agent to inject as SYSTEM, or feeding them media to decode | random pipe names created as the first instance, SYSTEM-only DACLs, each side checks the other's process id; neither decodes remote media |
| Files on the clipboard | a peer writing outside the receiving folder, overwriting files, filling the disk, or making the helper read files as SYSTEM | the `files` permission; every path checked (no `..`, absolute paths, reserved names or devices) and written only as new files inside the session's folder, deleted when the session ends; 1000 entries and 1 GiB per offer, checked on both sides and while receiving; the helper reads and writes as the console's user and never interprets the bytes |
| The host user's control | a controller locking them out with the input block or privacy mode | both need `control-input`, end with the session, and the host user lifts them with Ctrl+Alt+Shift+Esc (seen by the block's own hook) or Ctrl+Alt+Del (never hookable); Ctrl+Alt+Del is sent only by the helper, with the services policy switched on just for the call |

Out of scope: malware running as the same user (it can drive the desktop anyway), someone at the
unlocked desktop, and a compromised provider.

## Known limitations

- On a machine without a working credential store (or with it locked) the identity is not
  persisted.
- The temporary password can be shown and copied by anyone at the unlocked desktop; the tray never
  shows it.
- A device that knows the password gets the password grants without anyone confirming; turning the
  grants off in Settings, Access (or a password change) keeps it out. A device without the password
  can still ask, and is held back by the confirmation and the rate limits.
- An AI task sends the screen to the provider the host's user chose, including whatever other
  apps show; the approval policy and local input are defence in depth, not a classifier of every
  sensitive action.
