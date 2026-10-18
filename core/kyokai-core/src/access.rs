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
const PASSWORD_GRANTS_NAME: &str = "password_grants";
const PASSWORD_MODE_NAME: &str = "password_mode";
const WITHOUT_ASKING_NAME: &str = "connect_without_asking";

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
}

/// A resume token's grant: who may resume, with what, until when.
struct Resume {
    key: String,
    granted: Vec<Permission>,
    until: Instant,
}

/// A confirmation waiting for the host's user.
struct Pending {
    id: u64,
    key: String,
    device_id: String,
    known: bool,
    new_key: bool,
    /// Whether the controller proved a password (false: it only asks).
    password: bool,
    want: Vec<Permission>,
    about: About,
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
    /// Resume tokens issued to accepted controllers, in memory only.
    resumes: Mutex<HashMap<String, Resume>>,
    serve: Mutex<Serve>,
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
            resumes: Mutex::default(),
            serve: Mutex::default(),
        })
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
    /// Inputs: `password` says whether the controller proved a password.
    /// Returns: the receiver of the decision (granted permissions, or None
    /// for a denial), or `Busy` when too many are open.
    pub fn ask(
        &self,
        key: &str,
        device_id: &str,
        want: Vec<Permission>,
        about: &About,
        password: bool,
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
                    "password": q.password,
                    "want": q.want,
                    "about": q.about,
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
        self.store(TRUSTED_NAME, &all)
    }

    /// Forgets a trusted device.
    pub fn untrust(&self, key: &str) -> Result<(), String> {
        let mut all = self.trusted();
        all.remove(key).ok_or("not a trusted device")?;
        // A revoked device cannot slip back in through a resume.
        if let Ok(mut r) = self.resumes.lock() {
            r.retain(|_, v| v.key != key);
        }
        self.store(TRUSTED_NAME, &all)
    }

    /// Issues a single-use token that lets this controller key resume a
    /// dropped session with the same permissions (host side).
    pub fn issue_resume(&self, key: &str, granted: &[Permission]) -> String {
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
                },
            );
        }
        token
    }

    /// Redeems a resume token for the controller key that holds it.
    /// Returns: the permissions to grant again; None for an unknown, used,
    /// expired or foreign token.
    pub fn take_resume(&self, token: &str, key: &str) -> Option<Vec<Permission>> {
        let r = self.resumes.lock().ok()?.remove(token)?;
        (r.key == key && r.until > Instant::now()).then_some(r.granted)
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

    /// Pins a host key for its Device ID, or refreshes its last connection
    /// (controller side).
    pub fn pin_host(&self, device_id: &str, key: &str, about: &About) -> Result<(), String> {
        let mut all = self.known_hosts();
        all.insert(
            device_id.to_owned(),
            KnownHost {
                key: key.to_owned(),
                last: unix_now(),
                about: about.clone(),
            },
        );
        self.store(KNOWN_HOSTS_NAME, &all)
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
        self.store(KNOWN_HOSTS_NAME, &all)
    }

    fn known_hosts(&self) -> BTreeMap<String, KnownHost> {
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
            )
            .unwrap();
        let open = a.pending();
        assert_eq!(open[0]["new_key"], true);
        assert_eq!(open[0]["known"], false);
        assert_eq!(open[0]["password"], false);
        assert_eq!(
            a.ask("key-B", "100000002", vec![], &About::default(), true)
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
        let t = a.issue_resume("key-A", &[Permission::ViewScreen]);
        assert_eq!(a.take_resume(&t, "key-B"), None, "another key");
        let t = a.issue_resume("key-A", &[Permission::ViewScreen]);
        assert_eq!(
            a.take_resume(&t, "key-A"),
            Some(vec![Permission::ViewScreen])
        );
        assert_eq!(a.take_resume(&t, "key-A"), None, "used once");
    }

    #[test]
    fn pinned_hosts_persist_and_can_be_forgotten() {
        let a = access();
        assert_eq!(a.known_host("100000002"), None);
        a.pin_host("100000002", "key-H", &About::default()).unwrap();
        assert_eq!(a.known_host("100000002").as_deref(), Some("key-H"));
        a.forget_host("100000002").unwrap();
        assert_eq!(a.known_host("100000002"), None);
    }
}
