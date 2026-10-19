# Networking

Kyokai devices reach each other through EasyTier nodes embedded in the engine (`net.rs` in the
core): one for each network the user joined, every one announcing this device's Device ID. A node
joins its EasyTier network through standard EasyTier servers; there is no TUN device, no
administrator right and no custom coordination server.

## The node

| Setting | Value |
|---|---|
| Instance id | UUIDv5 over the 9-digit Device ID (namespace `6b796f6b-6169-4e00-8000-000000005331`), the same in every network |
| Hostname | `Kyokai-<Device ID>`, the same in every network |
| Instance name | `kyokai-<network id>` |
| TUN | none (`no_tun`); traffic uses EasyTier's in-process data plane (smoltcp) |
| Address | IPv4 from the network's DHCP |
| Listeners | none, or with direct paths TCP and UDP on every address at the first port of 47311–47320 free for both and not another of this engine's nodes' (none when all are taken, as by other engines on the computer) |
| Direct paths | off by default: no UDP, TCP or symmetric hole punching, no UPnP, no STUN and no listener, so nothing waits for unsolicited packets and Windows Firewall does not ask (the NAT type then reads "unknown"); the node still dials a peer that listens (TCP first, an outgoing connection), and otherwise traffic goes through the server's relay. On with "Try direct connections" (`direct`, on the Network page, for every network), and always in the helper (see below): hole punching, UPnP, the listeners and direct connections, preferring UDP (`default_protocol`) |
| Data plane whitelists | TCP and UDP port 47310 only; everything else from the network is dropped |
| Runtime | a two-thread Tokio runtime owned by the core, shared by the nodes |

A node runs for each joined network while the engine runs. Changing a network restarts its node
only, leaving it stops it, and changing Try direct connections restarts them all.

Sessions run over the data plane's TCP (smoltcp). Its flush waits until the peer acknowledged
everything sent, so a session never flushes after a message, which would allow one message per
round trip (with the peer's delayed acknowledgement, about 90 a second on a LAN, shared by video,
sound and input): the bytes leave as they are written. Only a goodbye waits, briefly, for the peer
([protocol.md](protocol.md)). The send and receive buffers are 128 KiB each.

## Settings

The user keeps up to 8 networks, each with up to 8 server URLs (`tcp`, `udp`, `ws`, `wss`,
`quic`, `wg`; `faketcp` and others are refused), a network name (at most 64 characters), the
network secret, a name shown in Kyokai (optional, at most 64 characters; the network name
otherwise) and whether this device joins it; any number may be joined at once. A random id (16 hex
digits) names each one. The whole list, secrets included, is one entry in the secrets store
(`networks`, [security.md](security.md)), so without a working OS store it lasts only for the run.
A secret shorter than 16 characters is reported as weak.

