# Networking

Kyokai devices reach each other through an embedded EasyTier node in the engine (`net.rs` in the
core). The node joins an EasyTier network through standard EasyTier servers; there is no TUN
device, no administrator right and no custom coordination server.

## The node

| Setting | Value |
|---|---|
| Instance id | UUIDv5 over the 9-digit Device ID (namespace `6b796f6b-6169-4e00-8000-000000005331`) |
| Hostname | `Kyokai-<Device ID>` |
| TUN | none (`no_tun`); traffic uses EasyTier's in-process data plane (smoltcp) |
| Address | IPv4 from the network's DHCP |
| Listeners | none |
| Direct paths | off by default: no UDP, TCP or symmetric hole punching and no UPnP, so nothing waits for unsolicited packets and Windows Firewall does not ask; traffic goes through the server's relay. On with "Try direct connections" (`direct`), and always in the helper (see below) |
| Data plane whitelists | TCP and UDP port 47310 only; everything else from the network is dropped |
| Runtime | a two-thread Tokio runtime owned by the core |

The node runs while the engine runs and a network is configured; it restarts when the settings
change.

## Settings

The user configures one network: up to 8 server URLs (`tcp`, `udp`, `ws`, `wss`, `quic`, `wg`;
`faketcp` and others are refused), a network name (at most 64 characters) and the network secret.
The whole entry, secret included, is kept in the secrets store ([security.md](security.md)), so
without a working OS store it lasts only for the run. A secret shorter than 16 characters is
reported as weak. Clearing the servers and the name turns the node off.

## Direct paths

Hole punching binds a UDP socket on every address (`0.0.0.0:port`, with a UPnP mapping) and waits
for the other side's packets; Windows Firewall treats that as accepting inbound traffic and asks to
allow the program, a prompt a standard user cannot approve (seen on the test VM; this machine had
rules from earlier answers). So in No privileges the node starts with
`disable_udp_hole_punching`, `disable_tcp_hole_punching`, `disable_sym_hole_punching` and
`disable_upnp`, and sessions use the server's relay. The Network page's **Try direct connections**
(`direct` in the settings, kept with them) turns punching on, accepting that Windows may ask. The
helper always punches: its installer adds an inbound allow rule (`Kyokai helper`) for its own
`kyokai-svc.exe`, removed with it ([run-modes.md](run-modes.md)).

## Status

The engine reads the node's status when asked, never from a cache:

| Field | Meaning |
|---|---|
| `state` | `off`, `connecting`, `online` (a connection to a server or peer is up) or `error` |
| `error` | why the node could not start |
| `address` | the virtual IPv4 address, once assigned |
| `nat` | the NAT type found through STUN: `unknown`, `open`, `no-pat`, `full-cone`, `restricted`, `port-restricted`, `symmetric`, `symmetric-easy`, `symmetric-firewall` |
| `devices` | other Kyokai devices the network routes to: announced Device ID, `direct` or `relay` path, latency in ms |

Sessions attached to the local API receive a new status whenever it changes (re-read every
2 seconds).

## Trust

EasyTier is an untrusted pipe. Any member of the network can announce any instance id and
hostname, and two nodes may announce the same one (both stay routable), so the Device IDs in
`devices` are hints for finding a device, never proof of who it is. Every Kyokai session is
authenticated end to end on top of the data plane. EasyTier's own encryption shares one key per
network among all its members.

## Build

The core enables only EasyTier's `smoltcp`, `aes-gcm`, `dhcp-ipv4`, `dns-resolver`, `websocket`,
`quic` and `wireguard` features. `dns-resolver` resolves the STUN servers, which NAT detection and
hole punching need. EasyTier's `thunk-rs` build step (Windows 7 support, unverified downloads, a
relink against `msvcrt.dll`) is replaced by a no-op crate, so every Windows build keeps the static
C runtime; the build uses a pinned, checksum-verified `protoc` instead of EasyTier's download
([development.md](development.md)).
