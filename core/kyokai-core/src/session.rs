//! Sessions between a controller and a host (doc/en-us/protocol.md): a Noise
//! XX handshake that proves both device keys, OPAQUE password login bound to
//! the handshake, the host user's confirmation, then a heartbeat until
//! either side leaves. Generic over the stream, so tests run it in memory.

use crate::about::About;
use crate::access::{Access, CONFIRM_TIMEOUT, Permission, Refusal, Serve};
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

use crate::{audio, media, video};

/// Version of the wire protocol; a mismatch closes the connection.
pub const PROTOCOL: u32 = 10;

/// How many frames the host sends ahead of the controller's
/// acknowledgements: enough for the frame-rate cap over a relay's round
/// trip, few enough that a slow path delays the picture by a few frames,
/// not seconds.
const IN_FLIGHT: u32 = 3;
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
/// How long a session that says goodbye waits for the peer to close.
const BYE_WAIT: Duration = Duration::from_millis(500);
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
    /// One OPAQUE credential request per password slot; None asks the
    /// host's user without a password.
    requests: Option<[String; 2]>,
    /// Without a password: the controller's description, for the host's
    /// user to judge the request by.
    #[serde(default)]
    about: Option<About>,
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
    /// The controller's description, used only once the login holds.
    #[serde(default)]
    about: Option<About>,
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
    /// With an acceptance: the host's description.
    #[serde(default)]
    about: Option<About>,
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
    /// controller → host: every chunk of this frame (and of the ones
    /// before it) arrived; the host keeps at most `IN_FLIGHT` frames
    /// unacknowledged.
    Received {
        frame: u32,
    },
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
    /// `speed`), frame-rate cap (RustDesk's presets) and whether it wants
    /// full colour (4:4:4) where both sides do it.
    Quality {
        preset: String,
        fps: u32,
        #[serde(default)]
        i444: bool,
    },
    /// host → controller: what QoS measured and chose, for the viewer, and
    /// how long the host took from a picture to its frame (the average of
    /// the last second's frames).
    Stats {
        delay_ms: u32,
        fps: u32,
        bitrate: u32,
        #[serde(default)]
        encode_ms: u32,
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
    /// either way (`run-ai-task`): the shared draft of the next instruction,
    /// as typed on any side; the host passes it to every controller.
    AiDraft {
        text: String,
    },
    /// either way (`clipboard`): text copied on the sender's side.
    Clipboard {
        text: String,
    },
    /// either way (`clipboard`): an image copied on the sender's side, as
    /// base64 PNG (8-bit RGBA).
    ClipboardImage {
        png: String,
    },
    /// either way (`files`): files copied on the sender's side, offered by
    /// path and size; the contents follow a `files-get`.
    ClipboardFiles {
        id: u64,
        files: Vec<crate::files::Entry>,
    },
    /// either way (`files`): accepts an offer; send its contents.
    FilesGet {
        id: u64,
    },
    /// either way (`files`): the receiver wants no more of an offer.
    FilesStop {
        id: u64,
    },
    /// either way (`files`): the sender cannot finish an accepted offer.
    FilesCancel {
        id: u64,
    },
    /// host → controller, with video: the host's displays and the one
    /// shown.
    Displays {
        list: Vec<video::Display>,
        current: usize,
    },
    /// controller → host (`view-screen`): show this display instead.
    Display {
        index: usize,
    },
    /// controller → host, as the session starts: the codecs it decodes,
    /// and those it decodes in full colour (4:4:4).
    Codecs {
        decode: Vec<String>,
        #[serde(default)]
        i444: Vec<String>,
    },
    /// host → controller, after `codecs`: what the host can send that the
    /// controller decodes, in the host's order, for the viewer to choose.
    CodecOffer {
        codecs: Vec<String>,
    },
    /// controller → host: the viewer's codec, `auto` or one of the offer.
    Codec {
        codec: String,
    },
    /// controller → host: whether the viewer plays the host's sound; the
    /// host captures it only while asked and the screen may be viewed.
    Audio {
        on: bool,
    },
    /// host → controller, with video: where the host's pointer is, as
    /// fractions of the picture, and whether it shows there.
    Cursor {
        x: f32,
        y: f32,
        visible: bool,
    },
    /// controller → host (`control-input`): lock the screen, send
    /// Ctrl+Alt+Del, or block or unblock the host's own input.
    Action {
        action: SessionAction,
    },
    /// host → controller (`control-input`), as the session starts and when
    /// the block or privacy mode changes: whether Ctrl+Alt+Del can be sent,
    /// whether the host's input is blocked and whether privacy mode is on
    /// for this session.
    Actions {
        ctrl_alt_del: bool,
        blocked: bool,
        privacy: bool,
    },
}

/// What a controller may do to the host's session (RustDesk's session
/// actions).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum SessionAction {
    Lock,
    CtrlAltDel,
    Block,
    Unblock,
    PrivacyOn,
    PrivacyOff,
}

/// The session actions as the host offers them now.
#[cfg(windows)]
fn offered(inj: &crate::input::Injector) -> Message {
    Message::Actions {
        ctrl_alt_del: inj.can_ctrl_alt_del(),
        blocked: inj.input_blocked(),
        privacy: inj.privacy_on(),
    }
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

/// How long a host waits for the controller's codecs before it starts the
/// video as for a controller that names none (H.264 on Windows).
const CODECS_WAIT: Duration = Duration::from_secs(1);

/// Describes a newly opened video to the controller: the stream, and the
/// displays when there is a choice.
async fn announce<W: AsyncWrite + Unpin>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    s: &video::Source,
    displays: &[video::Display],
) -> Result<(), Failure> {
    seal_json(noise, buf, w, &Message::VideoStart(s.start.clone())).await?;
    if let Some(m) = shown(displays, s) {
        seal_json(noise, buf, w, &m).await?;
    }
    Ok(())
}

/// A display as the input service's area.
fn area(d: video::Display) -> crate::input::Area {
    crate::input::Area {
        x: d.x,
        y: d.y,
        width: d.width,
        height: d.height,
    }
}

/// The displays message for a source, when there is a choice and it shows
/// one of them.
fn shown(displays: &[video::Display], s: &video::Source) -> Option<Message> {
    if displays.len() < 2 {
        return None;
    }
    let current = displays.iter().position(|d| Some(*d) == s.display)?;
    Some(Message::Displays {
        list: displays.to_vec(),
        current,
    })
}

/// How often the host looks where its pointer is (with video); it reports
/// only a change, so the viewer's ring follows a moving pointer smoothly.
const POINTER_EVERY: Duration = Duration::from_millis(16);

/// How long after a person's input the host sends at the viewer's frame-rate
/// cap: dragging and typing need every frame, not the QoS's slow climb from
/// its start (RustDesk's QoS knows nothing of input).
const INPUT_BOOST: Duration = Duration::from_secs(2);
/// Above this delay the network cannot carry more frames: the QoS decides.
const BOOST_DELAY_MS: u32 = 100;

/// The frame rate to send: the QoS's, or the viewer's cap while a person's
/// input came within INPUT_BOOST and the delay is low.
fn boosted(qos_fps: u32, cap: u32, input_at: Option<Instant>, delay_ms: u32) -> u32 {
    match input_at {
        Some(t) if t.elapsed() < INPUT_BOOST && delay_ms < BOOST_DELAY_MS => qos_fps.max(cap),
        _ => qos_fps,
    }
}

/// How often a controller whose GUI lost frames asks the host for a key
/// frame at most.
const KEY_AGAIN: Duration = Duration::from_secs(1);

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
    /// What the other device said about itself after the login.
    peer: About,
    stop: Arc<Notify>,
    /// Controller side: media for the GUI, and the GUI's control messages.
    media: Arc<media::Queue>,
    control_tx: mpsc::Sender<Value>,
    control_rx: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
    /// Controller side: the viewer's latest choices, for a resumed session.
    choices: Arc<Mutex<Choices>>,
    /// Controller side: what to do with the password once the host answers.
    remember: Remember,
    /// Controller side: the address dialed, or empty over the network.
    address: String,
    /// Removed from the interface's list (Home's Remove): left out of the
    /// status, and dropped once it ends.
    removed: bool,
}

/// A viewer's latest quality and codec messages: sent again when its
/// session resumes, since the host starts each connection with defaults.
/// They are settings, not input: nothing is replayed that acts.
#[derive(Default)]
pub struct Choices {
    quality: Option<Value>,
    codec: Option<Value>,
    audio: Option<Value>,
    /// The viewer's clipboard choice, which stays on this side.
    clipboard: ClipUse,
}

/// A viewer's clipboard choice for its session (the GUI's local
/// `clipboard-use` message, never sent to the host): whether the session
/// shares the clipboard at all, and whether its tab is the one in front,
/// which alone exchanges it (doc/en-us/computer-use.md, "Clipboard").
/// Both are on until the viewer says otherwise, as an older GUI never does.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ClipUse {
    pub share: bool,
    pub front: bool,
}

impl Default for ClipUse {
    fn default() -> Self {
        ClipUse {
            share: true,
            front: true,
        }
    }
}

