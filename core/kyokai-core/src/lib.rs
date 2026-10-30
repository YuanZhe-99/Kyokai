//! The Kyokai core: the native library that the GUI and the engine load with
//! purego (doc/en-us/architecture.md, "FFI").
//!
//! The C ABI takes and returns integers and pointers only. Requests and
//! results are JSON documents: `kyokai_start` and `kyokai_call` write a
//! response `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` into a
//! buffer the core allocates, which the caller hands back to `kyokai_free`.

mod about;
mod access;
mod agent;
mod ai;
mod annexb;
mod audio;
mod auth;
mod backup;
mod clipboard;
mod decode;
mod external;
mod files;
mod identity;
mod input;
mod keyboard;
mod keys;
#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "macos")]
mod mac;
mod media;
#[cfg(windows)]
mod mf;
mod net;
mod opus;
mod password;
mod power;
#[cfg(windows)]
mod privacy;
mod session;
mod soft;
mod store;
mod system;
mod tunnel;
mod video;
#[cfg(target_os = "macos")]
mod vt;
mod watch;

use serde::Deserialize;
use serde_json::{Value, json};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

/// Version of the C ABI. Hosts compare it with their own before any other
/// call; changing an export's signature or meaning bumps it.
pub const ABI_VERSION: u32 = 7;

/// The running core: what `kyokai_start` set up, until `kyokai_stop`.
struct Core {
    /// What the core started with, to rebuild it after a restore or reset.
    cfg: StartConfig,
    state_dir: PathBuf,
    persistence: store::Persistence,
    /// Identity, access state and sessions, shared with the session tasks.
    ctx: Arc<session::Ctx>,
    password: password::Temporary,
    network: net::Network,
    /// Pauses the AI task when the computer goes to sleep.
    _suspend: power::Suspend,
}

impl Core {
    /// Replaces the temporary password and its OPAQUE record.
    fn new_password(&mut self) -> Result<(), String> {
        let password = password::Temporary::generate();
        self.ctx
            .access
            .slots
            .lock()
            .map_err(|_| "access state poisoned")?
            .set_temporary(password.reveal(), &self.ctx.identity.public_key())?;
        self.password = password;
        Ok(())
    }
}

/// A `session.connect` request.
#[derive(Deserialize)]
struct Connect {
    device_id: String,
    /// Dial this address (IP address or domain name) alone, typed for this
    /// connection; the device there must prove to be `device_id`, and the
    /// known host keeps the address.
    #[serde(default)]
    address: Option<String>,
    /// The route: `auto`, `network` or one of the host's addresses; absent,
    /// the one chosen for the host (doc/en-us/networking.md, "Routes").
    #[serde(default)]
    route: Option<String>,
    password: zeroize::Zeroizing<String>,
    #[serde(default)]
    want: Vec<access::Permission>,
    /// Keep the password for this host after a login it accepts.
    #[serde(default)]
    remember: bool,
}

/// A `request.decide` answer to a host confirmation.
#[derive(Deserialize)]
struct Decide {
    id: u64,
    allow: bool,
    #[serde(default)]
    permissions: Vec<access::Permission>,
}

static CORE: Mutex<Option<Core>> = Mutex::new(None);

/// What the host passes to `kyokai_start`.
#[derive(Clone, Deserialize)]
struct StartConfig {
    /// Product version of the host binary (internal/version).
    version: String,
    /// Directory of this installation's state: secrets, identity.
    state_dir: PathBuf,
    /// Name of a development profile, which keys the OS credential store
    /// entry so a second instance on one machine stays apart.
    #[serde(default)]
    profile: String,
    /// When the engine is the privileged helper: the kyokai-svc it starts
    /// as the session agent in the console session (Windows), or the socket
    /// its LaunchAgent connects to (macOS).
    #[serde(default)]
    agent: Option<PathBuf>,
}

/// A request to `kyokai_call`.
#[derive(Deserialize)]
struct Call {
    method: String,
    #[serde(default)]
    params: Value,
}

/// Returns the C ABI version, the first call every host makes.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_abi_version() -> u32 {
    ABI_VERSION
}

/// Starts the core with a JSON `StartConfig`.
/// Inputs: `cfg`/`cfg_len` — the UTF-8 JSON, borrowed for the call.
/// Returns: 0, with a JSON response (the status) written to `*out`/`*out_len`,
/// which the caller frees with `kyokai_free`; -1 when the arguments are
/// unusable (nothing is written).
/// Side effects: creates the state directory, reads or creates the device
/// identity, generates a temporary password.
///
/// # Safety
/// `cfg` points to `cfg_len` readable bytes; `out` and `out_len` are valid
/// for writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_start(
    cfg: *const u8,
    cfg_len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    // SAFETY: forwarded from this function's contract.
    unsafe {
        exchange(cfg, cfg_len, out, out_len, |input| {
            start(input).map(|()| status())
        })
    }
}

/// Answers one JSON `Call` (`{"method": …, "params": …}`).
/// Returns: as `kyokai_start`.
///
/// # Safety
/// As `kyokai_start`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_call(
    req: *const u8,
    req_len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    // SAFETY: forwarded from this function's contract.
    unsafe { exchange(req, req_len, out, out_len, call) }
}

/// Stops the core, dropping its in-memory secrets. Calling it twice is
/// harmless.
/// Side effects: sessions end first, each connected one with a goodbye its
/// peer acknowledged (waiting up to GOODBYE_WAIT).
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_stop() {
    let _ = catch_unwind(|| {
        // Every session says goodbye first, so the other device ends it at
        // once instead of waiting for the connection to time out (an ended
        // session's goodbye was acknowledged, or its wait ran out).
        let ctx = CORE
            .lock()
            .ok()
            .and_then(|c| c.as_ref().map(|c| c.ctx.clone()));
        if let Some(ctx) = ctx
            && ctx.sessions.any_connected()
        {
            ctx.sessions.close_all();
            let until = std::time::Instant::now() + GOODBYE_WAIT;
            while ctx.sessions.any_connected() && std::time::Instant::now() < until {
                std::thread::sleep(std::time::Duration::from_millis(20));
            }
        }
        if let Ok(mut core) = CORE.lock() {
            *core = None;
        }
    });
}

/// How long stopping waits for the sessions' goodbyes.
const GOODBYE_WAIT: std::time::Duration = std::time::Duration::from_secs(1);

/// Waits for the next media packet of a controller session, for the GUI
/// (doc/en-us/architecture.md, "FFI"); a tunnel's session gives its bytes as
/// packet 14.
/// Inputs: `buf`/`cap` — memory the caller owns, written by the core.
/// Returns: the packet's length; -1 when `timeout_ms` passed without one;
/// -2 when the session's media ended or there is no such session; -3 when
/// the packet does not fit `cap` (it is dropped).
///
/// # Safety
/// `buf` is valid for `cap` bytes of writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_media_read(
    session: u64,
    buf: *mut u8,
    cap: usize,
    timeout_ms: u32,
) -> i64 {
    let wait = std::time::Duration::from_millis(timeout_ms.into());
    let got = catch_unwind(|| {
        // A tunnel's bytes (packet 14).
        if let Some(read) = tunnel_read(session, wait) {
            return Some(read);
        }
        // The core's lock is held only to find the queue, never while waiting.
        let queue = CORE
            .lock()
            .ok()?
            .as_ref()?
            .ctx
            .sessions
            .media(session)
            .map(|(q, _)| q)?;
        Some(queue.pop(std::time::Duration::from_millis(timeout_ms.into())))
    });
    let packet = match got {
        Ok(Some(Ok(p))) => p,
        Ok(Some(Err(media::Empty::Timeout))) => return -1,
        _ => return -2,
    };
    if buf.is_null() || packet.len() > cap {
        return -3;
    }
    // SAFETY: the caller guarantees `cap` writable bytes; `packet.len() <= cap`.
    unsafe { std::ptr::copy_nonoverlapping(packet.as_ptr(), buf, packet.len()) };
    packet.len() as i64
}

