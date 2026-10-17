//! The session agent (doc/en-us/run-modes.md, "The session agent").
//!
//! In the helper modes the engine runs as SYSTEM in session 0, which has no
//! desktop. It starts a copy of kyokai-svc as SYSTEM in the console session
//! (`kyokai-svc agent`), which follows the input desktop (also the UAC
//! secure desktop and the lock screen) and does the desktop's work there:
//! capture and encoding, input, the pointer, the clipboard and the AI's
//! desktop calls. Only encoded video, input events, clipboard text and the
//! AI's screenshots cross between them; nothing decodes remote media.
//!
//! This module is both ends: [`Link`] in the helper, [`serve`] in the agent.
//! Video, input, the pointer, the clipboard and the AI's desktop check
//! [`link`] and send their work through it when it is set.
//!
//! The helper runs on Windows only until the Mac pass; elsewhere the link
//! is never set and most of this module has no caller.
#![cfg_attr(not(windows), allow(dead_code))]

use crate::ai::desktop::Frame;
use crate::input::{Area, Event, LocalInput};
use crate::media::VideoStart;
use crate::video::{Display, Encoded, Targets};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::collections::HashMap;
use std::io::{BufWriter, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, OnceLock, mpsc as std_mpsc};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

/// Message kinds on the pipes, after a u32 little-endian length.
const JSON: u8 = 0;
/// A video frame: stream u32, flags u8, presentation time u64, bytes.
const VIDEO: u8 = 1;
/// A screenshot reply: request id u64, width u32, height u32, BGRA.
const SCREEN: u8 = 2;

/// Longest message accepted (a 4K BGRA screenshot is 33 MB).
const MAX_MESSAGE: usize = 64 << 20;

/// How long a call waits for the agent.
const CALL_TIMEOUT: Duration = Duration::from_secs(5);

/// What a call answers while no agent is connected.
pub const NO_DESKTOP: &str = "the desktop is not available (no session agent)";

/// The helper's link, set when the engine runs as the helper.
static LINK: OnceLock<Arc<Link>> = OnceLock::new();

/// Whether this process is the agent (its capture threads follow the input
/// desktop).
static AGENT: AtomicBool = AtomicBool::new(false);

/// The helper's link to its agent, when this engine is the helper.
pub fn link() -> Option<&'static Arc<Link>> {
    LINK.get()
}

/// Whether this process is the session agent.
pub fn is_agent() -> bool {
    AGENT.load(Ordering::Relaxed)
}

/// Writes one message.
fn write_message(w: &mut impl Write, kind: u8, parts: &[&[u8]]) -> std::io::Result<()> {
    let len = 1 + parts.iter().map(|p| p.len()).sum::<usize>();
    w.write_all(&(len as u32).to_le_bytes())?;
    w.write_all(&[kind])?;
    for p in parts {
        w.write_all(p)?;
    }
    w.flush()
}

/// Reads one message: its kind and body.
fn read_message(r: &mut impl Read) -> std::io::Result<(u8, Vec<u8>)> {
    let mut len = [0u8; 4];
    r.read_exact(&mut len)?;
    let len = u32::from_le_bytes(len) as usize;
    if len == 0 || len > MAX_MESSAGE {
        return Err(std::io::Error::other("bad agent message length"));
    }
    let mut body = vec![0u8; len];
    r.read_exact(&mut body)?;
    let kind = body.remove(0);
    Ok((kind, body))
}

/// A reply the link waits for.
enum Reply {
    Json(Result<Value, String>),
    Screen(Frame),
}

/// A video stream the agent produces for one session.
struct Stream {
    tx: mpsc::Sender<Encoded>,
}

/// The helper's end: calls into the agent and the video it streams.
pub struct Link {
    out: Mutex<Option<BufWriter<Box<dyn Write + Send>>>>,
    next: AtomicU64,
    waiting: Mutex<HashMap<u64, std_mpsc::Sender<Reply>>>,
    streams: Mutex<HashMap<u32, Stream>>,
    next_stream: AtomicU32,
}

impl Link {
    fn new() -> Link {
        Link {
            out: Mutex::new(None),
            next: AtomicU64::new(1),
            waiting: Mutex::default(),
            streams: Mutex::default(),
            next_stream: AtomicU32::new(1),
        }
    }

    /// Uses a newly connected agent: `out` carries requests to it, `input`
    /// its replies and video, read on a thread of its own until it ends.
    /// Side effects: when the agent goes, every call waiting fails and every
    /// video stream ends.
    fn attach(self: &Arc<Self>, input: Box<dyn Read + Send>, out: Box<dyn Write + Send>) {
        *self.out.lock().expect("agent link") = Some(BufWriter::new(out));
        let link = self.clone();
        std::thread::spawn(move || {
            let mut input = input;
            while let Ok((kind, body)) = read_message(&mut input) {
                link.received(kind, &body);
            }
            *link.out.lock().expect("agent link") = None;
            link.waiting.lock().expect("agent link").clear();
            link.streams.lock().expect("agent link").clear();
        });
    }

