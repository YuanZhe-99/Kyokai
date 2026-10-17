//! Sessions between a controller and a host (doc/en-us/protocol.md): a Noise
//! XX handshake that proves both device keys, OPAQUE password login bound to
//! the handshake, the host user's confirmation, then a heartbeat until
//! either side leaves. Generic over the stream, so tests run it in memory.

use crate::access::{Access, CONFIRM_TIMEOUT, Permission, Refusal};
use crate::auth::{self, Slot};
use crate::identity::{self, Identity};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::net::IpAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::sync::Notify;

/// Version of the wire protocol; a mismatch closes the connection.
pub const PROTOCOL: u32 = 1;
const PATTERN: &str = "Noise_XX_25519_ChaChaPoly_BLAKE2s";
const PROLOGUE: &[u8] = b"kyokai/session/v1";
const STATIC_TAG: &[u8] = b"kyokai/noise-static/v1";
/// Shown to controllers (AGPL-3.0 section 13).
pub const SOURCE_URL: &str = "https://github.com/YuanZhe-99/Kyokai";
const MAX_FRAME: usize = 65_535;
/// Noise adds a 16-byte tag to every message.
const MAX_PLAINTEXT: usize = MAX_FRAME - 16;
/// How long one handshake step may take.
const STEP_TIMEOUT: Duration = Duration::from_secs(15);
const PING_EVERY: Duration = Duration::from_secs(5);
const IDLE_LIMIT: Duration = Duration::from_secs(15);
/// Finished sessions kept for the interface.
const KEEP_CLOSED: usize = 8;

#[derive(Serialize, Deserialize)]
struct HostHello {
    protocol: u32,
    version: String,
    source: String,
    device_key: String,
    signature: String,
}

#[derive(Serialize, Deserialize)]
struct ClientHello {
    protocol: u32,
    version: String,
    device_key: String,
    signature: String,
    want: Vec<Permission>,
    requests: [String; 2],
}

#[derive(Serialize, Deserialize)]
struct Challenge {
    responses: [String; 2],
}

#[derive(Serialize, Deserialize)]
struct Login {
    slot: Option<Slot>,
    finalization: Option<String>,
}

#[derive(Serialize, Deserialize)]
struct Verdict {
    accepted: bool,
    #[serde(default)]
    granted: Vec<Permission>,
    #[serde(default)]
    reason: Option<Refusal>,
}

/// Messages of an established session.
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
enum Message {
    Ping,
    Pong,
    Bye,
}

/// Why a session ended or never started, as shown to people.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Failure {
    /// Refused by the host for this reason.
    Refused(Refusal),
    /// No route to the device, or it did not answer.
    Unreachable,
    /// The host's device key does not derive the dialed Device ID.
    WrongDevice,
    /// The host's key differs from the one pinned for its Device ID.
    KeyChanged,
    /// The other side speaks another protocol version.
    Incompatible,
    /// The connection broke or a message was malformed.
    Broken,
}

impl From<std::io::Error> for Failure {
    fn from(_: std::io::Error) -> Self {
        Failure::Broken
    }
}

impl From<snow::Error> for Failure {
    fn from(_: snow::Error) -> Self {
        Failure::Broken
    }
}

/// What a session is doing, for the interface.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum State {
    Connecting,
    Confirming,
    Connected,
    Closed,
    Failed,
}

/// Which end of a session this device is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Role {
    Host,
    Controller,
}

/// One session as the interface sees it.
#[derive(Clone)]
struct Entry {
    id: u64,
    role: Role,
    device_id: String,
    state: State,
    failure: Option<Failure>,
    granted: Vec<Permission>,
    peer_version: String,
    peer_source: String,
    stop: Arc<Notify>,
}

/// Every session of this device, live and recently finished.
#[derive(Default)]
pub struct Sessions {
    inner: Mutex<(u64, Vec<Entry>)>,
}