/// Passes one packet from the GUI to a controller session: a control message
/// (`media::packet::CONTROL` + JSON) for the host, or a tunnel's bytes
/// (packet 14), which always wait for room.
/// Inputs: `ptr`/`len` — the packet, borrowed for the call.
/// Returns: 0; -1 for a malformed packet; -2 when there is no such session
/// or it ended; -3 when the session is behind and refused it (input: a
/// pointer move, press, click or text while it cannot take input; another
/// message that found no room within CONTROL_WAIT).
/// Notes: input never waits (`session::InputGate`): a release is never
/// refused, and what could not be delivered is reconciled with the host
/// later. Other messages wait CONTROL_WAIT at most, so the caller (the
/// engine's media thread, outside any runtime) never hangs on a stalled
/// session.
///
/// # Safety
/// `ptr` points to `len` readable bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_media_write(session: u64, ptr: *const u8, len: usize) -> i32 {
    if ptr.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `ptr`.
    let packet = unsafe { std::slice::from_raw_parts(ptr, len) };
    catch_unwind(|| {
        // A tunnel's bytes (packet 14), lossless: waits for room.
        if let Some((&media::packet::TUNNEL, bytes)) = packet.split_first() {
            return tunnel_write(session, bytes);
        }
        let Some((&media::packet::CONTROL, json)) = packet.split_first() else {
            return -1;
        };
        let Ok(mut msg) = serde_json::from_slice::<Value>(json) else {
            return -1;
        };
        let Some((tx, gate)) = CORE
            .lock()
            .ok()
            .and_then(|c| c.as_ref()?.ctx.sessions.input(session))
        else {
            return -2;
        };
        // Input is a stream: the gate decides at once (a malformed event
        // goes on, and the host drops it).
        if msg["type"] == "input"
            && let Ok(e) = serde_json::from_value::<input::Event>(msg["event"].clone())
        {
            return gate.write(&e, || tx.try_send(msg).is_ok());
        }
        let until = std::time::Instant::now() + CONTROL_WAIT;
        loop {
            match tx.try_send(msg) {
                Ok(()) => return 0,
                Err(tokio::sync::mpsc::error::TrySendError::Closed(_)) => return -2,
                Err(tokio::sync::mpsc::error::TrySendError::Full(m)) => msg = m,
            }
            if std::time::Instant::now() >= until {
                return -3;
            }
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
    })
    .unwrap_or(-2)
}

/// Longest a control message other than input waits for room in its
/// session (a quality, codec or AI request while the session is behind).
const CONTROL_WAIT: std::time::Duration = std::time::Duration::from_millis(200);

/// Opens a viewer decoder for a stream described by `media::VideoStart`
/// JSON (`start`/`len`, borrowed). Needs no started core.
/// Returns: the decoder's handle, or 0 when the codec is not available.
///
/// # Safety
/// `start` points to `len` readable bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_decoder_open(start: *const u8, len: usize) -> u64 {
    if start.is_null() {
        return 0;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `start`.
    let start = unsafe { std::slice::from_raw_parts(start, len) };
    catch_unwind(|| decode::open(start).unwrap_or(0)).unwrap_or(0)
}

thread_local! {
    /// The calling thread's picture buffer, reused between frames.
    static PICTURE: std::cell::RefCell<Vec<u8>> = const { std::cell::RefCell::new(Vec::new()) };
}

/// Decodes one frame; a finished picture is written as RGBA into `out`.
/// Inputs: `frame`/`len` — the encoded frame, borrowed; `out`/`cap` —
/// memory the caller owns.
/// Returns: 1 with the picture's size in `*width`/`*height`; 0 when the
/// decoder needs more input; -1 on an error; -3 when the picture does not
/// fit `cap`.
///
/// # Safety
/// `frame` points to `len` readable bytes; `out` is valid for `cap` bytes
/// of writes; `width` and `height` are valid for writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_decoder_decode(
    id: u64,
    frame: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
    width: *mut u32,
    height: *mut u32,
) -> i32 {
    if frame.is_null() || out.is_null() || width.is_null() || height.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `frame`.
    let frame = unsafe { std::slice::from_raw_parts(frame, len) };
    let result = catch_unwind(AssertUnwindSafe(|| {
        PICTURE.with_borrow_mut(|pic| match decode::decode(id, frame, pic) {
            Ok(Some((w, h))) if pic.len() > cap => {
                let _ = (w, h);
                -3
            }
            Ok(Some((w, h))) => {
                // SAFETY: `out` has `cap >= pic.len()` writable bytes; the
                // size pointers are valid, per the contract.
                unsafe {
                    std::ptr::copy_nonoverlapping(pic.as_ptr(), out, pic.len());
                    *width = w;
                    *height = h;
                }
                1
            }
            Ok(None) => 0,
            Err(_) => -1,
        })
    }));
    result.unwrap_or(-1)
}

/// Asks macOS for a privacy permission from the calling process: the app
/// calls it so that the system's prompt names and comes from Kyokai itself
/// (a request from the background engine is refused without one). Needs no
/// started core.
/// Inputs: `name`/`len` — `screen`, `accessibility` or `input-monitoring`,
/// borrowed for the call.
/// Returns: 1 granted now, 0 not (yet), -1 an unknown name or a system
/// without these permissions.
/// Side effects: macOS may show its prompt.
///
/// # Safety
/// `name` points to `len` readable bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_privacy_request(name: *const u8, len: usize) -> i32 {
    if name.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `name`.
    let bytes = unsafe { std::slice::from_raw_parts(name, len) };
    let Ok(name) = std::str::from_utf8(bytes) else {
        return -1;
    };
    if name == "keychain" {
        return -1;
    }
    match catch_unwind(|| request_permission(name)) {
        Ok(Ok(granted)) => i32::from(granted),
        _ => -1,
    }
}

/// Describes a viewer decoder as `{"decoder": name, "hardware": bool}`
/// JSON written into `out`: which decoder it is and whether it decodes in
/// hardware, as it reports (a VideoToolbox decoder knows once its first key
/// frame opened it).
/// Inputs: `out`/`cap` — memory the caller owns.
/// Returns: the length written; -1 for an unknown handle; -3 when it does
/// not fit `cap`.
///
/// # Safety
/// `out` is valid for `cap` bytes of writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_decoder_info(id: u64, out: *mut u8, cap: usize) -> i32 {
    if out.is_null() {
        return -1;
    }
    let Ok(Some((decoder, hardware))) = catch_unwind(|| decode::info(id)) else {
        return -1;
    };
    let json = serde_json::json!({"decoder": decoder, "hardware": hardware}).to_string();
    if json.len() > cap {
        return -3;
    }
    // SAFETY: `out` has `cap >= json.len()` writable bytes, per the contract.
    unsafe { std::ptr::copy_nonoverlapping(json.as_ptr(), out, json.len()) };
    json.len() as i32
}

/// Opens a decoder of the host's sound (Opus, 48 kHz stereo). Needs no
/// started core.
/// Returns: its handle, or 0 when it cannot be opened.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_audio_open() -> u64 {
    catch_unwind(|| decode::audio_open().unwrap_or(0)).unwrap_or(0)
}

thread_local! {
    /// The calling thread's sound buffer, reused between packets.
    static SOUND: std::cell::RefCell<Vec<f32>> = const { std::cell::RefCell::new(Vec::new()) };
}

/// Decodes one packet of the host's sound into `out` as interleaved stereo
/// 32-bit floats at 48 kHz.
/// Inputs: `packet`/`len` — the Opus packet, borrowed; `out`/`cap` — room
/// for `cap` floats the caller owns.
/// Returns: the frames written (2 floats each); -1 on an error; -3 when
/// they do not fit `cap`.
///
/// # Safety
/// `packet` points to `len` readable bytes; `out` is valid for `cap` floats
/// of writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_audio_decode(
    id: u64,
    packet: *const u8,
    len: usize,
    out: *mut f32,
    cap: usize,
) -> i32 {
    if packet.is_null() || out.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `packet`.
    let packet = unsafe { std::slice::from_raw_parts(packet, len) };
    let result = catch_unwind(AssertUnwindSafe(|| {
        SOUND.with_borrow_mut(|pcm| match decode::audio_decode(id, packet, pcm) {
            Ok(_) if pcm.len() > cap => -3,
            Ok(frames) => {
                // SAFETY: `out` has `cap >= pcm.len()` writable floats, per
                // the contract.
                unsafe { std::ptr::copy_nonoverlapping(pcm.as_ptr(), out, pcm.len()) };
                frames as i32
            }
            Err(_) => -1,
        })
    }));
    result.unwrap_or(-1)
}

