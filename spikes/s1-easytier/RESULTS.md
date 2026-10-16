# S1 — EasyTier v2.7.0 embedded in-process: results

**Verdict: feasible.** Two `easytier` v2.7.0 instances run in one process with no
TUN and no admin rights. They join a stock `easytier-core` v2.7.0 server, and
B's data-plane listener accepts A's connection with bytes flowing both ways.
The whitelist plus the userspace stack isolate the host's own ports. Stop and
restart in the same process is clean over 10 cycles. The crate builds natively
for `aarch64-pc-windows-msvc` and cross-builds for `x86_64-pc-windows-msvc`.

Caveats that shape the design (details below):
- This network is **symmetric NAT on both sides**, so UDP hole punching was
  correctly *skipped*. Hole punching through real NATs is **not proven** here.
- Without local listeners, all A↔B traffic is relayed by the server. With
  `listeners` enabled, a direct LAN TCP link formed in about 6 s.
- The first DHCP lease takes about 10 s. One rare DHCP collision was observed.

All logs referenced below are in `.dev/logs/` (git-ignored).

---

## Environment

| | |
|---|---|
| Date | 2026-10-16 |
| OS | Windows 11 Pro 10.0.26200, **ARM64**, Medium integrity, not admin |
| Rust | 1.99.1 `aarch64-pc-windows-msvc`, pinned by the repo root `rust-toolchain.toml`. Added `x86_64-pc-windows-msvc` std. |
| C toolchain | VS 2026 Community 18, "C++ Clang Compiler for Windows": `VC\Tools\Llvm\ARM64\bin\clang.exe`, plus MSVC x64 libs |
| Network | Wi-Fi 172.28.147.35/20, campus NAT (public IP 128.146.189.99, STUN result: Symmetric). The user's own EasyTier already holds :11010, so the spike uses :21010+ |

## Exact dependency

- Tag `v2.7.0` is a **lightweight** tag. `gh api repos/EasyTier/EasyTier/git/ref/tags/v2.7.0`
  returns `object.type = "commit"`, sha **`8b7f1f0196edb5b580829ed5f135050fb5853478`**
  ("fix(web): accept client handshakes concurrently (#2633)", 2026-10-05).
  The release is a prerelease published 2026-10-06T13:21:27Z.
- `Cargo.toml` depends on `easytier` and `easytier-core` by git + `rev`, with `default-features = false`.

### Feature set used

| easytier feature | why | notes |
|---|---|---|
| `smoltcp` | in-process data plane (`data_plane_tcp_bind/connect`) | maps to `easytier-core/proxy-smoltcp-stack` |
| `aes-gcm` | RustCrypto AES-GCM, the default algorithm | see the encryption note below |
| `dhcp-ipv4` | virtual IPv4 from the network | |
| `websocket` | `ws://`, `wss://` connectors | rustls+ring, rcgen |
| `quic` | `quic://` connector | quinn + rustls/ring; also enables `easytier-core/proxy-packet` |
| `wireguard` | `wg://` connector | boringtun; **also enables `vpn-portal` + `ring-crypto`**. ring then becomes the AEAD backend, ahead of RustCrypto. |

`tcp://` and `udp://` connectors and **UDP hole punching are always compiled in**
(no feature flag). `tcp-hole-punch` is a separate feature; I left it out.

Excluded (all in `default`): `tun`, `socks5`, `kcp`, `faketcp`, `magic-dns`,
`cli`, `management` (RPC portal / web client / config server), `upnp`,
`icmp-proxy`, `endpoint-discovery`, `extended-services`, `zstd`,
`linux-netlink`, `tcp-hole-punch`.

**Encryption note.** The task assumed that without `aes-gcm` EasyTier silently
falls back to XOR. That is **no longer true at v2.7.0**.
`easytier-core/src/tunnel/encrypt/mod.rs` validates the configured algorithm
and returns `AlgorithmUnavailable` (or an `UnsupportedCipher` encryptor that
errors at use). `EncryptionAlgorithm::default()` is `AesGcm`. XOR is only used
when explicitly configured as `"xor"`. Keep `aes-gcm` anyway.

