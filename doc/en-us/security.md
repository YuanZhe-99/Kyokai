# Security

## Secrets store

- The core keeps every secret in one JSON map encrypted with XChaCha20-Poly1305 (24-byte random
  nonce, associated data `kyokai/secrets/v1`) in `secrets.bin`, written atomically.
- The 32-byte master key comes from the operating system: on Windows a DPAPI blob for the current
  user (`master.key`), on macOS a generic password in the login Keychain (service
  `com.yuanzhe.kyokai`, account `master-key` or `master-key.<profile>`).
- Without a working OS store nothing is persisted: secrets live in memory for that run and the
  interface says the Device ID will change. A store that exists but cannot be decrypted is never
  overwritten; that run uses a temporary identity and says so.
- Secrets are zeroized when dropped; `kyokai_stop` drops them all.

## Device identity

- An Ed25519 signing key, created with the OS random source and kept in the secrets store.
- The Device ID is derived from the public key: SHA-256 over `kyokai/device-id/v1` and the key, its
  first 8 bytes as a number, reduced into 100 000 000–999 999 999. It locates a device; it never
  proves one. The fingerprint (SHA-256 of the public key) is the full identity.

## Temporary password

8 symbols from `abcdefghijkmnpqrstuvwxyz23456789` (no 0, 1, l, o), about 40 bits, from the OS
random source. It exists only in the core's memory, is new on every start and on request, and
reaches the GUI only when the user shows or copies it; a random serial tells the GUI it changed
without revealing it.

## Logging

Logs record versions, the Device ID and how secrets are kept — never a password, key or secret.
The engine writes `logs/engine.log` in the state directory.

## Local API

Only the engine's own user is served (peer credentials, never the request body); see
[architecture.md](architecture.md).

## Known limitations

- On a machine without a working credential store (or with it locked) the identity is not
  persisted.
- The temporary password can be shown and copied by anyone at the unlocked desktop; the tray never
  shows it.
