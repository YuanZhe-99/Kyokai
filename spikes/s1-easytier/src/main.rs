//! S1 spike: two embedded EasyTier v2.7.0 instances in one process, joined
//! through a standard `easytier-core` server, exchanging bytes over the
//! in-process smoltcp data plane. No TUN, no admin rights.
//!
//! Usage: s1-easytier <server-host> <network-name> <network-secret> [peer-url...]
//! Peer URLs default to tcp://<server-host>:21010.

use std::{
    net::{Ipv4Addr, SocketAddr},
    sync::Arc,
    time::{Duration, Instant},
};

use anyhow::{Context, bail};
use easytier::instance::factory::{NativeCoreInstance, create_native_instance};
use easytier_core::config::toml::TomlConfig;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use uuid::Uuid;

/// Fixed namespace for Kyokai instance ids (UUIDv5 over the 9-digit Kyokai id).
const KYOKAI_NS: Uuid = Uuid::from_u128(0x6b796f6b_6169_4e00_8000_000000005331);
const TEST_PORT: u16 = 7001;
const NON_WHITELISTED_PORT: u16 = 7003;
const LOOPBACK_PORT: u16 = 7002;
const TIMEOUT: Duration = Duration::from_secs(5);

struct Args {
    network: String,
    secret: String,
    peers: Vec<String>,
}

fn instance_id(kyokai_id: &str) -> Uuid {
    Uuid::new_v5(&KYOKAI_NS, kyokai_id.as_bytes())
}

fn config(args: &Args, name: &str, kyokai_id: &str, hostname: &str) -> anyhow::Result<TomlConfig> {
    let peers: String = args
        .peers
        .iter()
        .map(|p| format!("[[peer]]\nuri = \"{p}\"\n"))
        .collect();
    let toml = format!(
        r#"
instance_name = "{name}"
instance_id = "{id}"
hostname = "{hostname}"
dhcp = true
listeners = [{listeners}]
tcp_whitelist = ["{TEST_PORT}"]
udp_whitelist = ["{TEST_PORT}"]

[network_identity]
network_name = "{net}"
network_secret = "{secret}"

{peers}
[flags]
no_tun = true
"#,
        id = instance_id(kyokai_id),
        // e.g. S1_LISTENERS=udp://0.0.0.0:0,tcp://0.0.0.0:0 (default: none)
        listeners = std::env::var("S1_LISTENERS")
            .unwrap_or_default()
            .split(',')
            .filter(|l| !l.is_empty())
            .map(|l| format!("\"{l}\""))
            .collect::<Vec<_>>()
            .join(", "),
        net = args.network,
        secret = args.secret,
    );
    TomlConfig::new_from_str(&toml)
}

async fn start(args: &Args, name: &str, kyokai_id: &str, hostname: &str) -> anyhow::Result<Arc<NativeCoreInstance>> {
    let inst = create_native_instance(config(args, name, kyokai_id, hostname)?)?;
    let t = Instant::now();
    inst.start().await.with_context(|| format!("start {name}"))?;
    println!(
        "[{name}] started in {:?}: inst_id={} peer_id={} hostname={hostname} listeners={:?}",
        t.elapsed(),
        inst.instance_id(),
        inst.peer_id(),
        inst.running_listeners()
    );
    Ok(inst)
}

