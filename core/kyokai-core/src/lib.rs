//! The Kyokai core: the native library that the GUI and the engine load with
//! purego (doc/en-us/architecture.md, "FFI").
//!
//! The C ABI takes and returns integers and pointers only. Requests and
//! results are JSON documents: `kyokai_start` and `kyokai_call` write a
//! response `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` into a
//! buffer the core allocates, which the caller hands back to `kyokai_free`.

mod access;
mod auth;
mod backup;
mod identity;
mod net;
mod password;
mod session;
mod store;

use serde::Deserialize;
use serde_json::{Value, json};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

/// Version of the C ABI. Hosts compare it with their own before any other
/// call; changing an export's signature or meaning bumps it.
pub const ABI_VERSION: u32 = 1;

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
    let secrets = Arc::new(secrets);
    let identity = Arc::new(identity::Identity::load_or_create(&secrets)?);
    let ctx = Arc::new(session::Ctx {
        identity,
        access: Arc::new(access::Access::load(secrets.clone())?),
        sessions: Arc::default(),
        version: cfg.version.clone(),
    });
    let network = net::Network::start(&secrets, ctx.clone())?;
    let mut core = Core {
        state_dir: cfg.state_dir.clone(),
        cfg,
        persistence,
        ctx,
        password: password::Temporary::generate(),
        network,
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
    *guard = None; // stops the network and drops the old identity
    let core = build(cfg)?;
    let id = core.ctx.identity.device_id();
    *guard = Some(core);
    Ok(json!({"device_id": id}))
}

fn call(input: &[u8]) -> Result<Value, String> {
    let mut req: Call = serde_json::from_slice(input).map_err(|e| format!("bad call: {e}"))?;
    match req.method.as_str() {
        "status" => Ok(status()),
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
                    })
                })
                .collect::<Value>()
        }),
        "trusted.remove" => {
            let key = req.params["key"].as_str().ok_or("bad key")?.to_owned();
            try_core(|c| c.ctx.access.untrust(&key).map(|()| json!({})))
        }
        "host.forget" => {
            let id = req.params["device_id"]
                .as_str()
                .ok_or("bad Device ID")?
                .to_owned();
            try_core(|c| c.ctx.access.forget_host(&id).map(|()| json!({})))
        }
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
            "requests": c.ctx.access.pending(),
            "sessions": c.ctx.sessions.list(),
            "recent": c
                .ctx
                .access
                .recent()
                .into_iter()
                .map(|(id, last)| json!({"device_id": id, "last": last}))
                .collect::<Value>(),
        })
    })
    .unwrap_or_else(|e| json!({"error": e}))
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
        kyokai_stop();
        let _ = std::fs::remove_dir_all(dir);
    }
}
