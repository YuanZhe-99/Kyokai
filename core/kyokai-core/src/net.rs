//! The embedded EasyTier nodes: one for each network the user joined, each
//! through standard EasyTier servers, without TUN, every one announcing this
//! device's Device ID (doc/en-us/networking.md).
//!
//! EasyTier is an untrusted pipe: its routes, instance ids and hostnames are
//! hints for finding a device, never proof of who it is.

use crate::access::{Permission, Source};
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

/// Name of the settings entry in the secrets store: every network the user
/// keeps, their secrets included, so the whole entry is kept there.
const SECRET_NAME: &str = "networks";
/// The entry of the one network 0.3.1 and earlier kept, moved into
/// SECRET_NAME once.
const OLD_SECRET_NAME: &str = "network";
/// Most networks one device keeps.
const MAX_PROFILES: usize = 8;
/// Longest name a network is shown by.
const MAX_LABEL: usize = 64;
/// The only port Kyokai uses inside the virtual network; the data plane's
/// whitelists drop everything else. A host that accepts connections by
/// address listens on it (TCP) on this computer's own addresses too.
pub const KYOKAI_PORT: u16 = 47_310;
/// The ports the nodes may listen on for direct connections (TCP and UDP,
/// the first free one each): with direct connections on, devices on the
/// same network reach each other by their local addresses, which hole
/// punching through a shared router often cannot.
const LISTEN_PORTS: std::ops::RangeInclusive<u16> = 47_311..=47_320;
/// Namespace of instance ids: `instance_id = UUIDv5(KYOKAI_NS, device id)`.
const KYOKAI_NS: Uuid = Uuid::from_u128(0x6b796f6b_6169_4e00_8000_000000005331);
/// Server URL schemes the minimal EasyTier build connects with.
const SCHEMES: &[&str] = &["tcp", "udp", "ws", "wss", "quic", "wg"];
/// Most servers one network may list.
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
/// Most route entries tried for one Device ID in each network (anyone can
/// announce one, so one network's squatters cannot crowd out another's).
const MAX_CANDIDATES: usize = 3;
/// How long stopping the nodes may take before they are dropped.
const STOP_TIMEOUT: Duration = Duration::from_secs(3);

/// A network the user keeps: its servers, EasyTier network name and secret,
/// the name Kyokai shows for it, and whether this device joins it.
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Profile {
    /// Random and stable (16 hex digits): how requests and the status name it.
    pub id: String,
    /// The name shown; empty shows the network name.
    #[serde(default)]
    pub label: String,
    /// Standard EasyTier server URLs, tried in order.
    pub servers: Vec<String>,
    /// The EasyTier network name.
    pub name: String,
    /// The EasyTier network secret.
    pub secret: String,
    /// Whether this device joins it now.
    #[serde(default)]
    pub joined: bool,
}

impl Profile {
    /// The name the network is shown by.
    fn shown(&self) -> &str {
        if self.label.is_empty() {
            &self.name
        } else {
            &self.label
        }
    }

    /// Whether a node of `self` would be a node of `other`: the same servers,
    /// network name and secret (the label changes nothing).
    fn same_node(&self, other: &Profile) -> bool {
        (&self.servers, &self.name, &self.secret) == (&other.servers, &other.name, &other.secret)
    }

    /// The number the login limits count this network's addresses under
    /// (its id; 0 is connections by address).
    fn network(&self) -> u64 {
        u64::from_str_radix(&self.id, 16).unwrap_or(1).max(1)
    }
}

/// The networks as the store keeps them, and whether to try direct paths:
/// one choice for every network, since it opens this computer to them.
#[derive(Clone, Default, Serialize, Deserialize)]
struct Stored {
    #[serde(default)]
    direct: bool,
    #[serde(default)]
    profiles: Vec<Profile>,
}

/// The one network 0.3.1 and earlier kept, as stored.
#[derive(Deserialize)]
struct OldSettings {
    #[serde(default)]
    servers: Vec<String>,
    #[serde(default)]
    name: String,
    #[serde(default)]
    secret: String,
    #[serde(default)]
    direct: bool,
}

/// A `network.set` request (an older GUI, `ctl network-set`): the first
/// network's settings. A missing secret keeps the stored one; no servers and
/// no name remove the network.
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

/// A `network.profile.set` request: without an id it adds a network (its
/// servers and name needed; joined unless it says otherwise); with one, what
/// it leaves out keeps its value, the secret included.
#[derive(Default, Deserialize)]
pub struct ProfileUpdate {
    #[serde(default)]
    id: Option<String>,
    #[serde(default)]
    label: Option<String>,
    #[serde(default)]
    servers: Option<Vec<String>>,
    #[serde(default)]
    name: Option<String>,
    #[serde(default)]
    secret: Option<String>,
    #[serde(default)]
    joined: Option<bool>,
}

