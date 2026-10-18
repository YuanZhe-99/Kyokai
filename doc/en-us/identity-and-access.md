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
returned: the status says only which recent hosts have one (`saved_password`). An empty password
field then uses it. When the host refuses a saved password (it changed), the controller forgets it,
and Forget password on the recent row forgets it at once (`password.forget_saved`).

## Permissions

`view-screen`, `control-input`, `run-ai-task`, `approve-ai-action`, `clipboard`, `files`
(copying files through the clipboard; `clipboard` does not include it), `hear-sound` (the host's
sound; `view-screen` does not include it). A controller asks
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
| asks without a password | asks its user; a denial or no answer counts as a failed login |
| gives the permanent password with nobody at the computer | admits it with what a password grants (see Unattended access) |
| gives a correct password of a kind the host does not accept | refuses it as a wrong password |

Which passwords open the host is chosen in Settings, Access ("Accepted passwords": Temporary,
Permanent or Both, by default Both); "Connect without asking" (on by default) lets an accepted
password admit without confirmation, and off, every connection asks. What a password grants
without asking is chosen there too ("With a correct password"),
one switch per permission, kept in the secrets store: by default `view-screen`, `control-input`,
`clipboard`, `files` and `hear-sound` (a controller asks for these by default too); the AI permissions always ask unless turned on there.

## Host confirmation

The host's user sees the controller's friendly name and Device ID, whether its key is known, whether
it gave no password, and the permissions it asks for, and allows (choosing permissions) or denies
within 60 seconds. Silence denies. At most one confirmation is open per controller key and 4 in
total; further attempts are answered `busy`.

## Pinned keys

- **Host side (trusted devices):** an accepted controller's device key is recorded with its Device
  ID, the first and last connection time and the permissions last granted. A controller whose
  Device ID is known with another key is shown as "a new key claiming this ID".
- **Controller side (known hosts):** the first accepted session pins the host's device key for its
  Device ID; later sessions with another key are refused until the user forgets the host. Each
  accepted session also records when, and the newest 8 are offered as recent devices.

## Friendly names

Each device has a friendly name, set in Settings (Device identity; at most 64 characters; empty
uses the computer's name: `COMPUTERNAME` on Windows, the Sharing name on macOS) and kept in
`device.json` in the state folder. With it a device describes its hardware maker and model (SMBIOS
`SystemManufacturer` and `SystemProductName` on Windows; Apple and `hw.model` on macOS) and its
system. The controller sends this description in its `login`, the host in an accepting `verdict`
([protocol.md](protocol.md)), so nothing reaches a device before the password check and the EasyTier
hostname stays `Kyokai-<Device ID>`. A received description is cleaned (control characters and
bidirectional overrides removed, each field cut to 64 characters) and stored with the trusted device
(host side) or the known host (controller side); a resumed session keeps the last one. It is a
claim of the other device: the interface shows it beside the Device ID, never instead of it, and it
authenticates nothing.

## Identity backup, restore and reset

A backup holds the device key, the OPAQUE server key and permanent password record, the trusted
devices and the known hosts, as JSON encrypted with XChaCha20-Poly1305 under a key from Argon2id
(64 MiB, 3 passes) over a passphrase of at least 10 characters. Restoring checks the whole file
before writing; reset forgets all of these, giving a new Device ID. Both rebuild the core at once:
sessions end and the network reconnects. Network settings are not part of the identity.

## Unattended access

With nobody at the computer — a Background service helper without a GUI attached — the permanent
password admits a device without confirmation while Connect without asking is on, with what a
password grants within what it asks for; the temporary password, a request without a password and
a new key claiming a trusted Device ID find nobody to ask. Background service is itself chosen
explicitly (the helper is installed with an administrator prompt), so a computer is unattended
only when its user set it up so.

Whom the host serves depends on its run mode ([run-modes.md](run-modes.md)): everyone in No
privileges and while a GUI is attached to the helper; without a GUI, a Background service helper
serves only the permanent password and a Follow GUI helper no one (its host sessions end). A login
that nobody can confirm is refused at once with `absent`, before the password check when no one is
served.

## Rate limits

| Limit | Value |
|---|---|
| Failed logins per source address | 5 in 10 minutes, then that address is refused for 10 minutes; a denied or unanswered request without a password counts as one |
| All failed logins | more than 20 in a minute makes every unpinned controller wait 30 seconds; pinned keys are exempt, so guessing cannot lock the real user out |
| Incoming connections in their handshake or login | 16 at once; more are dropped |
| Connected sessions on a host | 4 at once (each may hold a capture and an encoder); more are refused as busy |
| Host confirmations waiting | 4; more are refused as busy |

Limits are counted per source virtual address, since keys cost nothing to create before they are
pinned.
