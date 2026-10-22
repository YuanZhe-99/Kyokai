# Remote services

A controller reaches a host's own SSH server, Remote Desktop or VNC (Screen Sharing on macOS)
through a tunnel inside a Kyokai session, to the host's loopback. Nothing listens on the network
for them: the services stay where they are, reached only by whoever Kyokai admitted.

## Services

| Service | Permission | Standard port on the host |
|---|---|---|
| `ssh` | `use-ssh` | 22 |
| `rdp` | `use-rdp` | 3389 |
| `vnc` | `use-vnc` | 5900 |

The host connects to 127.0.0.1, then ::1, on the service's port: the standard one, or the one
its user set under Permissions, "Ports of this computer's services" (`access.settings`
`{service_ports: {ssh?, rdp?, vnc?}}`, a port of 0 or left out is the standard one; the status
answers the ports in effect as `service_ports`). A controller names only the service, so it reaches
no other port of the host. Home's card offers the services that answer on those ports. Its user turns a service on
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
| `credentials.set` (`{device_id, service, credentials}`) | replaces the service's sign-in — its `method` (`key`, `password`, `account` or `none`, remembered with Remember) and user included —, except that a password or a server key left out stays and an empty password is removed |
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

## Clients

The GUI's clients (`internal/gui`: `remote.go`, `servicetab.go`, `ssh.go`, `desktops.go`,
`screen.go`) run on the controller and reach the service only through the tunnel; a service tab
is a tab like a desktop's ([gui.md](gui.md), "Connections").

- **The link (`serviceLink`).** `Open` is the tunnel (`tunnel.open` through the engine, a
  `net.Conn` carrying the service's bytes); `Credential` and `SaveCredential` are
  `credentials.get` and `credentials.set`; `Keys` and `Sign` are `keys.list` and `keys.sign`.
  `credentials.get` runs right before a client signs in; the password goes from the answer to the
  client and is never kept in the GUI's state. `credentials.set` replaces a service's saved
  sign-in. In development `KYOKAI_DEV_SERVICES=1` replaces the engine with a stub that dials
  `127.0.0.1` (ports from `KYOKAI_DEV_SSH_PORT`, `KYOKAI_DEV_RDP_PORT`, `KYOKAI_DEV_VNC_PORT`) and
  keeps sign-ins in memory; an engine that opens no tunnels is refused, never replaced by this
  computer's own ports.
- **The sign-in method** (`signInMethods`, `signInPlan`): a tab tries nothing the person did not
  choose. With no method remembered for the device's service (a first connection, Remember off, or
  a sign-in saved before 0.5.1) it shows the form before any tunnel opens: SSH offers Key (a user
  name and this device's keys) or Password (a user name and password); VNC Password only, User name
  and password (a Mac's) or No sign-in; Remote Desktop User name and password or On the remote
  screen. A sign-in from the form that works is saved with its method when Remember is on (the
  password too, for a method that has one), so the next connection signs in the same way without
  asking; with Remember off only the user name and domain stay, and a method remembered before is
  forgotten. A remembered method that fails, or "no sign-in" meeting a server that needs one, shows
  the form again with that method chosen.
- **SSH** (`golang.org/x/crypto/ssh`), by the method alone: Key offers this device's keys — each
  an `ssh.Signer` whose signature the engine makes (`keys.sign`, the exact user-auth request; RSA as
  `rsa-sha2-512` or `rsa-sha2-256`), so a private key never reaches the GUI — and never a password;
  Password never offers a key: the password also answers keyboard-interactive prompts that do not
  echo, but never a second time in a connection whose
  server refused it once (a Mac answers each failure about a minute late and counts it against the
  address, which for every tunnel is its own loopback). After the key exchange a refusal or a
  closed connection is a refused sign-in, and silence until the 30-second limit says the server did
  not answer. The server's key is pinned on first use
  (`Credentials.HostKey`, an authorized_keys line); a different key is refused before any password
  is sent, and only the key the person chose with Trust the new key is accepted instead. The shell
  has a PTY of `xterm-256color`; MyGo's terminal (`plugins/terminal`, libghostty-vt) shows it with
  the tunnel as its `Conn`, whose `Resize` sends a window change.
- **Remote Desktop** (vendored, `internal/rdp`): Network Level Authentication (CredSSP with NTLMv2)
  for User name and password, TLS alone for On the remote screen, which shows the Windows sign-in
  screen (a server that allows only Network Level Authentication asks for another method); the graphics
  pipeline without H.264 except on macOS (VideoToolbox). The server's certificate is accepted
  without pinning: the tunnel reaches the device's own port inside an authenticated Kyokai
  session. Keys go as scan codes.
- **VNC** (vendored, `internal/rfb`): security None, VNC password (2), Apple's Diffie-Hellman (30),
  RSA (33) and SRP (36) sign-ins with a user name and password when the server offers them; keys go
  as keysyms. A Mac signed in by someone else shares its screen (the console session). On Windows,
  TigerVNC works as installed; TightVNC and UltraVNC refuse connections from their own computer
  until "Allow loopback connections" (TightVNC) or `AllowLoopback` (UltraVNC) is on, and need a VNC
  password (UltraVNC: MS-Logon and encryption plugins off); RealVNC Server's own sign-ins (RA2) are
  not supported, only its VNC password with encryption allowed off. The tab says which: a server
  that closes before its greeting (`rfb.ErrClosedBeforeGreeting`) is named with the loopback
  setting, a refusal shows the server's own reason (`rfb.RefusedError`), and a server offering no
  supported sign-in (`rfb.ErrUnsupportedServer`, an RFB 3.3 type beyond a byte named whole) asks
  for VNC password authentication. Password only asks the server for the VNC password (2) alone,
  User name and password for Apple's sign-ins first, No sign-in for no security alone; a server
  that needs a sign-in it did not get asks for another method, never after a blank attempt that a
  Mac counts as a failure, and a server that closes the connection during a sign-in (`rfb.ErrSignInDropped`, what a
  Mac does to repeated failures) is a refused sign-in.
- **Both pictures** (`screenClient`): the framebuffer is converted to RGBA on the protocol's
  goroutine and shown on the main thread, fitted or at its original size as the viewer's; the
  pointer, its buttons and the wheel go in the other desktop's pixels. In Map and Translate the
  keyboard grab takes keys as for a desktop (`App.takeKey`); in Legacy named keys go with their
  modifiers and text as text. The clipboard's text crosses while the tab is in front: this
  computer's goes as the tab comes in front, the other desktop's comes back as it changes.
  Ctrl+Alt+Del is in Actions.
- **Sign-ins kept**: see the sign-in method above; SSH also saves the server's key pinned on first
  use, with the remembered method and user when the key is new to a remembered sign-in.
