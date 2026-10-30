//! Quiet status watchers (doc/en-us/computer-use.md, "Host status"): the
//! host's side, which sends its samples to a watcher admitted for
//! `see-status`, and the controller's, which keeps the latest samples of up
//! to MAX_WATCHED hosts while its GUI holds a lease, in memory only.

use crate::access::{Permission, Refusal};
use crate::net::{Ask, Dialer};
use crate::session::{
    Failure, IDLE_LIMIT, Kind, MAX_FRAME, PING_EVERY, Remember, feature, read_frame, write_frame,
};
use crate::system::Sample;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt};
use tokio::sync::{Notify, mpsc};

/// Most watchers a host serves at once, besides its other sessions.
pub const MAX_WATCHERS: usize = 16;
/// Most hosts a controller watches at once.
const MAX_WATCHED: usize = 8;
/// How long a lease lasts: the GUI renews it while Home shows.
const LEASE: Duration = Duration::from_secs(30);
/// The first wait before watching a host again, doubled up to BACKOFF_MAX.
const BACKOFF_FIRST: Duration = Duration::from_secs(10);
const BACKOFF_MAX: Duration = Duration::from_secs(5 * 60);
/// The wait after a host refused as `rate-limited`.
const RATE_LIMITED_WAIT: Duration = Duration::from_secs(10 * 60);

/// A watch's messages: the session messages it uses and the host's status.
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
enum Message {
    Ping,
    Pong,
    Bye,
    /// host → watcher: the host's status, every SAMPLE_EVERY (SLOW_EVERY
    /// while every watcher asks for the slow pace).
    System(Box<Sample>),
    /// watcher → host: the pace it wants, slow while its Home is hidden;
    /// the usual one until it says. Hosts before 0.6.3 ignore it.
    Pace {
        slow: bool,
    },
    /// Anything else a peer sends: ignored.
    #[serde(other)]
    Unknown,
}

/// Encrypts one message and writes it; a peer that reads nothing for
/// IDLE_LIMIT is gone.
async fn send<W: AsyncWrite + Unpin>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    msg: &Message,
) -> Result<(), Failure> {
    let plain = serde_json::to_vec(msg).map_err(|_| Failure::Broken)?;
    let n = noise.write_message(&plain, buf)?;
    tokio::time::timeout(IDLE_LIMIT, write_frame(w, &buf[..n]))
        .await
        .map_err(|_| Failure::Broken)?
}

/// Reads whole frames on a task of its own, so a select that ends never
/// leaves one half read.
fn reader<R: AsyncRead + Unpin + Send + 'static>(
    mut rd: R,
) -> (mpsc::Receiver<Vec<u8>>, tokio::task::JoinHandle<()>) {
    let (tx, rx) = mpsc::channel(8);
    let task = tokio::spawn(async move {
        while let Ok(f) = read_frame(&mut rd).await {
            if tx.send(f).await.is_err() {
                break;
            }
        }
    });
    (rx, task)
}

