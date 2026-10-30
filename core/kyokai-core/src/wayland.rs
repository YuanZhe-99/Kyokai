//! Linux on Wayland (doc/en-us/computer-use.md, "Linux"): the screen through
//! the ScreenCast portal and PipeWire, input through the RemoteDesktop
//! portal, and the host user's own input from the input devices.
//!
//! One portal session serves the whole engine: opened when a capture, an
//! injector or the display list first needs it (the desktop asks the host
//! user then, once: its restore token is kept), closed IDLE_CLOSE after the
//! last use. Wayland never tells a program where other programs' windows
//! are, so Kyokai's own windows cannot be blacked out or kept from remote
//! input here: the GUI hides them while the host is controlled and says
//! when one shows anyway (`set_kyokai_shown`), and then the picture holds
//! still and no remote or AI input is injected.

use crate::input::{Area, Event, LocalInput, Mods, PointerAction};
use crate::video::{Display, Got, Take, order};
use ashpd::desktop::PersistMode;
use ashpd::desktop::remote_desktop::{
    Axis, DeviceType, KeyState, RemoteDesktop, SelectDevicesOptions,
};
use ashpd::desktop::screencast::{CursorMode, Screencast, SelectSourcesOptions, SourceType};
use pipewire as pw;
use std::collections::BTreeSet;
use std::os::fd::{AsFd, OwnedFd};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

/// How long the portal session stays open after its last use, so that a
/// stream opened again (a recovery step, another display) does not ask
/// again or flash the desktop's sharing indicator.
const IDLE_CLOSE: Duration = Duration::from_secs(10);
/// Longest the desktop's consent dialog and the stream's first format are
/// waited for.
const OPEN_WAIT: Duration = Duration::from_secs(120);
const FORMAT_WAIT: Duration = Duration::from_secs(5);

/// Whether one of Kyokai's windows shows on this Wayland desktop (the GUI
/// says so); while it does the picture holds still and remote and AI input
/// is not injected, unless the host user lets capture show Kyokai.
static KYOKAI_SHOWN: AtomicBool = AtomicBool::new(false);

/// Sets whether one of Kyokai's windows shows.
pub fn set_kyokai_shown(shown: bool) {
    KYOKAI_SHOWN.store(shown, Ordering::Relaxed);
}

/// Whether captures and input pause for a Kyokai window: one shows and the
/// host user hides Kyokai from capture.
fn paused() -> bool {
    KYOKAI_SHOWN.load(Ordering::Relaxed) && !crate::video::show_kyokai()
}

/// Where the restore token is kept (the engine's state folder), set as the
/// core starts.
static TOKEN_FILE: Mutex<Option<PathBuf>> = Mutex::new(None);

/// Keeps the portal's restore token in `state_dir`.
pub fn set_state_dir(state_dir: &std::path::Path) {
    *TOKEN_FILE.lock().unwrap_or_else(|e| e.into_inner()) = Some(state_dir.join("portal-token"));
}

fn token() -> Option<String> {
    let path = TOKEN_FILE.lock().ok()?.clone()?;
    std::fs::read_to_string(path).ok().filter(|t| !t.is_empty())
}

fn save_token(t: Option<&str>) {
    let Some(path) = TOKEN_FILE.lock().ok().and_then(|p| p.clone()) else {
        return;
    };
    match t {
        Some(t) => {
            let _ = crate::store::write_atomic(&path, t.as_bytes());
        }
        None => {
            let _ = std::fs::remove_file(path);
        }
    }
}

/// A shared display of the portal session.
#[derive(Debug, Clone, Copy, PartialEq)]
struct Shared {
    node: u32,
    /// Its place and size in the desktop's logical coordinates.
    display: Display,
}

/// What the portal thread is asked to do.
enum Cmd {
    Motion { node: u32, x: f64, y: f64 },
    Button { code: i32, down: bool },
    Axis { vertical: bool, steps: i32 },
    Key { code: i32, down: bool },
    Keysym { sym: i32, down: bool },
    Close,
}

/// An open portal session.
struct Portal {
    cmds: tokio::sync::mpsc::UnboundedSender<Cmd>,
    streams: Vec<Shared>,
    /// The PipeWire remote of the session's streams.
    remote: OwnedFd,
    /// Whether the session injects input (the RemoteDesktop portal).
    input: bool,
}

/// The portal session, how many use it and since when none has.
#[derive(Default)]
struct State {
    portal: Option<Arc<Portal>>,
    users: usize,
    idle_since: Option<Instant>,
}

static STATE: Mutex<State> = Mutex::new(State {
    portal: None,
    users: 0,
    idle_since: None,
});