impl Sessions {
    fn open(&self, role: Role, device_id: &str) -> (u64, Arc<Notify>) {
        let mut s = self.inner.lock().expect("sessions lock");
        s.0 += 1;
        let id = s.0;
        let stop = Arc::new(Notify::new());
        s.1.push(Entry {
            id,
            role,
            device_id: device_id.to_owned(),
            state: State::Connecting,
            failure: None,
            granted: Vec::new(),
            peer_version: String::new(),
            peer_source: String::new(),
            stop: stop.clone(),
        });
        (id, stop)
    }

    fn update(&self, id: u64, f: impl FnOnce(&mut Entry)) {
        let mut s = self.inner.lock().expect("sessions lock");
        if let Some(e) = s.1.iter_mut().find(|e| e.id == id) {
            f(e);
        }
        let finished =
            s.1.iter()
                .filter(|e| matches!(e.state, State::Closed | State::Failed))
                .count();
        if finished > KEEP_CLOSED {
            let drop_first =
                s.1.iter()
                    .position(|e| matches!(e.state, State::Closed | State::Failed));
            if let Some(i) = drop_first {
                s.1.remove(i);
            }
        }
    }

    fn end(&self, id: u64, failure: Option<Failure>) {
        self.update(id, |e| {
            e.state = if failure.is_some() {
                State::Failed
            } else {
                State::Closed
            };
            e.failure = failure;
        });
    }

    /// Asks a live session to end.
    pub fn close(&self, id: u64) -> Result<(), String> {
        let s = self.inner.lock().map_err(|_| "sessions lock poisoned")?;
        let e = s.1.iter().find(|e| e.id == id).ok_or("no such session")?;
        e.stop.notify_one();
        Ok(())
    }

    /// The sessions, for the status (never a secret).
    pub fn list(&self) -> Value {
        let s = self.inner.lock().expect("sessions lock");
        s.1.iter()
            .map(|e| {
                json!({
                    "id": e.id,
                    "role": e.role,
                    "device_id": e.device_id,
                    "state": e.state,
                    "failure": e.failure,
                    "granted": e.granted,
                    "peer_version": e.peer_version,
                    "peer_source": e.peer_source,
                })
            })
            .collect()
    }
}

/// What both ends need: who this device is and the shared state.
pub struct Ctx {
    pub identity: Arc<Identity>,
    pub access: Arc<Access>,
    pub sessions: Arc<Sessions>,
    pub version: String,
}

/// A Noise channel over a stream, after the handshake.
struct Channel<S> {
    stream: S,
    noise: snow::TransportState,
    buf: Vec<u8>,
}

impl<S: AsyncRead + AsyncWrite + Unpin> Channel<S> {
    async fn send<T: Serialize>(&mut self, msg: &T) -> Result<(), Failure> {
        let plain = serde_json::to_vec(msg).map_err(|_| Failure::Broken)?;
        if plain.len() > MAX_PLAINTEXT {
            return Err(Failure::Broken);
        }
        let n = self.noise.write_message(&plain, &mut self.buf)?;
        // A peer that left may never let the flush finish.
        step(write_frame(&mut self.stream, &self.buf[..n])).await
    }

    async fn recv<T: DeserializeOwned>(&mut self) -> Result<T, Failure> {
        let frame = read_frame(&mut self.stream).await?;
        let n = self.noise.read_message(&frame, &mut self.buf)?;
        serde_json::from_slice(&self.buf[..n]).map_err(|_| Failure::Broken)
    }
}

async fn read_frame<R: AsyncRead + Unpin>(r: &mut R) -> Result<Vec<u8>, Failure> {
    let len = r.read_u16().await? as usize;
    let mut frame = vec![0; len];
    r.read_exact(&mut frame).await?;
    Ok(frame)
}

async fn write_frame<W: AsyncWrite + Unpin>(w: &mut W, frame: &[u8]) -> Result<(), Failure> {
    let len = u16::try_from(frame.len()).map_err(|_| Failure::Broken)?;
    w.write_u16(len).await?;
    w.write_all(frame).await?;
    w.flush().await?;
    Ok(())
}

async fn step<T>(f: impl Future<Output = Result<T, Failure>>) -> Result<T, Failure> {
    tokio::time::timeout(STEP_TIMEOUT, f)
        .await
        .unwrap_or(Err(Failure::Broken))
}

