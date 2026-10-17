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
mod auth;
mod backup;
mod clipboard;
mod decode;
mod files;
mod identity;
mod input;
mod keyboard;
#[cfg(target_os = "macos")]
mod mac;
mod media;
#[cfg(windows)]
mod mf;
mod net;
mod password;
mod power;
#[cfg(windows)]
mod privacy;
mod session;
mod soft;
mod store;
mod video;

use serde::Deserialize;
use serde_json::{Value, json};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

/// Version of the C ABI. Hosts compare it with their own before any other
/// call; changing an export's signature or meaning bumps it.
pub const ABI_VERSION: u32 = 3;

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
    password: zeroize::Zeroizing<String>,
    #[serde(default)]
    want: Vec<access::Permission>,
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
    /// as the session agent in the console session.
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
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_stop() {
    let _ = catch_unwind(|| {
        if let Ok(mut core) = CORE.lock() {
            *core = None;
        }
    });
}

/// Waits for the next media packet of a controller session, for the GUI
/// (doc/en-us/architecture.md, "FFI").
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
    let got = catch_unwind(|| {
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
/// (`media::packet::CONTROL` + JSON) for the host.
/// Inputs: `ptr`/`len` — the packet, borrowed for the call.
/// Returns: 0; -1 for a malformed packet; -2 when there is no such session
/// or it is not taking messages (full or ended).
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
        let Some((&media::packet::CONTROL, json)) = packet.split_first() else {
            return -1;
        };
        let Ok(msg) = serde_json::from_slice::<Value>(json) else {
            return -1;
        };
        let sender = CORE
            .lock()
            .ok()
            .and_then(|c| c.as_ref()?.ctx.sessions.media(session))
            .map(|(_, tx)| tx);
        match sender {
            Some(tx) if tx.try_send(msg).is_ok() => 0,
            _ => -2,
        }
    })
    .unwrap_or(-2)
}

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

/// Closes a viewer decoder; an unknown handle is ignored.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_decoder_close(id: u64) {
    let _ = catch_unwind(|| decode::close(id));
}

/// Grabs the keyboard for a viewer window, or ends its grab
/// (doc/en-us/computer-use.md, "Keyboard modes"). Needs no started core.
/// Inputs: `window` — the viewer's native window handle; `mode`/`len` — the
/// mode's name (`map`, `translate`, `off`), borrowed for the call.
/// Returns: 0; -1 for an unknown mode or no window; -2 when the keyboard cannot be
/// grabbed here (no hook, or not Windows yet).
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
    let Some(mode) = std::str::from_utf8(name)
        .ok()
        .and_then(keyboard::Mode::from_name)
    else {
        return -1;
    };
    // No window is in front at times (handle 0): a grab for it would take
    // keys from whatever comes next.
    if window == 0 && mode.is_some() {
        return -1;
    }
    match catch_unwind(|| keyboard::grab(window, mode)) {
        Ok(true) => 0,
        Ok(false) if mode.is_none() => 0,
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
    if out.is_null() || !cfg!(windows) {
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

/// Runs `f` on the request bytes and writes its JSON response, catching
/// panics so none crosses the C boundary.
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
        version: cfg.version.clone(),
        about: std::sync::Mutex::new(about::Local::load(&cfg.state_dir)),
    });
    #[cfg(windows)]
    if let Some(exe) = &cfg.agent {
        agent::start_helper(exe.clone());
    }
    let network = net::Network::start(&secrets, ctx.clone(), cfg.agent.is_some())?;
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
                let doc = backup::export_store(c.ctx.access.secrets(), &c.state_dir, &passphrase)?;
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
            let dir = try_core(|c| Ok(json!(c.state_dir)))?;
            let dir = PathBuf::from(dir.as_str().unwrap_or_default());
            replace_identity(|s| backup::import_store(s, &dir, &document, &passphrase))
        }
        "store.clear" => try_core(|c| {
            // The caller stops this engine next: it must not serve the
            // identity the other engine now holds.
            c.ctx.access.set_serve(access::Serve::None);
            c.ctx.sessions.close_hosts();
            backup::clear_store(c.ctx.access.secrets(), &c.state_dir).map(|()| json!({}))
        }),
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
                let id = c.network.connect(&r.device_id, r.password, r.want)?;
                Ok(json!({"id": id}))
            })
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
        "trusted.unattended" => {
            let key = req.params["key"].as_str().ok_or("bad key")?.to_owned();
            let days = req.params["days"].as_u64().ok_or("bad days")?;
            try_core(|c| c.ctx.access.set_unattended(&key, days).map(|()| json!({})))
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
                        "unattended_until": t.unattended_until,
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
        "ai.stop" => with_core(|c| {
            c.ctx.ai.stop_all();
            json!({})
        }),
        other => Err(format!("unknown method {other:?}")),
    }
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
            "permissions": permissions(c.persistence),
            "ai": c.ctx.ai.active(),
            "requests": c.ctx.access.pending(),
            "sessions": c.ctx.sessions.list(),
            "recent": c
                .ctx
                .access
                .recent()
                .into_iter()
                .map(|(id, h)| json!({"device_id": id, "last": h.last, "about": h.about}))
                .collect::<Value>(),
            "name": c.ctx.about.lock().map(|l| l.chosen().to_owned()).unwrap_or_default(),
            "serve": c.ctx.access.serve(),
            "about": c.ctx.about(),
        })
    })
    .unwrap_or_else(|e| json!({"error": e}))
}

/// The privacy permissions this system asks for and whether they are
/// granted (macOS, with whether the Keychain was refused), or null where it
/// asks for none.
fn permissions(persistence: store::Persistence) -> Value {
    #[cfg(target_os = "macos")]
    {
        json!(mac::permissions(!matches!(
            persistence,
            store::Persistence::Denied
        )))
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = persistence;
        Value::Null
    }
}

/// Asks the system for a permission (macOS: `screen`, `accessibility`,
/// `keychain`).
/// Returns: whether it is granted now.
/// Side effects: macOS may show its prompt; `keychain` opens the secrets
/// store again and, when it opens, rebuilds the core on it.
fn request_permission(name: &str) -> Result<bool, String> {
    if name == "keychain" {
        return reopen_store();
    }
    #[cfg(target_os = "macos")]
    {
        mac::request(name)
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
    }
}