/// A use of the portal session; dropping the last one closes it after
/// IDLE_CLOSE.
pub(crate) struct Use(Arc<Portal>);

impl Use {
    /// Uses the open session, or opens one (the desktop may ask the host
    /// user first).
    fn get() -> Result<Use, String> {
        let mut s = STATE.lock().unwrap_or_else(|e| e.into_inner());
        if s.portal.is_none() {
            s.portal = Some(Arc::new(open()?));
        }
        s.users += 1;
        s.idle_since = None;
        Ok(Use(s.portal.clone().expect("opened above")))
    }
}

impl Drop for Use {
    fn drop(&mut self) {
        let mut s = STATE.lock().unwrap_or_else(|e| e.into_inner());
        s.users -= 1;
        if s.users > 0 {
            return;
        }
        let since = Instant::now();
        s.idle_since = Some(since);
        std::thread::spawn(move || {
            std::thread::sleep(IDLE_CLOSE);
            let mut s = STATE.lock().unwrap_or_else(|e| e.into_inner());
            if s.users == 0
                && s.idle_since == Some(since)
                && let Some(p) = s.portal.take()
            {
                let _ = p.cmds.send(Cmd::Close);
            }
        });
    }
}

/// Opens a portal session on a thread of its own: RemoteDesktop with
/// ScreenCast where the desktop has both, else ScreenCast alone (no input).
/// Returns: why not when there is no portal or the host user declined.
fn open() -> Result<Portal, String> {
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let rt = match tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
        {
            Ok(rt) => rt,
            Err(e) => {
                let _ = tx.send(Err(e.to_string()));
                return;
            }
        };
        rt.block_on(serve(tx));
    });
    rx.recv_timeout(OPEN_WAIT)
        .map_err(|_| "the desktop did not answer the screen sharing request".to_string())?
}

/// The app id Kyokai registers with the portals (its desktop file's name).
const APP_ID: &str = "com.yuanzhe.kyokai";

/// The sources a session shares: every monitor, the pointer drawn in.
fn sources() -> SelectSourcesOptions {
    SelectSourcesOptions::default()
        .set_cursor_mode(CursorMode::Embedded)
        .set_sources(ashpd::enumflags2::BitFlags::from_flag(SourceType::Monitor))
        .set_multiple(true)
}

/// Starts a RemoteDesktop session sharing the screen too.
async fn start_remote(
    rd: &RemoteDesktop,
    sc: &Screencast,
    persist: Option<&str>,
) -> ashpd::Result<(
    ashpd::desktop::Session<RemoteDesktop>,
    ashpd::desktop::remote_desktop::SelectedDevices,
    OwnedFd,
)> {
    let session = rd.create_session(Default::default()).await?;
    let mut devices =
        SelectDevicesOptions::default().set_devices(DeviceType::Keyboard | DeviceType::Pointer);
    if persist.is_some() {
        devices = devices
            .set_persist_mode(PersistMode::ExplicitlyRevoked)
            .set_restore_token(persist.filter(|t| !t.is_empty()));
    }
    rd.select_devices(&session, devices).await?;
    sc.select_sources(&session, sources()).await?;
    let started = rd
        .start(&session, None, Default::default())
        .await?
        .response()?;
    let remote = sc
        .open_pipe_wire_remote(&session, Default::default())
        .await?;
    Ok((session, started, remote))
}

/// Starts a ScreenCast session alone (no input).
async fn start_cast(
    sc: &Screencast,
    persist: Option<&str>,
) -> ashpd::Result<(
    ashpd::desktop::Session<Screencast>,
    ashpd::desktop::screencast::Streams,
    OwnedFd,
)> {
    let session = sc.create_session(Default::default()).await?;
    let mut options = sources();
    if persist.is_some() {
        options = options
            .set_persist_mode(PersistMode::ExplicitlyRevoked)
            .set_restore_token(persist.filter(|t| !t.is_empty()));
    }
    sc.select_sources(&session, options).await?;
    let started = sc
        .start(&session, None, Default::default())
        .await?
        .response()?;
    let remote = sc
        .open_pipe_wire_remote(&session, Default::default())
        .await?;
    Ok((session, started, remote))
}

/// Whether the host user declined (the session is not asked for again).
fn declined(e: &ashpd::Error) -> bool {
    matches!(
        e,
        ashpd::Error::Response(ashpd::desktop::ResponseError::Cancelled)
    )
}