A network needs both a server and a name. Two joined networks with the same name and secret are
the same network, so joining the second is refused. 0.3.1 and earlier kept one network (the
`network` entry): it becomes the first network, joined, with its direct choice, and the old entry
is removed; settings 0.2.0 stored with a name and no server never joined anything and are dropped.
Removing a network (the GUI's **Remove**) forgets it, secret included. Reaching another device by
its IP address or domain name needs no network at all: see
[Connections by address](#connections-by-address).

**Connecting by Device ID.** Every joined network whose routes announce the Device ID offers
candidates, up to 3 in each (anyone can announce a Device ID, so one network's squatters cannot
crowd out another's), tried with direct paths first and then by the lowest latency; the handshake
decides which one is the device. A session that drops resumes through whichever networks are
joined then. A host serves sessions from every joined network.

## Direct paths

Hole punching binds a UDP socket on every address (`0.0.0.0:port`, with a UPnP mapping) and waits
for the other side's packets; Windows Firewall treats that as accepting inbound traffic and asks to
allow the program, a prompt a standard user cannot approve (seen on the test VM; this machine had
rules from earlier answers). STUN, which finds the NAT type for punching, does the same at
start (two short-lived UDP sockets on every address, seen on this machine). So in No privileges
the node starts with `disable_udp_hole_punching`, `disable_tcp_hole_punching`,
`disable_sym_hole_punching`, `disable_upnp` and empty `stun_servers` and `stun_servers_v6`; it then
holds only its TCP connection to the server and dials a peer that listens (TCP first,
`default_protocol`; an outgoing connection no firewall asks about), and otherwise sessions use the
server's relay. It does not set `disable_p2p`, which would also refuse a direct connection a
listening peer offers: a device left off then kept a LAN peer that listens on the relay
(2026-10-18). **Try direct connections** on the Network page (`direct`, one choice for every
network, applied at once) turns punching on and has every node listen, each on a port of its own,
accepting that Windows may ask. EasyTier's direct
connector reaches only peers that listen: it tries each of their interface addresses and their
public address. Hole punching alone needs the shared router to send a device's packets back into
its own network, which many do not, so two devices on one LAN went through the server's relay
until they listened (seen 2026-10-18). One device with direct connections on is enough on one LAN:
the other dials its listener. On macOS the first direct connection on the local
network asks for Kyokai's Local Network permission (not in the helper, a root daemon). The
helper always punches and listens: its installer adds an inbound allow rule (`Kyokai helper`)
for its own `kyokai-svc.exe`, removed with it ([run-modes.md](run-modes.md)).

## Connections by address

A controller can also reach a host by the host's own IP address or domain name, with no EasyTier
network: a LAN address, a Tailscale address, or a name that resolves to one. The person types it
where a Device ID goes ("Control another device"); the port is 47310 unless the address names one
(`host:port`, `[v6]:port`). The controller first opens a connection and reads the host's
`host_hello` only (`session.identify`, [protocol.md](protocol.md)) to learn which Device ID
answers there, then dials that address for the session, which must prove to be that device like
any other; the session is the same protocol, over plain TCP. The known host keeps the address,
so Recent connects the same way next time; connecting by Device ID goes back to the network.

A host accepts them only while its user turns on **Accept connections by address** (the Network
page; off by default): the engine then listens on TCP 47310 on every IPv4 address and, where
there is IPv6, every IPv6 address (IPv6 only on that socket), and closes at once any connection
from outside the allowed ranges, before reading a byte. The ranges default to the private IPv4
networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local `169.254.0.0/16`,
Tailscale's `100.64.0.0/10`, and IPv6 unique-local `fc00::/7` (Tailscale's IPv6 addresses among
them) and link-local `fe80::/10`; the user may list up to 32 ranges or single addresses. An IPv4
address mapped into IPv6 counts as IPv4. A port already taken (a second engine on this computer)
leaves it not listening, which the status reports. Windows Firewall may ask about the listener,
like direct connections. Everything after the connection is as over the network: the Noise
handshake, a password or the host user's confirmation, and the per-address login limits
([identity-and-access.md](identity-and-access.md)).

## Status

The engine reads the nodes' status when asked, never from a cache. `network` is all of them
together, as older GUIs, Home and the viewer read it; its `networks` lists each:

| Field | Meaning |
|---|---|
| `state` | `off`, `connecting`, `online` (a connection to a server or peer is up) or `error`; together: online when any network is, connecting when any is, an error only when every joined one failed, off when none is joined |
| `error` | why the node could not start |
| `address` | the virtual IPv4 address, once assigned (together: the first online network's) |
| `nat` | the NAT type found through STUN: `unknown`, `open`, `no-pat`, `full-cone`, `restricted`, `port-restricted`, `symmetric`, `symmetric-easy`, `symmetric-firewall` (together: the first online network's) |
| `listen_port` | the port other devices' nodes connect to directly, when it listens (direct connections on; together: the first online network's) |
| `devices` | other Kyokai devices the network routes to: announced Device ID, `direct` or `relay` path, latency in ms; together, each device once, by its best path (direct first, then the lowest latency) |
| `networks` | each network kept, in the order added: its `id`, `label` (the name it is shown by), `joined` and the fields above (`off` while it is not joined) |

Sessions attached to the local API receive a new status whenever it changes (re-read every
2 seconds).

## Trust

EasyTier is an untrusted pipe. Any member of the network can announce any instance id and
hostname, and two nodes may announce the same one (both stay routable), so the Device IDs in
`devices` are hints for finding a device, never proof of who it is. This device's nodes announce
the same instance id and hostname, each in its own network; the networks never see one another. Every Kyokai session is
authenticated end to end on top of the data plane. EasyTier's own encryption shares one key per
network among all its members.

## Build

The core enables only EasyTier's `smoltcp`, `aes-gcm`, `dhcp-ipv4`, `dns-resolver`, `websocket`,
`quic` and `wireguard` features. `dns-resolver` resolves the STUN servers, which NAT detection and
hole punching need. EasyTier's `thunk-rs` build step (Windows 7 support, unverified downloads, a
relink against `msvcrt.dll`) is replaced by a no-op crate, so every Windows build keeps the static
C runtime; the build uses a pinned, checksum-verified `protoc` instead of EasyTier's download
([development.md](development.md)).
