# Remote services

A controller reaches a host's own SSH, Remote Desktop and VNC (Screen Sharing on a Mac) servers
through tunnels inside Kyokai sessions, with built-in clients; nothing listens on the network for
them. Each service is a permission of its own (`use-ssh`, `use-rdp`, `use-vnc`), off by default.

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
- **SSH** (`golang.org/x/crypto/ssh`): this device's keys first — each an `ssh.Signer` whose
  signature the engine makes (`keys.sign`, the exact user-auth request; RSA as `rsa-sha2-512` or
  `rsa-sha2-256`), so a private key never reaches the GUI — then the password, which also answers
  keyboard-interactive prompts that do not echo. The server's key is pinned on first use
  (`Credentials.HostKey`, an authorized_keys line); a different key is refused before any password
  is sent, and only the key the person chose with Trust the new key is accepted instead. The shell
  has a PTY of `xterm-256color`; MyGo's terminal (`plugins/terminal`, libghostty-vt) shows it with
  the tunnel as its `Conn`, whose `Resize` sends a window change.
- **Remote Desktop** (vendored, `internal/rdp`): Network Level Authentication (CredSSP with NTLMv2)
  with a user name, TLS alone without one, which shows the Windows sign-in screen; the graphics
  pipeline without H.264 except on macOS (VideoToolbox). The server's certificate is accepted
  without pinning: the tunnel reaches the device's own port inside an authenticated Kyokai
  session. Keys go as scan codes.
- **VNC** (vendored, `internal/rfb`): security None, VNC password (2), Apple's Diffie-Hellman (30),
  RSA (33) and SRP (36) sign-ins with a user name and password when the server offers them; keys go
  as keysyms. A Mac signed in by someone else shares its screen (the console session).
- **Both pictures** (`screenClient`): the framebuffer is converted to RGBA on the protocol's
  goroutine and shown on the main thread, fitted or at its original size as the viewer's; the
  pointer, its buttons and the wheel go in the other desktop's pixels. In Map and Translate the
  keyboard grab takes keys as for a desktop (`App.takeKey`); in Legacy named keys go with their
  modifiers and text as text. The clipboard's text crosses while the tab is in front: this
  computer's goes as the tab comes in front, the other desktop's comes back as it changes.
  Ctrl+Alt+Del is in Actions.
- **Sign-ins kept**: after a sign-in from the form works, the user name (and domain) is saved, with
  the password when Remember password is on.
