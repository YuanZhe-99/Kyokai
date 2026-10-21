//! Sign-in keys (doc/en-us/identity-and-access.md, "Keys"): the public keys
//! that can connect to this host (`authorized_keys`), this device's own keys
//! (`login_keys`), the proof a controller gives with an Ed25519 key in its
//! hello, and SSH user-auth signatures for the built-in SSH client. Private
//! keys stay in the secrets store and never leave the core.

use crate::store::Secrets;
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::time::{SystemTime, UNIX_EPOCH};
use zeroize::Zeroizing;

/// What a key login signs: this tag, the handshake hash after frame 2, the
/// host's device key and the controller's device key.
const LOGIN_TAG: &[u8] = b"kyokai/key-login/v1";
const AUTHORIZED_NAME: &str = "authorized_keys";
const LOGIN_NAME: &str = "login_keys";
/// Most keys a host lets connect.
const MAX_AUTHORIZED: usize = 32;
/// Most keys this device keeps for signing in.
const MAX_LOGIN: usize = 16;
/// Most keys a controller offers in one hello, and a host looks at.
pub const MAX_OFFERED: usize = 8;
/// Longest name a key is shown by.
const MAX_NAME: usize = 64;
/// SSH_MSG_USERAUTH_REQUEST (RFC 4252).
const USERAUTH_REQUEST: u8 = 50;

/// What kind of key: only Ed25519 keys sign in to Kyokai; every kind signs
/// in to SSH.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum KeyType {
    Ed25519,
    Rsa,
    EcdsaP256,
    EcdsaP384,
}

impl KeyType {
    /// The key's name in an SSH public key blob.
    fn ssh_name(self) -> &'static str {
        match self {
            KeyType::Ed25519 => "ssh-ed25519",
            KeyType::Rsa => "ssh-rsa",
            KeyType::EcdsaP256 => "ecdsa-sha2-nistp256",
            KeyType::EcdsaP384 => "ecdsa-sha2-nistp384",
        }
    }

    /// Whether an SSH signature algorithm belongs to this kind of key (RSA
    /// with SHA-2 only).
    fn signs_with(self, algorithm: &str) -> bool {
        matches!(
            (self, algorithm),
            (KeyType::Ed25519, "ssh-ed25519")
                | (KeyType::Rsa, "rsa-sha2-256" | "rsa-sha2-512")
                | (KeyType::EcdsaP256, "ecdsa-sha2-nistp256")
                | (KeyType::EcdsaP384, "ecdsa-sha2-nistp384")
        )
    }
}

/// One of this device's sign-in keys, as kept in the secrets store.
#[derive(Clone, Serialize, Deserialize)]
struct LoginKey {
    id: String,
    name: String,
    #[serde(rename = "type")]
    kind: KeyType,
    /// The OpenSSH public key line.
    public: String,
    /// The Ed25519 seed or the PKCS#8 DER, base64.
    private: Zeroizing<String>,
    /// Unix seconds.
    added: u64,
}

/// A public key that can connect to this host.
#[derive(Clone, Serialize, Deserialize)]
pub struct Authorized {
    pub id: String,
    pub name: String,
    /// The OpenSSH public key line (Ed25519).
    pub public: String,
    /// Unix seconds; `last` is its last sign-in, 0 before the first.
    pub added: u64,
    #[serde(default)]
    pub last: u64,
}

/// One key's proof in a controller's hello: its Ed25519 public key and its
/// signature over the handshake, base64.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Proof {
    pub key: String,
    pub signature: String,
}

/// What the host made of the keys a controller offered.
pub enum Verified {
    /// None of them can connect here.
    None,
    /// An authorized key whose proof holds.
    Key(Authorized),
    /// An authorized key with a proof that does not hold: a failed login.
    Bad,
}

/// What a key login signs.
fn login_message(h2: &[u8], host_key: &[u8; 32], controller_key: &[u8; 32]) -> Vec<u8> {
    [LOGIN_TAG, h2, host_key, controller_key].concat()
}

/// The proofs a controller offers a host: with the key remembered for it
/// when that is still kept, else with every Ed25519 key (at most
/// MAX_OFFERED).
/// Inputs: `h2`, the handshake hash after frame 2.
pub fn proofs(
    secrets: &Secrets,
    remembered: Option<&str>,
    h2: &[u8],
    host_key: &[u8; 32],
    controller_key: &[u8; 32],
) -> Vec<Proof> {
    let keys: Vec<LoginKey> = login(secrets)
        .into_iter()
        .filter(|k| k.kind == KeyType::Ed25519)
        .collect();
    let chosen: Vec<&LoginKey> = match keys.iter().find(|k| Some(k.id.as_str()) == remembered) {
        Some(k) => vec![k],
        None => keys.iter().take(MAX_OFFERED).collect(),
    };
    let message = login_message(h2, host_key, controller_key);
    chosen
        .into_iter()
        .filter_map(|k| {
            let signing = ed25519_signing(&k.private)?;
            use ed25519_dalek::Signer;
            Some(Proof {
                key: STANDARD.encode(signing.verifying_key().to_bytes()),
                signature: STANDARD.encode(signing.sign(&message).to_bytes()),
            })
        })
        .collect()
}