### `easytier-core` / `easytier-mini` as smaller alternatives

- **`easytier-core`** ("OS-free control-plane core primitives") is a *dependency
  of* `easytier`. It owns `CoreInstance`, `start/stop`, `route_snapshots` and the
  data plane, but has no native host (sockets, listeners, connectors). The native
  host lives in `easytier` (`instance::factory::create_native_instance`).
  Embedding `easytier-core` alone would mean re-implementing that host, so it
  does **not** give a usable smaller footprint today.
- **`easytier-mini`** (`easytier-contrib/easytier-mini`) is a **binary**, not a
  library. It enables `tun` and `web-client`, so it is not a fit.
- **Smaller footprint that does work:** drop the extra connectors. In this spike
  `--no-default-features` builds with tcp/udp only:

  | variant | normal+build deps (unique) | `s1-easytier.exe` (release, debug=1, no LTO) |
  |---|---|---|
  | smoltcp+aes-gcm+dhcp **+ ws/quic/wg** | 443 | 20.66 MB |
  | smoltcp+aes-gcm+dhcp (tcp/udp only) | 417 | 15.53 MB (−25%) |
  | official `easytier-core.exe` arm64 (full features, LTO+strip), for scale | – | 17.82 MB |

  `ring` remains even in the minimal variant (`snow` in easytier-core for secure
  mode), so clang is always required on ARM64. `easytier` still pulls `clap`,
  `clap_complete`, `service-manager`, `windows-service`, `winreg`, `nix` and
  `rust-i18n` unconditionally, even with all features off.
- Also noticed, not evaluated: v2.7.0 ships **`easytier-go`**, which runs the
  `wasm32-wasip1` build of easytier-core inside a pure-Go process via wazero.
  Given the Go/MyGo GUI, it is worth a separate spike (see Recommendations).

## Server

```
gh release download v2.7.0 -R EasyTier/EasyTier -p easytier-windows-arm64-v2.7.0.zip -p SHA256SUMS
sha256sum easytier-windows-arm64-v2.7.0.zip
  65f6ed5f6f747b6ae74a07f55b84cf78127a89b618249626c98f8e82bd35d762  (matches SHA256SUMS and the GitHub asset digest) -> CHECKSUM_OK
easytier-core.exe --version  ->  easytier-core 2.7.0-8b7f1f01     (Authenticode: NotSigned)
```

Run non-elevated, listening on the interface address, not loopback (`.dev/logs/server-same-net.log`):

```
easytier-core.exe --network-name kyokai-s1 --network-secret <32 hex, .dev/secret.txt> --hostname s1-server --no-tun \
  -l tcp://172.28.147.35:21010 udp://172.28.147.35:21010 ws://172.28.147.35:21011/ quic://172.28.147.35:21012 wg://172.28.147.35:21013 \
  --rpc-portal 127.0.0.1:21888
```

A second server (`--network-name public-relay`, :22010) emulates a **public
shared node**. In that mode Kyokai's network is carried as a *foreign network*
(`server-public.log`: `handshake as server with foreign network ... network_name="kyokai-s1"`).

## Embedded instances

`src/main.rs` builds each instance from a TOML string → `TomlConfig::new_from_str` →
`create_native_instance` → `start().await`:

```toml
instance_name = "A"
instance_id   = "<UUIDv5(KYOKAI_NS, \"100000001\")>"   # 956a7ced-a669-59f5-b18d-5c2e08987e52
hostname      = "Kyokai-100000001"
dhcp          = true
listeners     = []                 # or $S1_LISTENERS
tcp_whitelist = ["7001"]
udp_whitelist = ["7001"]
[network_identity]  network_name = "kyokai-s1"  network_secret = "…"
[[peer]]            uri = "tcp://172.28.147.35:21010"
[flags]             no_tun = true
```

