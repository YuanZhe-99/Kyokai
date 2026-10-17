//! Identity backup, restore and reset (doc/en-us/security.md): the device
//! key and everything bound to it travel in a file encrypted under a
//! passphrase, never in the clear.

use crate::store::Secrets;
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use chacha20poly1305::aead::{Aead, KeyInit, Payload};
use chacha20poly1305::{XChaCha20Poly1305, XNonce};
use opaque_ke::argon2::{Algorithm, Argon2, Params, Version};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::Path;
use zeroize::Zeroizing;

/// The secrets that make up a device's identity: its key and what is bound
/// to it (the password records use the key; the trusted devices and known
/// hosts were decided by this identity).
pub const IDENTITY_SECRETS: &[&str] = &[
    "device_key",
    "opaque_setup",
    "opaque_permanent",
    "trusted_devices",
    "known_hosts",
];
const FORMAT: &str = "kyokai-identity";
const VERSION: u32 = 1;
const AAD: &[u8] = b"kyokai/identity-backup/v1";
/// Shortest passphrase accepted for a backup.
pub const MIN_PASSPHRASE: usize = 10;
/// Argon2id cost for backups: 64 MiB, 3 passes (a backup is opened rarely).
#[cfg(not(test))]
const KDF_M: u32 = 64 * 1024;
#[cfg(not(test))]
const KDF_T: u32 = 3;
// Tests check the format and the checks, not the cost: keep them quick.
#[cfg(test)]
const KDF_M: u32 = 64;
#[cfg(test)]
const KDF_T: u32 = 1;

#[derive(Serialize, Deserialize)]
struct Kdf {
    alg: String,
    m: u32,
    t: u32,
    p: u32,
    salt: String,
}

#[derive(Serialize, Deserialize)]
struct File {
    format: String,
    version: u32,
    kdf: Kdf,
    nonce: String,
    data: String,
}

/// Writes the identity into a passphrase-protected backup document.
/// Returns: the document (JSON text) for the host to save.
pub fn export(secrets: &Secrets, passphrase: &str) -> Result<String, String> {
    if passphrase.chars().count() < MIN_PASSPHRASE {
        return Err(format!(
            "the passphrase needs at least {MIN_PASSPHRASE} characters"
        ));
    }
    let entries: BTreeMap<String, Zeroizing<String>> = IDENTITY_SECRETS
        .iter()
        .filter_map(|name| secrets.get(name).map(|v| (name.to_string(), v)))
        .collect();
    if !entries.contains_key("device_key") {
        return Err("there is no identity to back up".into());
    }
    seal(&entries, passphrase, FORMAT, AAD)
}

/// Replaces this device's identity with the one in a backup document.
/// Side effects: rewrites the identity's secrets (absent ones are removed);
/// the caller restarts the core so the new identity takes effect.
/// Notes: the document is checked completely before anything is written.
pub fn import(secrets: &Secrets, document: &str, passphrase: &str) -> Result<(), String> {
    let entries = open(document, passphrase, FORMAT, AAD)?;
    check_identity(&entries, |k| IDENTITY_SECRETS.contains(&k))?;
    for name in IDENTITY_SECRETS {
        match entries.get(*name) {
            Some(v) => secrets.set(name, v)?,
            None => secrets.remove(name)?,
        }
    }
    Ok(())
}

/// What a move between run modes carries besides the identity: the network
/// settings, every AI source's key (`ai.<source>.key`), and the state files
/// that belong to the device.
const MOVE_SECRETS: &[&str] = &["network"];

/// Whether a secret's name is an AI source's key.
fn is_ai_key(name: &str) -> bool {
    name.starts_with("ai.") && name.ends_with(".key")
}

/// The secrets a move carries from `secrets`.
fn moved(secrets: &Secrets) -> Vec<String> {
    secrets
        .names()
        .into_iter()
        .filter(|n| {
            IDENTITY_SECRETS.contains(&n.as_str())
                || MOVE_SECRETS.contains(&n.as_str())
                || is_ai_key(n)
        })
        .collect()
}
const MOVE_FILES: &[&str] = &["device.json", "ai.json"];
const MOVE_FORMAT: &str = "kyokai-move";
const MOVE_AAD: &[u8] = b"kyokai/store-move/v1";

