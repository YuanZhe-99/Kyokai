//! The embedded EasyTier node: joins the network the user configured through a
//! standard EasyTier server, without TUN (doc/en-us/networking.md).
//!
//! EasyTier is an untrusted pipe: its routes, instance ids and hostnames are
//! hints for finding a device, never proof of who it is.

use crate::access::Permission;
use crate::session::{self, Ctx};
use crate::store::Secrets;
use easytier::instance::factory::{NativeCoreInstance, create_native_instance};
use easytier::proto::common::NatType;
use easytier_core::config::toml::TomlConfig;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::task::JoinHandle;
use uuid::Uuid;

/// Name of the settings entry in the secrets store (the network secret is in
/// it, so the whole entry is kept there).
const SECRET_NAME: &str = "network";
/// The only port Kyokai uses inside the virtual network; the data plane's
/// whitelists drop everything else. A host that accepts connections by
/// address listens on it (TCP) on this computer's own addresses too.
pub const KYOKAI_PORT: u16 = 47_310;
/// The ports the node may listen on for direct connections (TCP and UDP,
/// the first free one): with direct connections on, devices on the same
/// network reach each other by their local addresses, which hole punching
/// through a shared router often cannot.
const LISTEN_PORTS: std::ops::RangeInclusive<u16> = 47_311..=47_320;
/// Namespace of instance ids: `instance_id = UUIDv5(KYOKAI_NS, device id)`.
const KYOKAI_NS: Uuid = Uuid::from_u128(0x6b796f6b_6169_4e00_8000_000000005331);
/// Server URL schemes the minimal EasyTier build connects with.
const SCHEMES: &[&str] = &["tcp", "udp", "ws", "wss", "quic", "wg"];
/// Most servers one configuration may list.
const MAX_SERVERS: usize = 8;
/// Network secrets shorter than this are reported as weak.
const STRONG_SECRET_LEN: usize = 16;
/// How long binding the listener or opening a connection may take.
const DIAL_TIMEOUT: Duration = Duration::from_secs(10);
/// How often to retry binding the listener until the address is assigned.
const BIND_RETRY: Duration = Duration::from_secs(2);
/// How long a dropped session keeps trying to resume.
const RESUME_FOR: Duration = Duration::from_secs(60);
/// The pause between resume attempts.
const RESUME_RETRY: Duration = Duration::from_secs(3);
/// Most route entries tried for one Device ID (anyone can announce one).
const MAX_CANDIDATES: usize = 3;
/// How long stopping the node may take before the runtime is dropped.
const STOP_TIMEOUT: Duration = Duration::from_secs(3);

/// The user's network settings, as stored (secret included).
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Settings {
    /// Standard EasyTier server URLs, tried in order.
    pub servers: Vec<String>,
    /// The EasyTier network name.
    pub name: String,
    /// The EasyTier network secret.
    pub secret: String,
    /// Tries direct paths (hole punching, UPnP), which open a UDP socket
    /// on every address and make Windows Firewall ask; off, traffic goes
    /// through the server's relay (doc/en-us/networking.md, "Direct
    /// paths").
    #[serde(default)]
    pub direct: bool,
}

/// A `network.set` request: a missing secret keeps the stored one.
#[derive(Deserialize)]
pub struct Update {
    // Missing or null lists and names mean none (leaving the network).
    #[serde(default)]
    servers: Option<Vec<String>>,
    #[serde(default)]
    name: Option<String>,
    secret: Option<String>,
    /// Missing keeps the current choice.
    #[serde(default)]
    direct: Option<bool>,
}

/// Where the node is in its life.
#[derive(Clone)]
enum Phase {
    /// No network configured.
    Off,
    /// The node is starting or has not reached a server yet.
    Connecting,
    /// The node's start failed; the reason is safe to show.
    Failed(String),
}

