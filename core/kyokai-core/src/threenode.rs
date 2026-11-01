//! The path baseline's three-node test: three embedded EasyTier nodes in one
//! network on this computer, a relay and two peers, where peer B's only
//! direct link to peer A runs through a proxy the test cuts. It measures how
//! long B takes to reach A again over the relay, and to go direct again
//! once the proxy heals.
//!
//! Run by hand (it listens on this computer's LAN address, since EasyTier
//! refuses loopback peers, and takes about a minute):
//!
//! ```text
//! cargo test --manifest-path core/Cargo.toml -p kyokai-core threenode -- --ignored --nocapture
//! ```

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::sync::Arc;
use std::time::{Duration, Instant};

use easytier::instance::factory::{NativeCoreInstance, create_native_instance};
use easytier_core::config::toml::TomlConfig;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::time::{sleep, timeout};

use crate::testproxy::Proxy;

/// The data plane port A echoes on.
const ECHO_PORT: u16 = 47310;
/// How long one attempt to reach A may take before the next one starts.
const TRY: Duration = Duration::from_secs(1);
/// How long any one step may take before the test gives up.
const STEP: Duration = Duration::from_secs(90);

/// Lan_ip is this computer's address toward the internet (no packet is
/// sent: connecting a UDP socket only picks the route).
fn lan_ip() -> IpAddr {
    let s = std::net::UdpSocket::bind((Ipv4Addr::UNSPECIFIED, 0)).unwrap();
    s.connect((Ipv4Addr::new(192, 0, 2, 1), 9)).unwrap();
    let ip = s.local_addr().unwrap().ip();
    assert!(!ip.is_loopback() && !ip.is_unspecified(), "no LAN address");
    ip
}

/// Free_port is a TCP port free on `ip` just now.
fn free_port(ip: IpAddr) -> u16 {
    std::net::TcpListener::bind((ip, 0))
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// Node starts one embedded node of the test network.
/// Inputs: `n` the last byte of its fixed virtual address; `listen` a TCP
/// listener URI or none; `peers` the URIs it dials; `disable_p2p` whether it
/// stays off direct links it did not dial itself.
/// Returns: the started node.
/// Notes: no TUN, hole punching, UPnP or STUN, so the only direct links are
/// the ones the test sets up.
async fn node(
    network: &str,
    n: u8,
    listen: Option<String>,
    peers: &[String],
    disable_p2p: bool,
) -> Arc<NativeCoreInstance> {
    let q = |v: &str| serde_json::to_string(v).unwrap();
    let listeners = listen.as_deref().map(q).unwrap_or_default();
    let peers: String = peers
        .iter()
        .map(|p| format!("[[peer]]\nuri = {}\n", q(p)))
        .collect();
    let toml = format!(
        r#"
instance_name = "threenode-{n}"
instance_id = "{id}"
hostname = "threenode-{n}"
ipv4 = "10.126.0.{n}"
dhcp = false
listeners = [{listeners}]
stun_servers = []
stun_servers_v6 = []

[network_identity]
network_name = {name}
network_secret = "threenode-secret"

{peers}
[flags]
no_tun = true
default_protocol = "tcp"
disable_p2p = {disable_p2p}
disable_udp_hole_punching = true
disable_tcp_hole_punching = true
disable_sym_hole_punching = true
disable_upnp = true
"#,
        id = uuid::Uuid::new_v4(),
        name = q(network),
    );
    let node = TomlConfig::new_from_str(&toml)
        .and_then(create_native_instance)
        .expect("node config");
    node.start().await.expect("node starts");
    node
}

/// Direct says whether `from` has a direct link to the node `to`.
async fn direct(from: &NativeCoreInstance, to: &NativeCoreInstance) -> bool {
    let id = to.peer_id();
    from.peer_snapshots()
        .await
        .iter()
        .any(|p| p.peer_id == id && !p.directly_connected_conns.is_empty())
}

/// Until polls `check` every 100 ms until it holds.
/// Returns: how long that took. Notes: panics after STEP, naming `what`.
async fn until<F: Future<Output = bool>>(what: &str, mut check: impl FnMut() -> F) -> Duration {
    let start = Instant::now();
    while !check().await {
        assert!(start.elapsed() < STEP, "{what}: not within {STEP:?}");
        sleep(Duration::from_millis(100)).await;
    }
    start.elapsed()
}

/// Reach opens a fresh connection from `b` to A's echo and sees a round trip.
/// Returns: whether it came back within TRY.
async fn reach(b: &NativeCoreInstance, a: SocketAddr) -> bool {
    timeout(TRY, async {
        let mut s = b.data_plane_tcp_connect(a, TRY).await.ok()?;
        s.write_all(b"ping").await.ok()?;
        let mut got = [0; 4];
        s.read_exact(&mut got).await.ok()?;
        (&got == b"ping").then_some(())
    })
    .await
    .ok()
    .flatten()
    .is_some()
}

#[ignore = "three live EasyTier nodes on the LAN address; run by hand"]
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn threenode_direct_to_relay_after_a_cut() {
    let ip = lan_ip();
    let network = format!("kyokai-threenode-{}", uuid::Uuid::new_v4().simple());
    let (relay_port, a_port) = (free_port(ip), free_port(ip));
    let tcp = |port: u16| format!("tcp://{}", SocketAddr::new(ip, port));

    let relay = node(&network, 1, Some(tcp(relay_port)), &[], false).await;
    let a = node(&network, 2, Some(tcp(a_port)), &[tcp(relay_port)], false).await;
    let proxy = Proxy::start(SocketAddr::new(ip, a_port)).await;
    let b = node(
        &network,
        3,
        None,
        &[tcp(relay_port), tcp(proxy.addr().port())],
        true,
    )
    .await;

    // A echoes on its data plane once it has its address.
    let mut listener = a
        .data_plane_tcp_bind(ECHO_PORT, Duration::from_secs(10))
        .await
        .expect("A listens");
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(async move {
                let (mut r, mut w) = tokio::io::split(s);
                let _ = tokio::io::copy(&mut r, &mut w).await;
            });
        }
    });
    let a_echo = SocketAddr::new(Ipv4Addr::new(10, 126, 0, 2).into(), ECHO_PORT);

    let up = until("B and A link directly, both reach the relay", || async {
        direct(&b, &a).await && direct(&b, &relay).await && direct(&a, &relay).await
    })
    .await;
    until("B reaches A", || reach(&b, a_echo)).await;
    println!("threenode: up, B direct to A, after {up:?}");
    // A connection opened before the cut, used again after it.
    let mut kept = b
        .data_plane_tcp_connect(a_echo, TRY)
        .await
        .expect("B connects to A");

    proxy.cut();
    // From the cut on, at once: fresh connections to A until one comes back,
    // B's view of its direct link, and the connection opened before it
    // (recorded, not asserted: whether it survives is part of the baseline).
    let (fresh, dropped, (kept_after, kept)) = tokio::join!(
        until("B reaches A over the relay", || reach(&b, a_echo)),
        until("B drops its direct link to A", || async {
            !direct(&b, &a).await
        }),
        async {
            let start = Instant::now();
            let mut got = [0; 4];
            let r = match kept.write_all(b"kept").await {
                Ok(()) => match timeout(STEP, kept.read_exact(&mut got)).await {
                    Ok(Ok(_)) if &got == b"kept" => "answered".to_string(),
                    Ok(Ok(_)) => "answered wrongly".to_string(),
                    Ok(Err(e)) => format!("ended: {e}"),
                    Err(_) => "no answer".to_string(),
                },
                Err(e) => format!("could not write: {e}"),
            };
            (start.elapsed(), r)
        },
    );
    println!("threenode: after the cut, a fresh connection reached A in {fresh:?}");
    println!("threenode: B dropped its direct link to A after {dropped:?}");
    println!("threenode: the connection opened before the cut {kept} after {kept_after:?}");

    proxy.heal();
    let back = until("B goes direct to A again", || async {
        direct(&b, &a).await
    })
    .await;
    println!("threenode: healed, B direct to A again after {back:?}");

    for n in [&b, &a, &relay] {
        n.stop().await;
    }
}