/// Seals this engine's whole store for another engine of the same device
/// (a run-mode change, doc/en-us/run-modes.md): the identity, the network
/// secret, the AI keys, the friendly name and the AI settings.
/// Returns: the document; `passphrase` is a one-time secret of the caller.
pub fn export_store(
    secrets: &Secrets,
    state_dir: &Path,
    passphrase: &str,
) -> Result<String, String> {
    let mut entries: BTreeMap<String, Zeroizing<String>> = moved(secrets)
        .into_iter()
        .filter_map(|name| secrets.get(&name).map(|v| (name, v)))
        .collect();
    if !entries.contains_key("device_key") {
        return Err("there is no identity to move".into());
    }
    for f in MOVE_FILES {
        if let Ok(text) = std::fs::read_to_string(state_dir.join(f)) {
            entries.insert(format!("file:{f}"), Zeroizing::new(text));
        }
    }
    seal(&entries, passphrase, MOVE_FORMAT, MOVE_AAD)
}

/// Takes over a store another engine of this device sealed.
/// Side effects: replaces every moved secret and file (absent ones are
/// removed); the caller restarts the core.
/// Notes: the document is checked completely before anything is written.
pub fn import_store(
    secrets: &Secrets,
    state_dir: &Path,
    document: &str,
    passphrase: &str,
) -> Result<(), String> {
    let entries = open(document, passphrase, MOVE_FORMAT, MOVE_AAD)?;
    check_identity(&entries, |k| {
        IDENTITY_SECRETS.contains(&k)
            || MOVE_SECRETS.contains(&k)
            || is_ai_key(k)
            || k.strip_prefix("file:")
                .is_some_and(|f| MOVE_FILES.contains(&f))
    })?;
    // What this engine held and the document does not is removed.
    for name in moved(secrets) {
        if !entries.contains_key(&name) {
            secrets.remove(&name)?;
        }
    }
    for (name, v) in entries.iter().filter(|(k, _)| !k.starts_with("file:")) {
        secrets.set(name, v)?;
    }
    for name in IDENTITY_SECRETS.iter().chain(MOVE_SECRETS) {
        if !entries.contains_key(*name) {
            secrets.remove(name)?;
        }
    }
    for f in MOVE_FILES {
        let path = state_dir.join(f);
        match entries.get(&format!("file:{f}")) {
            Some(text) => crate::store::write_atomic(&path, text.as_bytes())?,
            None => {
                let _ = std::fs::remove_file(&path);
            }
        }
    }
    Ok(())
}

/// Forgets everything a move carried, once the other engine holds it, so
/// one identity is never served by two engines.
/// Side effects: removes the secrets and files; the caller stops the engine.
pub fn clear_store(secrets: &Secrets, state_dir: &Path) -> Result<(), String> {
    for name in moved(secrets) {
        secrets.remove(&name)?;
    }
    for f in MOVE_FILES {
        let _ = std::fs::remove_file(state_dir.join(f));
    }
    Ok(())
}

/// Checks that a document holds a well-formed device key and only names
/// `allowed` accepts.
fn check_identity(
    entries: &BTreeMap<String, Zeroizing<String>>,
    allowed: impl Fn(&str) -> bool,
) -> Result<(), String> {
    let bad = || "this is not a Kyokai identity backup".to_string();
    let device_key = entries.get("device_key").ok_or_else(bad)?;
    let seed = Zeroizing::new(STANDARD.decode(device_key.as_bytes()).map_err(|_| bad())?);
    if seed.len() != 32 || entries.keys().any(|k| !allowed(k)) {
        return Err(bad());
    }
    Ok(())
}