/// A joined network's node, or why it could not start.
struct Node {
    /// The profile it is for.
    id: String,
    /// None when the node could not be made from the settings.
    node: Option<Arc<NativeCoreInstance>>,
    /// Starts the node, then accepts sessions; aborted when it stops.
    serving: Option<JoinHandle<()>>,
    /// Why it stopped, safe to show; None while it runs.
    failed: Arc<Mutex<Option<String>>>,
    /// The port it listens on for other devices, if it does.
    listen: Option<u16>,
}

/// What the status reads of a node: its network, the node, why it stopped
/// and the port it listens on.
struct NodeView {
    id: String,
    node: Option<Arc<NativeCoreInstance>>,
    failed: Option<String>,
    listen: Option<u16>,
}

/// The joined networks' nodes, shared with the connections, which dial
/// through the ones running when they dial (a resumed session too).
type Nodes = Arc<Mutex<Vec<Node>>>;

/// The running nodes and their runtime.
pub struct Network {
    rt: tokio::runtime::Runtime,
    ctx: Arc<Ctx>,
    device_id: String,
    stored: Stored,
    nodes: Nodes,
    /// The helper always tries direct paths (its installer allowed them).
    always_direct: bool,
    /// Accepts sessions by address while the host's user allows it.
    by_address: Option<JoinHandle<()>>,
    /// Why accepting by address could not listen, safe to show.
    by_address_error: Option<String>,
}

impl Network {
    /// Starts a node for every joined network of the stored settings (an
    /// older version's one network is moved into them first).
    /// Side effects: starts a tokio runtime; connects to the servers.
    /// Inputs: `always_direct`, set in the helper, whose installer allowed
    /// its own executable through the firewall.
    pub fn start(secrets: &Secrets, ctx: Arc<Ctx>, always_direct: bool) -> Result<Self, String> {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .thread_name("kyokai-net")
            .enable_all()
            .build()
            .map_err(|e| format!("network runtime: {e}"))?;
        let mut net = Network {
            rt,
            device_id: ctx.identity.device_id(),
            ctx,
            stored: load(secrets),
            nodes: Arc::default(),
            always_direct,
            by_address: None,
            by_address_error: None,
        };
        let joined: Vec<Profile> = net
            .stored
            .profiles
            .iter()
            .filter(|p| p.joined)
            .cloned()
            .collect();
        for p in &joined {
            net.start_node(p);
        }
        net.accept_by_address();
        Ok(net)
    }

    /// Whether the nodes try direct paths: the user's choice, or always in
    /// the helper.
    fn direct(&self) -> bool {
        self.stored.direct || self.always_direct
    }

    /// The first network's settings without the secret, for an older GUI
    /// and `ctl network` (`network.get`).
    pub fn settings(&self) -> Value {
        let first = self.stored.profiles.first();
        json!({
            "servers": first.map(|p| p.servers.clone()).unwrap_or_default(),
            "name": first.map(|p| p.name.as_str()).unwrap_or_default(),
            "has_secret": first.is_some_and(|p| !p.secret.is_empty()),
            "weak_secret": first.is_some_and(|p| is_weak(&p.secret)),
            "direct": self.direct(),
        })
    }

    /// Every network, without the secrets, and the direct choice
    /// (`network.profiles`).
    pub fn profiles(&self) -> Value {
        let profiles: Vec<Value> = self
            .stored
            .profiles
            .iter()
            .map(|p| {
                json!({
                    "id": p.id,
                    "label": p.label,
                    "servers": p.servers,
                    "name": p.name,
                    "has_secret": !p.secret.is_empty(),
                    "weak_secret": is_weak(&p.secret),
                    "joined": p.joined,
                })
            })
            .collect();
        json!({"direct": self.direct(), "profiles": profiles})
    }

    /// Sets the first network as an older GUI or `ctl network-set` does
    /// (`network.set`): no servers and no name remove it; otherwise it is
    /// joined, with the stored secret unless one is given.
    /// Side effects: rewrites the secrets store; reconnects.
    pub fn update(&mut self, secrets: &Secrets, update: Update) -> Result<(), String> {
        let mut stored = self.stored.clone();
        if let Some(direct) = update.direct {
            stored.direct = direct;
        }
        let servers = trimmed(update.servers.unwrap_or_default());
        let name = update.name.unwrap_or_default().trim().to_owned();
        if servers.is_empty() && name.is_empty() {
            if !stored.profiles.is_empty() {
                stored.profiles.remove(0);
            }
        } else {
            let first = stored.profiles.first().cloned();
            let p = Profile {
                id: first
                    .as_ref()
                    .map_or_else(|| new_id(&stored.profiles), |p| p.id.clone()),
                label: first.as_ref().map(|p| p.label.clone()).unwrap_or_default(),
                servers,
                name,
                secret: update
                    .secret
                    .unwrap_or_else(|| first.map(|p| p.secret).unwrap_or_default()),
                joined: true,
            };
            match stored.profiles.first_mut() {
                Some(f) => *f = p,
                None => stored.profiles.push(p),
            }
            check(&stored, 0)?;
        }
        self.apply(secrets, stored)
    }