/// The running node and its runtime.
pub struct Network {
    rt: tokio::runtime::Runtime,
    ctx: Arc<Ctx>,
    device_id: String,
    settings: Settings,
    node: Option<Arc<NativeCoreInstance>>,
    /// Starts the node, then accepts sessions; aborted when the node stops.
    serving: Option<JoinHandle<()>>,
    phase: Arc<Mutex<Phase>>,
    /// The helper always tries direct paths (its installer allowed them).
    always_direct: bool,
    /// The port the node listens on for other devices, if it does.
    listen: Option<u16>,
    /// Accepts sessions by address while the host's user allows it.
    by_address: Option<JoinHandle<()>>,
    /// Why accepting by address could not listen, safe to show.
    by_address_error: Option<String>,
}

impl Network {
    /// Starts the node with the stored settings, if there are any.
    /// Side effects: starts a tokio runtime; connects to the configured servers.
    /// Inputs: `always_direct`, set in the helper, whose installer allowed
    /// its own executable through the firewall.
    pub fn start(secrets: &Secrets, ctx: Arc<Ctx>, always_direct: bool) -> Result<Self, String> {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .thread_name("kyokai-net")
            .enable_all()
            .build()
            .map_err(|e| format!("network runtime: {e}"))?;
        let settings = secrets
            .get(SECRET_NAME)
            .and_then(|s| serde_json::from_str::<Settings>(&s).ok())
            .unwrap_or_default();
        let mut net = Network {
            rt,
            device_id: ctx.identity.device_id(),
            ctx,
            settings,
            node: None,
            serving: None,
            phase: Arc::new(Mutex::new(Phase::Off)),
            always_direct,
            listen: None,
            by_address: None,
            by_address_error: None,
        };
        net.restart();
        net.accept_by_address();
        Ok(net)
    }

    /// Returns the settings without the secret.
    pub fn settings(&self) -> Value {
        json!({
            "servers": self.settings.servers,
            "name": self.settings.name,
            "has_secret": !self.settings.secret.is_empty(),
            "weak_secret": is_weak(&self.settings.secret),
            "direct": self.settings.direct || self.always_direct,
        })
    }

    /// Validates and stores new settings, then restarts the node with them.
    /// Side effects: rewrites the secrets store; reconnects.
    pub fn update(&mut self, secrets: &Secrets, update: Update) -> Result<(), String> {
        let settings = Settings {
            secret: update
                .secret
                .unwrap_or_else(|| self.settings.secret.clone()),
            servers: update
                .servers
                .unwrap_or_default()
                .iter()
                .map(|s| s.trim().to_owned())
                .collect(),
            name: update.name.unwrap_or_default().trim().to_owned(),
            direct: update.direct.unwrap_or(self.settings.direct),
        };
        validate(&settings)?;
        let text =
            zeroize::Zeroizing::new(serde_json::to_string(&settings).map_err(|e| e.to_string())?);
        secrets.set(SECRET_NAME, &text)?;
        self.settings = settings;
        self.restart();
        Ok(())
    }

    /// The node's status: never a secret, never stale (read when asked).
    pub fn status(&self) -> Value {
        let phase = self.phase.lock().map(|p| p.clone()).unwrap_or(Phase::Off);
        if let Phase::Failed(error) = phase {
            return json!({"state": "error", "error": error});
        }
        let Some(node) = &self.node else {
            return json!({"state": "off"});
        };
        let (me, peers, routes) = self.rt.block_on(async {
            (
                node.node_snapshot().await,
                node.peer_snapshots().await,
                node.route_snapshots().await,
            )
        });
        let my_peer = node.peer_id();
        let reached_server = peers.iter().any(|p| p.conns.iter().any(|c| !c.is_closed));
        let devices: Vec<Value> = routes
            .iter()
            .filter(|r| r.peer_id != my_peer)
            .filter_map(|r| {
                let id = r.hostname.strip_prefix("Kyokai-")?;
                let direct = peers
                    .iter()
                    .any(|p| p.peer_id == r.peer_id && !p.directly_connected_conns.is_empty());
                Some(json!({
                    "device_id": id,
                    "path": if direct { "direct" } else { "relay" },
                    "latency_ms": r.path_latency.max(0),
                }))
            })
            .collect();
        json!({
            "state": if reached_server { "online" } else { "connecting" },
            "address": me.ipv4_addr.map(|a| a.address().to_string()),
            "nat": nat_name(me.stun_info.udp_nat_type),
            "listen_port": self.listen,
            "devices": devices,
        })
    }