/// Closes a sound decoder; an unknown handle is ignored.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_audio_close(id: u64) {
    let _ = catch_unwind(|| decode::audio_close(id));
}

/// Closes a viewer decoder; an unknown handle is ignored.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_decoder_close(id: u64) {
    let _ = catch_unwind(|| decode::close(id));
}

/// Grabs the keyboard for a viewer window, or ends its grab
/// (doc/en-us/computer-use.md, "Keyboard modes"); a grab asked again
/// renews it (the macOS tap switched on again or started anew, the Windows
/// hook installed again). Needs no started core.
/// Inputs: `window` — the viewer's native window handle; `mode`/`len` — the
/// mode's name (`map`, `translate`, `off`), borrowed for the call.
/// Returns: 0; -1 for an unknown mode or no window; -2 when the keyboard cannot be
/// grabbed here (no hook; on macOS, no Accessibility permission).
///
/// # Safety
/// `mode` points to `len` readable bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_keyboard_grab(window: usize, mode: *const u8, len: usize) -> i32 {
    if mode.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `mode`.
    let name = unsafe { std::slice::from_raw_parts(mode, len) };
    let Some(ask) = std::str::from_utf8(name)
        .ok()
        .and_then(keyboard::Ask::from_name)
    else {
        return -1;
    };
    let off = ask == keyboard::Ask::Off;
    // No window is in front at times (handle 0): a grab for it would take
    // keys from whatever comes next.
    if window == 0 && !off {
        return -1;
    }
    match catch_unwind(|| keyboard::grab(window, ask)) {
        Ok(true) => 0,
        Ok(false) if off => 0,
        _ => -2,
    }
}

/// Waits for the next key the keyboard grab took for a viewer.
/// Inputs: `out` — a `keyboard::KeyEvent` the caller owns, written by the
/// core.
/// Returns: 1 with the key in `*out`; 0 when `timeout_ms` passed without
/// one; -2 when the keyboard cannot be grabbed here.
///
/// # Safety
/// `out` is valid for a write of one `keyboard::KeyEvent`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_keyboard_read(
    out: *mut keyboard::KeyEvent,
    timeout_ms: u32,
) -> i32 {
    // Regression: this answered -2 off Windows, so a Mac viewer's reader
    // stopped and the keys its grab took went nowhere.
    if out.is_null() || !cfg!(any(windows, target_os = "macos")) {
        return -2;
    }
    match catch_unwind(|| keyboard::next(std::time::Duration::from_millis(timeout_ms.into()))) {
        Ok(Some(e)) => {
            // SAFETY: the caller guarantees `out` is writable.
            unsafe { out.write(e) };
            1
        }
        Ok(None) => 0,
        Err(_) => -2,
    }
}

/// Describes how a viewer window's keyboard grab stands as
/// `{"state": name, "holder": program}` JSON written into `out`: the
/// state's name is `taking`, `none`, `off`, `secure-input`, `other-window`,
/// `no-reader` or `behind` (`keyboard::Standing`); `holder`, present when
/// known, names the program holding Secure Event Input or in front. Needs
/// no started core.
/// Inputs: `window` — the viewer's native window handle; `out`/`cap` —
/// memory the caller owns.
/// Returns: the length written; -1 for a null `out`; -2 when the keyboard
/// cannot be grabbed here; -3 when it does not fit `cap`.
/// Side effects: on macOS reads the session's Secure Event Input holder
/// and the on-screen window list.
///
/// # Safety
/// `out` is valid for `cap` bytes of writes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_keyboard_state(window: usize, out: *mut u8, cap: usize) -> i32 {
    if out.is_null() {
        return -1;
    }
    if !cfg!(any(windows, target_os = "macos")) {
        return -2;
    }
    let Ok(standing) = catch_unwind(|| keyboard::state(window)) else {
        return -2;
    };
    let mut v = json!({"state": standing.name()});
    if !standing.holder().is_empty() {
        v["holder"] = json!(standing.holder());
    }
    let json = v.to_string();
    if json.len() > cap {
        return -3;
    }
    // SAFETY: `out` has `cap >= json.len()` writable bytes, per the contract.
    unsafe { std::ptr::copy_nonoverlapping(json.as_ptr(), out, json.len()) };
    json.len() as i32
}

/// Frees a response buffer from `kyokai_start` or `kyokai_call`.
///
/// # Safety
/// `ptr`/`len` are exactly a pair the core returned, freed once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn kyokai_free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() {
        // SAFETY: the pair came from `Box<[u8]>::into_raw` in `exchange`.
        drop(unsafe { Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr, len)) });
    }
}

/// Writes the log records of Kyokai's own code and of the vendored capturer
/// (never EasyTier's) to stderr with the local time, like the engine's own
/// lines: the engine points stderr at its log while logs are on
/// (doc/en-us/security.md, "Logging").
struct StderrLog;

impl log::Log for StderrLog {
    fn enabled(&self, m: &log::Metadata) -> bool {
        m.level() <= log::Level::Info
            && (m.target().starts_with("kyokai_core") || m.target().starts_with("rustdesk_"))
    }

    fn log(&self, r: &log::Record) {
        if self.enabled(r.metadata()) {
            let line = format!("core {}: {}", r.level(), r.args());
            eprintln!("{} {line}", now());
            crate::agent::forward_log(&line);
        }
    }

    fn flush(&self) {}
}

/// The local time as the engine's log writes it.
pub(crate) fn now() -> String {
    chrono::Local::now().format("%Y/%m/%d %H:%M:%S").to_string()
}

/// Installs, once per process, the core's logger and a panic hook that
/// records each panic with its thread, place and backtrace (the default
/// hook wrote the message alone, to a stderr nothing kept).
fn diagnostics() {
    static ONCE: std::sync::Once = std::sync::Once::new();
    ONCE.call_once(|| {
        if log::set_logger(&StderrLog).is_ok() {
            log::set_max_level(log::LevelFilter::Info);
        }
        std::panic::set_hook(Box::new(|info| {
            let thread = std::thread::current();
            let line = format!(
                "core panic on thread {}: {info}\n{}",
                thread.name().unwrap_or("unnamed"),
                std::backtrace::Backtrace::force_capture()
            );
            eprintln!("{} {line}", now());
            crate::agent::forward_log(&line);
        }));
    });
}

/// Runs the body of a callback that a system framework calls, so that a
/// panic (logged by the panic hook) ends the callback instead of unwinding
/// into foreign code or aborting the process.
#[cfg(target_os = "macos")]
pub(crate) fn guarded(f: impl FnOnce()) {
    let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(f));
}

/// Runs `f` on the request bytes and writes its JSON response, catching
/// panics so none crosses the C boundary.
/// Side effects: installs the core's diagnostics on the first call
/// (`diagnostics`).
///
/// # Safety
/// `input` points to `len` readable bytes; `out` and `out_len` are valid for
/// writes.
unsafe fn exchange(
    input: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
    f: impl FnOnce(&[u8]) -> Result<Value, String>,
) -> i32 {
    diagnostics();
    if input.is_null() || out.is_null() || out_len.is_null() {
        return -1;
    }
    // SAFETY: the caller guarantees `len` readable bytes at `input`.
    let bytes = unsafe { std::slice::from_raw_parts(input, len) };
    let response = match catch_unwind(AssertUnwindSafe(|| f(bytes))) {
        Ok(Ok(result)) => json!({"ok": true, "result": result}),
        Ok(Err(error)) => json!({"ok": false, "error": error}),
        Err(_) => json!({"ok": false, "error": "internal error"}),
    };
    let data = serde_json::to_vec(&response)
        .unwrap_or_default()
        .into_boxed_slice();
    let n = data.len();
    // SAFETY: the caller guarantees `out` and `out_len` are writable.
    unsafe {
        *out_len = n;
        *out = Box::into_raw(data) as *mut u8;
    }
    0
}

