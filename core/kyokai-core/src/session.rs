//! Sessions between a controller and a host (doc/en-us/protocol.md): a Noise
//! XX handshake that proves both device keys, OPAQUE password login bound to
//! the handshake, the host user's confirmation, then a heartbeat until
//! either side leaves. Generic over the stream, so tests run it in memory.

use crate::about::About;
use crate::access::{Access, CONFIRM_TIMEOUT, Permission, Refusal, Serve, Source};
use crate::auth::{self, Slot};
use crate::identity::{self, Identity};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::sync::{Notify, mpsc};

use crate::{audio, media, video};

/// Version of the wire protocol. It stays 10 while newer abilities are
/// announced as features (doc/en-us/protocol.md, "Compatibility").
pub const PROTOCOL: u32 = 10;
/// The oldest protocol this build speaks; a peer outside
/// MIN_PROTOCOL..=PROTOCOL is refused.
pub const MIN_PROTOCOL: u32 = 10;

/// Feature tokens a hello may announce. Announcing the list at all (even
/// empty) says the sender ignores message types and permission names it
/// does not know; 0.4.0 and older send none and end a session on them.
// Each token is used once the feature it names lands in this build.
#[allow(dead_code)]
pub mod feature {
    /// Sign-in with an SSH-style key (doc/en-us/identity-and-access.md).
    pub const KEY_LOGIN: &str = "key-login";
    /// Tunnels to the host's own SSH, Remote Desktop and VNC.
    pub const TUNNEL: &str = "tunnel";
    /// Quiet status watchers and the `system` message.
    pub const STATUS: &str = "status";
    /// The controller's `sync` input event (the keys it holds).
    pub const INPUT_SYNC: &str = "input-sync";
    /// The `wake` session action.
    pub const WAKE: &str = "wake";
    /// The `no-picture` message: the host recovers a picture its viewer
    /// says it does not show.
    pub const RECOVERY: &str = "recovery";
}

/// The features this build announces.
pub const FEATURES: &[&str] = &[
    feature::KEY_LOGIN,
    feature::TUNNEL,
    feature::STATUS,
    feature::INPUT_SYNC,
    feature::WAKE,
    feature::RECOVERY,
];

/// What the other side of a session announced: its features, none for
/// 0.4.0 and older.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Peer {
    pub features: Vec<String>,
}

impl Peer {
    /// Whether the peer announced a feature.
    pub fn has(&self, feature: &str) -> bool {
        self.features.iter().any(|f| f == feature)
    }
}

/// Whether a peer's protocol is one this build speaks.
fn speaks(protocol: u32) -> bool {
    (MIN_PROTOCOL..=PROTOCOL).contains(&protocol)
}

/// The features this build announces, as a hello carries them.
fn our_features() -> Vec<String> {
    FEATURES.iter().map(|f| f.to_string()).collect()
}

use video::IN_FLIGHT;
const PATTERN: &str = "Noise_XX_25519_ChaChaPoly_BLAKE2s";
const PROLOGUE: &[u8] = b"kyokai/session/v1";
const STATIC_TAG: &[u8] = b"kyokai/noise-static/v1";
/// Shown to controllers (AGPL-3.0 section 13).
pub const SOURCE_URL: &str = "https://github.com/YuanZhe-99/Kyokai";
pub(crate) const MAX_FRAME: usize = 65_535;
/// Noise adds a 16-byte tag to every message.
pub(crate) const MAX_PLAINTEXT: usize = MAX_FRAME - 16;
/// How long one handshake step may take.
const STEP_TIMEOUT: Duration = Duration::from_secs(15);
pub(crate) const PING_EVERY: Duration = Duration::from_secs(5);
pub(crate) const IDLE_LIMIT: Duration = Duration::from_secs(15);
/// How long a session that says goodbye waits for the peer to close.
const BYE_WAIT: Duration = Duration::from_millis(500);
/// How long a host that refused a controller waits for its refusal to
/// leave (and the controller to close) before it drops the connection.
const REFUSAL_WAIT: Duration = Duration::from_secs(3);
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
    /// What this build can do beyond protocol 10 (`feature`); absent from
    /// 0.4.0 and older.
    #[serde(default)]
    features: Vec<String>,
}

#[derive(Serialize, Deserialize)]
struct ClientHello {
    protocol: u32,
    version: String,
    device_key: String,
    signature: String,
    #[serde(deserialize_with = "crate::access::known_permissions")]
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
    /// What this build can do beyond protocol 10 (`feature`).
    #[serde(default)]
    features: Vec<String>,
    /// Proofs with sign-in keys, only to a host announcing `key-login`.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    keys: Vec<crate::keys::Proof>,
    /// A tunnel to this service instead of the desktop, only to a host
    /// announcing `tunnel`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    tunnel: Option<crate::tunnel::Service>,
    /// A quiet status watch instead of the desktop, only to a host
    /// announcing `status`.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    watch: bool,
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
    #[serde(default, deserialize_with = "crate::access::known_permissions")]
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
    /// With an acceptance: how the controller got in; absent from 0.4.0.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    via: Option<Via>,
    /// With an acceptance by key: the key that admitted (base64 Ed25519).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    key: Option<String>,
}

/// How a host admitted a controller (`Verdict.via`).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum Via {
    /// A sign-in key, with or without the host user's confirmation.
    Key,
    /// A password, with or without the host user's confirmation.
    Password,
    /// The host's user allowed a controller that gave no password.
    Confirmation,
    /// A resume token.
    Resume,
    /// A newer host's way: treated as none given.
    #[serde(other)]
    Unknown,
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
    /// controller → host (`recovery`): the viewer has shown no picture for
    /// `for_ms`, and why it thinks so (`waiting`: none came yet;
    /// `decode-error`: what came did not decode; `stale`: frames come but
    /// none shows); the host takes the next recovery step.
    NoPicture {
        for_ms: u32,
        #[serde(default)]
        why: String,
    },
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
    /// host → controller: what QoS measured and chose, for the viewer, how
    /// long the host took from a picture to its frame (the average of the
    /// last second's frames), and how many frames it sent that second (a
    /// viewer that gets none of them asks for a key frame; 0 from 0.4.0).
    Stats {
        delay_ms: u32,
        fps: u32,
        bitrate: u32,
        #[serde(default)]
        encode_ms: u32,
        #[serde(default)]
        frames: u32,
        /// The host's video, for the viewer to say what is happening:
        /// `live`, `dark` (the system reports the display off),
        /// `recovering` (the stream was opened again), `placeholder` (the
        /// screen is tried again meanwhile); empty from 0.5.2 and older.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        video: String,
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
    /// A message type a newer peer sends that this build does not know:
    /// ignored (0.4.0 and older ended the session on it).
    #[serde(other)]
    Unknown,
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
    /// Wake the host's displays (sent to hosts announcing `wake`; older
    /// ones get a Shift press and release instead).
    Wake,
    /// An action of a newer controller: ignored.
    #[serde(other)]
    Unknown,
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
    /// A command of a newer controller: ignored.
    #[serde(other)]
    Unknown,
}

/// How long a host waits for the controller's codecs before it starts the
/// video as for a controller that names none (H.264 on Windows).
const CODECS_WAIT: Duration = Duration::from_secs(1);