    /// Stops the current node and starts one for the current settings.
    fn restart(&mut self) {
        self.stop_node();
        // Settings stored by 0.2.0 may name a network without a server
        // (it took other devices' addresses then): there is nothing to join.
        if self.settings.name.is_empty() || self.settings.servers.is_empty() {
            self.set_phase(Phase::Off);
            return;
        }
        // EasyTier builds its peer manager on the current Tokio runtime.
        let _rt = self.rt.enter();
        let direct = self.settings.direct || self.always_direct;
        let listen = if direct { free_port() } else { None };
        self.listen = listen;
        let node = match config(&self.settings, &self.device_id, direct, listen)
            .and_then(create_native_instance)
        {
            Ok(node) => node,
            Err(e) => {
                self.set_phase(Phase::Failed(format!("network settings: {e}")));
                return;
            }
        };
        self.set_phase(Phase::Connecting);
        let phase = self.phase.clone();
        let starting = node.clone();
        let ctx = self.ctx.clone();
        self.serving = Some(self.rt.spawn(async move {
            let fail = |e: String| {
                if let Ok(mut p) = phase.lock() {
                    *p = Phase::Failed(e);
                }
            };
            if let Err(e) = starting.start().await {
                return fail(format!("{e:#}"));
            }
            // The data plane binds only once the node has its virtual address,
            // and a listener stays on the address it bound: when DHCP gives
            // the node another one, bind again.
            loop {
                let address = starting.node_snapshot().await.ipv4_addr;
                let Some(address) = address else {
                    tokio::time::sleep(BIND_RETRY).await;
                    continue;
                };
                let mut listener = match starting
                    .data_plane_tcp_bind(KYOKAI_PORT, DIAL_TIMEOUT)
                    .await
                {
                    Ok(l) => l,
                    Err(_) => {
                        tokio::time::sleep(BIND_RETRY).await;
                        continue;
                    }
                };
                let mut check = tokio::time::interval(BIND_RETRY);
                loop {
                    tokio::select! {
                        accepted = listener.accept() => match accepted {
                            Ok((stream, from)) => {
                                tokio::spawn(session::serve(ctx.clone(), stream, from.ip()));
                            }
                            Err(_) => break,
                        },
                        _ = check.tick() => {
                            if starting.node_snapshot().await.ipv4_addr != Some(address) {
                                break;
                            }
                        }
                    }
                }
            }
        }));
        self.node = Some(node);
    }

    /// Starts a session with the device that announces `device_id`, or
    /// with the one at `address` (an IP address or domain name, with or
    /// without a port), which must prove to be that device.
    /// Returns: the session's id at once; its progress shows in the status.
    /// Notes: every route that announces the Device ID is a candidate (up to
    /// MAX_CANDIDATES); the handshake decides which one is the device. A
    /// session by address needs no network.
    pub fn connect(
        &self,
        device_id: &str,
        address: Option<String>,
        password: zeroize::Zeroizing<String>,
        want: Vec<Permission>,
        remember: session::Remember,
    ) -> Result<u64, String> {
        let route = match address {
            Some(address) => {
                split_address(&address)?;
                Route::Address(address)
            }
            None => Route::Network(self.node.clone().ok_or("not connected to a network")?),
        };
        let (id, stop) = session::begin(&self.ctx, device_id, remember);
        if let Route::Address(address) = &route {
            session::dials_address(&self.ctx, id, address);
        }
        let ctx = self.ctx.clone();
        let device_id = device_id.to_owned();
        self.rt.spawn(async move {
            let mut resume: Option<zeroize::Zeroizing<String>> = None;
            let mut lost_at = None;
            loop {
                if resume.is_some() {
                    session::reconnecting(&ctx, id);
                    // Between attempts a local stop ends the session at once.
                    tokio::select! {
                        _ = stop.notified() => return session::give_up(&ctx, id, session::Failure::Broken),
                        _ = tokio::time::sleep(RESUME_RETRY) => {}
                    }
                }
                let resume_with = resume.take();
                let dialed = match &route {
                    Route::Network(node) => dial(node, &ctx, id, &stop, &device_id, &password, &want, resume_with).await,
                    Route::Address(address) => match dial_address(address).await {
                        Ok(stream) => Dialed::Outcome(
                            session::connect(ctx.clone(), id, stop.clone(), stream, device_id.clone(), password.clone(), want.clone(), resume_with).await,
                        ),
                        Err(_) => Dialed::Nothing(session::Failure::Unreachable, resume_with),
                    },
                };
                let failure = match dialed {
                    Dialed::Outcome(session::Outcome::Done) => return,
                    Dialed::Outcome(session::Outcome::Lost(token)) => {
                        lost_at.get_or_insert_with(std::time::Instant::now);
                        resume = Some(token);
                        session::Failure::Broken
                    }
                    Dialed::Outcome(session::Outcome::WrongDevice) => session::Failure::WrongDevice,
                    Dialed::Nothing(f, token) => {
                        resume = token;
                        f
                    }
                };
                // Keep resuming only while a token is held and the window lasts.
                if resume.is_none() || lost_at.is_some_and(|t: std::time::Instant| t.elapsed() > RESUME_FOR) {
                    return session::give_up(&ctx, id, failure);
                }
            }
        });
        Ok(id)
    }