/// With Kyokai dropping the silent direct link (`net::close_direct`, as a
/// session that fell silent does on both sides after `SILENT`), a fresh
/// connection reaches A over the relay about 5 seconds after the cut (2 of
/// silence, then EasyTier's routes settle), not the 14 or so EasyTier
/// takes on its own.
#[tokio::test(flavor = "multi_thread")]
#[ignore = "starts three EasyTier nodes on this machine's LAN address"]
async fn threenode_dropping_the_direct_link_moves_to_the_relay_at_once() {
    let ip = lan_ip();
    let network = format!("kyokai-threenode-{}", uuid::Uuid::new_v4().simple());
    let (relay_port, a_port) = (free_port(ip), free_port(ip));
    let tcp = |port: u16| format!("tcp://{}", SocketAddr::new(ip, port));
    let relay = node(&network, 1, Some(tcp(relay_port)), &[], false).await;
    let a = node(&network, 2, Some(tcp(a_port)), &[tcp(relay_port)], false).await;
    let proxy = Proxy::start(SocketAddr::new(ip, a_port)).await;
    let b = node(
        &network,
        3,
        None,
        &[tcp(relay_port), tcp(proxy.addr().port())],
        true,
    )
    .await;
    let mut listener = a
        .data_plane_tcp_bind(ECHO_PORT, Duration::from_secs(10))
        .await
        .expect("A listens");
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            tokio::spawn(async move {
                let (mut r, mut w) = tokio::io::split(s);
                let _ = tokio::io::copy(&mut r, &mut w).await;
            });
        }
    });
    let a_echo = SocketAddr::new(Ipv4Addr::new(10, 126, 0, 2).into(), ECHO_PORT);
    until("B and A link directly, both reach the relay", || async {
        direct(&b, &a).await && direct(&b, &relay).await && direct(&a, &relay).await
    })
    .await;
    until("B reaches A", || reach(&b, a_echo)).await;

    proxy.cut();
    let start = Instant::now();
    tokio::time::sleep(Duration::from_secs(2)).await;
    let closed_b = crate::net::close_direct(&b, Ipv4Addr::new(10, 126, 0, 2)).await;
    let closed_a = crate::net::close_direct(&a, Ipv4Addr::new(10, 126, 0, 3)).await;
    until("B reaches A over the relay", || reach(&b, a_echo)).await;
    let fresh = start.elapsed();
    println!(
        "threenode: links dropped at 2 s (B {closed_b}, A {closed_a}); a fresh connection reached A after {fresh:?}"
    );
    assert!(fresh < Duration::from_secs(8), "{fresh:?}");

    proxy.heal();
    for n in [&b, &a, &relay] {
        n.stop().await;
    }
}