fn noise(initiator: bool) -> Result<(snow::HandshakeState, Vec<u8>), Failure> {
    let params: snow::params::NoiseParams = PATTERN.parse()?;
    let builder = snow::Builder::new(params);
    let keys = builder.generate_keypair()?;
    let builder = builder
        .local_private_key(&keys.private)?
        .prologue(PROLOGUE)?;
    let hs = if initiator {
        builder.build_initiator()?
    } else {
        builder.build_responder()?
    };
    Ok((hs, keys.public))
}

/// Signs this device's Noise static key with its device key.
fn sign_static(identity: &Identity, noise_static: &[u8]) -> String {
    STANDARD.encode(identity.sign(&[STATIC_TAG, noise_static].concat()))
}

/// Checks that `device_key` signed the peer's Noise static key.
/// Returns: the device key's bytes.
fn verify_static(
    device_key: &str,
    signature: &str,
    noise_static: Option<&[u8]>,
) -> Result<[u8; 32], Failure> {
    let key: [u8; 32] = STANDARD
        .decode(device_key)
        .ok()
        .and_then(|k| k.try_into().ok())
        .ok_or(Failure::Broken)?;
    let sig: [u8; 64] = STANDARD
        .decode(signature)
        .ok()
        .and_then(|s| s.try_into().ok())
        .ok_or(Failure::Broken)?;
    let noise_static = noise_static.ok_or(Failure::Broken)?;
    if !identity::verify(&key, &[STATIC_TAG, noise_static].concat(), &sig) {
        return Err(Failure::Broken);
    }
    Ok(key)
}

/// Serves one incoming connection as the host.
/// Side effects: registers the session, may ask the host's user, records the
/// controller as trusted when accepted, counts failed logins.
pub async fn serve<S: AsyncRead + AsyncWrite + Unpin>(ctx: Arc<Ctx>, mut stream: S, from: IpAddr) {
    let (hs, my_static) = match noise(false) {
        Ok(v) => v,
        Err(_) => return,
    };
    let mut buf = vec![0u8; MAX_FRAME];
    let result = async {
        let mut hs = hs;
        // 1: -> e
        let frame = step(read_frame(&mut stream)).await?;
        hs.read_message(&frame, &mut buf)?;
        // 2: <- e, ee, s, es + host_hello
        let hello = HostHello {
            protocol: PROTOCOL,
            version: ctx.version.clone(),
            source: SOURCE_URL.into(),
            device_key: STANDARD.encode(ctx.identity.public_key()),
            signature: sign_static(&ctx.identity, &my_static),
        };
        let payload = serde_json::to_vec(&hello).map_err(|_| Failure::Broken)?;
        let n = hs.write_message(&payload, &mut buf)?;
        step(write_frame(&mut stream, &buf[..n])).await?;
        // 3: -> s, se + client_hello
        let frame = step(read_frame(&mut stream)).await?;
        let n = hs.read_message(&frame, &mut buf)?;
        let client: ClientHello = serde_json::from_slice(&buf[..n]).map_err(|_| Failure::Broken)?;
        if client.protocol != PROTOCOL {
            return Err(Failure::Incompatible);
        }
        let client_key = verify_static(
            &client.device_key,
            &client.signature,
            hs.get_remote_static(),
        )?;
        let context = hs.get_handshake_hash().to_vec();
        let channel = Channel {
            stream,
            noise: hs.into_transport_mode()?,
            buf,
        };
        Ok::<_, Failure>((channel, client, client_key, context))
    }
    .await;
    let Ok((mut ch, client, client_key, context)) = result else {
        return;
    };
    let device_id = identity::device_id(&client_key);
    let (id, stop) = ctx.sessions.open(Role::Host, &device_id);
    ctx.sessions.update(id, |e| {
        e.peer_version = client.version.clone();
    });
    match host_login(&ctx, &mut ch, &client, &client_key, &context, from, id).await {
        Ok(granted) => {
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let failure = run(&mut ch, &stop).await.err();
            ctx.sessions.end(id, failure);
        }
        Err(f) => ctx.sessions.end(id, Some(f)),
    }
}