/// The portal thread: opens a session, reports it, then runs commands until
/// told to close. Tries RemoteDesktop and then ScreenCast alone, each first
/// kept (a restore token, so the host user is asked once) and then not
/// (portals that keep nothing for a program without an app id refuse
/// that); the host user declining ends it.
async fn serve(ready: std::sync::mpsc::Sender<Result<Portal, String>>) {
    let (cmds, mut rx) = tokio::sync::mpsc::unbounded_channel();
    // Portals before 1.19 do not know the registry: nothing to do then.
    if let Ok(id) = ashpd::AppID::try_from(APP_ID) {
        let _ = ashpd::register_host_app(id).await;
    }
    let Ok(sc) = Screencast::new().await else {
        let _ = ready.send(Err("this desktop has no screen sharing portal".into()));
        return;
    };
    let saved = token().unwrap_or_default();
    let mut why = String::from("this desktop has no screen sharing portal");
    if let Ok(rd) = RemoteDesktop::new().await {
        for persist in [Some(saved.as_str()), None] {
            match start_remote(&rd, &sc, persist).await {
                Ok((session, started, remote)) => {
                    if persist.is_some() {
                        save_token(started.restore_token());
                    }
                    let _ = ready.send(Ok(Portal {
                        cmds,
                        streams: shared(started.streams()),
                        remote,
                        input: started.devices().contains(DeviceType::Pointer),
                    }));
                    while let Some(cmd) = rx.recv().await {
                        let r = match cmd {
                            Cmd::Close => break,
                            Cmd::Motion { node, x, y } => {
                                rd.notify_pointer_motion_absolute(
                                    &session,
                                    node,
                                    x,
                                    y,
                                    Default::default(),
                                )
                                .await
                            }
                            Cmd::Button { code, down } => {
                                rd.notify_pointer_button(
                                    &session,
                                    code,
                                    state(down),
                                    Default::default(),
                                )
                                .await
                            }
                            Cmd::Axis { vertical, steps } => {
                                let axis = if vertical {
                                    Axis::Vertical
                                } else {
                                    Axis::Horizontal
                                };
                                rd.notify_pointer_axis_discrete(
                                    &session,
                                    axis,
                                    steps,
                                    Default::default(),
                                )
                                .await
                            }
                            Cmd::Key { code, down } => {
                                rd.notify_keyboard_keycode(
                                    &session,
                                    code,
                                    state(down),
                                    Default::default(),
                                )
                                .await
                            }
                            Cmd::Keysym { sym, down } => {
                                rd.notify_keyboard_keysym(
                                    &session,
                                    sym,
                                    state(down),
                                    Default::default(),
                                )
                                .await
                            }
                        };
                        if let Err(e) = r {
                            log::info!("wayland: input through the portal failed: {e}");
                        }
                    }
                    let _ = session.close().await;
                    return;
                }
                Err(e) if declined(&e) => {
                    let _ = ready.send(Err("the host user declined screen sharing".into()));
                    return;
                }
                Err(e) => {
                    log::info!("wayland: remote desktop (kept: {}): {e}", persist.is_some());
                    why = format!("screen sharing: {e}");
                }
            }
        }
    }
    for persist in [Some(saved.as_str()), None] {
        match start_cast(&sc, persist).await {
            Ok((session, started, remote)) => {
                if persist.is_some() {
                    save_token(started.restore_token());
                }
                let _ = ready.send(Ok(Portal {
                    cmds,
                    streams: shared(started.streams()),
                    remote,
                    input: false,
                }));
                while let Some(cmd) = rx.recv().await {
                    if matches!(cmd, Cmd::Close) {
                        break;
                    }
                }
                let _ = session.close().await;
                return;
            }
            Err(e) if declined(&e) => {
                let _ = ready.send(Err("the host user declined screen sharing".into()));
                return;
            }
            Err(e) => {
                log::info!("wayland: screen cast (kept: {}): {e}", persist.is_some());
                why = format!("screen sharing: {e}");
            }
        }
    }
    let _ = ready.send(Err(why));
}

fn state(down: bool) -> KeyState {
    if down {
        KeyState::Pressed
    } else {
        KeyState::Released
    }
}

/// The portal's streams as displays: the first is the primary (the portal
/// does not say), the others in the usual order.
fn shared(streams: &[ashpd::desktop::screencast::Stream]) -> Vec<Shared> {
    let mut out: Vec<Shared> = streams
        .iter()
        .enumerate()
        .filter_map(|(i, s)| {
            let (w, h) = s.size()?;
            let (x, y) = s.position().unwrap_or((0, 0));
            Some(Shared {
                node: s.pipe_wire_node_id(),
                display: Display {
                    x,
                    y,
                    width: w.max(1) as u32,
                    height: h.max(1) as u32,
                    primary: i == 0,
                },
            })
        })
        .collect();
    out.sort_by_key(|s| (!s.display.primary, s.display.x, s.display.y));
    out
}