    /// Starts or stops accepting sessions by address, as the host's user
    /// set it: TCP on KYOKAI_PORT on every address of this computer, a
    /// connection from outside the allowed ranges closed at once.
    /// Side effects: binds or closes the listeners; Windows Firewall may
    /// ask about them.
    pub fn accept_by_address(&mut self) {
        if let Some(task) = self.by_address.take() {
            task.abort();
            // Its listeners close only once the task is dropped: wait, so
            // the port binds again (regression: changing the ranges left
            // the port taken by the old listener).
            let _ = self.rt.block_on(task);
        }
        self.by_address_error = None;
        let by = self.ctx.access.by_address();
        if !by.on {
            return;
        }
        let _rt = self.rt.enter();
        let v4 = listen_tcp(IpAddr::V4(Ipv4Addr::UNSPECIFIED));
        // Without IPv6 on this computer, IPv4 alone.
        let v6 = listen_tcp(IpAddr::V6(Ipv6Addr::UNSPECIFIED)).ok();
        let v4 = match v4 {
            Ok(l) => l,
            Err(e) => {
                self.by_address_error = Some(format!("port {KYOKAI_PORT}: {e}"));
                return;
            }
        };
        let ctx = self.ctx.clone();
        self.by_address = Some(self.rt.spawn(async move {
            loop {
                let accepted = match &v6 {
                    Some(v6) => tokio::select! {
                        a = v4.accept() => a,
                        a = v6.accept() => a,
                    },
                    None => v4.accept().await,
                };
                let Ok((stream, from)) = accepted else {
                    // Out of descriptors and the like: try again shortly.
                    tokio::time::sleep(BIND_RETRY).await;
                    continue;
                };
                if !by.admits(from.ip()) {
                    continue; // dropped: closed without a word
                }
                let _ = stream.set_nodelay(true);
                tokio::spawn(session::serve(
                    ctx.clone(),
                    stream,
                    from.ip().to_canonical(),
                ));
            }
        }));
    }

    /// Accepting by address as the status shows it: the setting, and
    /// whether it listens.
    pub fn by_address_status(&self) -> Value {
        let by = self.ctx.access.by_address();
        json!({
            "on": by.on,
            "ranges": by.ranges,
            "port": self.by_address.is_some().then_some(KYOKAI_PORT),
            "error": self.by_address_error,
        })
    }

    /// The runtime's handle, for work that must not hold the core's lock
    /// (identifying the device at an address).
    pub fn handle(&self) -> tokio::runtime::Handle {
        self.rt.handle().clone()
    }

    fn stop_node(&mut self) {
        if let Some(serving) = self.serving.take() {
            serving.abort();
        }
        if let Some(node) = self.node.take() {
            let _ = self
                .rt
                .block_on(async { tokio::time::timeout(STOP_TIMEOUT, node.stop()).await });
        }
    }

