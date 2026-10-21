//! Who may connect to this host and how the host decides
//! (doc/en-us/identity-and-access.md): password slots, rate limits, the
//! host user's confirmations, trusted devices (pinned controller keys) and
//! known hosts (pinned host keys on the controller side).

use crate::about::About;
use crate::auth::{Slot, Slots};
use crate::store::Secrets;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::collections::{BTreeMap, HashMap, VecDeque};
use std::net::IpAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::sync::oneshot;

/// What a controller may do; none implies another.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Permission {
    ViewScreen,
    ControlInput,
    RunAiTask,
    ApproveAiAction,
    Clipboard,
    /// Copying files through the clipboard; `Clipboard` does not include it.
    Files,
    /// Hearing what the host plays; `ViewScreen` does not include it.
    HearSound,
    /// Seeing the host's system status (CPU, memory, disk, network) as a
    /// quiet watcher; needs a peer announcing `status`.
    SeeStatus,
    /// A tunnel to the host's own SSH server; needs `tunnel`.
    UseSsh,
    /// A tunnel to the host's own Remote Desktop; needs `tunnel`.
    UseRdp,
    /// A tunnel to the host's own VNC or Screen Sharing; needs `tunnel`.
    UseVnc,
}

impl Permission {
    /// The feature a peer must announce before this permission crosses the
    /// wire to it (older builds end a session on a name they do not know).
    pub fn feature(self) -> Option<&'static str> {
        use crate::session::feature;
        match self {
            Permission::SeeStatus => Some(feature::STATUS),
            Permission::UseSsh | Permission::UseRdp | Permission::UseVnc => Some(feature::TUNNEL),
            _ => None,
        }
    }
}

/// What of `want` may be sent to a peer: the permissions it knows.
pub fn wire_want(want: &[Permission], peer: &crate::session::Peer) -> Vec<Permission> {
    want.iter()
        .copied()
        .filter(|p| p.feature().is_none_or(|f| peer.has(f)))
        .collect()
}

/// Reads a list of permissions, leaving out names this build does not know
/// (a newer peer's), so they never end a session.
pub fn known_permissions<'de, D: serde::Deserializer<'de>>(
    d: D,
) -> Result<Vec<Permission>, D::Error> {
    let names = Vec::<Value>::deserialize(d)?;
    Ok(names
        .into_iter()
        .filter_map(|n| serde_json::from_value(n).ok())
        .collect())
}

/// Why a host refused a controller (names cross the wire and the FFI).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Refusal {
    Password,
    Denied,
    Timeout,
    Busy,
    RateLimited,
    /// Nobody is at the host to confirm, and the controller has no
    /// unattended grant (or the host serves no one now).
    Absent,
}

/// Whom the host serves now (the engine decides from its run mode and
/// whether a GUI is attached; doc/en-us/run-modes.md).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Serve {
    /// Everyone who logs in, with confirmation or an unattended grant.
    #[default]
    All,
    /// Only unattended grants: nobody is there to confirm.
    Unattended,
    /// No one (Follow GUI without a GUI).
    None,
}

const TRUSTED_NAME: &str = "trusted_devices";
const KNOWN_HOSTS_NAME: &str = "known_hosts";
/// A saved host password's secret is this prefix and the Device ID.
const SAVED_PASSWORD_PREFIX: &str = "saved_password.";
/// What else is saved for a host (`Saved`) is this prefix and the Device ID.
const SAVED_PREFIX: &str = "saved.";
const PASSWORD_GRANTS_NAME: &str = "password_grants";
const PASSWORD_MODE_NAME: &str = "password_mode";
const WITHOUT_ASKING_NAME: &str = "connect_without_asking";
const BY_ADDRESS_NAME: &str = "connect_by_address";
/// Set while this device keeps no connection history (Settings, No logs).
const NO_HISTORY_NAME: &str = "no_history";
/// Set while the host is in protected mode: its interface offers only the
/// AI settings and tasks until the permanent password turns it off.
const PROTECTED_NAME: &str = "protected";
/// The connection history: whom this host let in, whom this device
/// connected to.
const HISTORY: [&str; 2] = [TRUSTED_NAME, KNOWN_HOSTS_NAME];
/// The source addresses a host accepts connections by address from until
/// its user changes them: the private and link-local IPv4 networks,
/// Tailscale's addresses (100.64.0.0/10) and IPv6 unique-local (Tailscale's
/// IPv6 addresses among them) and link-local addresses.
pub const DEFAULT_BY_ADDRESS_RANGES: [&str; 7] = [
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "169.254.0.0/16",
    "100.64.0.0/10",
    "fc00::/7",
    "fe80::/10",
];
/// Most address ranges a host lists.
const MAX_RANGES: usize = 32;

/// Whether this host accepts sessions by address, on this computer's own
/// addresses, and from which source addresses (Settings, Access).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ByAddress {
    pub on: bool,
    /// Address ranges (`192.168.0.0/16`, `fe80::/10`) or single addresses.
    pub ranges: Vec<String>,
}

impl Default for ByAddress {
    fn default() -> Self {
        ByAddress {
            on: false,
            ranges: DEFAULT_BY_ADDRESS_RANGES.map(String::from).to_vec(),
        }
    }
}