/// The host's side of login and confirmation.
/// Returns: the granted permissions, after telling the controller.
async fn host_login<S: AsyncRead + AsyncWrite + Unpin>(
    ctx: &Ctx,
    ch: &mut Channel<S>,
    client: &ClientHello,
    client_key: &[u8; 32],
    context: &[u8],
    from: IpAddr,
    id: u64,
) -> Result<Vec<Permission>, Failure> {
    let key_b64 = STANDARD.encode(client_key);
    let pinned = ctx.access.trusted().contains_key(&key_b64);
    let refuse = |reason| Verdict {
        accepted: false,
        granted: Vec::new(),
        reason: Some(reason),
    };
    if let Err(reason) = ctx.access.admit(from, pinned) {
        let _ = ch.send(&refuse(reason)).await;
        return Err(Failure::Refused(reason));
    }
    let host_key = ctx.identity.public_key();
    let (responses, half) = ctx
        .access
        .slots
        .lock()
        .map_err(|_| Failure::Broken)?
        .respond(&client.requests, &host_key, context)
        .map_err(|_| Failure::Broken)?;
    step(ch.send(&Challenge { responses })).await?;
    let login: Login = step(ch.recv()).await?;
    let ok = match (login.slot, login.finalization) {
        (Some(slot), Some(fin)) => half.finish(slot, &fin, &host_key, context),
        _ => false,
    };
    if !ok {
        ctx.access.failed(from);
        let _ = ch.send(&refuse(Refusal::Password)).await;
        return Err(Failure::Refused(Refusal::Password));
    }
    ctx.sessions.update(id, |e| e.state = State::Confirming);
    let device_id = identity::device_id(client_key);
    let (ask_id, decision) = match ctx.access.ask(&key_b64, &device_id, client.want.clone()) {
        Ok(v) => v,
        Err(reason) => {
            let _ = ch.send(&refuse(reason)).await;
            return Err(Failure::Refused(reason));
        }
    };
    let granted = match tokio::time::timeout(CONFIRM_TIMEOUT, decision).await {
        Ok(Ok(Some(granted))) => granted,
        Ok(Ok(None)) | Ok(Err(_)) => {
            ctx.access.withdraw(ask_id);
            let _ = ch.send(&refuse(Refusal::Denied)).await;
            return Err(Failure::Refused(Refusal::Denied));
        }
        Err(_) => {
            ctx.access.withdraw(ask_id);
            let _ = ch.send(&refuse(Refusal::Timeout)).await;
            return Err(Failure::Refused(Refusal::Timeout));
        }
    };
    let _ = ctx.access.trust(&key_b64, &device_id, &granted);
    step(ch.send(&Verdict {
        accepted: true,
        granted: granted.clone(),
        reason: None,
    }))
    .await?;
    Ok(granted)
}

