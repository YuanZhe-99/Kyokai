//! The secrets store: one JSON map of secrets, encrypted with
//! XChaCha20-Poly1305 under a random master key that the operating system
//! protects (Windows DPAPI for the user; the macOS login Keychain).
//!
//! Without a working OS store nothing is persisted: secrets live in memory
//! for this run only. An existing store that cannot be decrypted is never
//! overwritten.

use chacha20poly1305::aead::{Aead, KeyInit, Payload};
use chacha20poly1305::{XChaCha20Poly1305, XNonce};
use serde::Serialize;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use zeroize::Zeroizing;

const FILE: &str = "secrets.bin";
const AAD: &[u8] = b"kyokai/secrets/v1";
const NONCE_LEN: usize = 24;

/// How secrets are kept in this run.
#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Persistence {
    /// Encrypted on disk under an OS-protected key.
    OsStore,
    /// No OS credential store: memory only, gone at exit.
    MemoryOnly,
    /// A store exists but cannot be decrypted (its key is gone); it is left
    /// untouched and this run keeps secrets in memory.
    Unreadable,
}

type Map = BTreeMap<String, Zeroizing<String>>;

/// The secrets of this installation.
pub struct Secrets {
    map: Mutex<Map>,
    /// Where and with what key to persist; None keeps everything in memory.
    disk: Option<(PathBuf, Zeroizing<[u8; 32]>)>,
}

/// Opens the store in state_dir, keyed in the OS store by profile.
pub fn open(state_dir: &Path, profile: &str) -> (Secrets, Persistence) {
    let path = state_dir.join(FILE);
    let memory = |p| (Secrets::memory(), p);
    let Some(key) = os::master_key(state_dir, profile) else {
        return memory(Persistence::MemoryOnly);
    };
    let map = match std::fs::read(&path) {
        Ok(data) => match decrypt(&key, &data) {
            Some(map) => map,
            None => return memory(Persistence::Unreadable),
        },
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Map::new(),
        Err(_) => return memory(Persistence::Unreadable),
    };
    (
        Secrets {
            map: Mutex::new(map),
            disk: Some((path, key)),
        },
        Persistence::OsStore,
    )
}

impl Secrets {
    /// Returns a copy of the secret stored under name.
    pub fn get(&self, name: &str) -> Option<Zeroizing<String>> {
        self.map.lock().ok()?.get(name).cloned()
    }

    /// A store that lives in memory only (tests, and runs without an OS
    /// credential store).
    pub fn memory() -> Self {
        Secrets {
            map: Mutex::new(Map::new()),
            disk: None,
        }
    }

    /// Stores a secret and, when the store persists, rewrites the file
    /// atomically.
    pub fn set(&self, name: &str, value: &str) -> Result<(), String> {
        self.write(|map| {
            map.insert(name.to_owned(), Zeroizing::new(value.to_owned()));
        })
    }

    /// Removes a secret, rewriting the file like `set`.
    pub fn remove(&self, name: &str) -> Result<(), String> {
        self.write(|map| {
            map.remove(name);
        })
    }

    fn write(&self, change: impl FnOnce(&mut Map)) -> Result<(), String> {
        let mut map = self.map.lock().map_err(|_| "secrets store poisoned")?;
        change(&mut map);
        let Some((path, key)) = &self.disk else {
            return Ok(());
        };
        write_atomic(path, &encrypt(key, &map)?)
    }
}

/// Replaces a file through a temporary file next to it, so a crash leaves
/// either the old or the new contents.
pub fn write_atomic(path: &Path, data: &[u8]) -> Result<(), String> {
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, data).map_err(|e| format!("write {}: {e}", path.display()))?;
    std::fs::rename(&tmp, path).map_err(|e| format!("replace {}: {e}", path.display()))
}

fn encrypt(key: &[u8; 32], map: &Map) -> Result<Vec<u8>, String> {
    let plain = Zeroizing::new(
        serde_json::to_vec(
            &map.iter()
                .map(|(k, v)| (k, v.as_str()))
                .collect::<BTreeMap<_, _>>(),
        )
        .map_err(|e| e.to_string())?,
    );
    let mut nonce = [0u8; NONCE_LEN];
    getrandom::fill(&mut nonce).map_err(|e| format!("OS random source: {e}"))?;
    let cipher = XChaCha20Poly1305::new_from_slice(key).map_err(|_| "bad key")?;
    let sealed = cipher
        .encrypt(
            &XNonce::from(nonce),
            Payload {
                msg: &plain,
                aad: AAD,
            },
        )
        .map_err(|_| "encryption failed")?;
    Ok([nonce.as_slice(), &sealed].concat())
}

fn decrypt(key: &[u8; 32], data: &[u8]) -> Option<Map> {
    let (nonce, sealed) = data.split_at_checked(NONCE_LEN)?;
    let cipher = XChaCha20Poly1305::new_from_slice(key).ok()?;
    let nonce: [u8; NONCE_LEN] = nonce.try_into().ok()?;
    let plain = Zeroizing::new(
        cipher
            .decrypt(
                &XNonce::from(nonce),
                Payload {
                    msg: sealed,
                    aad: AAD,
                },
            )
            .ok()?,
    );
    let raw: BTreeMap<String, String> = serde_json::from_slice(&plain).ok()?;
    Some(
        raw.into_iter()
            .map(|(k, v)| (k, Zeroizing::new(v)))
            .collect(),
    )
}