    /// Adds a network or changes one (`network.profile.set`).
    /// Side effects: rewrites the secrets store; starts, restarts or stops
    /// that network's node.
    pub fn set_profile(&mut self, secrets: &Secrets, u: ProfileUpdate) -> Result<(), String> {
        let stored = patched(&self.stored, u)?;
        self.apply(secrets, stored)
    }

    /// Forgets a network, its secret too (`network.profile.remove`).
    /// Side effects: rewrites the secrets store; stops its node.
    pub fn remove_profile(&mut self, secrets: &Secrets, id: &str) -> Result<(), String> {
        let mut stored = self.stored.clone();
        let before = stored.profiles.len();
        stored.profiles.retain(|p| p.id != id);
        if stored.profiles.len() == before {
            return Err("no such network".into());
        }
        self.apply(secrets, stored)
    }

    /// Tries direct paths in every network, or not (`network.direct`).
    /// Side effects: rewrites the secrets store; restarts every node.
    pub fn set_direct(&mut self, secrets: &Secrets, direct: bool) -> Result<(), String> {
        let mut stored = self.stored.clone();
        stored.direct = direct;
        self.apply(secrets, stored)
    }

    /// Stores `stored` and brings the nodes in line with it: a network left,
    /// removed or changed stops, one joined or changed starts, and every one
    /// restarts when whether to try direct paths changed.
    fn apply(&mut self, secrets: &Secrets, stored: Stored) -> Result<(), String> {
        save(secrets, &stored)?;
        let old = std::mem::replace(&mut self.stored, stored);
        let restart_all = old.direct != self.stored.direct;
        let running: Vec<String> = self
            .nodes
            .lock()
            .map(|n| n.iter().map(|n| n.id.clone()).collect())
            .unwrap_or_default();
        let stop: Vec<String> = running
            .into_iter()
            .filter(|id| {
                let now = self
                    .stored
                    .profiles
                    .iter()
                    .find(|p| &p.id == id && p.joined);
                let before = old.profiles.iter().find(|p| &p.id == id);
                restart_all || !matches!((now, before), (Some(n), Some(b)) if n.same_node(b))
            })
            .collect();
        self.stop_nodes(&stop);
        let start: Vec<Profile> = self
            .stored
            .profiles
            .iter()
            .filter(|p| p.joined && !self.has_node(&p.id))
            .cloned()
            .collect();
        for p in &start {
            self.start_node(p);
        }
        Ok(())
    }

    /// Whether a network has a node (running, or failed to start).
    fn has_node(&self, id: &str) -> bool {
        self.nodes
            .lock()
            .is_ok_and(|n| n.iter().any(|n| n.id == id))
    }

    /// Every network's status, then all of them together (never a secret,
    /// never stale: read when asked).
    pub fn status(&self) -> Value {
        // What each node is, then their snapshots in one go.
        let nodes: Vec<NodeView> = self
            .nodes
            .lock()
            .map(|n| {
                n.iter()
                    .map(|n| NodeView {
                        id: n.id.clone(),
                        node: n.node.clone(),
                        failed: n.failed.lock().ok().and_then(|f| f.clone()),
                        listen: n.listen,
                    })
                    .collect()
            })
            .unwrap_or_default();
        let snapshots = self.rt.block_on(async {
            let mut out = Vec::new();
            for NodeView { node, .. } in &nodes {
                out.push(match node {
                    Some(n) => Some((
                        n.node_snapshot().await,
                        n.peer_snapshots().await,
                        n.route_snapshots().await,
                        n.peer_id(),
                    )),
                    None => None,
                });
            }
            out
        });
        let networks: Vec<Value> = self
            .stored
            .profiles
            .iter()
            .map(|p| {
                let mut entry = match nodes.iter().position(|n| n.id == p.id) {
                    _ if !p.joined => json!({"state": "off"}),
                    None => json!({"state": "off"}),
                    Some(i) => match (&nodes[i].failed, &snapshots[i]) {
                        (Some(error), _) => json!({"state": "error", "error": error}),
                        (None, Some((me, peers, routes, my_peer))) => {
                            let reached =
                                peers.iter().any(|p| p.conns.iter().any(|c| !c.is_closed));
                            let devices: Vec<Value> = routes
                                .iter()
                                .filter(|r| r.peer_id != *my_peer)
                                .filter_map(|r| {
                                    let id = r.hostname.strip_prefix("Kyokai-")?;
                                    let direct = peers.iter().any(|p| {
                                        p.peer_id == r.peer_id
                                            && !p.directly_connected_conns.is_empty()
                                    });
                                    Some(json!({
                                        "device_id": id,
                                        "path": if direct { "direct" } else { "relay" },
                                        "latency_ms": r.path_latency.max(0),
                                    }))
                                })
                                .collect();
                            json!({
                                "state": if reached { "online" } else { "connecting" },
                                "address": me.ipv4_addr.map(|a| a.address().to_string()),
                                "nat": nat_name(me.stun_info.udp_nat_type),
                                "listen_port": nodes[i].listen,
                                "devices": devices,
                            })
                        }
                        (None, None) => json!({"state": "connecting"}),
                    },
                };
                entry["id"] = json!(p.id);
                entry["label"] = json!(p.shown());
                entry["joined"] = json!(p.joined);
                entry
            })
            .collect();
        aggregate(networks)
    }

