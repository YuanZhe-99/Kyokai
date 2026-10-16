//! The host's temporary password: random, held only in memory, new on every
//! start and on request.

use zeroize::Zeroizing;

/// 32 symbols without look-alikes (no 0, 1, l, o), so each is 5 bits.
const ALPHABET: &[u8; 32] = b"abcdefghijkmnpqrstuvwxyz23456789";
/// 8 symbols ≈ 40 bits; password-authenticated key exchange and rate limits
/// keep online guessing hopeless (doc/en-us/security.md).
const LEN: usize = 8;

/// A temporary password and the serial that tells hosts it changed.
pub struct Temporary {
    value: Zeroizing<String>,
    serial: u64,
}

impl Temporary {
    /// Generates a new password from the OS random source.
    /// Notes: the serial is random too, so a restart never repeats one.
    pub fn generate() -> Self {
        let mut bytes = Zeroizing::new([0u8; LEN]);
        getrandom::fill(bytes.as_mut()).expect("OS random source");
        let value = bytes
            .iter()
            .map(|b| ALPHABET[(b & 31) as usize] as char)
            .collect();
        Temporary {
            value: Zeroizing::new(value),
            serial: getrandom::u64().expect("OS random source"),
        }
    }

    /// Returns the password, for the host's own interface only.
    pub fn reveal(&self) -> &str {
        &self.value
    }

    /// Changes whenever the password does.
    pub fn serial(&self) -> u64 {
        self.serial
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn passwords_use_the_alphabet_and_differ() {
        let a = Temporary::generate();
        let b = Temporary::generate();
        assert_eq!(a.reveal().len(), LEN);
        assert!(a.reveal().bytes().all(|c| ALPHABET.contains(&c)));
        assert_ne!(a.reveal(), b.reveal());
        assert_ne!(a.serial(), b.serial());
    }
}