async fn my_ipv4(inst: &NativeCoreInstance, deadline: Duration) -> anyhow::Result<Ipv4Addr> {
    let t = Instant::now();
    loop {
        if let Some(ip) = inst.node_snapshot().await.ipv4_addr {
            return Ok(ip.address());
        }
        if t.elapsed() > deadline {
            bail!("no DHCP IPv4 for {} after {deadline:?}", inst.instance_name());
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
}

/// Waits until `inst` has a route whose inst_id matches `target`.
async fn wait_route(inst: &NativeCoreInstance, target: Uuid, deadline: Duration) -> anyhow::Result<Duration> {
    let t = Instant::now();
    let target = target.to_string();
    loop {
        let routes = inst.route_snapshots().await;
        if routes.iter().any(|r| r.inst_id == target && r.ipv4_addr.is_some()) {
            return Ok(t.elapsed());
        }
        if t.elapsed() > deadline {
            bail!("{} never saw route to {target}", inst.instance_name());
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
}

/// Waits until `inst` has a direct (non-relayed) conn to `peer_id`.
async fn wait_direct(inst: &NativeCoreInstance, peer_id: u32, deadline: Duration) -> Option<Duration> {
    let t = Instant::now();
    loop {
        let peers = inst.peer_snapshots().await;
        if peers.iter().any(|p| p.peer_id == peer_id && !p.conns.is_empty()) {
            return Some(t.elapsed());
        }
        if t.elapsed() > deadline {
            return None;
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

async fn print_stun(inst: &NativeCoreInstance) {
    use easytier::proto::common::NatType;
    let n = inst.node_snapshot().await;
    let s = &n.stun_info;
    println!(
        "[{}] stun: udp_nat={:?} tcp_nat={:?} public_ip={:?} ports={}..{}; listeners={:?}",
        inst.instance_name(),
        NatType::try_from(s.udp_nat_type),
        NatType::try_from(s.tcp_nat_type),
        s.public_ip,
        s.min_port,
        s.max_port,
        n.listeners.iter().map(|u| u.as_str()).collect::<Vec<_>>(),
    );
}

async fn print_routes(inst: &NativeCoreInstance) {
    println!("[{}] route_snapshots():", inst.instance_name());
    for r in inst.route_snapshots().await {
        println!(
            "    peer_id={:<10} inst_id={} hostname={:<20} ipv4={} next_hop={} cost={} version={}",
            r.peer_id,
            r.inst_id,
            r.hostname,
            r.ipv4_addr.map(|a| a.to_string()).unwrap_or_else(|| "-".into()),
            r.next_hop_peer_id,
            r.cost,
            r.version,
        );
    }
}

async fn print_peers(inst: &NativeCoreInstance) {
    println!("[{}] peer_snapshots() (direct conns):", inst.instance_name());
    for p in inst.peer_snapshots().await {
        for c in &p.conns {
            let t = c.tunnel.as_ref();
            println!(
                "    peer_id={:<10} tunnel={} local={} remote={}",
                p.peer_id,
                t.map(|t| t.tunnel_type.as_str()).unwrap_or("-"),
                t.and_then(|t| t.local_addr.as_ref()).map(|u| u.url.as_str()).unwrap_or("-"),
                t.and_then(|t| t.remote_addr.as_ref()).map(|u| u.url.as_str()).unwrap_or("-"),
            );
        }
    }
}

/// Per-process resource snapshot via PowerShell (threads, TCP listen/UDP sockets, handles).
async fn resources(label: &str) {
    let pid = std::process::id();
    let script = format!(
        "$p=Get-Process -Id {pid}; \
         $t=@(Get-NetTCPConnection -OwningProcess {pid} -ErrorAction SilentlyContinue); \
         $u=@(Get-NetUDPEndpoint -OwningProcess {pid} -ErrorAction SilentlyContinue); \
         \"threads=$($p.Threads.Count) handles=$($p.HandleCount) ws_mb=$([int]($p.WorkingSet64/1MB)) tcp=$($t.Count) udp=$($u.Count)\"; \
         $t | ForEach-Object {{ \"    tcp $($_.LocalAddress):$($_.LocalPort) $($_.State) -> $($_.RemoteAddress):$($_.RemotePort)\" }}; \
         $u | ForEach-Object {{ \"    udp $($_.LocalAddress):$($_.LocalPort)\" }}"
    );
    let out = tokio::process::Command::new("powershell")
        .args(["-NoProfile", "-Command", &script])
        .output()
        .await;
    match out {
        Ok(o) => print!("[res:{label}] {}", String::from_utf8_lossy(&o.stdout)),
        Err(e) => println!("[res:{label}] failed: {e}"),
    }
}

/// (b)+(d): B binds TEST_PORT on its data plane; A connects; ping/pong both ways.
async fn check_echo(a: &NativeCoreInstance, b: &NativeCoreInstance, a_ip: Ipv4Addr, b_ip: Ipv4Addr) -> anyhow::Result<()> {
    let mut listener = b.data_plane_tcp_bind(TEST_PORT, TIMEOUT).await?;
    println!("[B] data_plane_tcp_bind -> listening on {}", listener.local_addr());
    let server = tokio::spawn(async move {
        let (mut s, peer) = listener.accept().await?;
        let mut buf = [0u8; 64];
        let n = s.read(&mut buf).await?;
        s.write_all(b"pong from B").await?;
        s.flush().await?;
        anyhow::Ok((peer, String::from_utf8_lossy(&buf[..n]).into_owned()))
    });
    let t = Instant::now();
    let mut c = a.data_plane_tcp_connect(SocketAddr::new(b_ip.into(), TEST_PORT), TIMEOUT).await?;
    println!("[A] data_plane_tcp_connect({b_ip}:{TEST_PORT}) ok in {:?}, local={}", t.elapsed(), c.local_addr());
    c.write_all(b"ping from A").await?;
    c.flush().await?;
    let mut buf = [0u8; 64];
    let n = tokio::time::timeout(TIMEOUT, c.read(&mut buf)).await??;
    let (peer, got) = tokio::time::timeout(TIMEOUT, server).await???;
    println!("[B] accept() peer={peer} received={got:?}");
    println!("[A] received={:?}", String::from_utf8_lossy(&buf[..n]));
    println!(
        "    accept() peer ip == A's virtual ip ({a_ip}): {}",
        if peer.ip() == std::net::IpAddr::V4(a_ip) { "YES" } else { "NO" }
    );
    Ok(())
}

/// Connects and reports whether a handshake + one echo round trip succeeded.
async fn try_connect(a: &NativeCoreInstance, dst: SocketAddr) -> String {
    let t = Instant::now();
    match a.data_plane_tcp_connect(dst, TIMEOUT).await {
        Ok(mut s) => {
            let _ = s.write_all(b"probe").await;
            let mut buf = [0u8; 16];
            match tokio::time::timeout(Duration::from_secs(3), s.read(&mut buf)).await {
                Ok(Ok(n)) if n > 0 => format!("CONNECTED and got {n} bytes back after {:?}", t.elapsed()),
                Ok(Ok(_)) => format!("connect() returned Ok but stream closed with no data ({:?})", t.elapsed()),
                Ok(Err(e)) => format!("connect() returned Ok but read failed: {e} ({:?})", t.elapsed()),
                Err(_) => format!("connect() returned Ok but no data within 3s ({:?})", t.elapsed()),
            }
        }
        Err(e) => format!("REFUSED/FAILED after {:?}: {e}", t.elapsed()),
    }
}

/// (c): an OS loopback listener on B's machine must not be reachable via B's virtual IP,
/// and a data-plane listener on a non-whitelisted port must be blocked by B's whitelist.
async fn check_isolation(a: &NativeCoreInstance, b: &NativeCoreInstance, b_ip: Ipv4Addr) -> anyhow::Result<()> {
    let os = tokio::net::TcpListener::bind(("127.0.0.1", LOOPBACK_PORT)).await?;
    tokio::spawn(async move {
        while let Ok((mut s, peer)) = os.accept().await {
            println!("    !!! OS loopback listener accepted {peer}");
            let _ = s.write_all(b"leak").await;
        }
    });
    println!("[B-host] OS TcpListener on 127.0.0.1:{LOOPBACK_PORT}");
    println!(
        "[A] connect {b_ip}:{LOOPBACK_PORT} (OS loopback port, whitelisted? no) -> {}",
        try_connect(a, SocketAddr::new(b_ip.into(), LOOPBACK_PORT)).await
    );

    let mut dp = b.data_plane_tcp_bind(NON_WHITELISTED_PORT, TIMEOUT).await?;
    tokio::spawn(async move {
        while let Ok((mut s, peer)) = dp.accept().await {
            println!("    !!! non-whitelisted data-plane listener accepted {peer}");
            let _ = s.write_all(b"leak").await;
        }
    });
    println!("[B] data_plane_tcp_bind({NON_WHITELISTED_PORT}) (not in tcp_whitelist)");
    println!(
        "[A] connect {b_ip}:{NON_WHITELISTED_PORT} -> {}",
        try_connect(a, SocketAddr::new(b_ip.into(), NON_WHITELISTED_PORT)).await
    );
    Ok(())
}

/// (g): one-way bulk transfer A -> B over the data plane.
async fn check_throughput(a: &NativeCoreInstance, b: &NativeCoreInstance, b_ip: Ipv4Addr, secs: u64) -> anyhow::Result<()> {
    let mut listener = b.data_plane_tcp_bind(TEST_PORT, TIMEOUT).await?;
    let sink = tokio::spawn(async move {
        let (mut s, _) = listener.accept().await?;
        let mut buf = vec![0u8; 256 * 1024];
        let mut total: u64 = 0;
        let t = Instant::now();
        loop {
            let n = s.read(&mut buf).await?;
            if n == 0 {
                break;
            }
            total += n as u64;
        }
        anyhow::Ok((total, t.elapsed()))
    });
    let mut c = a.data_plane_tcp_connect(SocketAddr::new(b_ip.into(), TEST_PORT), TIMEOUT).await?;
    let chunk = vec![0x5au8; 64 * 1024];
    let t = Instant::now();
    let mut sent: u64 = 0;
    while t.elapsed() < Duration::from_secs(secs) {
        c.write_all(&chunk).await?;
        sent += chunk.len() as u64;
    }
    c.shutdown().await?;
    drop(c);
    let (recv, dur) = tokio::time::timeout(Duration::from_secs(30), sink).await???;
    let mbps = recv as f64 * 8.0 / dur.as_secs_f64() / 1e6;
    println!(
        "[g] sent={} MiB recv={} MiB in {:.2}s => {:.1} Mbit/s ({:.1} MiB/s)",
        sent / (1 << 20),
        recv / (1 << 20),
        dur.as_secs_f64(),
        mbps,
        recv as f64 / dur.as_secs_f64() / (1 << 20) as f64
    );
    Ok(())
}

async fn privileges() {
    let out = tokio::process::Command::new("powershell")
        .args([
            "-NoProfile",
            "-Command",
            "$p=[Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent(); \
             \"IsAdministrator=$($p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))\"; \
             (whoami /groups | Select-String 'Mandatory Label').Line.Trim(); \
             \"Wintun/TAP adapters named Kyokai*: $(@(Get-NetAdapter -IncludeHidden | Where-Object Name -like 'Kyokai*').Count)\"",
        ])
        .output()
        .await;
    if let Ok(o) = out {
        print!("{}", String::from_utf8_lossy(&o.stdout));
    }
}

async fn run(args: Args) -> anyhow::Result<()> {
    let (id_a, id_b) = ("100000001", "100000002");
    println!("=== (h) privileges ===");
    privileges().await;
    resources("before-any-instance").await;

    println!("\n=== start A and B ===");
    println!("instance_id(A)=UUIDv5(ns,{id_a})={}", instance_id(id_a));
    println!("instance_id(B)=UUIDv5(ns,{id_b})={}", instance_id(id_b));
    let a = start(&args, "A", id_a, &format!("Kyokai-{id_a}")).await?;
    let b = start(&args, "B", id_b, &format!("Kyokai-{id_b}")).await?;
    let a_ip = my_ipv4(&a, Duration::from_secs(30)).await?;
    let b_ip = my_ipv4(&b, Duration::from_secs(30)).await?;
    println!("DHCP: A={a_ip} B={b_ip}");
    if std::env::var_os("S1_DHCP_ONLY").is_some() {
        // Does EasyTier resolve a simultaneous-start collision by itself?
        tokio::time::sleep(Duration::from_secs(20)).await;
        let (a2, b2) = (my_ipv4(&a, TIMEOUT).await?, my_ipv4(&b, TIMEOUT).await?);
        println!(
            "DHCP-ONLY first: A={a_ip} B={b_ip} collision={} | after 20s: A={a2} B={b2} collision={}",
            a_ip == b_ip,
            a2 == b2
        );
        a.stop().await;
        b.stop().await;
        return Ok(());
    }

    println!("\n=== (a) A sees B in route_snapshots ===");
    let d = wait_route(&a, instance_id(id_b), Duration::from_secs(30)).await?;
    println!("A saw B's route after {d:?}");
    print_routes(&a).await;
    print_peers(&a).await;
    let p2p_wait: u64 = std::env::var("S1_P2P_WAIT").ok().and_then(|v| v.parse().ok()).unwrap_or(45);
    match wait_direct(&a, b.peer_id(), Duration::from_secs(p2p_wait)).await {
        Some(d) => println!("DIRECT A<->B conn established after {d:?}"),
        None => println!("NO direct A<->B conn within {p2p_wait}s (traffic relayed via server)"),
    }
    print_stun(&a).await;
    print_stun(&b).await;
    print_routes(&a).await;
    print_peers(&a).await;
    resources("A+B running").await;

    println!("\n=== (b)+(d) data-plane echo A -> B:{TEST_PORT} (both sides whitelist only {TEST_PORT}) ===");
    check_echo(&a, &b, a_ip, b_ip).await?;

    println!("\n=== (c) isolation ===");
    check_isolation(&a, &b, b_ip).await?;

    println!("\n=== (g) throughput A -> B, 10 s ===");
    check_throughput(&a, &b, b_ip, 10).await?;
    print_peers(&a).await;
    if std::env::var_os("S1_SHORT").is_some() {
        a.stop().await;
        b.stop().await;
        println!("\nSHORT RUN COMPLETED");
        return Ok(());
    }

    println!("\n=== (e) duplicate inst_id: C uses B's inst_id ===");
    let c = start(&args, "C", id_b, "Kyokai-dup-of-B").await?;
    for i in 1..=3 {
        tokio::time::sleep(Duration::from_secs(5)).await;
        println!("-- t+{}s --", i * 5);
        print_routes(&a).await;
    }
    let c_ip = my_ipv4(&c, Duration::from_secs(10)).await.ok();
    println!("C ipv4={c_ip:?}");
    println!("A -> B:{TEST_PORT} while C shares B's inst_id:");
    match check_echo(&a, &b, a_ip, b_ip).await {
        Ok(()) => println!("    echo still works"),
        Err(e) => println!("    echo FAILED: {e:#}"),
    }
    c.stop().await;
    drop(c);

    println!("\n=== (f) stop + fresh start in the same process ===");
    let t = Instant::now();
    a.stop().await;
    b.stop().await;
    println!("stopped A,B in {:?}; states {:?} {:?}", t.elapsed(), a.state(), b.state());
    drop(a);
    drop(b);
    tokio::time::sleep(Duration::from_secs(3)).await;
    resources("after-stop-1").await;
    let cycles: u32 = std::env::var("S1_CYCLES").ok().and_then(|v| v.parse().ok()).unwrap_or(3);
    for round in 2..=cycles {
        println!("-- round {round} --");
        let a = start(&args, "A", id_a, &format!("Kyokai-{id_a}")).await?;
        let b = start(&args, "B", id_b, &format!("Kyokai-{id_b}")).await?;
        let a_ip = my_ipv4(&a, Duration::from_secs(30)).await?;
        let b_ip = my_ipv4(&b, Duration::from_secs(30)).await?;
        let d = wait_route(&a, instance_id(id_b), Duration::from_secs(30)).await?;
        println!("DHCP: A={a_ip} B={b_ip}; A saw B after {d:?}");
        if let Err(e) = check_echo(&a, &b, a_ip, b_ip).await {
            println!("    echo FAILED this round: {e:#}");
        }
        resources(&format!("running-{round}")).await;
        a.stop().await;
        b.stop().await;
        drop(a);
        drop(b);
        tokio::time::sleep(Duration::from_secs(3)).await;
        resources(&format!("after-stop-{round}")).await;
    }
    println!("\nALL CHECKS COMPLETED");
    Ok(())
}

fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("warn")),
        )
        .with_writer(std::io::stderr)
        .init();

    let mut it = std::env::args().skip(1);
    let usage = "usage: s1-easytier <server-host> <network-name> <network-secret> [peer-url...]";
    let host = it.next().context(usage)?;
    let network = it.next().context(usage)?;
    let secret = it.next().context(usage)?;
    let mut peers: Vec<String> = it.collect();
    if peers.is_empty() {
        peers.push(format!("tcp://{host}:21010"));
    }
    let args = Args { network, secret, peers };

    let rt = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(4)
        .enable_all()
        .build()?;
    rt.block_on(run(args))?;
    drop(rt);
    Ok(())
}