    /// Starts the node of a joined network, listening on a port no other
    /// node of this engine has when direct paths are tried.
    /// Side effects: connects to the network's servers.
    fn start_node(&mut self, p: &Profile) {
        // EasyTier builds its peer manager on the current Tokio runtime.
        let _rt = self.rt.enter();
        let direct = self.direct();
        let taken: Vec<u16> = self
            .nodes
            .lock()
            .map(|n| n.iter().filter_map(|n| n.listen).collect())
            .unwrap_or_default();
        let listen = if direct { free_port(&taken) } else { None };
        let failed = Arc::new(Mutex::new(None));
        let mut node = Node {
            id: p.id.clone(),
            node: None,
            serving: None,
            failed: failed.clone(),
            listen,
        };
        match config(p, &self.device_id, direct, listen).and_then(create_native_instance) {
            Ok(instance) => {
                node.serving = Some(self.rt.spawn(serve_node(
                    instance.clone(),
                    self.ctx.clone(),
                    p.network(),
                    failed,
                )));
                node.node = Some(instance);
            }
            Err(e) => {
                if let Ok(mut f) = failed.lock() {
                    *f = Some(format!("network settings: {e}"));
                }
                node.listen = None;
            }
        }
        if let Ok(mut n) = self.nodes.lock() {
            n.push(node);
        }
    }

    /// Stops networks' nodes and forgets them, together.
    fn stop_nodes(&mut self, ids: &[String]) {
        let stopping: Vec<Node> = match self.nodes.lock() {
            Ok(mut n) => {
                let (gone, kept) = std::mem::take(&mut *n)
                    .into_iter()
                    .partition(|n| ids.contains(&n.id));
                *n = kept;
                gone
            }
            Err(_) => return,
        };
        let mut instances = Vec::new();
        for n in stopping {
            if let Some(serving) = n.serving {
                serving.abort();
            }
            instances.extend(n.node);
        }
        if instances.is_empty() {
            return;
        }
        let _ = self.rt.block_on(async {
            tokio::time::timeout(STOP_TIMEOUT, async {
                for node in &instances {
                    node.stop().await;
                }
            })
            .await
        });
    }

    /// Starts a session with the device that announces `device_id` in any
    /// joined network, or with the one at `address` (an IP address or domain
    /// name, with or without a port), which must prove to be that device.
    /// Returns: the session's id at once; its progress shows in the status.
    /// Notes: every route that announces the Device ID is a candidate (up to
    /// MAX_CANDIDATES in each network), direct paths first; the handshake
    /// decides which one is the device. A session by address needs no
    /// network.
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
            None if running(&self.nodes).is_empty() => {
                return Err("not connected to a network".into());
            }
            None => Route::Network(self.nodes.clone()),
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
                    // The networks joined now: a resumed session may go
                    // through another one.
                    Route::Network(nodes) => dial(&running(nodes), &ctx, id, &stop, &device_id, &password, &want, resume_with).await,
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
                    Source {
                        network: 0,
                        ip: from.ip().to_canonical(),
                    },
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
}

impl Drop for Network {
    fn drop(&mut self) {
        if let Some(task) = self.by_address.take() {
            task.abort();
        }
        let all: Vec<String> = self
            .nodes
            .lock()
            .map(|n| n.iter().map(|n| n.id.clone()).collect())
            .unwrap_or_default();
        self.stop_nodes(&all);
    }
}

/// Starts a network's node, then accepts sessions on its data plane,
/// counted for the login limits under `network`.
/// Side effects: records why the node could not start in `failed`.
async fn serve_node(
    node: Arc<NativeCoreInstance>,
    ctx: Arc<Ctx>,
    network: u64,
    failed: Arc<Mutex<Option<String>>>,
) {
    if let Err(e) = node.start().await {
        if let Ok(mut f) = failed.lock() {
            *f = Some(format!("{e:#}"));
        }
        return;
    }
    // The data plane binds only once the node has its virtual address, and
    // a listener stays on the address it bound: when DHCP gives the node
    // another one, bind again.
    loop {
        let Some(address) = node.node_snapshot().await.ipv4_addr else {
            tokio::time::sleep(BIND_RETRY).await;
            continue;
        };
        let mut listener = match node.data_plane_tcp_bind(KYOKAI_PORT, DIAL_TIMEOUT).await {
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
                        tokio::spawn(session::serve(ctx.clone(), stream, Source { network, ip: from.ip() }));
                    }
                    Err(_) => break,
                },
                _ = check.tick() => {
                    if node.node_snapshot().await.ipv4_addr != Some(address) {
                        break;
                    }
                }
            }
        }
    }
}

