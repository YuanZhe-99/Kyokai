# Identity and access

Who may connect to a host, with what, and how the host decides. The handshake itself is in
[protocol.md](protocol.md); keys and the secrets store are in [security.md](security.md).

## Passwords

| Slot | Lifetime | Where its OPAQUE record lives |
|---|---|---|
| Temporary | new on every engine start and on request | memory only |
| Permanent | set by the host's user, removable | the secrets store |

The host never stores or receives a password, only OPAQUE records; the OPAQUE server key
(`ServerSetup`) is created once and kept in the secrets store. A controller never learns which
slots are set.

## Saved passwords

A controller may keep a host's password (Remember password on the connect form,
`session.connect` `remember`). It is kept only after a login the host accepts, in the secrets
store under `saved_password.<Device ID>`, encrypted like every other secret and never shown or
returned: the status says only which recent hosts have one (`saved_password`, and `saved` with a
flag per kind of sign-in kept). An empty password field then uses it. When the host refuses a saved
password (it changed), the controller forgets it. What else is kept for a host — the sign-in key
it admitted (see Keys) and its services' sign-ins ([remote-services.md](remote-services.md)) —
lives under `saved.<Device ID>`; Forget password on the recent row
(`password.forget_saved`) and removing the device forget both at once.

## Keys