fn start(input: &[u8]) -> Result<(), String> {
    let cfg: StartConfig =
        serde_json::from_slice(input).map_err(|e| format!("bad start config: {e}"))?;
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    if guard.is_some() {
        return Err("the core is already started".into());
    }
    *guard = Some(build(cfg)?);
    Ok(())
}

/// Opens the secrets store, loads the identity and access state, starts the
/// network and generates a temporary password.
fn build(cfg: StartConfig) -> Result<Core, String> {
    std::fs::create_dir_all(&cfg.state_dir).map_err(|e| format!("state directory: {e}"))?;
    let (secrets, persistence) = store::open(&cfg.state_dir, &cfg.profile);
    build_on(cfg, secrets, persistence)
}

/// `build` on a store already opened.
fn build_on(
    cfg: StartConfig,
    secrets: store::Secrets,
    persistence: store::Persistence,
) -> Result<Core, String> {
    let secrets = Arc::new(secrets);
    let identity = Arc::new(identity::Identity::load_or_create(&secrets)?);
    let ctx = Arc::new(session::Ctx {
        clipboard: Arc::new(clipboard::system),
        ai: Arc::new(ai::Ai::new(&cfg.state_dir, secrets.clone())?),
        identity,
        access: Arc::new(access::Access::load(secrets.clone())?),
        sessions: Arc::default(),
        watching: Arc::default(),
        version: cfg.version.clone(),
        about: std::sync::Mutex::new(about::Local::load(&cfg.state_dir)),
    });
    #[cfg(any(windows, target_os = "macos"))]
    if let Some(agent) = &cfg.agent {
        agent::start_helper(agent.clone());
    }
    let network = net::Network::start(&secrets, ctx.clone(), cfg.agent.is_some(), &cfg.state_dir)?;
    let suspend = power::Suspend::watch(&ctx.ai);
    let mut core = Core {
        state_dir: cfg.state_dir.clone(),
        cfg,
        persistence,
        ctx,
        password: password::Temporary::generate(),
        network,
        _suspend: suspend,
    };
    core.new_password()?;
    Ok(core)
}

/// Changes the identity's secrets, then rebuilds the core so every part
/// uses the new identity.
/// Side effects: ends every session and reconnects the network.
fn replace_identity(
    change: impl FnOnce(&store::Secrets) -> Result<(), String>,
) -> Result<Value, String> {
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    let core = guard.as_ref().ok_or("the core is not started")?;
    change(core.ctx.access.secrets())?;
    let cfg = core.cfg.clone();
    // Whom the host serves is the engine's decision and outlives the
    // identity (a helper without a GUI must not start serving everyone).
    let serve = core.ctx.access.serve();
    *guard = None; // stops the network and drops the old identity
    let core = build(cfg)?;
    core.ctx.access.set_serve(serve);
    let id = core.ctx.identity.device_id();
    *guard = Some(core);
    Ok(json!({"device_id": id}))
}

/// Opens the secrets store again after the OS store refused it, and
/// rebuilds the core on it when it opens: the stored identity, passwords
/// and settings come back, and what this run kept in memory is dropped.
/// Returns: whether the store is open now.
/// Side effects: ends every session and reconnects the network when the
/// store opened.
fn reopen_store() -> Result<bool, String> {
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    let core = guard.as_ref().ok_or("the core is not started")?;
    if !matches!(core.persistence, store::Persistence::Denied) {
        return Ok(true);
    }
    rebuild_on_store(&mut guard)
}

/// Starts over when the saved store cannot be unlocked (its key is gone):
/// the old store is moved aside, untouched, and a new one with a new
/// identity is created and kept from now on.
/// Returns: whether the new store is kept by the OS store.
/// Side effects: renames secrets.bin; rebuilds the core on the new store,
/// with a new Device ID.
fn start_over() -> Result<bool, String> {
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    let core = guard.as_ref().ok_or("the core is not started")?;
    if !matches!(core.persistence, store::Persistence::Unreadable) {
        return Err("the saved identity is readable".into());
    }
    store::set_aside(&core.cfg.state_dir)?;
    rebuild_on_store(&mut guard)
}

/// Opens the store again and rebuilds the core on it, unless the OS store
/// refuses again.
fn rebuild_on_store(guard: &mut Option<Core>) -> Result<bool, String> {
    let core = guard.as_ref().ok_or("the core is not started")?;
    let cfg = core.cfg.clone();
    // Refused again: this run goes on with what it keeps in memory.
    let (secrets, persistence) = store::open(&cfg.state_dir, &cfg.profile);
    if matches!(persistence, store::Persistence::Denied) {
        return Ok(false);
    }
    let serve = core.ctx.access.serve();
    *guard = None; // stops the network and drops the in-memory identity
    let core = build_on(cfg, secrets, persistence)?;
    core.ctx.access.set_serve(serve);
    *guard = Some(core);
    Ok(true)
}