/// Connects to a host as the controller, over a stream already open to it.
/// Inputs: the Device ID that was dialed, the password the person typed and
/// the permissions to ask for.
/// Returns: false when the device on this route has another key than the
/// dialed Device ID derives (a squatter): the caller tries the next route.
/// Side effects: pins the host key on success, then keeps the session alive
/// until either side ends it; records how it ended.
pub async fn connect<S: AsyncRead + AsyncWrite + Unpin>(
    ctx: Arc<Ctx>,
    id: u64,
    stop: Arc<Notify>,
    mut stream: S,
    device_id: String,
    password: zeroize::Zeroizing<String>,
    want: Vec<Permission>,
) -> bool {
    let result = async {
        let (mut hs, my_static) = noise(true)?;
        let mut buf = vec![0u8; MAX_FRAME];
        // 1: -> e
        let n = hs.write_message(&[], &mut buf)?;
        step(write_frame(&mut stream, &buf[..n])).await?;
        // 2: <- e, ee, s, es + host_hello
        let frame = step(read_frame(&mut stream)).await?;
        let n = hs.read_message(&frame, &mut buf)?;
        let host: HostHello = serde_json::from_slice(&buf[..n]).map_err(|_| Failure::Broken)?;
        if host.protocol != PROTOCOL {
            return Err(Failure::Incompatible);
        }
        let host_key = verify_static(&host.device_key, &host.signature, hs.get_remote_static())?;
        if identity::device_id(&host_key) != device_id {
            return Err(Failure::WrongDevice);
        }
        if ctx
            .access
            .known_host(&device_id)
            .is_some_and(|pinned| pinned != host.device_key)
        {
            return Err(Failure::KeyChanged);
        }
        ctx.sessions.update(id, |e| {
            e.peer_version = host.version.clone();
            e.peer_source = host.source.clone();
        });
        // 3: -> s, se + client_hello with both credential requests
        let (half, requests) = auth::client_start(&password).map_err(|_| Failure::Broken)?;
        let hello = ClientHello {
            protocol: PROTOCOL,
            version: ctx.version.clone(),
            device_key: STANDARD.encode(ctx.identity.public_key()),
            signature: sign_static(&ctx.identity, &my_static),
            want,
            requests,
        };
        let payload = serde_json::to_vec(&hello).map_err(|_| Failure::Broken)?;
        let n = hs.write_message(&payload, &mut buf)?;
        step(write_frame(&mut stream, &buf[..n])).await?;
        let context = hs.get_handshake_hash().to_vec();
        let mut ch = Channel {
            stream,
            noise: hs.into_transport_mode()?,
            buf,
        };
        // 4: challenge, or an early refusal (rate limits).
        let first: Value = step(ch.recv()).await?;
        if first.get("accepted").is_some() {
            let v: Verdict = serde_json::from_value(first).map_err(|_| Failure::Broken)?;
            return Err(Failure::Refused(v.reason.unwrap_or(Refusal::Denied)));
        }
        let challenge: Challenge = serde_json::from_value(first).map_err(|_| Failure::Broken)?;
        // 5: login
        let opened = half.finish(&password, &challenge.responses, &host_key, &context);
        let login = match opened {
            Some((slot, fin)) => Login {
                slot: Some(slot),
                finalization: Some(fin),
            },
            None => Login {
                slot: None,
                finalization: None,
            },
        };
        step(ch.send(&login)).await?;
        ctx.sessions.update(id, |e| e.state = State::Confirming);
        // 6: verdict, after the host's user decided.
        let verdict: Verdict = tokio::time::timeout(CONFIRM_TIMEOUT + STEP_TIMEOUT, ch.recv())
            .await
            .unwrap_or(Err(Failure::Broken))?;
        if !verdict.accepted {
            return Err(Failure::Refused(verdict.reason.unwrap_or(Refusal::Denied)));
        }
        if ctx.access.known_host(&device_id).is_none() {
            let _ = ctx.access.pin_host(&device_id, &host.device_key);
        }
        Ok((ch, verdict.granted))
    }
    .await;
    match result {
        Ok((mut ch, granted)) => {
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let failure = run(&mut ch, &stop).await.err();
            ctx.sessions.end(id, failure);
        }
        Err(Failure::WrongDevice) => return false,
        Err(f) => ctx.sessions.end(id, Some(f)),
    }
    true
}

/// Registers an outgoing session before its connection is opened, so the
/// interface sees it at once.
pub fn begin(ctx: &Ctx, device_id: &str) -> (u64, Arc<Notify>) {
    ctx.sessions.open(Role::Controller, device_id)
}

/// Ends an outgoing session that found no device to talk to.
pub fn give_up(ctx: &Ctx, id: u64, failure: Failure) {
    ctx.sessions.end(id, Some(failure));
}