    /// Hands one message from the agent to whoever waits for it.
    fn received(&self, kind: u8, body: &[u8]) {
        match kind {
            JSON => {
                let Ok(v) = serde_json::from_slice::<Value>(body) else {
                    return;
                };
                let Some(id) = v["id"].as_u64() else {
                    return;
                };
                let reply = match v.get("err").and_then(Value::as_str) {
                    Some(e) => Err(e.to_owned()),
                    None => Ok(v["ok"].clone()),
                };
                if let Some(w) = self.waiting.lock().expect("agent link").remove(&id) {
                    let _ = w.send(Reply::Json(reply));
                }
            }
            VIDEO if body.len() >= 13 => {
                let stream = u32::from_le_bytes(body[0..4].try_into().expect("4 bytes"));
                let flags = body[4];
                let pts = u64::from_le_bytes(body[5..13].try_into().expect("8 bytes"));
                if let Some(s) = self.streams.lock().expect("agent link").get(&stream) {
                    // The newest frame wins: a full channel drops this one.
                    let _ = s.tx.try_send((flags, pts, body[13..].to_vec()));
                }
            }
            SCREEN if body.len() >= 16 => {
                let id = u64::from_le_bytes(body[0..8].try_into().expect("8 bytes"));
                let width = u32::from_le_bytes(body[8..12].try_into().expect("4 bytes"));
                let height = u32::from_le_bytes(body[12..16].try_into().expect("4 bytes"));
                let frame = Frame {
                    bgra: body[16..].to_vec(),
                    width,
                    height,
                };
                if let Some(w) = self.waiting.lock().expect("agent link").remove(&id) {
                    let _ = w.send(Reply::Screen(frame));
                }
            }
            _ => {}
        }
    }

    /// Sends a request without waiting.
    fn send(&self, op: &str, params: Value) -> Result<(), String> {
        self.post(0, op, params)
    }

    fn post(&self, id: u64, op: &str, mut params: Value) -> Result<(), String> {
        params["op"] = json!(op);
        params["id"] = json!(id);
        let body = serde_json::to_vec(&params).map_err(|e| e.to_string())?;
        let mut out = self.out.lock().expect("agent link");
        let w = out.as_mut().ok_or(NO_DESKTOP)?;
        write_message(w, JSON, &[&body]).map_err(|_| NO_DESKTOP.to_string())
    }

    /// Sends a request and waits for its reply.
    fn request(&self, op: &str, params: Value) -> Result<Reply, String> {
        let id = self.next.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = std_mpsc::channel();
        self.waiting.lock().expect("agent link").insert(id, tx);
        if let Err(e) = self.post(id, op, params) {
            self.waiting.lock().expect("agent link").remove(&id);
            return Err(e);
        }
        rx.recv_timeout(CALL_TIMEOUT).map_err(|_| {
            self.waiting.lock().expect("agent link").remove(&id);
            NO_DESKTOP.to_string()
        })
    }

    /// Sends a request and decodes its JSON reply.
    fn call<T: for<'de> Deserialize<'de>>(&self, op: &str, params: Value) -> Result<T, String> {
        match self.request(op, params)? {
            Reply::Json(r) => serde_json::from_value(r?).map_err(|e| e.to_string()),
            Reply::Screen(_) => Err("unexpected screenshot".into()),
        }
    }

    /// The agent's displays (empty without an agent).
    pub fn displays(&self) -> Vec<Display> {
        self.call("displays", json!({})).unwrap_or_default()
    }

    /// Starts a video stream in the agent: `display` (the primary when
    /// None) in `codec`; its frames go to `tx`, and `key`, `targets` and
    /// `stop` are followed until `stop` is set.
    /// Returns: the stream's start and the display shown, or None when the
    /// agent cannot start it (the caller shows the test pattern).
    pub fn open_video(
        self: &Arc<Self>,
        display: Option<Display>,
        codec: &str,
        tx: mpsc::Sender<Encoded>,
        key: Arc<AtomicBool>,
        targets: Arc<Targets>,
        stop: Arc<AtomicBool>,
    ) -> Option<(VideoStart, Option<Display>)> {
        let stream = self.next_stream.fetch_add(1, Ordering::Relaxed);
        self.streams
            .lock()
            .expect("agent link")
            .insert(stream, Stream { tx });
        #[derive(Deserialize)]
        struct Opened {
            start: VideoStart,
            display: Option<Display>,
        }
        let opened: Opened = match self.call(
            "video.open",
            json!({"stream": stream, "display": display, "codec": codec}),
        ) {
            Ok(o) => o,
            Err(_) => {
                self.streams.lock().expect("agent link").remove(&stream);
                return None;
            }
        };
        // Follows the session's requests on the stream's behalf.
        let link = self.clone();
        std::thread::spawn(move || {
            let mut sent = (0, 0);
            while !stop.load(Ordering::Relaxed) {
                if key.swap(false, Ordering::Relaxed) {
                    let _ = link.send("video.key", json!({"stream": stream}));
                }
                let now = (
                    targets.fps.load(Ordering::Relaxed),
                    targets.bitrate.load(Ordering::Relaxed),
                );
                if now != sent {
                    sent = now;
                    let _ = link.send(
                        "video.targets",
                        json!({"stream": stream, "fps": now.0, "bitrate": now.1}),
                    );
                }
                std::thread::sleep(Duration::from_millis(20));
            }
            let _ = link.send("video.close", json!({"stream": stream}));
            link.streams.lock().expect("agent link").remove(&stream);
        });
        Some((opened.start, opened.display))
    }

    /// A new input session in the agent.
    pub fn injector(&self) -> u64 {
        let id = self.next.fetch_add(1, Ordering::Relaxed);
        let _ = self.send("input.open", json!({"injector": id}));
        id
    }

    /// Injects an event through the agent's injector `id`.
    /// Returns: whether it was injected (false without an agent).
    pub fn inject(&self, id: u64, e: &Event, area: Option<Area>) -> bool {
        self.call("input", json!({"injector": id, "event": e, "area": area}))
            .unwrap_or(false)
    }

    /// Releases what injector `id` holds.
    pub fn release(&self, id: u64) {
        let _ = self.send("input.release", json!({"injector": id}));
    }

    /// Ends injector `id`.
    pub fn drop_injector(&self, id: u64) {
        let _ = self.send("input.close", json!({"injector": id}));
    }

    /// Locks the screen of the console session.
    /// Returns: whether Windows locked it (false without an agent).
    pub fn lock_screen(&self) -> bool {
        self.call("lock", json!({})).unwrap_or(false)
    }