fn call(input: &[u8]) -> Result<Value, String> {
    let mut req: Call = serde_json::from_slice(input).map_err(|e| format!("bad call: {e}"))?;
    match req.method.as_str() {
        "status" => Ok(status()),
        // The session agent's whole life: serves the helper until it goes.
        #[cfg(windows)]
        "agent.serve" => {
            let p = &req.params;
            let (down, up) = (p["down"].as_str(), p["up"].as_str());
            let helper = p["helper_pid"].as_u64();
            match (down, up, helper) {
                (Some(down), Some(up), Some(pid)) => {
                    agent::run_agent(down, up, pid as u32).map(|()| json!({}))
                }
                _ => Err("bad agent request".into()),
            }
        }
        #[cfg(target_os = "macos")]
        "agent.serve" => match req.params["socket"].as_str() {
            Some(socket) => agent::run_agent_socket(socket).map(|()| json!({})),
            None => Err("bad agent request".into()),
        },
        "password.reveal" => with_core(|c| json!({"temporary": c.password.reveal()})),
        "password.regenerate" => try_core(|c| {
            c.new_password()?;
            Ok(json!({"serial": c.password.serial()}))
        }),
        "password.set_permanent" => {
            // {"password": "…"} sets it, {"password": null} removes it.
            let password: Option<zeroize::Zeroizing<String>> =
                serde_json::from_value(req.params["password"].take())
                    .map_err(|e| format!("bad password: {e}"))?;
            if password.is_none() && with_core(|c| json!(c.ctx.access.protected()))? == json!(true)
            {
                return Err(
                    "protected mode needs the permanent password: turn it off first".into(),
                );
            }
            if password
                .as_ref()
                .is_some_and(|p| p.chars().count() < MIN_PERMANENT)
            {
                return Err(format!(
                    "the permanent password needs at least {MIN_PERMANENT} characters"
                ));
            }
            try_core(|c| {
                let mut slots = c
                    .ctx
                    .access
                    .slots
                    .lock()
                    .map_err(|_| "access state poisoned")?;
                let key = c.ctx.identity.public_key();
                slots.set_permanent(
                    c.ctx.access.secrets(),
                    password.as_deref().map(String::as_str),
                    &key,
                )?;
                Ok(json!({"permanent_password": slots.has_permanent()}))
            })
        }
        "identity.export" => {
            let passphrase: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["passphrase"].take())
                    .map_err(|e| format!("bad passphrase: {e}"))?;
            try_core(|c| {
                let doc = backup::export(c.ctx.access.secrets(), &passphrase)?;
                Ok(json!({"document": doc}))
            })
        }
        "identity.import" => {
            let document = req.params["document"]
                .as_str()
                .ok_or("bad backup")?
                .to_owned();
            let passphrase: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["passphrase"].take())
                    .map_err(|e| format!("bad passphrase: {e}"))?;
            replace_identity(|s| backup::import(s, &document, &passphrase))
        }
        "identity.reset" => replace_identity(backup::reset),
        // A run-mode change moves the whole store between this device's
        // engines (doc/en-us/run-modes.md).
        "store.export" => {
            let passphrase: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["passphrase"].take())
                    .map_err(|e| format!("bad passphrase: {e}"))?;
            try_core(|c| {
                let doc = backup::export_store(
                    c.ctx.access.secrets(),
                    c.ctx.access.held_history(),
                    &c.state_dir,
                    &passphrase,
                )?;
                Ok(json!({"document": doc}))
            })
        }
        "store.import" => {
            let document = req.params["document"]
                .as_str()
                .ok_or("bad document")?
                .to_owned();
            let passphrase: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["passphrase"].take())
                    .map_err(|e| format!("bad passphrase: {e}"))?;
            let replace = req.params["replace"].as_bool().unwrap_or(false);
            let dir = try_core(|c| Ok(json!(c.state_dir)))?;
            let dir = PathBuf::from(dir.as_str().unwrap_or_default());
            replace_identity(|s| backup::import_store(s, &dir, &document, &passphrase, replace))
        }
        "store.clear" => {
            let to = match req.params["to"].as_str() {
                Some(to @ ("helper" | "user")) => to.to_owned(),
                _ => return Err("bad destination".into()),
            };
            try_core(|c| {
                // The caller stops this engine next: it must not serve the
                // identity the other engine now holds.
                c.ctx.access.set_serve(access::Serve::None);
                c.ctx.sessions.close_hosts();
                let id = c.ctx.identity.device_id();
                backup::clear_store(c.ctx.access.secrets(), &c.state_dir, &to, &id)
                    .map(|()| json!({}))
            })
        }
        "device.set_name" => {
            let name = req.params["name"].as_str().ok_or("bad name")?.to_owned();
            if name.chars().count() > about::MAX_NAME {
                return Err(format!("a name has at most {} characters", about::MAX_NAME));
            }
            try_core(|c| {
                let mut l = c.ctx.about.lock().map_err(|_| "core state poisoned")?;
                l.set_name(&name)?;
                Ok(json!({"name": l.chosen(), "about": l.about()}))
            })
        }
        "network.get" => with_core(|c| c.network.settings()),
        "network.set" => {
            let update: net::Update = serde_json::from_value(req.params)
                .map_err(|e| format!("bad network settings: {e}"))?;
            try_core(|c| {
                c.network.update(c.ctx.access.secrets(), update)?;
                Ok(c.network.settings())
            })
        }
        // The networks this device keeps, joined or not (each answers with
        // them all, without the secrets).
        "network.profiles" => with_core(|c| c.network.profiles()),
        "network.profile.set" => {
            let update: net::ProfileUpdate = serde_json::from_value(req.params)
                .map_err(|e| format!("bad network settings: {e}"))?;
            try_core(|c| {
                c.network.set_profile(c.ctx.access.secrets(), update)?;
                Ok(c.network.profiles())
            })
        }
        "network.profile.remove" => {
            let id = req.params["id"].as_str().ok_or("bad network")?.to_owned();
            try_core(|c| {
                c.network.remove_profile(c.ctx.access.secrets(), &id)?;
                Ok(c.network.profiles())
            })
        }
        "network.latency_first" => {
            let on = req.params["latency_first"]
                .as_bool()
                .ok_or("bad latency choice")?;
            try_core(|c| {
                c.network.set_latency_first(c.ctx.access.secrets(), on)?;
                Ok(c.network.profiles())
            })
        }
        "network.backend" => {
            let easytier = match &req.params["easytier"] {
                Value::Null => None,
                Value::String(tag) => Some(tag.clone()),
                _ => return Err("bad network backend".into()),
            };
            try_core(|c| {
                c.network.set_backend(c.ctx.access.secrets(), easytier)?;
                Ok(c.network.profiles())
            })
        }
        "network.direct" => {
            let direct = req.params["direct"].as_bool().ok_or("bad direct choice")?;
            try_core(|c| {
                c.network.set_direct(c.ctx.access.secrets(), direct)?;
                Ok(c.network.profiles())
            })
        }
        "session.connect" => {
            let r: Connect = serde_json::from_value(req.params)
                .map_err(|e| format!("bad connect request: {e}"))?;
            if r.device_id.len() != 9 || !r.device_id.bytes().all(|b| b.is_ascii_digit()) {
                return Err("a Device ID has 9 digits".into());
            }
            try_core(|c| {
                if r.device_id == c.ctx.identity.device_id() {
                    return Err("that is this device's own ID".into());
                }
                // An empty password uses the one saved for this host, if any.
                let saved = r
                    .password
                    .is_empty()
                    .then(|| c.ctx.access.saved_password(&r.device_id))
                    .flatten();
                let remember = session::Remember {
                    save: r.remember,
                    saved: saved.is_some(),
                };
                let route = net::Choice::ask(
                    &c.ctx,
                    &r.device_id,
                    r.address.as_deref(),
                    r.route.as_deref(),
                );
                let id = c.network.connect(net::Ask {
                    device_id: r.device_id,
                    route,
                    password: saved.unwrap_or(r.password),
                    want: r.want,
                    remember,
                    kind: session::Kind::Desktop,
                })?;
                Ok(json!({"id": id}))
            })
        }
        // A tunnel to a device's own service: the saved password if any,
        // else sign-in keys or the host user's answer (doc/en-us/remote-services.md).
        "tunnel.open" => {
            #[derive(Deserialize)]
            struct Open {
                device_id: String,
                #[serde(default)]
                address: Option<String>,
                #[serde(default)]
                route: Option<String>,
                service: tunnel::Service,
            }
            let r: Open = serde_json::from_value(req.params)
                .map_err(|e| format!("bad tunnel request: {e}"))?;
            try_core(|c| {
                let saved = c.ctx.access.saved_password(&r.device_id);
                let route = net::Choice::ask(
                    &c.ctx,
                    &r.device_id,
                    r.address.as_deref(),
                    r.route.as_deref(),
                );
                let id = c.network.connect(net::Ask {
                    remember: session::Remember {
                        save: false,
                        saved: saved.is_some(),
                    },
                    password: saved.unwrap_or_default(),
                    device_id: r.device_id,
                    route,
                    want: vec![r.service.permission()],
                    kind: session::Kind::Tunnel(r.service),
                })?;
                Ok(json!({"id": id}))
            })
        }
        // Watches these hosts' status for a while: a lease the GUI renews
        // (doc/en-us/computer-use.md, "Host status").
        // Changes a known host's routes: adds or removes an address, or
        // chooses the route (doc/en-us/networking.md, "Routes").
        "routes.set" => {
            #[derive(Deserialize)]
            struct Set {
                device_id: String,
                #[serde(default)]
                add: Option<String>,
                #[serde(default)]
                remove: Option<String>,
                #[serde(default)]
                prefer: Option<String>,
            }
            let r: Set = serde_json::from_value(req.params)
                .map_err(|e| format!("bad routes request: {e}"))?;
            let add = r.add.as_deref().map(str::trim).filter(|a| !a.is_empty());
            if let Some(a) = add {
                net::split_address(a)?;
            }
            try_core(|c| {
                let host = c.ctx.access.set_routes(
                    &r.device_id,
                    add,
                    r.remove.as_deref(),
                    r.prefer.as_deref().map(str::trim),
                )?;
                if add.is_some() {
                    c.network.probe(vec![r.device_id.clone()]);
                }
                Ok(
                    json!({"routes": routes_json(&host, &c.network.probes(&r.device_id)), "prefer": host.prefer}),
                )
            })
        }
        // Looks at the addresses of these known hosts in the background;
        // the status shows what was found.
        "routes.probe" => {
            let devices: Vec<String> = serde_json::from_value(req.params["devices"].take())
                .map_err(|e| format!("bad devices: {e}"))?;
            if devices.len() > 16 {
                return Err("too many devices".into());
            }
            try_core(|c| {
                c.network.probe(devices);
                Ok(json!({}))
            })
        }
        "watch.set" => {
            let devices: Vec<String> = serde_json::from_value(req.params["devices"].take())
                .map_err(|e| format!("bad devices: {e}"))?;
            if devices
                .iter()
                .any(|d| d.len() != 9 || !d.bytes().all(|b| b.is_ascii_digit()))
            {
                return Err("a Device ID has 9 digits".into());
            }
            // Those of them at the slow pace (Home hidden); none from an
            // older GUI.
            let slow: Option<Vec<String>> = serde_json::from_value(req.params["slow"].take())
                .map_err(|e| format!("bad slow: {e}"))?;
            let slow = slow.unwrap_or_default();
            try_core(|c| {
                c.ctx
                    .watching
                    .set(&devices, &slow, &c.network.dialer(), &c.network.handle());
                Ok(json!({}))
            })
        }
        // Waits (without the core's lock) until a session is connected or
        // ended, for at most `timeout_ms`.
        "tunnel.wait" => {
            let id = req.params["id"].as_u64().ok_or("bad session id")?;
            let wait = req.params["timeout_ms"]
                .as_u64()
                .unwrap_or(90_000)
                .min(90_000);
            let sessions = {
                let guard = CORE.lock().map_err(|_| "core state poisoned")?;
                guard
                    .as_ref()
                    .ok_or("the core is not started")?
                    .ctx
                    .sessions
                    .clone()
            };
            Ok(sessions.wait(id, std::time::Duration::from_millis(wait)))
        }
        "credentials.get" => {
            let (device, service) = device_service(&req.params)?;
            try_core(|c| {
                let mut saved = c.ctx.access.saved(&device);
                Ok(json!(saved.service(service).take().unwrap_or_default()))
            })
        }
        "credentials.set" => {
            let (device, service) = device_service(&req.params)?;
            let creds: access::Credentials =
                serde_json::from_value(req.params["credentials"].take())
                    .map_err(|e| format!("bad credentials: {e}"))?;
            try_core(|c| {
                c.ctx
                    .access
                    .save_credentials(&device, service, creds)
                    .map(|()| json!({}))
            })
        }
        // Forgets one service's sign-in, the key a host admitted (`key`),
        // or everything saved for the host (no service).
        "credentials.forget" => {
            let device = req.params["device_id"]
                .as_str()
                .ok_or("bad Device ID")?
                .to_owned();
            let what = req.params["service"].as_str().unwrap_or("").to_owned();
            try_core(|c| {
                match what.as_str() {
                    "" => c.ctx.access.forget_saved(&device),
                    w => c.ctx.access.forget_credentials(&device, w),
                }
                .map(|()| json!({}))
            })
        }
        "session.identify" => {
            let address = req.params["address"]
                .as_str()
                .ok_or("bad address")?
                .to_owned();
            net::split_address(&address)?;
            // Dialing may take seconds: not under the core's lock.
            let (handle, own) = {
                let guard = CORE.lock().map_err(|_| "core state poisoned")?;
                let c = guard.as_ref().ok_or("the core is not started")?;
                (c.network.handle(), c.ctx.identity.device_id())
            };
            Ok(match handle.block_on(net::identify(&address)) {
                Ok(id) if id == own => json!({"failure": "self"}),
                Ok(id) => json!({"device_id": id}),
                Err(failure) => json!({"failure": failure}),
            })
        }
        "protect.set" => {
            let on = req.params["on"]
                .as_bool()
                .ok_or("bad protected-mode choice")?;
            let password: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["password"].take()).unwrap_or_default();
            try_core(|c| {
                let key = c.ctx.identity.public_key();
                c.ctx
                    .access
                    .set_protected(on, &password, &key)
                    .map(|()| json!({}))
            })
        }
        "access.history" => {
            let keep = req.params["keep"].as_bool().ok_or("bad history choice")?;
            try_core(|c| c.ctx.access.keep_history(keep).map(|()| json!({})))
        }
        "device.remove" => {
            let id = req.params["device_id"]
                .as_str()
                .ok_or("bad Device ID")?
                .to_owned();
            try_core(|c| {
                c.ctx.sessions.remove_host(&id);
                c.ctx.access.forget_saved(&id)?;
                // A device never connected to has no pinned key.
                let _ = c.ctx.access.forget_host(&id);
                Ok(json!({}))
            })
        }
        // Forgets everything kept for signing in to the host: its password,
        // the key it admitted and its services' sign-ins.
        "password.forget_saved" => {
            let device = req.params["device_id"].as_str().ok_or("bad Device ID")?;
            try_core(|c| c.ctx.access.forget_saved(device).map(|()| json!({})))
        }
        "keys.list" => with_core(|c| keys::list_login(c.ctx.access.secrets())),
        "keys.add" => {
            let name = req.params["name"].as_str().unwrap_or("").to_owned();
            let private: zeroize::Zeroizing<String> =
                serde_json::from_value(req.params["private"].take())
                    .map_err(|_| "the private key is missing")?;
            try_core(|c| keys::add_login(c.ctx.access.secrets(), &name, &private))
        }
        "keys.remove" => {
            let id = req.params["id"].as_str().ok_or("bad key")?.to_owned();
            try_core(|c| keys::remove_login(c.ctx.access.secrets(), &id).map(|()| json!({})))
        }
        "keys.sign" => {
            let p = &req.params;
            let (Some(id), Some(algorithm), Some(data)) = (
                p["key_id"].as_str(),
                p["algorithm"].as_str(),
                p["data"].as_str(),
            ) else {
                return Err("bad signing request".into());
            };
            try_core(|c| {
                keys::sign_ssh(c.ctx.access.secrets(), id, algorithm, data)
                    .map(|s| json!({"signature": s}))
            })
        }
        "authorized.list" => with_core(|c| json!(keys::authorized(c.ctx.access.secrets()))),
        "authorized.add" => {
            let name = req.params["name"].as_str().unwrap_or("").to_owned();
            let public = req.params["public"]
                .as_str()
                .ok_or("bad public key")?
                .to_owned();
            try_core(|c| {
                keys::add_authorized(c.ctx.access.secrets(), &name, &public).map(|a| json!(a))
            })
        }
        "authorized.set" => {
            let id = req.params["id"].as_str().ok_or("bad key")?.to_owned();
            let off = req.params["off"].as_bool().ok_or("bad switch")?;
            try_core(|c| keys::set_authorized(c.ctx.access.secrets(), &id, off).map(|()| json!({})))
        }
        "authorized.remove" => {
            let id = req.params["id"].as_str().ok_or("bad key")?.to_owned();
            try_core(|c| keys::remove_authorized(c.ctx.access.secrets(), &id).map(|()| json!({})))
        }
        "session.close" => {
            let id = req.params["id"].as_u64().ok_or("bad session id")?;
            try_core(|c| c.ctx.sessions.close(id).map(|()| json!({})))
        }
        "request.decide" => {
            let d: Decide =
                serde_json::from_value(req.params).map_err(|e| format!("bad decision: {e}"))?;
            try_core(|c| {
                let granted = d.allow.then_some(d.permissions);
                c.ctx.access.decide(d.id, granted).map(|()| json!({}))
            })
        }
        "host.serve" => {
            let s: access::Serve = serde_json::from_value(req.params["serve"].take())
                .map_err(|e| format!("bad serve: {e}"))?;
            try_core(|c| {
                c.ctx.access.set_serve(s);
                // Follow GUI without a GUI serves no one: its sessions end.
                if s == access::Serve::None {
                    c.ctx.sessions.close_hosts();
                }
                Ok(json!({}))
            })
        }
        "sessions.close_controllers" => try_core(|c| {
            c.ctx.sessions.close_controllers();
            Ok(json!({}))
        }),
        "agent.restart" => match agent::link() {
            Some(link) => link.restart().map(|()| json!({})),
            None => Err("this engine has no session agent".into()),
        },
        "store.start_over" => start_over().map(|kept| json!({"kept": kept})),
        // A person's remote input may reach Kyokai's windows, or not (the
        // host user's Hide Kyokai from capture). Each session decides per
        // event and tells the helper's agent with it.
        "input.kyokai" => {
            let allow = req.params["allow"].as_bool().ok_or("bad input setting")?;
            input::set_kyokai_input(allow);
            Ok(json!({}))
        }
        "capture.show" => {
            let show = req.params["show"].as_bool().ok_or("bad capture setting")?;
            video::set_show_kyokai(show);
            // In the helper the session agent captures: it follows too.
            if let Some(link) = agent::link() {
                link.show_kyokai(show);
            }
            Ok(json!({}))
        }
        "sound.source" => {
            let source = req.params["source"].as_str().ok_or("bad sound source")?;
            audio::set_source(source);
            // In the helper the session agent captures: it follows too.
            if let Some(link) = agent::link() {
                link.sound_source(source);
            }
            Ok(json!({}))
        }
        "sound.devices" => match agent::link() {
            Some(link) => link.call::<Value>("sound.devices", json!({})),
            None => Ok(json!(audio::devices())),
        },
        "permission.request" => {
            let name = req.params["name"].as_str().ok_or("bad permission")?;
            request_permission(name).map(|granted| json!({"granted": granted}))
        }
        "access.password_grants" => {
            let grants: Vec<access::Permission> =
                serde_json::from_value(req.params["grants"].take())
                    .map_err(|e| format!("bad permissions: {e}"))?;
            try_core(|c| c.ctx.access.set_password_grants(grants).map(|()| json!({})))
        }
        "access.settings" => {
            #[derive(Deserialize)]
            struct Settings {
                password_mode: Option<access::PasswordMode>,
                without_asking: Option<bool>,
                by_address: Option<access::ByAddress>,
                service_ports: Option<access::ServicePorts>,
            }
            let p: Settings = serde_json::from_value(req.params)
                .map_err(|e| format!("bad access settings: {e}"))?;
            try_core(|c| {
                if let Some(m) = p.password_mode {
                    c.ctx.access.set_password_mode(m)?;
                }
                if let Some(on) = p.without_asking {
                    c.ctx.access.set_without_asking(on)?;
                }
                if let Some(by) = p.by_address {
                    c.ctx.access.set_by_address(by)?;
                    c.network.accept_by_address();
                }
                if let Some(ports) = p.service_ports {
                    c.ctx.access.set_service_ports(ports)?;
                }
                Ok(json!({}))
            })
        }
        "trusted.list" => with_core(|c| {
            c.ctx
                .access
                .trusted()
                .into_iter()
                .map(|(key, t)| {
                    json!({
                        "key": key,
                        "device_id": t.device_id,
                        "first": t.first,
                        "last": t.last,
                        "granted": t.granted,
                        "about": t.about,
                    })
                })
                .collect::<Value>()
        }),
        "trusted.remove" => {
            let key = req.params["key"].as_str().ok_or("bad key")?.to_owned();
            try_core(|c| session::untrust(&c.ctx, &key).map(|()| json!({})))
        }
        "host.forget" => {
            let id = req.params["device_id"]
                .as_str()
                .ok_or("bad Device ID")?
                .to_owned();
            try_core(|c| c.ctx.access.forget_host(&id).map(|()| json!({})))
        }
        "ai.settings.get" => with_core(|c| c.ctx.ai.settings()),
        "ai.settings.set" => try_core(|c| c.ctx.ai.set_settings(&req.params)),
        "ai.key.set" => {
            // {"source": "claude", "key": "…"} stores it, {"key": null} removes
            // it (`provider` is the first versions' name of the field).
            let source = req.params["source"]
                .as_str()
                .or(req.params["provider"].as_str())
                .ok_or("bad source")?
                .to_owned();
            let key: Option<zeroize::Zeroizing<String>> =
                serde_json::from_value(req.params["key"].take())
                    .map_err(|e| format!("bad key: {e}"))?;
            try_core(|c| {
                c.ctx
                    .ai
                    .set_key(&source, key.as_deref().map(String::as_str))?;
                Ok(c.ctx.ai.settings())
            })
        }
        "ai.models.list" => {
            // Asks the source for its models, only when the user asks.
            let source = req.params["source"]
                .as_str()
                .ok_or("bad source")?
                .to_owned();
            let ai = {
                let guard = CORE.lock().map_err(|_| "core state poisoned")?;
                guard
                    .as_ref()
                    .ok_or("the core is not started")?
                    .ctx
                    .ai
                    .clone()
            };
            ai.list_models(&source).map(|m| json!({"models": m}))
        }
        "ai.task.start" => {
            let key = req.params["key"].as_str().unwrap_or("").to_owned();
            let instruction = req.params["instruction"]
                .as_str()
                .ok_or("bad instruction")?
                .to_owned();
            try_core(|c| Ok(json!({"id": c.ctx.ai.start(&key, &instruction, None)?})))
        }
        "ai.task.reply" => {
            let id = req.params["id"].as_u64().ok_or("bad task id")?;
            let text = req.params["text"].as_str().ok_or("bad text")?.to_owned();
            try_core(|c| c.ctx.ai.reply(id, &text).map(|()| json!({})))
        }
        "ai.task.pause" | "ai.task.resume" | "ai.task.cancel" | "ai.task.decide" => {
            let id = req.params["id"].as_u64().ok_or("bad task id")?;
            let approve = req.params["approve"].as_bool();
            try_core(|c| {
                match req.method.as_str() {
                    "ai.task.pause" => c.ctx.ai.pause(id, ai::Pause::HostUser),
                    "ai.task.resume" => c.ctx.ai.resume(id),
                    "ai.task.cancel" => c.ctx.ai.cancel(id),
                    _ => c.ctx.ai.decide(id, approve.ok_or("bad decision")?),
                }
                .map(|()| json!({}))
            })
        }
        "ai.tasks" => with_core(|c| c.ctx.ai.tasks()),
        "ai.history.clear" => try_core(|c| c.ctx.ai.clear().map(|()| json!({}))),
        "ai.draft.set" => {
            let text = req.params["text"].as_str().ok_or("bad draft")?;
            try_core(|c| c.ctx.ai.set_draft(text).map(|()| json!({})))
        }
        "ai.stop" => with_core(|c| {
            c.ctx.ai.stop_all();
            json!({})
        }),
        other => Err(format!("unknown method {other:?}")),
    }
}