    fn set_phase(&self, phase: Phase) {
        if let Ok(mut p) = self.phase.lock() {
            *p = phase;
        }
    }
}

impl Drop for Network {
    fn drop(&mut self) {
        if let Some(task) = self.by_address.take() {
            task.abort();
        }
        self.stop_node();
    }
}

/// How a controller reaches a host.
enum Route {
    /// The routes of the EasyTier network that announce its Device ID.
    Network(Arc<NativeCoreInstance>),
    /// An address as the person typed it.
    Address(String),
}

/// A TCP listener on KYOKAI_PORT of every address of one family (IPv6
/// only for IPv6, so both families can listen side by side).
fn listen_tcp(any: IpAddr) -> std::io::Result<tokio::net::TcpListener> {
    use socket2::{Domain, Protocol, Socket, Type};
    let domain = if any.is_ipv4() {
        Domain::IPV4
    } else {
        Domain::IPV6
    };
    let socket = Socket::new(domain, Type::STREAM, Some(Protocol::TCP))?;
    if any.is_ipv6() {
        socket.set_only_v6(true)?;
    }
    socket.bind(&SocketAddr::new(any, KYOKAI_PORT).into())?;
    socket.listen(64)?;
    socket.set_nonblocking(true)?;
    tokio::net::TcpListener::from_std(socket.into())
}

/// Splits an address as typed into a host and a port: an IP address or
/// domain name, with KYOKAI_PORT unless it names one (`host:port`,
/// `[v6]:port`).
pub fn split_address(address: &str) -> Result<(String, u16), String> {
    let bad = || format!("{address:?} is not an address");
    let a = address.trim();
    if a.is_empty()
        || a.len() > 270
        || a.contains("://")
        || a.chars()
            .any(|c| c.is_whitespace() || c.is_control() || c == '/')
    {
        return Err(bad());
    }
    if let Ok(ip) = a.parse::<IpAddr>() {
        return Ok((ip.to_string(), KYOKAI_PORT));
    }
    let (host, port) = if let Some(rest) = a.strip_prefix('[') {
        let (ip, rest) = rest.split_once(']').ok_or_else(bad)?;
        ip.parse::<Ipv6Addr>().map_err(|_| bad())?;
        match rest {
            "" => (ip, None),
            _ => (ip, Some(rest.strip_prefix(':').ok_or_else(bad)?)),
        }
    } else {
        match a.split_once(':') {
            Some((host, port)) => (host, Some(port)),
            None => (a, None),
        }
    };
    let port = match port {
        Some(p) => p.parse::<u16>().ok().filter(|&p| p != 0).ok_or_else(bad)?,
        None => KYOKAI_PORT,
    };
    if host.is_empty() {
        return Err(bad());
    }
    Ok((host.to_owned(), port))
}

/// Opens a TCP connection to an address as typed: every address its name
/// resolves to, in order, until one answers.
async fn dial_address(address: &str) -> Result<tokio::net::TcpStream, session::Failure> {
    let (host, port) = split_address(address).map_err(|_| session::Failure::Unreachable)?;
    let addrs = tokio::time::timeout(DIAL_TIMEOUT, tokio::net::lookup_host((host.as_str(), port)))
        .await
        .map_err(|_| session::Failure::Unreachable)?
        .map_err(|_| session::Failure::Unreachable)?;
    for addr in addrs {
        if let Ok(Ok(stream)) =
            tokio::time::timeout(DIAL_TIMEOUT, tokio::net::TcpStream::connect(addr)).await
        {
            let _ = stream.set_nodelay(true);
            return Ok(stream);
        }
    }
    Err(session::Failure::Unreachable)
}

/// Learns which device answers at an address (the GUI then connects to
/// that Device ID there).
/// Returns: its Device ID, or why there is none: `unreachable`, `incompatible`
/// (another protocol version) or `broken` (not a Kyokai device, or it broke
/// off).
pub async fn identify(address: &str) -> Result<String, &'static str> {
    let stream = dial_address(address).await.map_err(|_| "unreachable")?;
    session::identify(stream).await.map_err(|f| match f {
        session::Failure::Incompatible => "incompatible",
        _ => "broken",
    })
}