    /// Blocks or unblocks the host's own input for injector `id`.
    /// Returns: whether it is blocked for it now (false without an agent).
    pub fn block_input(&self, id: u64, on: bool) -> bool {
        self.call("input.block", json!({"injector": id, "on": on}))
            .unwrap_or(false)
    }

    /// Turns privacy mode on or off for injector `id`.
    /// Returns: whether it is on for it now (false without an agent).
    pub fn privacy(&self, id: u64, on: bool) -> bool {
        self.call("privacy", json!({"injector": id, "on": on}))
            .unwrap_or(false)
    }

    /// Whether privacy mode is still on for injector `id`.
    pub fn privacy_on(&self, id: u64) -> bool {
        self.call("privacy.on", json!({"injector": id}))
            .unwrap_or(false)
    }

    /// Whether the host's input is still blocked for injector `id`.
    pub fn input_blocked(&self, id: u64) -> bool {
        self.call("input.blocked", json!({"injector": id}))
            .unwrap_or(false)
    }

    /// The pointer as fractions of `area` (the primary when None) and
    /// whether it is shown.
    pub fn pointer(&self, area: Option<Area>) -> Option<(f32, f32, bool)> {
        self.call("pointer", json!({"area": area})).ok().flatten()
    }

    /// The agent's clipboard.
    pub fn clipboard(self: &Arc<Self>) -> Box<dyn crate::clipboard::Clipboard> {
        Box::new(RemoteClipboard(self.clone()))
    }

    /// The AI's desktop in the agent.
    pub fn desktop(self: &Arc<Self>) -> Result<RemoteDesktop, String> {
        let id = self.next.fetch_add(1, Ordering::Relaxed);
        self.call::<()>("ai.open", json!({"desk": id}))?;
        Ok(RemoteDesktop {
            link: self.clone(),
            id,
        })
    }
}

/// A clipboard item on the pipe: text, or an image as PNG (encoded by the
/// agent from this computer's clipboard; images from a controller are not
/// decoded in the helper modes).
#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "kebab-case")]
enum ClipItem {
    Text { text: String },
    Image { png: String },
}

/// The agent's clipboard, seen from the helper.
struct RemoteClipboard(Arc<Link>);

impl crate::clipboard::Clipboard for RemoteClipboard {
    fn serial(&mut self) -> Option<u32> {
        self.0.call("clip.serial", json!({})).ok().flatten()
    }

    fn item(&mut self) -> Option<crate::clipboard::Item> {
        match self
            .0
            .call::<Option<ClipItem>>("clip.item", json!({}))
            .ok()??
        {
            ClipItem::Text { text } => Some(crate::clipboard::Item::Text(text)),
            // A privileged process never decodes images; it forwards the PNG.
            ClipItem::Image { png } => Some(crate::clipboard::Item::Png(png)),
        }
    }

    fn set(&mut self, item: &crate::clipboard::Item) -> bool {
        match item {
            crate::clipboard::Item::Text(text) => self
                .0
                .call(
                    "clip.set",
                    json!({"item": ClipItem::Text { text: text.clone() }}),
                )
                .unwrap_or(false),
            _ => false,
        }
    }
}

/// The AI's desktop in the agent, seen from the helper.
pub struct RemoteDesktop {
    link: Arc<Link>,
    id: u64,
}

/// How long ago the host user last used the keyboard or mouse, in
/// milliseconds (Instants do not cross processes).
#[derive(Serialize, Deserialize)]
struct InputAges {
    press: Option<u64>,
    moved: Option<u64>,
}

impl crate::ai::desktop::Desktop for RemoteDesktop {
    fn size(&mut self) -> Result<(u32, u32), String> {
        self.link.call("ai.size", json!({"desk": self.id}))
    }

    fn capture(&mut self) -> Result<Frame, String> {
        match self.link.request("ai.capture", json!({"desk": self.id}))? {
            Reply::Screen(f) => Ok(f),
            Reply::Json(r) => Err(r.err().unwrap_or_else(|| "no screenshot".into())),
        }
    }

    fn inject(&mut self, e: &Event) -> bool {
        self.link
            .call("ai.inject", json!({"desk": self.id, "event": e}))
            .unwrap_or(false)
    }

    fn release(&mut self) {
        let _ = self.link.send("ai.release", json!({"desk": self.id}));
    }

    fn cursor(&mut self) -> Option<(f32, f32)> {
        self.link
            .call("ai.cursor", json!({"desk": self.id}))
            .ok()
            .flatten()
    }

    fn local_input(&mut self) -> LocalInput {
        let now = Instant::now();
        let ago = |ms: Option<u64>| ms.and_then(|ms| now.checked_sub(Duration::from_millis(ms)));
        match self
            .link
            .call::<InputAges>("ai.local", json!({"desk": self.id}))
        {
            Ok(a) => LocalInput {
                press: ago(a.press),
                moved: ago(a.moved),
            },
            // Without the agent the desktop is gone: treat it as the host
            // user taking over, so a task never acts blind.
            Err(_) => LocalInput {
                press: Some(now),
                moved: Some(now),
            },
        }
    }

    fn stop_requested(&mut self) -> bool {
        self.link
            .call("ai.stop", json!({"desk": self.id}))
            .unwrap_or(true)
    }
}

impl Drop for RemoteDesktop {
    fn drop(&mut self) {
        let _ = self.link.send("ai.close", json!({"desk": self.id}));
    }
}