/// A controller tunnel's next bytes for the GUI, as packet 14, for
/// `kyokai_media_read`.
/// Returns: None when `session` is no tunnel; else the packet, or why there
/// is none (the wait ran out, or the tunnel ended).
fn tunnel_read(session: u64, wait: std::time::Duration) -> Option<Result<Vec<u8>, media::Empty>> {
    // The core's lock is held only to find the tunnel, never while waiting.
    let (ends, rt) = {
        let core = CORE.lock().ok()?;
        let c = core.as_ref()?;
        (c.ctx.sessions.tunnel(session)?, c.network.handle())
    };
    let next = rt.block_on(async {
        tokio::time::timeout(wait, async { ends.down.lock().await.recv().await }).await
    });
    Some(match next {
        Ok(Some(bytes)) => {
            let mut packet = Vec::with_capacity(1 + bytes.len());
            packet.push(media::packet::TUNNEL);
            packet.extend_from_slice(&bytes);
            Ok(packet)
        }
        Ok(None) => Err(media::Empty::Closed),
        Err(_) => Err(media::Empty::Timeout),
    })
}

/// Passes the GUI's bytes to a controller tunnel, waiting for room, for
/// `kyokai_media_write` (outside any runtime).
/// Returns: 0, or -2 when there is no such tunnel or it ended.
fn tunnel_write(session: u64, bytes: &[u8]) -> i32 {
    let ends = CORE
        .lock()
        .ok()
        .and_then(|c| c.as_ref()?.ctx.sessions.tunnel(session));
    let Some(ends) = ends else {
        return -2;
    };
    for piece in bytes.chunks(tunnel::PIECE) {
        if ends.up.blocking_send(piece.to_vec()).is_err() {
            return -2;
        }
    }
    0
}