fn new_key() -> Option<Zeroizing<[u8; 32]>> {
    let mut key = Zeroizing::new([0u8; 32]);
    getrandom::fill(key.as_mut()).ok()?;
    Some(key)
}

#[cfg(windows)]
mod os {
    //! The master key as a DPAPI blob (user scope) next to the store.
    use super::new_key;
    use std::path::Path;
    use windows_sys::Win32::Foundation::LocalFree;
    use windows_sys::Win32::Security::Cryptography::{
        CRYPT_INTEGER_BLOB, CRYPTPROTECT_UI_FORBIDDEN, CryptProtectData, CryptUnprotectData,
    };
    use zeroize::Zeroizing;

    const FILE: &str = "master.key";

    /// Reads the master key, or creates and protects a new one.
    pub fn master_key(state_dir: &Path, _profile: &str) -> Option<Zeroizing<[u8; 32]>> {
        let path = state_dir.join(FILE);
        if let Ok(blob) = std::fs::read(&path) {
            let plain = Zeroizing::new(dpapi(&blob, false)?);
            return Some(Zeroizing::new(plain.as_slice().try_into().ok()?));
        }
        let key = new_key()?;
        std::fs::write(&path, dpapi(key.as_ref(), true)?).ok()?;
        Some(key)
    }

    /// Protects (or unprotects) data for the current user.
    fn dpapi(data: &[u8], protect: bool) -> Option<Vec<u8>> {
        let input = CRYPT_INTEGER_BLOB {
            cbData: data.len() as u32,
            pbData: data.as_ptr() as *mut u8,
        };
        let mut output = CRYPT_INTEGER_BLOB {
            cbData: 0,
            pbData: std::ptr::null_mut(),
        };
        // SAFETY: input points to data.len() readable bytes for the call;
        // output is a valid out-parameter that DPAPI fills with a
        // LocalAlloc'd buffer, freed below.
        let ok = unsafe {
            if protect {
                CryptProtectData(
                    &input,
                    std::ptr::null(),
                    std::ptr::null(),
                    std::ptr::null(),
                    std::ptr::null(),
                    CRYPTPROTECT_UI_FORBIDDEN,
                    &mut output,
                )
            } else {
                CryptUnprotectData(
                    &input,
                    std::ptr::null_mut(),
                    std::ptr::null(),
                    std::ptr::null(),
                    std::ptr::null(),
                    CRYPTPROTECT_UI_FORBIDDEN,
                    &mut output,
                )
            }
        };
        if ok == 0 || output.pbData.is_null() {
            return None;
        }
        // SAFETY: DPAPI returned cbData bytes at pbData.
        let out =
            unsafe { std::slice::from_raw_parts(output.pbData, output.cbData as usize) }.to_vec();
        // SAFETY: pbData was allocated by DPAPI with LocalAlloc.
        unsafe { LocalFree(output.pbData as _) };
        Some(out)
    }
}

#[cfg(target_os = "macos")]
mod os {
    //! The master key as a generic password in the login Keychain.
    use super::new_key;
    use base64::Engine;
    use base64::engine::general_purpose::STANDARD;
    use security_framework::passwords::{get_generic_password, set_generic_password};
    use std::path::Path;
    use zeroize::Zeroizing;

    const SERVICE: &str = "com.yuanzhe.kyokai";

    /// Reads the master key, or creates and stores a new one.
    pub fn master_key(_state_dir: &Path, profile: &str) -> Option<Zeroizing<[u8; 32]>> {
        let account = if profile.is_empty() {
            "master-key".to_owned()
        } else {
            format!("master-key.{profile}")
        };
        if let Ok(text) = get_generic_password(SERVICE, &account) {
            let raw = Zeroizing::new(STANDARD.decode(&text).ok()?);
            return Some(Zeroizing::new(raw.as_slice().try_into().ok()?));
        }
        let key = new_key()?;
        let text = Zeroizing::new(STANDARD.encode(key.as_ref()));
        set_generic_password(SERVICE, &account, text.as_bytes()).ok()?;
        Some(key)
    }
}

#[cfg(not(any(windows, target_os = "macos")))]
mod os {
    //! No supported OS credential store.
    use std::path::Path;
    use zeroize::Zeroizing;

    pub fn master_key(_state_dir: &Path, _profile: &str) -> Option<Zeroizing<[u8; 32]>> {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sealed_maps_round_trip_and_reject_tampering() {
        let key = [9u8; 32];
        let mut map = Map::new();
        map.insert("device_key".into(), Zeroizing::new("abc".into()));
        let mut data = encrypt(&key, &map).unwrap();
        assert_eq!(decrypt(&key, &data).unwrap()["device_key"].as_str(), "abc");
        assert!(decrypt(&[8u8; 32], &data).is_none());
        let last = data.len() - 1;
        data[last] ^= 1;
        assert!(decrypt(&key, &data).is_none());
    }
}