/// The agent's end: serves the helper's requests on this desktop until the
/// helper goes.
/// Inputs: `input` carries requests, `out` replies and video.
/// Side effects: captures, injects, reads and writes the clipboard.
pub fn serve(input: Box<dyn Read + Send>, out: Box<dyn Write + Send>) {
    AGENT.store(true, Ordering::Relaxed);
    let out = Arc::new(Mutex::new(BufWriter::new(out)));
    let mut agent = Agent {
        out: out.clone(),
        sources: HashMap::new(),
        injectors: HashMap::new(),
        clipboard: None,
        desks: HashMap::new(),
    };
    let mut input = input;
    while let Ok((kind, body)) = read_message(&mut input) {
        if kind != JSON {
            continue;
        }
        let Ok(req) = serde_json::from_slice::<Value>(&body) else {
            continue;
        };
        follow_input_desktop();
        let id = req["id"].as_u64().unwrap_or(0);
        let msg = match agent.handle(id, &req) {
            Ok(Some(v)) => json!({"id": id, "ok": v}),
            Ok(None) => continue,
            Err(e) => json!({"id": id, "err": e}),
        };
        if id != 0 {
            let body = serde_json::to_vec(&msg).expect("json");
            if write_message(&mut *out.lock().expect("agent out"), JSON, &[&body]).is_err() {
                break;
            }
        }
    }
}

/// What the agent keeps for the helper's sessions.
struct Agent {
    out: Arc<Mutex<BufWriter<Box<dyn Write + Send>>>>,
    sources: HashMap<u32, crate::video::Source>,
    injectors: HashMap<u64, Injector>,
    clipboard: Option<Box<dyn crate::clipboard::Clipboard>>,
    desks: HashMap<u64, Box<dyn crate::ai::desktop::Desktop>>,
}

#[cfg(windows)]
type Injector = crate::input::Injector;

/// Off Windows there is nothing to inject into yet.
#[cfg(not(windows))]
#[derive(Default)]
struct Injector;

#[cfg(not(windows))]
impl Injector {
    fn set_area(&mut self, _: Option<Area>) {}
    fn inject(&mut self, _: &Event) -> bool {
        false
    }
    fn release_all(&mut self) {}
    fn block_input(&mut self, _: bool) -> bool {
        false
    }
    fn input_blocked(&self) -> bool {
        false
    }
    fn lock_screen(&self) -> bool {
        false
    }
    fn privacy(&mut self, _: bool) -> bool {
        false
    }
    fn privacy_on(&self) -> bool {
        false
    }
}

impl Agent {
    /// Answers one request.
    /// Returns: the reply, or None when the reply was sent already (a
    /// screenshot) or none is due; an error is sent as the reply.
    fn handle(&mut self, id: u64, req: &Value) -> Result<Option<Value>, String> {
        let stream = req["stream"].as_u64().unwrap_or(0) as u32;
        let injector = req["injector"].as_u64().unwrap_or(0);
        let desk = req["desk"].as_u64().unwrap_or(0);
        let area: Option<Area> = serde_json::from_value(req["area"].clone()).unwrap_or(None);
        Ok(Some(match req["op"].as_str().unwrap_or_default() {
            "displays" => json!(crate::video::displays()),
            "video.open" => {
                let display: Option<Display> =
                    serde_json::from_value(req["display"].clone()).unwrap_or(None);
                let mut source =
                    crate::video::Source::open(display, req["codec"].as_str().unwrap_or("h264"));
                let reply = json!({"start": source.start, "display": source.display});
                let (_, empty) = mpsc::channel(1);
                let mut frames = std::mem::replace(&mut source.frames, empty);
                let out = self.out.clone();
                std::thread::spawn(move || {
                    while let Some((flags, pts, bytes)) = frames.blocking_recv() {
                        let head =
                            [&stream.to_le_bytes()[..], &[flags], &pts.to_le_bytes()].concat();
                        let mut w = out.lock().expect("agent out");
                        if write_message(&mut *w, VIDEO, &[&head, &bytes]).is_err() {
                            break;
                        }
                    }
                });
                self.sources.insert(stream, source);
                reply
            }
            "video.key" => {
                if let Some(s) = self.sources.get(&stream) {
                    s.key_frame();
                }
                return Ok(None);
            }
            "video.targets" => {
                if let Some(s) = self.sources.get(&stream) {
                    s.set_targets(
                        req["fps"].as_u64().unwrap_or(30) as u32,
                        req["bitrate"].as_u64().unwrap_or(4_000_000) as u32,
                    );
                }
                return Ok(None);
            }
            "video.close" => {
                self.sources.remove(&stream);
                return Ok(None);
            }
            "input.open" => {
                self.injectors.insert(injector, Injector::default());
                return Ok(None);
            }
            "input" => {
                let Ok(e) = serde_json::from_value::<Event>(req["event"].clone()) else {
                    return Ok(Some(json!(false)));
                };
                let inj = self.injectors.entry(injector).or_default();
                inj.set_area(area);
                json!(inj.inject(&e))
            }
            "input.release" => {
                if let Some(i) = self.injectors.get_mut(&injector) {
                    i.release_all();
                }
                return Ok(None);
            }
            "input.close" => {
                if let Some(mut i) = self.injectors.remove(&injector) {
                    i.release_all();
                }
                return Ok(None);
            }
            "input.block" => json!(
                self.injectors
                    .entry(injector)
                    .or_default()
                    .block_input(req["on"].as_bool().unwrap_or(false))
            ),
            "input.blocked" => json!(
                self.injectors
                    .get(&injector)
                    .is_some_and(|i| i.input_blocked())
            ),
            "privacy" => json!(
                self.injectors
                    .entry(injector)
                    .or_default()
                    .privacy(req["on"].as_bool().unwrap_or(false))
            ),
            "privacy.on" => json!(
                self.injectors
                    .get(&injector)
                    .is_some_and(|i| i.privacy_on())
            ),
            "lock" => json!(Injector::default().lock_screen()),
            "pointer" => json!(match area {
                Some(a) => crate::input::pointer_in(a),
                None => crate::input::pointer(),
            }),
            "clip.serial" => json!(self.clip().and_then(|c| c.serial())),
            "clip.item" => json!(self.clip().and_then(|c| c.item()).and_then(|i| match i {
                crate::clipboard::Item::Text(text) => Some(ClipItem::Text { text }),
                crate::clipboard::Item::Image(img) =>
                    crate::clipboard::to_png(&img).map(|png| ClipItem::Image { png }),
                crate::clipboard::Item::Png(png) => Some(ClipItem::Image { png }),
            })),
            "clip.set" => match serde_json::from_value::<ClipItem>(req["item"].clone()) {
                Ok(ClipItem::Text { text }) => json!(
                    self.clip()
                        .is_some_and(|c| c.set(&crate::clipboard::Item::Text(text)))
                ),
                // Images from a controller are not taken in the helper modes.
                _ => json!(false),
            },
            "ai.open" => {
                let d = crate::ai::open_screen()?;
                self.desks.insert(desk, d);
                json!(null)
            }
            "ai.close" => {
                self.desks.remove(&desk);
                return Ok(None);
            }
            op => {
                let Some(d) = self.desks.get_mut(&desk) else {
                    return Err("no such desktop".into());
                };
                match op {
                    "ai.size" => json!(d.size()?),
                    "ai.capture" => {
                        let f = d.capture()?;
                        let head = [
                            &id.to_le_bytes()[..],
                            &f.width.to_le_bytes(),
                            &f.height.to_le_bytes(),
                        ]
                        .concat();
                        let mut w = self.out.lock().expect("agent out");
                        let _ = write_message(&mut *w, SCREEN, &[&head, &f.bgra]);
                        return Ok(None);
                    }
                    "ai.inject" => match serde_json::from_value::<Event>(req["event"].clone()) {
                        Ok(e) => json!(d.inject(&e)),
                        Err(_) => json!(false),
                    },
                    "ai.release" => {
                        d.release();
                        return Ok(None);
                    }
                    "ai.cursor" => json!(d.cursor()),
                    "ai.local" => {
                        let l = d.local_input();
                        let age = |t: Option<Instant>| t.map(|t| t.elapsed().as_millis() as u64);
                        json!(InputAges {
                            press: age(l.press),
                            moved: age(l.moved),
                        })
                    }
                    "ai.stop" => json!(d.stop_requested()),
                    _ => return Err("unknown request".into()),
                }
            }
        }))
    }