/// What one round of dialing a Device ID came to.
enum Dialed {
    /// A handshake ran on some route.
    Outcome(session::Outcome),
    /// No route answered, or every one was a squatter; the resume token, if
    /// any, is handed back unused.
    Nothing(session::Failure, Option<zeroize::Zeroizing<String>>),
}

/// Tries the routes that announce `device_id` (at most MAX_CANDIDATES) and
/// runs the session on the first that proves to be the device.
#[allow(clippy::too_many_arguments)]
async fn dial(
    node: &NativeCoreInstance,
    ctx: &Arc<Ctx>,
    id: u64,
    stop: &Arc<tokio::sync::Notify>,
    device_id: &str,
    password: &zeroize::Zeroizing<String>,
    want: &[Permission],
    resume: Option<zeroize::Zeroizing<String>>,
) -> Dialed {
    let hostname = format!("Kyokai-{device_id}");
    let candidates: Vec<Ipv4Addr> = node
        .route_snapshots()
        .await
        .into_iter()
        .filter(|r| r.hostname == hostname)
        .filter_map(|r| r.ipv4_addr.and_then(|a| a.address).map(Ipv4Addr::from))
        .take(MAX_CANDIDATES)
        .collect();
    let mut failure = session::Failure::Unreachable;
    for ip in candidates {
        let addr = SocketAddr::new(ip.into(), KYOKAI_PORT);
        let Ok(stream) = node.data_plane_tcp_connect(addr, DIAL_TIMEOUT).await else {
            continue;
        };
        let outcome = session::connect(
            ctx.clone(),
            id,
            stop.clone(),
            stream,
            device_id.to_owned(),
            password.clone(),
            want.to_vec(),
            resume.clone(),
        )
        .await;
        match outcome {
            // A squatter with another key fails the handshake: try the next.
            session::Outcome::WrongDevice => failure = session::Failure::WrongDevice,
            other => return Dialed::Outcome(other),
        }
    }
    Dialed::Nothing(failure, resume)
}

/// The instance id other devices compute for a Device ID.
pub fn instance_id(device_id: &str) -> Uuid {
    Uuid::new_v5(&KYOKAI_NS, device_id.as_bytes())
}

/// A port of LISTEN_PORTS free for both TCP and UDP on every IPv4 address,
/// or None: EasyTier refuses to start when a listener it was given cannot
/// bind, so a second engine on this computer runs without one.
fn free_port() -> Option<u16> {
    LISTEN_PORTS.into_iter().find(|&port| {
        std::net::TcpListener::bind((Ipv4Addr::UNSPECIFIED, port)).is_ok()
            && std::net::UdpSocket::bind((Ipv4Addr::UNSPECIFIED, port)).is_ok()
    })
}