/// Reads a credential request's `device_id` and `service`.
fn device_service(params: &Value) -> Result<(String, tunnel::Service), String> {
    let device = params["device_id"].as_str().ok_or("bad Device ID")?;
    let service = serde_json::from_value(params["service"].clone())
        .map_err(|_| "bad service: ssh, rdp or vnc")?;
    Ok((device.to_owned(), service))
}

fn with_core(f: impl FnOnce(&mut Core) -> Value) -> Result<Value, String> {
    try_core(|c| Ok(f(c)))
}

fn try_core(f: impl FnOnce(&mut Core) -> Result<Value, String>) -> Result<Value, String> {
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    let core = guard.as_mut().ok_or("the core is not started")?;
    f(core)
}

/// Shortest permanent password accepted.
const MIN_PERMANENT: usize = 8;

/// The status a host shows: never a secret.
fn status() -> Value {
    with_core(|c| {
        json!({
            "version": c.ctx.version,
            "abi": ABI_VERSION,
            "device_id": c.ctx.identity.device_id(),
            "fingerprint": c.ctx.identity.fingerprint(),
            "state_dir": c.state_dir,
            "persistence": c.persistence,
            "password_serial": c.password.serial(),
            "network": c.network.status(),
            "permanent_password": c.ctx.access.slots.lock().is_ok_and(|s| s.has_permanent()),
            "password_grants": c.ctx.access.password_grants(),
            "password_mode": c.ctx.access.password_mode(),
            "without_asking": c.ctx.access.without_asking(),
            "service_ports": c.ctx.access.service_ports().status(),
            "by_address": c.network.by_address_status(),
            "keeps_history": c.ctx.access.keeps_history(),
            "protected": c.ctx.access.protected(),
            "permissions": permissions(c.persistence),
            "ai": c.ctx.ai.active(),
            "ai_draft": c.ctx.ai.draft().1,
            "requests": c.ctx.access.pending(),
            "sessions": c.ctx.sessions.list(),
            "watching": c.ctx.watching.list(),
            "watchers": c.ctx.sessions.watchers(),
            "recent": c
                .ctx
                .access
                .recent()
                .into_iter()
                .map(|(id, h)| {
                    let password = c.ctx.access.saved_password(&id).is_some();
                    let saved = c.ctx.access.saved(&id);
                    let key = saved
                        .key
                        .as_ref()
                        .is_some_and(|k| keys::is_login(c.ctx.access.secrets(), k));
                    json!({
                        "device_id": id, "last": h.last, "about": h.about, "saved_password": password,
                        "routes": routes_json(&h, &c.network.probes(&id)),
                        "network_route": h.network, "prefer": h.prefer, "features": h.features,
                        "saved": {
                            "password": password, "key": key, "ssh": saved.ssh.is_some(),
                            "rdp": saved.rdp.is_some(), "vnc": saved.vnc.is_some(),
                        },
                    })
                })
                .collect::<Value>(),
            "name": c.ctx.about.lock().map(|l| l.chosen().to_owned()).unwrap_or_default(),
            "serve": c.ctx.access.serve(),
            "about": c.ctx.about(),
        })
    })
    .unwrap_or_else(|e| json!({"error": e}))
}