/// Serves an admitted watcher until either side ends, as the host: a
/// `system` message with each sample (with what the watcher's password
/// grants without asking and whether the host's AI is ready) at the pace it
/// asks for, the heartbeat, and the end when `see-status` leaves the
/// password grants.
/// Notes: no capture, no input, no awake request.
pub async fn host<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    stream: S,
    mut noise: snow::TransportState,
    stop: &Notify,
    access: &crate::access::Access,
    ai: &crate::ai::Ai,
) -> Result<(), Failure> {
    let (rd, mut wr) = tokio::io::split(stream);
    let (mut frames, reading) = reader(rd);
    let mut status = crate::system::watch();
    // Which services answer, on the ports the host's user set: looked at
    // with the first sample and every PROBE_EVERY.
    let mut services = Vec::new();
    let mut probed: Option<Instant> = None;
    let mut ping = tokio::time::interval(PING_EVERY);
    let mut heard = Instant::now();
    let (mut buf, mut plain) = (vec![0u8; MAX_FRAME], vec![0u8; MAX_FRAME]);
    let outcome = async {
        loop {
            tokio::select! {
                _ = stop.notified() => {
                    send(&mut noise, &mut buf, &mut wr, &Message::Bye).await?;
                    return Ok(());
                }
                _ = ping.tick() => {
                    if heard.elapsed() > IDLE_LIMIT {
                        return Err(Failure::Broken);
                    }
                    send(&mut noise, &mut buf, &mut wr, &Message::Ping).await?;
                }
                changed = status.samples.changed() => {
                    changed.map_err(|_| Failure::Broken)?;
                    // The host's user took `see-status` out of the grants.
                    if !access.password_grants().contains(&Permission::SeeStatus) {
                        send(&mut noise, &mut buf, &mut wr, &Message::Bye).await?;
                        return Ok(());
                    }
                    let sample = status.samples.borrow_and_update().clone();
                    if let Some(s) = sample {
                        let mut s = (*s).clone();
                        s.offers = if access.without_asking() { access.password_grants() } else { Vec::new() };
                        s.ai = ai.ready();
                        if probed.is_none_or(|t| t.elapsed() >= crate::system::PROBE_EVERY) {
                            services = crate::system::listening(access.service_ports()).await;
                            probed = Some(Instant::now());
                        }
                        s.services = services.clone();
                        #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
                        {
                            s.nanokvm = access.nanokvm().offered();
                        }
                        send(&mut noise, &mut buf, &mut wr, &Message::System(Box::new(s))).await?;
                    }
                }
                frame = frames.recv() => {
                    let frame = frame.ok_or(Failure::Broken)?;
                    heard = Instant::now();
                    let n = noise.read_message(&frame, &mut plain)?;
                    match serde_json::from_slice::<Message>(&plain[..n]) {
                        Ok(Message::Ping) => send(&mut noise, &mut buf, &mut wr, &Message::Pong).await?,
                        Ok(Message::Bye) => return Ok(()),
                        Ok(Message::Pace { slow }) => status.set_slow(slow),
                        Ok(_) => {}
                        Err(_) => return Err(Failure::Broken),
                    }
                }
            }
        }
    }
    .await;
    reading.abort();
    let _ = wr.flush().await;
    outcome
}

/// Follows a host's status as its watcher until either side ends, handing
/// each sample to `sampled`, and tells the host the pace `slow` holds, as
/// the session starts and whenever it changes.
pub async fn follow<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    stream: S,
    mut noise: snow::TransportState,
    stop: &Notify,
    mut slow: tokio::sync::watch::Receiver<bool>,
    mut sampled: impl FnMut(Sample),
) -> Result<(), Failure> {
    let (rd, mut wr) = tokio::io::split(stream);
    let (mut frames, reading) = reader(rd);
    let mut ping = tokio::time::interval(PING_EVERY);
    let mut heard = Instant::now();
    let (mut buf, mut plain) = (vec![0u8; MAX_FRAME], vec![0u8; MAX_FRAME]);
    let outcome = async {
        let pace = Message::Pace { slow: *slow.borrow_and_update() };
        send(&mut noise, &mut buf, &mut wr, &pace).await?;
        loop {
            tokio::select! {
                _ = stop.notified() => {
                    send(&mut noise, &mut buf, &mut wr, &Message::Bye).await?;
                    return Ok(());
                }
                // The lease that held the pace let go: the watch ends with it.
                changed = slow.changed() => {
                    if changed.is_err() {
                        send(&mut noise, &mut buf, &mut wr, &Message::Bye).await?;
                        return Ok(());
                    }
                    let pace = Message::Pace { slow: *slow.borrow_and_update() };
                    send(&mut noise, &mut buf, &mut wr, &pace).await?;
                }
                _ = ping.tick() => {
                    if heard.elapsed() > IDLE_LIMIT {
                        return Err(Failure::Broken);
                    }
                    send(&mut noise, &mut buf, &mut wr, &Message::Ping).await?;
                }
                frame = frames.recv() => {
                    let frame = frame.ok_or(Failure::Broken)?;
                    heard = Instant::now();
                    let n = noise.read_message(&frame, &mut plain)?;
                    match serde_json::from_slice::<Message>(&plain[..n]) {
                        Ok(Message::System(s)) => sampled(*s),
                        Ok(Message::Ping) => send(&mut noise, &mut buf, &mut wr, &Message::Pong).await?,
                        Ok(Message::Bye) => return Ok(()),
                        Ok(_) => {}
                        Err(_) => return Err(Failure::Broken),
                    }
                }
            }
        }
    }
    .await;
    reading.abort();
    let _ = wr.flush().await;
    outcome
}