/// The host's look at the keys a controller offered: the first authorized
/// one whose proof holds admits; an authorized key whose proof fails is a
/// failed login; keys it does not know are ignored.
/// Side effects: records the admitted key's sign-in time.
pub fn verify(
    secrets: &Secrets,
    offered: &[Proof],
    h2: &[u8],
    host_key: &[u8; 32],
    controller_key: &[u8; 32],
) -> Verified {
    let mut all = authorized(secrets);
    let message = login_message(h2, host_key, controller_key);
    for proof in offered.iter().take(MAX_OFFERED) {
        let Some(raw) = decode32(&proof.key) else {
            continue;
        };
        let Some(i) = all.iter().position(|a| ed25519_of(&a.public) == Some(raw)) else {
            continue;
        };
        let signature: Option<[u8; 64]> = STANDARD
            .decode(&proof.signature)
            .ok()
            .and_then(|s| s.try_into().ok());
        if !signature.is_some_and(|s| crate::identity::verify(&raw, &message, &s)) {
            return Verified::Bad;
        }
        all[i].last = unix_now();
        let admitted = all[i].clone();
        let _ = store(secrets, AUTHORIZED_NAME, &all);
        return Verified::Key(admitted);
    }
    Verified::None
}

/// The login key whose Ed25519 public key (base64, as a proof names it) is
/// `key`, if this device keeps it.
pub fn login_id(secrets: &Secrets, key: &str) -> Option<String> {
    let raw = decode32(key)?;
    login(secrets)
        .into_iter()
        .find(|k| ed25519_of(&k.public) == Some(raw))
        .map(|k| k.id)
}

/// Whether this device keeps a sign-in key with this id.
pub fn is_login(secrets: &Secrets, id: &str) -> bool {
    login(secrets).iter().any(|k| k.id == id)
}

/// This device's sign-in keys, public parts only (`keys.list`).
pub fn list_login(secrets: &Secrets) -> Value {
    login(secrets)
        .into_iter()
        .map(|k| json!({"id": k.id, "name": k.name, "type": k.kind, "public": k.public, "added": k.added}))
        .collect()
}

/// Keeps a sign-in key (`keys.add`).
/// Inputs: `private`, the Ed25519 seed or a PKCS#8 DER (RSA of at least
/// 2048 bits, ECDSA P-256 or P-384), base64; `name`, empty for the kind's
/// name.
/// Returns: the key's public parts.
/// Side effects: rewrites the secrets store.
pub fn add_login(secrets: &Secrets, name: &str, private: &str) -> Result<Value, String> {
    let der = Zeroizing::new(
        STANDARD
            .decode(private.trim())
            .map_err(|_| "the private key is not base64")?,
    );
    let (kind, blob) = parse_private(&der)?;
    let mut all = login(secrets);
    if all.len() >= MAX_LOGIN {
        return Err(format!("at most {MAX_LOGIN} keys"));
    }
    let id = key_id(&blob);
    if all.iter().any(|k| k.id == id) {
        return Err("that key is kept already".into());
    }
    let key = LoginKey {
        id,
        name: named(name, kind.ssh_name())?,
        kind,
        public: format!("{} {}", kind.ssh_name(), STANDARD.encode(&blob)),
        private: Zeroizing::new(STANDARD.encode(der.as_slice())),
        added: unix_now(),
    };
    let shown = json!({"id": key.id, "name": key.name, "type": key.kind, "public": key.public, "added": key.added});
    all.push(key);
    store(secrets, LOGIN_NAME, &all)?;
    Ok(shown)
}

/// Forgets a sign-in key (`keys.remove`).
pub fn remove_login(secrets: &Secrets, id: &str) -> Result<(), String> {
    let mut all = login(secrets);
    let before = all.len();
    all.retain(|k| k.id != id);
    if all.len() == before {
        return Err("no such key".into());
    }
    store(secrets, LOGIN_NAME, &all)
}

/// The keys that can connect to this host (`authorized.list`).
pub fn authorized(secrets: &Secrets) -> Vec<Authorized> {
    secrets
        .get(AUTHORIZED_NAME)
        .and_then(|t| serde_json::from_str(&t).ok())
        .unwrap_or_default()
}