/// The displays the host user shares: the primary first, the others left to
/// right.
pub fn displays() -> Vec<Display> {
    Use::get()
        .map(|u| {
            let mut list: Vec<Display> = u.0.streams.iter().map(|s| s.display).collect();
            order(&mut list);
            list
        })
        .unwrap_or_default()
}

/// The newest picture of a stream.
#[derive(Default)]
struct Latest {
    seq: u64,
    bytes: Vec<u8>,
    width: usize,
    height: usize,
    stopped: bool,
}

/// What a stream's PipeWire thread and its reader share.
#[derive(Default)]
struct Frames {
    latest: Mutex<Latest>,
    ready: Condvar,
}

/// A shared display through its PipeWire stream, the pointer drawn in.
pub struct Capture {
    _use: Use,
    frames: Arc<Frames>,
    seen: u64,
    quit: pw::channel::Sender<()>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Capture {
    /// Opens the stream of `display` and waits for its first picture's
    /// size.
    pub fn open(display: Display) -> Result<(Capture, u32, u32), String> {
        let u = Use::get()?;
        let node =
            u.0.streams
                .iter()
                .find(|s| (s.display.x, s.display.y) == (display.x, display.y))
                .or_else(|| u.0.streams.first())
                .ok_or("no display is shared")?
                .node;
        let remote =
            u.0.remote
                .as_fd()
                .try_clone_to_owned()
                .map_err(|e| e.to_string())?;
        let frames = Arc::new(Frames::default());
        let (quit, quit_rx) = pw::channel::channel();
        let shared = frames.clone();
        let thread = std::thread::spawn(move || {
            if let Err(e) = stream(remote, node, &shared, quit_rx) {
                log::info!("wayland: the PipeWire stream ended: {e}");
            }
            let mut l = shared.latest.lock().unwrap_or_else(|e| e.into_inner());
            l.stopped = true;
            shared.ready.notify_all();
        });
        let mut cap = Capture {
            _use: u,
            frames,
            seen: 0,
            quit,
            thread: Some(thread),
        };
        let (w, h) = cap.first_size()?;
        Ok((cap, w, h))
    }

    /// Waits for the stream's first picture and gives its size.
    fn first_size(&mut self) -> Result<(u32, u32), String> {
        let l = self.frames.latest.lock().map_err(|_| "the stream failed")?;
        let (l, _) = self
            .frames
            .ready
            .wait_timeout_while(l, FORMAT_WAIT, |l| l.seq == 0 && !l.stopped)
            .map_err(|_| "the stream failed")?;
        if l.seq == 0 {
            return Err("the shared screen gave no picture".into());
        }
        Ok((l.width as u32, l.height as u32))
    }

    /// Waits up to `timeout` for a picture newer than the last one taken.
    pub fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
        let Ok(l) = self.frames.latest.lock() else {
            return Got::Lost;
        };
        let seen = self.seen;
        let Ok((l, _)) = self
            .frames
            .ready
            .wait_timeout_while(l, timeout, |l| l.seq == seen && !l.stopped)
        else {
            return Got::Lost;
        };
        if l.stopped {
            return Got::Lost;
        }
        if l.seq == seen || paused() {
            return Got::Nothing;
        }
        self.seen = l.seq;
        take(&l.bytes, l.width, l.height, l.width * 4);
        Got::Picture
    }

    /// The newest picture at once, waiting for one at most `timeout`.
    pub fn grab(&mut self, timeout: Duration) -> Result<crate::ai::desktop::Frame, String> {
        let mut out = None;
        self.seen = 0;
        if self.frame(timeout, &mut |bgra, w, h, _| {
            out = Some(crate::ai::desktop::Frame {
                bgra: bgra.to_vec(),
                width: w as u32,
                height: h as u32,
            })
        }) != Got::Picture
        {
            return Err("the shared screen gave no picture".into());
        }
        out.ok_or_else(|| "the shared screen gave no picture".into())
    }
}