/// Encrypts `entries` under a key derived from `passphrase`.
/// Returns: the document (JSON text).
fn seal(
    entries: &BTreeMap<String, Zeroizing<String>>,
    passphrase: &str,
    format: &str,
    aad: &[u8],
) -> Result<String, String> {
    let plain = Zeroizing::new(
        serde_json::to_vec(
            &entries
                .iter()
                .map(|(k, v)| (k.as_str(), v.as_str()))
                .collect::<BTreeMap<_, _>>(),
        )
        .map_err(|e| e.to_string())?,
    );
    let mut salt = [0u8; 16];
    let mut nonce = [0u8; 24];
    getrandom::fill(&mut salt).map_err(|e| format!("OS random source: {e}"))?;
    getrandom::fill(&mut nonce).map_err(|e| format!("OS random source: {e}"))?;
    let key = derive(passphrase, &salt, KDF_M, KDF_T, 1)?;
    let cipher = XChaCha20Poly1305::new_from_slice(key.as_ref()).map_err(|_| "bad key")?;
    let sealed = cipher
        .encrypt(&XNonce::from(nonce), Payload { msg: &plain, aad })
        .map_err(|_| "encryption failed")?;
    let file = File {
        format: format.into(),
        version: VERSION,
        kdf: Kdf {
            alg: "argon2id".into(),
            m: KDF_M,
            t: KDF_T,
            p: 1,
            salt: STANDARD.encode(salt),
        },
        nonce: STANDARD.encode(nonce),
        data: STANDARD.encode(sealed),
    };
    serde_json::to_string_pretty(&file).map_err(|e| e.to_string())
}

/// Decrypts a document of `format` with `passphrase`.
/// Returns: its entries.
fn open(
    document: &str,
    passphrase: &str,
    format: &str,
    aad: &[u8],
) -> Result<BTreeMap<String, Zeroizing<String>>, String> {
    let file: File =
        serde_json::from_str(document).map_err(|_| "this is not a Kyokai identity backup")?;
    if file.format != format || file.version != VERSION || file.kdf.alg != "argon2id" {
        return Err("this backup comes from an unsupported version".into());
    }
    // Bounded so a crafted file cannot make the KDF exhaust memory or time.
    if file.kdf.m > 1024 * 1024 || file.kdf.t > 16 || file.kdf.p != 1 {
        return Err("this backup has unsupported settings".into());
    }
    let bad = || "this is not a Kyokai identity backup".to_string();
    let salt = STANDARD.decode(&file.kdf.salt).map_err(|_| bad())?;
    let nonce: [u8; 24] = STANDARD
        .decode(&file.nonce)
        .ok()
        .and_then(|n| n.try_into().ok())
        .ok_or_else(bad)?;
    let sealed = STANDARD.decode(&file.data).map_err(|_| bad())?;
    let key = derive(passphrase, &salt, file.kdf.m, file.kdf.t, file.kdf.p)?;
    let cipher = XChaCha20Poly1305::new_from_slice(key.as_ref()).map_err(|_| "bad key")?;
    let plain = Zeroizing::new(
        cipher
            .decrypt(&XNonce::from(nonce), Payload { msg: &sealed, aad })
            .map_err(|_| "wrong passphrase, or the backup is damaged")?,
    );
    serde_json::from_slice(&plain).map_err(|_| bad())
}

/// Forgets this device's identity; the next start creates a new one (a new
/// Device ID), with no permanent password, trusted devices or known hosts.
/// Side effects: rewrites the secrets store; the caller restarts the core.
pub fn reset(secrets: &Secrets) -> Result<(), String> {
    for name in IDENTITY_SECRETS {
        secrets.remove(name)?;
    }
    Ok(())
}