impl ByAddress {
    /// Whether a connection from `ip` is accepted (an IPv4 address mapped
    /// into IPv6 counts as IPv4).
    pub fn admits(&self, ip: IpAddr) -> bool {
        let ip = ip.to_canonical();
        self.on
            && self
                .ranges
                .iter()
                .filter_map(|r| parse_range(r))
                .any(|(net, len)| in_range(net, len, ip))
    }

    /// Checks the ranges at the local API boundary.
    fn check(&self) -> Result<(), String> {
        if self.ranges.len() > MAX_RANGES {
            return Err(format!("at most {MAX_RANGES} address ranges"));
        }
        match self.ranges.iter().find(|r| parse_range(r).is_none()) {
            Some(bad) => Err(format!("{bad:?} is not an address range")),
            None => Ok(()),
        }
    }
}

/// Reads `a.b.c.d/n`, `v6/n` or a single address (a full-length prefix).
fn parse_range(range: &str) -> Option<(IpAddr, u8)> {
    let (ip, len) = match range.split_once('/') {
        Some((ip, len)) => (ip.parse::<IpAddr>().ok()?, len.parse::<u8>().ok()?),
        None => {
            let ip = range.parse::<IpAddr>().ok()?;
            (ip, if ip.is_ipv4() { 32 } else { 128 })
        }
    };
    let max = if ip.is_ipv4() { 32 } else { 128 };
    (len <= max).then_some((ip, len))
}

/// Whether `ip` lies in the network `net`/`len` (both of one family).
fn in_range(net: IpAddr, len: u8, ip: IpAddr) -> bool {
    match (net, ip) {
        (IpAddr::V4(n), IpAddr::V4(i)) => {
            let mask = u32::MAX.checked_shl(32 - u32::from(len)).unwrap_or(0);
            u32::from(n) & mask == u32::from(i) & mask
        }
        (IpAddr::V6(n), IpAddr::V6(i)) => {
            let mask = u128::MAX.checked_shl(128 - u32::from(len)).unwrap_or(0);
            u128::from(n) & mask == u128::from(i) & mask
        }
        _ => false,
    }
}

/// Which of the host's passwords open it (Settings, Access, "Accepted
/// passwords").
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum PasswordMode {
    Temporary,
    Permanent,
    #[default]
    Both,
}

impl PasswordMode {
    /// Whether a login that opened `slot` counts.
    pub fn accepts(self, slot: Slot) -> bool {
        matches!(
            (self, slot),
            (PasswordMode::Both, _)
                | (PasswordMode::Temporary, Slot::Temporary)
                | (PasswordMode::Permanent, Slot::Permanent)
        )
    }
}
/// What a correct password grants without asking until the host's user
/// changes it: the manual session; AI tasks always ask.
const DEFAULT_PASSWORD_GRANTS: [Permission; 5] = [
    Permission::ViewScreen,
    Permission::ControlInput,
    Permission::Clipboard,
    Permission::Files,
    Permission::HearSound,
];
/// Failed logins one source address may make in `FAIL_WINDOW`.
pub(crate) const FAILS_PER_ADDRESS: usize = 5;
const FAIL_WINDOW: Duration = Duration::from_secs(10 * 60);
/// How long a source address that hit the limit is refused.
const ADDRESS_BLOCK: Duration = Duration::from_secs(10 * 60);
/// Failed logins of all sources per minute before unpinned controllers wait.
const GLOBAL_FAILS: usize = 20;
const GLOBAL_WAIT: Duration = Duration::from_secs(30);
/// How long the host's user has to answer a confirmation.
pub const CONFIRM_TIMEOUT: Duration = Duration::from_secs(60);
/// Open confirmations in total.
const MAX_PENDING: usize = 4;
/// How long a resume token stays valid after it was issued.
const RESUME_WINDOW: Duration = Duration::from_secs(120);
/// Resume tokens kept at most; the oldest go first.
const MAX_RESUMES: usize = 64;
/// Recent devices listed at most.
const MAX_RECENT: usize = 8;

/// A controller key the host has accepted before.
#[derive(Clone, Serialize, Deserialize)]
pub struct Trusted {
    pub device_id: String,
    /// Unix seconds.
    pub first: u64,
    pub last: u64,
    pub granted: Vec<Permission>,
    /// What the controller last said about itself.
    #[serde(default)]
    pub about: About,
}

/// The host user's answer to a confirmation: the granted permissions, or
/// None for a denial.
pub type Decision = oneshot::Receiver<Option<Vec<Permission>>>;

/// A host this device connected to (controller side): its pinned key and
/// when the last session with it was accepted.
#[derive(Clone, Serialize, Deserialize)]
pub struct KnownHost {
    pub key: String,
    /// Unix seconds.
    pub last: u64,
    /// What the host last said about itself.
    #[serde(default)]
    pub about: About,
    /// The address the last session dialed, or empty over the network: the
    /// next connection from Home goes the same way.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub address: String,
    /// What the host announced in the last session accepted (`feature`),
    /// so Home knows what it offers before dialing.
    #[serde(default)]
    pub features: Vec<String>,
}

/// What this device keeps for signing in to one host besides its password
/// (`saved.<Device ID>` in the secrets store, controller side).
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Saved {
    /// The id of the login key the host last admitted.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
}