/// Describes a newly opened video to the controller: the stream, and the
/// displays when there is a choice.
fn announce(out: &Out, s: &video::Source, displays: &[video::Display]) -> Result<(), Failure> {
    out.in_video(&Message::VideoStart(s.start.clone()))?;
    if let Some(m) = shown(displays, s) {
        out.in_video(&m)?;
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

/// Whether a person's input from a session may reach Kyokai's own windows:
/// only while the host user lets it (`allowed`: Hide Kyokai from capture is
/// off), and not while a controller waits for the host user's answer or the
/// AI for approval, so that nobody answers those in the host user's place
/// through Kyokai's window. The host's AI never may (its own injector).
#[cfg(any(windows, target_os = "macos"))]
fn kyokai_reachable(allowed: bool, access: &Access, ai: &crate::ai::Ai) -> bool {
    allowed && !access.asking() && !ai.awaiting_approval()
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

/// Watches the host's pointer for a session on a thread of its own, every
/// POINTER_EVERY: a tokio interval waits in whole ticks of Windows' timer,
/// and in the helper modes each look is a call to the agent, which must not
/// hold up the network's two threads. Dropping it ends the thread.
struct PointerWatch {
    /// The display the pointer is measured against (None: the primary).
    area: Arc<Mutex<Option<crate::input::Area>>>,
    /// Where the pointer is, as fractions of `area`, as it changes.
    at: tokio::sync::watch::Receiver<Option<(f32, f32)>>,
    stop: Arc<AtomicBool>,
}

impl PointerWatch {
    /// Starts watching, against the primary display until told otherwise.
    fn start() -> PointerWatch {
        let area = Arc::new(Mutex::new(None));
        let stop = Arc::new(AtomicBool::new(false));
        let (tx, at) = tokio::sync::watch::channel(None);
        let (shown, done) = (area.clone(), stop.clone());
        std::thread::spawn(move || {
            while !done.load(Ordering::Relaxed) {
                let on = *shown.lock().unwrap_or_else(|e| e.into_inner());
                let now = on.map_or_else(crate::input::pointer, crate::input::pointer_in);
                tx.send_if_modified(|was| {
                    let changed = *was != now;
                    *was = now;
                    changed
                });
                std::thread::sleep(POINTER_EVERY);
            }
        });
        PointerWatch { area, at, stop }
    }

    /// Measures the pointer against another display from now on, and
    /// reports where it is even if it did not move (a new stream).
    fn show(&mut self, area: Option<crate::input::Area>) {
        *self.area.lock().unwrap_or_else(|e| e.into_inner()) = area;
        self.at.mark_changed();
    }
}

impl Drop for PointerWatch {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

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

/// Whether a controller asks the host for a key frame now: its GUI lost
/// frames and it last asked KEY_AGAIN ago or never. Checked as frames come
/// and once a second, so a still screen, which sends nothing, is asked too
/// (regression: the request went out only as a frame arrived).
fn key_due(waiting: bool, asked: Option<Instant>, now: Instant) -> bool {
    waiting && asked.is_none_or(|t| now.saturating_duration_since(t) >= KEY_AGAIN)
}

/// How long frames may wait for the controller's acknowledgement before the
/// host logs the path as slow. Every controller acknowledges every frame
/// (protocol 10) over an ordered, reliable stream, so a late acknowledgement
/// is a slow path, never a lost frame: the host waits for it (a dead
/// connection ends by IDLE_LIMIT). Regression: taking the frames for lost
/// after 3 s sent three more and a fresh key frame onto a path that had not
/// delivered the last ones, every 3 s; on a path of a few hundred kbit/s the
/// backlog grew without end, the picture froze, input paused and the
/// session broke.
const ACK_SLOW: Duration = Duration::from_secs(5);

/// Whether the key frame sent last is still on its way to the controller:
/// a request for another, or the viewer saying it has no picture, waits
/// for it (a fresh key frame would queue behind it, and on a slow path
/// both arrive later).
fn key_in_flight(key_sent: Option<(u32, Instant)>, acked: u32) -> bool {
    key_sent.is_some_and(|(no, _)| (acked.wrapping_sub(no) as i32) < 0)
}

/// How long a host's stream may send nothing while a picture is due (it
/// just opened, or the controller asked for a key frame) before it counts
/// as a stream that gives no picture.
const NO_PICTURE: Duration = Duration::from_secs(5);
/// How long a viewer may say it shows no picture before the host takes the
/// next recovery step, and how long the host then gives the step to work.
const VIEWER_WAIT: Duration = Duration::from_secs(3);
const STEP_GRACE: Duration = Duration::from_secs(4);
/// How long after a key frame was sent a request for another counts as one
/// the viewer could not use.
const KEY_UNUSED: Duration = Duration::from_secs(2);
/// How long a stream must go without trouble for the recovery to start over
/// from its first step.
const RECOVERED: Duration = Duration::from_secs(10);
/// The pause before the second reopening, and how often the screen is tried
/// again while the placeholder shows: sooner at first, then every
/// PROBE_MAX.
const REOPEN_PAUSE: Duration = Duration::from_secs(2);
const PROBE_FIRST: Duration = Duration::from_secs(2);
const PROBE_MAX: Duration = Duration::from_secs(10);

/// What a host does next for a stream whose picture does not reach the
/// viewer: it ended, did not open, sends nothing although a picture is due,
/// or the viewer says it shows none (doc/en-us/computer-use.md, "Video").
#[derive(Debug, PartialEq, Eq)]
enum Recovery {
    /// Open the capture and its encoder again, after this long.
    Reopen(Duration),
    /// Open them again with the next codec the controller decodes first.
    NextCodec,
    /// Show the placeholder and try the screen again after this long.
    Placeholder(Duration),
}

/// The recovery step for the `trouble`-th trouble in a row (0 first): open
/// again at once, again after REOPEN_PAUSE, in the next codec, then the
/// placeholder while the screen is tried again after PROBE_FIRST, doubling
/// up to PROBE_MAX. It never gives up: the screen is tried as long as the
/// session lasts, and a stream that goes RECOVERED without trouble starts
/// over (regression: after three ends within 30 s the host sent the test
/// pattern for good, and a Mac whose display had slept never showed its
/// screen again).
fn recovery(trouble: u32) -> Recovery {
    match trouble {
        0 => Recovery::Reopen(Duration::ZERO),
        1 => Recovery::Reopen(REOPEN_PAUSE),
        2 => Recovery::NextCodec,
        n => Recovery::Placeholder(
            PROBE_FIRST
                .saturating_mul(2u32.saturating_pow((n - 3).min(8)))
                .min(PROBE_MAX),
        ),
    }
}

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
    /// The host announced no support for what was asked (a tunnel, a
    /// status watch): nothing was asked of it.
    Unsupported,
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

/// What a session carries.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Kind {
    /// The desktop: video, input, sound, the clipboard and AI tasks.
    #[default]
    Desktop,
    /// A tunnel to the host's own service (doc/en-us/remote-services.md).
    Tunnel(crate::tunnel::Service),
    /// A quiet watch of the host's status (doc/en-us/computer-use.md,
    /// "Host status"): left out of the sessions listed.
    Watch,
}

impl Kind {
    /// A tunnel's service.
    fn service(self) -> Option<crate::tunnel::Service> {
        match self {
            Kind::Tunnel(s) => Some(s),
            Kind::Desktop | Kind::Watch => None,
        }
    }

    /// Whether sessions of this kind stay out of the interface's list.
    fn hidden(self) -> bool {
        self == Kind::Watch
    }
}

/// One session as the interface sees it.
#[derive(Clone)]
struct Entry {
    id: u64,
    role: Role,
    /// What it carries.
    kind: Kind,
    device_id: String,
    state: State,
    failure: Option<Failure>,
    granted: Vec<Permission>,
    peer_version: String,
    peer_source: String,
    /// What the other device said about itself after the login.
    peer: About,
    /// What the other device announced in its hello (`feature`).
    features: Peer,
    stop: Arc<Notify>,
    /// Host side: notified when a resumed connection replaces this session,
    /// which then ends at once (`Sessions::supersede`).
    cut: Arc<Notify>,
    /// Controller side: media for the GUI, and the GUI's control messages.
    media: Arc<media::Queue>,
    control_tx: mpsc::Sender<Value>,
    control_rx: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
    /// Controller side: the viewer's latest choices, for a resumed session.
    choices: Arc<Mutex<Choices>>,
    /// Controller side: the gate its input passes (`InputGate`).
    input: Arc<InputGate>,
    /// Controller side: what it saw of its route (`RouteMeter`).
    meter: Arc<RouteMeter>,
    /// Controller side: what to do with the password once the host answers.
    remember: Remember,
    /// Controller side: the address dialed, or empty over the network.
    address: String,
    /// Removed from the interface's list (Home's Remove): left out of the
    /// status, and dropped once it ends.
    removed: bool,
    /// Controller side of a tunnel: the GUI's ends, and the session's,
    /// which it takes once connected.
    tunnel: Option<crate::tunnel::Ends>,
    wire: Arc<Mutex<Option<crate::tunnel::Wire>>>,
    /// Unix seconds when it connected (a host's watchers).
    since: u64,
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

/// A controller session's input on its way to the host
/// (doc/en-us/computer-use.md, "Input"): input is a stream, so the GUI's
/// writes never wait. While the session cannot take input (reconnecting,
/// or no word from a host that sends video for STALL) presses, clicks and
/// text are refused as behind and dropped, never delivered late; releases
/// are never refused: the gate keeps what the session holds on the host,
/// and what it could not deliver is reconciled once the path moves.
#[derive(Default)]
pub struct InputGate {
    inner: Mutex<Gate>,
}

impl Gate {
    /// Whether the session takes input now: a connection runs, and a host
    /// that sends video said something within STALL.
    fn takes(&self) -> bool {
        self.open && self.heard.is_none_or(|t| t.elapsed() <= STALL)
    }
}

/// The gate's state (`InputGate`).
#[derive(Default)]
struct Gate {
    /// What this session made the host hold, as sent.
    held: crate::input::Held,
    /// A connection runs (`run`).
    open: bool,
    /// When the host last said anything, once it sends video (its stats):
    /// silent for STALL, the path is stalled. Checked as input comes, so a
    /// session busy writing to a stalled path refuses input too.
    heard: Option<Instant>,
    /// Releases that could not be queued: the host is told what is held
    /// (or sent them again) once the path moves.
    owed: bool,
    lost: Vec<crate::input::Event>,
    /// When a press was last refused because the session's queue was full.
    refused: Option<Instant>,
    /// Connections that ran: a later one is a resume.
    runs: u32,
}

/// What the GUI is told: input is paused while the session cannot take it,
/// and for PAUSE_LINGER after a press found its queue full.
const PAUSE_LINGER: Duration = Duration::from_millis(500);

/// How long a controller whose host sends video waits for any word from it
/// before it takes the path for stalled and refuses input.
const STALL: Duration = Duration::from_millis(1500);

/// How often a controller session looks at its input gate.
const INPUT_TICK: Duration = Duration::from_millis(100);

impl InputGate {
    /// Passes one input event from the GUI on, or refuses it.
    /// Inputs: `send` puts it into the session's queue without waiting
    /// (false: the queue is full).
    /// Returns: 0 when it was queued (a release always counts as handled);
    /// -3 when it was refused as behind.
    pub fn write(&self, e: &crate::input::Event, send: impl FnOnce() -> bool) -> i32 {
        use crate::input::Hold;
        let Ok(mut g) = self.inner.lock() else {
            return -2;
        };
        let takes = g.takes();
        match e.hold() {
            Hold::Release => {
                g.held.follow(e);
                if !(g.open && send()) {
                    g.owed = true;
                    g.lost.push(e.clone());
                }
                0
            }
            _ if !takes => -3,
            hold => {
                if !send() {
                    g.refused = Some(Instant::now());
                    return -3;
                }
                if hold == Hold::Press {
                    g.held.follow(e);
                }
                0
            }
        }
    }

    /// A connection starts: input flows again; after a resume the host
    /// released what the last connection held, and is told so again.
    fn start(&self) {
        if let Ok(mut g) = self.inner.lock() {
            g.open = true;
            g.heard = None;
            g.runs += 1;
            if g.runs > 1 {
                g.held = Default::default();
                g.owed = true;
            }
        }
    }

    /// The connection ended: input is refused until the next.
    fn stop(&self) {
        if let Ok(mut g) = self.inner.lock() {
            g.open = false;
        }
    }

    /// The host said something at `at` (it sends video).
    fn heard(&self, at: Instant) {
        if let Ok(mut g) = self.inner.lock() {
            g.heard = Some(at);
        }
    }

    /// Whether the GUI should say input is paused now.
    fn paused(&self) -> bool {
        self.inner
            .lock()
            .is_ok_and(|g| !g.takes() || g.refused.is_some_and(|t| t.elapsed() < PAUSE_LINGER))
    }

    /// What reconciles the host with this session once its queue is empty
    /// and the path moves: to a host that announced `input-sync` one `sync`
    /// with what is held, to an older host the releases that were lost
    /// (those pressed again since left out).
    /// Returns: the input events to send, none when nothing is owed.
    fn reconcile(&self, sync: bool) -> Vec<Value> {
        let Ok(mut g) = self.inner.lock() else {
            return Vec::new();
        };
        if !g.owed || !g.takes() {
            return Vec::new();
        }
        g.owed = false;
        let lost = std::mem::take(&mut g.lost);
        if sync {
            return vec![json!(crate::input::Sync {
                held: g.held.clone()
            })];
        }
        let mut released = crate::input::Held::default();
        let mut at = (0.0, 0.0);
        for e in &lost {
            if let crate::input::Event::Pointer { x, y, .. } = e {
                at = (*x, *y);
            }
            released.add(e);
        }
        released
            .releases_beyond(&g.held, at)
            .iter()
            .filter_map(|e| serde_json::to_value(e).ok())
            .collect()
    }
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
            kind: Kind::Desktop,
            device_id: device_id.to_owned(),
            state: State::Connecting,
            failure: None,
            granted: Vec::new(),
            peer_version: String::new(),
            peer_source: String::new(),
            peer: About::default(),
            features: Peer::default(),
            stop: stop.clone(),
            cut: Arc::default(),
            media: Arc::default(),
            control_tx,
            control_rx: Arc::new(tokio::sync::Mutex::new(control_rx)),
            choices: Arc::default(),
            input: Arc::default(),
            meter: Arc::default(),
            remember: Remember::default(),
            address: String::new(),
            removed: false,
            tunnel: None,
            wire: Arc::default(),
            since: 0,
        });
        (id, stop)
    }

    /// Takes a finished session off the books (a status watch, which the
    /// interface never lists).
    /// Returns: how it ended, or None while it is live or when unknown.
    pub fn forget(&self, id: u64) -> Option<Option<Failure>> {
        let mut s = self.inner.lock().ok()?;
        let i =
            s.1.iter()
                .position(|e| e.id == id && matches!(e.state, State::Closed | State::Failed))?;
        Some(s.1.remove(i).failure)
    }

    /// The devices watching this host's status, for the status.
    pub fn watchers(&self) -> Value {
        let s = self.inner.lock().expect("sessions lock");
        s.1.iter()
            .filter(|e| {
                e.role == Role::Host && e.kind == Kind::Watch && e.state == State::Connected
            })
            .map(|e| json!({"device_id": e.device_id, "about": e.peer, "since": e.since}))
            .collect()
    }

    /// A controller tunnel's ends for the GUI.
    pub fn tunnel(&self, id: u64) -> Option<crate::tunnel::Ends> {
        let s = self.inner.lock().ok()?;
        s.1.iter()
            .find(|e| e.id == id && e.role == Role::Controller)?
            .tunnel
            .clone()
    }

    /// Takes a controller tunnel's session ends, once.
    fn take_wire(&self, id: u64) -> Option<crate::tunnel::Wire> {
        let s = self.inner.lock().ok()?;
        let e = s.1.iter().find(|e| e.id == id)?;
        e.wire.lock().ok()?.take()
    }

    /// How many host sessions of the kinds `of` picks are connected.
    fn connected(&self, of: fn(Kind) -> bool) -> usize {
        self.inner.lock().map_or(usize::MAX, |s| {
            s.1.iter()
                .filter(|e| e.role == Role::Host && e.state == State::Connected && of(e.kind))
                .count()
        })
    }

    fn update(&self, id: u64, f: impl FnOnce(&mut Entry)) {
        let mut s = self.inner.lock().expect("sessions lock");
        if let Some(e) = s.1.iter_mut().find(|e| e.id == id) {
            f(e);
        }
        s.1.retain(|e| !(e.removed && matches!(e.state, State::Closed | State::Failed)));
        let finished =
            s.1.iter()
                .filter(|e| !e.kind.hidden() && matches!(e.state, State::Closed | State::Failed))
                .count();
        if finished > KEEP_CLOSED {
            let drop_first = s
                .1
                .iter()
                .position(|e| !e.kind.hidden() && matches!(e.state, State::Closed | State::Failed));
            if let Some(i) = drop_first {
                s.1.remove(i);
            }
        }
    }

    /// Waits until a session is connected or has ended, at most `wait`
    /// (`tunnel.wait`; called outside any runtime).
    /// Returns: `{state, failure}` as it then is, `connecting` or
    /// `confirming` when the wait ran out.
    pub fn wait(&self, id: u64, wait: Duration) -> Value {
        let until = Instant::now() + wait;
        loop {
            let now = self.inner.lock().ok().and_then(|s| {
                s.1.iter()
                    .find(|e| e.id == id)
                    .map(|e| (e.state, e.failure))
            });
            match now {
                None => return json!({"state": State::Closed}),
                Some((state, failure))
                    if !matches!(state, State::Connecting | State::Confirming)
                        || Instant::now() >= until =>
                {
                    return json!({"state": state, "failure": failure});
                }
                _ => std::thread::sleep(Duration::from_millis(50)),
            }
        }
    }

    fn end(&self, id: u64, failure: Option<Failure>) {
        self.update(id, |e| {
            e.media.close();
            // A tunnel that never connected lets go of its GUI's reads.
            if let Ok(mut w) = e.wire.lock() {
                w.take();
            }
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

    /// A controller session's control sender and the gate its input passes,
    /// for the GUI's input.
    pub fn input(&self, id: u64) -> Option<(mpsc::Sender<Value>, Arc<InputGate>)> {
        let s = self.inner.lock().ok()?;
        let e =
            s.1.iter()
                .find(|e| e.id == id && e.role == Role::Controller)?;
        Some((e.control_tx.clone(), e.input.clone()))
    }

    fn side(&self, id: u64) -> Option<Side> {
        let s = self.inner.lock().ok()?;
        let e = s.1.iter().find(|e| e.id == id)?;
        Some(Side::Controller {
            queue: e.media.clone(),
            control: e.control_rx.clone(),
            choices: e.choices.clone(),
            input: e.input.clone(),
            meter: e.meter.clone(),
            host: e.features.clone(),
        })
    }

    /// How many host desktop sessions are connected (tunnels have a limit
    /// of their own).
    fn connected_hosts(&self) -> usize {
        self.connected(|k| k == Kind::Desktop)
    }

    /// Ends a live host session that a resumed connection replaces, at once
    /// and without a goodbye: its connection is gone, though this host has
    /// not noticed yet.
    fn supersede(&self, id: u64) {
        if let Ok(s) = self.inner.lock()
            && let Some(e) = s.1.iter().find(|e| {
                e.id == id
                    && e.role == Role::Host
                    && !matches!(e.state, State::Closed | State::Failed)
            })
        {
            e.cut.notify_one();
        }
    }

    /// What tells a host session that a resumed connection replaced it
    /// (`supersede`).
    fn cut(&self, id: u64) -> Arc<Notify> {
        self.inner
            .lock()
            .ok()
            .and_then(|s| s.1.iter().find(|e| e.id == id).map(|e| e.cut.clone()))
            .unwrap_or_default()
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

    /// Ends this device's controller sessions (a GUI that quit or went
    /// away from the helper: nobody looks at them any more).
    pub fn close_controllers(&self) {
        let s = self.inner.lock().expect("sessions lock");
        for e in s.1.iter().filter(|e| e.role == Role::Controller) {
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
            .filter(|e| !e.removed && !e.kind.hidden())
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
                    "service": e.kind.service(),
                    // A controller's route: an address, or empty for the network.
                    "route": (e.role == Role::Controller).then_some(&e.address),
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
    /// The hosts this device watches the status of (controller side).
    pub watching: Arc<crate::watch::Watching>,
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

    /// Lets what was written reach the peer before the connection closes:
    /// EasyTier's data plane drops what it has not sent when its socket is
    /// dropped, and frames are never flushed as they are written. Waits
    /// until the peer closed its end (it does once it read a refusal), or
    /// REFUSAL_WAIT passed.
    async fn linger(&mut self) {
        let _ = tokio::time::timeout(REFUSAL_WAIT, async {
            let _ = self.stream.flush().await;
            let mut sink = [0u8; 512];
            while matches!(self.stream.read(&mut sink).await, Ok(1..)) {}
        })
        .await;
    }

    async fn recv<T: DeserializeOwned>(&mut self) -> Result<T, Failure> {
        let frame = read_frame(&mut self.stream).await?;
        let n = self.noise.read_message(&frame, &mut self.buf)?;
        serde_json::from_slice(&self.buf[..n]).map_err(|_| Failure::Broken)
    }
}

pub(crate) async fn read_frame<R: AsyncRead + Unpin>(r: &mut R) -> Result<Vec<u8>, Failure> {
    let len = r.read_u16().await? as usize;
    let mut frame = vec![0; len];
    r.read_exact(&mut frame).await?;
    Ok(frame)
}

pub(crate) async fn write_frame<W: AsyncWrite + Unpin>(
    w: &mut W,
    frame: &[u8],
) -> Result<(), Failure> {
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
    from: Source,
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
            features: our_features(),
        };
        let payload = serde_json::to_vec(&hello).map_err(|_| Failure::Broken)?;
        let n = hs.write_message(&payload, &mut buf)?;
        step(write_frame(&mut stream, &buf[..n])).await?;
        // What a key login signs: the handshake as it stands after frame 2,
        // which the controller reads right after reading it.
        let h2 = hs.get_handshake_hash().to_vec();
        // 3: -> s, se + client_hello
        let frame = step(read_frame(&mut stream)).await?;
        let n = hs.read_message(&frame, &mut buf)?;
        let client: ClientHello = serde_json::from_slice(&buf[..n]).map_err(|_| Failure::Broken)?;
        if !speaks(client.protocol) {
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
        Ok::<_, Failure>((channel, client, client_key, context, h2))
    }
    .await;
    let Ok((mut ch, client, client_key, context, h2)) = result else {
        return;
    };
    let device_id = identity::device_id(&client_key);
    let (id, stop) = ctx.sessions.open(Role::Host, &device_id);
    let cut = ctx.sessions.cut(id);
    let kind = match (client.tunnel, client.watch) {
        (Some(s), _) => Kind::Tunnel(s),
        (None, true) => Kind::Watch,
        (None, false) => Kind::Desktop,
    };
    ctx.sessions.update(id, |e| {
        e.kind = kind;
        e.peer_version = client.version.clone();
        e.features = Peer {
            features: client.features.clone(),
        };
    });
    let login = host_login(
        &ctx,
        &mut ch,
        &client,
        &client_key,
        Handshake {
            context: &context,
            h2: &h2,
        },
        from,
        id,
        kind,
    )
    .await;
    drop(incoming);
    match login {
        // A tunnel carries the service's bytes until either side ends; the
        // host stays awake meanwhile.
        Ok(Admitted {
            granted,
            peer,
            service: Some(service),
        }) => {
            ctx.sessions.update(id, |e| {
                e.peer = peer;
                e.state = State::Connected;
                e.granted = granted;
            });
            let _awake = crate::power::keep_awake();
            let (from_service, to_service, reading) = crate::tunnel::bridge(service);
            let Channel { stream, noise, .. } = ch;
            let failure = crate::tunnel::relay(stream, noise, &stop, from_service, to_service)
                .await
                .err();
            reading.abort();
            ctx.sessions.end(id, failure);
        }
        // A watcher gets the host's status until either side ends; it
        // holds no capture and no awake request, and is never listed.
        Ok(Admitted { granted, peer, .. }) if kind == Kind::Watch => {
            let since = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map_or(0, |d| d.as_secs());
            ctx.sessions.update(id, |e| {
                e.peer = peer;
                e.state = State::Connected;
                e.granted = granted;
                e.since = since;
            });
            let Channel { stream, noise, .. } = ch;
            let failure = crate::watch::host(stream, noise, &stop, &ctx.access, &ctx.ai)
                .await
                .err();
            ctx.sessions.end(id, failure);
            ctx.sessions.forget(id);
        }
        Ok(Admitted { granted, peer, .. }) => {
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
            let failure = tokio::select! {
                r = run(
                    ch,
                    &stop,
                    Side::Host {
                        video,
                        sound,
                        input,
                        ai,
                        access: ctx.access.clone(),
                    },
                    clipboard,
                ) => r.err(),
                // A resumed connection took this session's place: its
                // connection is gone, so it ends at once, with no goodbye,
                // letting go of its capture and encoder.
                () = cut.notified() => None,
            };
            // The controller's AI task does not go on without it (as after
            // any drop, a resumed one too).
            ctx.ai.controller_left(&device_id);
            ctx.sessions.end(id, failure);
        }
        Err(f) => {
            // A refusal reaches the controller before the connection closes.
            if matches!(f, Failure::Refused(_)) {
                ch.linger().await;
            }
            ctx.sessions.end(id, Some(f));
            if kind.hidden() {
                ctx.sessions.forget(id);
            }
        }
    }
}

/// What a login binds to: the handshake hash after the last handshake
/// frame (OPAQUE's context) and after frame 2 (what a key login signs).
struct Handshake<'a> {
    context: &'a [u8],
    h2: &'a [u8],
}

/// What a controller proved before the host decides.
enum Proven {
    /// A password of this slot.
    Password(Slot),
    /// An authorized sign-in key.
    Key(crate::keys::Authorized),
}

/// A controller the host let in.
struct Admitted {
    granted: Vec<Permission>,
    /// What it said about itself.
    peer: About,
    /// A tunnel's connection to the host's own service.
    service: Option<tokio::net::TcpStream>,
}

/// The host's side of login and confirmation.
/// Returns: the controller as admitted, after telling it.
/// Notes: a correct password or an authorized key admits at once within
/// the host's password grants (or an unattended grant); anything more, and
/// a controller that asks without a password, waits for the host's user. A
/// tunnel asks for its service's permission alone, is admitted like any
/// permission, and only once its service answers; it is never resumed.
#[allow(clippy::too_many_arguments)]
async fn host_login<S: AsyncRead + AsyncWrite + Unpin>(
    ctx: &Ctx,
    ch: &mut Channel<S>,
    client: &ClientHello,
    client_key: &[u8; 32],
    hs: Handshake<'_>,
    from: Source,
    id: u64,
    kind: Kind,
) -> Result<Admitted, Failure> {
    let key_b64 = STANDARD.encode(client_key);
    let pinned = ctx.access.trusted().contains_key(&key_b64);
    let refuse = |reason| Verdict {
        accepted: false,
        granted: Vec::new(),
        reason: Some(reason),
        resume: None,
        about: None,
        via: None,
        key: None,
    };
    if let Err(reason) = ctx.access.admit(from, pinned) {
        let _ = ch.send(&refuse(reason)).await;
        return Err(Failure::Refused(reason));
    }
    if ctx.access.serve() == Serve::None {
        let _ = ch.send(&refuse(Refusal::Absent)).await;
        return Err(Failure::Refused(Refusal::Absent));
    }
    // Resuming a dropped session: same key, same permissions, no new login.
    // The session it resumes ends now if this host has not noticed the drop
    // yet: it would hold its capture and the hardware encoder until its idle
    // limit. A resume takes that session's place, so it is never busy.
    if let Some(token) = &client.resume
        && kind == Kind::Desktop
        && let Some((granted, old)) = ctx.access.take_resume(token, &key_b64)
    {
        ctx.sessions.supersede(old);
        let resume = ctx.access.issue_resume(&key_b64, &granted, id);
        step(ch.send(&Verdict {
            accepted: true,
            granted: granted.clone(),
            reason: None,
            resume: Some(resume),
            about: Some(ctx.about()),
            via: Some(Via::Resume),
            key: None,
        }))
        .await?;
        // A resumed controller sent no description: the last one stands.
        let peer = ctx
            .access
            .trusted()
            .remove(&key_b64)
            .map(|t| t.about)
            .unwrap_or_default();
        return Ok(Admitted {
            granted,
            peer,
            service: None,
        });
    }
    let busy = match kind {
        Kind::Desktop => ctx.sessions.connected_hosts() >= MAX_HOST_SESSIONS,
        Kind::Tunnel(_) => {
            ctx.sessions.connected(|k| matches!(k, Kind::Tunnel(_))) >= crate::tunnel::MAX_TUNNELS
        }
        Kind::Watch => ctx.sessions.connected(|k| k == Kind::Watch) >= crate::watch::MAX_WATCHERS,
    };
    if busy {
        let _ = ch.send(&refuse(Refusal::Busy)).await;
        return Err(Failure::Refused(Refusal::Busy));
    }
    let host_key = ctx.identity.public_key();
    // A sign-in key admits before any password; an authorized key whose
    // proof fails is a failed login, keys this host does not know are
    // ignored.
    let by_key = match crate::keys::verify(
        ctx.access.secrets(),
        &client.keys,
        hs.h2,
        &host_key,
        client_key,
    ) {
        crate::keys::Verified::Key(k) => Some(k),
        crate::keys::Verified::None => None,
        crate::keys::Verified::Bad => {
            ctx.access.failed(from);
            let _ = ch.send(&refuse(Refusal::Password)).await;
            return Err(Failure::Refused(Refusal::Password));
        }
    };
    // What the controller proved (None when it only asks), and its
    // description: with a key or without a password it came in the hello.
    let (proven, about) = match (by_key, &client.requests) {
        (Some(k), _) => (Some(Proven::Key(k)), client.about.clone()),
        (None, Some(requests)) => {
            let (responses, half) = ctx
                .access
                .slots
                .lock()
                .map_err(|_| Failure::Broken)?
                .respond(requests, &host_key, hs.context)
                .map_err(|_| Failure::Broken)?;
            step(ch.send(&Challenge { responses })).await?;
            let login: Login = step(ch.recv()).await?;
            let opened = match (login.slot, login.finalization) {
                (Some(slot), Some(fin)) if half.finish(slot, &fin, &host_key, hs.context) => {
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
            (Some(Proven::Password(slot)), login.about)
        }
        (None, None) => (None, client.about.clone()),
    };
    let peer = about.unwrap_or_default().clean();
    let device_id = identity::device_id(client_key);
    // A tunnel asks for its service's permission and a watcher for
    // `see-status`, whatever they named.
    let mut want = match kind {
        Kind::Tunnel(s) => vec![s.permission()],
        Kind::Watch => vec![Permission::SeeStatus],
        Kind::Desktop => client.want.clone(),
    };
    want.sort();
    want.dedup();
    // Nobody at the computer (Background service): the permanent password
    // or a key admits while Connect without asking is on.
    let lasting = matches!(
        proven,
        Some(Proven::Password(Slot::Permanent) | Proven::Key(_))
    );
    let unattended = (lasting && ctx.access.serve() != Serve::All)
        .then(|| ctx.access.unattended(&key_b64, &device_id, &want))
        .flatten();
    if unattended.is_none() && ctx.access.serve() != Serve::All {
        let _ = ch.send(&refuse(Refusal::Absent)).await;
        return Err(Failure::Refused(Refusal::Absent));
    }
    let (via, admitting_key) = match &proven {
        Some(Proven::Key(k)) => (Via::Key, ed25519_b64(&k.public)),
        Some(Proven::Password(_)) => (Via::Password, None),
        None => (Via::Confirmation, None),
    };
    // A correct password or key within what the host lets it grant.
    let by_proof = (proven.is_some() && ctx.access.admits_by_password(&key_b64, &device_id, &want))
        .then(|| want.clone());
    let granted = match unattended.or(by_proof) {
        Some(granted) => granted,
        // A watcher never asks the host's user.
        None if kind == Kind::Watch => {
            let _ = ch.send(&refuse(Refusal::Denied)).await;
            return Err(Failure::Refused(Refusal::Denied));
        }
        None => {
            ctx.sessions.update(id, |e| e.state = State::Confirming);
            let key_name = match &proven {
                Some(Proven::Key(k)) => Some(k.name.as_str()),
                _ => None,
            };
            let (ask_id, decision) = match ctx.access.ask(
                &key_b64,
                &device_id,
                want,
                &peer,
                proven.is_some(),
                key_name,
                kind.service(),
            ) {
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
            match answer {
                Ok(granted) => granted,
                Err(reason) => {
                    ctx.access.withdraw(ask_id);
                    // Asking without a password counts like a failed login, so
                    // requests cannot be repeated without end.
                    if proven.is_none() {
                        ctx.access.failed(from);
                    }
                    let _ = ch.send(&refuse(reason)).await;
                    return Err(Failure::Refused(reason));
                }
            }
        }
    };
    // A tunnel goes ahead only with its permission and once its service
    // answers.
    let service = match kind {
        Kind::Tunnel(s) => {
            if !granted.contains(&s.permission()) {
                let _ = ch.send(&refuse(Refusal::Denied)).await;
                return Err(Failure::Refused(Refusal::Denied));
            }
            match crate::tunnel::reach(ctx.access.service_ports().port(s)).await {
                Some(stream) => Some(stream),
                None => {
                    let _ = ch.send(&refuse(Refusal::ServiceDown)).await;
                    return Err(Failure::Refused(Refusal::ServiceDown));
                }
            }
        }
        Kind::Desktop | Kind::Watch => None,
    };
    // A watcher leaves no trace among the trusted devices.
    if kind != Kind::Watch {
        let _ = ctx.access.trust(&key_b64, &device_id, &granted, &peer);
    }
    let resume = (kind == Kind::Desktop).then(|| ctx.access.issue_resume(&key_b64, &granted, id));
    step(ch.send(&Verdict {
        accepted: true,
        granted: granted.clone(),
        reason: None,
        resume,
        about: Some(ctx.about()),
        via: Some(via),
        key: admitting_key,
    }))
    .await?;
    Ok(Admitted {
        granted,
        peer,
        service,
    })
}

/// The base64 Ed25519 key of an authorized key's line, as a proof names it.
fn ed25519_b64(line: &str) -> Option<String> {
    let b64 = line.split_whitespace().nth(1)?;
    let blob = STANDARD.decode(b64).ok()?;
    // string "ssh-ed25519" (4 + 11 bytes), then string key (4 + 32 bytes).
    (blob.len() == 51).then(|| STANDARD.encode(&blob[19..]))
}

/// How a controller's attempt over one connection ended.
pub enum Outcome {
    /// The device on this route has another key than the dialed Device ID
    /// derives (a squatter): try the next route.
    WrongDevice,
    /// Finished; the session's entry records how.
    Done,
    /// The connection dropped (or a resume attempt could not get through):
    /// dial again and resume with this token. `connected`: the session was
    /// connected over this connection before it dropped (a resume that went
    /// through starts a new resume window).
    Lost {
        token: zeroize::Zeroizing<String>,
        connected: bool,
    },
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
        // What a key login signs: the handshake as it stands after frame 2.
        let h2 = hs.get_handshake_hash().to_vec();
        let host: HostHello = serde_json::from_slice(&buf[..n]).map_err(|_| Failure::Broken)?;
        if !speaks(host.protocol) {
            // Not yet authenticated: kept only when it reads as a version,
            // so the controller can say which version the host runs.
            if is_version(&host.version) {
                ctx.sessions
                    .update(id, |e| e.peer_version = host.version.clone());
            }
            return Err(Failure::Incompatible);
        }
        let host_key = verify_static(&host.device_key, &host.signature, hs.get_remote_static())?;
        let peer = Peer {
            features: host.features.clone(),
        };
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
        let mut kind = Kind::Desktop;
        ctx.sessions.update(id, |e| {
            e.peer_version = host.version.clone();
            e.peer_source = host.source.clone();
            e.features = peer.clone();
            kind = e.kind;
        });
        // A host that announced no tunnels never gets asked for one, nor
        // for a status watch without `status`.
        let offered = match kind {
            Kind::Tunnel(_) => peer.has(feature::TUNNEL),
            Kind::Watch => peer.has(feature::STATUS),
            Kind::Desktop => true,
        };
        if !offered {
            return Err(Failure::Unsupported);
        }
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
        // Sign-in keys, only to a host that takes them and never on a
        // resume: the one this host admitted last, else every Ed25519 key.
        let keys = if peer.has(feature::KEY_LOGIN) && resume.is_none() {
            crate::keys::proofs(
                ctx.access.secrets(),
                ctx.access.saved(&device_id).key.as_deref(),
                &h2,
                &host_key,
                &ctx.identity.public_key(),
            )
        } else {
            Vec::new()
        };
        let hello = ClientHello {
            protocol: PROTOCOL,
            version: ctx.version.clone(),
            device_key: STANDARD.encode(ctx.identity.public_key()),
            signature: sign_static(&ctx.identity, &my_static),
            want: match kind {
                Kind::Tunnel(s) => vec![s.permission()],
                Kind::Watch => vec![Permission::SeeStatus],
                Kind::Desktop => crate::access::wire_want(&want, &peer),
            },
            // Its description goes with a key or without a password, as
            // the host may admit or ask before any login.
            about: (half.is_none() || !keys.is_empty()).then(|| ctx.about()),
            requests,
            resume: resume.as_ref().map(|t| t.to_string()),
            features: our_features(),
            keys,
            tunnel: kind.service(),
            watch: kind == Kind::Watch,
        };
        let offered_keys = !hello.keys.is_empty();
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
        // without a password, or with a key the host may ask its user
        // about, the verdict after the host's user decided.
        let first: Value = match &half {
            Some(_) if !offered_keys => step(ch.recv()).await?,
            Some(_) => tokio::time::timeout(CONFIRM_TIMEOUT + STEP_TIMEOUT, ch.recv())
                .await
                .unwrap_or(Err(Failure::Broken))?,
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
        // A status watch is no connection of the person's: it leaves when
        // the host was last connected to as it was, and notes only what the
        // host announced.
        let _ = match kind {
            Kind::Watch => ctx.access.note_features(&device_id, &host.features),
            _ => ctx.access.pin_host(
                &device_id,
                &host.device_key,
                &peer,
                &address,
                &host.features,
            ),
        };
        // The key the host admitted is the one offered next time; a host
        // that admitted another way no longer takes the one remembered.
        match verdict.via {
            Some(Via::Key) => {
                let id = verdict
                    .key
                    .as_deref()
                    .and_then(|k| crate::keys::login_id(ctx.access.secrets(), k));
                let _ = ctx.access.remember_key(&device_id, id.as_deref());
            }
            Some(Via::Password | Via::Confirmation) => {
                let _ = ctx.access.remember_key(&device_id, None);
            }
            Some(Via::Resume | Via::Unknown) | None => {}
        }
        // A password is kept only once it admitted (an older host does not
        // say how; a password it accepted did).
        let mut remember = Remember::default();
        ctx.sessions.update(id, |e| remember = e.remember);
        if remember.save
            && !password.is_empty()
            && matches!(verdict.via, None | Some(Via::Password))
        {
            let _ = ctx.access.save_password(&device_id, &password);
        }
        ctx.sessions.update(id, |e| e.peer = peer);
        Ok((ch, verdict.granted, verdict.resume, kind))
    };
    // A session closed here while it signs in or waits for the host's
    // answer ends at once (regression: a close took effect only once the
    // session was connected, so a connection whose tab had closed came
    // through, showed connected for a moment and got a new tab of its own,
    // which said the session ended).
    let result = tokio::select! {
        biased;
        _ = stop.notified() => {
            ctx.sessions.end(id, None);
            return Outcome::Done;
        }
        r = result => r,
    };
    match result {
        // A tunnel carries the GUI's bytes until either side ends; it is
        // never resumed.
        Ok((ch, granted, _, Kind::Tunnel(_))) => {
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let result = match ctx.sessions.take_wire(id) {
                Some(wire) => {
                    let Channel { stream, noise, .. } = ch;
                    crate::tunnel::relay(stream, noise, &stop, wire.up, wire.down).await
                }
                None => Err(Failure::Broken),
            };
            ctx.sessions.end(id, result.err());
            Outcome::Done
        }
        // A status watch hands the host's samples to the watch manager, and
        // the lease's pace to the host.
        Ok((ch, granted, _, Kind::Watch)) => {
            ctx.sessions.update(id, |e| {
                e.state = State::Connected;
                e.granted = granted;
            });
            let Channel { stream, noise, .. } = ch;
            let pace = ctx.watching.pace(&device_id);
            let result = crate::watch::follow(stream, noise, &stop, pace, |sample| {
                ctx.watching.sampled(&device_id, sample)
            })
            .await;
            ctx.sessions.end(id, result.err());
            Outcome::Done
        }
        Ok((ch, granted, token, Kind::Desktop)) => {
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
                    Outcome::Lost {
                        token: zeroize::Zeroizing::new(token),
                        connected: true,
                    }
                }
                (result, _) => {
                    ctx.sessions.end(id, result.err());
                    Outcome::Done
                }
            }
        }
        Err(Failure::WrongDevice) => Outcome::WrongDevice,
        // A resume that could not get through keeps its (unused) token.
        Err(Failure::Broken) if resume.is_some() => Outcome::Lost {
            token: resume.expect("checked"),
            connected: false,
        },
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

/// `begin_as` for a desktop session, as the tests start one.
#[cfg(test)]
pub fn begin(ctx: &Ctx, device_id: &str, remember: Remember) -> (u64, Arc<Notify>) {
    begin_as(ctx, device_id, remember, Kind::Desktop)
}

/// Registers an outgoing session before its connection is opened, so the
/// interface sees it at once; a tunnel's gets its pipes.
pub fn begin_as(ctx: &Ctx, device_id: &str, remember: Remember, kind: Kind) -> (u64, Arc<Notify>) {
    let (id, stop) = ctx.sessions.open(Role::Controller, device_id);
    let pipes = matches!(kind, Kind::Tunnel(_)).then(crate::tunnel::pipe);
    ctx.sessions.update(id, |e| {
        e.remember = remember;
        e.kind = kind;
        if let Some((ends, wire)) = pipes {
            e.tunnel = Some(ends);
            e.wire = Arc::new(Mutex::new(Some(wire)));
        }
    });
    (id, stop)
}

/// Records the route an outgoing session's current connection took: an
/// address (which the known host keeps for the next one), or empty for the
/// Kyokai network.
pub fn dials_address(ctx: &Ctx, id: u64, address: &str) {
    ctx.sessions.update(id, |e| e.address = address.to_owned());
}

/// What a controller session saw of its route (doc/en-us/networking.md,
/// "Routes"): the latest round trip of its delay probes and the bitrates
/// the host's quality control settled on, which Auto remembers per route.
#[derive(Default)]
pub struct RouteMeter {
    delay_ms: AtomicU32,
    kbps_sum: AtomicU64,
    samples: AtomicU32,
}

impl RouteMeter {
    fn delay(&self, ms: u32) {
        self.delay_ms.store(ms.max(1), Ordering::Relaxed);
    }

    fn bitrate(&self, kbps: u32) {
        self.kbps_sum.fetch_add(u64::from(kbps), Ordering::Relaxed);
        self.samples.fetch_add(1, Ordering::Relaxed);
    }

    /// The round trip and the average bitrate since the last take, and
    /// starts over; None when the session saw neither.
    fn take(&self) -> Option<(u32, u32)> {
        let delay = self.delay_ms.swap(0, Ordering::Relaxed);
        let sum = self.kbps_sum.swap(0, Ordering::Relaxed);
        let n = self.samples.swap(0, Ordering::Relaxed);
        let kbps = if n == 0 {
            0
        } else {
            (sum / u64::from(n)) as u32
        };
        (delay > 0 || kbps > 0).then_some((delay, kbps))
    }
}

/// What a controller session saw of the route its last connection took
/// (`RouteMeter::take`), or None.
pub fn route_seen(ctx: &Ctx, id: u64) -> Option<(u32, u32)> {
    let s = ctx.sessions.inner.lock().ok()?;
    s.1.iter().find(|e| e.id == id)?.meter.take()
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
    if !speaks(host.protocol) {
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
        // Read by the input service of Windows and macOS.
        #[cfg_attr(not(any(windows, target_os = "macos")), allow(dead_code))]
        input: bool,
        ai: HostAi,
        /// The host's access state: while a controller waits for the host
        /// user, nobody's input reaches Kyokai's windows (`kyokai_reachable`).
        #[cfg_attr(not(any(windows, target_os = "macos")), allow(dead_code))]
        access: Arc<Access>,
    },
    /// The controller: puts media into the queue its GUI drains, and sends
    /// the GUI's control messages (input, key-frame requests) to the host;
    /// `choices` are the viewer's latest quality and codec, `input` the
    /// gate its input passes (`InputGate`), `host` what the host announced.
    Controller {
        queue: Arc<media::Queue>,
        control: Arc<tokio::sync::Mutex<mpsc::Receiver<Value>>>,
        choices: Arc<Mutex<Choices>>,
        input: Arc<InputGate>,
        host: Peer,
        meter: Arc<RouteMeter>,
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
                Steer::Unknown => Ok(()),
            },
            Message::AiDecide { id, approve } if self.approve => self.ai.decide(id, approve),
            Message::AiDraft { text } if self.run => self.ai.set_draft(&text),
            _ => Err(not_allowed()),
        }
    }
}

/// Keeps a viewer's setting (quality, codec, sound, the clipboard choice)
/// from a control message that found the session reconnecting, for the
/// connection that resumes it.
fn keep_choice(choices: &Mutex<Choices>, m: &Value) {
    let Ok(mut c) = choices.lock() else {
        return;
    };
    match m["type"].as_str() {
        Some("quality") => c.quality = Some(m.clone()),
        Some("codec") => c.codec = Some(m.clone()),
        Some("audio") => c.audio = Some(m.clone()),
        Some("clipboard-use") => {
            c.clipboard = ClipUse {
                share: m["share"].as_bool().unwrap_or(true),
                front: m["front"].as_bool().unwrap_or(true),
            }
        }
        _ => {}
    }
}

/// Encrypts one plaintext and writes it as a frame (the tests' peers; a
/// session sends through its writer, `Out`).
#[cfg(test)]
async fn seal<W: AsyncWrite + Unpin>(
    noise: &mut snow::TransportState,
    buf: &mut [u8],
    w: &mut W,
    plain: &[u8],
) -> Result<(), Failure> {
    let n = noise.write_message(plain, buf)?;
    step(write_frame(w, &buf[..n])).await
}

/// What a session's writer sends next: a plaintext, or word once the
/// plaintexts before it were written.
enum Outgoing {
    Plain(Vec<u8>),
    Flushed(tokio::sync::oneshot::Sender<()>),
}

/// A running session's way out (doc/en-us/protocol.md, "Session"): the
/// session hands its plaintexts to a writer of their own and never waits
/// on the network, so input, acknowledgements and the host's word go out
/// and come in whatever the video does. The writer takes what is urgent
/// first — everything but the video — and the video's chunks in between
/// (regression: the session wrote each frame whole before it read again;
/// on a slow path a frame took seconds, the host read no input meanwhile,
/// the controller's writes stalled behind it, and video and input held
/// each other up until the session broke).
struct Out {
    urgent: mpsc::UnboundedSender<Outgoing>,
    video: mpsc::UnboundedSender<Vec<u8>>,
    /// The video and file plaintexts waiting for the writer, and word as
    /// they go (`bulk_room`).
    queued: Arc<AtomicUsize>,
    room: Arc<Notify>,
}

/// How many video or file plaintexts may wait for the writer before a file
/// copy waits for room: a copy never fills memory, nor holds up the video.
const BULK_ROOM: usize = 8;

impl Out {
    /// Starts the writer on `w`, encrypting with the session's `noise`.
    /// Returns: the way out, and the writer's task (it ends with the first
    /// failed write, or once the way out is dropped).
    fn start<W: AsyncWrite + Unpin + Send + 'static>(
        mut w: W,
        noise: Arc<Mutex<snow::TransportState>>,
        mut buf: Vec<u8>,
    ) -> (Out, tokio::task::JoinHandle<()>) {
        let (urgent, mut urgent_rx) = mpsc::unbounded_channel();
        let (video, mut video_rx) = mpsc::unbounded_channel::<Vec<u8>>();
        let queued = Arc::new(AtomicUsize::new(0));
        let room = Arc::new(Notify::new());
        let (left, freed) = (queued.clone(), room.clone());
        let task = tokio::spawn(async move {
            loop {
                let next = tokio::select! {
                    biased;
                    Some(o) = urgent_rx.recv() => o,
                    Some(v) = video_rx.recv() => {
                        left.fetch_sub(1, Ordering::Relaxed);
                        freed.notify_one();
                        Outgoing::Plain(v)
                    }
                    else => return,
                };
                match next {
                    Outgoing::Plain(plain) => {
                        let sealed = noise
                            .lock()
                            .map_err(|_| ())
                            .and_then(|mut n| n.write_message(&plain, &mut buf).map_err(|_| ()));
                        let Ok(n) = sealed else {
                            return;
                        };
                        if step(write_frame(&mut w, &buf[..n])).await.is_err() {
                            return;
                        }
                    }
                    Outgoing::Flushed(done) => {
                        let _ = w.flush().await;
                        let _ = done.send(());
                    }
                }
            }
        });
        (
            Out {
                urgent,
                video,
                queued,
                room,
            },
            task,
        )
    }

    /// Queues an urgent plaintext.
    /// Returns: Broken once the writer ended.
    fn plain(&self, plain: Vec<u8>) -> Result<(), Failure> {
        self.urgent
            .send(Outgoing::Plain(plain))
            .map_err(|_| Failure::Broken)
    }

    /// Queues an urgent message (in parts when long).
    fn json<T: Serialize>(&self, msg: &T) -> Result<(), Failure> {
        for part in plaintexts(msg)? {
            self.plain(part)?;
        }
        Ok(())
    }

    /// Queues one chunk of a video frame.
    fn chunk(&self, chunk: Vec<u8>) -> Result<(), Failure> {
        self.queued.fetch_add(1, Ordering::Relaxed);
        self.video.send(chunk).map_err(|_| Failure::Broken)
    }

    /// Waits until few video or file plaintexts wait for the writer
    /// (BULK_ROOM).
    async fn bulk_room(&self) {
        while self.queued.load(Ordering::Relaxed) >= BULK_ROOM {
            self.room.notified().await;
        }
    }

    /// Queues a message that keeps its place among the video's chunks: a
    /// stream's description goes after the old stream's frames and before
    /// the new one's.
    fn in_video<T: Serialize>(&self, msg: &T) -> Result<(), Failure> {
        for part in plaintexts(msg)? {
            self.chunk(part)?;
        }
        Ok(())
    }

    /// Waits until what is urgent so far was written and flushed.
    async fn flush(&self) {
        let (done, wait) = tokio::sync::oneshot::channel();
        if self.urgent.send(Outgoing::Flushed(done)).is_ok() {
            let _ = wait.await;
        }
    }
}

/// A message's plaintexts: itself, or its parts when it is longer than a
/// frame takes.
fn plaintexts<T: Serialize>(msg: &T) -> Result<Vec<Vec<u8>>, Failure> {
    let plain = serde_json::to_vec(msg).map_err(|_| Failure::Broken)?;
    if plain.len() <= MAX_PLAINTEXT {
        return Ok(vec![plain]);
    }
    split(&plain).ok_or(Failure::Broken)
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
    let Channel { stream, noise, buf } = ch;
    let (mut rd, wr) = tokio::io::split(stream);
    // Sent by a writer of its own (Out), received here: the Noise state is
    // shared, each side holding it briefly.
    let noise = Arc::new(Mutex::new(noise));
    let (out, writer) = Out::start(wr, noise.clone(), buf);
    let _writer = AbortOnDrop(writer);
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
    // Until when the host waits for the codecs: once, and no longer once
    // they came (regression: the elapsed wait ran again whenever a recovery
    // step dropped the video, which then opened on the primary display as
    // for a controller that names no codecs — none on a Mac).
    let mut codecs_due = wants_video.then(|| tokio::time::Instant::now() + CODECS_WAIT);
    // The host's input service; dropping it releases what it holds.
    #[cfg(any(windows, target_os = "macos"))]
    let mut injector = match side {
        Side::Host { input: true, .. } => Some(crate::input::Injector::default()),
        _ => None,
    };
    // The displays the controller may choose from, with the one shown.
    let mut displays = if wants_video {
        video::displays()
    } else {
        Vec::new()
    };
    // What this Mac lets the session do, for the log (the helper's agent
    // has grants of its own).
    #[cfg(target_os = "macos")]
    if matches!(side, Side::Host { .. }) && crate::agent::link().is_none() {
        let p = crate::mac::permissions(false);
        log::info!(
            "host session: screen recording {}, accessibility {}, input monitoring {}",
            p.screen,
            p.accessibility,
            p.input_monitoring
        );
    }
    // Input and AI commands queued while a session reconnected are dropped,
    // never delivered late (the host sends its current AI task instead);
    // the viewer's settings among them are kept, and sent below. Input
    // flows again.
    if let Side::Controller {
        control,
        choices,
        input,
        ..
    } = &side
    {
        let mut rx = control.lock().await;
        while let Ok(m) = rx.try_recv() {
            keep_choice(choices, &m);
        }
        input.start();
    }
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
                out.json(&m)?;
            }
        }
        let (decode, i444) = tokio::task::spawn_blocking(video::decodable)
            .await
            .unwrap_or_default();
        out.json(&Message::Codecs { decode, i444 })?;
    }
    let (queue, control, choices, gate) = match &side {
        Side::Controller {
            queue,
            control,
            choices,
            input,
            host,
            ..
        } => (
            Some(queue.clone()),
            Some(control.clone()),
            Some(choices.clone()),
            Some((input.clone(), host.has(feature::INPUT_SYNC))),
        ),
        Side::Host { .. } => (None, None, None, None),
    };
    // A host without the `wake` action is woken by a Shift press instead.
    let host_wakes = matches!(&side, Side::Controller { host, .. } if host.has(feature::WAKE));
    let host_recovers =
        matches!(&side, Side::Controller { host, .. } if host.has(feature::RECOVERY));
    let meter = match &side {
        Side::Controller { meter, .. } => Some(meter.clone()),
        Side::Host { .. } => None,
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
                out.json(&m)?;
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
    let mut pointer = wants_video.then(PointerWatch::start);
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
    // the transport's buffers on a slow path. Since when the frames in
    // flight have waited for an acknowledgement, and whether that wait was
    // logged as a slow path (ACK_SLOW).
    let mut acked: u32 = 0;
    let mut acked_at = Instant::now();
    let mut slow_logged = false;
    // The recovery of the host's picture (`recovery`): troubles in a row, since
    // when the stream has gone without trouble, since when a picture has
    // been due (the stream opened, a key frame was asked for) with no frame
    // sent, and when the last step was taken; when the video opens again
    // (or the placeholder tries the screen), the next time in the next
    // codec.
    let mut trouble: u32 = 0;
    let mut good_since = Instant::now();
    let mut due_since: Option<Instant> = None;
    let mut stepped_at: Option<Instant> = None;
    let mut reopen_at: Option<tokio::time::Instant> = None;
    let mut next_codec = false;
    // The display the viewer chose (None: the primary), kept while the
    // stream is closed or the placeholder shows.
    let mut want_display: Option<video::Display> = None;
    // The last key frame sent (its number and when), and the key frames the
    // controller asked for again a while after it had one: ones it could not
    // use.
    let mut key_sent: Option<(u32, Instant)> = None;
    let mut unused_keys: u32 = 0;
    // Whether the system reported the display off at the last look.
    let mut dark = false;
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
    // frames out, and its own check for that each second (`key_due`).
    let mut key_asked: Option<Instant> = None;
    let mut key_check = tokio::time::interval(KEY_AGAIN);
    // A controller's input gate, looked at every INPUT_TICK: it tells the
    // GUI whether input is paused and reconciles what the host holds once
    // the queue is empty; it hears of the host as frames come, once the
    // host's stats show that it sends video.
    let mut input_tick = tokio::time::interval(INPUT_TICK);
    let mut told_paused: Option<bool> = None;
    let mut host_sends_video = false;
    // The host's record of what this session's input holds on it, and
    // where the pointer was last, for a controller's `sync`.
    #[cfg(any(windows, target_os = "macos"))]
    let mut held = crate::input::Held::default();
    #[cfg(any(windows, target_os = "macos"))]
    let mut pointer_at = (0.5f32, 0.5f32);
    let mut heard = Instant::now();
    let mut plain = vec![0u8; MAX_FRAME];
    // Whether this session holds the block or privacy mode; checked every
    // second, since the host user can lift them.
    #[cfg_attr(not(windows), allow(unused_mut))]
    let mut holding = false;
    let mut block_check = tokio::time::interval(Duration::from_secs(1));
    #[cfg(windows)]
    if let Some(inj) = &injector {
        out.json(&offered(inj))?;
    }
    // Takes the next recovery step for a stream whose picture does not reach
    // the viewer (`recovery`), and says why in the log: the stream is dropped
    // and opened again (soon, or in the next codec), or the placeholder
    // shows while the screen is tried again.
    macro_rules! trouble {
        ($why:expr) => {{
            let why: String = $why;
            let mut next = recovery(trouble);
            trouble = trouble.saturating_add(1);
            if next == Recovery::NextCodec && chosen.len() <= 1 {
                next = recovery(trouble);
                trouble = trouble.saturating_add(1);
            }
            log::info!(
                "host session: the video so far: {frame_no} frames sent, {acked} acknowledged, a key frame sent since the last ask: {}, {unused_keys} key frames asked again, the display off: {dark}",
                key_sent.is_some()
            );
            let now = Instant::now();
            (good_since, due_since, stepped_at, unused_keys) = (now, None, Some(now), 0);
            match next {
                Recovery::Reopen(after) => {
                    log::info!("host session: {why}: opening the video again in {} s", after.as_secs());
                    source = None;
                    reopen_at = Some(tokio::time::Instant::now() + after);
                }
                Recovery::NextCodec => {
                    log::info!("host session: {why}: opening the video again in the next codec");
                    source = None;
                    next_codec = true;
                    reopen_at = Some(tokio::time::Instant::now());
                }
                Recovery::Placeholder(after) => {
                    log::info!("host session: {why}: the placeholder shows, the screen is tried again in {} s", after.as_secs());
                    if source.as_ref().is_none_or(|s| s.why.is_none()) {
                        let p = video::Source::pattern("retrying", &why);
                        announce(&out, &p, &displays)?;
                        acked = frame_no;
                        p.set_room(IN_FLIGHT);
                        source = Some(p);
                    }
                    reopen_at = Some(tokio::time::Instant::now() + after);
                }
            }
        }};
    }
    // Opens the video on a display (None: the primary) from the chosen
    // candidates (without the first after NextCodec), once the old capture
    // let go of its encoder (dropping it parks its thread, which
    // Source::open waits for), and describes it to the controller, with the
    // displays as they are now. Opening blocks for as long as a second: it
    // runs off the network's two threads. A source that is the placeholder
    // (it did not open, or panicked) is trouble for the recovery.
    macro_rules! reopen {
        ($display:expr) => {{
            let display = $display;
            want_display = display;
            source = None;
            let mut wanted = chosen.clone();
            if std::mem::take(&mut next_codec) && wanted.len() > 1 {
                wanted.remove(0);
            }
            let (s, list) = tokio::task::spawn_blocking(move || {
                (video::Source::open(display, &wanted), video::displays())
            })
            .await
            .unwrap_or_else(|_| {
                (
                    video::Source::pattern("retrying", "the capture failed"),
                    Vec::new(),
                )
            });
            if !list.is_empty() {
                displays = list;
            }
            #[cfg(any(windows, target_os = "macos"))]
            if let Some(inj) = injector.as_mut() {
                inj.set_area(s.display.map(area));
            }
            announce(&out, &s, &displays)?;
            base_kbps = rustdesk_qos::base_bitrate(s.start.width, s.start.height);
            if let Some(p) = pointer.as_mut() {
                p.show(s.display.map(area));
            }
            // The new stream starts with the window open: frames of the
            // old one still unacknowledged are not waited for.
            acked = frame_no;
            s.set_room(IN_FLIGHT);
            let why = s.why.clone();
            source = Some(s);
            pointer_sent = None;
            reopen_at = None;
            due_since = Some(Instant::now());
            key_sent = None;
            // No codec in common: nothing to try until the controller's
            // codecs or choice change (which open the video again).
            if let Some(why) = why
                && !chosen.is_empty()
            {
                trouble!(why);
            }
        }};
    }
    let outcome: Result<(), Failure> = async { loop {
        tokio::select! {
            _ = stop.notified() => {
                let _ = out.json(&Message::Bye);
                // The stream stays open until the peer read the goodbye and
                // closed its end (or BYE_WAIT passed): closed first, the
                // peer's next write would fail and it would take the
                // connection for lost and resume it.
                let _ = tokio::time::timeout(BYE_WAIT, async {
                    out.flush().await;
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
                    out.json(&Message::AiTask { task })?;
                    ai_sent = now;
                }
                // The shared draft, as anyone typed it last.
                let (serial, text) = h.ai.draft();
                if serial != draft_sent {
                    out.json(&Message::AiDraft { text })?;
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
                    // A file's pieces wait for the video's turn (BULK_ROOM).
                    Some(t) => {
                        out.bulk_room().await;
                        t.next().await
                    }
                    None => std::future::pending().await,
                }
            } => {
                use crate::files::Step;
                match step {
                    Step::Offer(id, files) => {
                        clip_history(&queue, "controller", Crossed::Files(&files));
                        out.json(&Message::ClipboardFiles { id, files })?;
                    }
                    Step::Piece(p) => out.chunk(p)?,
                    Step::Cancel(id) => out.json(&Message::FilesCancel { id })?,
                    Step::Stop(id) => {
                        receiving_files = None;
                        out.json(&Message::FilesStop { id })?;
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
                out.plain(sound_packet(&p))?;
            }
            () = async {
                match codecs_due {
                    Some(t) => tokio::time::sleep_until(t).await,
                    None => std::future::pending().await,
                }
            } => {
                // A controller that names no codecs gets H.264 (Windows).
                codecs_due = None;
                chosen = video::choose(&[], &[], false, false);
                reopen!(None);
            }
            Ok(()) = async {
                match pointer.as_mut() {
                    Some(p) => p.at.changed().await,
                    None => std::future::pending().await,
                }
            }, if source.is_some() => {
                let at = pointer.as_mut().and_then(|p| *p.at.borrow_and_update());
                if let Some((x, y)) = at {
                    // Shown while it is on the display shown, on every
                    // platform (whether the system draws its cursor is left
                    // out: Windows hides it with no mouse attached).
                    let visible = (0.0..=1.0).contains(&x) && (0.0..=1.0).contains(&y);
                    let now = (((x * 4096.0) as i32, (y * 4096.0) as i32), visible);
                    if pointer_sent != Some(now) {
                        pointer_sent = Some(now);
                        out.json(&Message::Cursor { x, y, visible })?;
                    }
                }
            }
            _ = block_check.tick(), if holding => {
                #[cfg(windows)]
                if let Some(inj) = &injector
                    && !inj.input_blocked()
                {
                    holding = false;
                    out.json(&offered(inj))?;
                }
            }
            _ = ping.tick() => {
                if heard.elapsed() > IDLE_LIMIT {
                    return Err(Failure::Broken);
                }
                out.json(&Message::Ping)?;
            }
            _ = input_tick.tick(), if gate.is_some() => {
                let (g, sync) = gate.as_ref().expect("checked");
                let paused = g.paused();
                if told_paused != Some(paused)
                    && let Some(q) = &queue
                {
                    told_paused = Some(paused);
                    let mut packet = vec![media::packet::INPUT];
                    packet.extend(serde_json::to_vec(&json!({"paused": paused})).map_err(|_| Failure::Broken)?);
                    q.push(packet);
                }
                // Only behind what the GUI queued before: a release that
                // could not be delivered must not overtake its press.
                if control.as_ref().is_some_and(|rx| rx.is_empty()) {
                    for event in g.reconcile(*sync) {
                        out.json(&Message::Input { event })?;
                    }
                }
            }
            // A controller whose GUI lost frames asks again each second,
            // also while no frame comes (a still screen sends none).
            tick = key_check.tick(), if queue.is_some() => {
                // By the tick's time, so the next tick is a whole KEY_AGAIN later.
                let now = tick.into_std();
                if let Some(q) = &queue
                    && key_due(q.waiting_for_key(), key_asked, now)
                {
                    key_asked = Some(now);
                    out.json(&Message::KeyFrame)?;
                }
            }
            _ = probe.tick(), if source.is_some() => {
                match probe_sent {
                    None => {
                        probe_sent = Some(Instant::now());
                        out.json(&Message::TestDelay { reply: false })?;
                    }
                    Some(t) => qos.user_delay_response_elapsed(QOS_VIEWER, t.elapsed().as_millis()),
                }
                qos.update_display_data(QOS_DISPLAY, sent_this_second);
                let encode_ms = (took_this_second / sent_this_second.max(1) as u64 / 1000) as u32;
                let frames = sent_this_second as u32;
                sent_this_second = 0;
                took_this_second = 0;
                let fps = boosted(qos.fps(), fps_cap, input_at, last_delay);
                let kbps = (base_kbps as f32 * qos.ratio()) as u32;
                last_kbps = kbps;
                qos.store_bitrate(kbps);
                if let Some(s) = &source {
                    s.set_targets(fps, kbps.saturating_mul(1000));
                }
                // Late acknowledgements are a slow path: the frames wait,
                // and the delay the probes measure lowers the bitrate.
                if frame_no != acked && acked_at.elapsed() >= ACK_SLOW && !slow_logged {
                    slow_logged = true;
                    log::info!("host session: the path is slow: {} frames wait for the controller for {} s or more ({} ms delay)", frame_no.wrapping_sub(acked), ACK_SLOW.as_secs(), last_delay);
                }
                // The picture's recovery, once a second: a display that came
                // back is tried at once from the placeholder; a stream that
                // sends nothing while a picture is due takes the next step;
                // one that went RECOVERED without trouble starts over.
                let off = crate::power::display_off();
                let came_on = dark && !off;
                dark = off;
                let placeholder = source.as_ref().is_some_and(|s| s.why.is_some());
                if placeholder && came_on && reopen_at.is_some() {
                    reopen_at = Some(tokio::time::Instant::now());
                } else if !placeholder
                    && reopen_at.is_none()
                    && due_since.is_some_and(|t| t.elapsed() >= NO_PICTURE)
                    // Frames that wait for room are held by the path, not
                    // missing from the capture.
                    && frame_no.wrapping_sub(acked) < IN_FLIGHT
                {
                    trouble!(format!("no frame for {} s although a picture was due", NO_PICTURE.as_secs()));
                } else if trouble > 0
                    && !placeholder
                    && due_since.is_none()
                    && good_since.elapsed() >= RECOVERED
                {
                    trouble = 0;
                    log::info!("host session: the picture is back");
                }
                let video = match () {
                    _ if source.as_ref().is_some_and(|s| s.why.is_some()) => "placeholder",
                    _ if off => "dark",
                    _ if trouble > 0 => "recovering",
                    _ => "live",
                };
                let stats = Message::Stats { delay_ms: last_delay, fps, bitrate: kbps.saturating_mul(1000), encode_ms, frames, video: video.into() };
                out.json(&stats)?;
            }
            () = async {
                match reopen_at {
                    Some(t) => tokio::time::sleep_until(t).await,
                    None => std::future::pending().await,
                }
            } => {
                reopen!(want_display);
            }
            f = async {
                match source.as_mut() {
                    Some(s) => s.frames.recv().await,
                    None => std::future::pending().await,
                }
            }, if frame_no.wrapping_sub(acked) < IN_FLIGHT => {
                let Some(f) = f else {
                    // The capture ended by itself (its encoder failed twice,
                    // its thread could not follow the input desktop, the
                    // helper's agent went): the next recovery step.
                    trouble!("the video ended".to_string());
                    continue;
                };
                if frame_no == acked {
                    acked_at = Instant::now();
                }
                frame_no = frame_no.wrapping_add(1);
                if let Some(s) = &source {
                    s.set_room(IN_FLIGHT.saturating_sub(frame_no.wrapping_sub(acked)));
                }
                due_since = None;
                if f.flags & media::KEY_FRAME != 0 {
                    key_sent = Some((frame_no, Instant::now()));
                }
                sent_this_second += 1;
                took_this_second += u64::from(f.took_us);
                for chunk in media::chunks(frame_no, f.flags, f.pts, &f.data, MAX_PLAINTEXT) {
                    out.chunk(chunk)?;
                    // Sound that came meanwhile goes between the chunks: a
                    // large picture does not hold it up.
                    // Only what waits now: more comes every 10 ms.
                    for _ in 0..sound.as_ref().map_or(0, |s| s.packets.len()) {
                        let Some(p) = sound.as_mut().and_then(|s| s.packets.try_recv().ok()) else {
                            break;
                        };
                        out.plain(sound_packet(&p))?;
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
                                out.json(&Message::FilesCancel { id })?;
                            }
                            if let Some(id) = received {
                                out.json(&Message::FilesStop { id })?;
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
                if let Ok(m @ (Message::KeyFrame | Message::Display { .. } | Message::Codecs { .. } | Message::Input { .. } | Message::Action { .. } | Message::Quality { .. } | Message::Codec { .. } | Message::Audio { .. } | Message::AiStart { .. } | Message::AiCommand { .. } | Message::AiDecide { .. } | Message::AiDraft { .. } | Message::NoPicture { .. })) = serde_json::from_value::<Message>(msg) {
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
                    // Only a host that recovers its picture hears that the
                    // viewer shows none (a 0.4.0 host would not parse it).
                    if matches!(m, Message::NoPicture { .. }) && !host_recovers {
                        continue;
                    }
                    if matches!(m, Message::Action { action: SessionAction::Wake }) && !host_wakes {
                        for down in [true, false] {
                            let shift = Message::Input { event: json!({"kind": "scan", "code": 0x2A, "down": down}) };
                            out.json(&shift)?;
                        }
                        continue;
                    }
                    out.json(&m)?;
                }
            }
            frame = frames.recv() => {
                let frame = frame.ok_or(Failure::Broken)?;
                heard = Instant::now();
                // The gate refuses input once a host that sends video falls
                // silent (InputGate::takes).
                if host_sends_video && let Some((g, _)) = &gate {
                    g.heard(heard);
                }
                let n = noise
                    .lock()
                    .map_err(|_| Failure::Broken)?
                    .read_message(&frame, &mut plain)?;
                let plain = &plain[..n];
                if plain.first() == Some(&media::CHUNK_TAG) {
                    if let Some(frame) = media::last_chunk(plain) {
                        out.json(&Message::Received { frame })?;
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
                        // until it comes, `key_check`).
                        if key_due(q.waiting_for_key(), key_asked, Instant::now()) {
                            key_asked = Some(Instant::now());
                            out.json(&Message::KeyFrame)?;
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
                    Message::Ping => out.json(&Message::Pong)?,
                    // A newer peer's message: ignored, never the end of the session.
                    Message::Unknown => {}
                    Message::Pong => {}
                    Message::Quality { preset, fps, i444 } => {
                        // Full colour switched: the video restarts when that
                        // changes the codec or its chroma.
                        if i444 != full_colour {
                            full_colour = i444;
                            let c = video::prefer(video::choose(&decodes.0, &decodes.1, full_colour, hevc_first), preferred.as_deref());
                            let first_changed = c.first() != chosen.first();
                            chosen = c;
                            if wants_video && source.is_some() && first_changed {
                                reopen!(want_display);
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
                            acked_at = Instant::now();
                            slow_logged = false;
                            // A picture due counts from when there is room
                            // to send it.
                            if due_since.is_some() {
                                due_since = Some(Instant::now());
                            }
                            if let Some(s) = &source {
                                s.set_room(IN_FLIGHT.saturating_sub(frame_no.wrapping_sub(acked)));
                            }
                        }
                    }
                    // The controller echoes probes at once; the host measures them.
                    // The host starts its sound for a controller that may
                    // hear it and asks; it stops when asked, or when it
                    // cannot capture (it then sends none).
                    Message::Audio { on } if matches!(side, Side::Host { .. }) => {
                        if on && may_hear {
                            if sound.is_none() {
                                // Opening may wait on the system: off the
                                // network's threads.
                                sound = tokio::task::spawn_blocking(audio::Source::open)
                                    .await
                                    .map_err(|e| e.to_string())
                                    .and_then(|r| r)
                                    .inspect_err(|e| log::info!("sound did not open: {e}"))
                                    .ok();
                            }
                        } else {
                            sound = None;
                        }
                    }
                    Message::Audio { .. } => {}
                    Message::TestDelay { reply: false } => {
                        out.json(&Message::TestDelay { reply: true })?;
                    }
                    Message::TestDelay { reply: true } => {
                        if let Some(t) = probe_sent.take() {
                            last_delay = t.elapsed().as_millis() as u32;
                            if let Some(m) = &meter {
                                m.delay(last_delay);
                            }
                            qos.user_network_delay(QOS_VIEWER, last_delay);
                        }
                    }
                    Message::Stats { delay_ms, fps, bitrate, encode_ms, frames, video } => {
                        host_sends_video = true;
                        if let Some(m) = &meter {
                            m.bitrate(bitrate / 1000);
                        }
                        if let Some(q) = &queue {
                            let mut packet = vec![media::packet::STATS];
                            packet.extend(serde_json::to_vec(&json!({"delay_ms": delay_ms, "fps": fps, "bitrate": bitrate, "encode_ms": encode_ms, "frames": frames, "video": video})).map_err(|_| Failure::Broken)?);
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
                            out.json(&Message::AiError { error })?;
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
                            out.json(&Message::FilesGet { id })?;
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
                        codecs_due = None;
                        decodes = (decode, i444);
                        // Listing the encoders asks the system: off the
                        // network's threads.
                        hevc_first = tokio::task::spawn_blocking(video::hevc_hardware).await.unwrap_or(false);
                        let c = video::prefer(video::choose(&decodes.0, &decodes.1, full_colour, hevc_first), preferred.as_deref());
                        if wants_video {
                            offer = video::offer(&decodes.0, &video::encodable()).iter().map(|c| c.to_string()).collect();
                            out.json(&Message::CodecOffer { codecs: offer.clone() })?;
                        }
                        // The video opens again only when the first choice
                        // changes: the fallbacks after it differing (codecs
                        // that came after the wait) is no reason to drop a
                        // stream and its hardware encoder.
                        let first_changed = source.is_none() || c.first() != chosen.first();
                        chosen = c;
                        if wants_video && first_changed {
                            reopen!(want_display);
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
                                reopen!(want_display);
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
                        let mut unused = false;
                        if let Some(s) = &source {
                            if !key_in_flight(key_sent, acked) {
                                s.key_frame();
                            }
                            announce(&out, s, &displays)?;
                            if !offer.is_empty() {
                                out.json(&Message::CodecOffer { codecs: offer.clone() })?;
                            }
                            if s.why.is_none() {
                                due_since.get_or_insert_with(Instant::now);
                                // A key frame that arrived (acknowledged) and
                                // was asked for again a while later is one
                                // the viewer could not use: after three, the
                                // next recovery step (for viewers that do
                                // not say so themselves).
                                if let Some((no, at)) = key_sent
                                    && at.elapsed() >= KEY_UNUSED
                                    && (acked.wrapping_sub(no) as i32) >= 0
                                    && stepped_at.is_none_or(|t| t.elapsed() >= STEP_GRACE)
                                {
                                    key_sent = None;
                                    unused_keys += 1;
                                    unused = unused_keys >= 3;
                                }
                            }
                        }
                        if unused {
                            trouble!("the viewer could not use the key frames it got".to_string());
                        }
                    }
                    // The viewer has shown no picture for a while (a
                    // `recovery` controller says so each second): after
                    // VIEWER_WAIT the next recovery step, each given
                    // STEP_GRACE to work.
                    Message::NoPicture { for_ms, why } => {
                        if source.as_ref().is_some_and(|s| s.why.is_none())
                            && reopen_at.is_none()
                            // A key frame on its way, or one due that the
                            // host's own check follows (NO_PICTURE), is
                            // waited for.
                            && !key_in_flight(key_sent, acked)
                            && due_since.is_none_or(|t| t.elapsed() >= NO_PICTURE)
                            && u128::from(for_ms) >= VIEWER_WAIT.as_millis()
                            && stepped_at.is_none_or(|t| t.elapsed() >= STEP_GRACE)
                        {
                            trouble!(format!("the viewer shows no picture ({why})"));
                        }
                    }
                    // Input reaches the host only with control-input; a
                    // malformed event is dropped, not fatal.
                    // A person's input takes over from the host's AI task:
                    // it pauses before the event is injected. Pointer moves
                    // alone (a hand resting on the viewer) neither take over
                    // nor reach the desktop while the AI acts.
                    // A controller's `sync` (after a stall or a resume): the
                    // host lets go of whatever else this session holds. It
                    // only releases, so it takes nothing over.
                    #[cfg(any(windows, target_os = "macos"))]
                    Message::Input { event } if event["kind"] == "sync" => {
                        if let (Some(inj), Ok(s), Side::Host { ai, access, .. }) = (injector.as_mut(), serde_json::from_value::<crate::input::Sync>(event), &side)
                            && s.held.valid()
                        {
                            for e in held.releases_beyond(&s.held, pointer_at) {
                                held.follow(&e);
                                inj.inject(&e, kyokai_reachable(crate::input::kyokai_input(), access, &ai.ai));
                            }
                        }
                    }
                    #[cfg(any(windows, target_os = "macos"))]
                    Message::Input { event } => {
                        if let (Some(inj), Ok(e), Side::Host { ai, access, .. }) = (injector.as_mut(), serde_json::from_value::<crate::input::Event>(event), &side) {
                            let hover = matches!(e, crate::input::Event::Pointer { action: crate::input::PointerAction::Move, .. });
                            if let crate::input::Event::Pointer { x, y, .. } = e {
                                pointer_at = (x, y);
                            }
                            if !(hover && ai.ai.acting()) {
                                held.follow(&e);
                                ai.ai.take_over();
                                inj.inject(&e, kyokai_reachable(crate::input::kyokai_input(), access, &ai.ai));
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
                    // A newer controller's action this build does not know.
                    Message::Action { action: SessionAction::Unknown } => {}
                    // Session actions need control-input, like input; a
                    // person acting takes over from the host's AI task.
                    // Waking the display does neither: it changes nothing
                    // on the desktop, so no window guard stands in its way
                    // (regression: Wake it's Shift press was refused while
                    // Kyokai's window had the focus).
                    #[cfg(any(windows, target_os = "macos"))]
                    Message::Action { action: SessionAction::Wake } => {
                        if injector.is_some() {
                            log::info!("host session: the viewer asks to wake the display");
                            crate::power::wake_display();
                            if let Some(s) = &source {
                                s.key_frame();
                            }
                        }
                    }
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
                                SessionAction::Wake | SessionAction::Unknown => {}
                            }
                            if matches!(action, SessionAction::Block | SessionAction::Unblock | SessionAction::PrivacyOn | SessionAction::PrivacyOff) {
                                let now = offered(inj);
                                holding = matches!(now, Message::Actions { blocked: true, .. });
                                out.json(&now)?;
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
    // Input waits for the next connection no more: it is refused until a
    // resumed one opens the gate again, and the GUI says so.
    if let Some((g, _)) = &gate {
        g.stop();
        if let Some(q) = &queue {
            let mut packet = vec![media::packet::INPUT];
            packet.extend(br#"{"paused":true}"#);
            q.push(packet);
        }
    }
    // A write that failed because the peer left after saying bye (its bye
    // waiting behind our frames) is an orderly close, not a broken one.
    if outcome.is_err() {
        while let Ok(frame) = frames.try_recv() {
            if let Ok(mut noise) = noise.lock()
                && let Ok(n) = noise.read_message(&frame, &mut plain)
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

/// Frozen copies of what 0.4.0 (protocol 10, no features) parses, to check
/// that everything a newer build sends it still reads.
#[cfg(test)]
mod legacy {
    use serde::Deserialize;

    /// 0.4.0's permissions.
    #[derive(Deserialize, Debug, PartialEq)]
    #[serde(rename_all = "kebab-case")]
    pub enum Permission {
        ViewScreen,
        ControlInput,
        RunAiTask,
        ApproveAiAction,
        Clipboard,
        Files,
        HearSound,
    }

    /// 0.4.0's host_hello.
    #[derive(Deserialize)]
    #[allow(dead_code)]
    pub struct HostHello {
        pub protocol: u32,
        pub version: String,
        pub source: String,
        pub device_key: String,
        pub signature: String,
    }

    /// 0.4.0's client_hello.
    #[derive(Deserialize)]
    #[allow(dead_code)]
    pub struct ClientHello {
        pub protocol: u32,
        pub version: String,
        pub device_key: String,
        pub signature: String,
        pub want: Vec<Permission>,
        pub requests: Option<[String; 2]>,
        #[serde(default)]
        pub resume: Option<String>,
    }

    /// 0.4.0's verdict.
    #[derive(Deserialize)]
    #[allow(dead_code)]
    pub struct Verdict {
        pub accepted: bool,
        #[serde(default)]
        pub granted: Vec<Permission>,
    }
}

#[cfg(test)]
mod compat_tests {
    use super::*;

    #[test]
    fn legacy_peers_parse_the_hellos_a_new_build_sends() {
        let host = HostHello {
            protocol: PROTOCOL,
            version: "0.4.1".into(),
            source: SOURCE_URL.into(),
            device_key: "k".into(),
            signature: "s".into(),
            features: vec![feature::STATUS.into(), feature::TUNNEL.into()],
        };
        let old: legacy::HostHello =
            serde_json::from_value(serde_json::to_value(&host).unwrap()).unwrap();
        assert_eq!(old.protocol, 10);
        // What a new controller sends a legacy host: no permission it lacks.
        let want = crate::access::wire_want(
            &[
                Permission::ViewScreen,
                Permission::SeeStatus,
                Permission::UseSsh,
            ],
            &Peer::default(),
        );
        let client = ClientHello {
            protocol: PROTOCOL,
            version: "0.4.1".into(),
            device_key: "k".into(),
            signature: "s".into(),
            want,
            requests: None,
            about: None,
            resume: None,
            features: our_features(),
            keys: vec![crate::keys::Proof {
                key: "k".into(),
                signature: "s".into(),
            }],
            tunnel: Some(crate::tunnel::Service::Ssh),
            watch: true,
        };
        let old: legacy::ClientHello =
            serde_json::from_value(serde_json::to_value(&client).unwrap()).unwrap();
        assert_eq!(old.want, vec![legacy::Permission::ViewScreen]);
        let verdict = Verdict {
            accepted: true,
            granted: vec![Permission::ViewScreen],
            reason: None,
            resume: None,
            about: None,
            via: Some(Via::Key),
            key: Some("k".into()),
        };
        let old: legacy::Verdict =
            serde_json::from_value(serde_json::to_value(&verdict).unwrap()).unwrap();
        assert!(old.accepted);
    }

    #[test]
    fn want_is_filtered_by_what_the_host_announced() {
        let want = [
            Permission::ViewScreen,
            Permission::SeeStatus,
            Permission::UseRdp,
        ];
        let cases: [(&[&str], &[Permission]); 4] = [
            (&[], &[Permission::ViewScreen]),
            (
                &[feature::STATUS],
                &[Permission::ViewScreen, Permission::SeeStatus],
            ),
            (
                &[feature::TUNNEL],
                &[Permission::ViewScreen, Permission::UseRdp],
            ),
            (&[feature::STATUS, feature::TUNNEL], &want),
        ];
        for (features, expect) in cases {
            let peer = Peer {
                features: features.iter().map(|f| f.to_string()).collect(),
            };
            assert_eq!(
                crate::access::wire_want(&want, &peer),
                expect,
                "{features:?}"
            );
        }
    }

    #[test]
    fn new_builds_ignore_unknown_types_and_names() {
        // A hello from 0.4.0: no features.
        let old =
            r#"{"protocol":10,"version":"0.4.0","source":"s","device_key":"k","signature":"s"}"#;
        let host: HostHello = serde_json::from_str(old).unwrap();
        assert!(host.features.is_empty());
        let m: Message = serde_json::from_str(r#"{"type":"from-the-future","x":1}"#).unwrap();
        assert!(matches!(m, Message::Unknown));
        let m: Message = serde_json::from_str(r#"{"type":"action","action":"teleport"}"#).unwrap();
        assert!(matches!(
            m,
            Message::Action {
                action: SessionAction::Unknown
            }
        ));
        let m: Message =
            serde_json::from_str(r#"{"type":"ai-command","id":1,"command":"dance"}"#).unwrap();
        assert!(matches!(
            m,
            Message::AiCommand {
                command: Steer::Unknown,
                ..
            }
        ));
        let v: Verdict =
            serde_json::from_str(r#"{"accepted":true,"granted":["view-screen","levitate"]}"#)
                .unwrap();
        assert_eq!(v.granted, vec![Permission::ViewScreen]);
        // A protocol outside the range is still refused.
        assert!(speaks(10) && !speaks(9) && !speaks(PROTOCOL + 1));
    }
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

    /// A host's end of a connection that can drop without the host noticing:
    /// once cut, the other end sees it close while the host's reads wait
    /// forever and its writes vanish (a network gone, not yet timed out).
    #[derive(Clone)]
    struct Stall(Arc<Mutex<Option<tokio::io::DuplexStream>>>);

    impl Stall {
        fn cut(&self) {
            *self.0.lock().unwrap() = None;
        }
    }

    impl AsyncRead for Stall {
        fn poll_read(
            self: std::pin::Pin<&mut Self>,
            cx: &mut std::task::Context<'_>,
            buf: &mut tokio::io::ReadBuf<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            match self.0.lock().unwrap().as_mut() {
                Some(s) => std::pin::Pin::new(s).poll_read(cx, buf),
                None => std::task::Poll::Pending,
            }
        }
    }

    impl AsyncWrite for Stall {
        fn poll_write(
            self: std::pin::Pin<&mut Self>,
            cx: &mut std::task::Context<'_>,
            b: &[u8],
        ) -> std::task::Poll<std::io::Result<usize>> {
            match self.0.lock().unwrap().as_mut() {
                Some(s) => std::pin::Pin::new(s).poll_write(cx, b),
                None => std::task::Poll::Ready(Ok(b.len())),
            }
        }
        fn poll_flush(
            self: std::pin::Pin<&mut Self>,
            cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            match self.0.lock().unwrap().as_mut() {
                Some(s) => std::pin::Pin::new(s).poll_flush(cx),
                None => std::task::Poll::Ready(Ok(())),
            }
        }
        fn poll_shutdown(
            self: std::pin::Pin<&mut Self>,
            cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            match self.0.lock().unwrap().as_mut() {
                Some(s) => std::pin::Pin::new(s).poll_shutdown(cx),
                None => std::task::Poll::Ready(Ok(())),
            }
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
    pub(super) fn device(version: &str) -> Arc<Ctx> {
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
            watching: Arc::default(),
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
        // The host unmarks its side on its own task too.
        let until = Instant::now() + Duration::from_secs(2);
        while host.sessions.any_connected() && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
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
        // The injector drops presses while a Kyokai window is in front (its
        // releases always pass, so nothing stays held): nothing to compare.
        if sent.iter().all(|s| s.2 & 0x2 != 0) {
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
            (SessionAction::Wake, "wake"),
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
    pub(super) async fn attempt_with(
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
        let from = Source {
            network: 1,
            ip: "10.126.126.2".parse().unwrap(),
        };
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

    pub(super) fn session(ctx: &Ctx, id: u64) -> Value {
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
        // The host's codec offer, whether input is paused, its stats and
        // pointer may come first: they go ahead of the video.
        let start = loop {
            let p = queue.pop(Duration::from_secs(5)).unwrap();
            if p[0] == media::packet::VIDEO_START {
                break p;
            }
            assert_ne!(
                p[0],
                media::packet::FRAME,
                "a frame before the stream's description"
            );
        };
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
            if matches!(
                p[0],
                media::packet::CURSOR | media::packet::DISPLAYS | media::packet::INPUT
            ) {
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
        let from = Source {
            network: 1,
            ip: "10.126.126.2".parse().unwrap(),
        };
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
        let Outcome::Lost { token, .. } = first.await.unwrap() else {
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

    /// A host that has not noticed its connection drop ends that session as
    /// the controller resumes it, instead of keeping it (with its capture and
    /// hardware encoder) until its idle limit: one host session is left
    /// connected (regression: the resumed stream fell to the software
    /// encoder).
    #[tokio::test]
    async fn a_resumed_session_ends_the_one_it_replaces() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let from = Source {
            network: 1,
            ip: "10.126.126.2".parse().unwrap(),
        };
        // The host's connected sessions, by id.
        let connected_hosts = |ctx: &Ctx| -> Vec<u64> {
            ctx.sessions
                .list()
                .as_array()
                .unwrap()
                .iter()
                .filter(|e| e["role"] == "host" && e["state"] == "connected")
                .filter_map(|e| e["id"].as_u64())
                .collect()
        };
        let (a, b) = tokio::io::duplex(1 << 16);
        let line = Stall(Arc::new(Mutex::new(Some(b))));
        tokio::spawn(serve(host.clone(), line.clone(), from));
        let (id, stop) = begin(&controller, &dialed, Remember::default());
        // Nothing asked for beyond the login: no capture in the test.
        let first = tokio::spawn(connect(
            controller.clone(),
            id,
            stop.clone(),
            a,
            dialed.clone(),
            zeroize::Zeroizing::new("k7mq2xpa".into()),
            Vec::new(),
            None,
        ));
        for _ in 0..200 {
            if session(&controller, id)["state"] == "connected" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        let old = connected_hosts(&host);
        assert_eq!(old.len(), 1);
        // The network drops: the controller sees it, the host does not.
        line.cut();
        let Outcome::Lost { token, .. } = first.await.unwrap() else {
            panic!("a dropped session should be resumable");
        };
        assert_eq!(connected_hosts(&host), old, "the host has not noticed");
        let (a, b) = tokio::io::duplex(1 << 16);
        tokio::spawn(serve(host.clone(), b, from));
        tokio::spawn(connect(
            controller.clone(),
            id,
            stop,
            a,
            dialed,
            zeroize::Zeroizing::new("not the password".into()),
            Vec::new(),
            Some(token),
        ));
        for _ in 0..200 {
            let now = connected_hosts(&host);
            if session(&controller, id)["state"] == "connected" && now.len() == 1 && now != old {
                return;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        panic!(
            "host sessions connected after the resume: {:?} (before: {old:?})",
            connected_hosts(&host)
        );
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
        let from = Source {
            network: 1,
            ip: "10.126.126.3".parse().unwrap(),
        };
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
        // this machine cannot capture). A description of the old stream sent
        // just before may still come first: it keeps its place among the
        // video's chunks (session::Out).
        control
            .try_send(json!({"type": "codec", "codec": "vp8"}))
            .unwrap();
        let until = Instant::now() + Duration::from_secs(8);
        loop {
            let start = next_packet(&queue, media::packet::VIDEO_START, "restarted video");
            if matches!(start["codec"].as_str(), Some("vp8" | "raw-rgba")) {
                break;
            }
            assert!(
                Instant::now() < until,
                "the video never restarted in vp8: {start}"
            );
        }
        // The connection drops and resumes: the host gets the choices again
        // before the video starts.
        host_side.abort();
        let Outcome::Lost { token, .. } = first.await.unwrap() else {
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

    /// Regression: the host's wait for the controller's codecs stayed ready
    /// once it had fired, and ran again whenever a recovery step dropped the
    /// video before opening it again: the host opened the primary display
    /// as for a controller that names no codecs (H.264 on Windows; on a Mac
    /// none, the "no codec" placeholder for good). A recovery opens the
    /// video again in the controller's codec.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_recovery_keeps_the_controller_s_codec() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let dialed = host.identity.device_id();
        let from = Source {
            network: 1,
            ip: "10.126.126.3".parse().unwrap(),
        };
        let (a, b) = tokio::io::duplex(1 << 20);
        tokio::spawn(serve(host.clone(), b, from));
        let (id, stop) = begin(&controller, &dialed, Remember::default());
        tokio::spawn(connect(
            controller.clone(),
            id,
            stop.clone(),
            a,
            dialed,
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
        // The controller decodes VP8 alone, which every host encodes (said
        // once the video runs: what a GUI sends before its session runs is
        // dropped).
        next_packet(&queue, media::packet::VIDEO_START, "video");
        control
            .try_send(json!({"type": "codecs", "decode": ["vp8"], "i444": []}))
            .unwrap();
        let until = Instant::now() + Duration::from_secs(8);
        loop {
            let start = next_packet(&queue, media::packet::VIDEO_START, "video in vp8");
            if start["codec"] == "vp8" {
                break;
            }
            // A machine that cannot capture shows the placeholder, which
            // takes no recovery step.
            if start["placeholder"].is_string() && start["placeholder"] != "no-codec" {
                stop.notify_one();
                return;
            }
            assert!(
                Instant::now() < until,
                "the video never started in vp8: {start}"
            );
        }
        // The viewer says it shows nothing: two recovery steps, opening the
        // video again at once and then after a pause.
        let until = Instant::now() + STEP_GRACE * 2 + REOPEN_PAUSE;
        let mut said: Option<Instant> = None;
        let mut reopened = 0;
        while reopened < 2 && Instant::now() < until {
            if said.is_none_or(|t| t.elapsed() >= Duration::from_millis(500)) {
                control
                    .try_send(json!({"type": "no-picture", "for_ms": 60_000, "why": "waiting"}))
                    .unwrap();
                said = Some(Instant::now());
            }
            let Ok(p) = queue.pop(Duration::from_millis(100)) else {
                continue;
            };
            if p[0] == media::packet::VIDEO_START {
                let start: Value = serde_json::from_slice(&p[1..]).unwrap();
                assert_eq!(
                    start["codec"], "vp8",
                    "a recovery left the controller's codec: {start}"
                );
                reopened += 1;
            }
        }
        assert_eq!(reopened, 2, "recovery steps");
        stop.notify_one();
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
        let from = Source {
            network: 1,
            ip: "10.126.126.9".parse().unwrap(),
        };
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
            assert!(
                !matches!(outcome, Outcome::Lost { .. }),
                "nothing to resume"
            );
        }
    }

    #[tokio::test]
    async fn a_changed_host_key_is_refused() {
        let host = device("1.0.0");
        let controller = device("1.0.0");
        let dialed = host.identity.device_id();
        controller
            .access
            .pin_host(&dialed, "c29tZW9uZSBlbHNl", &About::default(), "", &[])
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

    /// Regression: the session wrote each frame whole before it did anything
    /// else, so on a slow path input, acknowledgements and the host's word
    /// waited behind seconds of video. The writer sends what is urgent ahead
    /// of the video waiting, and queuing never waits on the network.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn urgent_messages_pass_the_video_waiting_for_a_stalled_path() {
        let (a, mut peer) = raw_channels();
        let (_, wr) = tokio::io::split(a.stream);
        let (out, _writer) = Out::start(wr, Arc::new(Mutex::new(a.noise)), a.buf);
        // More video than the path takes before the peer reads.
        const CHUNKS: usize = 30;
        for i in 0..CHUNKS {
            out.chunk(vec![i as u8; 60_000]).unwrap();
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
        out.json(&Message::Ping).unwrap();
        let mut plain = vec![0u8; MAX_FRAME];
        let mut at = None;
        for i in 0..=CHUNKS {
            let frame = read_frame(&mut peer.stream).await.unwrap();
            let n = peer.noise.read_message(&frame, &mut plain).unwrap();
            if matches!(serde_json::from_slice(&plain[..n]), Ok(Message::Ping)) {
                at = Some(i);
            }
        }
        let at = at.expect("the ping arrived");
        assert!(at < CHUNKS - 5, "the ping waited behind the video: {at}");
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
            access: host.access.clone(),
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

    /// Late acknowledgements hold the frames back however long they take,
    /// and the acknowledgement opens the window again (regression: after
    /// 3 s the host took the frames in flight for lost and sent three more
    /// and a fresh key frame onto a slow path every 3 s, until the picture
    /// froze, input paused and the session broke).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn late_acknowledgements_hold_the_window_until_they_come() {
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
            access: host.access.clone(),
        };
        let stopped = stop.clone();
        let task = tokio::spawn(async move { run(host_end, &stopped, side, None).await });
        peer.send(&Message::Codecs {
            decode: vec!["none".into()],
            i444: vec![],
        })
        .await
        .unwrap();
        let (count, newest) = raw_frames(&mut peer, Duration::from_millis(800)).await;
        assert_eq!(count, IN_FLIGHT as usize);
        let held = raw_frames(&mut peer, Duration::from_secs(4)).await.0;
        assert_eq!(held, 0, "nothing more while the acknowledgements are late");
        peer.send(&Message::Received {
            frame: newest.unwrap(),
        })
        .await
        .unwrap();
        let (count, _) = raw_frames(&mut peer, Duration::from_millis(800)).await;
        assert_eq!(
            count, IN_FLIGHT as usize,
            "the window opens with the acknowledgement"
        );
        stop.notify_one();
        let _ = task.await;
    }

    /// A key frame still on its way satisfies requests for another, so a
    /// slow path is not sent one key frame after another.
    #[test]
    fn a_key_frame_on_its_way_is_waited_for() {
        let now = Instant::now();
        for (key_sent, acked, waits) in [
            (None, 7, false),
            (Some((8, now)), 7, true),
            (Some((8, now)), 8, false),
            (Some((8, now)), 9, false),
            (Some((1, now)), u32::MAX, true),
        ] {
            assert_eq!(
                key_in_flight(key_sent, acked),
                waits,
                "{key_sent:?} {acked}"
            );
        }
    }

    /// A controller whose viewer lost frames asks for a key frame each
    /// second while none comes, a still screen sending nothing (regression:
    /// it asked only as a frame arrived).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_controller_asks_for_a_key_frame_each_second_without_frames() {
        let (ctl_end, mut peer) = raw_channels();
        let stop = Arc::new(Notify::new());
        let queue: Arc<media::Queue> = Arc::default();
        // A viewer reads, and fell behind: frames were left out.
        queue.push(vec![media::packet::STATS]);
        queue.pop(Duration::ZERO).unwrap();
        for _ in 0..4 {
            queue.push(vec![media::packet::FRAME, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1]);
        }
        assert!(queue.waiting_for_key());
        let reader = queue.clone();
        let reading = std::thread::spawn(move || {
            for _ in 0..20 {
                reader.push(vec![media::packet::STATS]);
                let _ = reader.pop(Duration::from_millis(200));
                std::thread::sleep(Duration::from_millis(200));
            }
        });
        let (_control_tx, control_rx) = mpsc::channel(4);
        let side = Side::Controller {
            queue,
            control: Arc::new(tokio::sync::Mutex::new(control_rx)),
            choices: Arc::default(),
            input: Arc::default(),
            host: Peer::default(),
            meter: Arc::default(),
        };
        let stopped = stop.clone();
        let task = tokio::spawn(async move { run(ctl_end, &stopped, side, None).await });
        let until = Instant::now() + Duration::from_millis(3500);
        let mut asked = 0;
        while let Some(left) = until.checked_duration_since(Instant::now()) {
            let Ok(Ok(m)) = tokio::time::timeout(left, peer.recv::<Value>()).await else {
                break;
            };
            asked += usize::from(m["type"] == "key-frame");
        }
        assert!((2..=4).contains(&asked), "asked {asked} times in 3.5 s");
        stop.notify_one();
        let _ = task.await;
        reading.join().unwrap();
    }

    /// Input never waits and never jams: presses are refused while the
    /// session cannot take them (never delivered late), releases never are,
    /// and what a release could not deliver is reconciled once the path
    /// moves — as one `sync` to a host that announced `input-sync`, as the
    /// releases themselves to an older host.
    #[test]
    fn input_is_refused_while_behind_and_releases_are_reconciled() {
        use crate::input::Event;
        let key = |code, down| Event::Scan {
            code,
            down,
            caps: false,
            num: false,
        };
        let text = Event::Text { text: "x".into() };
        let g = InputGate::default();
        let queued = std::cell::RefCell::new(Vec::new());
        let write = |e: &Event, room: bool| {
            g.write(e, || {
                if room {
                    queued.borrow_mut().push(e.clone());
                }
                room
            })
        };
        // No connection yet: a press is refused, a release is not.
        assert_eq!(write(&key(0x1E, true), true), -3);
        g.start();
        assert_eq!(write(&key(0x1E, true), true), 0);
        assert_eq!(write(&key(0x2A, true), true), 0);
        // A full queue refuses a press and the GUI hears input is paused.
        assert_eq!(write(&text, false), -3);
        assert!(g.paused());
        // The path stalls: presses and text are refused, never sent late.
        g.heard(Instant::now() - 2 * STALL);
        assert_eq!(write(&key(0x30, true), true), -3);
        assert_eq!(write(&text, true), -3);
        // A release goes on into the queue, in order behind its press.
        assert_eq!(write(&key(0x1E, false), true), 0);
        g.heard(Instant::now());
        assert_eq!(queued.borrow().len(), 3, "{queued:?}");
        // The connection drops with Shift held: its release cannot be
        // delivered, and is owed.
        g.stop();
        assert_eq!(write(&key(0x2A, false), true), 0);
        assert_eq!(
            queued.borrow().len(),
            3,
            "nothing queued for a dead connection"
        );
        assert!(
            g.reconcile(true).is_empty(),
            "not while there is no connection"
        );
        g.start();
        let sync = g.reconcile(true);
        assert_eq!(
            sync,
            vec![json!({"kind": "sync", "held": {"scans": [], "keys": [], "buttons": []}})]
        );
        assert!(g.reconcile(true).is_empty(), "reconciled once");
        // To an older host the lost releases go again, unless pressed since.
        g.stop();
        assert_eq!(write(&key(0x1D, false), true), 0);
        assert_eq!(write(&key(0x38, false), true), 0);
        g.start();
        assert_eq!(write(&key(0x38, true), true), 0);
        assert_eq!(
            g.reconcile(false),
            vec![serde_json::to_value(key(0x1D, false)).unwrap()]
        );
    }

    /// Input and AI commands queued while a session reconnected are dropped
    /// at the resume, never delivered late; the viewer's settings among
    /// them reach the host.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn input_queued_across_a_resume_is_dropped() {
        let (ctl_end, mut peer) = raw_channels();
        let stop = Arc::new(Notify::new());
        let (control_tx, control_rx) = mpsc::channel(8);
        for m in [
            json!({"type": "input", "event": {"kind": "text", "text": "late"}}),
            json!({"type": "ai-command", "id": 1, "command": "cancel"}),
            json!({"type": "quality", "preset": "speed", "fps": 15, "i444": false}),
        ] {
            control_tx.try_send(m).unwrap();
        }
        let side = Side::Controller {
            queue: Arc::default(),
            control: Arc::new(tokio::sync::Mutex::new(control_rx)),
            choices: Arc::default(),
            input: Arc::default(),
            host: Peer::default(),
            meter: Arc::default(),
        };
        let stopped = stop.clone();
        let task = tokio::spawn(async move { run(ctl_end, &stopped, side, None).await });
        let until = Instant::now() + Duration::from_millis(800);
        let mut seen = Vec::new();
        while let Some(left) = until.checked_duration_since(Instant::now()) {
            let Ok(Ok(m)) = tokio::time::timeout(left, peer.recv::<Value>()).await else {
                break;
            };
            seen.push(m["type"].as_str().unwrap_or_default().to_owned());
        }
        assert!(seen.contains(&"quality".to_owned()), "{seen:?}");
        assert!(
            !seen.iter().any(|t| t == "input" || t == "ai-command"),
            "{seen:?}"
        );
        stop.notify_one();
        let _ = task.await;
    }

    #[test]
    fn key_frames_are_asked_for_at_most_each_second() {
        let now = Instant::now() + Duration::from_secs(10);
        let ago = |ms| Some(now - Duration::from_millis(ms));
        for (waiting, asked, want) in [
            (false, None, false),
            (true, None, true),
            (true, ago(400), false),
            (true, ago(1000), true),
            (false, ago(5000), false),
        ] {
            assert_eq!(key_due(waiting, asked, now), want, "{waiting} {asked:?}");
        }
    }

    /// The recovery of a host's picture: open again at once, then after a
    /// pause, then in the next codec, then the placeholder while the screen
    /// is tried again ever less often but never given up (regression: the
    /// third end within 30 s gave the test pattern for good).
    #[test]
    fn the_host_never_gives_up_on_the_picture() {
        let s = Duration::from_secs;
        let cases = [
            (0, Recovery::Reopen(Duration::ZERO)),
            (1, Recovery::Reopen(REOPEN_PAUSE)),
            (2, Recovery::NextCodec),
            (3, Recovery::Placeholder(s(2))),
            (4, Recovery::Placeholder(s(4))),
            (5, Recovery::Placeholder(s(8))),
            (6, Recovery::Placeholder(PROBE_MAX)),
            (40, Recovery::Placeholder(PROBE_MAX)),
            (u32::MAX, Recovery::Placeholder(PROBE_MAX)),
        ];
        for (trouble, want) in cases {
            assert_eq!(recovery(trouble), want, "trouble {trouble}");
        }
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
            access: host.access.clone(),
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
        tokio::spawn(serve(
            host.clone(),
            b,
            Source {
                network: 0,
                ip: "192.168.1.20".parse().unwrap(),
            },
        ));
        assert_eq!(identify(a).await.unwrap(), host.identity.device_id());
        assert!(host.sessions.list().as_array().unwrap().is_empty());
    }

    /// A session closed while it waits for the host's answer ends at once
    /// and never connects (regression: the close took effect only once the
    /// host accepted; the connection showed connected for a moment and the
    /// GUI opened a tab of its own for it).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_session_closed_while_it_waits_never_connects() {
        let (host, controller) = (device("0.5.4"), device("0.5.4"));
        let dialed = host.identity.device_id();
        let (a, b) = tokio::io::duplex(1 << 16);
        tokio::spawn(serve(
            host.clone(),
            b,
            Source {
                network: 0,
                ip: "192.168.1.20".parse().unwrap(),
            },
        ));
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
        for _ in 0..500 {
            if session(&controller, id)["state"] == "confirming"
                && !host.access.pending().as_array().unwrap().is_empty()
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(session(&controller, id)["state"], "confirming");
        controller.sessions.close(id).unwrap();
        tokio::time::timeout(Duration::from_secs(2), run)
            .await
            .expect("the close ends the wait at once")
            .unwrap();
        assert_eq!(session(&controller, id)["state"], "closed");
        // The host's user saying yes afterwards changes nothing here.
        let q = host.access.pending()[0]["id"].as_u64();
        if let Some(q) = q {
            let _ = host.access.decide(q, Some(vec![Permission::ViewScreen]));
        }
        tokio::time::sleep(Duration::from_millis(300)).await;
        assert_eq!(session(&controller, id)["state"], "closed");
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
        tokio::spawn(serve(
            host.clone(),
            b,
            Source {
                network: 0,
                ip: "192.168.1.20".parse().unwrap(),
            },
        ));
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
        assert!(net.by_address_status()["port"].is_null());
        // A macOS runner once answered right after the listener closed:
        // the port must refuse within 2 s.
        let refused = (0..20).any(|_| {
            std::thread::sleep(Duration::from_millis(100));
            identify(&net) == Err("unreachable")
        });
        assert!(refused, "off");
    }
}

/// Sign-in with keys (doc/en-us/identity-and-access.md, "Keys").
#[cfg(test)]
mod login_tests {
    use super::tests::{attempt_with, device, session};
    use super::*;

    /// Gives the controller an Ed25519 sign-in key from `seed` and lets it
    /// connect to the host under `name`. Returns: the controller's key id.
    fn pair(host: &Ctx, controller: &Ctx, seed: [u8; 32], name: &str) -> String {
        let kept = crate::keys::add_login(controller.access.secrets(), "", &STANDARD.encode(seed))
            .unwrap();
        let line = kept["public"].as_str().unwrap();
        crate::keys::add_authorized(host.access.secrets(), name, line).unwrap();
        kept["id"].as_str().unwrap().to_owned()
    }

    /// Ends a controller's session that connected.
    fn close(controller: &Ctx, id: u64) {
        let _ = controller.sessions.close(id);
    }

    /// What a key login signs is the handshake after frame 2: the host
    /// reads it right after writing frame 2, the controller right after
    /// reading it, and both must see the same.
    #[test]
    fn both_sides_hash_frame_2_alike() {
        let (mut i, _) = noise(true).unwrap();
        let (mut r, _) = noise(false).unwrap();
        let (mut msg, mut out) = (vec![0u8; MAX_FRAME], vec![0u8; MAX_FRAME]);
        let n = i.write_message(&[], &mut msg).unwrap();
        r.read_message(&msg[..n], &mut out).unwrap();
        let n = r.write_message(b"{\"host_hello\":1}", &mut msg).unwrap();
        let host_h2 = r.get_handshake_hash().to_vec();
        i.read_message(&msg[..n], &mut out).unwrap();
        let controller_h2 = i.get_handshake_hash().to_vec();
        assert_eq!(host_h2, controller_h2);
        let n = i.write_message(b"{\"client_hello\":1}", &mut msg).unwrap();
        r.read_message(&msg[..n], &mut out).unwrap();
        assert_eq!(i.get_handshake_hash(), r.get_handshake_hash());
        assert_ne!(
            i.get_handshake_hash(),
            host_h2.as_slice(),
            "frame 3 moves it on"
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_key_admits_within_the_grants_and_is_remembered() {
        use Permission::*;
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access.set_password_grants(vec![ViewScreen]).unwrap();
        let key = pair(&host, &controller, [11; 32], "laptop");
        let dialed = host.identity.device_id();
        // No password: the key admits within the grants, without asking.
        let id = attempt_with(&host, &controller, &dialed, "", None, vec![ViewScreen]).await;
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen"]));
        assert_eq!(
            controller.access.saved(&dialed).key.as_deref(),
            Some(key.as_str())
        );
        assert!(crate::keys::authorized(host.access.secrets())[0].last > 0);
        close(&controller, id);
        // More than the grants: the host's user is asked (and allows).
        let want = vec![ViewScreen, RunAiTask];
        let id = attempt_with(&host, &controller, &dialed, "", Some(want.clone()), want).await;
        let s = session(&controller, id);
        assert_eq!(s["granted"], json!(["view-screen", "run-ai-task"]), "{s}");
        close(&controller, id);
        // The key no longer authorized: the password gets in, and the key
        // remembered for this host is forgotten.
        let a = crate::keys::authorized(host.access.secrets());
        crate::keys::remove_authorized(host.access.secrets(), &a[0].id).unwrap();
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        let id = attempt_with(
            &host,
            &controller,
            &dialed,
            "k7mq2xpa",
            None,
            vec![ViewScreen],
        )
        .await;
        assert_eq!(session(&controller, id)["state"], "connected");
        assert_eq!(controller.access.saved(&dialed).key, None);
        close(&controller, id);
    }

    /// With nobody at the computer a key admits like the permanent
    /// password, while Connect without asking is on.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn nobody_there_a_key_admits_like_the_permanent_password() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access
            .set_password_grants(vec![Permission::ViewScreen])
            .unwrap();
        pair(&host, &controller, [12; 32], "laptop");
        host.access.set_serve(Serve::Unattended);
        let dialed = host.identity.device_id();
        let want = vec![Permission::ViewScreen, Permission::ControlInput];
        let id = attempt_with(&host, &controller, &dialed, "", None, want.clone()).await;
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["granted"], json!(["view-screen"]));
        close(&controller, id);
        host.access.set_without_asking(false).unwrap();
        let id = attempt_with(&host, &controller, &dialed, "", None, want).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "absent"})
        );
    }

    /// Plays a host announcing `features` up to the controller's hello.
    /// Returns: the hello as it came, as JSON.
    async fn hello_to(features: &[&str]) -> Value {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        pair(&host, &controller, [13; 32], "laptop");
        let dialed = host.identity.device_id();
        let (a, mut b) = tokio::io::duplex(1 << 16);
        let (id, stop) = begin(&controller, &dialed, Remember::default());
        let password = zeroize::Zeroizing::new("k7mq2xpa".to_owned());
        let want = vec![Permission::ViewScreen];
        tokio::spawn(connect(
            controller.clone(),
            id,
            stop,
            a,
            dialed,
            password,
            want,
            None,
        ));
        let (mut hs, my_static) = noise(false).unwrap();
        let mut buf = vec![0u8; MAX_FRAME];
        let f = read_frame(&mut b).await.unwrap();
        hs.read_message(&f, &mut buf).unwrap();
        let hello = HostHello {
            protocol: PROTOCOL,
            version: "1.0.0".into(),
            source: SOURCE_URL.into(),
            device_key: STANDARD.encode(host.identity.public_key()),
            signature: sign_static(&host.identity, &my_static),
            features: features.iter().map(|f| f.to_string()).collect(),
        };
        let n = hs
            .write_message(&serde_json::to_vec(&hello).unwrap(), &mut buf)
            .unwrap();
        write_frame(&mut b, &buf[..n]).await.unwrap();
        let f = read_frame(&mut b).await.unwrap();
        let n = hs.read_message(&f, &mut buf).unwrap();
        serde_json::from_slice(&buf[..n]).unwrap()
    }

    /// Keys go only to a host that announced `key-login`: a 0.4.0 host
    /// gets the hello it knows.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn keys_go_only_to_hosts_that_take_them() {
        let old = hello_to(&[]).await;
        assert!(old.get("keys").is_none(), "{old}");
        let new = hello_to(&[feature::KEY_LOGIN]).await;
        assert_eq!(new["keys"].as_array().map(Vec::len), Some(1), "{new}");
        assert!(new["about"].is_object(), "a key may admit before any login");
    }
}

/// Tunnels to a host's own services (doc/en-us/remote-services.md).
#[cfg(test)]
mod tunnel_tests {
    use super::tests::{device, session};
    use super::*;
    use crate::tunnel::Service;
    use crate::tunnel::tests::serve_on;

    /// A listener standing in for a service on this computer, which `reach`
    /// dials for `s`.
    async fn service(s: Service) -> tokio::net::TcpListener {
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        serve_on(s, l.local_addr().unwrap().port());
        l
    }

    /// Lets a host's temporary password admit within `grants`.
    fn host_with(grants: Vec<Permission>) -> Arc<Ctx> {
        let host = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        host.access.set_password_grants(grants).unwrap();
        host
    }

    /// Opens a tunnel to `s` from the controller over an in-memory pipe and
    /// lets `decide` answer the host's confirmation.
    /// Returns: the controller's session id once it settled, and the
    /// confirmation the host showed, if any.
    async fn open(
        host: &Arc<Ctx>,
        controller: &Arc<Ctx>,
        s: Service,
        password: &str,
        decide: Option<Vec<Permission>>,
    ) -> (u64, Option<Value>) {
        let (a, b) = tokio::io::duplex(1 << 16);
        let from = Source {
            network: 1,
            ip: "10.126.126.2".parse().unwrap(),
        };
        tokio::spawn(serve(host.clone(), b, from));
        let dialed = host.identity.device_id();
        let (id, stop) = begin_as(controller, &dialed, Remember::default(), Kind::Tunnel(s));
        let password = zeroize::Zeroizing::new(password.to_owned());
        let want = vec![s.permission()];
        tokio::spawn(connect(
            controller.clone(),
            id,
            stop,
            a,
            dialed,
            password,
            want,
            None,
        ));
        let mut asked = None;
        for _ in 0..400 {
            tokio::time::sleep(Duration::from_millis(20)).await;
            if asked.is_none()
                && let Some(q) = host
                    .access
                    .pending()
                    .as_array()
                    .and_then(|a| a.first().cloned())
            {
                host.access
                    .decide(q["id"].as_u64().unwrap(), decide.clone())
                    .unwrap();
                asked = Some(q);
            }
            let st = session(controller, id)["state"].clone();
            if st != "connecting" && st != "confirming" {
                break;
            }
        }
        (id, asked)
    }

    /// Reads the host's next bytes through the GUI's end of a tunnel.
    async fn next(ends: &crate::tunnel::Ends) -> Option<Vec<u8>> {
        tokio::time::timeout(Duration::from_secs(5), async {
            ends.down.lock().await.recv().await
        })
        .await
        .expect("bytes or the end in time")
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_tunnel_carries_bytes_and_ends_with_its_service() {
        let listener = service(Service::Ssh).await;
        let served = tokio::spawn(async move {
            let (mut s, _) = listener.accept().await.unwrap();
            s.write_all(b"SSH-2.0-test\r\n").await.unwrap();
            let mut hello = [0u8; 5];
            s.read_exact(&mut hello).await.unwrap();
            s.write_all(&hello.to_ascii_uppercase()).await.unwrap();
            // The service ends the connection.
        });
        let host = host_with(vec![Permission::UseSsh]);
        let controller = device("1.0.1");
        let (id, asked) = open(&host, &controller, Service::Ssh, "k7mq2xpa", None).await;
        assert!(asked.is_none(), "within the grants: nobody is asked");
        let s = session(&controller, id);
        assert_eq!(s["state"], "connected", "{s}");
        assert_eq!(s["service"], "ssh");
        assert_eq!(s["granted"], json!(["use-ssh"]));
        // The host lists it with its service, apart from desktop sessions.
        let listed = host.sessions.list();
        assert_eq!(listed[0]["service"], "ssh", "{listed}");
        assert_eq!(host.sessions.connected_hosts(), 0, "not a desktop session");
        assert_eq!(host.sessions.connected(|k| matches!(k, Kind::Tunnel(_))), 1);
        let ends = controller.sessions.tunnel(id).unwrap();
        assert_eq!(next(&ends).await.unwrap(), b"SSH-2.0-test\r\n");
        ends.up.send(b"hello".to_vec()).await.unwrap();
        assert_eq!(next(&ends).await.unwrap(), b"HELLO");
        served.await.unwrap();
        assert!(next(&ends).await.is_none(), "it ends with the service");
        let until = Instant::now() + Duration::from_secs(5);
        while session(&controller, id)["state"] == "connected" && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(session(&controller, id)["state"], "closed");
    }

    /// A service's permission works like any other: outside the password
    /// grants the host's user is asked, and with nobody there it is refused.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn outside_the_grants_the_host_user_is_asked() {
        let listener = service(Service::Rdp).await;
        // A service that keeps its connections open.
        tokio::spawn(async move {
            let mut open = Vec::new();
            while let Ok((s, _)) = listener.accept().await {
                open.push(s);
            }
        });
        let host = host_with(vec![Permission::ViewScreen]);
        let controller = device("1.0.1");
        let rdp = Some(vec![Permission::UseRdp]);
        let (id, asked) = open(&host, &controller, Service::Rdp, "k7mq2xpa", rdp).await;
        let asked = asked.expect("the host's user is asked");
        assert_eq!(asked["service"], "rdp");
        assert_eq!(asked["want"], json!(["use-rdp"]));
        assert_eq!(session(&controller, id)["granted"], json!(["use-rdp"]));
        // Denied, or allowed without the service's permission: refused.
        let (id, _) = open(&host, &controller, Service::Rdp, "k7mq2xpa", Some(vec![])).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "denied"})
        );
        // Nobody at the computer: refused, the permanent password too.
        host.access.set_serve(Serve::Unattended);
        host.access
            .slots
            .lock()
            .unwrap()
            .set_permanent(
                host.access.secrets(),
                Some("long-lived-pw"),
                &host.identity.public_key(),
            )
            .unwrap();
        let (id, _) = open(&host, &controller, Service::Rdp, "long-lived-pw", None).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "absent"})
        );
        // Within the grants it goes unattended.
        host.access
            .set_password_grants(vec![Permission::UseRdp])
            .unwrap();
        let (id, _) = open(&host, &controller, Service::Rdp, "long-lived-pw", None).await;
        assert_eq!(session(&controller, id)["state"], "connected");
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_service_that_is_down_is_refused_by_name() {
        // A port nothing listens on.
        drop(service(Service::Vnc).await);
        let host = host_with(vec![Permission::UseVnc]);
        let controller = device("1.0.1");
        let (id, _) = open(&host, &controller, Service::Vnc, "k7mq2xpa", None).await;
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "service-down"})
        );
    }

    /// A tunnel reaches its service on the port the host's user set, not the
    /// standard one; port 0 is refused.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_tunnel_reaches_the_port_its_host_set() {
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let host = host_with(vec![Permission::UseSsh]);
        let ports = crate::access::ServicePorts {
            ssh: Some(l.local_addr().unwrap().port()),
            ..Default::default()
        };
        host.access.set_service_ports(ports).unwrap();
        assert_eq!(host.access.service_ports(), ports);
        let controller = device("1.0.1");
        let (id, _) = open(&host, &controller, Service::Ssh, "k7mq2xpa", None).await;
        assert_eq!(session(&controller, id)["state"], "connected");
        let reached = tokio::time::timeout(Duration::from_secs(5), l.accept()).await;
        assert!(matches!(reached, Ok(Ok(_))), "the set port was dialed");
        let zero = crate::access::ServicePorts {
            vnc: Some(0),
            ..Default::default()
        };
        assert!(host.access.set_service_ports(zero).is_err());
        assert_eq!(host.access.service_ports(), ports, "unchanged");
    }

    /// A host that announced no tunnels never gets asked for one: the
    /// controller stops after its hello, before frame 3.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn legacy_hosts_never_get_a_tunnel_request() {
        let host = device("0.4.0");
        let controller = device("1.0.1");
        let dialed = host.identity.device_id();
        let (a, mut b) = tokio::io::duplex(1 << 16);
        let kind = Kind::Tunnel(Service::Ssh);
        let (id, stop) = begin_as(&controller, &dialed, Remember::default(), kind);
        let password = zeroize::Zeroizing::new("k7mq2xpa".to_owned());
        let want = vec![Permission::UseSsh];
        let c = controller.clone();
        let dialing =
            tokio::spawn(
                async move { connect(c, id, stop, a, dialed, password, want, None).await },
            );
        let (mut hs, my_static) = noise(false).unwrap();
        let mut buf = vec![0u8; MAX_FRAME];
        let f = read_frame(&mut b).await.unwrap();
        hs.read_message(&f, &mut buf).unwrap();
        let hello = HostHello {
            protocol: PROTOCOL,
            version: "0.4.0".into(),
            source: SOURCE_URL.into(),
            device_key: STANDARD.encode(host.identity.public_key()),
            signature: sign_static(&host.identity, &my_static),
            features: Vec::new(),
        };
        let n = hs
            .write_message(&serde_json::to_vec(&hello).unwrap(), &mut buf)
            .unwrap();
        write_frame(&mut b, &buf[..n]).await.unwrap();
        assert!(matches!(dialing.await.unwrap(), Outcome::Done));
        assert!(read_frame(&mut b).await.is_err(), "no frame 3");
        assert_eq!(session(&controller, id)["failure"], "unsupported");
    }
}

/// Quiet status watchers (doc/en-us/computer-use.md, "Host status").
#[cfg(test)]
mod watch_tests {
    use super::tests::device;
    use super::*;

    /// A host whose temporary and permanent passwords admit within
    /// `grants`.
    fn host_with(grants: Vec<Permission>) -> Arc<Ctx> {
        let host = device("1.0.0");
        {
            let mut slots = host.access.slots.lock().unwrap();
            let key = host.identity.public_key();
            slots.set_temporary("k7mq2xpa", &key).unwrap();
            slots
                .set_permanent(host.access.secrets(), Some("long-lived-pw"), &key)
                .unwrap();
        }
        host.access.set_password_grants(grants).unwrap();
        host
    }

    /// Starts watching the host from the controller over an in-memory
    /// pipe. Returns: the controller's session id, once it is connected or
    /// has ended, and the task running it.
    async fn watch(
        host: &Arc<Ctx>,
        controller: &Arc<Ctx>,
        password: &str,
    ) -> (u64, tokio::task::JoinHandle<Outcome>) {
        let (a, b) = tokio::io::duplex(1 << 16);
        let from = Source {
            network: 1,
            ip: "10.126.126.2".parse().unwrap(),
        };
        tokio::spawn(serve(host.clone(), b, from));
        let dialed = host.identity.device_id();
        controller.watching.expect(&dialed);
        let (id, stop) = begin_as(controller, &dialed, Remember::default(), Kind::Watch);
        let password = zeroize::Zeroizing::new(password.to_owned());
        let want = vec![Permission::SeeStatus];
        let c = controller.clone();
        let task =
            tokio::spawn(
                async move { connect(c, id, stop, a, dialed, password, want, None).await },
            );
        for _ in 0..200 {
            if !matches!(
                state(controller, id),
                Some(State::Connecting | State::Confirming)
            ) {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        (id, task)
    }

    /// A session's state and failure from the books (watches are never
    /// listed).
    fn state(ctx: &Ctx, id: u64) -> Option<State> {
        let s = ctx.sessions.inner.lock().unwrap();
        s.1.iter().find(|e| e.id == id).map(|e| e.state)
    }

    fn failure(ctx: &Ctx, id: u64) -> Option<Failure> {
        let s = ctx.sessions.inner.lock().unwrap();
        s.1.iter().find(|e| e.id == id).and_then(|e| e.failure)
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_watcher_gets_samples_quietly() {
        let host = host_with(vec![Permission::ViewScreen, Permission::SeeStatus]);
        let controller = device("1.0.1");
        let (id, _task) = watch(&host, &controller, "k7mq2xpa").await;
        assert_eq!(state(&controller, id), Some(State::Connected));
        // Samples arrive, the first within a sampling period or so.
        let dialed = host.identity.device_id();
        let until = Instant::now() + crate::system::SAMPLE_EVERY * 3;
        let mut listed = Value::Null;
        while Instant::now() < until {
            listed = controller.watching.list();
            if listed[0]["state"] == "watching" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        assert_eq!(listed[0]["device_id"], json!(dialed), "{listed}");
        assert!(listed[0]["sample"]["memory"]["total"].as_u64().unwrap() > 0);
        // The sample says what the password grants without asking, so the
        // controller offers only that, and whether the host's AI is ready.
        let offers = listed[0]["sample"]["offers"].as_array().unwrap();
        assert!(offers.contains(&json!("view-screen")), "{listed}");
        assert!(!offers.contains(&json!("run-ai-task")), "{listed}");
        assert!(listed[0]["sample"]["ai"].is_boolean(), "{listed}");
        // Quiet on both sides: not among the sessions, no trace among the
        // host's trusted devices, nobody asked; the host lists its watcher.
        assert_eq!(controller.sessions.list(), json!([]));
        assert_eq!(host.sessions.list(), json!([]));
        assert!(host.access.trusted().is_empty());
        let watchers = host.sessions.watchers();
        assert_eq!(
            watchers[0]["device_id"],
            json!(controller.identity.device_id())
        );
        assert!(watchers[0]["since"].as_u64().unwrap() > 0);
        // `see-status` turned off ends it at the next sample.
        host.access
            .set_password_grants(vec![Permission::ViewScreen])
            .unwrap();
        let until = Instant::now() + crate::system::SAMPLE_EVERY * 3;
        while state(&controller, id) == Some(State::Connected) && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        assert_eq!(state(&controller, id), Some(State::Closed));
        let until = Instant::now() + Duration::from_secs(2);
        while host.sessions.watchers() != json!([]) && Instant::now() < until {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        assert_eq!(host.sessions.watchers(), json!([]));
    }

    /// A watcher whose Home hides asks its host for the slow pace, and for
    /// the usual one as Home shows again, on the session it has (regression:
    /// Home dropped its watches as it hid and dialed every host again as it
    /// showed).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_watcher_changes_its_pace_on_the_same_session() {
        let host = host_with(vec![Permission::SeeStatus]);
        let controller = device("1.0.1");
        let (id, _task) = watch(&host, &controller, "k7mq2xpa").await;
        assert_eq!(state(&controller, id), Some(State::Connected));
        let dialed = host.identity.device_id();
        for (slow, watchers) in [(true, 1), (false, 0)] {
            controller.watching.expect_slow(&dialed, slow);
            let until = Instant::now() + Duration::from_secs(2);
            while crate::system::slow_watchers() != watchers && Instant::now() < until {
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
            assert_eq!(crate::system::slow_watchers(), watchers, "slow: {slow}");
            assert_eq!(state(&controller, id), Some(State::Connected));
        }
    }

    /// A watcher is admitted only by a password or key within the grants,
    /// never by asking the host's user; with nobody at the computer only
    /// the permanent password (or a key) admits.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_watcher_never_asks() {
        let host = host_with(vec![Permission::ViewScreen]);
        let controller = device("1.0.1");
        let (id, _task) = watch(&host, &controller, "k7mq2xpa").await;
        assert_eq!(
            failure(&controller, id),
            Some(Failure::Refused(Refusal::Denied))
        );
        let (id, _task) = watch(&host, &controller, "").await;
        assert_eq!(
            failure(&controller, id),
            Some(Failure::Refused(Refusal::Denied))
        );
        assert_eq!(host.access.pending(), json!([]), "nobody was asked");
        host.access
            .set_password_grants(vec![Permission::SeeStatus])
            .unwrap();
        host.access.set_serve(Serve::Unattended);
        let (id, _task) = watch(&host, &controller, "k7mq2xpa").await;
        assert_eq!(
            failure(&controller, id),
            Some(Failure::Refused(Refusal::Absent))
        );
        let (id, task) = watch(&host, &controller, "long-lived-pw").await;
        assert_eq!(state(&controller, id), Some(State::Connected));
        task.abort();
    }

    /// Watchers have a limit of their own, apart from the desktop sessions.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn watchers_have_a_limit_of_their_own() {
        let host = host_with(vec![Permission::SeeStatus]);
        for _ in 0..crate::watch::MAX_WATCHERS {
            let (id, _) = host.sessions.open(Role::Host, "100000009");
            host.sessions.update(id, |e| {
                e.kind = Kind::Watch;
                e.state = State::Connected;
            });
        }
        assert_eq!(host.sessions.connected_hosts(), 0, "not desktop sessions");
        let controller = device("1.0.1");
        let (id, _task) = watch(&host, &controller, "k7mq2xpa").await;
        assert_eq!(
            failure(&controller, id),
            Some(Failure::Refused(Refusal::Busy))
        );
    }
}

/// Refusals over a connection that loses what it has not sent when it
/// closes, as EasyTier's data plane does.
#[cfg(test)]
mod refusal_tests {
    use super::tests::{device, session};
    use super::*;
    use std::pin::Pin;
    use std::task::{Context, Poll, Waker};

    /// A real TCP connection whose writes leave a little later, on a task of
    /// their own, and whose unsent bytes are lost when it is dropped; a
    /// flush waits until they left.
    struct Lossy {
        rd: tokio::net::tcp::OwnedReadHalf,
        tx: mpsc::UnboundedSender<Vec<u8>>,
        pending: Arc<AtomicUsize>,
        waker: Arc<Mutex<Option<Waker>>>,
        sending: tokio::task::JoinHandle<()>,
    }

    impl Lossy {
        fn new(stream: tokio::net::TcpStream) -> Lossy {
            let (rd, mut wr) = stream.into_split();
            let (tx, mut rx) = mpsc::unbounded_channel::<Vec<u8>>();
            let pending = Arc::new(AtomicUsize::new(0));
            let waker: Arc<Mutex<Option<Waker>>> = Arc::default();
            let (left, wake) = (pending.clone(), waker.clone());
            let sending = tokio::spawn(async move {
                while let Some(bytes) = rx.recv().await {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                    if wr.write_all(&bytes).await.is_err() {
                        return;
                    }
                    if left.fetch_sub(bytes.len(), Ordering::SeqCst) == bytes.len()
                        && let Some(w) = wake.lock().unwrap().take()
                    {
                        w.wake();
                    }
                }
            });
            Lossy {
                rd,
                tx,
                pending,
                waker,
                sending,
            }
        }
    }

    impl Drop for Lossy {
        fn drop(&mut self) {
            // What has not left is lost with the socket.
            self.sending.abort();
        }
    }

    impl AsyncRead for Lossy {
        fn poll_read(
            mut self: Pin<&mut Self>,
            cx: &mut Context<'_>,
            buf: &mut tokio::io::ReadBuf<'_>,
        ) -> Poll<std::io::Result<()>> {
            Pin::new(&mut self.rd).poll_read(cx, buf)
        }
    }

    impl AsyncWrite for Lossy {
        fn poll_write(
            self: Pin<&mut Self>,
            _: &mut Context<'_>,
            buf: &[u8],
        ) -> Poll<std::io::Result<usize>> {
            self.pending.fetch_add(buf.len(), Ordering::SeqCst);
            let _ = self.tx.send(buf.to_vec());
            Poll::Ready(Ok(buf.len()))
        }

        fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
            if self.pending.load(Ordering::SeqCst) == 0 {
                return Poll::Ready(Ok(()));
            }
            *self.waker.lock().unwrap() = Some(cx.waker().clone());
            if self.pending.load(Ordering::SeqCst) == 0 {
                Poll::Ready(Ok(()))
            } else {
                Poll::Pending
            }
        }

        fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
            self.poll_flush(cx)
        }
    }

    /// Regression: the host dropped the connection right after writing its
    /// refusal, and over EasyTier the refusal never left: the controller's
    /// tunnel ended `broken` instead of naming why it was refused.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_refusal_reaches_the_controller_before_the_connection_closes() {
        let host = device("1.0.0");
        let controller = device("1.0.1");
        host.access.set_serve(Serve::None);
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let at = listener.local_addr().unwrap();
        let h = host.clone();
        tokio::spawn(async move {
            let (stream, from) = listener.accept().await.unwrap();
            let from = Source {
                network: 0,
                ip: from.ip(),
            };
            serve(h, Lossy::new(stream), from).await;
        });
        let stream = tokio::net::TcpStream::connect(at).await.unwrap();
        let dialed = host.identity.device_id();
        let kind = Kind::Tunnel(crate::tunnel::Service::Ssh);
        let (id, stop) = begin_as(&controller, &dialed, Remember::default(), kind);
        let password = zeroize::Zeroizing::new("k7mq2xpa".to_owned());
        let want = vec![Permission::UseSsh];
        let done = connect(
            controller.clone(),
            id,
            stop,
            stream,
            dialed,
            password,
            want,
            None,
        );
        let outcome = tokio::time::timeout(Duration::from_secs(10), done)
            .await
            .expect("the controller hears back");
        assert!(matches!(outcome, Outcome::Done));
        assert_eq!(
            session(&controller, id)["failure"],
            json!({"refused": "absent"})
        );
    }
}

/// Device routes (doc/en-us/networking.md, "Routes").
#[cfg(test)]
mod route_tests {
    use super::tests::device;
    use super::*;
    use crate::net::{Ask, Choice, Network};

    /// Serves `host` on a fresh port of 127.0.0.1, as it serves connections
    /// by address. Returns: the address.
    async fn listen(host: Arc<Ctx>) -> String {
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = l.local_addr().unwrap().port();
        tokio::spawn(async move {
            while let Ok((stream, from)) = l.accept().await {
                let from = Source {
                    network: 0,
                    ip: from.ip(),
                };
                tokio::spawn(serve(host.clone(), stream, from));
            }
        });
        format!("127.0.0.1:{port}")
    }

    /// A host whose temporary password admits a viewer without asking.
    fn host() -> Arc<Ctx> {
        let host = device("1.0.0");
        host.access
            .slots
            .lock()
            .unwrap()
            .set_temporary("k7mq2xpa", &host.identity.public_key())
            .unwrap();
        host.access
            .set_password_grants(vec![Permission::ViewScreen])
            .unwrap();
        host
    }

    /// Connects to `device_id` over `route` and waits until the session
    /// connected or ended. Returns: its entry in the status.
    fn connect(net: &Network, controller: &Ctx, device_id: &str, route: Choice) -> Value {
        let id = net
            .connect(Ask {
                device_id: device_id.to_owned(),
                route,
                password: zeroize::Zeroizing::new("k7mq2xpa".into()),
                want: vec![Permission::ViewScreen],
                remember: Remember::default(),
                kind: Kind::Desktop,
            })
            .unwrap();
        let until = Instant::now() + Duration::from_secs(20);
        loop {
            let list = controller.sessions.list();
            let entry = list
                .as_array()
                .unwrap()
                .iter()
                .find(|e| e["id"] == id)
                .cloned()
                .unwrap_or(Value::Null);
            if matches!(
                entry["state"].as_str(),
                Some("connected" | "failed" | "closed")
            ) || Instant::now() > until
            {
                let _ = controller.sessions.close(id);
                return entry;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    /// A known host keeps every address it was reached at; Auto skips an
    /// address that does not answer and one where another device answers,
    /// connects over the one that is the device and remembers how it went;
    /// a route fixed by hand is used alone.
    #[test]
    fn auto_takes_the_route_that_is_the_device() {
        let (host, squatter, controller) = (host(), self::host(), device("1.0.0"));
        let net = Network::start(controller.access.secrets(), controller.clone(), false).unwrap();
        let rt = net.handle();
        let (good, other) =
            rt.block_on(async { (listen(host.clone()).await, listen(squatter).await) });
        let id = host.identity.device_id();
        // The first connection, typed by address, makes it a known host.
        let first = connect(&net, &controller, &id, Choice::Address(good.clone()));
        assert_eq!(first["state"], "connected", "{first}");
        assert_eq!(first["route"], json!(good));
        let dead = "127.0.0.1:9";
        for a in [dead, other.as_str()] {
            controller
                .access
                .set_routes(&id, Some(a), None, None)
                .unwrap();
        }
        // What answers where.
        let probes = rt.block_on(net.dialer().probe(&id));
        let states: Vec<_> = probes
            .iter()
            .map(|p| (p.address.as_str(), p.state))
            .collect();
        assert_eq!(
            states,
            [
                (good.as_str(), "ok"),
                (dead, "unreachable"),
                (other.as_str(), "other-device")
            ]
        );
        assert!(probes[0].rtt_ms.is_some());
        // Auto connects over the device's own address.
        let auto = connect(&net, &controller, &id, Choice::Auto);
        assert_eq!(auto["state"], "connected", "{auto}");
        assert_eq!(auto["route"], json!(good));
        let known = controller.access.known(&id).unwrap();
        let addresses: Vec<_> = known.routes.iter().map(|r| r.address.as_str()).collect();
        assert_eq!(addresses, [good.as_str(), dead, other.as_str()]);
        assert!(known.routes[0].stats.last_ok > 0);
        // Fixed by hand on the squatter's address: that route alone, refused.
        controller
            .access
            .set_routes(&id, None, None, Some(&other))
            .unwrap();
        let route = Choice::ask(&controller, &id, None, None);
        assert_eq!(route, Choice::Address(other.clone()));
        let fixed = connect(&net, &controller, &id, route);
        assert_eq!(fixed["state"], "failed", "{fixed}");
        assert_eq!(fixed["failure"], "wrong-device");
    }
}