Run: `target/release/s1-easytier.exe 172.28.147.35 kyokai-s1 <secret> [peer-url…]`.
Environment knobs: `S1_LISTENERS`, `S1_P2P_WAIT`, `S1_SHORT`, `S1_CYCLES`, `S1_DHCP_ONLY`, `RUST_LOG`.

---

## Checks

Main run: `run-same-net-tcp.log`. `start()` returns in about 1–5 ms.

### (a) A's `route_snapshots()` show B's inst_id, hostname and IPv4 — **PASS**

```
DHCP: A=10.126.126.145 B=10.126.126.1
[A] route_snapshots():
    peer_id=2763384932 inst_id=9d84ec3a-…-aca4f5c6a329 hostname=s1-server         ipv4=-               next_hop=2763384932 cost=1 version=2.7.0-8b7f1f01
    peer_id=2117210918 inst_id=d409b924-1be1-5f9e-b719-f4bdaa8608b1 hostname=Kyokai-100000002 ipv4=10.126.126.1/24 next_hop=2763384932 cost=2 version=2.7.0-8b7f1f01
```

`inst_id` is exactly UUIDv5(ns, "100000002"), and the hostname is kept verbatim.
The same result holds through the public-relay server (`run-public-relay.log`;
the server shows as `PublicServer_s1-public`).

### (b) B `data_plane_tcp_bind(7001)` accepts A `data_plane_tcp_connect(B_ip, 7001)`; bytes both ways; accept() reports A's virtual address — **PASS**

```
[B] data_plane_tcp_bind -> listening on 10.126.126.1:7001
[A] data_plane_tcp_connect(10.126.126.1:7001) ok in 2.0593ms, local=10.126.126.145:50145
[B] accept() peer=10.126.126.145:50145 received="ping from A"
[A] received="pong from B"
    accept() peer ip == A's virtual ip (10.126.126.145): YES
```

It passed in every run (14 echo rounds in the 10-cycle run) and over every connector (tcp, udp, ws, quic, wg).

### (c) A plain loopback listener on B is not reachable from A — **PASS**

```
[B-host] OS TcpListener on 127.0.0.1:7002
[A] connect 10.126.126.1:7002 (OS loopback port, whitelisted? no) -> REFUSED/FAILED after 5.002472s: data-plane deadline exceeded
[B] data_plane_tcp_bind(7003) (not in tcp_whitelist)
[A] connect 10.126.126.1:7003 -> REFUSED/FAILED after 5.0020997s: data-plane deadline exceeded
```

There are two layers of protection. (1) The data plane is a userspace smoltcp
stack, so nothing reaches the OS stack (there is no TUN and no forwarding). (2)
`tcp_whitelist` becomes an inbound ACL (`config/peers.rs`: allow 7001 *stateful*,
then drop 0-65535), and it blocks even a real data-plane listener on 7003. The
failure shows up as a connect **timeout** (SYN dropped), not RST. Kyokai should
use short connect deadlines.

### (d) The whitelist does not break A's outbound connection — **PASS**

A carries the same `tcp_whitelist=["7001"]`. Its outbound connect from ephemeral
port 50145 works and B's replies get through, because the allow rule is
`stateful: true` (return traffic is conntracked) while the deny rule only covers
new inbound flows. See the (b) excerpt. The 1.1 GiB transfer in (g) confirms it
holds under load.

### (e) Two nodes sharing one inst_id — **both coexist; no dedup, no error**

C started with B's inst_id `d409b924-…` (hostname `Kyokai-dup-of-B`):

