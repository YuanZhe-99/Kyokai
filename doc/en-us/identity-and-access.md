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
them.

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
  Device ID; later sessions with another key are refused until the user forgets the host.

## Rate limits

| Limit | Value |
|---|---|
| Failed logins per source address | 5 in 10 minutes, then that address is refused for 10 minutes |
| All failed logins | more than 20 in a minute makes every unpinned controller wait 30 seconds; pinned keys are exempt, so guessing cannot lock the real user out |

Limits are counted per source virtual address, since keys cost nothing to create before they are
pinned.
