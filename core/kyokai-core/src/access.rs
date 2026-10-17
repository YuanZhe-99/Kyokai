//! Who may connect to this host and how the host decides
//! (doc/en-us/identity-and-access.md): password slots, rate limits, the
//! host user's confirmations, trusted devices (pinned controller keys) and
//! known hosts (pinned host keys on the controller side).

use crate::auth::Slots;
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
}

const TRUSTED_NAME: &str = "trusted_devices";
const KNOWN_HOSTS_NAME: &str = "known_hosts";
/// Failed logins one source address may make in `FAIL_WINDOW`.
const FAILS_PER_ADDRESS: usize = 5;
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

/// A controller key the host has accepted before.
#[derive(Clone, Serialize, Deserialize)]
pub struct Trusted {
    pub device_id: String,
    /// Unix seconds.
    pub first: u64,
    pub last: u64,
    pub granted: Vec<Permission>,
}

/// The host user's answer to a confirmation: the granted permissions, or
/// None for a denial.
pub type Decision = oneshot::Receiver<Option<Vec<Permission>>>;

/// A confirmation waiting for the host's user.
struct Pending {
    id: u64,
    key: String,
    device_id: String,
    known: bool,
    new_key: bool,
    want: Vec<Permission>,
    reply: oneshot::Sender<Option<Vec<Permission>>>,
}

#[derive(Default)]
struct Limits {
    fails: HashMap<IpAddr, VecDeque<Instant>>,
    blocked: HashMap<IpAddr, Instant>,
    global: VecDeque<Instant>,
    global_until: Option<Instant>,
}

/// The host's access state, shared by the core and every session task.
pub struct Access {
    secrets: Arc<Secrets>,
    pub slots: Mutex<Slots>,
    limits: Mutex<Limits>,
    pending: Mutex<(u64, Vec<Pending>)>,
}

impl Access {
    /// Loads the password slots and starts with no failures counted.
    pub fn load(secrets: Arc<Secrets>) -> Result<Self, String> {
        let slots = Slots::load(&secrets)?;
        Ok(Access {
            secrets,
            slots: Mutex::new(slots),
            limits: Mutex::default(),
            pending: Mutex::new((0, Vec::new())),
        })
    }

    /// The secrets store the slots persist into.
    pub fn secrets(&self) -> &Secrets {
        &self.secrets
    }

    /// Refuses an address that is blocked, and unpinned controllers while
    /// the global backoff runs.
    pub fn admit(&self, from: IpAddr, pinned: bool) -> Result<(), Refusal> {
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

    /// Counts one failed login from an address.
    /// Side effects: may block the address or start the global backoff.
    pub fn failed(&self, from: IpAddr) {
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
    /// Returns: the receiver of the decision (granted permissions, or None
    /// for a denial), or `Busy` when too many are open.
    pub fn ask(
        &self,
        key: &str,
        device_id: &str,
        want: Vec<Permission>,
    ) -> Result<(u64, Decision), Refusal> {
        let trusted = self.trusted();
        let known = trusted.contains_key(key);
        let new_key = !known && trusted.values().any(|t| t.device_id == device_id);
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
            want,
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
                    "want": q.want,
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
        self.secrets
            .get(TRUSTED_NAME)
            .and_then(|t| serde_json::from_str(&t).ok())
            .unwrap_or_default()
    }

    /// Records an accepted controller.
    /// Side effects: rewrites the secrets store.
    pub fn trust(&self, key: &str, device_id: &str, granted: &[Permission]) -> Result<(), String> {
        let now = unix_now();
        let mut all = self.trusted();
        let entry = all.entry(key.to_owned()).or_insert(Trusted {
            device_id: device_id.to_owned(),
            first: now,
            last: now,
            granted: Vec::new(),
        });
        entry.last = now;
        entry.device_id = device_id.to_owned();
        entry.granted = granted.to_vec();
        self.store(TRUSTED_NAME, &all)
    }

    /// Forgets a trusted device.
    pub fn untrust(&self, key: &str) -> Result<(), String> {
        let mut all = self.trusted();
        all.remove(key).ok_or("not a trusted device")?;
        self.store(TRUSTED_NAME, &all)
    }

    /// The host key pinned for a Device ID, if any (controller side).
    pub fn known_host(&self, device_id: &str) -> Option<String> {
        self.known_hosts().remove(device_id)
    }

    /// Pins a host key for its Device ID (controller side).
    pub fn pin_host(&self, device_id: &str, key: &str) -> Result<(), String> {
        let mut all = self.known_hosts();
        all.insert(device_id.to_owned(), key.to_owned());
        self.store(KNOWN_HOSTS_NAME, &all)
    }

    /// Forgets a pinned host key, so the next session pins anew.
    pub fn forget_host(&self, device_id: &str) -> Result<(), String> {
        let mut all = self.known_hosts();
        all.remove(device_id).ok_or("not a known host")?;
        self.store(KNOWN_HOSTS_NAME, &all)
    }

    fn known_hosts(&self) -> BTreeMap<String, String> {
        self.secrets
            .get(KNOWN_HOSTS_NAME)
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
        let ip: IpAddr = "10.126.126.9".parse().unwrap();
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
        for i in 0..=GLOBAL_FAILS {
            a.failed(IpAddr::from([10, 0, 1, (i % 250) as u8]));
        }
        let other: IpAddr = "10.126.126.77".parse().unwrap();
        assert_eq!(a.admit(other, false), Err(Refusal::RateLimited));
        assert!(
            a.admit(other, true).is_ok(),
            "pinned keys are exempt from the global backoff"
        );
    }

    #[test]
    fn confirmations_grant_only_what_was_asked_and_flag_new_keys() {
        let a = access();
        a.trust("key-A", "100000002", &[Permission::ViewScreen])
            .unwrap();
        let (id, mut rx) = a
            .ask(
                "key-B",
                "100000002",
                vec![Permission::ViewScreen, Permission::ControlInput],
            )
            .unwrap();
        let open = a.pending();
        assert_eq!(open[0]["new_key"], true);
        assert_eq!(open[0]["known"], false);
        assert_eq!(
            a.ask("key-B", "100000002", vec![]).err(),
            Some(Refusal::Busy)
        );
        a.decide(
            id,
            Some(vec![Permission::ControlInput, Permission::RunAiTask]),
        )
        .unwrap();
        assert_eq!(rx.try_recv().unwrap(), Some(vec![Permission::ControlInput]));
        assert!(a.decide(id, None).is_err(), "answered once");
    }

    #[test]
    fn pinned_hosts_persist_and_can_be_forgotten() {
        let a = access();
        assert_eq!(a.known_host("100000002"), None);
        a.pin_host("100000002", "key-H").unwrap();
        assert_eq!(a.known_host("100000002").as_deref(), Some("key-H"));
        a.forget_host("100000002").unwrap();
        assert_eq!(a.known_host("100000002"), None);
    }
}