```
-- t+15s --
    peer_id=2139878468 inst_id=d409b924-1be1-5f9e-b719-f4bdaa8608b1 hostname=Kyokai-dup-of-B  ipv4=10.126.126.2/24 …
    peer_id=2763384932 inst_id=9d84ec3a-…                           hostname=s1-server        ipv4=-
    peer_id=2117210918 inst_id=d409b924-1be1-5f9e-b719-f4bdaa8608b1 hostname=Kyokai-100000002 ipv4=10.126.126.1/24 …
A -> B:7001 while C shares B's inst_id:  … echo still works
```

Routing is keyed by the random per-start `peer_id`, not by `inst_id`. Both
routes stay stable for 15 s+ with distinct IPs, and traffic to B is unaffected.
**Implication:** EasyTier does not enforce uniqueness of the Kyokai id. A
second (or malicious) node can advertise any inst_id and hostname. Kyokai must
not use `inst_id`/hostname from routes as identity. Treat them as hints, and
authenticate via its own Noise+OPAQUE layer bound to the virtual IP it
connects to.

### (f) `stop().await` + fresh start in the same process; no leaked ports/threads — **PASS**

`stop()` of A+B takes about 3 ms. After stop, all EasyTier TCP/UDP sockets are
gone (the remaining `127.0.0.1:7002` is the spike's own test listener). Over 10
cycles (`run-full-10cycles.log`, Windows thread/handle counts via `Get-Process`):

```
[res:before-any-instance] threads=11 handles=124 ws_mb=8  tcp=0 udp=0
[res:A+B running]         threads=12 handles=173 ws_mb=19 tcp=4 udp=0
[res:after-stop-1]        threads=12 handles=176 ws_mb=20 tcp=1
[res:after-stop-3]        threads=12 handles=188 ws_mb=20 tcp=1
[res:after-stop-6]        threads=11 handles=186 ws_mb=20 tcp=1
[res:after-stop-10]       threads=12 handles=188 ws_mb=20 tcp=1
```

Handles plateau at about 176–188 and threads at 11–13. There is no monotonic
growth, so I see no leak. `CoreInstance` is documented as one-shot ("after it
is stopped, construct a new instance"), and a fresh `create_native_instance`
with the same inst_id works every time.

### (g) Data-plane throughput A→B, 10 s — **PASS (numbers vary with load)**

Same machine, so this measures EasyTier/smoltcp/crypto overhead, not the network.

| path | Mbit/s | log |
|---|---|---|
| relayed via server, tcp connector (idle machine) | 957, 1002 | run-same-net-tcp, run-p2p-nolisteners |
| relayed via public-relay server (foreign network) | 1300 | run-public-relay |
| **direct A↔B LAN TCP (listeners on)** | **1423** | run-p2p-listeners |
| relayed, tcp connector (~50% CPU busy from parallel builds) | 472–489 (4 reps), 358, 483 | rep-tcp-*, throughput-reps |
| relayed, udp connector | 19 (first), 315–465 (busy) | run-connector-udp, throughput-reps |
| relayed, quic / ws / wg | 250 / 223–255 / 99 | run-connector-* |
| x64 build under Prism emulation, tcp relay | 665 | run-x64-emulated |

Note: in one repetition loop, 4 of 6 runs printed no throughput line (stderr was
discarded). Four follow-up repetitions with full logs all passed. The likely
cause is the DHCP collision below, but this is **unconfirmed**.

### (h) No administrator rights — **PASS**

```
IsAdministrator=False
Mandatory Label\Medium Mandatory Level
Wintun/TAP adapters named Kyokai*: 0
```

The server, both instances and the builds all ran at Medium integrity. The
binaries import only OS DLLs (`advapi32, bcrypt, iphlpapi, kernel32, ntdll,
ws2_32`, plus `msvcrt.dll` on x64). There is **no** `wintun.dll` or
`Packet.dll`, and the WinDivert driver is never loaded.
Caveat: with `S1_LISTENERS=…0.0.0.0:0` the process opens listening sockets
on all interfaces. On a fresh machine Windows Defender Firewall may prompt
(allowing it needs admin). Outbound-only mode (`listeners=[]`) needs no
firewall changes.

### (i) Builds: native aarch64 and cross x86_64 — **PASS with quirks**

```
PATH="…\VC\Tools\Llvm\ARM64\bin:$PATH" cargo build --release                                # 6m25s cold, OK
PATH="…\VC\Tools\Llvm\ARM64\bin:$PATH" cargo build --release --target x86_64-pc-windows-msvc # 7m31s cold, OK
file: release/s1-easytier.exe  PE32+ … ARM64      x86_64-pc-windows-msvc/release/s1-easytier.exe  PE32+ … x86-64
```

The x64 binary also ran the full short check under emulation (`run-x64-emulated.log`).

---

## Build quirks and workarounds

1. **clang required (ring, aarch64-msvc).** Without it the build fails:
   `ring@0.17.14: failed to find tool "clang"` (`build-arm64-1.log`). Workaround:
   put `VC\Tools\Llvm\ARM64\bin` on PATH (or set `CC_aarch64_pc_windows_msvc`).
   `ring` is unavoidable (`snow`, rustls, boringtun). The x64 cross build found
   the MSVC x64 `cl.exe` by itself.
2. **protoc is auto-downloaded on Windows, unverified.** `easytier-proto/build/main.rs`:
   if `PROTOC` is unset and no `protoc.exe` is on PATH, it fetches
   `protoc-26.0-rc-1-win64.zip` (an x64 **release candidate**, run under
   emulation on ARM64) with `reqwest::blocking::get(...).unwrap()`. There is no
   checksum check, and network access is required. Workaround: set `PROTOC` to a
   vetted protoc.exe. Even so, `reqwest` (default TLS → **aws-lc-sys**, a C build)
   is still compiled as a Windows build-dependency.
3. **thunk-rs / VC-LTL / YY-Thunks on x86 and x64 only.** `easytier/build/main.rs`
   calls `thunk::thunk()` for x86/x86_64 Windows. It shells out to **`curl`**
   and **`7z`** (here NanaZip provided `7z`) to download VC-LTL 5.2.2 and
   YY-Thunks 1.1.7 from GitHub, again with no checksum. It then links our whole
   binary against `msvcrt.dll` with Win7 thunks (`cargo::warning=VC-LTL5 Enabled:
   6.0.6000.0(x64)`). This changes the CRT for *all* of kyokai-svc on x64. The
   build fails if `7z` is not on PATH.
4. **WinDivert (x86/x64 only):** `windivert` with `features=["static"]` is an
   unconditional target dependency. `windivert-sys` compiles C with MSVC `cl`.
   It worked with no extra setup, but it adds a C build and a WinDivert symbol
   surface even though faketcp is off.
5. **Git-pinned transitive deps:** `thunk-rs` (git, *no rev*: `cbbeec75` now),
   `tokio-websockets` (EasyTier fork, no rev), `service-manager-rs`
   (`branch = "main"`), `windivert-rust` (rev), and `tun-easytier` (unused). Our
   Cargo.lock pins them, but `cargo update` can move the unpinned ones.
6. **Toolchain race:** the repo root `rust-toolchain.toml` (1.99.1) triggered a
   rustup auto-install that collided with a parallel session (`could not rename
   'downloaded' file`). A retry fixed it.

## Runtime observations

- **Hole punching.** STUN reports `udp_nat=Symmetric tcp_nat=Symmetric` on both
  nodes. For sym↔sym, `get_punch_hole_method` only allows
  easy-sym↔easy-sym (port-predictable). These are plain `Symmetric`, so no punch
  is attempted and traffic stays
  relayed (`NO direct A<->B conn within 60s`). This proves nothing about
  cone/easy-sym NATs. **A two-machine, two-network test is still needed.**
- **Listeners enable direct paths.** With `S1_LISTENERS=udp://0.0.0.0:0,tcp://0.0.0.0:0`,
  port 0 is accepted and ephemeral ports are bound on both IPv4 and IPv6. The
  direct connector then forms a LAN TCP link in about 6 s, and routes/peers show
  `peer_id=85260439 tunnel=tcp local=tcp://0.0.0.0:59271 remote=…`.
- **STUN TXT lookup unsupported** in this feature set: `get txt record failed
  … stun.easytier.cn … this build does not include TXT DNS resolution` (needs
  `dns-resolver`/hickory). The other default STUN hosts (miwifi, bilibili, hitv)
  still produced a NAT type. Kyokai should set `stun_servers` explicitly
  (`host:port`) to servers it chooses.
- **DHCP latency and collisions.** The first lease arrives about 9–10 s after
  `start()` (peering with the server takes 150 ms). Once, two instances started
  at the same moment got the **same** IP (`run-connector-ws.log: DHCP: A=10.126.126.2
  B=10.126.126.2`, then connect refused). 0/12 collisions occurred in the dedicated
  trial (`dhcp-collision.log`). Kyokai must re-read its IPv4 before every connect
  and watch `DhcpIpv4Changed`, or assign IPs deterministically (static `ipv4`
  derived from the Kyokai id, with collision handling).
- **Log noise.** Without a subscriber, every event logs `WARN … Failed to send
  event … receiver count: 0`. Subscribe with `subscribe_native_instance_event`
  (also useful for DHCP/peer events) or filter `easytier::common::global_ctx`.
  `ERROR quinn::endpoint: socket closed` at stop is benign.
- Every instance also exposes a `ring://<inst_id>` listener (in-process only).

## Recommendations

1. **Feature set for kyokai-svc:** `smoltcp`, `aes-gcm`, `dhcp-ipv4`, plus
   `websocket` (wss:// gets through restrictive networks) and `quic`. Drop
   `wireguard`: it drags in boringtun, vpn-portal and ring-crypto, and its relay
   was the slowest (99 Mbit/s). Measure `udp`/`quic` throughput again on a
   quiet machine before committing. Consider `tcp-hole-punch` only after the
   two-network NAT test.
2. **Listeners vs. hole punching:** run outbound-only (`listeners=[]`) by default.
   It needs no firewall prompt and still gets UDP hole punching for cone NATs.
   Offer an opt-in "LAN direct" mode with `udp://0.0.0.0:0` (and maybe tcp), which
   gave the only direct path on this network. Always keep a relay server as
   fallback: symmetric NAT on both sides (common on campus and corporate
   networks) means relay-only.
3. **Identity:** never trust route `inst_id`/hostname (see e). Use them only to
   find a candidate virtual IP, then authenticate with Kyokai's Noise+OPAQUE.
4. **Readiness:** treat the instance as ready only when it has a DHCP IPv4 *and*
   a route to the peer. Re-validate the IP after `DhcpIpv4Changed`.
5. **Build hygiene before product use:**
   - vendor or pin `PROTOC`;
   - patch out or replace `thunk-rs` (it downloads unverified binaries with
     curl/7z and switches the CRT) with a `[patch]` or fork, or accept it
     deliberately for Win7 support;
   - pin the git deps with `rev`;
   - document the clang prerequisite in the build docs and CI;
   - make the build fully offline (`cargo vendor` + `PROTOC`).
6. **Risks:** prerelease tag; large dependency surface (~420–440 crates); the
   embedding API (`CoreInstance`, `create_native_instance`) is new in the 2.7
   refactor and may change; hole-punch effectiveness unproven; DHCP collision
   rare but real.
7. **Follow-up spikes:** (a) two machines on different NATs: hole punching,
   relay fallback, wss through a proxy; (b) macOS build/run; (c) `easytier-go`
   (WASI core in Go via wazero) as an alternative to a Rust engine for the
   Go/MyGo stack.