/// Lets a public key connect (`authorized.add`).
/// Inputs: an OpenSSH public key line (Ed25519); `name`, empty for the
/// line's comment.
/// Side effects: rewrites the secrets store.
pub fn add_authorized(secrets: &Secrets, name: &str, line: &str) -> Result<Authorized, String> {
    let (kind, blob, comment) = parse_public(line)?;
    if kind != KeyType::Ed25519 {
        return Err("only Ed25519 keys sign in to Kyokai".into());
    }
    let mut all = authorized(secrets);
    if all.len() >= MAX_AUTHORIZED {
        return Err(format!("at most {MAX_AUTHORIZED} keys"));
    }
    let id = key_id(&blob);
    if all.iter().any(|a| a.id == id) {
        return Err("that key can connect already".into());
    }
    let name = if name.trim().is_empty() {
        comment
    } else {
        name
    };
    let a = Authorized {
        id,
        name: named(name, "Ed25519")?,
        public: format!("ssh-ed25519 {}", STANDARD.encode(&blob)),
        added: unix_now(),
        last: 0,
    };
    all.push(a.clone());
    store(secrets, AUTHORIZED_NAME, &all)?;
    Ok(a)
}

/// Stops a public key connecting (`authorized.remove`).
pub fn remove_authorized(secrets: &Secrets, id: &str) -> Result<(), String> {
    let mut all = authorized(secrets);
    let before = all.len();
    all.retain(|a| a.id != id);
    if all.len() == before {
        return Err("no such key".into());
    }
    store(secrets, AUTHORIZED_NAME, &all)
}

/// Signs an SSH user-auth request with a kept key (`keys.sign`), for the
/// built-in SSH client: only the request RFC 4252 section 7 names, for this
/// very key and an algorithm of its kind, so the engine is no general
/// signing oracle.
/// Inputs: `data`, the exact request to sign, base64.
/// Returns: the SSH signature (string format, string blob), base64.
pub fn sign_ssh(
    secrets: &Secrets,
    id: &str,
    algorithm: &str,
    data: &str,
) -> Result<String, String> {
    let key = login(secrets)
        .into_iter()
        .find(|k| k.id == id)
        .ok_or("no such key")?;
    if !key.kind.signs_with(algorithm) {
        return Err(format!("a {:?} key does not sign {algorithm}", key.kind));
    }
    let data = STANDARD
        .decode(data)
        .map_err(|_| "the request is not base64")?;
    let blob = blob_of(&key.public).ok_or("the kept key is damaged")?;
    check_userauth(&data, algorithm, &blob)?;
    let private = Zeroizing::new(
        STANDARD
            .decode(key.private.as_bytes())
            .map_err(|_| "the kept key is damaged")?,
    );
    let rng = ring::rand::SystemRandom::new();
    let signature = match key.kind {
        KeyType::Ed25519 => {
            use ed25519_dalek::Signer;
            let signing = ed25519_signing(&key.private).ok_or("the kept key is damaged")?;
            signing.sign(&data).to_bytes().to_vec()
        }
        KeyType::Rsa => {
            let pair =
                ring::rsa::KeyPair::from_pkcs8(&private).map_err(|_| "the kept key is damaged")?;
            let padding: &'static dyn ring::signature::RsaEncoding = if algorithm == "rsa-sha2-512"
            {
                &ring::signature::RSA_PKCS1_SHA512
            } else {
                &ring::signature::RSA_PKCS1_SHA256
            };
            let mut sig = vec![0; pair.public().modulus_len()];
            pair.sign(padding, &rng, &data, &mut sig)
                .map_err(|_| "signing failed")?;
            sig
        }
        KeyType::EcdsaP256 | KeyType::EcdsaP384 => {
            let (alg, half) = if key.kind == KeyType::EcdsaP256 {
                (&ring::signature::ECDSA_P256_SHA256_FIXED_SIGNING, 32)
            } else {
                (&ring::signature::ECDSA_P384_SHA384_FIXED_SIGNING, 48)
            };
            let pair = ring::signature::EcdsaKeyPair::from_pkcs8(alg, &private, &rng)
                .map_err(|_| "the kept key is damaged")?;
            let fixed = pair.sign(&rng, &data).map_err(|_| "signing failed")?;
            let (r, s) = fixed.as_ref().split_at(half);
            let mut rs = Vec::new();
            put_mpint(&mut rs, r);
            put_mpint(&mut rs, s);
            rs
        }
    };
    let mut out = Vec::new();
    put_string(&mut out, algorithm.as_bytes());
    put_string(&mut out, &signature);
    Ok(STANDARD.encode(out))
}