/// Builds the node's configuration: no TUN, DHCP, data plane whitelists that
/// admit only Kyokai's port. With `direct`, hole punching, UPnP and direct
/// connections, preferring UDP, and listeners on `listen` (TCP and UDP) so
/// that devices on the same network connect directly; without it nothing
/// listens or waits for unsolicited packets, but the node still dials a
/// device that listens (TCP first, an outgoing connection no firewall asks
/// about), so a device with direct connections on reaches it directly too.
fn config(
    s: &Settings,
    device_id: &str,
    direct: bool,
    listen: Option<u16>,
) -> anyhow::Result<TomlConfig> {
    // JSON string literals are valid TOML basic strings, which keeps user
    // input from changing the document's structure.
    let q = |v: &str| serde_json::to_string(v).unwrap_or_default();
    let peers: String = s
        .servers
        .iter()
        .map(|p| format!("[[peer]]\nuri = {}\n", q(p)))
        .collect();
    let toml = format!(
        r#"
instance_name = "kyokai"
instance_id = "{id}"
hostname = "Kyokai-{device_id}"
dhcp = true
listeners = [{listeners}]
tcp_whitelist = ["{KYOKAI_PORT}"]
udp_whitelist = ["{KYOKAI_PORT}"]
{stun}
[network_identity]
network_name = {name}
network_secret = {secret}

{peers}
[flags]
no_tun = true
default_protocol = "{protocol}"
disable_udp_hole_punching = {off}
disable_tcp_hole_punching = {off}
disable_sym_hole_punching = {off}
disable_upnp = {off}
"#,
        off = !direct,
        protocol = if direct { "udp" } else { "tcp" },
        listeners = match listen.filter(|_| direct) {
            Some(port) => format!(r#""tcp://0.0.0.0:{port}", "udp://0.0.0.0:{port}""#),
            None => String::new(),
        },
        // STUN only serves hole punching; its UDP sockets on every address
        // would make Windows Firewall ask too.
        stun = if direct {
            ""
        } else {
            "stun_servers = []
stun_servers_v6 = []
"
        },
        id = instance_id(device_id),
        name = q(&s.name),
        secret = q(&s.secret),
    );
    TomlConfig::new_from_str(&toml)
}

/// Checks settings at the local API boundary.
fn validate(s: &Settings) -> Result<(), String> {
    if s.servers.is_empty() != s.name.is_empty() {
        return Err("a network needs a server and a network name".into());
    }
    if s.servers.len() > MAX_SERVERS {
        return Err(format!("at most {MAX_SERVERS} servers"));
    }
    if s.name.chars().count() > 64 || s.name.chars().any(char::is_control) {
        return Err("the network name is too long or has control characters".into());
    }
    if s.secret.chars().any(char::is_control) {
        return Err("the network secret has control characters".into());
    }
    for server in &s.servers {
        let (scheme, rest) = server
            .split_once("://")
            .ok_or_else(|| format!("{server:?} is not a server URL"))?;
        if !SCHEMES.contains(&scheme) {
            return Err(format!(
                "{scheme:?} servers are not supported (use {})",
                SCHEMES.join(", ")
            ));
        }
        if rest.is_empty() || server.chars().any(|c| c.is_whitespace() || c.is_control()) {
            return Err(format!("{server:?} is not a server URL"));
        }
    }
    Ok(())
}

fn is_weak(secret: &str) -> bool {
    !secret.is_empty() && secret.chars().count() < STRONG_SECRET_LEN
}

/// The NAT type as a stable name (enums cross boundaries as names).
fn nat_name(n: i32) -> &'static str {
    match NatType::try_from(n).unwrap_or(NatType::Unknown) {
        NatType::Unknown => "unknown",
        NatType::OpenInternet => "open",
        NatType::NoPat => "no-pat",
        NatType::FullCone => "full-cone",
        NatType::Restricted => "restricted",
        NatType::PortRestricted => "port-restricted",
        NatType::Symmetric => "symmetric",
        NatType::SymUdpFirewall => "symmetric-firewall",
        NatType::SymmetricEasyInc | NatType::SymmetricEasyDec => "symmetric-easy",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn settings(servers: &[&str], name: &str, secret: &str) -> Settings {
        Settings {
            servers: servers.iter().map(|s| s.to_string()).collect(),
            name: name.into(),
            secret: secret.into(),
            direct: false,
        }
    }

    #[test]
    fn validation() {
        let cases = [
            (settings(&[], "", ""), true),
            (settings(&["tcp://example.org:11010"], "kyokai", "s"), true),
            (settings(&["wss://example.org/et"], "kyokai", ""), true),
            (settings(&["tcp://example.org:11010"], "", ""), false),
            (settings(&[], "kyokai", ""), false),
            (settings(&["faketcp://example.org:1"], "n", ""), false),
            (settings(&["example.org:11010"], "n", ""), false),
            (settings(&["tcp://"], "n", ""), false),
            (settings(&["tcp://a b"], "n", ""), false),
            (settings(&["tcp://a"], "n\n", ""), false),
            (settings(&["tcp://a"; 9], "n", ""), false),
        ];
        for (s, ok) in cases {
            assert_eq!(validate(&s).is_ok(), ok, "{:?} {:?}", s.servers, s.name);
        }
    }

    /// An address as typed: a host and Kyokai's port unless it names one.
    #[test]
    fn addresses_as_typed() {
        for (address, want) in [
            ("192.168.1.20", Some(("192.168.1.20", 47310))),
            (" 192.168.1.20:5000 ", Some(("192.168.1.20", 5000))),
            ("office.example.org", Some(("office.example.org", 47310))),
            (
                "office.example.org:9000",
                Some(("office.example.org", 9000)),
            ),
            ("fe80::1", Some(("fe80::1", 47310))),
            (
                "[fd7a:115c:a1e0::1]:47312",
                Some(("fd7a:115c:a1e0::1", 47312)),
            ),
            ("[fe80::1]", Some(("fe80::1", 47310))),
            ("", None),
            ("a b", None),
            ("tcp://a:1", None),
            ("a:0", None),
            ("a:x", None),
            (":47310", None),
            ("[nope]:1", None),
        ] {
            let got = split_address(address).ok();
            let want = want.map(|(h, p)| (h.to_owned(), p));
            assert_eq!(got, want, "{address:?}");
        }
    }

    #[test]
    fn config_quotes_user_input() {
        use easytier_core::config::toml::ConfigLoader;
        let name = "n\"\nnetwork_secret = \"x";
        let s = settings(&["tcp://a:1"], name, "p\"w\\");
        let cfg = config(&s, "100000002", false, None).unwrap();
        let identity = cfg.get_network_identity();
        assert_eq!(identity.network_name, name);
        assert_eq!(identity.network_secret.as_deref(), Some("p\"w\\"));
        assert_eq!(cfg.get_hostname(), "Kyokai-100000002");
    }

    #[test]
    fn leaving_accepts_null_lists() {
        let u: Update = serde_json::from_str(r#"{"servers": null, "name": ""}"#).unwrap();
        assert!(u.servers.is_none());
        let u: Update = serde_json::from_str("{}").unwrap();
        assert!(u.name.is_none());
    }

    #[test]
    fn direct_paths_are_off_unless_chosen() {
        use easytier_core::config::toml::ConfigLoader;
        let s = settings(&["tcp://a:1"], "n", "p");
        for direct in [false, true] {
            let cfg = config(&s, "100000002", direct, Some(47_311)).unwrap();
            let f = cfg.get_flags();
            // Off: nothing waits for unsolicited packets, so no firewall
            // prompt; the node still dials a device that listens (regression:
            // with disable_p2p a device left off kept a LAN peer on the relay).
            assert_eq!(f.disable_udp_hole_punching, !direct);
            assert_eq!(f.disable_tcp_hole_punching, !direct);
            assert_eq!(f.disable_sym_hole_punching, !direct);
            assert_eq!(f.disable_upnp, !direct);
            assert!(!f.disable_p2p);
            assert_eq!(f.default_protocol, if direct { "udp" } else { "tcp" });
            let stun = cfg.get_stun_servers();
            assert_eq!(
                stun.is_some_and(|v| v.is_empty()),
                !direct,
                "no STUN without direct paths"
            );
            // On: devices on the same network reach the listeners directly.
            let listeners: Vec<String> = cfg
                .get_listeners()
                .unwrap_or_default()
                .iter()
                .map(|u| u.to_string())
                .collect();
            let want: &[&str] = if direct {
                &["tcp://0.0.0.0:47311", "udp://0.0.0.0:47311"]
            } else {
                &[]
            };
            assert_eq!(listeners, want);
            assert!(f.no_tun);
        }
        // No free port: direct paths without a listener.
        let cfg = config(&s, "100000002", true, None).unwrap();
        assert!(cfg.get_listeners().unwrap_or_default().is_empty());
    }

    /// The port check finds a port of the range, and skips one taken.
    #[test]
    fn a_taken_listen_port_is_skipped() {
        let Some(first) = free_port() else {
            return eprintln!("skipped: no port of the range is free");
        };
        let _held = std::net::UdpSocket::bind((Ipv4Addr::UNSPECIFIED, first)).unwrap();
        let next = free_port();
        assert_ne!(next, Some(first));
        assert!(next.is_none_or(|p| LISTEN_PORTS.contains(&p)));
    }

    #[test]
    fn instance_id_is_stable() {
        assert_eq!(
            instance_id("100000002").to_string(),
            "d409b924-1be1-5f9e-b719-f4bdaa8608b1"
        );
    }
}
