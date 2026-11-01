# Networking

Kyokai devices reach each other through EasyTier nodes embedded in the engine (`net.rs` in the
core): one for each network the user joined, every one announcing this device's Device ID. A node
joins its EasyTier network through standard EasyTier servers; there is no TUN device, no
administrator right and no custom coordination server. Settings can run the networks on an official
EasyTier core instead ([Official EasyTier core](#official-easytier-core)).

## The node

| Setting | Value |
|---|---|
| Instance id | UUIDv5 over the 9-digit Device ID (namespace `6b796f6b-6169-4e00-8000-000000005331`), the same in every network |
| Hostname | `Kyokai-<Device ID>`, the same in every network |
| Instance name | `kyokai-<network id>` |
| TUN | none (`no_tun`); traffic uses EasyTier's in-process data plane (smoltcp) |
| Address | IPv4 from the network's DHCP |
| Listeners | none, or with direct paths TCP and UDP on every address at the first port of 47311–47320 free for both and not another of this engine's nodes' (none when all are taken, as by other engines on the computer) |
| Direct paths | off by default: no UDP or symmetric hole punching, no STUN and no listener, so nothing waits for unsolicited packets and Windows Firewall does not ask (the NAT type then reads "unknown"); the node still dials a peer that listens (TCP first, an outgoing connection), and otherwise traffic goes through the server's relay. On with "Try direct connections" (`direct`, under Advanced on the Network page, for every network), and always in the helper (see below): UDP hole punching, the listeners and direct connections, preferring UDP (`default_protocol`). TCP hole punching and UPnP are not part of the build (EasyTier's `tcp-hole-punching` and `upnp` features are off), so their switches change nothing |
| Data plane whitelists | TCP and UDP port 47310 only; everything else from the network is dropped |
| Proxy input and foreign networks | every node refuses EasyTier's QUIC and KCP proxy input (`disable_quic_input`, `disable_kcp_input`: a network member could otherwise open a connection to this computer's loopback through them, guarded only by the whitelist) and peers of other networks at its listener (`private_mode`: a listening node relays no foreign network). Kyokai's sessions never use those proxies: the data plane always carries them over smoltcp |
| Routing | the fewest hops, or with "Latency-first routing" (`latency_first`, under Advanced, for every network) the lowest delay |
| Runtime | a Tokio runtime owned by the core, shared by the nodes and sessions: half the logical processors as worker threads, at least 2 and at most 4 |

A node runs for each joined network while the engine runs. Changing a network restarts its node
only, leaving it stops it, and changing Try direct connections restarts them all.