/// The nodes running now (not those that failed to start).
fn running(nodes: &Nodes) -> Vec<Arc<NativeCoreInstance>> {
    nodes
        .lock()
        .map(|n| n.iter().filter_map(|n| n.node.clone()).collect())
        .unwrap_or_default()
}

/// The stored networks: the entry, or the one network an older version kept,
/// moved into it (written, then the old entry removed).
fn load(secrets: &Secrets) -> Stored {
    if let Some(s) = secrets
        .get(SECRET_NAME)
        .and_then(|s| serde_json::from_str::<Stored>(&s).ok())
    {
        return s;
    }
    let Some(old) = secrets
        .get(OLD_SECRET_NAME)
        .and_then(|s| serde_json::from_str::<OldSettings>(&s).ok())
    else {
        return Stored::default();
    };
    let stored = migrate(old);
    if save(secrets, &stored).is_ok() {
        let _ = secrets.remove(OLD_SECRET_NAME);
    }
    stored
}

/// An older version's one network as the first, joined; one with no server
/// or no name (0.2.0 took other devices' addresses then) never joined
/// anything and is left out.
fn migrate(old: OldSettings) -> Stored {
    let profiles = if old.servers.is_empty() || old.name.is_empty() {
        Vec::new()
    } else {
        vec![Profile {
            id: new_id(&[]),
            label: String::new(),
            servers: old.servers,
            name: old.name,
            secret: old.secret,
            joined: true,
        }]
    };
    Stored {
        direct: old.direct,
        profiles,
    }
}

/// Writes the networks into the secrets store.
fn save(secrets: &Secrets, stored: &Stored) -> Result<(), String> {
    let text = zeroize::Zeroizing::new(serde_json::to_string(stored).map_err(|e| e.to_string())?);
    secrets.set(SECRET_NAME, &text)
}

/// A new network id: 16 hex digits of a random number, never zero and none
/// of `taken`'s.
fn new_id(taken: &[Profile]) -> String {
    loop {
        let mut b = [0u8; 8];
        let _ = getrandom::fill(&mut b);
        let n = u64::from_le_bytes(b);
        let id = format!("{n:016x}");
        if n != 0 && !taken.iter().any(|p| p.id == id) {
            return id;
        }
    }
}

/// The networks with one added or changed as `u` says: a new one is joined
/// unless it says otherwise; a change keeps what it leaves out, the secret
/// included.
/// Returns: why not, when there would be too many networks, `u` names none,
/// or the result fails `check`.
fn patched(stored: &Stored, u: ProfileUpdate) -> Result<Stored, String> {
    let mut stored = stored.clone();
    let i = match u.id.as_deref() {
        None => {
            if stored.profiles.len() >= MAX_PROFILES {
                return Err(format!("at most {MAX_PROFILES} networks"));
            }
            stored.profiles.push(Profile {
                id: new_id(&stored.profiles),
                label: u.label.unwrap_or_default().trim().to_owned(),
                servers: trimmed(u.servers.unwrap_or_default()),
                name: u.name.unwrap_or_default().trim().to_owned(),
                secret: u.secret.unwrap_or_default(),
                joined: u.joined.unwrap_or(true),
            });
            stored.profiles.len() - 1
        }
        Some(id) => {
            let i = stored
                .profiles
                .iter()
                .position(|p| p.id == id)
                .ok_or("no such network")?;
            let p = &mut stored.profiles[i];
            if let Some(label) = u.label {
                p.label = label.trim().to_owned();
            }
            if let Some(servers) = u.servers {
                p.servers = trimmed(servers);
            }
            if let Some(name) = u.name {
                p.name = name.trim().to_owned();
            }
            if let Some(secret) = u.secret {
                p.secret = secret;
            }
            if let Some(joined) = u.joined {
                p.joined = joined;
            }
            i
        }
    };
    check(&stored, i)?;
    Ok(stored)
}

/// Server URLs as typed, trimmed.
fn trimmed(servers: Vec<String>) -> Vec<String> {
    servers.iter().map(|s| s.trim().to_owned()).collect()
}

/// Checks the network at `i` of `stored`: its settings, and that it is not
/// joined twice (another joined network with its name and secret is the
/// same network).
fn check(stored: &Stored, i: usize) -> Result<(), String> {
    let p = &stored.profiles[i];
    validate(p)?;
    if p.joined
        && let Some(other) = stored
            .profiles
            .iter()
            .find(|o| o.id != p.id && o.joined && (&o.name, &o.secret) == (&p.name, &p.secret))
    {
        return Err(format!(
            "this network is joined already, as {}",
            other.shown()
        ));
    }
    Ok(())
}

