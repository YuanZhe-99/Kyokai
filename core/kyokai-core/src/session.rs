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
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::sync::{Notify, mpsc};

use crate::{media, video};

/// Version of the wire protocol; a mismatch closes the connection.
pub const PROTOCOL: u32 = 2;
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
/// The QoS viewer id and display name of a session (one viewer, the primary
/// display).
const QOS_VIEWER: i32 = 1;
const QOS_DISPLAY: &str = "primary";
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
    /// A resume token from an earlier session with this host.
    #[serde(default)]
    resume: Option<String>,
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
    /// With an acceptance: a single-use token for resuming after a dropped
    /// connection.
    #[serde(default)]
    resume: Option<String>,
}

/// Messages of an established session.
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
enum Message {
    Ping,
    Pong,
    Bye,
    /// host → controller: the video stream that follows.
    VideoStart(media::VideoStart),
    /// controller → host: send a key frame next.
    KeyFrame,
    /// controller → host: one input event (Phase 3 step 3.5).
    Input {
        event: Value,
    },
    /// host → controller: a delay probe in the video stream; the controller
    /// echoes it with `reply` set (RustDesk's TestDelay).
    TestDelay {
        #[serde(default)]
        reply: bool,
    },
    /// controller → host: the viewer's image quality (`best`, `balanced`,
    /// `speed`) and frame-rate cap (RustDesk's presets).
    Quality {
        preset: String,
        fps: u32,
    },
    /// host → controller: what QoS measured and chose, for the viewer.
    Stats {
        delay_ms: u32,
        fps: u32,
        bitrate: u32,
    },
    /// controller → host (`run-ai-task`): start an AI task on the host; the
    /// key makes a repeated start (after a reconnect) start one task.
    AiStart {
        key: String,
        instruction: String,
    },
    /// controller → host (`run-ai-task`): steer a task this controller
    /// started; `text` is the reply's.
    AiCommand {
        id: u64,
        command: Steer,
        #[serde(default)]
        text: String,
    },
    /// controller → host (`approve-ai-action`): answer the approval of a
    /// task this controller started.
    AiDecide {
        id: u64,
        approve: bool,
    },
    /// host → controller: the newest task this controller started, whenever
    /// it changes and when a session (re)starts.
    AiTask {
        task: Value,
    },
    /// host → controller: why an AI request was refused.
    AiError {
        error: String,
    },
    /// either way (`clipboard`): text copied on the sender's side.
    Clipboard {
        text: String,
    },
    /// host → controller, with video: where the host's pointer is, as
    /// fractions of the picture, and whether it shows there.
    Cursor {
        x: f32,
        y: f32,
        visible: bool,
    },
}

/// What a controller may do to its AI task.
#[derive(Clone, Copy, Debug, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum Steer {
    Pause,
    Resume,
    Cancel,
    Reply,
}

/// How often the host looks where its pointer is (with video).
const POINTER_EVERY: Duration = Duration::from_millis(50);

/// How often the host looks for changes of a controller's AI task.
const AI_SYNC: Duration = Duration::from_millis(400);

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
    /// The connection dropped; the controller is resuming it.
    Reconnecting,
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
    /// Controller side: media for the GUI, and the GUI's control messages.
    media: Arc<media::Queue>,
    control_tx: mpsc::Sender<Value>,
    control_rx: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
}

/// Every session of this device, live and recently finished.
#[derive(Default)]
pub struct Sessions {
    inner: Mutex<(u64, Vec<Entry>)>,
    /// Incoming connections not yet finished with their handshake and login.
    incoming: AtomicUsize,
}

/// Most incoming connections handled at once; more are dropped.
const MAX_INCOMING: usize = 16;
/// Most connected host sessions at once (each may hold a capture and an
/// encoder); more are refused as busy.
const MAX_HOST_SESSIONS: usize = 4;

/// An incoming connection being handled; counted until dropped.
struct Incoming(Arc<Ctx>);

impl Drop for Incoming {
    fn drop(&mut self) {
        self.0.sessions.incoming.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Counts an incoming connection, unless too many are being handled.
fn enter(ctx: &Arc<Ctx>) -> Option<Incoming> {
    let n = ctx.sessions.incoming.fetch_add(1, Ordering::Relaxed);
    let guard = Incoming(ctx.clone());
    (n < MAX_INCOMING).then_some(guard)
}

impl Sessions {
    fn open(&self, role: Role, device_id: &str) -> (u64, Arc<Notify>) {
        let mut s = self.inner.lock().expect("sessions lock");
        s.0 += 1;
        let id = s.0;
        let stop = Arc::new(Notify::new());
        let (control_tx, control_rx) = mpsc::channel(64);
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
            media: Arc::default(),
            control_tx,
            control_rx: Arc::new(tokio::sync::Mutex::new(control_rx)),
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
            e.media.close();
            e.state = if failure.is_some() {
                State::Failed
            } else {
                State::Closed
            };
            e.failure = failure;
        });
    }

    /// A controller session's media queue and control sender, for the GUI.
    pub fn media(&self, id: u64) -> Option<(Arc<media::Queue>, mpsc::Sender<Value>)> {
        let s = self.inner.lock().ok()?;
        let e =
            s.1.iter()
                .find(|e| e.id == id && e.role == Role::Controller)?;
        Some((e.media.clone(), e.control_tx.clone()))
    }