    /// The agent's clipboard, opened on first use.
    fn clip(&mut self) -> Option<&mut Box<dyn crate::clipboard::Clipboard>> {
        if self.clipboard.is_none() {
            self.clipboard = crate::clipboard::system();
        }
        self.clipboard.as_mut()
    }
}

/// Moves this thread to the desktop that receives input now (the user's,
/// the UAC secure desktop or the lock screen), when this process is the
/// agent.
#[cfg(windows)]
pub fn follow_input_desktop() {
    if is_agent() {
        windows_impl::follow_input_desktop();
    }
}

#[cfg(not(windows))]
pub fn follow_input_desktop() {}

#[cfg(windows)]
pub use windows_impl::{run_agent, send_sas, start_helper};

#[cfg(windows)]
mod windows_impl {
    //! The pipes, the agent's process and its desktop on Windows.
    use super::*;
    use std::cell::Cell;
    use std::fs::File;
    use std::os::windows::io::FromRawHandle;
    use std::path::{Path, PathBuf};
    use windows::Win32::Foundation::{CloseHandle, HANDLE, HLOCAL, LocalFree, WAIT_TIMEOUT};
    use windows::Win32::Security::Authorization::{
        ConvertStringSecurityDescriptorToSecurityDescriptorW, SDDL_REVISION_1,
    };
    use windows::Win32::Security::{
        DuplicateTokenEx, PSECURITY_DESCRIPTOR, SECURITY_ATTRIBUTES, SecurityIdentification,
        SetTokenInformation, TOKEN_ALL_ACCESS, TokenPrimary, TokenSessionId,
    };
    use windows::Win32::Storage::FileSystem::{
        FILE_FLAG_FIRST_PIPE_INSTANCE, PIPE_ACCESS_INBOUND, PIPE_ACCESS_OUTBOUND,
    };
    use windows::Win32::System::Pipes::{
        ConnectNamedPipe, CreateNamedPipeW, GetNamedPipeClientProcessId,
        GetNamedPipeServerProcessId, PIPE_READMODE_BYTE, PIPE_TYPE_BYTE, PIPE_WAIT,
    };
    use windows::Win32::System::RemoteDesktop::WTSGetActiveConsoleSessionId;
    use windows::Win32::System::StationsAndDesktops::{
        CloseDesktop, DESKTOP_ACCESS_FLAGS, DESKTOP_CONTROL_FLAGS, GetUserObjectInformationW,
        HDESK, OpenInputDesktop, SetThreadDesktop, UOI_NAME,
    };
    use windows::Win32::System::Threading::{
        CREATE_NO_WINDOW, CREATE_UNICODE_ENVIRONMENT, CreateProcessAsUserW, GetCurrentProcess,
        GetCurrentProcessId, OpenProcessToken, PROCESS_INFORMATION, STARTUPINFOW, TerminateProcess,
        WaitForSingleObject,
    };
    use windows::core::{HSTRING, PCWSTR, PWSTR};

    /// Only SYSTEM may open the pipes.
    const PIPE_SDDL: &str = "D:P(A;;GA;;;SY)";

    /// How often the helper looks at the console session.
    const WATCH: Duration = Duration::from_secs(2);

    /// How long a newly started agent has to connect.
    const CONNECT_WAIT: Duration = Duration::from_secs(15);