/// All networks' status together, as an older GUI, Home and the viewer
/// read it, with each network's under `networks`: online when any is,
/// connecting when any is, an error when every joined one failed, off when
/// none is joined; the address, NAT and port of the first online one; each
/// device once, by its best path (direct first, then the lowest latency).
fn aggregate(networks: Vec<Value>) -> Value {
    let state = |s: &str| networks.iter().find(|n| n["state"] == s);
    let mut all = match (state("online"), state("connecting"), state("error")) {
        (Some(n), _, _) => json!({
            "state": "online",
            "address": n["address"],
            "nat": n["nat"],
            "listen_port": n["listen_port"],
        }),
        (None, Some(_), _) => json!({"state": "connecting"}),
        (None, None, Some(n)) => json!({"state": "error", "error": n["error"]}),
        (None, None, None) => json!({"state": "off"}),
    };
    let mut devices: Vec<Value> = Vec::new();
    for d in networks
        .iter()
        .filter_map(|n| n["devices"].as_array())
        .flatten()
    {
        let rank = |d: &Value| {
            (
                d["path"] != "direct",
                d["latency_ms"].as_i64().unwrap_or(i64::MAX),
            )
        };
        match devices
            .iter_mut()
            .find(|o| o["device_id"] == d["device_id"])
        {
            Some(o) if rank(d) < rank(o) => *o = d.clone(),
            Some(_) => {}
            None => devices.push(d.clone()),
        }
    }
    all["devices"] = json!(devices);
    all["networks"] = json!(networks);
    all
}

