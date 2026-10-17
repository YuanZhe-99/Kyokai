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

## Permissions

`view-screen`, `control-input`, `run-ai-task`, `approve-ai-action`, `clipboard`. A controller asks
for a set; the host's user grants a subset; none implies another. Network membership grants none of
them. `run-ai-task` lets a controller start and steer its own AI tasks on the host, which run with
the host's key; `approve-ai-action` lets it answer their approvals
([computer-use.md](computer-use.md)). Removing a trusted device revokes it at once: its live
sessions with this host end and its resume tokens are dropped.

## Host confirmation

After a correct password the host's user sees the controller's friendly name and Device ID, whether its key is known,
and the permissions it asks for, and allows (choosing permissions) or denies within 60 seconds.
Silence denies. At most one confirmation is open per controller key and 4 in total; further
attempts are answered `busy`.

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

The host's user can let a trusted device connect without confirmation for 30 days (Settings,
Access: "Connect without asking"; at most 365 days through the local API). Such a login needs the
device's pinned key **and** the permanent password (the temporary one always asks), gets the
permissions last granted to that device within what it asks for, and stops once the grant
expires or is switched off. Off by default; nothing else skips the confirmation.

Whom the host serves depends on its run mode ([run-modes.md](run-modes.md)): everyone in No
privileges and while a GUI is attached to the helper; without a GUI, a Background service helper
serves only unattended grants and a Follow GUI helper no one (its host sessions end). A login that
nobody can confirm is refused at once with `absent`, before the password check when no one is
served.

## Rate limits

| Limit | Value |
|---|---|
| Failed logins per source address | 5 in 10 minutes, then that address is refused for 10 minutes |
| All failed logins | more than 20 in a minute makes every unpinned controller wait 30 seconds; pinned keys are exempt, so guessing cannot lock the real user out |
| Incoming connections in their handshake or login | 16 at once; more are dropped |
| Connected sessions on a host | 4 at once (each may hold a capture and an encoder); more are refused as busy |
| Host confirmations waiting | 4; more are refused as busy |

Limits are counted per source virtual address, since keys cost nothing to create before they are
pinned.