    /// Where Windows keeps the policy that lets services send Ctrl+Alt+Del.
    const SAS_POLICY_KEY: &str = r"SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System";
    const SAS_POLICY: &str = "SoftwareSASGeneration";
    /// The policy's bit for services.
    const SAS_BY_SERVICES: u32 = 1;

    /// Sends Ctrl+Alt+Del (the secure attention sequence) to the console
    /// session, as RustDesk's service does with `SendSAS`.
    /// Returns: whether it was sent.
    /// Side effects: lets services send it for the moment of the call
    /// (`SoftwareSASGeneration`), then puts the policy back as it was.
    /// Notes: works only in a service running as SYSTEM: the helper.
    pub fn send_sas() -> bool {
        use windows::Win32::Foundation::FreeLibrary;
        use windows::Win32::System::LibraryLoader::{
            GetProcAddress, LOAD_LIBRARY_SEARCH_SYSTEM32, LoadLibraryExW,
        };
        use windows::Win32::System::Registry::{
            HKEY, HKEY_LOCAL_MACHINE, KEY_QUERY_VALUE, KEY_SET_VALUE, REG_DWORD, RRF_RT_REG_DWORD,
            RegCloseKey, RegDeleteValueW, RegGetValueW, RegOpenKeyExW, RegSetValueExW,
        };
        use windows::core::{s, w};
        let key_name = HSTRING::from(SAS_POLICY_KEY);
        let value = HSTRING::from(SAS_POLICY);
        // SAFETY: the key and the library are closed and freed below; the
        // function pointer has SendSAS's documented signature.
        unsafe {
            let mut before = 0u32;
            let mut size = 4u32;
            let had = RegGetValueW(
                HKEY_LOCAL_MACHINE,
                &key_name,
                &value,
                RRF_RT_REG_DWORD,
                None,
                Some(&mut before as *mut u32 as *mut _),
                Some(&mut size),
            )
            .is_ok();
            let mut key = HKEY::default();
            if RegOpenKeyExW(
                HKEY_LOCAL_MACHINE,
                &key_name,
                None,
                KEY_QUERY_VALUE | KEY_SET_VALUE,
                &mut key,
            )
            .is_err()
            {
                return false;
            }
            let set = |v: u32| RegSetValueExW(key, &value, None, REG_DWORD, Some(&v.to_le_bytes()));
            let allowed = had && before & SAS_BY_SERVICES != 0;
            if !allowed {
                let _ = set(before | SAS_BY_SERVICES);
            }
            let sent = match LoadLibraryExW(w!("sas.dll"), None, LOAD_LIBRARY_SEARCH_SYSTEM32) {
                Ok(lib) => {
                    let sent = match GetProcAddress(lib, s!("SendSAS")) {
                        Some(f) => {
                            let send: unsafe extern "system" fn(i32) = std::mem::transmute(f);
                            // FALSE: as a service, not as the user.
                            send(0);
                            true
                        }
                        None => false,
                    };
                    let _ = FreeLibrary(lib);
                    sent
                }
                Err(_) => false,
            };
            if !allowed {
                let _ = if had {
                    set(before)
                } else {
                    RegDeleteValueW(key, &value)
                };
            }
            let _ = RegCloseKey(key);
            sent
        }
    }

    /// Sets the link and keeps an agent running in the console session,
    /// starting `exe agent …` there with this process's SYSTEM token and
    /// replacing it when the console session changes.
    /// Side effects: starts a supervising thread and agent processes.
    pub fn start_helper(exe: PathBuf) {
        let link = Arc::new(Link::new());
        if LINK.set(link.clone()).is_err() {
            return;
        }
        std::thread::spawn(move || supervise(&link, &exe));
    }

    /// An agent process and the console session it runs in.
    struct Child {
        process: HANDLE,
        session: u32,
    }

    impl Child {
        fn alive(&self) -> bool {
            // SAFETY: a wait on a process handle this struct owns.
            unsafe { WaitForSingleObject(self.process, 0) == WAIT_TIMEOUT }
        }
    }

    impl Drop for Child {
        fn drop(&mut self) {
            // SAFETY: ends and closes the process handle this struct owns.
            unsafe {
                let _ = TerminateProcess(self.process, 0);
                let _ = CloseHandle(self.process);
            }
        }
    }

    fn supervise(link: &Arc<Link>, exe: &Path) {
        let mut child: Option<Child> = None;
        loop {
            // SAFETY: a plain query.
            let session = unsafe { WTSGetActiveConsoleSessionId() };
            let current = child
                .as_ref()
                .is_some_and(|c| c.alive() && c.session == session && link.connected());
            if !current {
                child = None; // ends the old agent
                if session != u32::MAX {
                    match start(link, exe, session) {
                        Ok(c) => child = Some(c),
                        Err(e) => eprintln!("kyokai: session agent: {e}"),
                    }
                }
            }
            std::thread::sleep(WATCH);
        }
    }

    impl Link {
        fn connected(&self) -> bool {
            self.out.lock().expect("agent link").is_some()
        }
    }

