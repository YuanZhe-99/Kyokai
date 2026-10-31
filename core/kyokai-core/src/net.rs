//! The embedded EasyTier nodes: one for each network the user joined, each
//! through standard EasyTier servers, without TUN, every one announcing this
//! device's Device ID (doc/en-us/networking.md).
//!
//! EasyTier is an untrusted pipe: its routes, instance ids and hostnames are
//! hints for finding a device, never proof of who it is.

use crate::access::{Permission, Source};
use crate::external::{self, External};
use crate::session::{self, Ctx};
use crate::store::Secrets;
use easytier::instance::factory::{NativeCoreInstance, create_native_instance};
use easytier::proto::common::NatType;
use easytier_core::config::toml::TomlConfig;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
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

/// The number the login limits count sessions through an official core
/// under: every one comes from loopback, whichever network it crossed.
const EXTERNAL_NETWORK: u64 = u64::MAX;
/// The worker threads of the runtime the nodes and sessions share: half the
/// logical processors, at least 2 and at most 4.
fn net_workers() -> usize {
    let n = std::thread::available_parallelism().map_or(2, |n| n.get());
    (n / 2).clamp(2, 4)
}

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
    /// The official core's own options for this network, as TOML (none of
    /// `external::OWNED`); used only while EasyTier is chosen.
    #[serde(default)]
    pub toml: String,
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
        (&self.servers, &self.name, &self.secret, &self.toml)
            == (&other.servers, &other.name, &other.secret, &other.toml)
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
    /// Prefer the lowest-latency route over the fewest hops (EasyTier's
    /// `latency_first`), in every network; absent in older settings: off.
    #[serde(default)]
    latency_first: bool,
    #[serde(default)]
    profiles: Vec<Profile>,
    /// The official EasyTier core every network runs on, by its tag (a
    /// folder of `external::CORES_DIR`); None (as before 0.6.6) runs the
    /// embedded node: Kyokai Network.
    #[serde(default)]
    easytier: Option<String>,
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
    /// The official core's options as TOML, replacing them.
    #[serde(default)]
    toml: Option<String>,
}

/// A joined network's node, or why it could not start.
struct Node {
    /// The profile it is for.
    id: String,
    /// None when the node could not be made from the settings.
    node: Option<Core>,
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
    node: Option<Core>,
    failed: Option<String>,
    listen: Option<u16>,
}

/// The joined networks' nodes, shared with the connections, which dial
/// through the ones running when they dial (a resumed session too).
type Nodes = Arc<Mutex<Vec<Node>>>;

/// A network's node: the embedded one, or an official core run as a
/// process of its own (`external`), set once it has started.
#[derive(Clone)]
enum Core {
    Native(Arc<NativeCoreInstance>),
    External(Arc<std::sync::OnceLock<External>>),
}

/// What a node knows of its network now: its own address and NAT, whether
/// it reaches anyone, and every other node by hostname with its address,
/// path and latency.
struct Seen {
    address: Option<Ipv4Addr>,
    nat: &'static str,
    reached: bool,
    peers: Vec<SeenPeer>,
}

/// Another node of the network as `Seen` lists it.
struct SeenPeer {
    hostname: String,
    ip: Option<Ipv4Addr>,
    direct: bool,
    latency: i32,
}

impl Core {
    /// What the node knows of its network; None while an official core
    /// starts or does not answer.
    async fn seen(&self) -> Option<Seen> {
        match self {
            Core::Native(n) => {
                let (me, peers, routes, my_peer) = (
                    n.node_snapshot().await,
                    n.peer_snapshots().await,
                    n.route_snapshots().await,
                    n.peer_id(),
                );
                let reached = peers.iter().any(|p| p.conns.iter().any(|c| !c.is_closed));
                let others = routes
                    .into_iter()
                    .filter(|r| r.peer_id != my_peer)
                    .map(|r| SeenPeer {
                        direct: peers.iter().any(|p| {
                            p.peer_id == r.peer_id && !p.directly_connected_conns.is_empty()
                        }),
                        ip: r.ipv4_addr.and_then(|a| a.address).map(Ipv4Addr::from),
                        hostname: r.hostname,
                        latency: r.path_latency,
                    })
                    .collect();
                Some(Seen {
                    address: me.ipv4_addr.map(|a| a.address()),
                    nat: nat_name(me.stun_info.udp_nat_type),
                    reached,
                    peers: others,
                })
            }
            Core::External(slot) => {
                let rows = slot.get()?.peers().await?;
                let me = rows.iter().find(|r| r.local());
                Some(Seen {
                    address: me.and_then(|r| r.ip()),
                    nat: nat_name(
                        me.and_then(|r| NatType::from_str_name(&r.nat_type))
                            .unwrap_or(NatType::Unknown) as i32,
                    ),
                    reached: rows.iter().any(|r| !r.local()),
                    peers: rows
                        .iter()
                        .filter(|r| !r.local())
                        .map(|r| SeenPeer {
                            hostname: r.hostname.clone(),
                            ip: r.ip(),
                            direct: r.direct(),
                            latency: r.latency(),
                        })
                        .collect(),
                })
            }
        }
    }

    /// Opens a connection to Kyokai's port at `ip` in the network.
    async fn connect(&self, ip: Ipv4Addr) -> Option<Box<dyn Io>> {
        match self {
            Core::Native(n) => {
                let addr = SocketAddr::new(ip.into(), KYOKAI_PORT);
                let stream = n.data_plane_tcp_connect(addr, DIAL_TIMEOUT).await.ok()?;
                Some(Box::new(stream))
            }
            Core::External(slot) => {
                let stream = slot.get()?.connect(ip, KYOKAI_PORT, DIAL_TIMEOUT).await?;
                Some(Box::new(stream))
            }
        }
    }

    /// Stops the node.
    async fn stop(&self) {
        match self {
            Core::Native(n) => n.stop().await,
            Core::External(slot) => {
                if let Some(e) = slot.get() {
                    e.stop().await;
                }
            }
        }
    }
}

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
    /// The latest looks at known hosts' addresses (memory only).
    probes: Arc<Probes>,
    /// The names the devices of the networks show before a login (memory
    /// only).
    names: Arc<Names>,
    /// Where the official cores and their runtime files are.
    state_dir: PathBuf,
    /// Accepts the official cores' connections on loopback while one runs
    /// and accepting by address (which takes them then) is off.
    local: Option<JoinHandle<()>>,
    /// Why the cores' connections cannot be accepted (the port taken, by
    /// another program or another Kyokai): other devices cannot reach this
    /// one, while it still reaches them. Safe to show.
    local_error: Option<String>,
}