impl Drop for Capture {
    fn drop(&mut self) {
        let _ = self.quit.send(());
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

/// The pixel formats asked of the compositor, all four bytes a pixel.
const FORMATS: [pw::spa::param::video::VideoFormat; 4] = [
    pw::spa::param::video::VideoFormat::BGRx,
    pw::spa::param::video::VideoFormat::BGRA,
    pw::spa::param::video::VideoFormat::RGBx,
    pw::spa::param::video::VideoFormat::RGBA,
];

/// Runs one PipeWire stream until `quit`: each picture is copied, top-down
/// BGRA, into `frames`.
fn stream(
    remote: OwnedFd,
    node: u32,
    frames: &Arc<Frames>,
    quit: pw::channel::Receiver<()>,
) -> Result<(), String> {
    use pw::spa;
    pw::init();
    let mainloop = pw::main_loop::MainLoopRc::new(None).map_err(|e| e.to_string())?;
    let context = pw::context::ContextRc::new(&mainloop, None).map_err(|e| e.to_string())?;
    let core = context
        .connect_fd_rc(remote, None)
        .map_err(|e| e.to_string())?;
    let stream = pw::stream::StreamBox::new(
        &core,
        "kyokai-capture",
        pw::properties::properties! {
            *pw::keys::MEDIA_TYPE => "Video",
            *pw::keys::MEDIA_CATEGORY => "Capture",
            *pw::keys::MEDIA_ROLE => "Screen",
        },
    )
    .map_err(|e| e.to_string())?;
    let ended = mainloop.clone();
    let _quit = quit.attach(mainloop.loop_(), move |()| ended.quit());
    let format: Arc<Mutex<spa::param::video::VideoInfoRaw>> = Arc::default();
    let parsed = format.clone();
    let failed = mainloop.clone();
    let shared = frames.clone();
    let _listener = stream
        .add_local_listener::<()>()
        .state_changed(move |_, _, _, new| {
            if matches!(
                new,
                pw::stream::StreamState::Error(_) | pw::stream::StreamState::Unconnected
            ) {
                failed.quit();
            }
        })
        .param_changed(move |_, _, id, param| {
            let Some(param) = param else {
                return;
            };
            if id != spa::param::ParamType::Format.as_raw() {
                return;
            }
            if let Ok(mut f) = parsed.lock() {
                let _ = f.parse(param);
            }
        })
        .process(move |stream, _| {
            let Some(mut buffer) = stream.dequeue_buffer() else {
                return;
            };
            let Ok(info) = format.lock().map(|f| *f) else {
                return;
            };
            let datas = buffer.datas_mut();
            let Some(data) = datas.first_mut() else {
                return;
            };
            let (offset, stride) = (data.chunk().offset() as usize, data.chunk().stride());
            let (w, h) = (info.size().width as usize, info.size().height as usize);
            let swap = matches!(
                info.format(),
                spa::param::video::VideoFormat::RGBx | spa::param::video::VideoFormat::RGBA
            );
            let Some(bytes) = data.data() else {
                return;
            };
            let stride = if stride > 0 { stride as usize } else { w * 4 };
            if w == 0 || h == 0 || bytes.len() < offset + stride * (h - 1) + w * 4 {
                return;
            }
            let mut l = shared.latest.lock().unwrap_or_else(|e| e.into_inner());
            l.bytes.resize(w * h * 4, 0);
            copy_rows(&bytes[offset..], stride, &mut l.bytes, w, h, swap);
            (l.width, l.height) = (w, h);
            l.seq += 1;
            shared.ready.notify_all();
        })
        .register()
        .map_err(|e| e.to_string())?;
    let obj = spa::pod::object!(
        spa::utils::SpaTypes::ObjectParamFormat,
        spa::param::ParamType::EnumFormat,
        spa::pod::property!(
            spa::param::format::FormatProperties::MediaType,
            Id,
            spa::param::format::MediaType::Video
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::MediaSubtype,
            Id,
            spa::param::format::MediaSubtype::Raw
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::VideoFormat,
            Choice,
            Enum,
            Id,
            FORMATS[0],
            FORMATS[0],
            FORMATS[1],
            FORMATS[2],
            FORMATS[3],
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::VideoSize,
            Choice,
            Range,
            Rectangle,
            spa::utils::Rectangle {
                width: 1920,
                height: 1080
            },
            spa::utils::Rectangle {
                width: 1,
                height: 1
            },
            spa::utils::Rectangle {
                width: 8192,
                height: 8192
            }
        ),
        spa::pod::property!(
            spa::param::format::FormatProperties::VideoFramerate,
            Choice,
            Range,
            Fraction,
            spa::utils::Fraction { num: 30, denom: 1 },
            spa::utils::Fraction { num: 0, denom: 1 },
            spa::utils::Fraction {
                num: crate::video::MAX_FPS,
                denom: 1
            }
        ),
    );
    let values: Vec<u8> = spa::pod::serialize::PodSerializer::serialize(
        std::io::Cursor::new(Vec::new()),
        &spa::pod::Value::Object(obj),
    )
    .map_err(|e| format!("{e:?}"))?
    .0
    .into_inner();
    let mut params = [spa::pod::Pod::from_bytes(&values).ok_or("bad format")?];
    stream
        .connect(
            spa::utils::Direction::Input,
            Some(node),
            pw::stream::StreamFlags::AUTOCONNECT | pw::stream::StreamFlags::MAP_BUFFERS,
            &mut params,
        )
        .map_err(|e| e.to_string())?;
    mainloop.run();
    Ok(())
}

/// Copies `h` rows of `w` four-byte pixels from a buffer with `stride` bytes
/// a row into a tight BGRA picture, swapping red and blue when the source is
/// RGB-ordered.
pub(crate) fn copy_rows(src: &[u8], stride: usize, dst: &mut [u8], w: usize, h: usize, swap: bool) {
    for (row, out) in dst.chunks_exact_mut(w * 4).take(h).enumerate() {
        let line = &src[row * stride..row * stride + w * 4];
        if swap {
            let (outs, _) = out.as_chunks_mut::<4>();
            let (ins, _) = line.as_chunks::<4>();
            for (o, i) in outs.iter_mut().zip(ins) {
                *o = [i[2], i[1], i[0], i[3]];
            }
        } else {
            out.copy_from_slice(line);
        }
    }
}

/// The evdev codes of the mouse buttons (left, right, middle).
const BUTTONS: [i32; 3] = [0x110, 0x111, 0x112];

/// The host's input on Wayland through the RemoteDesktop portal: nothing
/// while a Kyokai window shows (unless a person's input may reach it), and
/// whatever is held released at the end.
pub struct Injector {
    u: Option<Use>,
    area: Option<Area>,
    keys: BTreeSet<u8>,
    buttons: BTreeSet<u8>,
    scroll: (f32, f32),
    /// Where the pointer was last put, as fractions of the primary display.
    pub(crate) at: Option<(f32, f32)>,
}

impl Default for Injector {
    fn default() -> Self {
        Injector {
            u: Use::get().ok().filter(|u| u.0.input),
            area: None,
            keys: BTreeSet::new(),
            buttons: BTreeSet::new(),
            scroll: (0.0, 0.0),
            at: None,
        }
    }
}

impl Injector {
    /// Sets the display pointer positions refer to (None: the primary).
    pub fn set_area(&mut self, area: Option<Area>) {
        self.area = area;
    }

    fn send(&self, cmd: Cmd) {
        if let Some(u) = &self.u {
            let _ = u.0.cmds.send(cmd);
        }
    }

    fn key(&mut self, code: u8, down: bool) {
        if down {
            self.keys.insert(code);
        } else {
            self.keys.remove(&code);
        }
        self.send(Cmd::Key {
            code: code.into(),
            down,
        });
    }

    fn sync_mods(&mut self, mods: &Mods) {
        for (code, wanted) in crate::linux::MODIFIERS {
            let held = self.keys.contains(&code);
            if wanted(mods) != held {
                self.key(code, !held);
            }
        }
    }

    /// Injects an event, unless a Kyokai window shows and `kyokai_ok` is
    /// false (releases always pass: they start nothing).
    /// Returns: whether it was injected.
    pub fn inject(&mut self, e: &Event, kyokai_ok: bool) -> bool {
        let Some(u) = &self.u else {
            return false;
        };
        let release = e.guard() == crate::input::Guard::None;
        if !e.valid() || (!kyokai_ok && !release && KYOKAI_SHOWN.load(Ordering::Relaxed)) {
            return false;
        }
        match e {
            Event::Pointer {
                action,
                button,
                x,
                y,
            } => {
                let streams = &u.0.streams;
                let target = self
                    .area
                    .and_then(|a| {
                        streams
                            .iter()
                            .find(|s| (s.display.x, s.display.y) == (a.x, a.y))
                    })
                    .or_else(|| streams.first());
                let Some(s) = target else {
                    return false;
                };
                let (fx, fy) = (x.clamp(0.0, 1.0), y.clamp(0.0, 1.0));
                if s.display.primary {
                    self.at = Some((fx, fy));
                }
                self.send(Cmd::Motion {
                    node: s.node,
                    x: f64::from(fx) * f64::from(s.display.width.max(1) - 1),
                    y: f64::from(fy) * f64::from(s.display.height.max(1) - 1),
                });
                let code = BUTTONS[*button as usize];
                match action {
                    PointerAction::Move => {}
                    PointerAction::Down => {
                        self.send(Cmd::Button { code, down: true });
                        self.buttons.insert(*button);
                    }
                    PointerAction::Up => {
                        self.send(Cmd::Button { code, down: false });
                        self.buttons.remove(button);
                    }
                }
            }
            Event::Scroll { dx, dy } => {
                self.scroll.0 += dx;
                self.scroll.1 += dy;
                let (lx, ly) = (self.scroll.0.trunc(), self.scroll.1.trunc());
                self.scroll.0 -= lx;
                self.scroll.1 -= ly;
                // A positive dy turns the wheel toward the user (down).
                if ly != 0.0 {
                    self.send(Cmd::Axis {
                        vertical: true,
                        steps: ly as i32,
                    });
                }
                if lx != 0.0 {
                    self.send(Cmd::Axis {
                        vertical: false,
                        steps: lx as i32,
                    });
                }
            }
            Event::Key { key, down, .. } => {
                let Some(code) = crate::linux::keycode(key) else {
                    return false;
                };
                let held = e.mods_held().unwrap_or_default();
                if *down {
                    self.sync_mods(&held);
                    self.key(code, true);
                } else {
                    self.key(code, false);
                    self.sync_mods(&held);
                }
            }
            Event::Text { text } => {
                self.sync_mods(&e.mods_held().unwrap_or_default());
                for c in text.chars() {
                    let sym = crate::linux::keysym(c) as i32;
                    self.send(Cmd::Keysym { sym, down: true });
                    self.send(Cmd::Keysym { sym, down: false });
                }
            }
            Event::Scan { code, down, .. } => {
                // The compositor keeps its own lock state; the key alone
                // goes, by its place.
                let Some(key) = crate::linux::scan_evdev(*code) else {
                    return false;
                };
                self.key(key, *down);
            }
        }
        true
    }

    /// Releases every key and button this session holds.
    pub fn release_all(&mut self) {
        let keys: Vec<u8> = self.keys.iter().rev().copied().collect();
        for k in keys {
            self.key(k, false);
        }
        for b in std::mem::take(&mut self.buttons) {
            self.send(Cmd::Button {
                code: BUTTONS[b as usize],
                down: false,
            });
        }
    }
}

impl Drop for Injector {
    fn drop(&mut self) {
        self.release_all();
    }
}

/// What the evdev watch's thread and its readers share.
#[derive(Default)]
struct Watched {
    press: Mutex<Option<Instant>>,
    moved: Mutex<Option<Instant>>,
    stop: AtomicBool,
    ended: AtomicBool,
}

/// Watches the host user's own keyboard and mouse straight from the input
/// devices (`/dev/input/event*`), which only the `input` group and root may
/// read: Wayland lets no program see input meant for others. Kyokai's own
/// input goes through the compositor and never shows there.
pub struct Evdev {
    watched: Arc<Watched>,
}

impl Evdev {
    /// Opens every input device it may read and watches them.
    /// Returns: an error when it may read none.
    pub fn start() -> Result<Evdev, String> {
        let mut fds = Vec::new();
        for entry in std::fs::read_dir("/dev/input")
            .map_err(|e| e.to_string())?
            .flatten()
        {
            let name = entry.file_name();
            if !name.to_string_lossy().starts_with("event") {
                continue;
            }
            let path = std::ffi::CString::new(entry.path().as_os_str().as_encoded_bytes())
                .map_err(|e| e.to_string())?;
            // SAFETY: a zero-terminated path; the descriptor is owned below.
            let fd = unsafe {
                libc::open(
                    path.as_ptr(),
                    libc::O_RDONLY | libc::O_NONBLOCK | libc::O_CLOEXEC,
                )
            };
            if fd >= 0 {
                // SAFETY: a descriptor just opened and owned by no one else.
                fds.push(unsafe { <OwnedFd as std::os::fd::FromRawFd>::from_raw_fd(fd) });
            }
        }
        if fds.is_empty() {
            return Err(
                "on Wayland, watching the host user's own input needs Kyokai Helper or the input group"
                    .into(),
            );
        }
        let watched = Arc::new(Watched::default());
        let shared = watched.clone();
        std::thread::spawn(move || watch_evdev(fds, &shared));
        Ok(Evdev { watched })
    }

    pub fn input(&self) -> LocalInput {
        LocalInput {
            press: self.watched.press.lock().ok().and_then(|p| *p),
            moved: self.watched.moved.lock().ok().and_then(|m| *m),
        }
    }

    pub fn stop_requested(&self) -> bool {
        self.watched.stop.swap(false, Ordering::Relaxed)
    }
}

impl Drop for Evdev {
    fn drop(&mut self) {
        self.watched.ended.store(true, Ordering::Release);
    }
}

/// The evdev event types the watch reads.
const EV_KEY: u16 = 1;
const EV_REL: u16 = 2;
const EV_ABS: u16 = 3;

/// One `struct input_event`: what the watch reads of it.
#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct Input {
    pub kind: u16,
    pub code: u16,
    pub value: i32,
}

/// Reads the events in a buffer of `struct input_event` (a 64-bit time,
/// then type, code and value).
pub(crate) fn parse_events(buf: &[u8]) -> Vec<Input> {
    const SIZE: usize = 24;
    buf.as_chunks::<SIZE>()
        .0
        .iter()
        .map(|e| Input {
            kind: u16::from_ne_bytes([e[16], e[17]]),
            code: u16::from_ne_bytes([e[18], e[19]]),
            value: i32::from_ne_bytes([e[20], e[21], e[22], e[23]]),
        })
        .collect()
}

/// The watch's loop: notes presses and moves until the watch drops.
fn watch_evdev(fds: Vec<OwnedFd>, watched: &Watched) {
    use std::os::fd::AsRawFd;
    let mut polls: Vec<libc::pollfd> = fds
        .iter()
        .map(|f| libc::pollfd {
            fd: f.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        })
        .collect();
    let mut held = BTreeSet::new();
    let mut buf = [0u8; 24 * 64];
    while !watched.ended.load(Ordering::Acquire) {
        // SAFETY: `polls` holds valid descriptors owned by `fds`.
        let n = unsafe { libc::poll(polls.as_mut_ptr(), polls.len() as libc::nfds_t, 200) };
        if n <= 0 {
            continue;
        }
        for p in &mut polls {
            if p.revents & libc::POLLIN == 0 {
                continue;
            }
            // SAFETY: reads into a buffer of its length from an owned
            // descriptor.
            let got = unsafe { libc::read(p.fd, buf.as_mut_ptr().cast(), buf.len()) };
            if got <= 0 {
                continue;
            }
            for e in parse_events(&buf[..got as usize]) {
                let slot = match e.kind {
                    EV_KEY if e.value != 2 => {
                        if e.code < 248
                            && crate::linux::stop_shortcut(
                                &mut held,
                                e.code as u8 + 8,
                                e.value == 1,
                            )
                        {
                            watched.stop.store(true, Ordering::Relaxed);
                        }
                        if e.value != 1 {
                            continue;
                        }
                        &watched.press
                    }
                    EV_REL | EV_ABS => &watched.moved,
                    _ => continue,
                };
                if let Ok(mut t) = slot.lock() {
                    *t = Some(Instant::now());
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rows_are_copied_tight_and_rgb_turned_into_bgr() {
        // Two rows of two pixels, with a stride of 12 (4 bytes of padding).
        let src = [
            1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0, 0, //
            9, 10, 11, 12, 13, 14, 15, 16, 0, 0, 0, 0,
        ];
        let mut dst = [0u8; 16];
        copy_rows(&src, 12, &mut dst, 2, 2, false);
        assert_eq!(dst, [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]);
        copy_rows(&src, 12, &mut dst, 2, 2, true);
        assert_eq!(dst[..8], [3, 2, 1, 4, 7, 6, 5, 8]);
    }

    #[test]
    fn input_events_are_read() {
        let mut buf = vec![0u8; 48];
        buf[16..18].copy_from_slice(&1u16.to_ne_bytes());
        buf[18..20].copy_from_slice(&30u16.to_ne_bytes());
        buf[20..24].copy_from_slice(&1i32.to_ne_bytes());
        buf[24 + 16..24 + 18].copy_from_slice(&2u16.to_ne_bytes());
        buf[24 + 20..24 + 24].copy_from_slice(&(-3i32).to_ne_bytes());
        assert_eq!(
            parse_events(&buf),
            [
                Input {
                    kind: 1,
                    code: 30,
                    value: 1
                },
                Input {
                    kind: 2,
                    code: 0,
                    value: -3
                },
            ]
        );
        // A torn event at the end is not read.
        assert_eq!(parse_events(&buf[..40]).len(), 1);
    }

    /// A Kyokai window pauses the picture only while capture hides Kyokai.
    #[test]
    fn a_shown_kyokai_window_pauses_only_while_hidden() {
        let show = crate::video::show_kyokai();
        set_kyokai_shown(true);
        crate::video::set_show_kyokai(false);
        assert!(paused());
        crate::video::set_show_kyokai(true);
        assert!(!paused());
        set_kyokai_shown(false);
        crate::video::set_show_kyokai(false);
        assert!(!paused());
        crate::video::set_show_kyokai(show);
    }
}