    /// Starts an agent in `session` and attaches the link once both of its
    /// pipes are connected by that process.
    fn start(link: &Arc<Link>, exe: &Path, session: u32) -> Result<Child, String> {
        let tag = random_tag();
        let down_name = format!(r"\\.\pipe\Kyokai.agent.{tag}.down");
        let up_name = format!(r"\\.\pipe\Kyokai.agent.{tag}.up");
        let down = pipe(&down_name, PIPE_ACCESS_OUTBOUND.0)?;
        let up = pipe(&up_name, PIPE_ACCESS_INBOUND.0)?;
        // SAFETY: a plain query.
        let me = unsafe { GetCurrentProcessId() };
        let cmd = format!(
            "\"{}\" agent --down {down_name} --up {up_name} --helper-pid {me}",
            exe.display()
        );
        let (process, pid) = spawn_in_session(exe, &cmd, session)?;
        let child = Child { process, session };
        // Both ends connect, or the agent is ended and the next round tries
        // again.
        for (h, name) in [(down, &down_name), (up, &up_name)] {
            let (tx, rx) = std_mpsc::channel();
            let raw = h.0 as usize;
            std::thread::spawn(move || {
                // SAFETY: waits for a client on a pipe handle created above.
                let ok = unsafe { ConnectNamedPipe(HANDLE(raw as _), None) }.is_ok();
                let _ = tx.send(ok);
            });
            if rx.recv_timeout(CONNECT_WAIT).is_err() {
                // Unblocks the waiting thread, then gives up.
                let _ = std::fs::OpenOptions::new()
                    .read(true)
                    .write(true)
                    .open(name);
                return Err("the agent did not connect".into());
            }
            let mut client = 0u32;
            // SAFETY: a plain query of a connected pipe.
            let _ = unsafe { GetNamedPipeClientProcessId(h, &mut client) };
            if client != pid {
                return Err("a process other than the agent opened its pipe".into());
            }
        }
        // SAFETY: the handles are connected pipes this function created;
        // each File takes ownership of one.
        let (out, input) = unsafe {
            (
                File::from_raw_handle(down.0 as _),
                File::from_raw_handle(up.0 as _),
            )
        };
        link.attach(Box::new(input), Box::new(out));
        Ok(child)
    }

    /// Sixteen random bytes in hex, for the pipes' names.
    fn random_tag() -> String {
        let mut b = [0u8; 16];
        let _ = getrandom::fill(&mut b);
        b.iter().map(|x| format!("{x:02x}")).collect()
    }

