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

After a correct password the host's user sees the controller's Device ID, whether its key is known,
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

## Identity backup, restore and reset

A backup holds the device key, the OPAQUE server key and permanent password record, the trusted
devices and the known hosts, as JSON encrypted with XChaCha20-Poly1305 under a key from Argon2id
(64 MiB, 3 passes) over a passphrase of at least 10 characters. Restoring checks the whole file
before writing; reset forgets all of these, giving a new Device ID. Both rebuild the core at once:
sessions end and the network reconnects. Network settings are not part of the identity.

## Rate limits

| Limit | Value |
|---|---|
| Failed logins per source address | 5 in 10 minutes, then that address is refused for 10 minutes |
| All failed logins | more than 20 in a minute makes every unpinned controller wait 30 seconds; pinned keys are exempt, so guessing cannot lock the real user out |

Limits are counted per source virtual address, since keys cost nothing to create before they are
pinned.