    fn side(&self, id: u64) -> Option<Side> {
        let s = self.inner.lock().ok()?;
        let e = s.1.iter().find(|e| e.id == id)?;
        Some(Side::Controller {
            queue: e.media.clone(),
            control: e.control_rx.clone(),
        })
    }

    /// How many host sessions are connected.
    fn connected_hosts(&self) -> usize {
        self.inner.lock().map_or(MAX_HOST_SESSIONS, |s| {
            s.1.iter()
                .filter(|e| e.role == Role::Host && e.state == State::Connected)
                .count()
        })
    }

    /// Asks every live host session with a device to end.
    fn close_host_sessions(&self, device_id: &str) {
        if let Ok(s) = self.inner.lock() {
            for e in s.1.iter().filter(|e| {
                e.role == Role::Host
                    && e.device_id == device_id
                    && !matches!(e.state, State::Closed | State::Failed)
            }) {
                e.stop.notify_one();
            }
        }
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
    /// Opens this side's clipboard for a session that may share it.
    pub clipboard: crate::clipboard::Open,
    /// The host's AI tasks, which controllers may start and steer.
    pub ai: Arc<crate::ai::Ai>,
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
pub async fn serve<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    ctx: Arc<Ctx>,
    mut stream: S,
    from: IpAddr,
) {
    // Counted until the login ends; a flood of connections is dropped.
    let Some(incoming) = enter(&ctx) else {
        return;
    };
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
    let login = host_login(&ctx, &mut ch, &client, &client_key, &context, from, id).await;
    drop(incoming);
    match login {
        Ok(granted) => {
            let video = granted.contains(&Permission::ViewScreen);
            let input = granted.contains(&Permission::ControlInput);
            let clipboard = granted
                .contains(&Permission::Clipboard)
                .then(|| (ctx.clipboard)())
                .flatten();
            let ai = HostAi {
                ai: ctx.ai.clone(),
                peer: device_id.clone(),
                run: granted.contains(&Permission::RunAiTask),
                approve: granted.contains(&Permission::ApproveAiAction),
            };
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let failure = run(ch, &stop, Side::Host { video, input, ai }, clipboard)
                .await
                .err();
            // The controller's AI task does not go on without it.
            ctx.ai.controller_left(&device_id);
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
        resume: None,
    };
    if let Err(reason) = ctx.access.admit(from, pinned) {
        let _ = ch.send(&refuse(reason)).await;
        return Err(Failure::Refused(reason));
    }
    if ctx.sessions.connected_hosts() >= MAX_HOST_SESSIONS {
        let _ = ch.send(&refuse(Refusal::Busy)).await;
        return Err(Failure::Refused(Refusal::Busy));
    }
    // Resuming a dropped session: same key, same permissions, no new login.
    if let Some(token) = &client.resume
        && let Some(granted) = ctx.access.take_resume(token, &key_b64)
    {
        let resume = ctx.access.issue_resume(&key_b64, &granted);
        step(ch.send(&Verdict {
            accepted: true,
            granted: granted.clone(),
            reason: None,
            resume: Some(resume),
        }))
        .await?;
        return Ok(granted);
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
    let resume = ctx.access.issue_resume(&key_b64, &granted);
    step(ch.send(&Verdict {
        accepted: true,
        granted: granted.clone(),
        reason: None,
        resume: Some(resume),
    }))
    .await?;
    Ok(granted)
}

/// How a controller's attempt over one connection ended.
pub enum Outcome {
    /// The device on this route has another key than the dialed Device ID
    /// derives (a squatter): try the next route.
    WrongDevice,
    /// Finished; the session's entry records how.
    Done,
    /// The connection dropped (or a resume attempt could not get through):
    /// dial again and resume with this token.
    Lost(zeroize::Zeroizing<String>),
}

/// Connects to a host as the controller, over a stream already open to it.
/// Inputs: the Device ID that was dialed, the password the person typed, the
/// permissions to ask for, and a resume token when resuming.
/// Side effects: pins the host key on success, then keeps the session alive
/// until either side ends it; records how it ended unless it returns `Lost`.
#[allow(clippy::too_many_arguments)]
pub async fn connect<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    ctx: Arc<Ctx>,
    id: u64,
    stop: Arc<Notify>,
    mut stream: S,
    device_id: String,
    password: zeroize::Zeroizing<String>,
    want: Vec<Permission>,
    resume: Option<zeroize::Zeroizing<String>>,
) -> Outcome {
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
            resume: resume.as_ref().map(|t| t.to_string()),
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
        // 4: challenge, or at once a verdict (a refusal, or a resumption).
        let first: Value = step(ch.recv()).await?;
        let verdict: Verdict = if first.get("accepted").is_some() {
            serde_json::from_value(first).map_err(|_| Failure::Broken)?
        } else {
            let challenge: Challenge =
                serde_json::from_value(first).map_err(|_| Failure::Broken)?;
            // 5: login
            let login = match half.finish(&password, &challenge.responses, &host_key, &context) {
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
            tokio::time::timeout(CONFIRM_TIMEOUT + STEP_TIMEOUT, ch.recv())
                .await
                .unwrap_or(Err(Failure::Broken))?
        };
        if !verdict.accepted {
            return Err(Failure::Refused(verdict.reason.unwrap_or(Refusal::Denied)));
        }
        let _ = ctx.access.pin_host(&device_id, &host.device_key);
        Ok((ch, verdict.granted, verdict.resume))
    }
    .await;
    match result {
        Ok((ch, granted, token)) => {
            let clipboard = granted
                .contains(&Permission::Clipboard)
                .then(|| (ctx.clipboard)())
                .flatten();
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let side = ctx.sessions.side(id).ok_or(Failure::Broken);
            let result = match side {
                Ok(side) => run(ch, &stop, side, clipboard).await,
                Err(f) => Err(f),
            };
            match (result, token) {
                (Err(Failure::Broken), Some(token)) => {
                    ctx.sessions.update(id, |e| e.state = State::Reconnecting);
                    Outcome::Lost(zeroize::Zeroizing::new(token))
                }
                (result, _) => {
                    ctx.sessions.end(id, result.err());
                    Outcome::Done
                }
            }
        }
        Err(Failure::WrongDevice) => Outcome::WrongDevice,
        // A resume that could not get through keeps its (unused) token.
        Err(Failure::Broken) if resume.is_some() => Outcome::Lost(resume.expect("checked")),
        Err(f) => {
            ctx.sessions.end(id, Some(f));
            Outcome::Done
        }
    }
}

/// Revokes a trusted device: forgets it, drops its resume tokens and ends
/// its live sessions with this host.
pub fn untrust(ctx: &Ctx, key: &str) -> Result<(), String> {
    let device_id = ctx
        .access
        .trusted()
        .get(key)
        .map(|t| t.device_id.clone())
        .ok_or("not a trusted device")?;
    ctx.access.untrust(key)?;
    ctx.sessions.close_host_sessions(&device_id);
    Ok(())
}

/// Marks an outgoing session as resuming, between dial attempts.
pub fn reconnecting(ctx: &Ctx, id: u64) {
    ctx.sessions.update(id, |e| e.state = State::Reconnecting);
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

/// What one end of an established session does besides the heartbeat.
pub enum Side {
    /// The host: sends video while the controller may view the screen, and
    /// injects its input while it may control input.
    Host {
        video: bool,
        // Read by the Windows input service; macOS input comes with the Mac pass.
        #[cfg_attr(not(windows), allow(dead_code))]
        input: bool,
        ai: HostAi,
    },
    /// The controller: puts media into the queue its GUI drains, and sends
    /// the GUI's control messages (input, key-frame requests) to the host.
    Controller {
        queue: Arc<media::Queue>,
        control: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
    },
}

/// The host's AI as one session sees it: which controller it serves and
/// what that controller may do.
pub struct HostAi {
    ai: Arc<crate::ai::Ai>,
    /// The controller's Device ID: the origin of the tasks it starts.
    peer: String,
    /// `run-ai-task`: start and steer its own tasks.
    run: bool,
    /// `approve-ai-action`: answer its own tasks' approvals.
    approve: bool,
}

impl HostAi {
    /// Answers an AI request from the controller.
    /// Returns: why it was refused, safe to show.
    fn handle(&self, msg: Message) -> Result<(), String> {
        let not_allowed = || "this session may not do that".to_string();
        match msg {
            Message::AiStart { key, instruction } if self.run => self
                .ai
                .start(&key, &instruction, Some(&self.peer))
                .map(|_| ()),
            Message::AiCommand { id, command, text } if self.run => {
                if !self.ai.started_by(id, &self.peer) {
                    return Err(not_allowed());
                }
                match command {
                    Steer::Pause => self.ai.pause(id, crate::ai::Pause::Controller),
                    Steer::Resume => self.ai.resume(id),
                    Steer::Cancel => self.ai.cancel(id),
                    Steer::Reply => self.ai.reply(id, &text),
                }
            }
            Message::AiDecide { id, approve } if self.approve => {
                if !self.ai.started_by(id, &self.peer) {
                    return Err(not_allowed());
                }
                self.ai.decide(id, approve)
            }
            _ => Err(not_allowed()),
        }
    }
}

/// Encrypts one plaintext and writes it as a frame.
async fn seal<W: AsyncWrite + Unpin>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    plain: &[u8],
) -> Result<(), Failure> {
    let n = noise.write_message(plain, buf)?;
    step(write_frame(w, &buf[..n])).await
}

async fn seal_json<W: AsyncWrite + Unpin, T: Serialize>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    msg: &T,
) -> Result<(), Failure> {
    let plain = serde_json::to_vec(msg).map_err(|_| Failure::Broken)?;
    seal(noise, buf, w, &plain).await
}

/// Aborts a task when dropped (the reader of an ending session).
struct AbortOnDrop(tokio::task::JoinHandle<()>);

impl Drop for AbortOnDrop {
    fn drop(&mut self) {
        self.0.abort();
    }
}

/// Keeps an established session alive: pings, answers pings, carries
/// media, and ends on `bye`, silence, a broken channel or a local stop.
/// Notes: a reader task reads whole frames into a channel, so a select
/// branch that wins never leaves a frame half read.
async fn run<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    ch: Channel<S>,
    stop: &Notify,
    side: Side,
    clipboard: Option<Box<dyn crate::clipboard::Clipboard>>,
) -> Result<(), Failure> {
    let Channel {
        stream,
        mut noise,
        mut buf,
    } = ch;
    let (mut rd, mut wr) = tokio::io::split(stream);
    let (frames_tx, mut frames) = mpsc::channel::<Vec<u8>>(64);
    let _reader = AbortOnDrop(tokio::spawn(async move {
        while let Ok(f) = read_frame(&mut rd).await {
            if frames_tx.send(f).await.is_err() {
                break;
            }
        }
    }));
    // The host's video: the screen through H.264, or the test pattern.
    let mut source = match side {
        Side::Host { video: true, .. } => Some(video::Source::open()),
        _ => None,
    };
    // The host's input service; dropping it releases what it holds.
    #[cfg(windows)]
    let mut injector = match side {
        Side::Host { input: true, .. } => Some(crate::input::Injector::default()),
        _ => None,
    };
    if let Some(s) = &source {
        seal_json(
            &mut noise,
            &mut buf,
            &mut wr,
            &Message::VideoStart(s.start.clone()),
        )
        .await?;
    }
    let (queue, control) = match &side {
        Side::Controller { queue, control } => (Some(queue.clone()), Some(control.clone())),
        Side::Host { .. } => (None, None),
    };
    let host_ai = match &side {
        Side::Host { ai, .. } if ai.run || ai.approve => Some(ai),
        _ => None,
    };
    // Clipboard sharing, when the session may: local copies go out, the
    // other side's texts are put on the local clipboard.
    let mut clip = clipboard.map(crate::clipboard::share);
    let mut ai_sync = tokio::time::interval(AI_SYNC);
    // The host's pointer, which the captured picture leaves out.
    let mut pointer_tick = tokio::time::interval(POINTER_EVERY);
    let mut pointer_sent: Option<((i32, i32), bool)> = None;
    let mut ai_sent: Option<Value> = None;
    let mut control = match &control {
        Some(c) => Some(c.lock().await),
        None => None,
    };
    let mut reassembler = media::Reassembler::default();
    let mut frame_no: u32 = 0;
    let mut ping = tokio::time::interval(PING_EVERY);
    // RustDesk's QoS on the host: one probe a second, frames counted per
    // second, frame rate and bitrate applied to the source.
    let mut qos = rustdesk_qos::VideoQoS::default();
    let base_kbps = source
        .as_ref()
        .map(|s| rustdesk_qos::base_bitrate(s.start.width, s.start.height))
        .unwrap_or(0);
    if source.is_some() {
        qos.on_connection_open(QOS_VIEWER);
        qos.new_display(QOS_DISPLAY.into());
    }
    let mut probe = tokio::time::interval(Duration::from_secs(1));
    let mut probe_sent: Option<Instant> = None;
    let mut last_delay = 0u32;
    let mut sent_this_second = 0usize;
    let mut heard = Instant::now();
    let mut plain = vec![0u8; MAX_FRAME];
    let outcome: Result<(), Failure> = async { loop {
        tokio::select! {
            _ = stop.notified() => {
                let _ = seal_json(&mut noise, &mut buf, &mut wr, &Message::Bye).await;
                return Ok(());
            }
            _ = ai_sync.tick(), if host_ai.is_some() => {
                // The controller's task as it is now; sent again after a
                // reconnect, never replayed as commands.
                let h = host_ai.expect("checked");
                let now = h.ai.latest_for(&h.peer);
                if now.is_some() && now != ai_sent {
                    let task = now.clone().expect("checked");
                    seal_json(&mut noise, &mut buf, &mut wr, &Message::AiTask { task }).await?;
                    ai_sent = now;
                }
            }
            Some(text) = async {
                match clip.as_mut() {
                    Some(c) => c.offers.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                seal_json(&mut noise, &mut buf, &mut wr, &Message::Clipboard { text }).await?;
            }
            _ = pointer_tick.tick(), if source.is_some() => {
                if let Some((x, y, shown)) = crate::input::pointer() {
                    let visible = shown && (0.0..=1.0).contains(&x) && (0.0..=1.0).contains(&y);
                    let now = (((x * 4096.0) as i32, (y * 4096.0) as i32), visible);
                    if pointer_sent != Some(now) {
                        pointer_sent = Some(now);
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::Cursor { x, y, visible }).await?;
                    }
                }
            }
            _ = ping.tick() => {
                if heard.elapsed() > IDLE_LIMIT {
                    return Err(Failure::Broken);
                }
                seal_json(&mut noise, &mut buf, &mut wr, &Message::Ping).await?;
            }
            _ = probe.tick(), if source.is_some() => {
                match probe_sent {
                    None => {
                        probe_sent = Some(Instant::now());
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::TestDelay { reply: false }).await?;
                    }
                    Some(t) => qos.user_delay_response_elapsed(QOS_VIEWER, t.elapsed().as_millis()),
                }
                qos.update_display_data(QOS_DISPLAY, sent_this_second);
                sent_this_second = 0;
                let fps = qos.fps();
                let kbps = (base_kbps as f32 * qos.ratio()) as u32;
                qos.store_bitrate(kbps);
                if let Some(s) = &source {
                    s.set_targets(fps, kbps.saturating_mul(1000));
                }
                let stats = Message::Stats { delay_ms: last_delay, fps, bitrate: kbps.saturating_mul(1000) };
                seal_json(&mut noise, &mut buf, &mut wr, &stats).await?;
            }
            Some((flags, pts, data)) = async {
                match source.as_mut() {
                    Some(s) => s.frames.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                frame_no = frame_no.wrapping_add(1);
                sent_this_second += 1;
                for chunk in media::chunks(frame_no, flags, pts, &data, MAX_PLAINTEXT) {
                    seal(&mut noise, &mut buf, &mut wr, &chunk).await?;
                }
            }
            Some(msg) = async {
                match control.as_mut() {
                    Some(rx) => rx.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                // Only messages a GUI may send pass on to the host.
                if let Ok(m @ (Message::KeyFrame | Message::Input { .. } | Message::Quality { .. } | Message::AiStart { .. } | Message::AiCommand { .. } | Message::AiDecide { .. })) = serde_json::from_value::<Message>(msg) {
                    seal_json(&mut noise, &mut buf, &mut wr, &m).await?;
                }
            }
            frame = frames.recv() => {
                let frame = frame.ok_or(Failure::Broken)?;
                heard = Instant::now();
                let n = noise.read_message(&frame, &mut plain)?;
                let plain = &plain[..n];
                if plain.first() == Some(&media::CHUNK_TAG) {
                    if let (Some(q), Some(f)) = (&queue, reassembler.push(plain)) {
                        let mut packet = Vec::with_capacity(10 + f.data.len());
                        packet.push(media::packet::FRAME);
                        packet.push(f.flags);
                        packet.extend_from_slice(&f.pts_us.to_be_bytes());
                        packet.extend_from_slice(&f.data);
                        q.push(packet);
                    }
                    continue;
                }
                match serde_json::from_slice::<Message>(plain).map_err(|_| Failure::Broken)? {
                    Message::Ping => seal_json(&mut noise, &mut buf, &mut wr, &Message::Pong).await?,
                    Message::Pong => {}
                    Message::Quality { preset, fps } => {
                        let quality = match preset.as_str() {
                            "best" => Some(rustdesk_qos::ImageQuality::Best),
                            "balanced" => Some(rustdesk_qos::ImageQuality::Balanced),
                            "speed" => Some(rustdesk_qos::ImageQuality::Low),
                            _ => None,
                        };
                        if let Some(q) = quality {
                            qos.user_image_quality(QOS_VIEWER, q.value());
                        }
                        if (1..=video::MAX_FPS).contains(&fps) {
                            qos.user_custom_fps(QOS_VIEWER, fps);
                        }
                    }
                    // The controller echoes probes at once; the host measures them.
                    Message::TestDelay { reply: false } => {
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::TestDelay { reply: true }).await?;
                    }
                    Message::TestDelay { reply: true } => {
                        if let Some(t) = probe_sent.take() {
                            last_delay = t.elapsed().as_millis() as u32;
                            qos.user_network_delay(QOS_VIEWER, last_delay);
                        }
                    }
                    Message::Stats { delay_ms, fps, bitrate } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::STATS];
                            packet.extend(serde_json::to_vec(&json!({"delay_ms": delay_ms, "fps": fps, "bitrate": bitrate})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::Bye => return Ok(()),
                    m @ (Message::AiStart { .. } | Message::AiCommand { .. } | Message::AiDecide { .. }) => {
                        // Only a host answers AI requests.
                        if let Side::Host { ai, .. } = &side
                            && let Err(error) = ai.handle(m)
                        {
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::AiError { error }).await?;
                        }
                    }
                    Message::Clipboard { text } => {
                        if let Some(c) = &clip
                            && text.len() <= crate::clipboard::MAX_TEXT
                        {
                            let _ = c.writes.send(text);
                        }
                    }
                    Message::Cursor { x, y, visible } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::CURSOR];
                            packet.extend(serde_json::to_vec(&json!({"x": x, "y": y, "visible": visible})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::AiTask { task } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::AI_TASK];
                            packet.extend(serde_json::to_vec(&task).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::AiError { error } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::AI_ERROR];
                            packet.extend(serde_json::to_vec(&json!({"error": error})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::VideoStart(v) => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::VIDEO_START];
                            packet.extend(serde_json::to_vec(&v).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    // A viewer that (re)opens asks for a key frame: describe the
                    // stream again and start a new group of pictures.
                    Message::KeyFrame => {
                        if let Some(s) = &source {
                            s.key_frame();
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::VideoStart(s.start.clone())).await?;
                        }
                    }
                    // Input reaches the host only with control-input; a
                    // malformed event is dropped, not fatal.
                    // A person's input takes over from the host's AI task:
                    // it pauses before the event is injected. Pointer moves
                    // alone (a hand resting on the viewer) neither take over
                    // nor reach the desktop while the AI acts.
                    #[cfg(windows)]
                    Message::Input { event } => {
                        if let (Some(inj), Ok(e), Side::Host { ai, .. }) = (injector.as_mut(), serde_json::from_value::<crate::input::Event>(event), &side) {
                            let hover = matches!(e, crate::input::Event::Pointer { action: crate::input::PointerAction::Move, .. });
                            if !(hover && ai.ai.acting()) {
                                ai.ai.take_over();
                                inj.inject(&e);
                            }
                        }
                    }
                    #[cfg(not(windows))]
                    Message::Input { .. } => {}
                }
            }
        }
    } }
    .await;
    // A write that failed because the peer left after saying bye (its bye
    // waiting behind our frames) is an orderly close, not a broken one.
    if outcome.is_err() {
        while let Ok(frame) = frames.try_recv() {
            if let Ok(n) = noise.read_message(&frame, &mut plain)
                && matches!(
                    serde_json::from_slice::<Message>(&plain[..n]),
                    Ok(Message::Bye)
                )
            {
                return Ok(());
            }
        }
    }
    outcome
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ai::fake;
    use crate::store::Secrets;
    use std::sync::atomic::{AtomicU32, Ordering};

    /// A device whose AI runs on the fake desktop and reaches no provider.
    fn device(version: &str) -> Arc<Ctx> {
        static N: AtomicU32 = AtomicU32::new(0);
        let n = N.fetch_add(1, Ordering::Relaxed);
        let rig = fake::rig(&format!("session-{n}"), "claude", "http://127.0.0.1:9");
        device_with(rig.ai.clone(), version)
    }

    fn device_with(ai: Arc<crate::ai::Ai>, version: &str) -> Arc<Ctx> {
        device_full(ai, Arc::default(), version)
    }

    /// A device whose clipboard is `board`.
    fn device_full(
        ai: Arc<crate::ai::Ai>,
        board: crate::clipboard::fake::Board,
        version: &str,
    ) -> Arc<Ctx> {
        let secrets = Arc::new(Secrets::memory());
        Arc::new(Ctx {
            clipboard: crate::clipboard::fake::opener(board),
            ai,
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
        let want = vec![Permission::ViewScreen, Permission::ControlInput];
        attempt_with(host, controller, dialed, password, decide, want).await
    }

    /// `attempt`, asking for `want`.
    async fn attempt_with(
        host: &Arc<Ctx>,
        controller: &Arc<Ctx>,
        dialed: &str,
        password: &str,
        decide: Option<Vec<Permission>>,
        want: Vec<Permission>,
    ) -> u64 {
        let (a, b) = tokio::io::duplex(1 << 16);
        let from: IpAddr = "10.126.126.2".parse().unwrap();
        tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(controller, dialed);
        let c = controller.clone();
        let (dialed_owned, password) = (
            dialed.to_owned(),
            zeroize::Zeroizing::new(password.to_owned()),
        );
        tokio::spawn(async move {
            // As the dialer does: a wrong device key ends the attempt here.
            if let Outcome::WrongDevice =
                connect(c.clone(), id, stop, a, dialed_owned, password, want, None).await
            {
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

    /// Reads the controller's media until an AI packet satisfies `f`.
    fn ai_packet(queue: &media::Queue, what: &str, f: impl Fn(u8, &Value) -> bool) -> Value {
        let until = Instant::now() + Duration::from_secs(20);
        while Instant::now() < until {
            let Ok(p) = queue.pop(Duration::from_millis(200)) else {
                continue;
            };
            if let kind @ (media::packet::AI_TASK | media::packet::AI_ERROR) = p[0] {
                let v: Value = serde_json::from_slice(&p[1..]).unwrap();
                if f(kind, &v) {
                    return v;
                }
            }
        }
        panic!("no AI packet: {what}");
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_controller_runs_and_approves_an_ai_task_on_the_host() {
        let server = fake::server(vec![
            (0, fake::claude_turn("m1", fake::click_save())),
            (0, fake::claude_end("Saved the file.")),
        ]);
        let rig = fake::rig("session-ai", "claude", &server.url);
        rig.ai
            .set_settings(&json!({"approval": "every-action"}))
            .unwrap();
        let host = device_with(rig.ai.clone(), "1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let ai = vec![Permission::RunAiTask, Permission::ApproveAiAction];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(ai.clone()),
            ai,
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        let (queue, control) = controller.sessions.media(id).unwrap();
        let start = json!({"type": "ai-start", "key": "k1", "instruction": "Save the file"});
        control.try_send(start.clone()).unwrap();
        // A repeated start (as after a reconnect) starts one task.
        control.try_send(start).unwrap();
        let task = ai_packet(&queue, "approval", |_, t| t["state"] == "approval");
        assert_eq!(task["origin"], controller.identity.device_id());
        assert_eq!(task["approval"]["action"]["kind"], "click");
        let tid = task["id"].as_u64().unwrap();
        // The click, then the screenshot, each approved from the controller.
        let approve = json!({"type": "ai-decide", "id": tid, "approve": true});
        control.try_send(approve.clone()).unwrap();
        ai_packet(&queue, "the screenshot's approval", |_, t| {
            t["state"] == "approval" && t["steps"].as_array().unwrap().len() == 1
        });
        control.try_send(approve).unwrap();
        let done = ai_packet(&queue, "done", |_, t| t["state"] == "done");
        assert_eq!(done["text"], "Saved the file.");
        assert!(
            rig.log.lock().unwrap().pressed,
            "the AI pressed SAVE on the host"
        );
        assert_eq!(
            rig.ai.tasks().as_array().unwrap().len(),
            1,
            "one task for one key"
        );
        // A task the host's own user starts is not the controller's to steer.
        let own = rig.ai.start("host", "Something else", None).unwrap();
        control
            .try_send(json!({"type": "ai-command", "id": own, "command": "cancel"}))
            .unwrap();
        ai_packet(&queue, "refusal", |k, _| k == media::packet::AI_ERROR);
        assert_ne!(rig.task(own)["state"], "cancelled");
        rig.ai.cancel(own).unwrap();
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn ai_requests_need_their_permissions() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::ViewScreen];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(want.clone()),
            want,
        )
        .await;
        let (queue, control) = controller.sessions.media(id).unwrap();
        control
            .try_send(json!({"type": "ai-start", "key": "k", "instruction": "Open the settings"}))
            .unwrap();
        let refused = ai_packet(&queue, "refusal", |k, _| k == media::packet::AI_ERROR);
        assert!(refused["error"].as_str().unwrap().contains("may not"));
        assert_eq!(
            host.ai.tasks(),
            json!([]),
            "view-screen alone starts no AI task"
        );
    }

    // Multithreaded: the test waits for media with a blocking pop, which
    // must not stop the session tasks.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
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
        // Video flows: the stream description, then whole frames: the screen
        // through H.264 where this machine can capture, else the test pattern.
        let (queue, _) = controller.sessions.media(id).unwrap();
        let start = queue.pop(Duration::from_secs(5)).unwrap();
        assert_eq!(start[0], media::packet::VIDEO_START);
        let v: media::VideoStart = serde_json::from_slice(&start[1..]).unwrap();
        // The host's QoS stats and pointer arrive between frames; the test
        // wants frames.
        let mut stats = 0;
        let frame = loop {
            let p = queue.pop(Duration::from_secs(5)).unwrap();
            if p[0] == media::packet::STATS {
                stats += 1;
                continue;
            }
            if p[0] == media::packet::CURSOR {
                continue;
            }
            break p;
        };
        assert_eq!(frame[0], media::packet::FRAME);
        // Within two seconds a stats report (delay, frame rate, bitrate) arrives.
        let until = Instant::now() + Duration::from_secs(3);
        while stats == 0 && Instant::now() < until {
            if let Ok(p) = queue.pop(Duration::from_millis(200)) {
                stats += (p[0] == media::packet::STATS) as usize;
            }
        }
        assert!(stats > 0, "no QoS stats from the host");
        match v.codec.as_str() {
            "raw-rgba" => assert_eq!(frame.len(), 1 + 1 + 8 + 320 * 180 * 4),
            "h264" => assert_eq!(
                frame[1] & media::KEY_FRAME,
                media::KEY_FRAME,
                "a stream starts with a key frame"
            ),
            other => panic!("unexpected codec {other}"),
        }
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
    async fn a_dropped_session_resumes_without_a_new_login() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let from: IpAddr = "10.126.126.2".parse().unwrap();
        let (a, b) = tokio::io::duplex(1 << 16);
        let host_side = tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(&controller, &dialed);
        let first = tokio::spawn(connect(
            controller.clone(),
            id,
            stop.clone(),
            a,
            dialed.clone(),
            zeroize::Zeroizing::new("k7mq2xpa".into()),
            vec![Permission::ViewScreen],
            None,
        ));
        let until = |want: &'static str| {
            let controller = controller.clone();
            async move {
                for _ in 0..200 {
                    if session(&controller, id)["state"] == want {
                        return;
                    }
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                panic!("never {want}: {}", session(&controller, id));
            }
        };
        for _ in 0..200 {
            if let Some(q) = host
                .access
                .pending()
                .as_array()
                .and_then(|a| a.first().cloned())
            {
                host.access
                    .decide(
                        q["id"].as_u64().unwrap(),
                        Some(vec![Permission::ViewScreen]),
                    )
                    .unwrap();
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        until("connected").await;
        // The connection drops (the host side goes away).
        host_side.abort();
        let Outcome::Lost(token) = first.await.unwrap() else {
            panic!("a dropped session should be resumable");
        };
        assert_eq!(session(&controller, id)["state"], "reconnecting");
        // A new connection resumes: no password check, no confirmation.
        let (a, b) = tokio::io::duplex(1 << 16);
        tokio::spawn(serve(host.clone(), b, from));
        tokio::spawn(connect(
            controller.clone(),
            id,
            stop,
            a,
            dialed,
            zeroize::Zeroizing::new("not the password".into()),
            vec![Permission::ViewScreen],
            Some(token),
        ));
        until("connected").await;
        assert_eq!(session(&controller, id)["granted"], json!(["view-screen"]));
        assert_eq!(host.access.pending(), json!([]));
    }

    /// A small xorshift generator: the fuzz rounds are random but repeatable.
    fn rng(seed: u64) -> impl FnMut() -> u64 {
        let mut s = seed;
        move || {
            s ^= s << 13;
            s ^= s >> 7;
            s ^= s << 17;
            s
        }
    }

    /// Writes 1–3 random frames, with a well-formed length prefix or none.
    async fn garbage(
        w: &mut (impl AsyncWrite + Unpin),
        next: &mut impl FnMut() -> u64,
        prefixed: bool,
    ) {
        for _ in 0..next() % 3 + 1 {
            let len = (next() % 700) as usize;
            let bytes: Vec<u8> = (0..len).map(|_| next() as u8).collect();
            if prefixed {
                let _ = w.write_u16(len as u16).await;
            }
            let _ = w.write_all(&bytes).await;
        }
    }

    #[tokio::test]
    async fn garbage_never_panics_and_always_ends_the_host_side() {
        let host = device("1.0.0");
        let from: IpAddr = "10.126.126.9".parse().unwrap();
        let mut next = rng(0x9e37_79b9_7f4a_7c15);
        for round in 0..400 {
            let (mut a, b) = tokio::io::duplex(1 << 16);
            let served = tokio::spawn(serve(host.clone(), b, from));
            if round % 3 == 2 {
                // A real first message, then garbage where frame 3 belongs.
                let (mut hs, _) = noise(true).unwrap();
                let mut buf = vec![0u8; MAX_FRAME];
                let n = hs.write_message(&[], &mut buf).unwrap();
                write_frame(&mut a, &buf[..n]).await.unwrap();
                let _ = read_frame(&mut a).await;
            }
            garbage(&mut a, &mut next, round % 2 == 0).await;
            drop(a);
            tokio::time::timeout(Duration::from_secs(5), served)
                .await
                .expect("the host gives up on garbage")
                .expect("no panic");
        }
        assert!(host.access.pending().as_array().unwrap().is_empty());
        let sessions = host.sessions.list();
        assert!(
            sessions
                .as_array()
                .unwrap()
                .iter()
                .all(|s| s["state"] == "failed" || s["state"] == "closed"),
            "{sessions}"
        );
    }

    #[tokio::test]
    async fn garbage_never_panics_the_controller() {
        let controller = device("1.0.0");
        let mut next = rng(0x2545_f491_4f6c_dd1d);
        for round in 0..200 {
            let (a, mut b) = tokio::io::duplex(1 << 16);
            let (id, stop) = begin(&controller, "100000002");
            let dialing = tokio::spawn(connect(
                controller.clone(),
                id,
                stop,
                a,
                "100000002".into(),
                zeroize::Zeroizing::new("k7mq2xpa".into()),
                vec![Permission::ViewScreen],
                None,
            ));
            let _ = read_frame(&mut b).await;
            garbage(&mut b, &mut next, round % 2 == 0).await;
            drop(b);
            let outcome = tokio::time::timeout(Duration::from_secs(5), dialing)
                .await
                .expect("the controller gives up on garbage")
                .expect("no panic");
            assert!(!matches!(outcome, Outcome::Lost(_)), "nothing to resume");
        }
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

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn revoking_a_device_ends_its_sessions_and_its_resumes() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::Clipboard];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(want.clone()),
            want,
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        let key = STANDARD.encode(controller.identity.public_key());
        assert!(host.access.trusted().contains_key(&key));
        untrust(&host, &key).unwrap();
        assert!(!host.access.trusted().contains_key(&key));
        // The host's side ends at once; the controller cannot resume it.
        for _ in 0..100 {
            if state(&host)["state"] == "closed" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(state(&host)["state"], "closed");
        tokio::time::sleep(Duration::from_millis(300)).await;
        let s = session(&controller, id);
        assert_ne!(s["state"], "connected", "{s}");
        assert!(untrust(&host, &key).is_err(), "already revoked");
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_controllers_task_pauses_when_its_session_ends() {
        let server = fake::server(vec![(3000, fake::claude_end("late"))]);
        let rig = fake::rig("session-left", "claude", &server.url);
        let host = device_with(rig.ai.clone(), "1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::RunAiTask];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(want.clone()),
            want,
        )
        .await;
        let (queue, control) = controller.sessions.media(id).unwrap();
        control
            .try_send(json!({"type": "ai-start", "key": "k", "instruction": "Save the file"}))
            .unwrap();
        let task = ai_packet(&queue, "running", |_, t| t["state"] == "running");
        server.wait_for(1);
        controller.sessions.close(id).unwrap();
        let t = rig.until(task["id"].as_u64().unwrap(), "paused", |t| {
            t["state"] == "paused"
        });
        assert_eq!(t["paused"], "disconnected");
    }

    /// Waits until a fake clipboard holds `want`.
    async fn board_holds(board: &crate::clipboard::fake::Board, want: &str) -> bool {
        for _ in 0..150 {
            if board.lock().unwrap().as_deref() == Some(want) {
                return true;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        false
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_clipboard_is_shared_both_ways_only_with_its_permission() {
        let (host_board, controller_board) = (
            Arc::new(std::sync::Mutex::new(Some(
                "on the host before".to_string(),
            ))),
            Arc::new(std::sync::Mutex::new(None)),
        );
        let rig = fake::rig("session-clip", "claude", "http://127.0.0.1:9");
        let host = device_full(rig.ai.clone(), host_board.clone(), "1.0.0");
        let controller = device_full(rig.ai.clone(), controller_board.clone(), "1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::Clipboard];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(want.clone()),
            want,
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        // What was on a clipboard before the session is not offered.
        tokio::time::sleep(Duration::from_millis(700)).await;
        assert_eq!(*controller_board.lock().unwrap(), None);
        *controller_board.lock().unwrap() = Some("copied on the controller".into());
        assert!(board_holds(&host_board, "copied on the controller").await);
        *host_board.lock().unwrap() = Some("copied on the host".into());
        assert!(board_holds(&controller_board, "copied on the host").await);
        controller.sessions.close(id).unwrap();

        // Without the permission nothing crosses (a second controller).
        let other_board: crate::clipboard::fake::Board = Arc::default();
        let other = device_full(rig.ai.clone(), other_board.clone(), "1.0.2");
        let want = vec![Permission::ViewScreen];
        let id = attempt_with(&host, &other, &dialed, "k7mq2xpa", Some(want.clone()), want).await;
        assert_eq!(session(&other, id)["state"], "connected");
        *other_board.lock().unwrap() = Some("private".into());
        assert!(!board_holds(&host_board, "private").await);
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_host_serves_a_few_sessions_at_once_and_refuses_more_as_busy() {
        let host = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::Clipboard];
        let mut controllers = Vec::new();
        for _ in 0..MAX_HOST_SESSIONS {
            let c = device("1.0.1");
            let id = attempt_with(
                &host,
                &c,
                &dialed,
                "k7mq2xpa",
                Some(want.clone()),
                want.clone(),
            )
            .await;
            assert_eq!(session(&c, id)["state"], "connected");
            controllers.push(c);
        }
        let extra = device("1.0.1");
        let id = attempt_with(&host, &extra, &dialed, "k7mq2xpa", Some(want.clone()), want).await;
        assert_eq!(session(&extra, id)["failure"], json!({"refused": "busy"}));
    }
}
