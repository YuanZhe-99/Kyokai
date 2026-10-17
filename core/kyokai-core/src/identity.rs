//! The device identity: a long-term Ed25519 key, and the Device ID derived
//! from its public half.

use crate::store::Secrets;
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use ed25519_dalek::SigningKey;
use sha2::{Digest, Sha256};
use zeroize::Zeroizing;

const KEY_NAME: &str = "device_key";

/// The device's signing key and the values derived from it.
pub struct Identity {
    key: SigningKey,
}

impl Identity {
    /// Reads the device key from the secrets store, or creates and stores a
    /// new one.
    /// Side effects: writes the store when a key is created.
    /// Notes: a store that cannot persist (no OS credential store) yields a
    /// new identity on every start; the status says so.
    pub fn load_or_create(secrets: &Secrets) -> Result<Self, String> {
        if let Some(text) = secrets.get(KEY_NAME) {
            let seed = Zeroizing::new(
                STANDARD
                    .decode(text.as_bytes())
                    .map_err(|_| "stored device key is not base64")?,
            );
            let seed: &[u8; 32] = seed
                .as_slice()
                .try_into()
                .map_err(|_| "stored device key has the wrong length")?;
            return Ok(Identity {
                key: SigningKey::from_bytes(seed),
            });
        }
        let mut seed = Zeroizing::new([0u8; 32]);
        getrandom::fill(seed.as_mut()).map_err(|e| format!("OS random source: {e}"))?;
        let id = Identity {
            key: SigningKey::from_bytes(&seed),
        };
        secrets.set(KEY_NAME, &STANDARD.encode(seed.as_ref()))?;
        Ok(id)
    }

    /// Returns the public device key.
    pub fn public_key(&self) -> [u8; 32] {
        self.key.verifying_key().to_bytes()
    }

    /// Signs a message with the device key.
    pub fn sign(&self, message: &[u8]) -> [u8; 64] {
        use ed25519_dalek::Signer;
        self.key.sign(message).to_bytes()
    }

    /// Returns the 9-digit Device ID (never starting with 0).
    pub fn device_id(&self) -> String {
        device_id(&self.key.verifying_key().to_bytes())
    }

    /// Returns the full fingerprint, lowercase hex of SHA-256 of the public
    /// key, for the identity page and manual verification.
    pub fn fingerprint(&self) -> String {
        Sha256::digest(self.key.verifying_key().to_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }
}

/// Checks a device key's signature (strict: no malleable encodings).
pub fn verify(public_key: &[u8; 32], message: &[u8], signature: &[u8; 64]) -> bool {
    ed25519_dalek::VerifyingKey::from_bytes(public_key).is_ok_and(|k| {
        k.verify_strict(message, &ed25519_dalek::Signature::from_bytes(signature))
            .is_ok()
    })
}

/// Derives the Device ID of a public key: SHA-256 over a domain tag and the
/// key, its first 8 bytes as a number, reduced into 100 000 000–999 999 999.
/// Notes: it only locates a device; authentication checks the full key.
pub fn device_id(public_key: &[u8; 32]) -> String {
    let digest = Sha256::new()
        .chain_update(b"kyokai/device-id/v1")
        .chain_update(public_key)
        .finalize();
    let n = u64::from_be_bytes(digest[..8].try_into().expect("8 bytes"));
    (100_000_000 + n % 900_000_000).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn device_id_is_stable_and_nine_digits() {
        let a = device_id(&[7u8; 32]);
        assert_eq!(a, device_id(&[7u8; 32]));
        assert_eq!(a.len(), 9);
        assert_ne!(a, device_id(&[8u8; 32]));
    }
}