fn derive(
    passphrase: &str,
    salt: &[u8],
    m: u32,
    t: u32,
    p: u32,
) -> Result<Zeroizing<[u8; 32]>, String> {
    let params = Params::new(m, t, p, Some(32)).map_err(|_| "unsupported backup settings")?;
    let mut key = Zeroizing::new([0u8; 32]);
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(passphrase.as_bytes(), salt, key.as_mut())
        .map_err(|_| "key derivation failed")?;
    Ok(key)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::identity::Identity;

    #[test]
    fn a_backup_restores_the_same_identity_and_only_with_its_passphrase() {
        let a = Secrets::memory();
        let id = Identity::load_or_create(&a).unwrap().device_id();
        a.set("trusted_devices", "{}").unwrap();
        let doc = export(&a, "correct horse battery").unwrap();
        assert!(
            !doc.contains(&a.get("device_key").unwrap().to_string()),
            "never in the clear"
        );
        assert!(export(&a, "short").is_err());

        let b = Secrets::memory();
        Identity::load_or_create(&b).unwrap();
        b.set("known_hosts", "{\"1\":\"k\"}").unwrap();
        assert!(import(&b, &doc, "wrong passphrase!").is_err());
        assert!(import(&b, "{}", "correct horse battery").is_err());
        import(&b, &doc, "correct horse battery").unwrap();
        assert_eq!(Identity::load_or_create(&b).unwrap().device_id(), id);
        assert!(b.get("known_hosts").is_none(), "absent entries are removed");
        assert!(b.get("trusted_devices").is_some());
    }

    #[test]
    fn a_move_carries_the_whole_store_once_and_is_no_identity_backup() {
        let dir = |n: &str| {
            let d = std::env::temp_dir().join(format!("kyokai-move-{n}-{}", std::process::id()));
            let _ = std::fs::remove_dir_all(&d);
            std::fs::create_dir_all(&d).unwrap();
            d
        };
        let (from_dir, to_dir) = (dir("from"), dir("to"));
        let from = Secrets::memory();
        let id = Identity::load_or_create(&from).unwrap().device_id();
        from.set("network", "{\"name\":\"n\"}").unwrap();
        from.set("ai.claude.key", "sk-test").unwrap();
        from.set("ai.openrouter.key", "sk-or").unwrap();
        std::fs::write(from_dir.join("device.json"), "{\"name\":\"Office\"}").unwrap();
        let doc = export_store(&from, &from_dir, "one-time passphrase").unwrap();
        assert!(!doc.contains("sk-test"), "never in the clear");
        // An identity backup and a move are not interchangeable.
        assert!(import(&Secrets::memory(), &doc, "one-time passphrase").is_err());
        let backup = export(&from, "one-time passphrase").unwrap();
        assert!(import_store(&Secrets::memory(), &to_dir, &backup, "one-time passphrase").is_err());

        let to = Secrets::memory();
        Identity::load_or_create(&to).unwrap();
        to.set("ai.openai.key", "left over").unwrap();
        import_store(&to, &to_dir, &doc, "one-time passphrase").unwrap();
        assert_eq!(Identity::load_or_create(&to).unwrap().device_id(), id);
        assert_eq!(
            to.get("ai.claude.key").as_deref().map(String::as_str),
            Some("sk-test")
        );
        assert!(
            to.get("ai.openai.key").is_none(),
            "absent entries are removed"
        );
        assert!(to.get("network").is_some());
        assert!(
            to.get("ai.openrouter.key").is_some(),
            "every source's key moves"
        );
        assert_eq!(
            std::fs::read_to_string(to_dir.join("device.json")).unwrap(),
            "{\"name\":\"Office\"}"
        );
        // The source lets go, so one identity has one engine.
        clear_store(&from, &from_dir).unwrap();
        assert!(from.get("device_key").is_none() && from.get("ai.claude.key").is_none());
        assert!(!from_dir.join("device.json").exists());
        let _ = (
            std::fs::remove_dir_all(&from_dir),
            std::fs::remove_dir_all(&to_dir),
        );
    }

    #[test]
    fn damaged_backups_are_refused_without_writing() {
        let s = Secrets::memory();
        let id = Identity::load_or_create(&s).unwrap().device_id();
        let doc = export(&s, "correct horse battery").unwrap();
        let mut x = 0x1234_5678_9abc_def1u64;
        for i in 0..200 {
            x ^= x << 13;
            x ^= x >> 7;
            x ^= x << 17;
            // Flip one character of a real backup, or cut it short.
            let mut bytes = doc.clone().into_bytes();
            let at = (x as usize) % bytes.len();
            if i % 2 == 0 {
                bytes[at] ^= 1 << (x % 7);
            } else {
                bytes.truncate(at);
            }
            let damaged = String::from_utf8_lossy(&bytes);
            let _ = import(&s, &damaged, "correct horse battery");
            assert_eq!(Identity::load_or_create(&s).unwrap().device_id(), id);
        }
        let huge = doc.replace(&format!("\"m\": {KDF_M}"), "\"m\": 4000000000");
        assert_ne!(huge, doc);
        assert!(
            import(&s, &huge, "correct horse battery").is_err(),
            "the KDF cost is bounded"
        );
    }

    #[test]
    fn a_reset_gives_a_new_identity() {
        let s = Secrets::memory();
        let before = Identity::load_or_create(&s).unwrap().device_id();
        s.set("opaque_permanent", "x").unwrap();
        s.set("network", "{}").unwrap();
        reset(&s).unwrap();
        assert_ne!(Identity::load_or_create(&s).unwrap().device_id(), before);
        assert!(s.get("opaque_permanent").is_none());
        assert!(
            s.get("network").is_some(),
            "network settings are not identity"
        );
    }
}