/// How watching a host goes, for the GUI.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Phase {
    Connecting,
    Watching,
    /// No saved password and no key the host admitted: nothing is dialed.
    NoCredential,
    /// The host announced no `status` (an older version): nothing is dialed.
    Unsupported,
    Refused,
    Offline,
}

/// One host watched.
struct Watched {
    phase: Phase,
    sample: Option<Sample>,
    /// Unix milliseconds when the sample arrived here.
    received_at: u64,
    /// Whether a task watches it now.
    running: bool,
    /// Whether the lease asks for the slow pace, for its session (`follow`).
    slow: tokio::sync::watch::Sender<bool>,
}

impl Watched {
    /// A host about to be watched, at the usual pace.
    fn new() -> Watched {
        Watched {
            phase: Phase::Connecting,
            sample: None,
            received_at: 0,
            running: false,
            slow: tokio::sync::watch::Sender::new(false),
        }
    }
}

/// The hosts this device watches (controller side), in memory only.
#[derive(Default)]
pub struct Watching {
    /// Until when the GUI's lease holds, and the hosts.
    inner: Mutex<(Option<Instant>, BTreeMap<String, Watched>)>,
}

fn unix_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_millis() as u64)
}

impl Watching {
    /// Holds the lease on watching `devices` (the first MAX_WATCHED) for
    /// LEASE, those also in `slow` at the slow pace: hosts left out stop
    /// being watched, new ones start (`watch.set`).
    /// Side effects: spawns a task per host on `rt`, which dials it through
    /// `dialer` while the lease lasts; a host's session tells it a changed
    /// pace.
    pub fn set(
        self: &Arc<Self>,
        devices: &[String],
        slow: &[String],
        dialer: &Dialer,
        rt: &tokio::runtime::Handle,
    ) {
        let Ok(mut w) = self.inner.lock() else {
            return;
        };
        w.0 = (!devices.is_empty()).then(|| Instant::now() + LEASE);
        let wanted: Vec<&String> = devices.iter().take(MAX_WATCHED).collect();
        w.1.retain(|id, _| wanted.contains(&id));
        for id in wanted {
            let e = w.1.entry(id.clone()).or_insert_with(Watched::new);
            let paced = slow.contains(id);
            e.slow
                .send_if_modified(|s| std::mem::replace(s, paced) != paced);
            if !e.running {
                e.running = true;
                rt.spawn(keep(self.clone(), dialer.clone(), id.clone()));
            }
        }
    }

    /// Whether a host is still to be watched: listed, and the lease holds.
    fn wanted(&self, id: &str) -> bool {
        self.inner
            .lock()
            .is_ok_and(|w| w.0.is_some_and(|t| t > Instant::now()) && w.1.contains_key(id))
    }

    /// Lists a host under a fresh lease without dialing it, for tests that
    /// run its session themselves.
    #[cfg(test)]
    pub fn expect(&self, id: &str) {
        let mut w = self.inner.lock().unwrap();
        w.0 = Some(Instant::now() + LEASE);
        w.1.entry(id.to_owned())
            .or_insert_with(Watched::new)
            .running = true;
    }

    /// Asks for a listed host's slow pace, or the usual one, as a lease
    /// does, for the tests.
    #[cfg(test)]
    pub fn expect_slow(&self, id: &str, slow: bool) {
        if let Some(e) = self.inner.lock().unwrap().1.get(id) {
            e.slow.send_replace(slow);
        }
    }