/// A known host's addresses for the status: each with what earlier
/// sessions remember of it and the latest look at it (`probe`, or null).
fn routes_json(host: &access::KnownHost, probes: &[net::Probe]) -> Value {
    host.routes
        .iter()
        .map(|r| {
            json!({
                "address": r.address, "added": r.added, "last_ok": r.stats.last_ok,
                "rtt_ms": r.stats.rtt_ms, "kbps": r.stats.kbps, "fails": r.stats.fails,
                "probe": probes.iter().find(|p| p.address == r.address),
            })
        })
        .collect()
}

/// The privacy permissions this system asks for and whether they are
/// granted (macOS, with whether the Keychain was refused), or null where it
/// asks for none.
fn permissions(persistence: store::Persistence) -> Value {
    #[cfg(target_os = "macos")]
    {
        let keychain = !matches!(persistence, store::Persistence::Denied);
        // The helper (root) captures and injects through its agent, whose
        // own permissions count; none while no agent is connected.
        let mut p = match agent::link() {
            Some(link) => link
                .call::<mac::Permissions>("permissions", json!({}))
                .unwrap_or(mac::Permissions {
                    screen: false,
                    accessibility: false,
                    input_monitoring: false,
                    keychain,
                }),
            None => mac::permissions(keychain),
        };
        p.keychain = keychain;
        json!(p)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = persistence;
        Value::Null
    }
}

/// Asks the system for a permission (macOS: `screen`, `accessibility`,
/// `input-monitoring`, `keychain`).
/// Returns: whether it is granted now.
/// Side effects: macOS may show its prompt; `keychain` opens the secrets
/// store again and, when it opens, rebuilds the core on it.
fn request_permission(name: &str) -> Result<bool, String> {
    if name == "keychain" {
        return reopen_store();
    }
    #[cfg(target_os = "macos")]
    {
        match agent::link() {
            Some(link) => link.call("permission", json!({"name": name})),
            None => mac::request(name),
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = name;
        Err("this system asks for no permissions".into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn roundtrip(
        f: unsafe extern "C" fn(*const u8, usize, *mut *mut u8, *mut usize) -> i32,
        input: &str,
    ) -> Value {
        let (mut out, mut len) = (std::ptr::null_mut(), 0usize);
        // SAFETY: valid input slice and output pointers; the buffer is freed below.
        let rc = unsafe { f(input.as_ptr(), input.len(), &mut out, &mut len) };
        assert_eq!(rc, 0);
        // SAFETY: the core returned `out`/`len`.
        let v = serde_json::from_slice(unsafe { std::slice::from_raw_parts(out, len) }).unwrap();
        // SAFETY: freed once.
        unsafe { kyokai_free(out, len) };
        v
    }

    #[test]
    fn abi_version_is_exported_unchanged() {
        assert_eq!(kyokai_abi_version(), ABI_VERSION);
    }

    #[test]
    fn calls_report_errors_as_json_and_password_rotates() {
        let before = roundtrip(kyokai_call, r#"{"method":"status"}"#);
        assert_eq!(before["result"]["error"], "the core is not started");
        let dir = std::env::temp_dir().join(format!("kyokai-core-test-{}", std::process::id()));
        // A fresh profile per run: a Keychain item made by another test binary
        // would raise an access prompt that nobody answers on CI.
        let profile = format!("test-{}", std::process::id());
        // The Keychain item outlives the state directory; remove it even when
        // the test fails.
        struct Forget<'a>(&'a str);
        impl Drop for Forget<'_> {
            fn drop(&mut self) {
                store::forget_master_key(self.0);
            }
        }
        let _forget = Forget(&profile);
        let cfg = json!({"version": "0.0.0", "state_dir": dir, "profile": profile}).to_string();
        let started = roundtrip(kyokai_start, &cfg);
        assert_eq!(started["ok"], true, "{started}");
        let id = started["result"]["device_id"].as_str().unwrap().to_owned();
        assert_eq!(id.len(), 9);
        let first =
            roundtrip(kyokai_call, r#"{"method":"password.reveal"}"#)["result"]["temporary"]
                .clone();
        roundtrip(kyokai_call, r#"{"method":"password.regenerate"}"#);
        let second =
            roundtrip(kyokai_call, r#"{"method":"password.reveal"}"#)["result"]["temporary"]
                .clone();
        assert_ne!(first, second);
        let unknown = roundtrip(kyokai_call, r#"{"method":"nope"}"#);
        assert_eq!(unknown["ok"], false);
        // A new identity keeps whom the host serves (regression: a helper
        // without a GUI served everyone after a store moved in).
        roundtrip(
            kyokai_call,
            r#"{"method":"host.serve","params":{"serve":"none"}}"#,
        );
        let reset = roundtrip(kyokai_call, r#"{"method":"identity.reset"}"#);
        assert_eq!(reset["ok"], true, "{reset}");
        let after = roundtrip(kyokai_call, r#"{"method":"status"}"#);
        assert_eq!(after["result"]["serve"], "none");
        kyokai_stop();
        let _ = std::fs::remove_dir_all(dir);
        // Regression: every run left a `master-key.test-<pid>` Keychain item.
        assert!(
            store::forget_master_key(&profile),
            "the Keychain item remains"
        );
    }
}