SSH-style Ed25519 keys sign in to Kyokai without a password ([protocol.md](protocol.md), "Key
login").

- **Keys that can connect** (host side, `authorized_keys` in the secrets store, at most 32):
  OpenSSH public key lines (`ssh-ed25519 AAAA… comment`), each with a name (the comment unless one
  is given), when it was added, when it last signed in and whether it is turned off (`off`; a key
  is on when added, and a store without the flag reads as on); other key types are refused
  (`authorized.list`, `authorized.add`, `authorized.set` (`{id, off}`: turns it off or on, keeping
  it), `authorized.remove`). In Permissions each is a way to sign in with its own check box.
- **This device's keys** (controller side, `login_keys`, at most 16): Ed25519 keys sign in to
  Kyokai; Ed25519, RSA (2048 bits or more) and ECDSA P-256 or P-384 keys sign in to SSH servers
  through the built-in client. The GUI generates or imports a key and hands its private part (the
  Ed25519 seed or the PKCS#8 DER) to the engine once (`keys.add`); the engine checks it, keeps it
  in the secrets store and never returns it: `keys.list` answers names, types and public lines. A
  key is kept once: its id is the first 16 hex digits of the SHA-256 of its public key.
- A controller offers keys only to a host that announces `key-login`, and never on a resume: every
  Ed25519 key (at most 8), the one the host admitted last first. The host admits with the first
  authorized key whose proof holds; keys it does not know, and keys turned off, are ignored (no
  failed login; the controller falls back to a password or to asking), and an authorized key whose
  proof fails counts as a failed login.
- A key counts like the permanent password: it admits within the password grants while Connect
  without asking is on, whichever passwords are accepted, and with nobody at the computer (see
  Unattended access); it never turns protected mode off. When more is asked for, the host's user
  is asked and sees the key's name.
- The controller remembers the key a host admitted and offers it first next time, its other keys
  after it (a host that dropped it still finds the one it takes); when the host admits another way,
  it forgets it. A typed password is saved only when the host admitted
  with it.
- `keys.sign` (`{key_id, algorithm, data}`) signs only an SSH public key user-auth request
  (RFC 4252, section 7) for that very key, with `ssh-ed25519`, `rsa-sha2-256`, `rsa-sha2-512`,
  `ecdsa-sha2-nistp256` or `ecdsa-sha2-nistp384` as the key's kind allows, and answers the SSH
  signature (string format, string blob; base64); anything else is refused, so the engine is no
  general signing oracle. It is served only to the engine's owner or the helper's operator, never
  to the `kyokai` group.

## Permissions

`view-screen`, `control-input`, `run-ai-task`, `approve-ai-action`, `clipboard`, `files`
(copying files through the clipboard; `clipboard` does not include it), `hear-sound` (the host's
sound; `view-screen` does not include it), `see-status` (a quiet watch of the host's status,
[computer-use.md](computer-use.md), "Host status") and `use-ssh`, `use-rdp` and `use-vnc` (a tunnel to the
host's own SSH, Remote Desktop or VNC, [remote-services.md](remote-services.md)). A controller asks
for a set; the host's user grants a subset; none implies another. Network membership grants none of
them. `run-ai-task` lets a controller start AI tasks on the host, which run with the host's key, and
steer the host's task whoever started it (the host keeps one AI record for every controller);
`approve-ai-action` lets it answer that task's approvals
([computer-use.md](computer-use.md)). Removing a trusted device revokes it at once: its live
sessions with this host end and its resume tokens are dropped.

## Admission

| The controller | The host |
|---|---|
| gives a correct, accepted password and asks only for what a password grants | admits it at once with what it asked for (while Connect without asking is on) |
| gives a correct password and asks for more | asks its user |
| gives a correct password with a new key claiming a Device ID trusted under another key | asks its user |
| proves a key that can connect and asks only for what a password grants | admits it at once with what it asked for (while Connect without asking is on) |
| proves a key that can connect with a proof that fails | refuses it as a wrong password |
| offers only keys the host does not know or has turned off | ignores them, as if none were offered |
| watches the status with a password or key within the grants | admits it at once (while Connect without asking is on); never asks: anything else is refused as `denied` |
| asks without a password | asks its user; a denial or no answer counts as a failed login |
| gives the permanent password with nobody at the computer | admits it with what a password grants (see Unattended access) |
| gives a correct password of a kind the host does not accept | refuses it as a wrong password |

Which passwords open the host is chosen in Permissions, under "Ways to sign in": a check box for
the temporary password and one for the permanent password, in any combination (`password_mode`:
`temporary`, `permanent`, `both` or `none`; by default both). With `none` neither password opens
the host: only a key that can connect, or asking the host's user. An engine of 0.5.1 or before
reading a stored `none` falls back to its default, both. "Connect without asking" (on by default)
lets an accepted password or a key admit without confirmation, and off, every connection asks. What a password grants
without asking is chosen there too ("With a correct password"),
one switch per permission, kept in the secrets store: by default `view-screen`, `control-input`,
`clipboard`, `files`, `hear-sound` (a controller asks for these by default too) and `see-status`; the AI
permissions and the services always ask unless turned on there. A host whose grants an older version
stored gets a new permission's default once, as it starts (`password_grants_known` records which
permissions the stored grants were chosen over): `see-status` on, the services off.

## Host confirmation

The host's user sees the controller's friendly name and Device ID, whether its key is known, whether
it gave no password, the name of the sign-in key it proved, the permissions it asks for and a
tunnel's service, and allows (choosing permissions) or denies
within 60 seconds. Silence denies. At most one confirmation is open per controller key and 4 in
total; further attempts are answered `busy`.

## Pinned keys

- **Host side (trusted devices):** an accepted controller's device key is recorded with its Device
  ID, the first and last connection time and the permissions last granted. A controller whose
  Device ID is known with another key is shown as "a new key claiming this ID".
- **Controller side (known hosts):** the first accepted session pins the host's device key for its
  Device ID; later sessions with another key are refused until the user forgets the host. Each
  accepted session also records when, and adds the address it dialed to the host's routes
  ([networking.md](networking.md), "Routes": an address is only where to look; another device
  answering there fails the handshake like any squatter), and the newest 8 are offered as recent
  devices. Connecting by address, the person names no Device
  ID: the first session pins whichever device answered there, and its Device ID shows in the tab
  and on Recent. Each accepted session also records what the host announced (`features`), which
  the status passes on (`recent[].features`) so Home knows what a host offers before dialing.
  Removing a recent device forgets its pinned key and everything saved for it and takes its
  sessions off the list.
- **No logs** keeps both lists in memory only ([security.md](security.md)): after a restart no
  key is pinned, so a changed key is not noticed.

## Friendly names

Each device has a friendly name, set in Security (Device identity; at most 64 characters; empty
uses the computer's name: `COMPUTERNAME` on Windows, the Sharing name on macOS) and kept in
`device.json` in the state folder. With it a device describes its hardware maker and model (SMBIOS
`SystemManufacturer` and `SystemProductName` on Windows; Apple and `hw.model` on macOS) and its
system. The controller sends this description in its `login`, the host in an accepting `verdict`
([protocol.md](protocol.md)), so nothing reaches a device before the password check and the EasyTier
hostname stays `Kyokai-<Device ID>`. A received description is cleaned (control characters and
bidirectional overrides removed, each field cut to 64 characters) and stored with the trusted device
(host side) or the known host (controller side); a resumed session keeps the last one. It is a
claim of the other device: the interface shows it beside the Device ID, never instead of it, and it
authenticates nothing. The user may also give a device a name of their own (Recent, Rename…),
kept only in the GUI's settings on this device and shown instead of the device's friendly name,
again beside its Device ID.

## Protected mode

Security, **Protected mode** (off by default) leaves this computer's Kyokai offering only the
AI settings and AI tasks, for a computer whose local user should run AI tasks but change nothing
else. It turns on only while a permanent password is set (after a confirmation) and off only with
that password, entered on the AI page: the core checks it on this computer by running both sides
of an OPAQUE login against the permanent record (`Slots::check_permanent`; the temporary password
does not count), and refuses removing the permanent password while the mode is on. The choice
(`protected`) is kept in the secrets store, so it survives restarts and moves with the store. The
engine enforces it on the local API, so `ctl` is held to it too: while on it serves only the status,
the AI requests, answering confirmations, disconnecting, quitting, turning the mode off, and what the
GUI sends as it attaches (capture, sound, No logs, privacy permissions, the helper's update). The
host user stays in charge: confirmations and the devices connected here still show on every page.

## Identity backup, restore and reset

A backup holds the device key, the OPAQUE server key and permanent password record, the trusted
devices and the known hosts, as JSON encrypted with XChaCha20-Poly1305 under a key from Argon2id
(64 MiB, 3 passes) over a passphrase of at least 10 characters. Restoring checks the whole file
before writing; reset forgets all of these, giving a new Device ID. Both rebuild the core at once:
sessions end and the network reconnects. Network settings are not part of the identity.

## Unattended access

With nobody at the computer — a Background service helper without a GUI attached — the permanent
password or a key that can connect admits a device without confirmation while Connect without asking is on, with what a
password grants within what it asks for; the temporary password, a request without a password and
a new key claiming a trusted Device ID find nobody to ask. Background service is itself chosen
explicitly (the helper is installed with an administrator prompt), so a computer is unattended
only when its user set it up so.

Whom the host serves depends on its run mode ([run-modes.md](run-modes.md)): everyone in No
privileges and while a GUI is attached to the helper; without a GUI, a Background service helper
serves only the permanent password and keys, and a Follow GUI helper no one (its host sessions end). A login
that nobody can confirm is refused at once with `absent`, before the password check when no one is
served.

## Rate limits

| Limit | Value |
|---|---|
| Failed logins per source address | 5 in 10 minutes, then that address is refused for 10 minutes; a denied or unanswered request without a password counts as one |
| All failed logins | more than 20 in a minute makes every unpinned controller wait 30 seconds; pinned keys are exempt, so guessing cannot lock the real user out |
| Incoming connections in their handshake or login | 16 at once; more are dropped |
| Connected sessions on a host | 4 at once (each may hold a capture and an encoder); more are refused as busy |
| Tunnels on a host | 8 at once, besides the desktop sessions; more are refused as busy |
| Status watchers on a host | 16 at once, besides the other sessions; more are refused as busy |
| Host confirmations waiting | 4; more are refused as busy |

Limits are counted per source virtual address, since keys cost nothing to create before they are
pinned: per network and address, since EasyTier's address ranges repeat from one network to the
next and one network's guesses must not lock out a device in another (connections by address
count as a network of their own).