    /// The pace the lease asks for a host, as it changes, for its session;
    /// one that ends at once for a host the lease does not list.
    pub fn pace(&self, id: &str) -> tokio::sync::watch::Receiver<bool> {
        self.inner
            .lock()
            .ok()
            .and_then(|w| w.1.get(id).map(|e| e.slow.subscribe()))
            .unwrap_or_else(|| tokio::sync::watch::channel(false).1)
    }

    /// Keeps a host's newest sample.
    pub fn sampled(&self, id: &str, sample: Sample) {
        if let Ok(mut w) = self.inner.lock()
            && let Some(e) = w.1.get_mut(id)
        {
            e.sample = Some(sample);
            e.received_at = unix_ms();
            e.phase = Phase::Watching;
        }
    }

    /// When a host's newest sample arrived (0: none).
    fn received_at(&self, id: &str) -> u64 {
        self.inner
            .lock()
            .ok()
            .and_then(|w| w.1.get(id).map(|e| e.received_at))
            .unwrap_or(0)
    }

    fn phase(&self, id: &str, phase: Phase) {
        if let Ok(mut w) = self.inner.lock()
            && let Some(e) = w.1.get_mut(id)
        {
            e.phase = phase;
        }
    }

    /// A host's task ended: in `phase` while it stays listed, gone once
    /// the lease no longer lists it.
    fn ended(&self, id: &str, phase: Phase) {
        if let Ok(mut w) = self.inner.lock() {
            let leased = w.0.is_some_and(|t| t > Instant::now());
            match w.1.get_mut(id) {
                Some(e) if leased => {
                    e.phase = phase;
                    e.running = false;
                }
                Some(_) => {
                    w.1.remove(id);
                }
                None => {}
            }
        }
    }

    /// The hosts watched while the lease holds, for the status:
    /// `[{device_id, state, sample?, received_at}]`.
    pub fn list(&self) -> Value {
        let Ok(w) = self.inner.lock() else {
            return json!([]);
        };
        if !w.0.is_some_and(|t| t > Instant::now()) {
            return json!([]);
        }
        w.1.iter()
            .map(|(id, e)| {
                json!({
                    "device_id": id,
                    "state": e.phase,
                    "sample": e.sample,
                    "received_at": e.received_at,
                })
            })
            .collect()
    }
}

/// What to do after a watch's session ended, by how it ended.
/// Returns: the phase to show, and how long to wait before the next
/// attempt (None: stop watching this host until the GUI asks again).
fn after(failure: Option<Failure>, backoff: Duration) -> (Phase, Option<Duration>) {
    match failure {
        // The saved password is forgotten with the refusal: retrying
        // would guess.
        Some(Failure::Refused(Refusal::Password)) => (Phase::Refused, None),
        Some(Failure::Refused(Refusal::RateLimited)) => (Phase::Refused, Some(RATE_LIMITED_WAIT)),
        Some(Failure::Refused(_)) => (Phase::Refused, Some(backoff)),
        Some(Failure::Unsupported) => (Phase::Unsupported, None),
        Some(Failure::KeyChanged | Failure::WrongDevice | Failure::Incompatible) => {
            (Phase::Refused, None)
        }
        None | Some(Failure::Unreachable | Failure::Broken) => (Phase::Offline, Some(backoff)),
    }
}