/// How a controller reaches a host.
enum Route {
    /// The joined networks, whose routes announce its Device ID.
    Network(Nodes),
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

/// A route to a device in one of the joined networks: the node it goes
/// through (an index into the nodes dialed), its virtual address, whether
/// the path is direct, and its latency.
#[derive(Clone, Copy, Debug, PartialEq)]
struct Candidate {
    node: usize,
    ip: Ipv4Addr,
    direct: bool,
    latency: i32,
}

/// Puts the candidates in the order they are tried: direct paths first, then
/// the lowest latency; the networks' own order otherwise.
fn order(candidates: &mut [Candidate]) {
    candidates.sort_by_key(|c| (!c.direct, c.latency.max(0)));
}

/// Tries the routes that announce `device_id` in every joined network (at
/// most MAX_CANDIDATES in each), in `order`, and runs the session on the
/// first that proves to be the device.
#[allow(clippy::too_many_arguments)]
async fn dial(
    nodes: &[Arc<NativeCoreInstance>],
    ctx: &Arc<Ctx>,
    id: u64,
    stop: &Arc<tokio::sync::Notify>,
    device_id: &str,
    password: &zeroize::Zeroizing<String>,
    want: &[Permission],
    resume: Option<zeroize::Zeroizing<String>>,
) -> Dialed {
    let hostname = format!("Kyokai-{device_id}");
    let mut candidates = Vec::new();
    for (n, node) in nodes.iter().enumerate() {
        let peers = node.peer_snapshots().await;
        candidates.extend(
            node.route_snapshots()
                .await
                .into_iter()
                .filter(|r| r.hostname == hostname)
                .filter_map(|r| {
                    let ip = r.ipv4_addr.and_then(|a| a.address).map(Ipv4Addr::from)?;
                    let direct = peers
                        .iter()
                        .any(|p| p.peer_id == r.peer_id && !p.directly_connected_conns.is_empty());
                    Some(Candidate {
                        node: n,
                        ip,
                        direct,
                        latency: r.path_latency,
                    })
                })
                .take(MAX_CANDIDATES),
        );
    }
    order(&mut candidates);
    let mut failure = session::Failure::Unreachable;
    for c in candidates {
        let addr = SocketAddr::new(c.ip.into(), KYOKAI_PORT);
        let Ok(stream) = nodes[c.node]
            .data_plane_tcp_connect(addr, DIAL_TIMEOUT)
            .await
        else {
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

/// A port of LISTEN_PORTS free for both TCP and UDP on every IPv4 address
/// and none of `taken` (this engine's other nodes, which may not have bound
/// theirs yet), or None: EasyTier refuses to start when a listener it was
/// given cannot bind, so a node runs without one when every port is taken.
fn free_port(taken: &[u16]) -> Option<u16> {
    LISTEN_PORTS.into_iter().find(|port| {
        !taken.contains(port)
            && std::net::TcpListener::bind((Ipv4Addr::UNSPECIFIED, *port)).is_ok()
            && std::net::UdpSocket::bind((Ipv4Addr::UNSPECIFIED, *port)).is_ok()
    })
}

/// Builds a network's node configuration: this device's instance id and
/// hostname (the same in every network), no TUN, DHCP, data plane
/// whitelists that admit only Kyokai's port. With `direct`, hole punching, UPnP and direct
/// connections, preferring UDP, and listeners on `listen` (TCP and UDP) so
/// that devices on the same network connect directly; without it nothing
/// listens or waits for unsolicited packets, but the node still dials a
/// device that listens (TCP first, an outgoing connection no firewall asks
/// about), so a device with direct connections on reaches it directly too.
fn config(
    s: &Profile,
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
instance_name = "kyokai-{profile}"
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
        profile = s.id,
        name = q(&s.name),
        secret = q(&s.secret),
    );
    TomlConfig::new_from_str(&toml)
}

/// Checks a network's settings at the local API boundary.
fn validate(s: &Profile) -> Result<(), String> {
    if s.servers.is_empty() || s.name.is_empty() {
        return Err("a network needs a server and a network name".into());
    }
    if s.label.chars().count() > MAX_LABEL || s.label.chars().any(char::is_control) {
        return Err("the network's name here is too long or has control characters".into());
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

    fn profile(servers: &[&str], name: &str, secret: &str) -> Profile {
        Profile {
            id: "00000000000000a1".into(),
            label: String::new(),
            servers: servers.iter().map(|s| s.to_string()).collect(),
            name: name.into(),
            secret: secret.into(),
            joined: true,
        }
    }

    #[test]
    fn validation() {
        let labelled = |label: &str| Profile {
            label: label.into(),
            ..profile(&["tcp://a:1"], "n", "")
        };
        let cases = [
            (profile(&["tcp://example.org:11010"], "kyokai", "s"), true),
            (profile(&["wss://example.org/et"], "kyokai", ""), true),
            // A network needs both now: leaving it is a switch, removing it
            // a request of its own.
            (profile(&[], "", ""), false),
            (profile(&["tcp://example.org:11010"], "", ""), false),
            (profile(&[], "kyokai", ""), false),
            (profile(&["faketcp://example.org:1"], "n", ""), false),
            (profile(&["example.org:11010"], "n", ""), false),
            (profile(&["tcp://"], "n", ""), false),
            (profile(&["tcp://a b"], "n", ""), false),
            (profile(&["tcp://a"], "n\n", ""), false),
            (profile(&["tcp://a"; 9], "n", ""), false),
            (labelled("Home"), true),
            (labelled(&"x".repeat(65)), false),
            (labelled("a\tb"), false),
        ];
        for (p, ok) in cases {
            assert_eq!(
                validate(&p).is_ok(),
                ok,
                "{:?} {:?} {:?}",
                p.servers,
                p.name,
                p.label
            );
        }
    }

    /// An older version's one network becomes the first, joined, with its
    /// direct choice; one without a server never joined anything.
    #[test]
    fn the_old_network_is_moved_in() {
        let old: OldSettings = serde_json::from_str(
            r#"{"servers": ["tcp://a:1"], "name": "home", "secret": "s3", "direct": true}"#,
        )
        .unwrap();
        let s = migrate(old);
        assert!(s.direct);
        let [p] = &s.profiles[..] else {
            panic!("{} networks", s.profiles.len());
        };
        assert_eq!(
            (&p.servers[..], &p.name[..], &p.secret[..], p.joined),
            (&["tcp://a:1".to_owned()][..], "home", "s3", true)
        );
        assert_eq!(p.id.len(), 16);
        let none: OldSettings = serde_json::from_str(r#"{"name": "old", "secret": "s"}"#).unwrap();
        assert!(migrate(none).profiles.is_empty());
    }

    /// Adding joins unless told otherwise; a change keeps what it leaves
    /// out, the secret included; the same network is never joined twice;
    /// there are at most MAX_PROFILES.
    #[test]
    fn networks_are_added_and_changed() {
        let add = |stored: &Stored, name: &str, secret: &str| {
            patched(
                stored,
                ProfileUpdate {
                    servers: Some(vec![" tcp://a:1 ".into()]),
                    name: Some(name.into()),
                    secret: Some(secret.into()),
                    ..Default::default()
                },
            )
        };
        let one = add(&Stored::default(), "home", "s3cret").unwrap();
        let home = one.profiles[0].clone();
        assert!(home.joined);
        assert_eq!(home.servers, ["tcp://a:1"]);
        // A new label only: the secret and the rest stay.
        let renamed = patched(
            &one,
            ProfileUpdate {
                id: Some(home.id.clone()),
                label: Some(" Home ".into()),
                ..Default::default()
            },
        )
        .unwrap();
        let p = &renamed.profiles[0];
        assert_eq!(
            (&p.label[..], &p.secret[..], p.joined),
            ("Home", "s3cret", true)
        );
        assert!(p.same_node(&home), "a new label restarts nothing");
        // The same network again, joined: refused; left, it may be kept.
        let err = add(&renamed, "home", "s3cret").err().unwrap();
        assert!(err.contains("Home"), "{err}");
        let left = patched(
            &renamed,
            ProfileUpdate {
                servers: Some(vec!["tcp://b:2".into()]),
                name: Some("home".into()),
                secret: Some("s3cret".into()),
                joined: Some(false),
                ..Default::default()
            },
        )
        .unwrap();
        assert_eq!(left.profiles.len(), 2);
        assert_ne!(left.profiles[0].id, left.profiles[1].id);
        // No such network; too many.
        assert!(
            patched(
                &left,
                ProfileUpdate {
                    id: Some("nope".into()),
                    ..Default::default()
                }
            )
            .is_err()
        );
        let mut full = Stored::default();
        for i in 0..MAX_PROFILES {
            full = add(&full, &format!("n{i}"), "s").unwrap();
        }
        assert!(add(&full, "one more", "s").is_err());
    }

    /// Every network's node is this device: the same instance id and
    /// hostname, each with an instance name of its own.
    #[test]
    fn every_network_announces_this_device() {
        use easytier_core::config::toml::ConfigLoader;
        let a = profile(&["tcp://a:1"], "home", "s");
        let b = Profile {
            id: "00000000000000b2".into(),
            ..profile(&["tcp://b:1"], "office", "t")
        };
        let (ca, cb) = (
            config(&a, "100000002", false, None).unwrap(),
            config(&b, "100000002", false, None).unwrap(),
        );
        assert_eq!(ca.get_id(), cb.get_id());
        assert_eq!(ca.get_id(), instance_id("100000002"));
        assert_eq!(
            (ca.get_hostname(), cb.get_hostname()),
            ("Kyokai-100000002".into(), "Kyokai-100000002".into())
        );
        assert_ne!(ca.get_inst_name(), cb.get_inst_name());
    }

    /// Direct paths first, then the lowest latency; the networks' order
    /// for the rest.
    #[test]
    fn candidates_go_direct_first_then_by_latency() {
        let c = |node, last, direct, latency| Candidate {
            node,
            ip: Ipv4Addr::new(10, 126, 126, last),
            direct,
            latency,
        };
        let mut list = [
            c(0, 1, false, 20),
            c(0, 2, true, 90),
            c(1, 1, false, 5),
            c(1, 2, true, 30),
            c(2, 1, false, 5),
        ];
        order(&mut list);
        assert_eq!(
            list.map(|c| (c.node, c.ip.octets()[3])),
            [(1, 2), (0, 2), (1, 1), (2, 1), (0, 1)]
        );
    }

    /// All networks together: online when one is, the best path to each
    /// device once; an error only when every joined one failed.
    #[test]
    fn networks_add_up() {
        let device = |id: &str, path: &str, ms: i64| json!({"device_id": id, "path": path, "latency_ms": ms});
        let all = aggregate(vec![
            json!({"id": "a", "state": "connecting"}),
            json!({"id": "b", "state": "online", "address": "10.126.126.2", "nat": "open", "listen_port": 47312,
                   "devices": [device("482916735", "relay", 40), device("123456789", "relay", 80)]}),
            json!({"id": "c", "state": "online", "address": "10.0.0.5", "nat": "unknown",
                   "devices": [device("482916735", "direct", 70), device("123456789", "relay", 20)]}),
        ]);
        assert_eq!(
            (&all["state"], &all["address"], &all["listen_port"]),
            (&json!("online"), &json!("10.126.126.2"), &json!(47312))
        );
        assert_eq!(
            all["devices"],
            json!([
                device("482916735", "direct", 70),
                device("123456789", "relay", 20)
            ])
        );
        assert_eq!(all["networks"].as_array().map(Vec::len), Some(3));
        let failed = aggregate(vec![
            json!({"state": "error", "error": "network settings: no"}),
            json!({"state": "off"}),
        ]);
        assert_eq!(
            (&failed["state"], &failed["error"]),
            (&json!("error"), &json!("network settings: no"))
        );
        assert_eq!(
            aggregate(vec![
                json!({"state": "error"}),
                json!({"state": "connecting"})
            ])["state"],
            "connecting"
        );
        assert_eq!(aggregate(Vec::new())["state"], "off");
        assert_eq!(aggregate(Vec::new())["devices"], json!([]));
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
        let s = profile(&["tcp://a:1"], name, "p\"w\\");
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
        let s = profile(&["tcp://a:1"], "n", "p");
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

    /// The port check finds a port of the range, and skips one taken by
    /// another program or by another of this engine's nodes.
    #[test]
    fn a_taken_listen_port_is_skipped() {
        let Some(first) = free_port(&[]) else {
            return eprintln!("skipped: no port of the range is free");
        };
        let next = free_port(&[first]);
        assert_ne!(next, Some(first), "a port another node has");
        let _held = std::net::UdpSocket::bind((Ipv4Addr::UNSPECIFIED, first)).unwrap();
        let after = free_port(&[]);
        assert_ne!(after, Some(first), "a port another program holds");
        assert!(after.is_none_or(|p| LISTEN_PORTS.contains(&p)));
    }

    #[test]
    fn instance_id_is_stable() {
        assert_eq!(
            instance_id("100000002").to_string(),
            "d409b924-1be1-5f9e-b719-f4bdaa8608b1"
        );
    }
}