/// Keeps an established session alive: pings, answers pings, and ends on
/// `bye`, silence, a broken channel or a local stop.
async fn run<S: AsyncRead + AsyncWrite + Unpin>(
    ch: &mut Channel<S>,
    stop: &Notify,
) -> Result<(), Failure> {
    let mut ping = tokio::time::interval(PING_EVERY);
    let mut heard = Instant::now();
    loop {
        tokio::select! {
            _ = stop.notified() => {
                let _ = ch.send(&Message::Bye).await;
                return Ok(());
            }
            _ = ping.tick() => {
                if heard.elapsed() > IDLE_LIMIT {
                    return Err(Failure::Broken);
                }
                ch.send(&Message::Ping).await?;
            }
            msg = ch.recv::<Message>() => {
                heard = Instant::now();
                match msg? {
                    Message::Ping => ch.send(&Message::Pong).await?,
                    Message::Pong => {}
                    Message::Bye => return Ok(()),
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::Secrets;

    fn device(version: &str) -> Arc<Ctx> {
        let secrets = Arc::new(Secrets::memory());
        Arc::new(Ctx {
            identity: Arc::new(Identity::load_or_create(&secrets).unwrap()),
            access: Arc::new(Access::load(secrets).unwrap()),
            sessions: Arc::new(Sessions::default()),
            version: version.into(),
        })
    }

    fn state(ctx: &Ctx) -> Value {
        ctx.sessions.list()[0].clone()
    }

    /// Runs one session over an in-memory pipe and lets `decide` answer the
    /// host's confirmation.
    async fn attempt(
        host: &Arc<Ctx>,
        controller: &Arc<Ctx>,
        dialed: &str,
        password: &str,
        decide: Option<Vec<Permission>>,
    ) -> u64 {
        let (a, b) = tokio::io::duplex(1 << 16);
        let from: IpAddr = "10.126.126.2".parse().unwrap();
        tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(controller, dialed);
        let want = vec![Permission::ViewScreen, Permission::ControlInput];
        let c = controller.clone();
        let (dialed_owned, password) = (
            dialed.to_owned(),
            zeroize::Zeroizing::new(password.to_owned()),
        );
        tokio::spawn(async move {
            // As the dialer does: a wrong device key ends the attempt here.
            if !connect(c.clone(), id, stop, a, dialed_owned, password, want).await {
                give_up(&c, id, Failure::WrongDevice);
            }
        });
        for _ in 0..200 {
            tokio::time::sleep(Duration::from_millis(20)).await;
            let open = host.access.pending();
            if let Some(q) = open.as_array().and_then(|a| a.first()) {
                let qid = q["id"].as_u64().unwrap();
                host.access.decide(qid, decide.clone()).unwrap();
                break;
            }
            let s = state(controller);
            if s["state"] != "connecting" && s["state"] != "confirming" {
                break;
            }
        }
        for _ in 0..200 {
            let s = controller.sessions.list();
            let s = s
                .as_array()
                .unwrap()
                .iter()
                .find(|e| e["id"] == id)
                .unwrap()
                .clone();
            if s["state"] != "connecting" && s["state"] != "confirming" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        id
    }

    fn session(ctx: &Ctx, id: u64) -> Value {
        ctx.sessions
            .list()
            .as_array()
            .unwrap()
            .iter()
            .find(|e| e["id"] == id)
            .unwrap()
            .clone()
    }

    #[tokio::test]
    async fn the_right_password_and_a_confirmation_connect() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let id = attempt(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(vec![Permission::ViewScreen]),
        )
        .await;
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen"]));
        assert_eq!(s["peer_version"], "1.0.0");
        assert_eq!(s["peer_source"], SOURCE_URL);
        assert_eq!(state(&host)["state"], "connected");
        // The host now trusts the controller; the controller pinned the host.
        assert_eq!(host.access.trusted().len(), 1);
        assert!(controller.access.known_host(&dialed).is_some());
        // Closing from the controller ends both sides.
        controller.sessions.close(id).unwrap();
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert_eq!(session(&controller, id)["state"], "closed");
        assert_eq!(state(&host)["state"], "closed");
    }

    #[tokio::test]
    async fn refusals_are_named() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let id = attempt(&host, &controller, &dialed, "wrong", None).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "password"})
        );
        let id = attempt(&host, &controller, &dialed, "k7mq2xpa", None).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "denied"})
        );
        let id = attempt(&host, &controller, "100000002", "k7mq2xpa", None).await;
        assert_eq!(session(&controller, id)["failure"], "wrong-device");
    }

    #[tokio::test]
    async fn a_changed_host_key_is_refused() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        let dialed = host.identity.device_id();
        controller
            .access
            .pin_host(&dialed, "c29tZW9uZSBlbHNl")
            .unwrap();
        let id = attempt(&host, &controller, &dialed, "x", None).await;
        assert_eq!(session(&controller, id)["failure"], "key-changed");
    }
}