/// Checks that `data` is exactly an SSH user-auth request with a public key
/// signature (RFC 4252 section 7) for `algorithm` and the key `blob`.
fn check_userauth(data: &[u8], algorithm: &str, blob: &[u8]) -> Result<(), String> {
    let refuse = || "only an SSH user-auth request for this key is signed".to_string();
    let mut r = Reader(data);
    let session = r.string().ok_or_else(refuse)?;
    let ok = !session.is_empty()
        && r.byte() == Some(USERAUTH_REQUEST)
        && r.string().is_some() // user
        && r.string().is_some() // service
        && r.string() == Some(b"publickey")
        && r.byte() == Some(1)
        && r.string() == Some(algorithm.as_bytes())
        && r.string() == Some(blob)
        && r.0.is_empty();
    ok.then_some(()).ok_or_else(refuse)
}

/// Reads SSH wire data (RFC 4251 section 5).
struct Reader<'a>(&'a [u8]);

impl<'a> Reader<'a> {
    fn byte(&mut self) -> Option<u8> {
        let (&b, rest) = self.0.split_first()?;
        self.0 = rest;
        Some(b)
    }

    fn string(&mut self) -> Option<&'a [u8]> {
        let len = u32::from_be_bytes(self.0.get(..4)?.try_into().ok()?) as usize;
        let s = self.0.get(4..4 + len)?;
        self.0 = &self.0[4 + len..];
        Some(s)
    }
}

/// Appends an SSH string.
fn put_string(out: &mut Vec<u8>, s: &[u8]) {
    out.extend_from_slice(&(s.len() as u32).to_be_bytes());
    out.extend_from_slice(s);
}

/// Appends an SSH mpint of a positive big-endian number.
fn put_mpint(out: &mut Vec<u8>, n: &[u8]) {
    let n = &n[n.iter().position(|&b| b != 0).unwrap_or(n.len())..];
    let pad = n.first().is_some_and(|&b| b & 0x80 != 0);
    out.extend_from_slice(&((n.len() + usize::from(pad)) as u32).to_be_bytes());
    if pad {
        out.push(0);
    }
    out.extend_from_slice(n);
}

/// Reads a private key: a 32-byte Ed25519 seed, or a PKCS#8 DER of an RSA
/// (at least 2048 bits), ECDSA P-256 or P-384 key.
/// Returns: its kind and SSH public key blob.
fn parse_private(der: &[u8]) -> Result<(KeyType, Vec<u8>), String> {
    let mut blob = Vec::new();
    if let Ok(seed) = <[u8; 32]>::try_from(der) {
        let public = ed25519_dalek::SigningKey::from_bytes(&seed).verifying_key();
        put_string(&mut blob, b"ssh-ed25519");
        put_string(&mut blob, public.as_bytes());
        return Ok((KeyType::Ed25519, blob));
    }
    if let Ok(pair) = ring::rsa::KeyPair::from_pkcs8(der) {
        let p = ring::rsa::PublicKeyComponents::<Vec<u8>>::from(pair.public());
        put_string(&mut blob, b"ssh-rsa");
        put_mpint(&mut blob, &p.e);
        put_mpint(&mut blob, &p.n);
        return Ok((KeyType::Rsa, blob));
    }
    let rng = ring::rand::SystemRandom::new();
    for (kind, alg, curve) in [
        (
            KeyType::EcdsaP256,
            &ring::signature::ECDSA_P256_SHA256_FIXED_SIGNING,
            "nistp256",
        ),
        (
            KeyType::EcdsaP384,
            &ring::signature::ECDSA_P384_SHA384_FIXED_SIGNING,
            "nistp384",
        ),
    ] {
        if let Ok(pair) = ring::signature::EcdsaKeyPair::from_pkcs8(alg, der, &rng) {
            use ring::signature::KeyPair;
            put_string(&mut blob, kind.ssh_name().as_bytes());
            put_string(&mut blob, curve.as_bytes());
            put_string(&mut blob, pair.public_key().as_ref());
            return Ok((kind, blob));
        }
    }
    Err(
        "not an Ed25519 seed, nor an RSA (2048 bits or more) or ECDSA P-256/P-384 key in PKCS#8"
            .into(),
    )
}