    /// Creates one end of a SYSTEM-only pipe, refusing a name that exists.
    fn pipe(name: &str, access: u32) -> Result<HANDLE, String> {
        let mut sd = PSECURITY_DESCRIPTOR::default();
        // SAFETY: parses a constant SDDL string into `sd`, freed below.
        unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                &HSTRING::from(PIPE_SDDL),
                SDDL_REVISION_1,
                &mut sd,
                None,
            )
        }
        .map_err(|e| format!("pipe security: {e}"))?;
        let sa = SECURITY_ATTRIBUTES {
            nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: sd.0,
            bInheritHandle: false.into(),
        };
        // SAFETY: creates a pipe with the security attributes above, which
        // outlive the call.
        let h = unsafe {
            CreateNamedPipeW(
                &HSTRING::from(name),
                windows::Win32::Storage::FileSystem::FILE_FLAGS_AND_ATTRIBUTES(
                    access | FILE_FLAG_FIRST_PIPE_INSTANCE.0,
                ),
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
                1,
                1 << 20,
                1 << 20,
                0,
                Some(&sa),
            )
        };
        // SAFETY: frees the descriptor allocated above.
        unsafe {
            let _ = LocalFree(Some(HLOCAL(sd.0)));
        }
        if h.is_invalid() {
            return Err(format!("pipe {name}: {}", std::io::Error::last_os_error()));
        }
        Ok(h)
    }

    /// Starts `cmd` as SYSTEM in `session` on its interactive desktop.
    /// Returns: the process handle and id.
    fn spawn_in_session(exe: &Path, cmd: &str, session: u32) -> Result<(HANDLE, u32), String> {
        // SAFETY: duplicates this process's token, moves the copy to the
        // console session and starts a process with it; every handle is
        // closed below or returned.
        unsafe {
            let mut token = HANDLE::default();
            OpenProcessToken(GetCurrentProcess(), TOKEN_ALL_ACCESS, &mut token)
                .map_err(|e| format!("token: {e}"))?;
            let mut dup = HANDLE::default();
            let r = DuplicateTokenEx(
                token,
                TOKEN_ALL_ACCESS,
                None,
                SecurityIdentification,
                TokenPrimary,
                &mut dup,
            );
            let _ = CloseHandle(token);
            r.map_err(|e| format!("token: {e}"))?;
            let r = SetTokenInformation(
                dup,
                TokenSessionId,
                &session as *const u32 as *const _,
                std::mem::size_of::<u32>() as u32,
            );
            if let Err(e) = r {
                let _ = CloseHandle(dup);
                return Err(format!("session: {e}"));
            }
            let mut desktop: Vec<u16> = "winsta0\\default\0".encode_utf16().collect();
            let si = STARTUPINFOW {
                cb: std::mem::size_of::<STARTUPINFOW>() as u32,
                lpDesktop: PWSTR(desktop.as_mut_ptr()),
                ..Default::default()
            };
            let mut line: Vec<u16> = cmd.encode_utf16().chain(Some(0)).collect();
            let app = HSTRING::from(exe.as_os_str());
            let mut pi = PROCESS_INFORMATION::default();
            let r = CreateProcessAsUserW(
                Some(dup),
                PCWSTR(app.as_ptr()),
                Some(PWSTR(line.as_mut_ptr())),
                None,
                None,
                false,
                CREATE_NO_WINDOW | CREATE_UNICODE_ENVIRONMENT,
                None,
                None,
                &si,
                &mut pi,
            );
            let _ = CloseHandle(dup);
            r.map_err(|e| format!("start: {e}"))?;
            let _ = CloseHandle(pi.hThread);
            Ok((pi.hProcess, pi.dwProcessId))
        }
    }

    /// The agent's entry: opens the helper's pipes, checks that their server
    /// is the helper that started it, and serves until the helper goes.
    pub fn run_agent(down: &str, up: &str, helper_pid: u32) -> Result<(), String> {
        let open = |name: &str, write: bool| {
            std::fs::OpenOptions::new()
                .read(!write)
                .write(write)
                .open(name)
                .map_err(|e| format!("{name}: {e}"))
        };
        let input = open(down, false)?;
        let out = open(up, true)?;
        for f in [&input, &out] {
            let mut server = 0u32;
            use std::os::windows::io::AsRawHandle;
            // SAFETY: a plain query of an open pipe handle.
            let _ =
                unsafe { GetNamedPipeServerProcessId(HANDLE(f.as_raw_handle() as _), &mut server) };
            if server != helper_pid {
                return Err("the pipe does not belong to the helper".into());
            }
        }
        serve(Box::new(input), Box::new(out));
        Ok(())
    }

    thread_local! {
        /// The desktop this thread was moved to last.
        static DESKTOP: Cell<isize> = const { Cell::new(0) };
    }

    /// Moves this thread to the input desktop when it changed.
    pub fn follow_input_desktop() {
        // SAFETY: opens the input desktop, compares its name with the one
        // this thread is on and switches when it differs; the handle not
        // kept is closed.
        unsafe {
            let Ok(input) = OpenInputDesktop(
                DESKTOP_CONTROL_FLAGS(0),
                false,
                DESKTOP_ACCESS_FLAGS(0x01FF), // GENERIC_ALL for desktops
            ) else {
                return;
            };
            let current = DESKTOP.with(Cell::get);
            if current != 0 && name(HDESK(current as _)) == name(input) {
                let _ = CloseDesktop(input);
                return;
            }
            if SetThreadDesktop(input).is_ok() {
                if current != 0 {
                    let _ = CloseDesktop(HDESK(current as _));
                }
                DESKTOP.with(|d| d.set(input.0 as isize));
            } else {
                let _ = CloseDesktop(input);
            }
        }
    }

    /// A desktop's name ("Default", "Winlogon", …).
    fn name(d: HDESK) -> String {
        let mut buf = [0u16; 64];
        let mut need = 0u32;
        // SAFETY: fills `buf`, whose size in bytes is passed.
        let ok = unsafe {
            GetUserObjectInformationW(
                HANDLE(d.0),
                UOI_NAME,
                Some(buf.as_mut_ptr().cast()),
                std::mem::size_of_val(&buf) as u32,
                Some(&mut need),
            )
        }
        .is_ok();
        if !ok {
            return String::new();
        }
        let n = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
        String::from_utf16_lossy(&buf[..n])
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A helper's link and an agent serving it over in-memory pipes.
    fn linked() -> Arc<Link> {
        let (down_r, down_w) = std::io::pipe().unwrap();
        let (up_r, up_w) = std::io::pipe().unwrap();
        std::thread::spawn(move || serve(Box::new(down_r), Box::new(up_w)));
        let link = Arc::new(Link::new());
        link.attach(Box::new(up_r), Box::new(down_w));
        link
    }

    #[test]
    fn messages_keep_their_kind_and_body_and_refuse_bad_lengths() {
        let mut buf = Vec::new();
        write_message(&mut buf, VIDEO, &[b"ab", b"cd"]).unwrap();
        let (kind, body) = read_message(&mut buf.as_slice()).unwrap();
        assert_eq!((kind, body.as_slice()), (VIDEO, &b"abcd"[..]));
        for len in [0u32, (MAX_MESSAGE + 1) as u32] {
            let bad = len.to_le_bytes();
            assert!(read_message(&mut &bad[..]).is_err(), "length {len}");
        }
    }

    #[test]
    fn video_streams_through_the_agent_until_the_session_stops_it() {
        let link = linked();
        let (tx, mut rx) = mpsc::channel(2);
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let targets = Arc::new(Targets {
            fps: AtomicU32::new(30),
            bitrate: AtomicU32::new(1_000_000),
        });
        // A machine without a display shows the test pattern; with one, the
        // screen. Either way frames arrive through the agent.
        let (start, _) = link
            .open_video(None, "h264", tx, key, targets, stop.clone())
            .expect("the agent opens a stream");
        assert!(start.width > 0 && start.height > 0);
        let frame = rx.blocking_recv().expect("a frame through the agent");
        assert!(!frame.2.is_empty());
        stop.store(true, Ordering::Relaxed);
        // The stream ends: the link forgets it and its frames stop.
        let t0 = Instant::now();
        while !link.streams.lock().unwrap().is_empty() {
            assert!(t0.elapsed() < Duration::from_secs(5), "stream not closed");
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    #[test]
    fn calls_fail_cleanly_without_an_agent_and_after_it_left() {
        let link = Arc::new(Link::new());
        assert_eq!(link.displays(), Vec::new());
        assert!(!link.inject(1, &Event::Text { text: "x".into() }, None));
        assert!(link.desktop().is_err());
        // An agent that goes away mid-session: calls answer at once.
        let (down_r, down_w) = std::io::pipe().unwrap();
        let (up_r, up_w) = std::io::pipe().unwrap();
        let link = Arc::new(Link::new());
        link.attach(Box::new(up_r), Box::new(down_w));
        drop((down_r, up_w));
        let t0 = Instant::now();
        while link.connected_for_tests() {
            assert!(t0.elapsed() < Duration::from_secs(5));
            std::thread::sleep(Duration::from_millis(10));
        }
        assert!(link.pointer(None).is_none());
    }

    #[test]
    fn a_remote_desktop_without_its_agent_reads_as_the_user_taking_over() {
        use crate::ai::desktop::Desktop;
        let link = Arc::new(Link::new());
        let mut d = RemoteDesktop { link, id: 1 };
        let l = d.local_input();
        assert!(l.press.is_some(), "a task never acts without the desktop");
        assert!(d.stop_requested());
    }

    impl Link {
        fn connected_for_tests(&self) -> bool {
            self.out.lock().unwrap().is_some()
        }
    }
}