/// Watches one host while the lease lists it: dials it with its saved
/// password or the key it admitted, follows its samples, and dials again
/// after a drop with a growing wait.
async fn keep(w: Arc<Watching>, dialer: Dialer, id: String) {
    let ctx = dialer.ctx().clone();
    let mut backoff = BACKOFF_FIRST;
    let mut last = Phase::Connecting;
    while w.wanted(&id) {
        let Some(host) = ctx.access.known(&id) else {
            last = Phase::Unsupported;
            break;
        };
        if !host.features.iter().any(|f| f == feature::STATUS) {
            last = Phase::Unsupported;
            break;
        }
        let password = ctx.access.saved_password(&id);
        let key = ctx
            .access
            .saved(&id)
            .key
            .filter(|k| crate::keys::is_login(ctx.access.secrets(), k));
        if password.is_none() && key.is_none() {
            last = Phase::NoCredential;
            break;
        }
        w.phase(&id, Phase::Connecting);
        let started = unix_ms();
        let ask = Ask {
            device_id: id.clone(),
            route: crate::net::Choice::from_prefer(&host.prefer),
            remember: Remember {
                save: false,
                saved: password.is_some(),
            },
            password: password.unwrap_or_default(),
            want: vec![Permission::SeeStatus],
            kind: Kind::Watch,
        };
        let failure = match dialer.start(ask) {
            Err(_) => Some(Failure::Unreachable),
            Ok((session, work)) => {
                let mut run = tokio::spawn(work);
                loop {
                    tokio::select! {
                        _ = &mut run => break,
                        _ = tokio::time::sleep(Duration::from_secs(1)) => {
                            if !w.wanted(&id) {
                                let _ = ctx.sessions.close(session);
                            }
                        }
                    }
                }
                ctx.sessions.forget(session).flatten()
            }
        };
        if !w.wanted(&id) {
            break;
        }
        // A watch that delivered starts its waits over.
        if w.received_at(&id) >= started {
            backoff = BACKOFF_FIRST;
        }
        let (phase, wait) = after(failure, backoff);
        last = phase;
        let Some(wait) = wait else {
            break;
        };
        w.phase(&id, phase);
        let until = Instant::now() + wait;
        while Instant::now() < until && w.wanted(&id) {
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
        backoff = (backoff * 2).min(BACKOFF_MAX);
    }
    w.ended(&id, last);
}

#[cfg(test)]
mod tests {
    use super::*;

    /// After a watch ends: a wrong password stops it (the saved one is
    /// forgotten), rate limits wait 10 minutes, anything else waits the
    /// growing backoff.
    #[test]
    fn what_follows_a_watch_that_ended() {
        let b = Duration::from_secs(40);
        let cases = [
            (
                Some(Failure::Refused(Refusal::Password)),
                Phase::Refused,
                None,
            ),
            (
                Some(Failure::Refused(Refusal::RateLimited)),
                Phase::Refused,
                Some(RATE_LIMITED_WAIT),
            ),
            (
                Some(Failure::Refused(Refusal::Denied)),
                Phase::Refused,
                Some(b),
            ),
            (
                Some(Failure::Refused(Refusal::Busy)),
                Phase::Refused,
                Some(b),
            ),
            (Some(Failure::Unsupported), Phase::Unsupported, None),
            (Some(Failure::KeyChanged), Phase::Refused, None),
            (Some(Failure::Unreachable), Phase::Offline, Some(b)),
            (Some(Failure::Broken), Phase::Offline, Some(b)),
            (None, Phase::Offline, Some(b)),
        ];
        for (failure, phase, wait) in cases {
            assert_eq!(after(failure, b), (phase, wait), "{failure:?}");
        }
    }

    /// A lapsed lease lists nothing; a new one lists its hosts again.
    #[test]
    fn a_lapsed_lease_lists_nothing() {
        let w = Watching::default();
        {
            let mut inner = w.inner.lock().unwrap();
            inner.0 = Some(Instant::now() + LEASE);
            inner.1.insert("100000002".into(), Watched::new());
        }
        w.sampled("100000002", Sample::default());
        let listed = w.list();
        assert_eq!(listed[0]["state"], "watching");
        assert!(listed[0]["received_at"].as_u64().unwrap() > 0);
        assert!(w.wanted("100000002"));
        w.inner.lock().unwrap().0 = Some(Instant::now() - Duration::from_secs(1));
        assert_eq!(w.list(), json!([]));
        assert!(!w.wanted("100000002"), "its task stops");
        w.ended("100000002", Phase::Offline);
        assert!(w.inner.lock().unwrap().1.is_empty(), "forgotten");
    }
}