/// Reads an OpenSSH public key line (`type base64 [comment]`).
/// Returns: its kind, its blob and its comment.
fn parse_public(line: &str) -> Result<(KeyType, Vec<u8>, &str), String> {
    let mut parts = line.trim().splitn(3, char::is_whitespace);
    let (Some(name), Some(b64)) = (parts.next(), parts.next()) else {
        return Err("not an OpenSSH public key line".into());
    };
    let comment = parts.next().unwrap_or("").trim();
    let blob = STANDARD
        .decode(b64)
        .map_err(|_| "not an OpenSSH public key line")?;
    let kind = [
        KeyType::Ed25519,
        KeyType::Rsa,
        KeyType::EcdsaP256,
        KeyType::EcdsaP384,
    ]
    .into_iter()
    .find(|k| k.ssh_name() == name)
    .ok_or_else(|| format!("{name:?} keys are not supported"))?;
    let mut r = Reader(&blob);
    if r.string() != Some(name.as_bytes()) {
        return Err("the key does not match its type".into());
    }
    if kind == KeyType::Ed25519 {
        let key = r.string().filter(|k| k.len() == 32 && r.0.is_empty());
        let key: [u8; 32] = key
            .and_then(|k| k.try_into().ok())
            .ok_or("not an Ed25519 key")?;
        ed25519_dalek::VerifyingKey::from_bytes(&key).map_err(|_| "not an Ed25519 key")?;
    }
    Ok((kind, blob, comment))
}

/// The SSH public key blob of a public key line kept here.
fn blob_of(line: &str) -> Option<Vec<u8>> {
    parse_public(line).ok().map(|(_, blob, _)| blob)
}

/// The raw Ed25519 key of a public key line, if it is one.
fn ed25519_of(line: &str) -> Option<[u8; 32]> {
    let blob = blob_of(line)?;
    let mut r = Reader(&blob);
    (r.string()? == b"ssh-ed25519").then_some(())?;
    r.string()?.try_into().ok()
}

/// The Ed25519 signing key of a kept seed.
fn ed25519_signing(private: &str) -> Option<ed25519_dalek::SigningKey> {
    let seed = Zeroizing::new(STANDARD.decode(private.as_bytes()).ok()?);
    let seed: [u8; 32] = seed.as_slice().try_into().ok()?;
    Some(ed25519_dalek::SigningKey::from_bytes(&seed))
}