impl Network {
    /// Starts a node for every joined network of the stored settings (an
    /// older version's one network is moved into them first).
    /// Side effects: starts a tokio runtime; connects to the servers.
    /// Inputs: `always_direct`, set in the helper, whose installer allowed
    /// its own executable through the firewall.
    pub fn start(
        secrets: &Secrets,
        ctx: Arc<Ctx>,
        always_direct: bool,
        state_dir: &Path,
    ) -> Result<Self, String> {
        // A core that ended with Kyokai may have left its runtime file.
        external::clean(state_dir);
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(net_workers())
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
            probes: Arc::default(),
            names: Arc::default(),
            state_dir: state_dir.to_owned(),
            local: None,
            local_error: None,
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
                    "toml": p.toml,
                    // What switching back to Kyokai Network would remove.
                    "options": external::parse(&p.toml).map(|d| external::keys(&d)).unwrap_or_default(),
                })
            })
            .collect();
        json!({
            "direct": self.direct(),
            "latency_first": self.stored.latency_first,
            "easytier": self.stored.easytier,
            "profiles": profiles,
        })
    }

    /// Runs every network on an official EasyTier core (its tag, a folder
    /// of `external::CORES_DIR`) or on the embedded node (None):
    /// `network.backend`. Back on the embedded node, the networks' own
    /// options are removed (the GUI warned about any first).
    /// Side effects: rewrites the secrets store; restarts every node.
    pub fn set_backend(
        &mut self,
        secrets: &Secrets,
        easytier: Option<String>,
    ) -> Result<(), String> {
        if let Some(tag) = &easytier
            && external::programs(&self.state_dir, tag).is_none()
        {
            return Err(format!("EasyTier {tag} is not downloaded"));
        }
        let mut stored = self.stored.clone();
        if easytier.is_none() {
            for p in &mut stored.profiles {
                p.toml.clear();
            }
        }
        stored.easytier = easytier;
        self.apply(secrets, stored)?;
        // The by-address listener hands loopback to the cores or not.
        self.accept_by_address();
        Ok(())
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
                toml: first.as_ref().map(|p| p.toml.clone()).unwrap_or_default(),
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

    /// Prefers the lowest-latency route in every network, or the fewest
    /// hops (EasyTier's default).
    /// Side effects: rewrites the secrets store; restarts every node.
    pub fn set_latency_first(&mut self, secrets: &Secrets, on: bool) -> Result<(), String> {
        let mut stored = self.stored.clone();
        stored.latency_first = on;
        self.apply(secrets, stored)
    }

    /// Stores `stored` and brings the nodes in line with it: a network left,
    /// removed or changed stops, one joined or changed starts, and every one
    /// restarts when whether to try direct paths changed.
    fn apply(&mut self, secrets: &Secrets, stored: Stored) -> Result<(), String> {
        save(secrets, &stored)?;
        let old = std::mem::replace(&mut self.stored, stored);
        let restart_all = old.direct != self.stored.direct
            || old.latency_first != self.stored.latency_first
            || old.easytier != self.stored.easytier;
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
        self.accept_local();
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
                    Some(n) => n.seen().await,
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
                        (None, Some(seen)) => {
                            let devices: Vec<Value> = seen
                                .peers
                                .iter()
                                .filter_map(|r| {
                                    let id = r.hostname.strip_prefix("Kyokai-")?;
                                    let mut d = json!({
                                        "device_id": id,
                                        "path": if r.direct { "direct" } else { "relay" },
                                        "latency_ms": r.latency.max(0),
                                    });
                                    if let Some(name) = self.names.of(id) {
                                        d["name"] = json!(name);
                                    }
                                    Some(d)
                                })
                                .collect();
                            json!({
                                "state": if seen.reached { "online" } else { "connecting" },
                                "address": seen.address.map(|a| a.to_string()),
                                "nat": seen.nat,
                                "listen_port": nodes[i].listen,
                                "devices": devices,
                            })
                        }
                        (None, None) => json!({"state": "connecting"}),
                    },
                };
                // Reaching others still works without the loopback port.
                if entry["state"] != "error"
                    && entry["state"] != "off"
                    && let Some(e) = &self.local_error
                {
                    entry["unreachable"] = json!(e);
                }
                entry["id"] = json!(p.id);
                entry["label"] = json!(p.shown());
                entry["joined"] = json!(p.joined);
                entry
            })
            .collect();
        // The names the devices show, looked at again now and then.
        let ids: Vec<String> = snapshots
            .iter()
            .flatten()
            .flat_map(|s| &s.peers)
            .filter_map(|r| r.hostname.strip_prefix("Kyokai-"))
            .filter(|id| *id != self.device_id)
            .map(str::to_owned)
            .collect();
        for id in self.names.due(&ids, Instant::now()) {
            let (names, nodes) = (self.names.clone(), running(&self.nodes));
            self.rt.spawn(async move {
                let seen = look_name(nodes, &id).await;
                names.found(&id, seen);
            });
        }
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
        if let Some(tag) = self.stored.easytier.clone() {
            let slot = Arc::new(std::sync::OnceLock::new());
            let base = zeroize::Zeroizing::new(config_text(
                p,
                &self.device_id,
                direct,
                self.stored.latency_first,
                listen,
            ));
            node.serving = Some(self.rt.spawn(serve_external(
                slot.clone(),
                self.state_dir.clone(),
                tag,
                p.clone(),
                base,
                failed,
            )));
            node.node = Some(Core::External(slot));
            if let Ok(mut n) = self.nodes.lock() {
                n.push(node);
            }
            return;
        }
        match config(
            p,
            &self.device_id,
            direct,
            self.stored.latency_first,
            listen,
        )
        .and_then(create_native_instance)
        {
            Ok(instance) => {
                node.serving = Some(self.rt.spawn(serve_node(
                    instance.clone(),
                    self.ctx.clone(),
                    p.network(),
                    failed,
                )));
                node.node = Some(Core::Native(instance));
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
            // An official core stopped as it started may have left its
            // runtime file.
            let _ = std::fs::remove_file(external::run_file(&self.state_dir, &n.id));
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
    /// Notes: see `Dialer::start`.
    pub fn connect(&self, ask: Ask) -> Result<u64, String> {
        let (id, work) = self.dialer().start(ask)?;
        self.rt.spawn(work);
        Ok(id)
    }

    /// Runs work of the core's own on the network's runtime (this
    /// computer's own NanoKVM session, `session::local_kvm`).
    pub fn spawn(&self, work: impl Future<Output = ()> + Send + 'static) {
        self.rt.spawn(work);
    }

    /// What dials devices for the core's own connections (status watches).
    pub fn dialer(&self) -> Dialer {
        Dialer {
            nodes: self.nodes.clone(),
            ctx: self.ctx.clone(),
            probes: self.probes.clone(),
        }
    }

    /// The latest looks at a known host's addresses.
    pub fn probes(&self, device_id: &str) -> Vec<Probe> {
        self.probes.of(device_id)
    }

    /// Looks at the addresses of these known hosts in the background (Home
    /// asks while it shows them).
    pub fn probe(&self, devices: Vec<String>) {
        for device in devices {
            let dialer = self.dialer();
            self.rt.spawn(async move {
                dialer.probe(&device).await;
            });
        }
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
            self.accept_local();
            return;
        }
        let _rt = self.rt.enter();
        let v4 = listen_tcp(IpAddr::V4(Ipv4Addr::UNSPECIFIED), KYOKAI_PORT);
        // Without IPv6 on this computer, IPv4 alone.
        let v6 = listen_tcp(IpAddr::V6(Ipv6Addr::UNSPECIFIED), KYOKAI_PORT).ok();
        let v4 = match v4 {
            Ok(l) => l,
            Err(e) => {
                self.by_address_error = Some(format!("port {KYOKAI_PORT}: {e}"));
                return;
            }
        };
        let ctx = self.ctx.clone();
        let external = self.stored.easytier.is_some();
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
                let ip = from.ip().to_canonical();
                // With an official core, loopback is how it hands over the
                // network's connections (`accept_local` otherwise).
                let network = if external && ip.is_loopback() {
                    EXTERNAL_NETWORK
                } else if by.admits(ip) {
                    0
                } else {
                    continue; // dropped: closed without a word
                };
                let _ = stream.set_nodelay(true);
                tokio::spawn(session::serve(ctx.clone(), stream, Source { network, ip }));
            }
        }));
        self.accept_local();
    }

    /// Starts or stops accepting an official core's connections on
    /// loopback: while EasyTier is chosen and accepting by address, whose
    /// listener takes them then, is off.
    /// Side effects: binds or closes `127.0.0.1:KYOKAI_PORT`; a port taken
    /// fails every official core's network, saying so.
    fn accept_local(&mut self) {
        let want = self.stored.easytier.is_some() && self.by_address.is_none();
        if !want {
            self.local_error = None;
        }
        if want == self.local.is_some() {
            return;
        }
        if let Some(task) = self.local.take() {
            task.abort();
            let _ = self.rt.block_on(task);
            return;
        }
        let _rt = self.rt.enter();
        let listener = match std::net::TcpListener::bind((Ipv4Addr::LOCALHOST, KYOKAI_PORT))
            .and_then(|l| l.set_nonblocking(true).map(|()| l))
            .and_then(tokio::net::TcpListener::from_std)
        {
            Ok(l) => l,
            Err(e) => {
                self.local_error = Some(format!("port {KYOKAI_PORT}: {e}"));
                return;
            }
        };
        self.local_error = None;
        let ctx = self.ctx.clone();
        self.local = Some(self.rt.spawn(async move {
            loop {
                let Ok((stream, from)) = listener.accept().await else {
                    tokio::time::sleep(BIND_RETRY).await;
                    continue;
                };
                let _ = stream.set_nodelay(true);
                let source = Source {
                    network: EXTERNAL_NETWORK,
                    ip: from.ip(),
                };
                tokio::spawn(session::serve(ctx.clone(), stream, source));
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
        for task in [self.by_address.take(), self.local.take()]
            .into_iter()
            .flatten()
        {
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

/// Starts a network's official core, then watches it: a core that
/// stops leaves the network failed, with why.
/// Side effects: records why the core could not start in `failed`.
async fn serve_external(
    slot: Arc<std::sync::OnceLock<External>>,
    state_dir: PathBuf,
    tag: String,
    p: Profile,
    base: zeroize::Zeroizing<String>,
    failed: Arc<Mutex<Option<String>>>,
) {
    let fail = |why: String| {
        if let Ok(mut f) = failed.lock() {
            *f = Some(why);
        }
    };
    let started = match external::parse(&p.toml) {
        Ok(person) => External::start(&state_dir, &tag, &p.id, &base, &person, &p.secret).await,
        Err(e) => Err(e),
    };
    let core = match started {
        Ok(core) => slot.get_or_init(|| core),
        Err(e) => return fail(e),
    };
    loop {
        tokio::time::sleep(BIND_RETRY).await;
        if !core.alive().await {
            return fail(format!("EasyTier {tag} stopped"));
        }
    }
}

/// The nodes running now (not those that failed to start).
fn running(nodes: &Nodes) -> Vec<Core> {
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
            toml: String::new(),
        }]
    };
    Stored {
        direct: old.direct,
        latency_first: false,
        profiles,
        easytier: None,
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
                toml: u.toml.unwrap_or_default(),
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
            if let Some(toml) = u.toml {
                p.toml = toml;
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
    let doc = external::parse(&p.toml)?;
    if stored.easytier.is_none() && !external::keys(&doc).is_empty() {
        return Err("EasyTier's own options need the official EasyTier core".into());
    }
    let owned = external::owned_in(&doc);
    if !owned.is_empty() {
        return Err(format!("Kyokai sets these itself: {}", owned.join(", ")));
    }
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

/// Which routes a session to a host may take (doc/en-us/networking.md,
/// "Routes").
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Choice {
    /// The best now: the Kyokai network and the known host's addresses,
    /// ranked again at every connect and resume (`rank_routes`).
    Auto,
    /// The Kyokai network alone.
    Network,
    /// One address alone: chosen by hand, or typed in the connect form.
    Address(String),
}

impl Choice {
    /// The route a known host's `prefer` names (empty: Auto).
    pub fn from_prefer(prefer: &str) -> Choice {
        match prefer {
            "" => Choice::Auto,
            crate::access::PREFER_NETWORK => Choice::Network,
            a => Choice::Address(a.to_owned()),
        }
    }

    /// The route a request asks for: an address typed for this connection,
    /// else the route named (`auto`, `network` or an address), else the one
    /// chosen for the host (Auto when none).
    pub fn ask(ctx: &Ctx, device_id: &str, address: Option<&str>, route: Option<&str>) -> Choice {
        match (address.map(str::trim), route.map(str::trim)) {
            (Some(a), _) if !a.is_empty() => Choice::Address(a.to_owned()),
            (_, Some("auto")) => Choice::Auto,
            (_, Some(r)) if !r.is_empty() => Choice::from_prefer(r),
            _ => ctx
                .access
                .known(device_id)
                .map_or(Choice::Auto, |h| Choice::from_prefer(&h.prefer)),
        }
    }
}

/// How long a look at one route may take.
const PROBE_WAIT: Duration = Duration::from_secs(3);
/// How long the next route waits for the one before it to answer before it
/// is dialed too (happy eyeballs).
const STAGGER: Duration = Duration::from_millis(300);
/// The bitrate below which a route that also reached less than half of the
/// best route's is ranked as slow, in kbit/s.
const SLOW_KBPS: u32 = 8_000;
/// Failed dials in a row after which a route is tried last.
const FAILING: u8 = 2;

/// A route's standing when Auto ranks them.
#[derive(Clone, Debug, PartialEq)]
pub struct Standing {
    /// The address, or empty for the Kyokai network.
    pub address: String,
    /// Its round trip now (None: unknown, or it did not answer).
    pub rtt_ms: Option<u32>,
    /// Whether it answers now: the network announces the device, an
    /// address answered as the device (or was not looked at yet).
    pub reachable: bool,
    /// The network's path is direct (an address counts as direct).
    pub direct: bool,
    /// What earlier sessions remember of it.
    pub kbps: u32,
    pub fails: u8,
}

/// Ranks the routes Auto tries, best first: those that answer, then those
/// that did not failing FAILING times in a row, then those not remembered
/// slow (below SLOW_KBPS and below half of the best route's bitrate), by
/// round trip (unknown last), direct before relayed.
pub fn rank_routes(mut routes: Vec<Standing>) -> Vec<Standing> {
    let best = routes.iter().map(|r| r.kbps).max().unwrap_or(0);
    let slow = |r: &Standing| r.kbps != 0 && r.kbps < SLOW_KBPS && r.kbps < best / 2;
    routes.sort_by_key(|r| {
        (
            !r.reachable,
            r.fails >= FAILING,
            slow(r),
            r.rtt_ms.unwrap_or(u32::MAX),
            !r.direct,
        )
    });
    routes
}

/// What a look at one of a host's addresses found (memory only).
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Probe {
    pub address: String,
    /// The TCP handshake's round trip, when it answered.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rtt_ms: Option<u32>,
    /// `ok`, `unreachable`, `other-device` (another device answers there),
    /// `incompatible` or `broken` (not a Kyokai device).
    pub state: &'static str,
    /// Unix milliseconds.
    pub at: u64,
}

/// The latest looks at known hosts' addresses, by Device ID: what Home
/// shows and Auto ranks by.
#[derive(Default)]
pub struct Probes(Mutex<std::collections::HashMap<String, Vec<Probe>>>);

impl Probes {
    /// The latest looks at a host's addresses.
    pub fn of(&self, device_id: &str) -> Vec<Probe> {
        self.0
            .lock()
            .ok()
            .and_then(|m| m.get(device_id).cloned())
            .unwrap_or_default()
    }

    fn set(&self, device_id: &str, probes: Vec<Probe>) {
        if let Ok(mut m) = self.0.lock() {
            m.insert(device_id.to_owned(), probes);
        }
    }
}

/// How long the name a device of the networks shows is taken as it is
/// before another look (`Names`).
const NAME_AGAIN: Duration = Duration::from_secs(300);

/// The names the devices of the joined networks show before any login
/// (`about::Local::published`), as the last look at each found them, by
/// Device ID (memory only). Each is the device's claim, shown beside its
/// Device ID (doc/en-us/identity-and-access.md, "Friendly names").
#[derive(Default)]
pub struct Names(Mutex<std::collections::HashMap<String, Named>>);

/// The looks at a device's name: the name it showed (None: none), when the
/// last look began, and whether it is still under way.
#[derive(Default)]
struct Named {
    name: Option<String>,
    at: Option<Instant>,
    busy: bool,
}

impl Names {
    /// The name a device showed at the last look that reached it.
    fn of(&self, device_id: &str) -> Option<String> {
        self.0
            .lock()
            .ok()
            .and_then(|m| m.get(device_id).and_then(|n| n.name.clone()))
    }

    /// Takes the devices whose name is due for a look — never looked at, or
    /// last NAME_AGAIN ago and not under way — and marks them under way.
    fn due(&self, device_ids: &[String], now: Instant) -> Vec<String> {
        let Ok(mut m) = self.0.lock() else {
            return Vec::new();
        };
        let mut out = Vec::new();
        for id in device_ids {
            let named = m.entry(id.clone()).or_default();
            if named.busy
                || named
                    .at
                    .is_some_and(|at| now.duration_since(at) < NAME_AGAIN)
            {
                continue;
            }
            named.at = Some(now);
            named.busy = true;
            out.push(id.clone());
        }
        out
    }

    /// Records what a look found: the name shown (Some(None): none), or
    /// None when it reached nobody, which keeps the last name.
    fn found(&self, device_id: &str, seen: Option<Option<String>>) {
        if let Ok(mut m) = self.0.lock()
            && let Some(named) = m.get_mut(device_id)
        {
            named.busy = false;
            if let Some(name) = seen {
                named.name = name;
            }
        }
    }
}

/// Reads the name a device of the networks shows: its hello over the
/// first of its routes that answers, which must be the device itself.
/// Returns: the name shown (Some(None): none), or None when no route
/// answered as the device.
async fn look_name(nodes: Vec<Core>, device_id: &str) -> Option<Option<String>> {
    for c in candidates(&nodes, device_id).await {
        let look = async {
            let stream = open(Target::Net(c), nodes.clone()).await?;
            session::identify(stream).await.ok()
        };
        if let Ok(Some((id, name))) = tokio::time::timeout(PROBE_WAIT, look).await
            && id == device_id
        {
            return Some(name);
        }
    }
    None
}

/// Looks at an address: resolves it, times the TCP handshake and reads the
/// host's hello, which says which device answers.
pub async fn probe_address(address: &str, device_id: &str) -> Probe {
    let state = |state, rtt_ms| Probe {
        address: address.to_owned(),
        rtt_ms,
        state,
        at: unix_ms(),
    };
    let look = async {
        let Ok((stream, rtt)) = connect_address(address, PROBE_WAIT).await else {
            return state("unreachable", None);
        };
        let rtt = Some(rtt.as_millis().min(u128::from(u32::MAX)) as u32);
        match session::identify(stream).await {
            Ok((id, _)) if id == device_id => state("ok", rtt),
            Ok(_) => state("other-device", rtt),
            Err(session::Failure::Incompatible) => state("incompatible", rtt),
            Err(_) => state("broken", rtt),
        }
    };
    tokio::time::timeout(PROBE_WAIT, look)
        .await
        .unwrap_or_else(|_| state("unreachable", None))
}

fn unix_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_millis() as u64)
}

/// One way to open a connection to a host, in the order routes are tried.
#[derive(Clone, Debug, PartialEq)]
enum Target {
    /// A route in one of the joined networks.
    Net(Candidate),
    /// An address.
    Addr(String),
}

impl Target {
    /// The route it belongs to: empty for the network.
    fn address(&self) -> &str {
        match self {
            Target::Net(_) => "",
            Target::Addr(a) => a,
        }
    }
}

/// A connection, whichever route opened it.
trait Io: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send {}
impl<T: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send> Io for T {}

/// Opens a connection over one target.
async fn open(target: Target, nodes: Vec<Core>) -> Option<Box<dyn Io>> {
    match target {
        Target::Net(c) => nodes.get(c.node)?.connect(c.ip).await,
        Target::Addr(a) => {
            let (stream, _) = connect_address(&a, DIAL_TIMEOUT).await.ok()?;
            Some(Box::new(stream))
        }
    }
}

/// Opens the first connection that answers among `targets`, the next one
/// starting STAGGER after the one before it unless that answered first.
/// Returns: the index of the target that won, and its connection; the
/// targets whose connection failed before it are `failed`.
async fn race(
    targets: &[Target],
    nodes: &[Core],
    failed: &mut Vec<usize>,
) -> Option<(usize, Box<dyn Io>)> {
    let mut set = tokio::task::JoinSet::new();
    for (i, t) in targets.iter().enumerate() {
        let (t, nodes) = (t.clone(), nodes.to_vec());
        set.spawn(async move {
            tokio::time::sleep(STAGGER * i as u32).await;
            (i, open(t, nodes).await)
        });
    }
    while let Some(done) = set.join_next().await {
        match done {
            Ok((i, Some(io))) => {
                set.abort_all();
                return Some((i, io));
            }
            Ok((i, None)) => failed.push(i),
            Err(_) => {}
        }
    }
    None
}

/// A TCP listener on `port` (KYOKAI_PORT) of every address of one family
/// (IPv6 only for IPv6, so both families can listen side by side).
fn listen_tcp(any: IpAddr, port: u16) -> std::io::Result<tokio::net::TcpListener> {
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
    // A connection the last listener closed may linger in TIME-WAIT, which
    // keeps Linux and macOS from binding the port for a minute without it
    // (std and tokio set it too); on Windows it would let another socket
    // take the port, so not there.
    #[cfg(unix)]
    socket.set_reuse_address(true)?;
    socket.bind(&SocketAddr::new(any, port).into())?;
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
/// resolves to, in order, until one answers within `wait`.
/// Returns: the connection and how long its TCP handshake took (one round
/// trip; the name's lookup left out).
async fn connect_address(
    address: &str,
    wait: Duration,
) -> Result<(tokio::net::TcpStream, Duration), session::Failure> {
    let (host, port) = split_address(address).map_err(|_| session::Failure::Unreachable)?;
    let addrs = tokio::time::timeout(wait, tokio::net::lookup_host((host.as_str(), port)))
        .await
        .map_err(|_| session::Failure::Unreachable)?
        .map_err(|_| session::Failure::Unreachable)?;
    for addr in addrs {
        let began = std::time::Instant::now();
        if let Ok(Ok(stream)) =
            tokio::time::timeout(wait, tokio::net::TcpStream::connect(addr)).await
        {
            let rtt = began.elapsed();
            let _ = stream.set_nodelay(true);
            return Ok((stream, rtt));
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
    let (stream, _) = connect_address(address, DIAL_TIMEOUT)
        .await
        .map_err(|_| "unreachable")?;
    session::identify(stream)
        .await
        .map(|(id, _)| id)
        .map_err(|f| match f {
            session::Failure::Incompatible => "incompatible",
            _ => "broken",
        })
}

/// Decides whether a dropped session keeps resuming after an attempt.
/// Inputs: when the current outage began (None before the first drop),
/// whether this attempt's session was connected before it dropped (a
/// resume that went through), and the time now.
/// Returns: when the outage began, counted afresh after a session that
/// was connected again, and whether the RESUME_FOR window still lasts.
fn keep_resuming(
    lost_at: Option<std::time::Instant>,
    connected: bool,
    now: std::time::Instant,
) -> (std::time::Instant, bool) {
    let since = match lost_at {
        Some(t) if !connected => t,
        _ => now,
    };
    (since, now.duration_since(since) <= RESUME_FOR)
}

/// A session to start: with whom, by which routes, the password (empty
/// asks without one, or relies on a sign-in key), what to ask for, what to
/// do with the password, and what the session carries.
pub struct Ask {
    pub device_id: String,
    pub route: Choice,
    pub password: zeroize::Zeroizing<String>,
    pub want: Vec<Permission>,
    pub remember: session::Remember,
    pub kind: session::Kind,
}

/// Dials devices over their routes: the networks joined when it dials (a
/// resumed session may go through another one) and the addresses kept for
/// them.
#[derive(Clone)]
pub struct Dialer {
    nodes: Nodes,
    ctx: Arc<Ctx>,
    probes: Arc<Probes>,
}

impl Dialer {
    /// The state its sessions share.
    pub fn ctx(&self) -> &Arc<Ctx> {
        &self.ctx
    }

    /// Registers a session and returns the work that dials and runs it,
    /// resuming a dropped one while its window lasts.
    /// Returns: the session's id, and the work, which ends with the session
    /// (its entry records how).
    /// Notes: every attempt (the first and each resume) works out the
    /// targets again (`targets`); the handshake decides which one is the
    /// device. A session by address needs no network.
    pub fn start(
        &self,
        ask: Ask,
    ) -> Result<(u64, impl Future<Output = ()> + Send + 'static), String> {
        let Ask {
            device_id,
            route,
            password,
            want,
            remember,
            kind,
        } = ask;
        let no_network = running(&self.nodes).is_empty();
        match &route {
            Choice::Address(address) => {
                split_address(address)?;
            }
            Choice::Network if no_network => return Err("not connected to a network".into()),
            Choice::Auto
                if no_network
                    && self
                        .ctx
                        .access
                        .known(&device_id)
                        .is_none_or(|h| h.routes.is_empty()) =>
            {
                return Err("not connected to a network".into());
            }
            _ => {}
        }
        let (id, stop) = session::begin_as(&self.ctx, &device_id, remember, kind);
        let dialer = self.clone();
        let work = async move {
            let ctx = dialer.ctx.clone();
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
                let nodes = running(&dialer.nodes);
                let targets = dialer.targets(&nodes, &device_id, &route).await;
                let dialed = dial(
                    &ctx,
                    id,
                    &stop,
                    &nodes,
                    &targets,
                    &device_id,
                    &password,
                    &want,
                    resume.take(),
                )
                .await;
                let (failure, connected) = match dialed {
                    Dialed::Outcome(session::Outcome::Done) => return,
                    Dialed::Outcome(session::Outcome::Lost { token, connected }) => {
                        resume = Some(token);
                        (session::Failure::Broken, connected)
                    }
                    Dialed::Outcome(session::Outcome::WrongDevice) => {
                        (session::Failure::WrongDevice, false)
                    }
                    Dialed::Nothing(f, token) => {
                        resume = token;
                        (f, false)
                    }
                };
                // Keep resuming only while a token is held and the window lasts.
                let (since, keep) = keep_resuming(lost_at, connected, std::time::Instant::now());
                lost_at = Some(since);
                if resume.is_none() || !keep {
                    return session::give_up(&ctx, id, failure);
                }
            }
        };
        Ok((id, work))
    }

    /// The targets one attempt tries, in order: the network's routes to
    /// the device (direct first, then the lowest latency) and the addresses
    /// chosen; under Auto every kept address is looked at first
    /// (`probe_address`, all at once) and the routes ranked
    /// (`rank_routes`).
    async fn targets(&self, nodes: &[Core], device_id: &str, route: &Choice) -> Vec<Target> {
        match route {
            Choice::Network => candidates(nodes, device_id)
                .await
                .into_iter()
                .map(Target::Net)
                .collect(),
            Choice::Address(a) => vec![Target::Addr(a.clone())],
            Choice::Auto => {
                let host = self.ctx.access.known(device_id);
                let (probes, cands) =
                    tokio::join!(self.probe(device_id), candidates(nodes, device_id));
                let mut standing = Vec::new();
                if !nodes.is_empty() {
                    let best = cands.first();
                    standing.push(Standing {
                        address: String::new(),
                        rtt_ms: best.map(|c| c.latency.max(0) as u32),
                        reachable: best.is_some(),
                        direct: best.is_some_and(|c| c.direct),
                        kbps: host.as_ref().map_or(0, |h| h.network.kbps),
                        fails: host.as_ref().map_or(0, |h| h.network.fails),
                    });
                }
                for r in host.iter().flat_map(|h| &h.routes) {
                    let p = probes.iter().find(|p| p.address == r.address);
                    standing.push(Standing {
                        address: r.address.clone(),
                        rtt_ms: p.and_then(|p| p.rtt_ms),
                        reachable: p.is_none_or(|p| p.state == "ok"),
                        direct: true,
                        kbps: r.stats.kbps,
                        fails: r.stats.fails,
                    });
                }
                rank_routes(standing)
                    .into_iter()
                    .flat_map(|s| match s.address.as_str() {
                        "" => cands.iter().copied().map(Target::Net).collect::<Vec<_>>(),
                        a => vec![Target::Addr(a.to_owned())],
                    })
                    .collect()
            }
        }
    }

    /// Looks at every address kept for a known host at once, and keeps
    /// what it found for the status (`Probes`).
    pub async fn probe(&self, device_id: &str) -> Vec<Probe> {
        let routes = self
            .ctx
            .access
            .known(device_id)
            .map(|h| h.routes)
            .unwrap_or_default();
        let mut set = tokio::task::JoinSet::new();
        for (i, r) in routes.into_iter().enumerate() {
            let id = device_id.to_owned();
            set.spawn(async move { (i, probe_address(&r.address, &id).await) });
        }
        let mut found: Vec<(usize, Probe)> = Vec::new();
        while let Some(Ok(p)) = set.join_next().await {
            found.push(p);
        }
        found.sort_by_key(|(i, _)| *i);
        let found: Vec<Probe> = found.into_iter().map(|(_, p)| p).collect();
        self.probes.set(device_id, found.clone());
        found
    }
}

/// What one round of dialing a Device ID came to.
enum Dialed {
    /// A handshake ran on some route.
    Outcome(session::Outcome),
    /// No route answered, or every one was a squatter; the resume token, if
    /// any, is handed back unused.
    Nothing(session::Failure, Option<zeroize::Zeroizing<String>>),
}

/// Runs the session on the first target that answers (`race`) and proves to
/// be the device; another device answering moves on to the targets after
/// it. Notes how each address and the route that carried the session went
/// (`Access::note_route`).
#[allow(clippy::too_many_arguments)]
async fn dial(
    ctx: &Arc<Ctx>,
    id: u64,
    stop: &Arc<tokio::sync::Notify>,
    nodes: &[Core],
    targets: &[Target],
    device_id: &str,
    password: &zeroize::Zeroizing<String>,
    want: &[Permission],
    resume: Option<zeroize::Zeroizing<String>>,
) -> Dialed {
    let mut rest = targets;
    let mut failure = session::Failure::Unreachable;
    while !rest.is_empty() {
        let mut failed = Vec::new();
        let won = race(rest, nodes, &mut failed).await;
        for &i in &failed {
            if let Target::Addr(a) = &rest[i] {
                let _ = ctx.access.note_route(device_id, a, None);
            }
        }
        let Some((i, io)) = won else {
            break;
        };
        let target = rest[i].clone();
        session::dials_address(ctx, id, target.address());
        let outcome = session::connect(
            ctx.clone(),
            id,
            stop.clone(),
            io,
            device_id.to_owned(),
            password.clone(),
            want.to_vec(),
            resume.clone(),
        )
        .await;
        // A squatter with another key fails the handshake: try the rest.
        if matches!(outcome, session::Outcome::WrongDevice) {
            failure = session::Failure::WrongDevice;
            if let Target::Addr(a) = &target {
                let _ = ctx.access.note_route(device_id, a, None);
            }
            rest = &rest[i + 1..];
            continue;
        }
        // What the session saw of its route, for the next ranking.
        if let Some(seen) = session::route_seen(ctx, id) {
            let _ = ctx
                .access
                .note_route(device_id, target.address(), Some(seen));
        }
        return Dialed::Outcome(outcome);
    }
    Dialed::Nothing(failure, resume)
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

/// The routes that announce `device_id` in every joined network (at most
/// MAX_CANDIDATES in each), in `order`.
async fn candidates(nodes: &[Core], device_id: &str) -> Vec<Candidate> {
    let hostname = format!("Kyokai-{device_id}");
    let mut candidates = Vec::new();
    for (n, node) in nodes.iter().enumerate() {
        let Some(seen) = node.seen().await else {
            continue;
        };
        candidates.extend(
            seen.peers
                .into_iter()
                .filter(|r| r.hostname == hostname)
                .filter_map(|r| {
                    Some(Candidate {
                        node: n,
                        ip: r.ip?,
                        direct: r.direct,
                        latency: r.latency,
                    })
                })
                .take(MAX_CANDIDATES),
        );
    }
    order(&mut candidates);
    candidates
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
/// Every node refuses EasyTier's QUIC and KCP proxy input (a network member
/// could otherwise open a connection to this computer's loopback, guarded
/// only by the port whitelist) and peers of other networks at its listener
/// (`private_mode`: no relaying of foreign networks); Kyokai's sessions
/// never use those proxies.
/// Notes: an official core runs this same configuration, with the
/// network's own options over it (`external::merged`).
fn config(
    s: &Profile,
    device_id: &str,
    direct: bool,
    latency_first: bool,
    listen: Option<u16>,
) -> anyhow::Result<TomlConfig> {
    TomlConfig::new_from_str(&config_text(s, device_id, direct, latency_first, listen))
}

/// The text of a network's node configuration (`config`).
fn config_text(
    s: &Profile,
    device_id: &str,
    direct: bool,
    latency_first: bool,
    listen: Option<u16>,
) -> String {
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
latency_first = {latency_first}
disable_quic_input = true
disable_kcp_input = true
private_mode = true
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
    toml
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

    /// A host started again listens at once although its last listener
    /// closed a connection (TIME-WAIT on the port); regression: a Linux host
    /// restarted after a session listened nowhere for a minute.
    #[cfg(unix)]
    #[tokio::test]
    async fn the_port_is_listened_on_again_after_a_closed_connection() {
        let any = IpAddr::V4(Ipv4Addr::LOCALHOST);
        // A port of its own, free now.
        let port = std::net::TcpListener::bind((Ipv4Addr::LOCALHOST, 0))
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        let first = listen_tcp(any, port).unwrap();
        let client = tokio::net::TcpStream::connect((Ipv4Addr::LOCALHOST, port))
            .await
            .unwrap();
        let (served, _) = first.accept().await.unwrap();
        // The listening side closes first, so the port keeps TIME-WAIT.
        drop(served);
        drop(client);
        drop(first);
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert!(listen_tcp(any, port).is_ok());
    }

    /// Auto ranks routes that answer first, then those not failing, then
    /// those not remembered slow, by round trip (unknown last), direct
    /// before relayed.
    #[test]
    fn routes_rank_by_latency_history_and_failures() {
        let r = |a: &str, rtt: Option<u32>, reachable: bool, direct: bool, kbps: u32, fails: u8| {
            Standing {
                address: a.into(),
                rtt_ms: rtt,
                reachable,
                direct,
                kbps,
                fails,
            }
        };
        let order = |routes: Vec<Standing>| -> Vec<String> {
            rank_routes(routes).into_iter().map(|s| s.address).collect()
        };
        // By latency.
        assert_eq!(
            order(vec![
                r("wan", Some(40), true, true, 0, 0),
                r("lan", Some(2), true, true, 0, 0),
                r("", Some(230), true, false, 0, 0)
            ]),
            ["lan", "wan", ""]
        );
        // One that does not answer goes last, whatever its latency.
        assert_eq!(
            order(vec![
                r("lan", Some(1), false, true, 0, 0),
                r("", Some(230), true, false, 0, 0)
            ]),
            ["", "lan"]
        );
        // Remembered slow (below 8 Mbit/s and half the best) goes behind.
        assert_eq!(
            order(vec![
                r("slow", Some(5), true, true, 1_500, 0),
                r("", Some(60), true, true, 20_000, 0)
            ]),
            ["", "slow"]
        );
        // Slow only next to a far better one: alone it is not slow.
        assert_eq!(
            order(vec![
                r("a", Some(5), true, true, 6_000, 0),
                r("b", Some(60), true, true, 9_000, 0)
            ]),
            ["a", "b"]
        );
        // Two failures in a row go behind; unknown latency after known.
        assert_eq!(
            order(vec![
                r("failing", Some(1), true, true, 0, 2),
                r("unknown", None, true, true, 0, 0),
                r("known", Some(90), true, true, 0, 0)
            ]),
            ["known", "unknown", "failing"]
        );
        // Direct before relayed at equal latency.
        assert_eq!(
            order(vec![
                r("", Some(20), true, false, 0, 0),
                r("x", Some(20), true, true, 0, 0)
            ]),
            ["x", ""]
        );
    }

    /// A host's chosen route reads back as the route.
    #[test]
    fn a_chosen_route_reads_back() {
        assert_eq!(Choice::from_prefer(""), Choice::Auto);
        assert_eq!(Choice::from_prefer("network"), Choice::Network);
        assert_eq!(
            Choice::from_prefer("10.0.0.5"),
            Choice::Address("10.0.0.5".into())
        );
    }

    fn profile(servers: &[&str], name: &str, secret: &str) -> Profile {
        Profile {
            id: "00000000000000a1".into(),
            label: String::new(),
            servers: servers.iter().map(|s| s.to_string()).collect(),
            name: name.into(),
            secret: secret.into(),
            joined: true,
            toml: String::new(),
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
            config(&a, "100000002", false, false, None).unwrap(),
            config(&b, "100000002", false, false, None).unwrap(),
        );
        assert_eq!(ca.get_id(), cb.get_id());
        assert_eq!(ca.get_id(), instance_id("100000002"));
        assert_eq!(
            (ca.get_hostname(), cb.get_hostname()),
            ("Kyokai-100000002".into(), "Kyokai-100000002".into())
        );
        assert_ne!(ca.get_inst_name(), cb.get_inst_name());
    }

    /// A device's name is looked at once, then again only NAME_AGAIN later
    /// and never twice at a time; a look that reached nobody keeps the last
    /// name, one that reached the device without a name clears it.
    #[test]
    fn names_are_looked_at_now_and_then() {
        let names = Names::default();
        let ids = ["1".to_owned(), "2".to_owned()];
        let t0 = Instant::now();
        assert_eq!(names.due(&ids, t0), ids, "never looked at");
        assert!(names.due(&ids, t0 + NAME_AGAIN * 2).is_empty(), "under way");
        names.found("1", Some(Some("Den".into())));
        names.found("2", None);
        assert_eq!(names.of("1").as_deref(), Some("Den"));
        assert_eq!(names.of("2"), None);
        assert!(names.due(&ids, t0 + NAME_AGAIN / 2).is_empty(), "too soon");
        assert_eq!(names.due(&ids, t0 + NAME_AGAIN), ids);
        names.found("1", None);
        assert_eq!(names.of("1").as_deref(), Some("Den"), "nobody reached");
        names.found("2", Some(None));
        assert_eq!(names.due(&ids[..1], t0 + NAME_AGAIN * 2), ["1"]);
        names.found("1", Some(None));
        assert_eq!(names.of("1"), None, "no longer shown");
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
        let cfg = config(&s, "100000002", false, false, None).unwrap();
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
    fn every_node_refuses_proxy_input_and_foreign_networks() {
        use easytier_core::config::toml::ConfigLoader;
        let s = profile(&["tcp://a:1"], "n", "p");
        for (direct, latency_first) in [(false, false), (true, false), (false, true), (true, true)]
        {
            let f = config(&s, "100000002", direct, latency_first, Some(47_311))
                .unwrap()
                .get_flags();
            // A network member must never reach this computer's loopback
            // through EasyTier's QUIC or KCP proxy, nor use a listening node as
            // a relay for its own network.
            assert!(f.disable_quic_input && f.disable_kcp_input && f.private_mode);
            assert_eq!(f.latency_first, latency_first);
        }
    }

    #[test]
    fn latency_first_reads_as_off_from_older_settings() {
        let s: Stored = serde_json::from_str(r#"{"direct":true,"profiles":[]}"#).unwrap();
        assert!(s.direct && !s.latency_first);
    }

    #[test]
    fn the_runtime_uses_half_the_processors_within_bounds() {
        assert!((2..=4).contains(&net_workers()));
    }

    #[test]
    fn direct_paths_are_off_unless_chosen() {
        use easytier_core::config::toml::ConfigLoader;
        let s = profile(&["tcp://a:1"], "n", "p");
        for direct in [false, true] {
            let cfg = config(&s, "100000002", direct, false, Some(47_311)).unwrap();
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
        let cfg = config(&s, "100000002", true, false, None).unwrap();
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

    /// A network's EasyTier options need the official core, never hold a
    /// key Kyokai sets itself, and are kept as typed otherwise.
    #[test]
    fn easytier_options_are_checked() {
        let mut stored = Stored {
            profiles: vec![profile(&["tcp://a:1"], "n", "0123456789abcdef")],
            ..Stored::default()
        };
        let set = |stored: &Stored, toml: &str| {
            patched(
                stored,
                ProfileUpdate {
                    id: Some("00000000000000a1".into()),
                    toml: Some(toml.into()),
                    ..ProfileUpdate::default()
                },
            )
        };
        assert!(
            set(&stored, "mtu = 1300").is_err(),
            "options without the core"
        );
        // Comments only set nothing, which Kyokai Network takes too.
        assert!(set(&stored, "# none\n").is_ok());
        stored.easytier = Some("v2.7.0".into());
        let refused = set(&stored, "hostname = \"x\"\n[flags]\nno_tun = false\n")
            .err()
            .unwrap();
        assert!(
            refused.contains("hostname") && refused.contains("flags.no_tun"),
            "{refused}"
        );
        assert!(set(&stored, "mtu = ").is_err(), "invalid TOML");
        let kept = set(&stored, "mtu = 1300\n").unwrap();
        assert_eq!(kept.profiles[0].toml, "mtu = 1300\n");
    }

    #[test]
    fn instance_id_is_stable() {
        assert_eq!(
            instance_id("100000002").to_string(),
            "d409b924-1be1-5f9e-b719-f4bdaa8608b1"
        );
    }

    /// Regression: the resume window started at the first drop and never
    /// again, so a session that resumed and dropped a second time more than
    /// RESUME_FOR after the first gave up at once.
    #[test]
    fn a_session_that_resumed_gets_a_fresh_resume_window() {
        use std::time::{Duration, Instant};
        let t0 = Instant::now();
        let s = |n: u64| t0 + Duration::from_secs(n);
        // The first drop opens the window.
        assert_eq!(keep_resuming(None, true, s(0)), (s(0), true));
        // Attempts that do not get through keep it, until it runs out.
        assert_eq!(keep_resuming(Some(s(0)), false, s(30)), (s(0), true));
        assert_eq!(keep_resuming(Some(s(0)), false, s(61)), (s(0), false));
        // A resume that went through and dropped later starts anew.
        assert_eq!(keep_resuming(Some(s(0)), true, s(300)), (s(300), true));
        assert_eq!(keep_resuming(Some(s(300)), false, s(330)), (s(300), true));
    }
}