/// What crossed a session's clipboard, for the viewer's history.
enum Crossed<'a> {
    Text(&'a str),
    Png(&'a str),
    Files(&'a [crate::files::Entry]),
}

/// Adds what crossed a controller's session to its viewer's clipboard
/// history (media CLIPBOARD): `from` is "controller" for what this side
/// sent, "host" for what the host's side put on this computer's clipboard.
/// Notes: a host has no queue, and adds nothing.
fn clip_history(queue: &Option<Arc<media::Queue>>, from: &str, what: Crossed) {
    let Some(q) = queue else {
        return;
    };
    let at = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_millis() as u64);
    let body = match what {
        Crossed::Text(text) => json!({"from": from, "at": at, "text": text}),
        Crossed::Png(png) => json!({"from": from, "at": at, "png": png}),
        Crossed::Files(files) => json!({"from": from, "at": at, "files": files}),
    };
    let mut packet = vec![media::packet::CLIPBOARD];
    if let Ok(b) = serde_json::to_vec(&body) {
        packet.extend(b);
        q.push(packet);
    }
}

/// What a controller does with a session's password once the host answers
/// its login (doc/en-us/identity-and-access.md, "Saved passwords").
#[derive(Clone, Copy, Default)]
pub struct Remember {
    /// Keep the password for this host after a login it accepts.
    pub save: bool,
    /// The password came from the saved ones: forget it when the host
    /// refuses it.
    pub saved: bool,
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
            peer: About::default(),
            stop: stop.clone(),
            media: Arc::default(),
            control_tx,
            control_rx: Arc::new(tokio::sync::Mutex::new(control_rx)),
            choices: Arc::default(),
            remember: Remember::default(),
            address: String::new(),
            removed: false,
        });
        (id, stop)
    }

    fn update(&self, id: u64, f: impl FnOnce(&mut Entry)) {
        let mut s = self.inner.lock().expect("sessions lock");
        if let Some(e) = s.1.iter_mut().find(|e| e.id == id) {
            f(e);
        }
        s.1.retain(|e| !(e.removed && matches!(e.state, State::Closed | State::Failed)));
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
            choices: e.choices.clone(),
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
    /// Ends every session in which this device is the host.
    pub fn close_hosts(&self) {
        let s = self.inner.lock().expect("sessions lock");
        for e in s.1.iter().filter(|e| e.role == Role::Host) {
            e.stop.notify_one();
        }
    }

    /// Ends every session, each with a goodbye to its peer, so the other
    /// device ends it at once instead of waiting for the connection to die.
    pub fn close_all(&self) {
        let s = self.inner.lock().expect("sessions lock");
        for e in
            s.1.iter()
                .filter(|e| !matches!(e.state, State::Closed | State::Failed))
        {
            e.stop.notify_one();
        }
    }

    /// Whether any session is connected (and so has a goodbye to send).
    pub fn any_connected(&self) -> bool {
        let s = self.inner.lock().expect("sessions lock");
        s.1.iter().any(|e| e.state == State::Connected)
    }

    pub fn close(&self, id: u64) -> Result<(), String> {
        let s = self.inner.lock().map_err(|_| "sessions lock poisoned")?;
        let e = s.1.iter().find(|e| e.id == id).ok_or("no such session")?;
        e.stop.notify_one();
        Ok(())
    }

    /// Takes a host off the interface's list: its sessions from here end
    /// and are no longer listed (Home's Remove).
    pub fn remove_host(&self, device_id: &str) {
        let mut s = self.inner.lock().expect("sessions lock");
        s.1.retain(|e| {
            !(e.role == Role::Controller
                && e.device_id == device_id
                && matches!(e.state, State::Closed | State::Failed))
        });
        for e in
            s.1.iter_mut()
                .filter(|e| e.role == Role::Controller && e.device_id == device_id)
        {
            e.removed = true;
            e.stop.notify_one();
        }
    }

    /// The sessions, for the status (never a secret).
    pub fn list(&self) -> Value {
        let s = self.inner.lock().expect("sessions lock");
        s.1.iter()
            .filter(|e| !e.removed)
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
                    "peer": e.peer,
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
    /// This device's friendly name, maker and system.
    pub about: Mutex<crate::about::Local>,
}

impl Ctx {
    /// What this device tells a peer after the login.
    pub fn about(&self) -> About {
        self.about.lock().map(|l| l.about()).unwrap_or_default()
    }
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
    // No flush: EasyTier's TCP flush waits until the peer acknowledged
    // everything sent, which would allow one message per round trip. The
    // bytes leave as soon as they are written.
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
        Ok((granted, peer)) => {
            ctx.sessions.update(id, |e| e.peer = peer);
            let video = granted.contains(&Permission::ViewScreen);
            let input = granted.contains(&Permission::ControlInput);
            let sound = granted.contains(&Permission::HearSound);
            let clipboard = shared(&ctx, &granted);
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
            let failure = run(
                ch,
                &stop,
                Side::Host {
                    video,
                    sound,
                    input,
                    ai,
                },
                clipboard,
            )
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
/// Returns: the granted permissions and the controller's description,
/// after telling the controller.
/// Notes: a correct password admits at once within the host's password
/// grants (or an unattended grant); anything more, and a controller that
/// asks without a password, waits for the host's user.
async fn host_login<S: AsyncRead + AsyncWrite + Unpin>(
    ctx: &Ctx,
    ch: &mut Channel<S>,
    client: &ClientHello,
    client_key: &[u8; 32],
    context: &[u8],
    from: IpAddr,
    id: u64,
) -> Result<(Vec<Permission>, About), Failure> {
    let key_b64 = STANDARD.encode(client_key);
    let pinned = ctx.access.trusted().contains_key(&key_b64);
    let refuse = |reason| Verdict {
        accepted: false,
        granted: Vec::new(),
        reason: Some(reason),
        resume: None,
        about: None,
    };
    if let Err(reason) = ctx.access.admit(from, pinned) {
        let _ = ch.send(&refuse(reason)).await;
        return Err(Failure::Refused(reason));
    }
    if ctx.access.serve() == Serve::None {
        let _ = ch.send(&refuse(Refusal::Absent)).await;
        return Err(Failure::Refused(Refusal::Absent));
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
            about: Some(ctx.about()),
        }))
        .await?;
        // A resumed controller sent no description: the last one stands.
        let peer = ctx
            .access
            .trusted()
            .remove(&key_b64)
            .map(|t| t.about)
            .unwrap_or_default();
        return Ok((granted, peer));
    }
    // The slot the password opened, or None when the controller only asks.
    let (slot, about) = match &client.requests {
        Some(requests) => {
            let host_key = ctx.identity.public_key();
            let (responses, half) = ctx
                .access
                .slots
                .lock()
                .map_err(|_| Failure::Broken)?
                .respond(requests, &host_key, context)
                .map_err(|_| Failure::Broken)?;
            step(ch.send(&Challenge { responses })).await?;
            let login: Login = step(ch.recv()).await?;
            let opened = match (login.slot, login.finalization) {
                (Some(slot), Some(fin)) if half.finish(slot, &fin, &host_key, context) => {
                    Some(slot)
                }
                _ => None,
            };
            // A password of a kind the host does not accept counts as wrong.
            let opened = opened.filter(|s| ctx.access.password_mode().accepts(*s));
            let Some(slot) = opened else {
                ctx.access.failed(from);
                let _ = ch.send(&refuse(Refusal::Password)).await;
                return Err(Failure::Refused(Refusal::Password));
            };
            (Some(slot), login.about)
        }
        None => (None, client.about.clone()),
    };
    let peer = about.unwrap_or_default().clean();
    let device_id = identity::device_id(client_key);
    let mut want = client.want.clone();
    want.sort();
    want.dedup();
    // Nobody at the computer (Background service): the permanent password
    // admits while Connect without asking is on.
    let unattended = (slot == Some(Slot::Permanent) && ctx.access.serve() != Serve::All)
        .then(|| ctx.access.unattended(&key_b64, &device_id, &want))
        .flatten();
    if unattended.is_none() && ctx.access.serve() != Serve::All {
        let _ = ch.send(&refuse(Refusal::Absent)).await;
        return Err(Failure::Refused(Refusal::Absent));
    }
    // A correct password within what the host lets a password grant.
    let by_password = (slot.is_some()
        && ctx.access.admits_by_password(&key_b64, &device_id, &want))
    .then(|| want.clone());
    if let Some(granted) = unattended.or(by_password) {
        let _ = ctx.access.trust(&key_b64, &device_id, &granted, &peer);
        let resume = ctx.access.issue_resume(&key_b64, &granted);
        step(ch.send(&Verdict {
            accepted: true,
            granted: granted.clone(),
            reason: None,
            resume: Some(resume),
            about: Some(ctx.about()),
        }))
        .await?;
        return Ok((granted, peer));
    }
    ctx.sessions.update(id, |e| e.state = State::Confirming);
    let (ask_id, decision) = match ctx
        .access
        .ask(&key_b64, &device_id, want, &peer, slot.is_some())
    {
        Ok(v) => v,
        Err(reason) => {
            let _ = ch.send(&refuse(reason)).await;
            return Err(Failure::Refused(reason));
        }
    };
    let answer = match tokio::time::timeout(CONFIRM_TIMEOUT, decision).await {
        Ok(Ok(Some(granted))) => Ok(granted),
        Ok(Ok(None)) | Ok(Err(_)) => Err(Refusal::Denied),
        Err(_) => Err(Refusal::Timeout),
    };
    let granted = match answer {
        Ok(granted) => granted,
        Err(reason) => {
            ctx.access.withdraw(ask_id);
            // Asking without a password counts like a failed login, so
            // requests cannot be repeated without end.
            if slot.is_none() {
                ctx.access.failed(from);
            }
            let _ = ch.send(&refuse(reason)).await;
            return Err(Failure::Refused(reason));
        }
    };
    let _ = ctx.access.trust(&key_b64, &device_id, &granted, &peer);
    let resume = ctx.access.issue_resume(&key_b64, &granted);
    step(ch.send(&Verdict {
        accepted: true,
        granted: granted.clone(),
        reason: None,
        resume: Some(resume),
        about: Some(ctx.about()),
    }))
    .await?;
    Ok((granted, peer))
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

/// Reports whether a peer's unauthenticated version string looks like a
/// version ("0.1.3", "1.0.0-beta.1"), so nothing else reaches the interface.
fn is_version(v: &str) -> bool {
    (1..=24).contains(&v.len())
        && v.starts_with(|c: char| c.is_ascii_digit())
        && v.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-')
}

/// Connects to a host as the controller, over a stream already open to it.
/// Inputs: the Device ID that was dialed, the password the person typed (empty
/// asks the host's user without one), the
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
            // Not yet authenticated: kept only when it reads as a version,
            // so the controller can say which version the host runs.
            if is_version(&host.version) {
                ctx.sessions
                    .update(id, |e| e.peer_version = host.version.clone());
            }
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
        // 3: -> s, se + client_hello with both credential requests, or
        // none (and this device's description) to ask without a password
        let half = if password.is_empty() {
            None
        } else {
            Some(auth::client_start(&password).map_err(|_| Failure::Broken)?)
        };
        let (half, requests) = match half {
            Some((half, requests)) => (Some(half), Some(requests)),
            None => (None, None),
        };
        let hello = ClientHello {
            protocol: PROTOCOL,
            version: ctx.version.clone(),
            device_key: STANDARD.encode(ctx.identity.public_key()),
            signature: sign_static(&ctx.identity, &my_static),
            want,
            about: half.is_none().then(|| ctx.about()),
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
        // 4: challenge, or at once a verdict (a refusal, or a resumption);
        // without a password the verdict, after the host's user decided.
        let first: Value = match &half {
            Some(_) => step(ch.recv()).await?,
            None => {
                ctx.sessions.update(id, |e| e.state = State::Confirming);
                tokio::time::timeout(CONFIRM_TIMEOUT + STEP_TIMEOUT, ch.recv())
                    .await
                    .unwrap_or(Err(Failure::Broken))?
            }
        };
        let verdict: Verdict = if first.get("accepted").is_some() {
            serde_json::from_value(first).map_err(|_| Failure::Broken)?
        } else if let Some(half) = half {
            let challenge: Challenge =
                serde_json::from_value(first).map_err(|_| Failure::Broken)?;
            // 5: login
            let login = match half.finish(&password, &challenge.responses, &host_key, &context) {
                Some((slot, fin)) => Login {
                    slot: Some(slot),
                    finalization: Some(fin),
                    about: Some(ctx.about()),
                },
                None => Login {
                    slot: None,
                    finalization: None,
                    about: None,
                },
            };
            step(ch.send(&login)).await?;
            ctx.sessions.update(id, |e| e.state = State::Confirming);
            // 6: verdict, after the host's user decided.
            tokio::time::timeout(CONFIRM_TIMEOUT + STEP_TIMEOUT, ch.recv())
                .await
                .unwrap_or(Err(Failure::Broken))?
        } else {
            return Err(Failure::Broken);
        };
        if !verdict.accepted {
            return Err(Failure::Refused(verdict.reason.unwrap_or(Refusal::Denied)));
        }
        let peer = verdict.about.unwrap_or_default().clean();
        let mut address = String::new();
        ctx.sessions.update(id, |e| address.clone_from(&e.address));
        let _ = ctx
            .access
            .pin_host(&device_id, &host.device_key, &peer, &address);
        let mut remember = Remember::default();
        ctx.sessions.update(id, |e| remember = e.remember);
        if remember.save && !password.is_empty() {
            let _ = ctx.access.save_password(&device_id, &password);
        }
        ctx.sessions.update(id, |e| e.peer = peer);
        Ok((ch, verdict.granted, verdict.resume))
    }
    .await;
    match result {
        Ok((ch, granted, token)) => {
            let clipboard = shared(&ctx, &granted);
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
            if matches!(f, Failure::Refused(Refusal::Password)) {
                let mut remember = Remember::default();
                ctx.sessions.update(id, |e| remember = e.remember);
                if remember.saved {
                    let _ = ctx.access.forget_password(&device_id);
                }
            }
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
pub fn begin(ctx: &Ctx, device_id: &str, remember: Remember) -> (u64, Arc<Notify>) {
    let (id, stop) = ctx.sessions.open(Role::Controller, device_id);
    ctx.sessions.update(id, |e| e.remember = remember);
    (id, stop)
}

/// Records the address an outgoing session dials (connections by
/// address), which the known host keeps for the next one.
pub fn dials_address(ctx: &Ctx, id: u64, address: &str) {
    ctx.sessions.update(id, |e| e.address = address.to_owned());
}

/// Learns which device answers at an address: the first two handshake
/// messages, then the connection is dropped (the host sees a connection
/// that never logs in).
/// Returns: the Device ID its device key derives, proven by its signature.
pub async fn identify<S: AsyncRead + AsyncWrite + Unpin>(mut stream: S) -> Result<String, Failure> {
    let (mut hs, _) = noise(true)?;
    let mut buf = vec![0u8; MAX_FRAME];
    let n = hs.write_message(&[], &mut buf)?;
    step(write_frame(&mut stream, &buf[..n])).await?;
    let frame = step(read_frame(&mut stream)).await?;
    let n = hs.read_message(&frame, &mut buf)?;
    let host: HostHello = serde_json::from_slice(&buf[..n]).map_err(|_| Failure::Broken)?;
    if host.protocol != PROTOCOL {
        return Err(Failure::Incompatible);
    }
    let key = verify_static(&host.device_key, &host.signature, hs.get_remote_static())?;
    Ok(identity::device_id(&key))
}

/// Ends an outgoing session that found no device to talk to.
pub fn give_up(ctx: &Ctx, id: u64, failure: Failure) {
    ctx.sessions.end(id, Some(failure));
}

/// This side's clipboard in a session, and what the session may share on
/// it.
struct Shared {
    board: Box<dyn crate::clipboard::Clipboard>,
    /// `clipboard`: text and images.
    text: bool,
    /// `files`: copied files.
    files: bool,
}

/// Opens this side's clipboard when the session may share text or files.
fn shared(ctx: &Ctx, granted: &[Permission]) -> Option<Shared> {
    let text = granted.contains(&Permission::Clipboard);
    let files = granted.contains(&Permission::Files);
    let board = (text || files).then(|| (ctx.clipboard)()).flatten()?;
    Some(Shared { board, text, files })
}

/// What one end of an established session does besides the heartbeat.
pub enum Side {
    /// The host: sends video while the controller may view the screen, its
    /// sound while it may hear it, and injects its input while it may
    /// control input.
    Host {
        video: bool,
        sound: bool,
        // Read by the Windows input service; macOS input comes with the Mac pass.
        #[cfg_attr(not(windows), allow(dead_code))]
        input: bool,
        ai: HostAi,
    },
    /// The controller: puts media into the queue its GUI drains, and sends
    /// the GUI's control messages (input, key-frame requests) to the host;
    /// `choices` are the viewer's latest quality and codec.
    Controller {
        queue: Arc<media::Queue>,
        control: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
        choices: Arc<Mutex<Choices>>,
    },
}

/// The host's AI as one session sees it: which controller it serves and
/// what that controller may do.
pub struct HostAi {
    ai: Arc<crate::ai::Ai>,
    /// The controller's Device ID: the origin of the tasks it starts.
    peer: String,
    /// `run-ai-task`: start tasks and steer the host's task, whoever
    /// started it (one record for every controller).
    run: bool,
    /// `approve-ai-action`: answer the host's task's approvals.
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
            Message::AiCommand { id, command, text } if self.run => match command {
                Steer::Pause => self.ai.pause(id, crate::ai::Pause::Controller),
                Steer::Resume => self.ai.resume(id),
                Steer::Cancel => self.ai.cancel(id),
                Steer::Reply => self.ai.reply(id, &text),
            },
            Message::AiDecide { id, approve } if self.approve => self.ai.decide(id, approve),
            Message::AiDraft { text } if self.run => self.ai.set_draft(&text),
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

/// Tags one Opus packet of the host's sound for the wire.
fn sound_packet(opus: &[u8]) -> Vec<u8> {
    let mut packet = Vec::with_capacity(1 + opus.len());
    packet.push(media::AUDIO_TAG);
    packet.extend_from_slice(opus);
    packet
}

/// Starts a plaintext that is one part of a message longer than a frame.
const PART_TAG: u8 = 0x02;
/// Bytes before a part's piece: the tag, its index and the count (u16 each).
const PART_HEADER: usize = 5;
/// Longest message sent in parts.
const MAX_MESSAGE: usize = 4 << 20;

/// Encrypts one message and writes it, in parts when it is longer than a
/// frame (a long clipboard text, a long AI task).
async fn seal_json<W: AsyncWrite + Unpin, T: Serialize>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    msg: &T,
) -> Result<(), Failure> {
    let plain = serde_json::to_vec(msg).map_err(|_| Failure::Broken)?;
    if plain.len() <= MAX_PLAINTEXT {
        return seal(noise, buf, w, &plain).await;
    }
    for part in split(&plain).ok_or(Failure::Broken)? {
        seal(noise, buf, w, &part).await?;
    }
    Ok(())
}

/// Splits a long message into parts that each fit a frame.
/// Returns: None when it is longer than MAX_MESSAGE.
fn split(plain: &[u8]) -> Option<Vec<Vec<u8>>> {
    if plain.len() > MAX_MESSAGE {
        return None;
    }
    let room = MAX_PLAINTEXT - PART_HEADER;
    let count = u16::try_from(plain.len().div_ceil(room)).ok()?;
    Some(
        plain
            .chunks(room)
            .enumerate()
            .map(|(i, piece)| {
                let mut part = Vec::with_capacity(PART_HEADER + piece.len());
                part.push(PART_TAG);
                part.extend_from_slice(&(i as u16).to_be_bytes());
                part.extend_from_slice(&count.to_be_bytes());
                part.extend_from_slice(piece);
                part
            })
            .collect(),
    )
}

/// Puts a message sent in parts back together; the parts of one message
/// arrive in order, one message at a time.
#[derive(Default)]
struct Parts {
    buf: Vec<u8>,
    next: u16,
}

impl Parts {
    /// Takes one part.
    /// Returns: the whole message after its last part; an error for a part
    /// out of order, a malformed one, or a message past MAX_MESSAGE.
    fn push(&mut self, part: &[u8]) -> Result<Option<Vec<u8>>, Failure> {
        let (idx, count) = match part {
            [PART_TAG, i0, i1, c0, c1, ..] => (
                u16::from_be_bytes([*i0, *i1]),
                u16::from_be_bytes([*c0, *c1]),
            ),
            _ => return Err(Failure::Broken),
        };
        let piece = &part[PART_HEADER..];
        if idx != self.next || idx >= count || self.buf.len() + piece.len() > MAX_MESSAGE {
            return Err(Failure::Broken);
        }
        self.buf.extend_from_slice(piece);
        if idx + 1 == count {
            self.next = 0;
            return Ok(Some(std::mem::take(&mut self.buf)));
        }
        self.next += 1;
        Ok(None)
    }
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
    clipboard: Option<Shared>,
) -> Result<(), Failure> {
    let Channel {
        stream,
        mut noise,
        mut buf,
    } = ch;
    let (mut rd, mut wr) = tokio::io::split(stream);
    let (frames_tx, mut frames) = mpsc::channel::<Vec<u8>>(64);
    // A host stays awake while someone is connected.
    let _awake = matches!(side, Side::Host { .. }).then(crate::power::keep_awake);
    let _reader = AbortOnDrop(tokio::spawn(async move {
        while let Ok(f) = read_frame(&mut rd).await {
            if frames_tx.send(f).await.is_err() {
                break;
            }
        }
    }));
    // The host's video starts once the controller said which codecs it
    // decodes (or after CODECS_WAIT, in H.264): the codec decides which
    // encoder it takes.
    let wants_video = matches!(side, Side::Host { video: true, .. });
    let may_hear = matches!(side, Side::Host { sound: true, .. });
    let mut source: Option<video::Source> = None;
    // The host's sound, while the controller asks for it.
    let mut sound: Option<audio::Source> = None;
    let video_wait = tokio::time::sleep(CODECS_WAIT);
    tokio::pin!(video_wait);
    // The host's input service; dropping it releases what it holds.
    #[cfg(any(windows, target_os = "macos"))]
    let mut injector = match side {
        Side::Host { input: true, .. } => Some(crate::input::Injector::default()),
        _ => None,
    };
    // The displays the controller may choose from, with the one shown.
    let displays = if wants_video {
        video::displays()
    } else {
        Vec::new()
    };
    // A controller says which codecs it decodes before anything else;
    // a resumed one first gives the viewer's quality and codec again, so
    // the host opens the video as the viewer chose.
    if let Side::Controller { choices, .. } = &side {
        let again: Vec<Value> = choices
            .lock()
            .map(|c| {
                [c.quality.clone(), c.codec.clone(), c.audio.clone()]
                    .into_iter()
                    .flatten()
                    .collect()
            })
            .unwrap_or_default();
        for m in again {
            if let Ok(m) = serde_json::from_value::<Message>(m) {
                seal_json(&mut noise, &mut buf, &mut wr, &m).await?;
            }
        }
        let (decode, i444) = tokio::task::spawn_blocking(video::decodable)
            .await
            .unwrap_or_default();
        seal_json(
            &mut noise,
            &mut buf,
            &mut wr,
            &Message::Codecs { decode, i444 },
        )
        .await?;
    }
    let (queue, control, choices) = match &side {
        Side::Controller {
            queue,
            control,
            choices,
        } => (
            Some(queue.clone()),
            Some(control.clone()),
            Some(choices.clone()),
        ),
        Side::Host { .. } => (None, None, None),
    };
    let host_ai = match &side {
        Side::Host { ai, .. } if ai.run || ai.approve => Some(ai),
        _ => None,
    };
    // Clipboard sharing, when the session may: local copies go out, the
    // other side's items are put on the local clipboard; files go through
    // their transfers.
    let (text_ok, files_ok) = clipboard
        .as_ref()
        .map_or((false, false), |s| (s.text, s.files));
    let mut clip = clipboard.map(|s| crate::clipboard::share(s.board));
    let mut transfers =
        files_ok.then(|| crate::files::Transfers::new(MAX_PLAINTEXT - crate::files::HEADER));
    // The viewer's clipboard choice (a controller's; a host keeps both on),
    // the latest local copy made while its tab was not in front, offered
    // when it comes to the front, and the files offer being received, for
    // the history.
    let mut clip_use = choices
        .as_ref()
        .and_then(|c| c.lock().ok().map(|c| c.clipboard))
        .unwrap_or_default();
    let mut waiting: Option<crate::clipboard::Item> = None;
    let mut receiving_files: Option<Vec<crate::files::Entry>> = None;
    // Offers a local copy to the other side (files through their
    // transfers) and adds it to a controller's history.
    macro_rules! offer {
        ($item:expr) => {{
            let msg = match $item {
                crate::clipboard::Item::Files(paths) => {
                    if let Some(t) = transfers.as_mut() {
                        t.copied(paths);
                    }
                    None
                }
                _ if !text_ok => None,
                crate::clipboard::Item::Text(text) => Some(Message::Clipboard { text }),
                crate::clipboard::Item::Image(img) => {
                    crate::clipboard::to_png(&img).map(|png| Message::ClipboardImage { png })
                }
                crate::clipboard::Item::Png(png) => Some(Message::ClipboardImage { png }),
            };
            if let Some(m) = msg {
                seal_json(&mut noise, &mut buf, &mut wr, &m).await?;
                match &m {
                    Message::Clipboard { text } => {
                        clip_history(&queue, "controller", Crossed::Text(text))
                    }
                    Message::ClipboardImage { png } => {
                        clip_history(&queue, "controller", Crossed::Png(png))
                    }
                    _ => {}
                }
            }
        }};
    }
    let mut ai_sync = tokio::time::interval(AI_SYNC);
    // The host's pointer, which the captured picture leaves out.
    let mut pointer_tick = tokio::time::interval(POINTER_EVERY);
    let mut pointer_sent: Option<((i32, i32), bool)> = None;
    let mut ai_sent: Option<Value> = None;
    // The serial of the shared draft this controller has (host side).
    let mut draft_sent: u64 = 0;
    let mut control = match &control {
        Some(c) => Some(c.lock().await),
        None => None,
    };
    let mut reassembler = media::Reassembler::default();
    let mut parts = Parts::default();
    let mut frame_no: u32 = 0;
    // The newest frame the controller acknowledged: frames wait at the
    // capture (which keeps only the newest picture) instead of piling up in
    // the transport's buffers on a slow path.
    let mut acked: u32 = 0;
    let mut ping = tokio::time::interval(PING_EVERY);
    // RustDesk's QoS on the host: one probe a second, frames counted per
    // second, frame rate and bitrate applied to the source.
    let mut qos = rustdesk_qos::VideoQoS::default();
    let mut base_kbps = 0;
    // The host's codec choice: what the controller decodes (and in full
    // colour), whether its viewer wants full colour, whether this host had a
    // free hardware HEVC encoder when the codecs came (kept, so the stream's
    // own encoder does not change the choice), and the candidates the
    // current source was opened from.
    let mut decodes: (Vec<String>, Vec<String>) = Default::default();
    let mut hevc_first = false;
    let mut full_colour = false;
    // The viewer's codec (None: Auto), and what this host offers.
    let mut preferred: Option<String> = None;
    let mut offer: Vec<String> = Vec::new();
    let mut chosen: Vec<video::Choice> = Vec::new();
    if wants_video {
        qos.on_connection_open(QOS_VIEWER);
        qos.new_display(QOS_DISPLAY.into());
    }
    let mut probe = tokio::time::interval(Duration::from_secs(1));
    let mut probe_sent: Option<Instant> = None;
    let mut last_delay = 0u32;
    // The viewer's frame-rate cap, when a person's input last came, and the
    // bitrate last set (kbit/s), for the input boost.
    let mut fps_cap = rustdesk_qos::FPS;
    let mut input_at: Option<Instant> = None;
    let mut last_kbps = 0u32;
    let mut sent_this_second = 0usize;
    // The microseconds this second's frames took from picture to frame.
    let mut took_this_second = 0u64;
    // When this controller last asked for a key frame after its queue left
    // frames out.
    let mut key_asked: Option<Instant> = None;
    let mut heard = Instant::now();
    let mut plain = vec![0u8; MAX_FRAME];
    // Whether this session holds the block or privacy mode; checked every
    // second, since the host user can lift them.
    #[cfg_attr(not(windows), allow(unused_mut))]
    let mut holding = false;
    let mut block_check = tokio::time::interval(Duration::from_secs(1));
    #[cfg(windows)]
    if let Some(inj) = &injector {
        seal_json(&mut noise, &mut buf, &mut wr, &offered(inj)).await?;
    }
    // Opens the video on a display (None: the primary) from the chosen
    // candidates, once the old capture let go of its encoder, and describes
    // it to the controller.
    macro_rules! reopen {
        ($display:expr) => {{
            let display = $display;
            source = None;
            let s = video::Source::open(display, &chosen);
            #[cfg(any(windows, target_os = "macos"))]
            if let Some(inj) = injector.as_mut() {
                inj.set_area(s.display.map(area));
            }
            announce(&mut noise, &mut buf, &mut wr, &s, &displays).await?;
            base_kbps = rustdesk_qos::base_bitrate(s.start.width, s.start.height);
            source = Some(s);
            pointer_sent = None;
        }};
    }
    let outcome: Result<(), Failure> = async { loop {
        tokio::select! {
            _ = stop.notified() => {
                let _ = seal_json(&mut noise, &mut buf, &mut wr, &Message::Bye).await;
                // The stream stays open until the peer read the goodbye and
                // closed its end (or BYE_WAIT passed): closed first, the
                // peer's next write would fail and it would take the
                // connection for lost and resume it.
                let _ = tokio::time::timeout(BYE_WAIT, async {
                    let _ = wr.flush().await;
                    while frames.recv().await.is_some() {}
                })
                .await;
                return Ok(());
            }
            _ = ai_sync.tick(), if host_ai.is_some() => {
                // The host's task as it is now, the same for every
                // controller; sent again after a reconnect, never replayed
                // as commands.
                let h = host_ai.expect("checked");
                let now = h.ai.latest();
                if now.is_some() && now != ai_sent {
                    let task = now.clone().expect("checked");
                    seal_json(&mut noise, &mut buf, &mut wr, &Message::AiTask { task }).await?;
                    ai_sent = now;
                }
                // The shared draft, as anyone typed it last.
                let (serial, text) = h.ai.draft();
                if serial != draft_sent {
                    seal_json(&mut noise, &mut buf, &mut wr, &Message::AiDraft { text }).await?;
                    draft_sent = serial;
                }
            }
            Some(item) = async {
                match clip.as_mut() {
                    Some(c) => c.offers.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                // Only the tab in front shares, while its viewer shares at
                // all: a copy made while it is behind waits for the front.
                if !clip_use.share {
                } else if !clip_use.front {
                    waiting = Some(item);
                } else {
                    offer!(item);
                }
            }
            step = async {
                match transfers.as_mut() {
                    Some(t) => t.next().await,
                    None => std::future::pending().await,
                }
            } => {
                use crate::files::Step;
                match step {
                    Step::Offer(id, files) => {
                        clip_history(&queue, "controller", Crossed::Files(&files));
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::ClipboardFiles { id, files }).await?;
                    }
                    Step::Piece(p) => seal(&mut noise, &mut buf, &mut wr, &p).await?,
                    Step::Cancel(id) => seal_json(&mut noise, &mut buf, &mut wr, &Message::FilesCancel { id }).await?,
                    Step::Stop(id) => {
                        receiving_files = None;
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::FilesStop { id }).await?;
                    }
                    Step::Received(paths) => {
                        if let Some(f) = receiving_files.take() {
                            clip_history(&queue, "host", Crossed::Files(&f));
                        }
                        if let Some(c) = &clip {
                            let _ = c.writes.send(crate::clipboard::Item::Files(paths));
                        }
                    }
                }
            }
            Some(p) = async {
                match sound.as_mut() {
                    Some(s) => s.packets.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                seal(&mut noise, &mut buf, &mut wr, &sound_packet(&p)).await?;
            }
            _ = &mut video_wait, if wants_video && source.is_none() => {
                // A controller that names no codecs gets H.264.
                chosen = video::choose(&[], &[], false, false);
                reopen!(None);
            }
            _ = pointer_tick.tick(), if source.is_some() => {
                let at = source.as_ref().and_then(|s| s.display).map(area);
                if let Some((x, y, shown)) = at.map_or_else(crate::input::pointer, crate::input::pointer_in) {
                    let visible = shown && (0.0..=1.0).contains(&x) && (0.0..=1.0).contains(&y);
                    let now = (((x * 4096.0) as i32, (y * 4096.0) as i32), visible);
                    if pointer_sent != Some(now) {
                        pointer_sent = Some(now);
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::Cursor { x, y, visible }).await?;
                    }
                }
            }
            _ = block_check.tick(), if holding => {
                #[cfg(windows)]
                if let Some(inj) = &injector
                    && !inj.input_blocked()
                {
                    holding = false;
                    seal_json(&mut noise, &mut buf, &mut wr, &offered(inj)).await?;
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
                let encode_ms = (took_this_second / sent_this_second.max(1) as u64 / 1000) as u32;
                sent_this_second = 0;
                took_this_second = 0;
                let fps = boosted(qos.fps(), fps_cap, input_at, last_delay);
                let kbps = (base_kbps as f32 * qos.ratio()) as u32;
                last_kbps = kbps;
                qos.store_bitrate(kbps);
                if let Some(s) = &source {
                    s.set_targets(fps, kbps.saturating_mul(1000));
                }
                let stats = Message::Stats { delay_ms: last_delay, fps, bitrate: kbps.saturating_mul(1000), encode_ms };
                seal_json(&mut noise, &mut buf, &mut wr, &stats).await?;
            }
            Some(f) = async {
                match source.as_mut() {
                    Some(s) => s.frames.recv().await,
                    None => std::future::pending().await,
                }
            }, if frame_no.wrapping_sub(acked) < IN_FLIGHT => {
                frame_no = frame_no.wrapping_add(1);
                sent_this_second += 1;
                took_this_second += u64::from(f.took_us);
                for chunk in media::chunks(frame_no, f.flags, f.pts, &f.data, MAX_PLAINTEXT) {
                    seal(&mut noise, &mut buf, &mut wr, &chunk).await?;
                    // Sound that came meanwhile goes between the chunks: a
                    // large picture does not hold it up.
                    // Only what waits now: more comes every 10 ms.
                    for _ in 0..sound.as_ref().map_or(0, |s| s.packets.len()) {
                        let Some(p) = sound.as_mut().and_then(|s| s.packets.try_recv().ok()) else {
                            break;
                        };
                        seal(&mut noise, &mut buf, &mut wr, &sound_packet(&p)).await?;
                    }
                }
            }
            Some(msg) = async {
                match control.as_mut() {
                    Some(rx) => rx.recv().await,
                    None => std::future::pending().await,
                }
            } => {
                // The viewer's clipboard choice stays on this side.
                if msg["type"] == "clipboard-use" {
                    let now = ClipUse {
                        share: msg["share"].as_bool().unwrap_or(true),
                        front: msg["front"].as_bool().unwrap_or(true),
                    };
                    if let Some(c) = &choices
                        && let Ok(mut c) = c.lock()
                    {
                        c.clipboard = now;
                    }
                    if !now.share {
                        // Nothing crosses any more, files under way included.
                        waiting = None;
                        if clip_use.share && let Some(t) = transfers.as_mut() {
                            receiving_files = None;
                            let (sent, received) = t.halt();
                            if let Some(id) = sent {
                                seal_json(&mut noise, &mut buf, &mut wr, &Message::FilesCancel { id }).await?;
                            }
                            if let Some(id) = received {
                                seal_json(&mut noise, &mut buf, &mut wr, &Message::FilesStop { id }).await?;
                            }
                        }
                        clip_use = now;
                    } else if now.front && !clip_use.front {
                        // Come to the front: what was copied meanwhile goes.
                        clip_use = now;
                        if let Some(item) = waiting.take() {
                            offer!(item);
                        }
                    } else {
                        clip_use = now;
                    }
                    continue;
                }
                // Only messages a GUI may send pass on to the host.
                if let Ok(m @ (Message::KeyFrame | Message::Display { .. } | Message::Codecs { .. } | Message::Input { .. } | Message::Action { .. } | Message::Quality { .. } | Message::Codec { .. } | Message::Audio { .. } | Message::AiStart { .. } | Message::AiCommand { .. } | Message::AiDecide { .. } | Message::AiDraft { .. })) = serde_json::from_value::<Message>(msg) {
                    // The viewer's latest choices, for a resumed session.
                    if let (Some(c), Ok(v)) = (&choices, serde_json::to_value(&m))
                        && let Ok(mut c) = c.lock()
                    {
                        match m {
                            Message::Quality { .. } => c.quality = Some(v),
                            Message::Codec { .. } => c.codec = Some(v),
                            Message::Audio { .. } => c.audio = Some(v),
                            _ => {}
                        }
                    }
                    seal_json(&mut noise, &mut buf, &mut wr, &m).await?;
                }
            }
            frame = frames.recv() => {
                let frame = frame.ok_or(Failure::Broken)?;
                heard = Instant::now();
                let n = noise.read_message(&frame, &mut plain)?;
                let plain = &plain[..n];
                if plain.first() == Some(&media::CHUNK_TAG) {
                    if let Some(frame) = media::last_chunk(plain) {
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::Received { frame }).await?;
                    }
                    if let (Some(q), Some(f)) = (&queue, reassembler.push(plain)) {
                        let mut packet = Vec::with_capacity(10 + f.data.len());
                        packet.push(media::packet::FRAME);
                        packet.push(f.flags);
                        packet.extend_from_slice(&f.pts_us.to_be_bytes());
                        packet.extend_from_slice(&f.data);
                        q.push(packet);
                        // The GUI fell behind and lost frames: the host starts
                        // a new group of pictures (asked again each second
                        // until it comes).
                        if q.waiting_for_key() && key_asked.is_none_or(|t| t.elapsed() >= KEY_AGAIN) {
                            key_asked = Some(Instant::now());
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::KeyFrame).await?;
                        }
                    }
                    continue;
                }
                if plain.first() == Some(&media::AUDIO_TAG) {
                    if let Some(q) = &queue {
                        let mut packet = Vec::with_capacity(plain.len());
                        packet.push(media::packet::AUDIO);
                        packet.extend_from_slice(&plain[1..]);
                        q.push(packet);
                    }
                    continue;
                }
                if plain.first() == Some(&crate::files::TAG) {
                    if let Some(t) = transfers.as_mut() {
                        t.piece(plain).await;
                    }
                    continue;
                }
                // A message longer than a frame comes in parts.
                let whole = if plain.first() == Some(&PART_TAG) {
                    match parts.push(plain)? {
                        Some(bytes) => std::borrow::Cow::Owned(bytes),
                        None => continue,
                    }
                } else {
                    std::borrow::Cow::Borrowed(plain)
                };
                match serde_json::from_slice::<Message>(&whole).map_err(|_| Failure::Broken)? {
                    Message::Ping => seal_json(&mut noise, &mut buf, &mut wr, &Message::Pong).await?,
                    Message::Pong => {}
                    Message::Quality { preset, fps, i444 } => {
                        // Full colour switched: the video restarts when that
                        // changes the codec or its chroma.
                        if i444 != full_colour {
                            full_colour = i444;
                            let c = video::prefer(video::choose(&decodes.0, &decodes.1, full_colour, hevc_first), preferred.as_deref());
                            if wants_video && source.is_some() && c != chosen {
                                chosen = c;
                                reopen!(source.as_ref().and_then(|s| s.display));
                            }
                        }
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
                            fps_cap = fps;
                        }
                    }
                    // Only a frame sent and not yet acknowledged moves the mark.
                    Message::Received { frame } => {
                        if frame_no.wrapping_sub(frame) < frame_no.wrapping_sub(acked) {
                            acked = frame;
                        }
                    }
                    // The controller echoes probes at once; the host measures them.
                    // The host starts its sound for a controller that may
                    // hear it and asks; it stops when asked, or when it
                    // cannot capture (it then sends none).
                    Message::Audio { on } if matches!(side, Side::Host { .. }) => {
                        if on && may_hear {
                            if sound.is_none() {
                                sound = audio::Source::open().ok();
                            }
                        } else {
                            sound = None;
                        }
                    }
                    Message::Audio { .. } => {}
                    Message::TestDelay { reply: false } => {
                        seal_json(&mut noise, &mut buf, &mut wr, &Message::TestDelay { reply: true }).await?;
                    }
                    Message::TestDelay { reply: true } => {
                        if let Some(t) = probe_sent.take() {
                            last_delay = t.elapsed().as_millis() as u32;
                            qos.user_network_delay(QOS_VIEWER, last_delay);
                        }
                    }
                    Message::Stats { delay_ms, fps, bitrate, encode_ms } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::STATS];
                            packet.extend(serde_json::to_vec(&json!({"delay_ms": delay_ms, "fps": fps, "bitrate": bitrate, "encode_ms": encode_ms})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::Bye => return Ok(()),
                    // A controller shows the host's shared draft.
                    Message::AiDraft { text } if matches!(side, Side::Controller { .. }) => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::AI_DRAFT];
                            packet.extend(serde_json::to_vec(&json!({"text": text})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    m @ (Message::AiStart { .. } | Message::AiCommand { .. } | Message::AiDecide { .. } | Message::AiDraft { .. }) => {
                        // Only a host answers AI requests.
                        if let Side::Host { ai, .. } = &side
                            && let Err(error) = ai.handle(m)
                        {
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::AiError { error }).await?;
                        }
                    }
                    Message::Clipboard { text } => {
                        if let Some(c) = &clip
                            && text_ok
                            && clip_use.share
                            && clip_use.front
                            && text.len() <= crate::clipboard::MAX_TEXT
                        {
                            clip_history(&queue, "host", Crossed::Text(&text));
                            let _ = c.writes.send(crate::clipboard::Item::Text(text));
                        }
                    }
                    Message::ClipboardImage { png } => {
                        // The helper is privileged and never decodes remote
                        // media: images from a controller are not taken there.
                        if let Some(c) = &clip
                            && text_ok
                            && clip_use.share
                            && clip_use.front
                            && crate::agent::link().is_none()
                            && let Some(img) = crate::clipboard::from_png(&png)
                        {
                            clip_history(&queue, "host", Crossed::Png(&png));
                            let _ = c.writes.send(crate::clipboard::Item::Image(img));
                        }
                    }
                    Message::ClipboardFiles { id, files } => {
                        receiving_files = (queue.is_some() && clip_use.share && clip_use.front).then(|| files.clone());
                        if let Some(t) = transfers.as_mut()
                            && clip_use.share
                            && clip_use.front
                            && t.offered(id, files)
                        {
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::FilesGet { id }).await?;
                        }
                    }
                    Message::FilesGet { id } => {
                        if let Some(t) = transfers.as_mut() {
                            t.get(id);
                        }
                    }
                    Message::FilesStop { id } => {
                        if let Some(t) = transfers.as_mut() {
                            t.stop(id);
                        }
                    }
                    Message::FilesCancel { id } => {
                        if let Some(t) = transfers.as_mut() {
                            t.cancelled(id);
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
                    // The controller picks another display: capture it
                    // instead, and map input and the pointer onto it.
                    Message::Display { index } => {
                        if let (Some(s), Some(&d)) = (&source, displays.get(index))
                            && s.display != Some(d)
                        {
                            reopen!(Some(d));
                        }
                    }
                    // The controller's codecs: the video starts in the best
                    // of them this host can send.
                    Message::Codecs { decode, i444 } => {
                        decodes = (decode, i444);
                        hevc_first = video::hevc_hardware();
                        let c = video::prefer(video::choose(&decodes.0, &decodes.1, full_colour, hevc_first), preferred.as_deref());
                        if wants_video {
                            offer = video::offer(&decodes.0, &video::encodable()).iter().map(|c| c.to_string()).collect();
                            seal_json(&mut noise, &mut buf, &mut wr, &Message::CodecOffer { codecs: offer.clone() }).await?;
                        }
                        if wants_video && (source.is_none() || c != chosen) {
                            chosen = c;
                            reopen!(source.as_ref().and_then(|s| s.display));
                        }
                    }
                    // The viewer picks a codec of the offer, or Auto (a name
                    // the host does not send changes nothing); the video
                    // restarts when that changes which one goes first. A
                    // resumed session's choice comes before the codecs.
                    Message::Codec { codec } => {
                        preferred = (codec != "auto").then_some(codec);
                        if wants_video && !decodes.0.is_empty() {
                            let c = video::prefer(video::choose(&decodes.0, &decodes.1, full_colour, hevc_first), preferred.as_deref());
                            let first_changed = c.first() != chosen.first();
                            chosen = c;
                            if source.is_some() && first_changed {
                                reopen!(source.as_ref().and_then(|s| s.display));
                            }
                        }
                    }
                    // A controller shows what the host can send.
                    Message::CodecOffer { codecs } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::CODECS];
                            packet.extend(serde_json::to_vec(&json!({"codecs": codecs})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    Message::Displays { list, current } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::DISPLAYS];
                            packet.extend(serde_json::to_vec(&json!({"list": list, "current": current})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
                    // A viewer that (re)opens asks for a key frame: describe the
                    // stream, its displays and the codecs again and start a
                    // new group of pictures.
                    Message::KeyFrame => {
                        if let Some(s) = &source {
                            s.key_frame();
                            announce(&mut noise, &mut buf, &mut wr, s, &displays).await?;
                            if !offer.is_empty() {
                                seal_json(&mut noise, &mut buf, &mut wr, &Message::CodecOffer { codecs: offer.clone() }).await?;
                            }
                        }
                    }
                    // Input reaches the host only with control-input; a
                    // malformed event is dropped, not fatal.
                    // A person's input takes over from the host's AI task:
                    // it pauses before the event is injected. Pointer moves
                    // alone (a hand resting on the viewer) neither take over
                    // nor reach the desktop while the AI acts.
                    #[cfg(any(windows, target_os = "macos"))]
                    Message::Input { event } => {
                        if let (Some(inj), Ok(e), Side::Host { ai, .. }) = (injector.as_mut(), serde_json::from_value::<crate::input::Event>(event), &side) {
                            let hover = matches!(e, crate::input::Event::Pointer { action: crate::input::PointerAction::Move, .. });
                            if !(hover && ai.ai.acting()) {
                                ai.ai.take_over();
                                inj.inject(&e);
                                // The first input after a quiet spell raises
                                // the frame rate now, not at the next probe.
                                let quiet = input_at.is_none_or(|t| t.elapsed() >= INPUT_BOOST);
                                input_at = Some(Instant::now());
                                if quiet && last_kbps > 0 && let Some(s) = &source {
                                    s.set_targets(boosted(qos.fps(), fps_cap, input_at, last_delay), last_kbps.saturating_mul(1000));
                                }
                            }
                        }
                    }
                    #[cfg(not(any(windows, target_os = "macos")))]
                    Message::Input { .. } => {}
                    // Session actions need control-input, like input; a
                    // person acting takes over from the host's AI task.
                    #[cfg(windows)]
                    Message::Action { action } => {
                        if let (Some(inj), Side::Host { ai, .. }) = (injector.as_mut(), &side) {
                            ai.ai.take_over();
                            match action {
                                SessionAction::Lock => {
                                    inj.lock_screen();
                                }
                                SessionAction::CtrlAltDel => {
                                    inj.ctrl_alt_del();
                                }
                                SessionAction::Block | SessionAction::Unblock => {
                                    inj.block_input(action == SessionAction::Block);
                                }
                                SessionAction::PrivacyOn | SessionAction::PrivacyOff => {
                                    inj.privacy(action == SessionAction::PrivacyOn);
                                }
                            }
                            if matches!(action, SessionAction::Block | SessionAction::Unblock | SessionAction::PrivacyOn | SessionAction::PrivacyOff) {
                                let now = offered(inj);
                                holding = matches!(now, Message::Actions { blocked: true, .. });
                                seal_json(&mut noise, &mut buf, &mut wr, &now).await?;
                            }
                        }
                    }
                    #[cfg(not(windows))]
                    Message::Action { .. } => {}
                    Message::Actions { ctrl_alt_del, blocked, privacy } => {
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::ACTIONS];
                            packet.extend(serde_json::to_vec(&json!({"ctrl_alt_del": ctrl_alt_del, "blocked": blocked, "privacy": privacy})).map_err(|_| Failure::Broken)?);
                            q.push(packet);
                        }
                    }
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

    /// A writer like EasyTier's TCP stream, whose flush waits until the
    /// peer acknowledged everything: it never does here.
    struct AckFlush(Vec<u8>);

    impl AsyncWrite for AckFlush {
        fn poll_write(
            mut self: std::pin::Pin<&mut Self>,
            _: &mut std::task::Context<'_>,
            b: &[u8],
        ) -> std::task::Poll<std::io::Result<usize>> {
            self.0.extend_from_slice(b);
            std::task::Poll::Ready(Ok(b.len()))
        }
        fn poll_flush(
            self: std::pin::Pin<&mut Self>,
            _: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Pending
        }
        fn poll_shutdown(
            self: std::pin::Pin<&mut Self>,
            _: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Pending
        }
    }

    /// Writing a frame never waits for the peer's acknowledgement: with
    /// EasyTier's flush that allowed one message per round trip, which
    /// starved video and sound and stalled the session.
    #[tokio::test]
    async fn frames_do_not_wait_for_acknowledgement() {
        let mut w = AckFlush(Vec::new());
        for _ in 0..100 {
            tokio::time::timeout(Duration::from_millis(100), write_frame(&mut w, b"sound"))
                .await
                .expect("a frame write waited for the peer")
                .unwrap();
        }
        assert_eq!(w.0.len(), 100 * (2 + 5));
    }

    /// A person's recent input lifts the frame rate to the viewer's cap
    /// while the delay is low; otherwise the QoS decides.
    #[test]
    fn input_lifts_the_frame_rate() {
        let now = Some(Instant::now());
        let old = Instant::now().checked_sub(INPUT_BOOST + Duration::from_secs(1));
        for (qos, cap, at, delay, want) in [
            (15, 30, now, 20, 30),
            (15, 60, now, 20, 60),
            (15, 30, now, BOOST_DELAY_MS, 15),
            (15, 30, old, 20, 15),
            (15, 30, None, 20, 15),
            (30, 10, now, 20, 30),
        ] {
            assert_eq!(boosted(qos, cap, at, delay), want, "{qos} {cap} {delay}");
        }
    }

    /// The viewer's sound request crosses as `{"type": "audio", on}`.
    #[test]
    fn the_sound_request_crosses_by_name() {
        let m: Message = serde_json::from_str(r#"{"type":"audio","on":false}"#).unwrap();
        assert!(matches!(m, Message::Audio { on: false }));
        assert_eq!(
            serde_json::to_value(Message::Audio { on: true }).unwrap(),
            json!({"type": "audio", "on": true})
        );
    }

    #[test]
    fn only_a_version_from_an_incompatible_host_is_kept() {
        for (v, ok) in [
            ("0.1.2", true),
            ("1.0.0-beta.1", true),
            ("", false),
            ("v0.1.2", false),
            ("0.1.2 please update", false),
            ("0.1.2\nnext", false),
            ("1234567890123456789012345", false),
        ] {
            assert_eq!(is_version(v), ok, "{v:?}");
        }
    }
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

    /// A device whose clipboard is `board`. A password grants nothing
    /// without asking, so every login here is confirmed unless a test
    /// sets the password grants.
    fn device_full(
        ai: Arc<crate::ai::Ai>,
        board: crate::clipboard::fake::Board,
        version: &str,
    ) -> Arc<Ctx> {
        let secrets = Arc::new(Secrets::memory());
        let access = Access::load(secrets.clone()).unwrap();
        access.set_password_grants(Vec::new()).unwrap();
        Arc::new(Ctx {
            clipboard: crate::clipboard::fake::opener(board),
            ai,
            identity: Arc::new(Identity::load_or_create(&secrets).unwrap()),
            access: Arc::new(access),
            sessions: Arc::new(Sessions::default()),
            version: version.into(),
            about: Mutex::new(crate::about::Local::load(&test_dir())),
        })
    }

    /// An empty state folder of its own for a test device.
    fn test_dir() -> std::path::PathBuf {
        static N: AtomicUsize = AtomicUsize::new(0);
        let dir = std::env::temp_dir().join(format!(
            "kyokai-session-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn connect_without_asking_and_the_accepted_passwords_decide_admission() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        {
            let mut slots = host.access.slots.lock().unwrap();
            let key = host.identity.public_key();
            slots.set_temporary("k7mq2xpa", &key).unwrap();
            slots
                .set_permanent(host.access.secrets(), Some("long-lived-pw"), &key)
                .unwrap();
        }
        // What a password grants without asking (the test devices grant
        // nothing by default).
        host.access
            .set_password_grants(vec![Permission::ViewScreen])
            .unwrap();
        let dialed = host.identity.device_id();
        let view = vec![Permission::ViewScreen];
        let try_pw = |pw: &'static str, decide: Option<Vec<Permission>>| {
            let (host, controller, dialed, view) = (
                host.clone(),
                controller.clone(),
                dialed.clone(),
                view.clone(),
            );
            async move {
                let id = attempt_with(&host, &controller, &dialed, pw, decide, view).await;
                let s = session(&controller, id);
                if s["state"] == "connected" {
                    controller.sessions.close(id).unwrap();
                }
                s
            }
        };
        // Nobody at the computer (Background service): the permanent
        // password admits, Connect without asking being on by default,
        // with what a password grants; the temporary one finds nobody.
        host.access.set_serve(Serve::Unattended);
        let s = try_pw("long-lived-pw", None).await;
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen"]));
        assert_eq!(
            try_pw("k7mq2xpa", None).await["failure"],
            json!({"refused": "absent"})
        );
        // Turned off, even the permanent password finds nobody to ask.
        host.access.set_without_asking(false).unwrap();
        assert_eq!(
            try_pw("long-lived-pw", None).await["failure"],
            json!({"refused": "absent"})
        );
        // With someone there, a correct password asks (and is denied here).
        host.access.set_serve(Serve::All);
        assert_eq!(
            try_pw("k7mq2xpa", None).await["failure"],
            json!({"refused": "denied"})
        );
        // Only the permanent password accepted: the temporary one is wrong.
        host.access.set_without_asking(true).unwrap();
        host.access
            .set_password_mode(crate::access::PasswordMode::Permanent)
            .unwrap();
        assert_eq!(
            try_pw("k7mq2xpa", None).await["failure"],
            json!({"refused": "password"})
        );
        assert_eq!(try_pw("long-lived-pw", None).await["state"], "connected");
        // A host that serves no one refuses even the permanent password.
        host.access.set_serve(Serve::None);
        assert_eq!(
            try_pw("long-lived-pw", None).await["failure"],
            json!({"refused": "absent"})
        );
    }

    /// A host that stops (its user quits Kyokai) says goodbye: the
    /// controller's session ends at once, closed, instead of waiting for the
    /// connection to time out and then trying to resume it.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_stopping_host_ends_the_controllers_session() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        {
            let mut slots = host.access.slots.lock().unwrap();
            let key = host.identity.public_key();
            slots
                .set_permanent(host.access.secrets(), Some("long-lived-pw"), &key)
                .unwrap();
        }
        host.access
            .set_password_grants(vec![Permission::ViewScreen])
            .unwrap();
        host.access.set_serve(Serve::Unattended);
        let dialed = host.identity.device_id();
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "long-lived-pw",
            None,
            vec![Permission::ViewScreen],
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        // The host marks its side connected on its own task.
        let until = Instant::now() + Duration::from_secs(2);
        while !host.sessions.any_connected() && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert!(host.sessions.any_connected());
        host.sessions.close_all();
        let until = Instant::now() + Duration::from_secs(2);
        while session(&controller, id)["state"] == "connected" && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        let s = session(&controller, id);
        assert_eq!(s["state"], "closed", "{s}");
        assert!(!host.sessions.any_connected());
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_password_admits_within_its_grants_and_a_request_without_one_asks() {
        use Permission::*;
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        host.access
            .set_password_grants(vec![ViewScreen, ControlInput])
            .unwrap();
        let dialed = host.identity.device_id();
        // Within the grants: in at once (an open confirmation would be
        // denied here).
        let id = attempt(&host, &controller, &dialed, "k7mq2xpa", None).await;
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen", "control-input"]));
        controller.sessions.close(id).unwrap();
        // More than the grants: the host's user is asked and chooses.
        let want = vec![ViewScreen, RunAiTask];
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(vec![ViewScreen]),
            want,
        )
        .await;
        let s = session(&controller, id);
        assert_eq!(s["granted"], json!(["view-screen"]), "{s}");
        controller.sessions.close(id).unwrap();
        // Without a password: asked, and allowed with what the user chose;
        // the host learns who asks.
        let id = attempt(&host, &controller, &dialed, "", Some(vec![ViewScreen])).await;
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen"]));
        let hosted = host
            .sessions
            .list()
            .as_array()
            .unwrap()
            .last()
            .unwrap()
            .clone();
        assert_eq!(hosted["peer"], json!(controller.about()));
        controller.sessions.close(id).unwrap();
        // A denied request counts as a failed login: the fifth blocks the
        // address.
        for _ in 0..crate::access::FAILS_PER_ADDRESS {
            let id = attempt(&host, &controller, &dialed, "", None).await;
            assert_eq!(
                session(&controller, id)["failure"],
                json!({"refused": "denied"})
            );
        }
        let id = attempt(&host, &controller, &dialed, "k7mq2xpa", None).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "rate-limited"})
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn names_cross_only_after_the_login() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.about.lock().unwrap().set_name("Living room").unwrap();
        controller.about.lock().unwrap().set_name("Laptop").unwrap();
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        // A wrong password learns nothing, and tells nothing.
        let stranger = device("1.0.2");
        stranger.about.lock().unwrap().set_name("Stranger").unwrap();
        let id = attempt(&host, &stranger, &dialed, "wrong-one", None).await;
        assert_eq!(session(&stranger, id)["peer"]["name"], "");
        let hosts = host.sessions.list();
        assert!(
            hosts
                .as_array()
                .unwrap()
                .iter()
                .all(|e| e["peer"]["name"] == ""),
            "the host kept a name from a failed login"
        );
        let view = vec![Permission::ViewScreen];
        let id = attempt(&host, &controller, &dialed, "k7mq2xpa", Some(view)).await;
        assert_eq!(session(&controller, id)["state"], "connected");
        assert_eq!(session(&controller, id)["peer"]["name"], "Living room");
        assert_eq!(controller.access.recent()[0].1.about.name, "Living room");
        let trusted = host.access.trusted();
        assert_eq!(trusted.values().next().unwrap().about.name, "Laptop");
        let hosts = host.sessions.list();
        assert!(
            hosts
                .as_array()
                .unwrap()
                .iter()
                .any(|e| e["state"] == "connected" && e["peer"]["name"] == "Laptop")
        );
    }

    /// A host offers its session actions only to a controller that may
    /// control input; the user mode cannot send Ctrl+Alt+Del. (Locking and
    /// blocking would act on this machine's desktop and are not run here.)
    #[cfg(windows)]
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn session_actions_are_offered_with_control_input_only() {
        for (granted, offered) in [
            (vec![Permission::ViewScreen, Permission::ControlInput], true),
            (vec![Permission::ViewScreen], false),
        ] {
            let host = device("1.0.0");
            let controller = device("1.0.1");
            host.access
                .slots
                .lock()
                .unwrap()
                .set_temporary("k7mq2xpa", &host.identity.public_key())
                .unwrap();
            let dialed = host.identity.device_id();
            let id = attempt(&host, &controller, &dialed, "k7mq2xpa", Some(granted)).await;
            let (queue, _) = controller.sessions.media(id).unwrap();
            let until = Instant::now() + Duration::from_secs(3);
            let mut actions = None;
            while actions.is_none() && Instant::now() < until {
                if let Ok(p) = queue.pop(Duration::from_millis(200))
                    && p[0] == media::packet::ACTIONS
                {
                    actions = Some(serde_json::from_slice::<Value>(&p[1..]).unwrap());
                }
            }
            match (offered, actions) {
                (true, Some(a)) => assert_eq!(
                    a,
                    json!({"ctrl_alt_del": false, "blocked": false, "privacy": false})
                ),
                (false, None) => {}
                (_, a) => panic!("offered {offered}, got {a:?}"),
            }
            controller.sessions.close(id).unwrap();
        }
    }

    /// A Map-mode key travels from the controller's GUI through the session
    /// to the host's injector by scan code, press and release. (The
    /// injector's test sink records instead of injecting.)
    #[cfg(windows)]
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn map_mode_keys_reach_the_host_by_scan_code() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let granted = vec![Permission::ViewScreen, Permission::ControlInput];
        let id = attempt(&host, &controller, &dialed, "k7mq2xpa", Some(granted)).await;
        assert_eq!(session(&controller, id)["state"], "connected");
        let (_, control) = controller.sessions.media(id).unwrap();
        // Right Ctrl (extended) and D, which no other test presses.
        let ours = |s: &(u16, u16, u32, usize)| s.2 & 0x8 != 0 && matches!(s.1, 0x1D | 0x20);
        for (code, down) in [(0xE01D, true), (0x20, true), (0x20, false), (0xE01D, false)] {
            let event = json!({"kind": "scan", "code": code, "down": down});
            control
                .send(json!({"type": "input", "event": event}))
                .await
                .unwrap();
        }
        let until = Instant::now() + Duration::from_secs(3);
        let mut sent = Vec::new();
        while sent.len() < 4 && Instant::now() < until {
            sent.extend(crate::input::take_sent(ours));
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        if sent.is_empty() {
            // The injector drops keys while a Kyokai window is in front.
            controller.sessions.close(id).unwrap();
            return;
        }
        let tag = crate::input::INJECTED_TAG;
        assert_eq!(
            sent,
            [
                (0, 0x1D, 0x8 | 0x1, tag),
                (0, 0x20, 0x8, tag),
                (0, 0x20, 0x8 | 0x2, tag),
                (0, 0x1D, 0x8 | 0x1 | 0x2, tag),
            ]
        );
        controller.sessions.close(id).unwrap();
    }

    #[test]
    fn session_actions_have_wire_names() {
        for (action, name) in [
            (SessionAction::Lock, "lock"),
            (SessionAction::CtrlAltDel, "ctrl-alt-del"),
            (SessionAction::Block, "block"),
            (SessionAction::Unblock, "unblock"),
            (SessionAction::PrivacyOn, "privacy-on"),
            (SessionAction::PrivacyOff, "privacy-off"),
        ] {
            let m = serde_json::to_value(Message::Action { action }).unwrap();
            assert_eq!(m, json!({"type": "action", "action": name}));
        }
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
        attempt_remembering(
            host,
            controller,
            dialed,
            password,
            decide,
            want,
            Remember::default(),
        )
        .await
    }

    /// `attempt_with`, doing `remember` with the password.
    async fn attempt_remembering(
        host: &Arc<Ctx>,
        controller: &Arc<Ctx>,
        dialed: &str,
        password: &str,
        decide: Option<Vec<Permission>>,
        want: Vec<Permission>,
        remember: Remember,
    ) -> u64 {
        let (a, b) = tokio::io::duplex(1 << 16);
        let from: IpAddr = "10.126.126.2".parse().unwrap();
        tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(controller, dialed, remember);
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
            let s = session(controller, id);
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
        // The host keeps one AI record: a task its own user starts shows
        // to the controller too, which may stop it.
        let own = rig.ai.start("host", "Something else", None).unwrap();
        let shown = ai_packet(&queue, "the host user's task", |k, t| {
            k == media::packet::AI_TASK && t["id"] == own
        });
        assert!(shown["origin"].is_null(), "started here: {shown}");
        control
            .try_send(json!({"type": "ai-command", "id": own, "command": "cancel"}))
            .unwrap();
        ai_packet(&queue, "the host user's task, stopped", |k, t| {
            k == media::packet::AI_TASK && t["id"] == own && t["state"] == "cancelled"
        });
        // The draft of the next instruction is shared: the controller's
        // reaches the host, the host's (or another controller's) reaches it.
        control
            .try_send(json!({"type": "ai-draft", "text": "Rename the"}))
            .unwrap();
        let until = Instant::now() + Duration::from_secs(10);
        while rig.ai.draft().1 != "Rename the" {
            assert!(Instant::now() < until, "the host never got the draft");
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        rig.ai.set_draft("Rename the report").unwrap();
        let until = Instant::now() + Duration::from_secs(10);
        loop {
            assert!(Instant::now() < until, "the controller never got the draft");
            let Ok(p) = queue.pop(Duration::from_millis(200)) else {
                continue;
            };
            if p[0] == media::packet::AI_DRAFT
                && serde_json::from_slice::<Value>(&p[1..]).unwrap()["text"] == "Rename the report"
            {
                break;
            }
        }
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
        // The host's codec offer comes first.
        let start = loop {
            let p = queue.pop(Duration::from_secs(5)).unwrap();
            if p[0] != media::packet::CODECS {
                break p;
            }
        };
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
            if p[0] == media::packet::CURSOR || p[0] == media::packet::DISPLAYS {
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
            "h264" | "hevc" | "vp9" | "vp9-sw" | "av1" | "vp8" => assert_eq!(
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

    /// A password is kept only after a login the host accepts, and a kept
    /// one the host refuses (it changed) is forgotten.
    #[tokio::test]
    async fn saved_passwords_are_kept_on_success_and_forgotten_when_refused() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::ViewScreen];
        let save = Remember {
            save: true,
            saved: false,
        };
        // A refused password is not kept.
        let id = attempt_remembering(
            &host,
            &controller,
            &dialed,
            "wrong-one",
            None,
            want.clone(),
            save,
        )
        .await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "password"})
        );
        assert!(controller.access.saved_password(&dialed).is_none());
        // An accepted one is.
        let id = attempt_remembering(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            Some(want.clone()),
            want.clone(),
            save,
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        assert_eq!(
            controller.access.saved_password(&dialed).unwrap().as_str(),
            "k7mq2xpa"
        );
        // The host's password changes: the kept one is refused and forgotten.
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("n3wpassw", &host.identity.public_key())
            .unwrap();
        let used = Remember {
            save: false,
            saved: true,
        };
        let id =
            attempt_remembering(&host, &controller, &dialed, "k7mq2xpa", None, want, used).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "password"})
        );
        assert!(controller.access.saved_password(&dialed).is_none());
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
        let (id, stop) = begin(&controller, &dialed, Remember::default());
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

    /// Waits for the next media packet of `kind`, skipping the others.
    fn next_packet(queue: &media::Queue, kind: u8, what: &str) -> Value {
        let until = Instant::now() + Duration::from_secs(8);
        while Instant::now() < until {
            if let Ok(p) = queue.pop(Duration::from_millis(200))
                && p[0] == kind
            {
                return serde_json::from_slice(&p[1..]).unwrap_or(Value::Null);
            }
        }
        panic!("no {what} from the host");
    }

    // Multithreaded: the test waits for media with a blocking pop.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_viewer_s_choices_reach_the_host_and_survive_a_resume() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let from: IpAddr = "10.126.126.3".parse().unwrap();
        let (a, b) = tokio::io::duplex(1 << 20);
        let host_side = tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(&controller, &dialed, Remember::default());
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
        for _ in 0..200 {
            if let Some(q) = host
                .access
                .pending()
                .as_array()
                .and_then(|a| a.first().cloned())
            {
                let id = q["id"].as_u64().unwrap();
                host.access
                    .decide(id, Some(vec![Permission::ViewScreen]))
                    .unwrap();
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        let (queue, control) = controller.sessions.media(id).unwrap();
        // The host offers what it can send: the software codecs at least.
        let offer = next_packet(&queue, media::packet::CODECS, "codec offer");
        let codecs: Vec<String> = serde_json::from_value(offer["codecs"].clone()).unwrap();
        assert!(codecs.iter().any(|c| c == "vp9-sw"), "{codecs:?}");
        // A frame-rate cap of 5 shows in the host's stats (it starts at 15).
        control
            .try_send(json!({"type": "quality", "preset": "balanced", "fps": 5, "i444": false}))
            .unwrap();
        let capped = |queue: &media::Queue| {
            let until = Instant::now() + Duration::from_secs(8);
            while Instant::now() < until {
                let stats = next_packet(queue, media::packet::STATS, "stats");
                if stats["fps"] == 5 {
                    return true;
                }
            }
            false
        };
        assert!(capped(&queue), "the cap never reached the host");
        // Choosing a codec restarts the video in it (the test pattern where
        // this machine cannot capture).
        control
            .try_send(json!({"type": "codec", "codec": "vp8"}))
            .unwrap();
        let start = next_packet(&queue, media::packet::VIDEO_START, "restarted video");
        assert!(
            matches!(start["codec"].as_str(), Some("vp8" | "raw-rgba")),
            "{start}"
        );
        // The connection drops and resumes: the host gets the choices again
        // before the video starts.
        host_side.abort();
        let Outcome::Lost(token) = first.await.unwrap() else {
            panic!("a dropped session should be resumable");
        };
        while queue.pop(Duration::from_millis(50)).is_ok() {}
        let (a, b) = tokio::io::duplex(1 << 20);
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
        let start = next_packet(&queue, media::packet::VIDEO_START, "resumed video");
        assert!(
            matches!(start["codec"].as_str(), Some("vp8" | "raw-rgba")),
            "{start}"
        );
        assert!(capped(&queue), "a resumed session lost the viewer's cap");
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
            let (id, stop) = begin(&controller, "100000002", Remember::default());
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
            .pin_host(&dialed, "c29tZW9uZSBlbHNl", &About::default(), "")
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
        board_has(board, &crate::clipboard::Item::Text(want.into())).await
    }

    /// Waits until a fake clipboard holds `want`.
    async fn board_has(
        board: &crate::clipboard::fake::Board,
        want: &crate::clipboard::Item,
    ) -> bool {
        for _ in 0..150 {
            if board.lock().unwrap().as_ref() == Some(want) {
                return true;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        false
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_clipboard_is_shared_both_ways_only_with_its_permission() {
        let (host_board, controller_board) = (
            Arc::new(std::sync::Mutex::new(Some(crate::clipboard::Item::Text(
                "on the host before".to_string(),
            )))),
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
        *controller_board.lock().unwrap() = Some(crate::clipboard::Item::Text(
            "copied on the controller".into(),
        ));
        assert!(board_holds(&host_board, "copied on the controller").await);
        *host_board.lock().unwrap() =
            Some(crate::clipboard::Item::Text("copied on the host".into()));
        assert!(board_holds(&controller_board, "copied on the host").await);
        // An image crosses as PNG.
        let image = crate::clipboard::Item::Image(crate::clipboard::Image {
            width: 32,
            height: 16,
            rgba: (0..32 * 16 * 4).map(|i| i as u8).collect(),
        });
        *controller_board.lock().unwrap() = Some(image.clone());
        assert!(board_has(&host_board, &image).await);
        controller.sessions.close(id).unwrap();

        // Without the permission nothing crosses (a second controller).
        let other_board: crate::clipboard::fake::Board = Arc::default();
        let other = device_full(rig.ai.clone(), other_board.clone(), "1.0.2");
        let want = vec![Permission::ViewScreen];
        let id = attempt_with(&host, &other, &dialed, "k7mq2xpa", Some(want.clone()), want).await;
        assert_eq!(session(&other, id)["state"], "connected");
        *other_board.lock().unwrap() = Some(crate::clipboard::Item::Text("private".into()));
        assert!(!board_holds(&host_board, "private").await);
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_clipboard_follows_the_tab_in_front_and_the_viewer_s_switch() {
        use crate::clipboard::Item;
        let text = |t: &str| Some(Item::Text(t.into()));
        let (host_board, controller_board): (
            crate::clipboard::fake::Board,
            crate::clipboard::fake::Board,
        ) = (Arc::default(), Arc::default());
        let rig = fake::rig("session-clip-use", "claude", "http://127.0.0.1:9");
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
        let (queue, control) = controller.sessions.media(id).unwrap();
        let send = |share: bool, front: bool| {
            control
                .try_send(json!({"type": "clipboard-use", "share": share, "front": front}))
                .unwrap();
        };
        // Behind: nothing crosses either way; the copy waits.
        send(true, false);
        tokio::time::sleep(Duration::from_millis(700)).await;
        *controller_board.lock().unwrap() = text("copied while behind");
        *host_board.lock().unwrap() = text("the host's while behind");
        assert!(!board_holds(&controller_board, "the host's while behind").await);
        assert!(!board_holds(&host_board, "copied while behind").await);
        // In front: the waiting copy goes, and the host's copies come again.
        send(true, true);
        assert!(board_holds(&host_board, "copied while behind").await);
        *host_board.lock().unwrap() = text("the host's in front");
        assert!(board_holds(&controller_board, "the host's in front").await);
        // Not shared: nothing crosses, and what was copied meanwhile is not
        // sent once it is shared again; later copies are.
        send(false, true);
        tokio::time::sleep(Duration::from_millis(700)).await;
        *controller_board.lock().unwrap() = text("copied while off");
        *host_board.lock().unwrap() = text("the host's while off");
        assert!(!board_holds(&controller_board, "the host's while off").await);
        send(true, true);
        assert!(!board_holds(&host_board, "copied while off").await);
        *controller_board.lock().unwrap() = text("copied again");
        assert!(board_holds(&host_board, "copied again").await);
        // The local message never reached the host: the session is up.
        assert_eq!(session(&controller, id)["state"], "connected");
        // The history holds what crossed, by side, in order.
        let mut history = Vec::new();
        while let Ok(p) = queue.pop(Duration::from_millis(100)) {
            if p.first() == Some(&media::packet::CLIPBOARD) {
                let v: Value = serde_json::from_slice(&p[1..]).unwrap();
                assert!(v["at"].as_u64().unwrap() > 0, "{v}");
                history.push((
                    v["from"].as_str().unwrap().to_owned(),
                    v["text"].as_str().unwrap().to_owned(),
                ));
            }
        }
        let want: Vec<(String, String)> = [
            ("controller", "copied while behind"),
            ("host", "the host's in front"),
            ("controller", "copied again"),
        ]
        .iter()
        .map(|(a, b)| (a.to_string(), b.to_string()))
        .collect();
        assert_eq!(history, want);
    }

    #[test]
    fn a_reader_that_is_not_reading_keeps_a_few_clipboard_packets() {
        let q = media::Queue::default();
        for i in 0..20u8 {
            q.push(vec![media::packet::CLIPBOARD, i]);
        }
        let mut kept = Vec::new();
        while let Ok(p) = q.pop(Duration::from_millis(10)) {
            kept.push(p[1]);
        }
        assert_eq!(kept, (12..20).collect::<Vec<u8>>(), "the newest stay");
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

    #[test]
    fn long_messages_go_in_parts_and_come_back_whole() {
        for len in [MAX_PLAINTEXT + 1, 3 * MAX_PLAINTEXT + 17, 1 << 20] {
            let msg: Vec<u8> = (0..len).map(|i| (i % 251) as u8).collect();
            let parts = split(&msg).unwrap();
            assert!(parts.iter().all(|p| p.len() <= MAX_PLAINTEXT));
            let mut back = Parts::default();
            let mut whole = None;
            for p in &parts {
                whole = back.push(p).unwrap();
            }
            assert_eq!(whole.unwrap(), msg);
        }
        assert!(split(&vec![0; MAX_MESSAGE + 1]).is_none());
        let parts = split(&vec![7; 3 * MAX_PLAINTEXT]).unwrap();
        assert!(Parts::default().push(&parts[1]).is_err(), "out of order");
        assert!(Parts::default().push(&[PART_TAG, 0]).is_err(), "malformed");
        assert!(
            Parts::default().push(&[PART_TAG, 0, 2, 0, 1]).is_err(),
            "index past the count"
        );
    }

    /// Regression: a message longer than one frame (a long copied text) used
    /// to fail its encryption and end the session.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_long_clipboard_text_crosses_in_parts() {
        let (host_board, controller_board): (
            crate::clipboard::fake::Board,
            crate::clipboard::fake::Board,
        ) = (Arc::default(), Arc::default());
        let rig = fake::rig("session-long", "claude", "http://127.0.0.1:9");
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
        let long: String = "kyokai ".repeat(30_000);
        // Sharing starts after the session does, and what was copied before
        // stays put: a short text arriving first shows it is live (a fixed
        // wait raced a busy machine).
        let mut live = false;
        for n in 0..20 {
            let marker = format!("ready {n}");
            *controller_board.lock().unwrap() = Some(crate::clipboard::Item::Text(marker.clone()));
            if board_holds(&host_board, &marker).await {
                live = true;
                break;
            }
        }
        assert!(live, "clipboard sharing never started");
        *controller_board.lock().unwrap() = Some(crate::clipboard::Item::Text(long.clone()));
        assert!(
            board_holds(&host_board, &long).await,
            "the long text arrived"
        );
        assert_eq!(
            session(&controller, id)["state"],
            "connected",
            "the session goes on"
        );
    }

    /// Waits until a fake clipboard holds files.
    async fn board_files(board: &crate::clipboard::fake::Board) -> Option<Vec<std::path::PathBuf>> {
        for _ in 0..250 {
            if let Some(crate::clipboard::Item::Files(f)) = board.lock().unwrap().clone() {
                return Some(f);
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        None
    }

    /// A folder of its own for a test's files.
    fn files_dir(name: &str) -> std::path::PathBuf {
        let d = test_dir().join(name);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn files_cross_both_ways_only_with_their_own_permission() {
        let (host_board, controller_board): (
            crate::clipboard::fake::Board,
            crate::clipboard::fake::Board,
        ) = (Arc::default(), Arc::default());
        let rig = fake::rig("session-files", "claude", "http://127.0.0.1:9");
        let host = device_full(rig.ai.clone(), host_board.clone(), "1.0.0");
        let controller = device_full(rig.ai.clone(), controller_board.clone(), "1.0.1");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let want = vec![Permission::Files];
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
        tokio::time::sleep(Duration::from_millis(600)).await;
        // A folder and a file copied on the controller paste on the host.
        let src = files_dir("controller-copy");
        std::fs::create_dir_all(src.join("photos/empty")).unwrap();
        let big: Vec<u8> = (0..300_000u32).map(|i| (i % 253) as u8).collect();
        std::fs::write(src.join("photos/big.bin"), &big).unwrap();
        std::fs::write(src.join("notes.txt"), b"hello").unwrap();
        *controller_board.lock().unwrap() = Some(crate::clipboard::Item::Files(vec![
            src.join("photos"),
            src.join("notes.txt"),
        ]));
        let got = board_files(&host_board).await.expect("the files arrived");
        assert_eq!(got.len(), 2);
        assert!(
            got.iter().all(|p| !p.starts_with(&src)),
            "copies, not the originals"
        );
        assert_eq!(std::fs::read(got[0].join("big.bin")).unwrap(), big);
        assert!(got[0].join("empty").is_dir());
        assert_eq!(std::fs::read(&got[1]).unwrap(), b"hello");
        let inbox = got[0].parent().unwrap().parent().unwrap().to_path_buf();
        // And the other way round.
        *controller_board.lock().unwrap() = None;
        let back = files_dir("host-copy").join("report.txt");
        std::fs::write(&back, b"from the host").unwrap();
        *host_board.lock().unwrap() = Some(crate::clipboard::Item::Files(vec![back]));
        let got = board_files(&controller_board)
            .await
            .expect("files to the controller");
        assert_eq!(std::fs::read(&got[0]).unwrap(), b"from the host");
        // `files` alone shares no text.
        *controller_board.lock().unwrap() = Some(crate::clipboard::Item::Text("words".into()));
        assert!(!board_holds(&host_board, "words").await);
        // The received files go when the session ends.
        controller.sessions.close(id).unwrap();
        let mut gone = false;
        for _ in 0..150 {
            if !inbox.exists() {
                gone = true;
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert!(gone, "{} was left behind", inbox.display());

        // `clipboard` alone carries no files (a second controller).
        let other_board: crate::clipboard::fake::Board = Arc::default();
        let other = device_full(rig.ai.clone(), other_board.clone(), "1.0.2");
        let want = vec![Permission::Clipboard];
        let id = attempt_with(&host, &other, &dialed, "k7mq2xpa", Some(want.clone()), want).await;
        assert_eq!(session(&other, id)["state"], "connected");
        *host_board.lock().unwrap() = None;
        tokio::time::sleep(Duration::from_millis(600)).await;
        let secret = files_dir("other-copy").join("secret.txt");
        std::fs::write(&secret, b"private").unwrap();
        *other_board.lock().unwrap() = Some(crate::clipboard::Item::Files(vec![secret]));
        assert!(board_files(&host_board).await.is_none());
    }

    /// Two ends of an established channel in memory, without a login.
    fn raw_channels() -> (
        Channel<tokio::io::DuplexStream>,
        Channel<tokio::io::DuplexStream>,
    ) {
        let (a, b) = tokio::io::duplex(1 << 20);
        let (mut i, _) = noise(true).unwrap();
        let (mut r, _) = noise(false).unwrap();
        let (mut msg, mut out) = (vec![0u8; MAX_FRAME], vec![0u8; MAX_FRAME]);
        for initiator in [true, false, true] {
            let (w, rd) = if initiator {
                (&mut i, &mut r)
            } else {
                (&mut r, &mut i)
            };
            let n = w.write_message(&[], &mut msg).unwrap();
            rd.read_message(&msg[..n], &mut out).unwrap();
        }
        let channel = |stream, hs: snow::HandshakeState| Channel {
            stream,
            noise: hs.into_transport_mode().unwrap(),
            buf: vec![0u8; MAX_FRAME],
        };
        (channel(a, i), channel(b, r))
    }

    /// Reads a raw channel for `wait` and counts the frames whose last chunk
    /// arrived. Returns: the count and the newest frame's number.
    async fn raw_frames(
        ch: &mut Channel<tokio::io::DuplexStream>,
        wait: Duration,
    ) -> (usize, Option<u32>) {
        let until = Instant::now() + wait;
        let (mut count, mut newest) = (0, None);
        let mut plain = vec![0u8; MAX_FRAME];
        while let Some(left) = until.checked_duration_since(Instant::now()) {
            let Ok(Ok(frame)) = tokio::time::timeout(left, read_frame(&mut ch.stream)).await else {
                break;
            };
            let n = ch.noise.read_message(&frame, &mut plain).unwrap();
            if let Some(no) = media::last_chunk(&plain[..n]) {
                count += 1;
                newest = Some(no);
            }
        }
        (count, newest)
    }

    /// Regression: on a slow relay the host kept writing frames into the
    /// transport, seconds of video queued ahead of the picture and of the
    /// delay probe. It sends at most IN_FLIGHT frames ahead of the
    /// controller's acknowledgements.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_host_sends_only_a_few_frames_ahead_of_the_acknowledgements() {
        let (host_end, mut peer) = raw_channels();
        let stop = Arc::new(Notify::new());
        let host = device("1.0.0");
        let side = Side::Host {
            video: true,
            sound: false,
            input: false,
            ai: HostAi {
                ai: host.ai.clone(),
                peer: "123456789".into(),
                run: false,
                approve: false,
            },
        };
        let stopped = stop.clone();
        let task = tokio::spawn(async move { run(host_end, &stopped, side, None).await });
        // No codec this host encodes: the 30 fps test pattern.
        peer.send(&Message::Codecs {
            decode: vec!["none".into()],
            i444: vec![],
        })
        .await
        .unwrap();
        let (count, newest) = raw_frames(&mut peer, Duration::from_millis(800)).await;
        assert_eq!(count, IN_FLIGHT as usize, "frames without acknowledgements");
        // An acknowledgement of a frame never sent moves nothing.
        peer.send(&Message::Received { frame: 1000 }).await.unwrap();
        assert_eq!(raw_frames(&mut peer, Duration::from_millis(300)).await.0, 0);
        peer.send(&Message::Received {
            frame: newest.unwrap(),
        })
        .await
        .unwrap();
        let (count, _) = raw_frames(&mut peer, Duration::from_millis(500)).await;
        assert_eq!(
            count, IN_FLIGHT as usize,
            "frames after one acknowledgement"
        );
        stop.notify_one();
        let _ = task.await;
    }

    /// Reads from a raw channel until a message of `kind` arrives.
    /// Returns: its id, or None after a second without one.
    async fn raw_wait(ch: &mut Channel<tokio::io::DuplexStream>, kind: &str) -> Option<u64> {
        let until = Instant::now() + Duration::from_secs(1);
        loop {
            let left = until.checked_duration_since(Instant::now())?;
            let m: Value = tokio::time::timeout(left, ch.recv()).await.ok()?.ok()?;
            if m["type"] == kind {
                return m["id"].as_u64();
            }
        }
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn offers_with_unsafe_names_or_over_the_limits_are_refused() {
        let board: crate::clipboard::fake::Board = Arc::default();
        let (host_end, mut peer) = raw_channels();
        let stop = Arc::new(Notify::new());
        let host = device("1.0.0");
        let side = Side::Host {
            video: false,
            sound: false,
            input: false,
            ai: HostAi {
                ai: host.ai.clone(),
                peer: "123456789".into(),
                run: false,
                approve: false,
            },
        };
        let shared = Shared {
            board: Box::new(crate::clipboard::fake::Fake(board.clone())),
            text: false,
            files: true,
        };
        let stopped = stop.clone();
        let task = tokio::spawn(async move { run(host_end, &stopped, side, Some(shared)).await });
        let entry = |path: &str, size: u64| crate::files::Entry {
            path: path.into(),
            size,
            dir: false,
        };
        for (id, files) in [
            (1, vec![entry("../escape.txt", 4)]),
            (2, vec![entry("C:/Windows/evil.dll", 4)]),
            (3, vec![entry("folder/NUL", 4)]),
            (4, vec![entry("a.txt", 4), entry("A.TXT", 4)]),
            (5, vec![entry("huge.bin", crate::files::MAX_TOTAL + 1)]),
            (
                6,
                (0..=crate::files::MAX_ENTRIES)
                    .map(|i| entry(&format!("f{i}"), 0))
                    .collect(),
            ),
        ] {
            peer.send(&Message::ClipboardFiles { id, files })
                .await
                .unwrap();
            assert_eq!(
                raw_wait(&mut peer, "files-get").await,
                None,
                "offer {id} accepted"
            );
        }
        // A good offer is accepted, and only exactly its bytes are taken.
        peer.send(&Message::ClipboardFiles {
            id: 7,
            files: vec![entry("ok.txt", 4)],
        })
        .await
        .unwrap();
        assert_eq!(raw_wait(&mut peer, "files-get").await, Some(7));
        let mut piece = vec![crate::files::TAG];
        piece.extend_from_slice(&7u64.to_be_bytes());
        piece.extend_from_slice(b"data");
        seal(&mut peer.noise, &mut peer.buf, &mut peer.stream, &piece)
            .await
            .unwrap();
        let got = board_files(&board).await.expect("the good offer arrived");
        assert_eq!(std::fs::read(&got[0]).unwrap(), b"data");
        // More bytes than offered: the writer refuses, the sender is told.
        peer.send(&Message::ClipboardFiles {
            id: 8,
            files: vec![entry("short.txt", 2)],
        })
        .await
        .unwrap();
        assert_eq!(raw_wait(&mut peer, "files-get").await, Some(8));
        let mut piece = vec![crate::files::TAG];
        piece.extend_from_slice(&8u64.to_be_bytes());
        piece.extend_from_slice(b"toolong");
        seal(&mut peer.noise, &mut peer.buf, &mut peer.stream, &piece)
            .await
            .unwrap();
        assert_eq!(raw_wait(&mut peer, "files-stop").await, Some(8));
        stop.notify_one();
        assert!(task.await.unwrap().is_ok());
    }

    /// A device at an address tells its Device ID before any login, and
    /// the host is left with no session.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn identify_learns_the_device_id_at_an_address() {
        let host = device("0.3.0");
        let (a, b) = tokio::io::duplex(1 << 16);
        tokio::spawn(serve(host.clone(), b, "192.168.1.20".parse().unwrap()));
        assert_eq!(identify(a).await.unwrap(), host.identity.device_id());
        assert!(host.sessions.list().as_array().unwrap().is_empty());
    }

    /// Removing a host from Home ends its sessions from here and leaves
    /// none of them listed (regression: a removed device came back from
    /// its finished session).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn removing_a_host_drops_its_sessions_from_the_list() {
        let (host, controller) = (device("0.3.0"), device("0.3.0"));
        let dialed = host.identity.device_id();
        // Refused: a finished session.
        attempt(&host, &controller, &dialed, "", None).await;
        // Waiting for the host's user: a live one.
        let (a, b) = tokio::io::duplex(1 << 16);
        tokio::spawn(serve(host.clone(), b, "192.168.1.20".parse().unwrap()));
        let (id, stop) = begin(&controller, &dialed, Remember::default());
        let c = controller.clone();
        let d = dialed.clone();
        let run = tokio::spawn(async move {
            connect(
                c,
                id,
                stop,
                a,
                d,
                zeroize::Zeroizing::new(String::new()),
                vec![Permission::ViewScreen],
                None,
            )
            .await
        });
        // Until the host's user is asked (slow under a full test run).
        for _ in 0..500 {
            if session(&controller, id)["state"] == "confirming"
                && !host.access.pending().as_array().unwrap().is_empty()
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(controller.sessions.list().as_array().unwrap().len(), 2);
        controller.sessions.remove_host(&dialed);
        assert!(controller.sessions.list().as_array().unwrap().is_empty());
        // Once it ends (here: the host's user says no) it is gone.
        let q = host.access.pending()[0]["id"].as_u64().unwrap();
        host.access.decide(q, None).unwrap();
        tokio::time::timeout(Duration::from_secs(5), run)
            .await
            .unwrap()
            .unwrap();
        assert!(controller.sessions.inner.lock().unwrap().1.is_empty());
    }

    /// A host that accepts connections by address answers on its own
    /// addresses, keeps listening when its ranges change (regression: the
    /// old listener still held the port), and closes a connection from
    /// outside them.
    #[test]
    fn a_host_accepts_by_address_from_its_ranges() {
        use crate::access::ByAddress;
        if std::net::TcpListener::bind(("0.0.0.0", crate::net::KYOKAI_PORT)).is_err() {
            return eprintln!("skipped: port 47310 is taken on this computer");
        }
        let host = device("0.3.0");
        let mut net =
            crate::net::Network::start(host.access.secrets(), host.clone(), false).unwrap();
        let on = |ranges: &[&str]| ByAddress {
            on: true,
            ranges: ranges.iter().map(|r| r.to_string()).collect(),
        };
        let identify =
            |net: &crate::net::Network| net.handle().block_on(crate::net::identify("127.0.0.1"));
        host.access.set_by_address(on(&["10.0.0.0/8"])).unwrap();
        net.accept_by_address();
        assert_eq!(identify(&net), Err("broken"), "outside the ranges");
        host.access.set_by_address(on(&["127.0.0.1"])).unwrap();
        net.accept_by_address();
        assert_eq!(net.by_address_status()["port"], crate::net::KYOKAI_PORT);
        assert_eq!(identify(&net), Ok(host.identity.device_id()));
        host.access.set_by_address(ByAddress::default()).unwrap();
        net.accept_by_address();
        assert_eq!(identify(&net), Err("unreachable"), "off");
    }
}