/// A key's id: the first 16 hex digits of the SHA-256 of its blob, so one
/// key is kept once.
fn key_id(blob: &[u8]) -> String {
    Sha256::digest(blob)[..8]
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// A key's name as kept: trimmed, `fallback` when empty, at most MAX_NAME
/// characters, without control characters.
fn named(name: &str, fallback: &str) -> Result<String, String> {
    let name: String = name.trim().chars().filter(|c| !c.is_control()).collect();
    if name.chars().count() > MAX_NAME {
        return Err(format!("a key's name has at most {MAX_NAME} characters"));
    }
    Ok(if name.is_empty() {
        fallback.into()
    } else {
        name
    })
}

fn decode32(b64: &str) -> Option<[u8; 32]> {
    STANDARD.decode(b64).ok()?.try_into().ok()
}

fn login(secrets: &Secrets) -> Vec<LoginKey> {
    secrets
        .get(LOGIN_NAME)
        .and_then(|t| serde_json::from_str(&t).ok())
        .unwrap_or_default()
}

fn store<T: Serialize>(secrets: &Secrets, name: &str, value: &T) -> Result<(), String> {
    let text = Zeroizing::new(serde_json::to_string(value).map_err(|e| e.to_string())?);
    secrets.set(name, &text)
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// An RSA-2048 key in PKCS#8, made for this test only (it protects
    /// nothing): `ring` signs with RSA keys but does not make them.
    const TEST_RSA: &[&str] = &[
        "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC4Ew+wBb8QsqJ+fVlVcFBZgiIKQplj0u1LmXbK04kESdZNeSwp",
        "v6w3PjkfFBAmPZ3avEIgGGu8KLNewRs8GJdUL9N5hTRJFnAo+K6/nX/WTHn/98ma1v1V79gG/4iS7r4Z/DLdiiJjcS2+VHOx3DaQ",
        "TUbAXEpMfRE/Pmd9YlDLmiBDw9Eu7aF5lo96TaA4bNU9HOQFp/PDmvaq2kYb5w9Mv5R1GqO9p4nSRFY07wi+0cq+0dw8PYlzDA0O",
        "c1RaD6VZ8qzJ1FdBCZkeFyszQZ8FxivLK/C8v2IeN7CsbSwdhpGKkSYWJseweRjHfhR9pEncfBGRK45AOKcTfi5/qikvAgMBAAEC",
        "ggEAGZ1QFd7RgKGq/6Oya7OOVXZIsFl7firwKbtNjtnde4L0LExhUInt4rNo6syK7BdaQQGTc2M9PVit5SXwA+5+wxCeAo9TqvFR",
        "YueAFwVJWxPB96mOcgUwCZEE5FcTX9w9szX8tLFafwX/0eLEmrUZw/4iNfW3F7+2R7EHtlNDtPZykOikBGsl/Jo2TDsUBqq0LqKN",
        "zSmfFJZqKbuY5nfDPB3xWZnT8X68LgSihLdiP8ftRUgALmiBaUG03JyPGi6rfQgc8vkw5UuO/IemcpSMVImkuUjuc0GtLAr4/S7V",
        "Kvlo0+6kwAusknYONO+v/UNoN2wlj3EO1X/w8fpcuFv9YQKBgQDuQug+opZVwX0BOJvsgsIgQll8gRwihN3Fhc720pJmfzu1eoPq",
        "Bgsm27wj3ew3vYm7g+ZJ7GM4Y8zh+XcvkROI38+Be5sSHM3ufaOdc5yBadbBWxRm4kM20ghGOZT+yDlD4A4Ai2UWq743TvmpUvc8",
        "rDvwYipcAumInydrt0W0EQKBgQDFx2PhAWvcT8DrQ0Y4MbPMAdNwgi0iv6WU7KC3BsO+15Go2gfRgzYCu4Wu6PccmJu/ZjBjnje5",
        "VQAZLEgVlk3RKTzuAUbQPUm0HMLH1xWlcXk877tlxG+sveXIvqkQUoaCOXc3SEpyRooTAd9j3pxRJO/dfMgvbRLnMaITNIpJPwKB",
        "gQCl/3+HhAdxn+jT3PjlgwR1LUTjdaCAfEl2wI89b+AIAUU8Xytg9Oz4GceuxYM6Ipe/SVo8zrEiLIYmOoRLY6xUhHoyIEgeCmt/",
        "aZGAtXeownCZLje8po9ltleFdcDU8fLT01puDrBTZzyTlDZlPiR65RStmfchO/Yw9ZGlfn7VwQKBgDMfMuX6JBMyWj91iDUGQ987",
        "qZevTlNpDjfMYk5s5TPzLKdklBjHvbI/HnLzrMwrdjanB2L4hpbVLc1T8N4Dl+G0sxciJVe2P9LajE5zfhjhl+D0ZpALkoIoI7kb",
        "7z9U1YfbqsETYorfSNlY5pE9/VHMJmQt2ODJZNV1stsb/ZKpAoGAH5QNbQtC+kcZ89bV5ldBW4MgLMGCjlrXHDw17+cnEqXTJAWl",
        "+UPxk4eX0EaPd9TECLLbzqIbKHKGXs2lRggmfDDBhN28Wh5dGXg8aqJN3akY+72JA5HgXjwzWuBBpXVpfUKN1vCOBkJZOzgnoiPm",
        "kAj56Bb73liET6CafFdp5L8=",
    ];

    fn secrets() -> Secrets {
        Secrets::memory()
    }

    /// An SSH user-auth request as a client signs it (RFC 4252 section 7).
    fn userauth(algorithm: &str, blob: &[u8]) -> Vec<u8> {
        let mut d = Vec::new();
        put_string(&mut d, b"session-id-of-the-connection");
        d.push(USERAUTH_REQUEST);
        put_string(&mut d, b"me");
        put_string(&mut d, b"ssh-connection");
        put_string(&mut d, b"publickey");
        d.push(1);
        put_string(&mut d, algorithm.as_bytes());
        put_string(&mut d, blob);
        d
    }

    /// Keeps a key and returns its id and blob.
    fn keep(s: &Secrets, der: &[u8]) -> (String, Vec<u8>) {
        let added = add_login(s, "", &STANDARD.encode(der)).unwrap();
        let line = added["public"].as_str().unwrap();
        (added["id"].as_str().unwrap().into(), blob_of(line).unwrap())
    }

    /// Reads a signature as `sign_ssh` returns it: its format and blob.
    fn signature(b64: &str) -> (String, Vec<u8>) {
        let raw = STANDARD.decode(b64).unwrap();
        let mut r = Reader(&raw);
        let format = String::from_utf8(r.string().unwrap().to_vec()).unwrap();
        let blob = r.string().unwrap().to_vec();
        assert!(r.0.is_empty());
        (format, blob)
    }

    #[test]
    fn openssh_lines_parse_or_are_refused() {
        let key = ed25519_dalek::SigningKey::from_bytes(&[7; 32]).verifying_key();
        let mut blob = Vec::new();
        put_string(&mut blob, b"ssh-ed25519");
        put_string(&mut blob, key.as_bytes());
        let good = format!("ssh-ed25519 {} alice@laptop", STANDARD.encode(&blob));
        let s = secrets();
        let a = add_authorized(&s, "", &good).unwrap();
        assert_eq!(a.name, "alice@laptop", "the comment names it");
        assert_eq!(ed25519_of(&a.public), Some(key.to_bytes()));
        assert!(add_authorized(&s, "again", &good).is_err(), "kept once");
        let mut rsa = Vec::new();
        put_string(&mut rsa, b"ssh-rsa");
        put_mpint(&mut rsa, &[1, 0, 1]);
        put_mpint(&mut rsa, &[0xc0; 256]);
        let rsa = format!("ssh-rsa {}", STANDARD.encode(&rsa));
        assert_eq!(parse_public(&rsa).unwrap().0, KeyType::Rsa);
        assert!(
            add_authorized(&s, "", &rsa).is_err(),
            "only Ed25519 signs in"
        );
        let short = {
            let mut b = Vec::new();
            put_string(&mut b, b"ssh-ed25519");
            put_string(&mut b, &[7; 31]);
            format!("ssh-ed25519 {}", STANDARD.encode(b))
        };
        for bad in [
            "".to_string(),
            "ssh-ed25519".into(),
            "ssh-ed25519 not-base64!".into(),
            short,
            format!("ssh-dss {}", STANDARD.encode(&blob)),
            format!("ecdsa-sha2-nistp256 {}", STANDARD.encode(&blob)),
            format!("ssh-ed25519 {}", STANDARD.encode(&blob[..blob.len() - 1])),
        ] {
            assert!(add_authorized(&s, "", &bad).is_err(), "{bad:?}");
        }
        assert_eq!(authorized(&s).len(), 1);
        remove_authorized(&s, &a.id).unwrap();
        assert!(authorized(&s).is_empty());
    }

    #[test]
    fn signatures_verify_per_algorithm() {
        use ring::signature::{self as sig, KeyPair};
        let s = secrets();
        let rng = ring::rand::SystemRandom::new();
        // Ed25519
        let (id, blob) = keep(&s, &[9; 32]);
        let data = userauth("ssh-ed25519", &blob);
        let signed = sign_ssh(&s, &id, "ssh-ed25519", &STANDARD.encode(&data)).unwrap();
        let (format, raw) = signature(&signed);
        assert_eq!(format, "ssh-ed25519");
        let public = ed25519_dalek::SigningKey::from_bytes(&[9; 32]).verifying_key();
        let raw: [u8; 64] = raw.try_into().unwrap();
        assert!(crate::identity::verify(&public.to_bytes(), &data, &raw));
        // RSA with SHA-256 and SHA-512, never SHA-1.
        let der = STANDARD.decode(TEST_RSA.concat()).unwrap();
        let (id, blob) = keep(&s, &der);
        let pair = ring::rsa::KeyPair::from_pkcs8(&der).unwrap();
        let p = ring::rsa::PublicKeyComponents::<Vec<u8>>::from(pair.public());
        let public = sig::RsaPublicKeyComponents { n: &p.n, e: &p.e };
        for (alg, check) in [
            ("rsa-sha2-256", &sig::RSA_PKCS1_2048_8192_SHA256),
            ("rsa-sha2-512", &sig::RSA_PKCS1_2048_8192_SHA512),
        ] {
            let data = userauth(alg, &blob);
            let signed = sign_ssh(&s, &id, alg, &STANDARD.encode(&data)).unwrap();
            let (format, raw) = signature(&signed);
            assert_eq!(format, alg);
            public.verify(check, &data, &raw).unwrap();
        }
        let sha1 = userauth("ssh-rsa", &blob);
        assert!(sign_ssh(&s, &id, "ssh-rsa", &STANDARD.encode(sha1)).is_err());
        // ECDSA P-256 and P-384: r and s as mpints.
        let curves = [
            (
                &sig::ECDSA_P256_SHA256_FIXED_SIGNING,
                &sig::ECDSA_P256_SHA256_FIXED,
                "ecdsa-sha2-nistp256",
                32,
            ),
            (
                &sig::ECDSA_P384_SHA384_FIXED_SIGNING,
                &sig::ECDSA_P384_SHA384_FIXED,
                "ecdsa-sha2-nistp384",
                48,
            ),
        ];
        for (signing, verifying, alg, half) in curves {
            let der = sig::EcdsaKeyPair::generate_pkcs8(signing, &rng).unwrap();
            let point = sig::EcdsaKeyPair::from_pkcs8(signing, der.as_ref(), &rng)
                .unwrap()
                .public_key()
                .as_ref()
                .to_vec();
            let (id, blob) = keep(&s, der.as_ref());
            let data = userauth(alg, &blob);
            let signed = sign_ssh(&s, &id, alg, &STANDARD.encode(&data)).unwrap();
            let (format, raw) = signature(&signed);
            assert_eq!(format, alg);
            let mut r = Reader(&raw);
            let mut fixed = Vec::new();
            for n in [r.string().unwrap(), r.string().unwrap()] {
                let n = &n[n.len().saturating_sub(half)..];
                fixed.extend(std::iter::repeat_n(0, half - n.len()));
                fixed.extend_from_slice(n);
            }
            sig::UnparsedPublicKey::new(verifying, &point)
                .verify(&data, &fixed)
                .unwrap();
        }
        assert_eq!(login(&s).len(), 4);
        assert!(
            add_login(&s, "", &STANDARD.encode([9; 32])).is_err(),
            "kept once"
        );
        assert!(
            add_login(&s, "", &STANDARD.encode([1; 20])).is_err(),
            "not a key"
        );
    }

    #[test]
    fn only_a_user_auth_request_for_the_key_is_signed() {
        let s = secrets();
        let (id, blob) = keep(&s, &[9; 32]);
        let (_, other) = keep(&s, &[8; 32]);
        let good = userauth("ssh-ed25519", &blob);
        assert!(sign_ssh(&s, &id, "ssh-ed25519", &STANDARD.encode(&good)).is_ok());
        let session = 4 + b"session-id-of-the-connection".len();
        let mut trailing = good.clone();
        trailing.push(0);
        let mut wrong_type = good.clone();
        wrong_type[session] = 51;
        let mut no_session = Vec::new();
        put_string(&mut no_session, b"");
        no_session.extend_from_slice(&good[session..]);
        let password = {
            let mut d = Vec::new();
            put_string(&mut d, b"sid");
            d.push(USERAUTH_REQUEST);
            put_string(&mut d, b"me");
            put_string(&mut d, b"ssh-connection");
            put_string(&mut d, b"password");
            d.push(0);
            put_string(&mut d, b"hunter2");
            d
        };
        let query = {
            let mut d = good.clone();
            let at = d.len() - (4 + blob.len()) - (4 + "ssh-ed25519".len()) - 1;
            d[at] = 0;
            d
        };
        for (what, data) in [
            ("trailing bytes", trailing),
            ("another message", wrong_type),
            ("no session", no_session),
            ("a password request", password),
            ("a query, not a signature", query),
            ("another key", userauth("ssh-ed25519", &other)),
            ("another algorithm", userauth("rsa-sha2-256", &blob)),
            ("anything else", b"please sign this".to_vec()),
        ] {
            assert!(
                sign_ssh(&s, &id, "ssh-ed25519", &STANDARD.encode(&data)).is_err(),
                "{what}"
            );
        }
        assert!(sign_ssh(&s, &id, "rsa-sha2-256", &STANDARD.encode(&good)).is_err());
        assert!(sign_ssh(&s, "nope", "ssh-ed25519", &STANDARD.encode(&good)).is_err());
    }

    #[test]
    fn a_proof_holds_only_for_its_handshake() {
        let host = secrets();
        let controller = secrets();
        let (_, blob) = keep(&controller, &[9; 32]);
        keep(&controller, &[8; 32]);
        let line = format!("ssh-ed25519 {}", STANDARD.encode(&blob));
        add_authorized(&host, "laptop", &line).unwrap();
        let (h2, hk, ck) = ([1u8; 32], [2u8; 32], [3u8; 32]);
        let offered = proofs(&controller, None, &h2, &hk, &ck);
        assert_eq!(offered.len(), 2, "every Ed25519 key without one remembered");
        let Verified::Key(k) = verify(&host, &offered, &h2, &hk, &ck) else {
            panic!("the authorized key admits");
        };
        assert_eq!(k.name, "laptop");
        assert!(authorized(&host)[0].last > 0, "its sign-in is recorded");
        assert_eq!(login_id(&controller, &offered[0].key), Some(key_id(&blob)));
        // Another handshake, host or controller: the proof fails.
        for (h2, hk, ck) in [([9; 32], hk, ck), (h2, [9; 32], ck), (h2, hk, [9; 32])] {
            assert!(matches!(
                verify(&host, &offered, &h2, &hk, &ck),
                Verified::Bad
            ));
        }
        // Keys the host does not know are ignored.
        let stranger = secrets();
        keep(&stranger, &[5; 32]);
        let theirs = proofs(&stranger, None, &h2, &hk, &ck);
        assert!(matches!(
            verify(&host, &theirs, &h2, &hk, &ck),
            Verified::None
        ));
        // The key remembered for a host is the only one offered.
        let id = key_id(&blob);
        assert_eq!(proofs(&controller, Some(&id), &h2, &hk, &ck).len(), 1);
        assert_eq!(proofs(&controller, Some("gone"), &h2, &hk, &ck).len(), 2);
    }
}
