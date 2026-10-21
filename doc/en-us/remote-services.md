# Remote services

A controller reaches a host's own SSH server, Remote Desktop or VNC (Screen Sharing on macOS)
through a tunnel inside a Kyokai session, to the host's loopback. Nothing listens on the network
for them: the services stay where they are, reached only by whoever Kyokai admitted.

## Services

| Service | Permission | Port on the host |
|---|---|---|
| `ssh` | `use-ssh` | 22 |
| `rdp` | `use-rdp` | 3389 |
| `vnc` | `use-vnc` | 5900 |

The host connects to 127.0.0.1, then ::1, on the service's port. Its user turns a service on
(SSH in Windows' optional features or macOS's Remote Login, Remote Desktop, a VNC server or Screen
Sharing); Kyokai never changes them.

## Admission

Each service is a permission like the others ([identity-and-access.md](identity-and-access.md)):
none implies another, and a tunnel asks for its own alone. They are listed under "With a correct
password" and off by default. Switched on there, a correct Kyokai password or a key that can
connect reaches the service without asking, and so does an unattended host; off, the host's user
is asked, who sees the service in the confirmation, and with nobody at the computer the tunnel is
refused (`absent`). A controller with neither a saved password nor a key asks without one.

A tunnel is admitted only once the host reached its service, within 3 seconds; otherwise it is
refused as `service-down`. A host serves at most 8 tunnels besides its 4 desktop sessions (more
are refused as `busy`); each keeps the host awake like a desktop session and shows in Connected
here with its service. A tunnel is never resumed: a dropped one ends, and the client reconnects.

## Tunnels

One session carries one tunnel ([protocol.md](protocol.md), "Tunnels"): the controller names the
service in its hello, only to a host that announced `tunnel`, and the bytes then travel as
plaintexts tagged `0x05`. Every buffer on the way is bounded — 16 pieces of up to 64 KiB each way
between the service and the session, and between the session and the GUI — so a fast service
waits for a slow reader instead of filling memory, and the two directions never wait on each
other. A tunnel with no frame from its peer for 60 seconds ends. In the helper modes the helper
relays the bytes and decodes nothing.

On the controller the GUI opens a tunnel with `tunnel.open` (`{device_id, address?, service}`)
on the local API: the engine dials the host like a desktop session, with the saved password if
there is one, else the sign-in keys, else asking; waits up to 90 seconds for the host (its user may
be asked); and answers OK, after which the connection carries the bytes as packets of kind 14
both ways, until either side closes it. A refusal names why: `unsupported` (the host announced no
tunnels: nothing was asked of it), `service-down`, `denied`, `timeout`, `absent`, `busy`,
`password`, `rate-limited`, `unreachable`, `key-changed`, `wrong-device`, `incompatible`,
`broken` or `closed` (it ended before it connected). `localapi.Client.Tunnel` returns the bytes as a `net.Conn`. `kyokai-svc ctl tunnel
--device ID --service ssh` pipes a tunnel to its standard input and output, for OpenSSH's
`ProxyCommand`:

```bash
ssh -o ProxyCommand="kyokai-svc ctl tunnel --device 482916735 --service ssh" me@kyokai
```

## Saved sign-ins

The controller keeps each service's sign-in to a host under `saved.<Device ID>` in its secrets
store, next to the Kyokai password and the key the host admitted
([identity-and-access.md](identity-and-access.md), "Saved passwords"): for SSH a user, a
password or a sign-in key, and the server's key pinned on first use; for Remote Desktop a user, a
domain and a password; for VNC a user (Apple's Screen Sharing asks for one) and a password.

| Request | Does |
|---|---|
| `credentials.set` (`{device_id, service, credentials}`) | replaces the service's sign-in, except that a password or a server key left out stays and an empty password is removed |
| `credentials.get` (`{device_id, service}`) | answers it, the password included; served only to the engine's owner or the helper's operator, for a client signing in right then |
| `credentials.forget` (`{device_id, service?}`) | forgets the service's sign-in, the admitted key (`key`), or everything saved for the host (no service) |

The status says only which are kept (`recent[].saved`: `password`, `key`, `ssh`, `rdp`, `vnc`), never a
secret. Private keys never leave the engine: the SSH client signs through `keys.sign`
([identity-and-access.md](identity-and-access.md), "Keys").

## Security

- A tunnel reaches a service that trusts its loopback as the host's own: a service that treats
  local connections specially (no password from localhost) grants that to every controller
  admitted for it. Its permission is therefore off by default and asks the host's user.
- Inside a Remote Desktop or VNC session the controller works through the host's own service, not
  Kyokai's capture and input: Hide Kyokai from capture and the guard on Kyokai's windows do not
  apply there, and the service's own sign-in decides what the controller may do.
- Saved service passwords stay in the engine's store and reach the GUI's client only through
  `credentials.get`, at its sign-in, for the owner or the operator; never in the status or a log
  ([security.md](security.md)).
