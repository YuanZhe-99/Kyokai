//! The Kyokai core: the native library that the GUI and the engine load with
//! purego (doc/en-us/architecture.md, "FFI").
//!
//! The C ABI takes and returns integers and pointers only. Requests and
//! results are JSON documents: `kyokai_start` and `kyokai_call` write a
//! response `{"ok": true, "result": …}` or `{"ok": false, "error": "…"}` into a
//! buffer the core allocates, which the caller hands back to `kyokai_free`.

mod identity;
mod net;
mod password;
mod store;

use serde::Deserialize;
use serde_json::{Value, json};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::PathBuf;
use std::sync::Mutex;

/// Version of the C ABI. Hosts compare it with their own before any other
/// call; changing an export's signature or meaning bumps it.
pub const ABI_VERSION: u32 = 1;

/// The running core: what `kyokai_start` set up, until `kyokai_stop`.
struct Core {
    version: String,
    state_dir: PathBuf,
    identity: identity::Identity,
    persistence: store::Persistence,
    secrets: store::Secrets,
    password: password::Temporary,
    network: net::Network,
}

static CORE: Mutex<Option<Core>> = Mutex::new(None);

/// What the host passes to `kyokai_start`.
#[derive(Deserialize)]
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
    std::fs::create_dir_all(&cfg.state_dir).map_err(|e| format!("state directory: {e}"))?;
    let (secrets, persistence) = store::open(&cfg.state_dir, &cfg.profile);
    let identity = identity::Identity::load_or_create(&secrets)?;
    let network = net::Network::start(&secrets, &identity.device_id())?;
    *guard = Some(Core {
        version: cfg.version,
        state_dir: cfg.state_dir,
        identity,
        persistence,
        secrets,
        password: password::Temporary::generate(),
        network,
    });
    Ok(())
}

fn call(input: &[u8]) -> Result<Value, String> {
    let req: Call = serde_json::from_slice(input).map_err(|e| format!("bad call: {e}"))?;
    match req.method.as_str() {
        "status" => Ok(status()),
        "password.reveal" => with_core(|c| json!({"temporary": c.password.reveal()})),
        "password.regenerate" => with_core(|c| {
            c.password = password::Temporary::generate();
            json!({"serial": c.password.serial()})
        }),
        "network.get" => with_core(|c| c.network.settings()),
        "network.set" => {
            let update: net::Update = serde_json::from_value(req.params)
                .map_err(|e| format!("bad network settings: {e}"))?;
            let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
            let c = guard.as_mut().ok_or("the core is not started")?;
            c.network.update(&c.secrets, update)?;
            Ok(c.network.settings())
        }
        other => Err(format!("unknown method {other:?}")),
    }
}

fn with_core(f: impl FnOnce(&mut Core) -> Value) -> Result<Value, String> {
    let mut guard = CORE.lock().map_err(|_| "core state poisoned")?;
    let core = guard.as_mut().ok_or("the core is not started")?;
    Ok(f(core))
}

/// The status a host shows: never a secret.
fn status() -> Value {
    with_core(|c| {
        json!({
            "version": c.version,
            "abi": ABI_VERSION,
            "device_id": c.identity.device_id(),
            "fingerprint": c.identity.fingerprint(),
            "state_dir": c.state_dir,
            "persistence": c.persistence,
            "password_serial": c.password.serial(),
            "network": c.network.status(),
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