impl Saved {
    /// Whether nothing is kept.
    fn is_empty(&self) -> bool {
        self.key.is_none()
    }
}

/// A resume token's grant: who may resume, with what, until when, and
/// the session it resumes (which the new connection replaces).
struct Resume {
    key: String,
    granted: Vec<Permission>,
    until: Instant,
    session: u64,
}

/// A confirmation waiting for the host's user.
struct Pending {
    id: u64,
    key: String,
    device_id: String,
    known: bool,
    new_key: bool,
    /// Whether the controller proved a password or a key (false: it only
    /// asks).
    password: bool,
    want: Vec<Permission>,
    about: About,
    /// The name of the key the controller proved, if it proved one.
    key_name: Option<String>,
    reply: oneshot::Sender<Option<Vec<Permission>>>,
}

/// Where a connection comes from, as the login limits count it: an address
/// in one of the joined networks (each its own number: EasyTier's address
/// ranges repeat from one network to the next, so one network's guesses
/// never lock out another's device), or network 0, this computer's own
/// networks, for connections by address.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Source {
    pub network: u64,
    pub ip: IpAddr,
}

#[derive(Default)]
struct Limits {
    fails: HashMap<Source, VecDeque<Instant>>,
    blocked: HashMap<Source, Instant>,
    global: VecDeque<Instant>,
    global_until: Option<Instant>,
}

/// The host's access state, shared by the core and every session task.
pub struct Access {
    secrets: Arc<Secrets>,
    pub slots: Mutex<Slots>,
    limits: Mutex<Limits>,
    pending: Mutex<(u64, Vec<Pending>)>,
    /// Resume tokens issued to accepted controllers, in memory only.
    resumes: Mutex<HashMap<String, Resume>>,
    serve: Mutex<Serve>,
    /// The connection history while it is kept in memory only (No logs),
    /// by store name; None keeps it in the secrets store.
    memory: Mutex<Option<HashMap<&'static str, zeroize::Zeroizing<String>>>>,
}

impl Access {
    /// Loads the password slots and starts with no failures counted.
    pub fn load(secrets: Arc<Secrets>) -> Result<Self, String> {
        let slots = Slots::load(&secrets)?;
        let memory = secrets.get(NO_HISTORY_NAME).map(|_| HashMap::new());
        Ok(Access {
            secrets,
            slots: Mutex::new(slots),
            limits: Mutex::default(),
            pending: Mutex::new((0, Vec::new())),
            resumes: Mutex::default(),
            serve: Mutex::default(),
            memory: Mutex::new(memory),
        })
    }

    /// Whether the host is in protected mode.
    pub fn protected(&self) -> bool {
        self.secrets.get(PROTECTED_NAME).is_some()
    }

    /// Turns protected mode on (a permanent password must be set) or off
    /// (with the permanent password).
    /// Side effects: rewrites the secrets store.
    pub fn set_protected(&self, on: bool, password: &str, host_key: &[u8]) -> Result<(), String> {
        let slots = self.slots.lock().map_err(|_| "access state poisoned")?;
        if on {
            if !slots.has_permanent() {
                return Err("protected mode needs a permanent password".into());
            }
            return self.secrets.set(PROTECTED_NAME, "true");
        }
        if !slots.check_permanent(password, host_key) {
            return Err("that is not the permanent password".into());
        }
        self.secrets.remove(PROTECTED_NAME)
    }

    /// Whether the connection history is written to the store (off with
    /// No logs).
    pub fn keeps_history(&self) -> bool {
        self.memory.lock().map(|m| m.is_none()).unwrap_or(true)
    }

    /// Keeps the connection history (trusted devices, known hosts) in the
    /// store, or in memory only, where it is gone when the engine stops.
    /// Side effects: moves the history out of the store (dropping it from
    /// there) or back into it.
    pub fn keep_history(&self, keep: bool) -> Result<(), String> {
        let mut memory = self.memory.lock().map_err(|_| "access state poisoned")?;
        match (keep, memory.take()) {
            (false, None) => {
                let mut held = HashMap::new();
                for name in HISTORY {
                    if let Some(text) = self.secrets.get(name) {
                        held.insert(name, text);
                    }
                    self.secrets.remove(name)?;
                }
                self.secrets.set(NO_HISTORY_NAME, "true")?;
                *memory = Some(held);
            }
            (true, Some(held)) => {
                for (name, text) in held {
                    self.secrets.set(name, &text)?;
                }
                self.secrets.remove(NO_HISTORY_NAME)?;
            }
            (_, unchanged) => *memory = unchanged,
        }
        Ok(())
    }

    /// A history entry, from memory while No logs is on.
    fn history(&self, name: &'static str) -> Option<zeroize::Zeroizing<String>> {
        match self.memory.lock().ok()?.as_ref() {
            Some(held) => held.get(name).cloned(),
            None => self.secrets.get(name),
        }
    }

    /// Stores a history entry, in memory only while No logs is on.
    fn store_history<T: Serialize>(&self, name: &'static str, value: &T) -> Result<(), String> {
        let text =
            zeroize::Zeroizing::new(serde_json::to_string(value).map_err(|e| e.to_string())?);
        let mut memory = self.memory.lock().map_err(|_| "access state poisoned")?;
        match memory.as_mut() {
            Some(held) => {
                held.insert(name, text);
                Ok(())
            }
            None => self.secrets.set(name, &text),
        }
    }