Sessions run over the data plane's TCP (smoltcp). Its flush waits until the peer acknowledged
everything sent, so a session never flushes after a message, which would allow one message per
round trip (with the peer's delayed acknowledgement, about 90 a second on a LAN, shared by video,
sound and input): the bytes leave as they are written. Only a goodbye waits, briefly, for the peer
([protocol.md](protocol.md)). The send and receive buffers are 128 KiB each. Kyokai builds smoltcp
with CUBIC congestion control (its `socket-tcp-cubic` feature, switched on from `kyokai-core`'s
manifest, which every socket EasyTier makes then gets) and from its own vendored copy
(`core/third_party/smoltcp`, smoltcp 0.14.0 through `[patch.crates-io]`), whose retransmission
timeout has a floor of 200 ms (Linux's) instead of a second, so a segment lost on a fast path goes
again after a fifth of a second; `kyokai-core/tests/smoltcp.rs` checks both.

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

Hole punching binds a UDP socket on every address (`0.0.0.0:port`) and waits
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
(2026-10-18). **Try direct connections** under Advanced on the Network page (`direct`, one choice for every
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
any other; the session is the same protocol, over plain TCP. The known host keeps the address
among its routes (below).

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

## Routes

A known host has **routes**: the Kyokai network, when this device is on one, and up to 8
addresses (IP addresses or domain names, port 47310 unless one is named) — each address a
connection by address was made to, or one added in its Addresses editor (`routes.set`). Nothing
about routes crosses the wire: an address is only where to look; the handshake still proves the
Device ID and the pinned key, and a resume token is not bound to a path, so 0.4.x hosts take
part unchanged.

- **Auto** (the default) works out the routes again at every connect and every resume. The
  network's latency and direct or relayed path come from EasyTier's routes, with nothing dialed;
  each address is looked at, all at once within 3 seconds, by timing its TCP handshake and
  reading the host's hello, which names the device there (`ok`, `unreachable`, `other-device`,
  `incompatible`, `broken`). A redial after a drop does not wait for the looks: it ranks by the
  last ones and takes new looks beside it. `rank_routes` orders them: those that answer as the device, then
  those that did not fail their last two dials, then those not remembered slow (below 8 Mbit/s and
  below half the best route's), by round trip (unknown last), direct before relayed. Dialing
  follows that order with happy eyeballs: the next route starts 300 ms after the one before it
  unless that one answered; the first connection that answers runs the session, and another
  device answering moves on to the rest, as does a key other than the one pinned for the host (the
  session fails as `key-changed` only when no route reaches the pinned key).
- **A route chosen by hand** (the network, or one address) is used alone, also on resume.
- **Memory:** each route keeps when a session last connected over it, failed dials in a row, a
  moving average of its round trip (the session's delay probes) and of the bitrate the host's
  quality control settled on over it (`RouteMeter`), in the known host (`routes`, `network`,
  `prefer`). The latest looks at the addresses are kept in memory only; Home asks for them
  (`routes.probe`) every 60 seconds while it shows devices that have addresses.
- **Moves:** when a session's connection drops or falls silent, the resume attempts rank again
  under Auto and may resume over another route; under Auto a healthy session to a host that
  announced `move` also moves when another route is clearly better (see
  [Switching paths](#switching-paths)). The status names the route a controller session takes
  (`sessions[].route`: an address, or empty for the network).

An address kept from before 0.5.0 becomes the first route, and a connection by Device ID no
longer forgets the addresses.

## Switching paths

A path can die without a word: a Wi-Fi network left, a cable pulled, a NAT mapping gone. Neither
EasyTier nor TCP notices quickly, so a session watches for silence itself.

- **Silence.** A session pings a peer it has not heard from for a second ([protocol.md](protocol.md),
  "Session messages"). A controller whose ping then stays unanswered for 2 seconds, nothing else
  heard and nothing waiting to be read (about 3 seconds in all; 6 to a host that did not announce
  `move`, for which a resume restarts more and which opens its video inside its session loop)
  lets the connection go and redials at once: the first redial after a drop does not wait the
  3 seconds the later ones do, and under Auto it ranks the routes by the last looks at the
  addresses and takes new looks beside it ([Routes](#routes)). A host whose ping stays unanswered
  for 2 seconds the same way drops its direct link to the controller, once per silent spell, so the
  resume finds the relay already in place.
- **Dropping a direct link.** On Kyokai Network, dropping means closing this node's direct
  EasyTier connections to that peer (`net::drop_direct`, `close_direct`), at most once in
  30 seconds per peer, so a path that keeps failing is not fought over with EasyTier's own
  connector. EasyTier then carries the peer through the server's relay at once, and its connector
  brings the direct link back by itself once it works again (within about 0.1 seconds after a cut
  healed, in the test below). When the connection a controller let go ran over Kyokai Network, the
  controller drops its direct link to the host as well. With an official EasyTier core, paths are
  left to EasyTier.
- **Without dropping.** Measured with three EasyTier nodes in one process on this computer (a
  server and two peers, the direct link between the peers cut; test `threenode`): EasyTier drops
  the dead direct link by itself after about 12 seconds, and a fresh connection reaches the peer
  over the relay after about 14.5 seconds; a TCP connection open before the cut never carries on
  (it ends at about 25.5 seconds). With the direct links dropped 2 seconds after the cut, a fresh
  connection reaches the peer 3.1 seconds after it ([platform-notes.md](platform-notes.md)).
- **Healthy sessions move.** Under Auto, to a host that announced `move`, every 30 seconds the
  addresses are looked at and the network's path read. When another route is clearly better twice
  in a row — under half the current round trip and at least 20 ms less, or direct where the
  current one is relayed and no slower — the connection is let go and the session redials there;
  at most once a minute.
- **What moves.** A desktop session resumes ([protocol.md](protocol.md), "Resuming"): to a host
  that announced `move` the controller says `move`, and the host does not pause that controller's
  AI task as the old session ends; the video starts again (capture and encoder), and nothing
  else of the old connection is handed over. A tunnel to a controller that announced `move`
  carries on over the new connection with no byte lost or repeated ([protocol.md](protocol.md),
  "Tunnels"): the host keeps the service's connection open meanwhile. Between older peers
  sessions resume and tunnels end as before.

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
| `devices` | other Kyokai devices the network routes to: announced Device ID, `direct` or `relay` path, latency in ms, and `name`, the friendly name the device shows in its networks if it does (see Trust); together, each device once, by its best path (direct first, then the lowest latency). `lan` is true when the path is relayed yet the device is on this device's LAN: one of the interface addresses it reports through EasyTier's direct connector (`GetIpList`, which every node answers, direct connections on or not) lies in one of this device's private IPv4 subnets (loopback, link-local, `100.64.0.0/10`, the network's virtual subnet and this device's own address never count, nor a subnet wider than /8). It is looked at beside the 2-second status reading, at most once a minute per relayed peer, with a 3-second timeout (`net::look_lan`, `mark_lan`); it is left out otherwise, and always through an official core. Two places that use the same private subnet look like one LAN (with direct connections off there is no public address to compare) |
| `networks` | each network kept, in the order added: its `id`, `label` (the name it is shown by), `joined` and the fields above (`off` while it is not joined) |
| `unreachable` | with an official EasyTier core, why other devices cannot reach this one (its loopback port taken) while it still reaches them |

Sessions attached to the local API receive a new status whenever it changes (re-read every
2 seconds).

## Official EasyTier core

Settings › Network › **Runs on** chooses what every network runs on: **Kyokai Network**, the
embedded node above (the default), or an official EasyTier core (`network.backend`, the core's tag
in `easytier`; `ctl network-backend --easytier <tag>`). The engine lists EasyTier's GitHub
releases of the 2.6 and 2.7 lines (`cores.list`, never `nightly`) for which this system has an
asset (`easytier-<windows|macos|linux>-<arch>-<tag>.zip`) and the release a SHA-256 digest, and downloads one when it is chosen (`cores.install`,
`internal/cores`): the zip is checked against the digest before anything is unpacked, then
`easytier-core`, `easytier-cli` and their libraries go into `<state>/cores/<tag>/` (the helper's
state directory in the helper modes, written by the helper). easytier-mini is not offered: without
TUN it has no SOCKS5 portal and does not forward a connection to its own virtual address, so it
cannot carry Kyokai's sessions (a spike with two cores on one computer, 2026-10-28).

Each joined network then runs one `easytier-core` process (`core/kyokai-core/src/external.rs`),
without a window and with its output discarded (it prints its configuration, secret included):

- **Configuration.** Kyokai's own, as for the embedded node, with the network's **EasyTier
  options** (its `toml`, TOML) over it, tables key by key, and a SOCKS5 portal on a free loopback
  port. The options may not set what Kyokai sets itself (`external::OWNED`: the instance name, id
  and hostname, `network_identity`, `peer`, the portals, `tcp_whitelist`, `udp_whitelist`,
  `flags.no_tun`, `flags.disable_quic_input`, `flags.disable_kcp_input`, `flags.private_mode`);
  such keys and invalid TOML are refused with the reason. The servers, network name and secret
  stay the network's own fields, the secret in the secrets store. The whole configuration is
  written to `<state>/cores/run/<id>.toml`, owner-only, checked with the core's own
  `--check-config`, read by the core as it starts and removed once its RPC portal answers (or it
  fails); a run folder left by a crash is removed at the next start.
- **Reaching others.** Kyokai dials `<virtual address>:47310` through the core's SOCKS5 portal.
- **Being reached.** The core hands a connection to its own virtual address to
  `127.0.0.1:<port>`, only for ports `tcp_whitelist` admits (47310). Kyokai accepts there, or, with
  connections by address on, on that listener, taking loopback as the official core's. Every such
  session comes from `127.0.0.1`, so the login limits count all of them together, whichever
  device or network they came from. Only one program on a computer can hold that port: with
  another Kyokai (an installed helper accepting by address) or another program on it, other devices
  cannot reach this one (`unreachable`), while it still reaches them.
- **What it knows.** Peers, addresses, paths and latency come from `easytier-cli -o json peer`
  (the same in 2.6 and 2.7), never `node`, whose answer holds the network secret.

A network whose options set nothing (no keys; comments do not count) is the same on either: Runs
on switches both ways without a word. Back on Kyokai Network every network's options are removed,
after a warning naming them when any network sets one. Changing Runs on restarts every network.

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