    /// The secrets store the slots persist into.
    pub fn secrets(&self) -> &Secrets {
        &self.secrets
    }

    /// Whom the host serves now.
    pub fn serve(&self) -> Serve {
        self.serve.lock().map(|s| *s).unwrap_or(Serve::None)
    }

    /// Sets whom the host serves; open confirmations are withdrawn when
    /// nobody can answer them any more.
    pub fn set_serve(&self, s: Serve) {
        if let Ok(mut cur) = self.serve.lock() {
            *cur = s;
        }
        if s != Serve::All
            && let Ok(mut p) = self.pending.lock()
        {
            p.1.clear(); // dropping the replies denies them
        }
    }

    /// Which passwords open this host.
    pub fn password_mode(&self) -> PasswordMode {
        self.secrets
            .get(PASSWORD_MODE_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    /// Sets which passwords open this host.
    /// Side effects: rewrites the secrets store.
    pub fn set_password_mode(&self, mode: PasswordMode) -> Result<(), String> {
        self.store(PASSWORD_MODE_NAME, &mode)
    }

    /// Whether an accepted password admits without the host user's
    /// confirmation (on unless the host user turned it off).
    pub fn without_asking(&self) -> bool {
        self.secrets
            .get(WITHOUT_ASKING_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or(true)
    }

    /// Sets whether an accepted password admits without confirmation.
    /// Side effects: rewrites the secrets store.
    pub fn set_without_asking(&self, on: bool) -> Result<(), String> {
        self.store(WITHOUT_ASKING_NAME, &on)
    }

    /// Whether and from where this host accepts sessions by address (off
    /// unless the host user turned it on).
    pub fn by_address(&self) -> ByAddress {
        self.secrets
            .get(BY_ADDRESS_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    /// Sets whether and from where this host accepts sessions by address.
    /// Side effects: rewrites the secrets store.
    pub fn set_by_address(&self, mut by: ByAddress) -> Result<(), String> {
        by.ranges = by
            .ranges
            .iter()
            .map(|r| r.trim().to_owned())
            .filter(|r| !r.is_empty())
            .collect();
        by.check()?;
        self.store(BY_ADDRESS_NAME, &by)
    }

    /// What the permanent password admits with nobody at the computer
    /// (unattended): what a password grants, within `want`; None when
    /// Connect without asking is off, for a new key claiming a trusted
    /// Device ID, or when nothing wanted is a password grant.
    pub fn unattended(
        &self,
        key: &str,
        device_id: &str,
        want: &[Permission],
    ) -> Option<Vec<Permission>> {
        if !self.without_asking() || self.claims_other_key(key, device_id) {
            return None;
        }
        let grants = self.password_grants();
        let granted: Vec<Permission> = want
            .iter()
            .copied()
            .filter(|p| grants.contains(p))
            .collect();
        (!granted.is_empty()).then_some(granted)
    }

    /// The permissions a correct password grants without asking.
    pub fn password_grants(&self) -> Vec<Permission> {
        self.secrets
            .get(PASSWORD_GRANTS_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_else(|| DEFAULT_PASSWORD_GRANTS.to_vec())
    }

    /// Sets what a correct password grants without asking.
    /// Side effects: rewrites the secrets store.
    pub fn set_password_grants(&self, mut grants: Vec<Permission>) -> Result<(), String> {
        grants.sort();
        grants.dedup();
        self.store(PASSWORD_GRANTS_NAME, &grants)
    }

    /// Whether a controller that proved a password is admitted without
    /// asking: Connect without asking is on, everything it wants is a
    /// password grant, and its key is not a new one claiming a Device ID
    /// the host trusts under another key.
    pub fn admits_by_password(&self, key: &str, device_id: &str, want: &[Permission]) -> bool {
        let grants = self.password_grants();
        self.without_asking()
            && want.iter().all(|p| grants.contains(p))
            && !self.claims_other_key(key, device_id)
    }

    /// Whether `key` is new and its Device ID is trusted under another key
    /// (the interface says "a new key claiming this ID").
    fn claims_other_key(&self, key: &str, device_id: &str) -> bool {
        let trusted = self.trusted();
        !trusted.contains_key(key) && trusted.values().any(|t| t.device_id == device_id)
    }

    /// Refuses a source that is blocked, and unpinned controllers while the
    /// global backoff runs.
    pub fn admit(&self, from: Source, pinned: bool) -> Result<(), Refusal> {
        let l = self.limits.lock().map_err(|_| Refusal::Busy)?;
        let now = Instant::now();
        if l.blocked.get(&from).is_some_and(|until| *until > now) {
            return Err(Refusal::RateLimited);
        }
        if !pinned && l.global_until.is_some_and(|until| until > now) {
            return Err(Refusal::RateLimited);
        }
        Ok(())
    }

    /// Counts one failed login from a source.
    /// Side effects: may block the source or start the global backoff.
    pub fn failed(&self, from: Source) {
        let Ok(mut l) = self.limits.lock() else {
            return;
        };
        let now = Instant::now();
        let list = l.fails.entry(from).or_default();
        list.push_back(now);
        while list.front().is_some_and(|t| now - *t > FAIL_WINDOW) {
            list.pop_front();
        }
        if list.len() >= FAILS_PER_ADDRESS {
            list.clear();
            l.blocked.insert(from, now + ADDRESS_BLOCK);
        }
        l.global.push_back(now);
        while l
            .global
            .front()
            .is_some_and(|t| now - *t > Duration::from_secs(60))
        {
            l.global.pop_front();
        }
        if l.global.len() > GLOBAL_FAILS {
            l.global_until = Some(now + GLOBAL_WAIT);
        }
    }

    /// Opens a confirmation for the host's user.
    /// Inputs: `password` says whether the controller proved a password or a
    /// key; `key_name` names the key it proved.
    /// Returns: the receiver of the decision (granted permissions, or None
    /// for a denial), or `Busy` when too many are open.
    pub fn ask(
        &self,
        key: &str,
        device_id: &str,
        want: Vec<Permission>,
        about: &About,
        password: bool,
        key_name: Option<&str>,
    ) -> Result<(u64, Decision), Refusal> {
        let known = self.trusted().contains_key(key);
        let new_key = self.claims_other_key(key, device_id);
        let mut p = self.pending.lock().map_err(|_| Refusal::Busy)?;
        p.1.retain(|q| !q.reply.is_closed());
        if p.1.len() >= MAX_PENDING || p.1.iter().any(|q| q.key == key) {
            return Err(Refusal::Busy);
        }
        p.0 += 1;
        let id = p.0;
        let (reply, rx) = oneshot::channel();
        p.1.push(Pending {
            id,
            key: key.to_owned(),
            device_id: device_id.to_owned(),
            known,
            new_key,
            password,
            want,
            about: about.clone(),
            key_name: key_name.map(String::from),
            reply,
        });
        Ok((id, rx))
    }

    /// Closes a confirmation the session gave up on (timeout, disconnect).
    pub fn withdraw(&self, id: u64) {
        if let Ok(mut p) = self.pending.lock() {
            p.1.retain(|q| q.id != id);
        }
    }

    /// Whether a controller waits for the host user's answer: while one
    /// does, no session's input reaches Kyokai's own windows, so nobody
    /// answers it in the host user's place.
    pub fn asking(&self) -> bool {
        self.pending
            .lock()
            .is_ok_and(|p| p.1.iter().any(|q| !q.reply.is_closed()))
    }

    /// The open confirmations, for the host's interface.
    pub fn pending(&self) -> Value {
        let Ok(mut p) = self.pending.lock() else {
            return json!([]);
        };
        p.1.retain(|q| !q.reply.is_closed());
        p.1.iter()
            .map(|q| {
                json!({
                    "id": q.id,
                    "device_id": q.device_id,
                    "known": q.known,
                    "new_key": q.new_key,
                    "password": q.password,
                    "want": q.want,
                    "about": q.about,
                    "key": q.key_name,
                })
            })
            .collect()
    }

    /// Answers a confirmation: `granted` None denies; the granted set is cut
    /// down to what was asked for.
    pub fn decide(&self, id: u64, granted: Option<Vec<Permission>>) -> Result<(), String> {
        let mut p = self.pending.lock().map_err(|_| "access state poisoned")?;
        let i =
            p.1.iter()
                .position(|q| q.id == id)
                .ok_or("that request is no longer open")?;
        let q = p.1.remove(i);
        let granted = granted.map(|g| {
            let mut g: Vec<_> = g.into_iter().filter(|x| q.want.contains(x)).collect();
            g.sort();
            g.dedup();
            g
        });
        q.reply
            .send(granted)
            .map_err(|_| "that request is no longer open".to_string())
    }

    /// The trusted devices, keyed by base64 device key.
    pub fn trusted(&self) -> BTreeMap<String, Trusted> {
        self.history(TRUSTED_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    /// Records an accepted controller.
    /// Side effects: rewrites the secrets store.
    pub fn trust(
        &self,
        key: &str,
        device_id: &str,
        granted: &[Permission],
        about: &About,
    ) -> Result<(), String> {
        let now = unix_now();
        let mut all = self.trusted();
        let entry = all.entry(key.to_owned()).or_insert(Trusted {
            device_id: device_id.to_owned(),
            first: now,
            last: now,
            granted: Vec::new(),
            about: About::default(),
        });
        entry.about = about.clone();
        entry.last = now;
        entry.device_id = device_id.to_owned();
        entry.granted = granted.to_vec();
        self.store_history(TRUSTED_NAME, &all)
    }

    /// Forgets a trusted device.
    pub fn untrust(&self, key: &str) -> Result<(), String> {
        let mut all = self.trusted();
        all.remove(key).ok_or("not a trusted device")?;
        // A revoked device cannot slip back in through a resume.
        if let Ok(mut r) = self.resumes.lock() {
            r.retain(|_, v| v.key != key);
        }
        self.store_history(TRUSTED_NAME, &all)
    }

    /// Issues a single-use token that lets this controller key resume a
    /// dropped session with the same permissions (host side); `session` is
    /// the session it was issued in.
    pub fn issue_resume(&self, key: &str, granted: &[Permission], session: u64) -> String {
        let mut bytes = [0u8; 32];
        let _ = getrandom::fill(&mut bytes);
        let token = base64::Engine::encode(&base64::engine::general_purpose::STANDARD, bytes);
        if let Ok(mut r) = self.resumes.lock() {
            let now = Instant::now();
            r.retain(|_, v| v.until > now);
            if r.len() >= MAX_RESUMES
                && let Some(oldest) = r
                    .iter()
                    .min_by_key(|(_, v)| v.until)
                    .map(|(k, _)| k.clone())
            {
                r.remove(&oldest);
            }
            r.insert(
                token.clone(),
                Resume {
                    key: key.to_owned(),
                    granted: granted.to_vec(),
                    until: now + RESUME_WINDOW,
                    session,
                },
            );
        }
        token
    }

    /// Redeems a resume token for the controller key that holds it.
    /// Returns: the permissions to grant again and the session it resumes;
    /// None for an unknown, used, expired or foreign token.
    pub fn take_resume(&self, token: &str, key: &str) -> Option<(Vec<Permission>, u64)> {
        let r = self.resumes.lock().ok()?.remove(token)?;
        (r.key == key && r.until > Instant::now()).then_some((r.granted, r.session))
    }

    /// The host key pinned for a Device ID, if any (controller side).
    pub fn known_host(&self, device_id: &str) -> Option<String> {
        self.known_hosts().remove(device_id).map(|h| h.key)
    }

    /// Keeps a host's password for later connections (controller side), in
    /// the encrypted secrets store like every other secret.
    pub fn save_password(&self, device_id: &str, password: &str) -> Result<(), String> {
        self.secrets
            .set(&format!("{SAVED_PASSWORD_PREFIX}{device_id}"), password)
    }

    /// The password kept for a host, if any.
    pub fn saved_password(&self, device_id: &str) -> Option<zeroize::Zeroizing<String>> {
        self.secrets
            .get(&format!("{SAVED_PASSWORD_PREFIX}{device_id}"))
    }

    /// Forgets the password kept for a host.
    pub fn forget_password(&self, device_id: &str) -> Result<(), String> {
        self.secrets
            .remove(&format!("{SAVED_PASSWORD_PREFIX}{device_id}"))
    }

    /// What this device keeps for signing in to a host besides its password
    /// (controller side).
    pub fn saved(&self, device_id: &str) -> Saved {
        self.secrets
            .get(&format!("{SAVED_PREFIX}{device_id}"))
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    /// Keeps what this device saves for a host; an empty entry is removed.
    /// Side effects: rewrites the secrets store.
    pub fn set_saved(&self, device_id: &str, saved: &Saved) -> Result<(), String> {
        let name = format!("{SAVED_PREFIX}{device_id}");
        if saved.is_empty() {
            return self.secrets.remove(&name);
        }
        self.store(&name, saved)
    }

    /// Remembers the login key a host admitted, or forgets the one
    /// remembered (None) when the host admitted another way.
    pub fn remember_key(&self, device_id: &str, key: Option<&str>) -> Result<(), String> {
        let mut saved = self.saved(device_id);
        if saved.key.as_deref() == key {
            return Ok(());
        }
        saved.key = key.map(String::from);
        self.set_saved(device_id, &saved)
    }

    /// Forgets everything kept for signing in to a host: its password and
    /// its saved entry.
    pub fn forget_saved(&self, device_id: &str) -> Result<(), String> {
        self.forget_password(device_id)?;
        self.secrets.remove(&format!("{SAVED_PREFIX}{device_id}"))
    }

    /// Pins a host key for its Device ID, or refreshes its last connection
    /// (controller side).
    /// Inputs: `address`, the address dialed, or empty over the network;
    /// `features`, what the host announced in this session.
    pub fn pin_host(
        &self,
        device_id: &str,
        key: &str,
        about: &About,
        address: &str,
        features: &[String],
    ) -> Result<(), String> {
        let mut all = self.known_hosts();
        all.insert(
            device_id.to_owned(),
            KnownHost {
                key: key.to_owned(),
                last: unix_now(),
                about: about.clone(),
                address: address.to_owned(),
                features: features.to_vec(),
            },
        );
        self.store_history(KNOWN_HOSTS_NAME, &all)
    }

    /// The hosts connected to most recently, newest first (controller side).
    pub fn recent(&self) -> Vec<(String, KnownHost)> {
        let mut all: Vec<_> = self.known_hosts().into_iter().collect();
        all.sort_by_key(|(_, h)| std::cmp::Reverse(h.last));
        all.truncate(MAX_RECENT);
        all
    }

    /// Forgets a pinned host key, so the next session pins anew.
    pub fn forget_host(&self, device_id: &str) -> Result<(), String> {
        let mut all = self.known_hosts();
        all.remove(device_id).ok_or("not a known host")?;
        self.store_history(KNOWN_HOSTS_NAME, &all)
    }

    fn known_hosts(&self) -> BTreeMap<String, KnownHost> {
        self.history(KNOWN_HOSTS_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    fn store<T: Serialize>(&self, name: &str, value: &T) -> Result<(), String> {
        let text =
            zeroize::Zeroizing::new(serde_json::to_string(value).map_err(|e| e.to_string())?);
        self.secrets.set(name, &text)
    }
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn access() -> Access {
        Access::load(Arc::new(Secrets::memory())).unwrap()
    }

    #[test]
    fn an_address_is_blocked_after_five_failures_and_pinned_keys_skip_the_backoff() {
        let a = access();
        let at = |network, ip: &str| Source {
            network,
            ip: ip.parse().unwrap(),
        };
        let ip = at(7, "10.126.126.9");
        for _ in 0..FAILS_PER_ADDRESS - 1 {
            a.failed(ip);
        }
        assert!(a.admit(ip, false).is_ok());
        a.failed(ip);
        assert_eq!(a.admit(ip, false), Err(Refusal::RateLimited));
        assert_eq!(
            a.admit(ip, true),
            Err(Refusal::RateLimited),
            "the address block holds for everyone"
        );
        // The same address in another network, or by address, is another
        // device: its guesses are its own.
        assert!(a.admit(at(8, "10.126.126.9"), false).is_ok());
        assert!(a.admit(at(0, "10.126.126.9"), false).is_ok());
        for i in 0..=GLOBAL_FAILS {
            a.failed(Source {
                network: 7,
                ip: IpAddr::from([10, 0, 1, (i % 250) as u8]),
            });
        }
        let other = at(7, "10.126.126.77");
        assert_eq!(a.admit(other, false), Err(Refusal::RateLimited));
        assert!(
            a.admit(other, true).is_ok(),
            "pinned keys are exempt from the global backoff"
        );
    }

    #[test]
    fn confirmations_grant_only_what_was_asked_and_flag_new_keys() {
        let a = access();
        a.trust(
            "key-A",
            "100000002",
            &[Permission::ViewScreen],
            &About::default(),
        )
        .unwrap();
        let (id, mut rx) = a
            .ask(
                "key-B",
                "100000002",
                vec![Permission::ViewScreen, Permission::ControlInput],
                &About::default(),
                false,
                Some("laptop"),
            )
            .unwrap();
        // While a controller waits for the host user, nobody's input reaches
        // Kyokai's windows.
        assert!(a.asking());
        let open = a.pending();
        assert_eq!(open[0]["new_key"], true);
        assert_eq!(open[0]["known"], false);
        assert_eq!(open[0]["password"], false);
        assert_eq!(open[0]["key"], "laptop", "the key it proved, by name");
        assert_eq!(open[0]["key"], "laptop", "the key it proved, by name");
        assert_eq!(
            a.ask("key-B", "100000002", vec![], &About::default(), true, None)
                .err(),
            Some(Refusal::Busy)
        );
        a.decide(
            id,
            Some(vec![Permission::ControlInput, Permission::RunAiTask]),
        )
        .unwrap();
        assert_eq!(rx.try_recv().unwrap(), Some(vec![Permission::ControlInput]));
        assert!(a.decide(id, None).is_err(), "answered once");
        assert!(!a.asking(), "answered");
    }

    #[test]
    fn a_password_admits_within_its_grants_and_never_a_key_claiming_a_trusted_id() {
        use Permission::*;
        let a = access();
        type Case = (&'static str, &'static str, &'static [Permission], bool);
        let cases: &[Case] = &[
            (
                "key-A",
                "100000002",
                &[ViewScreen, ControlInput, Clipboard, Files],
                true,
            ),
            ("key-A", "100000002", &[ViewScreen, RunAiTask], false),
            ("key-A", "100000002", &[], true),
        ];
        for (key, id, want, expected) in cases {
            assert_eq!(a.admits_by_password(key, id, want), *expected, "{want:?}");
        }
        a.trust("key-A", "100000002", &[ViewScreen], &About::default())
            .unwrap();
        assert!(a.admits_by_password("key-A", "100000002", &[ViewScreen]));
        assert!(
            !a.admits_by_password("key-B", "100000002", &[ViewScreen]),
            "a new key claiming a trusted Device ID always asks"
        );
        // Sound is granted by a correct password unless the host's user
        // takes it out, and is its own permission.
        assert!(a.password_grants().contains(&HearSound));
        assert!(a.admits_by_password("key-A", "100000002", &[ViewScreen, HearSound]));
        assert_eq!(serde_json::to_value(HearSound).unwrap(), "hear-sound");
        a.set_password_grants(vec![RunAiTask, ViewScreen, ViewScreen])
            .unwrap();
        assert_eq!(a.password_grants(), vec![ViewScreen, RunAiTask]);
        assert!(!a.admits_by_password("key-A", "100000002", &[ControlInput]));
        a.set_password_grants(Vec::new()).unwrap();
        assert!(
            a.admits_by_password("key-A", "100000002", &[]),
            "an empty set still admits a login that asks for nothing"
        );
        assert!(!a.admits_by_password("key-A", "100000002", &[ViewScreen]));
    }

    #[test]
    fn resume_tokens_are_single_use_and_bound_to_their_key() {
        let a = access();
        let t = a.issue_resume("key-A", &[Permission::ViewScreen], 7);
        assert_eq!(a.take_resume(&t, "key-B"), None, "another key");
        let t = a.issue_resume("key-A", &[Permission::ViewScreen], 7);
        // The token names the session it resumes, which the new one ends.
        assert_eq!(
            a.take_resume(&t, "key-A"),
            Some((vec![Permission::ViewScreen], 7))
        );
        assert_eq!(a.take_resume(&t, "key-A"), None, "used once");
    }

    /// Forgetting what is saved for a host forgets its password and its
    /// saved entry together; other hosts keep theirs.
    #[test]
    fn forgetting_a_host_forgets_everything_saved_for_it() {
        let a = access();
        for id in ["100000002", "100000003"] {
            a.save_password(id, "long-lived-pw").unwrap();
            a.remember_key(id, Some("0123456789abcdef")).unwrap();
        }
        a.forget_saved("100000002").unwrap();
        assert!(a.saved_password("100000002").is_none());
        assert_eq!(a.saved("100000002").key, None);
        assert!(a.saved_password("100000003").is_some());
        assert!(a.saved("100000003").key.is_some());
        // An entry left empty is not kept at all.
        a.remember_key("100000003", None).unwrap();
        assert!(a.secrets().get("saved.100000003").is_none());
    }

    #[test]
    fn pinned_hosts_persist_and_can_be_forgotten() {
        let a = access();
        assert_eq!(a.known_host("100000002"), None);
        a.pin_host("100000002", "key-H", &About::default(), "", &[])
            .unwrap();
        assert_eq!(a.known_host("100000002").as_deref(), Some("key-H"));
        a.forget_host("100000002").unwrap();
        assert_eq!(a.known_host("100000002"), None);
    }

    /// Connections by address are accepted only while on, and only from
    /// the listed ranges.
    #[test]
    fn by_address_admits_listed_ranges() {
        let on = ByAddress {
            on: true,
            ..ByAddress::default()
        };
        for (ip, ok) in [
            ("192.168.1.20", true),
            ("10.1.2.3", true),
            ("172.31.255.1", true),
            ("172.32.0.1", false),
            ("100.101.102.103", true),
            ("100.128.0.1", false),
            ("8.8.8.8", false),
            ("127.0.0.1", false),
            ("::ffff:192.168.1.20", true),
            ("fe80::1", true),
            ("fd7a:115c:a1e0::1", true),
            ("2001:db8::1", false),
        ] {
            let ip: IpAddr = ip.parse().unwrap();
            assert_eq!(on.admits(ip), ok, "{ip}");
        }
        assert!(!ByAddress::default().admits("192.168.1.20".parse().unwrap()));
        let one = ByAddress {
            on: true,
            ranges: vec!["127.0.0.1".into(), "0.0.0.0/0".into()],
        };
        assert!(one.admits("127.0.0.1".parse().unwrap()));
        assert!(one.admits("8.8.8.8".parse().unwrap()));
        for (range, ok) in [
            ("192.168.0.0/16", true),
            ("::/0", true),
            ("192.168.0.0/33", false),
            ("192.168.0/16", false),
            ("example.org", false),
            ("", false),
        ] {
            let by = ByAddress {
                on: true,
                ranges: vec![range.into()],
            };
            assert_eq!(by.check().is_ok(), ok, "{range:?}");
        }
    }

    /// With No logs the connection history lives in memory only: none of
    /// it is in the store, it stays off after a restart, and turning it
    /// back on writes this run's history.
    #[test]
    fn no_logs_keeps_the_history_in_memory() {
        let secrets = Arc::new(Secrets::memory());
        let a = Access::load(secrets.clone()).unwrap();
        a.pin_host("100000002", "key-H", &About::default(), "", &[])
            .unwrap();
        a.trust(
            "key-C",
            "100000003",
            &[Permission::ViewScreen],
            &About::default(),
        )
        .unwrap();
        a.keep_history(false).unwrap();
        assert!(!a.keeps_history());
        assert!(secrets.get(KNOWN_HOSTS_NAME).is_none());
        assert!(secrets.get(TRUSTED_NAME).is_none());
        // Still known for this run, and new connections stay in memory.
        assert_eq!(a.known_host("100000002").as_deref(), Some("key-H"));
        a.pin_host("100000004", "key-J", &About::default(), "", &[])
            .unwrap();
        assert!(secrets.get(KNOWN_HOSTS_NAME).is_none());
        assert_eq!(a.recent().len(), 2);
        // A restart remembers the choice, not the history.
        let again = Access::load(secrets.clone()).unwrap();
        assert!(!again.keeps_history());
        assert!(again.recent().is_empty() && again.trusted().is_empty());
        // Back on: this run's history is written.
        a.keep_history(true).unwrap();
        assert!(a.keeps_history());
        assert_eq!(Access::load(secrets).unwrap().recent().len(), 2);
    }

    /// Protected mode needs a permanent password to turn on, and only the
    /// permanent password (not the temporary one) turns it off.
    #[test]
    fn protected_mode_needs_the_permanent_password() {
        let secrets = Arc::new(Secrets::memory());
        let a = Access::load(secrets).unwrap();
        let key = [7u8; 32];
        assert!(
            a.set_protected(true, "", &key).is_err(),
            "no permanent password"
        );
        {
            let mut slots = a.slots.lock().unwrap();
            slots.set_temporary("temp-pass", &key).unwrap();
            slots
                .set_permanent(a.secrets(), Some("long-lived-pw"), &key)
                .unwrap();
        }
        a.set_protected(true, "", &key).unwrap();
        assert!(a.protected());
        for wrong in ["", "temp-pass", "long-lived-pX"] {
            assert!(a.set_protected(false, wrong, &key).is_err(), "{wrong:?}");
            assert!(a.protected());
        }
        a.set_protected(false, "long-lived-pw", &key).unwrap();
        assert!(!a.protected());
    }
}
