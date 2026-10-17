# Wire protocol

A controller reaches a host's engine over the EasyTier data plane: TCP to the host's virtual IPv4
address, port 47310 ([networking.md](networking.md)). EasyTier is an untrusted pipe, so the session
is authenticated and encrypted end to end on top of it. The protocol version is 1.

## Framing

Every frame is a 2-byte big-endian length followed by that many bytes (at most 65 535). The first
three frames are Noise handshake messages; every later frame is one Noise transport message whose
plaintext is a JSON document.

## Handshake

Noise `XX_25519_ChaChaPoly_BLAKE2s`, prologue `kyokai/session/v1`. Both sides use a fresh X25519
static key per session and prove their device identity by signing it.

| Frame | Direction | Noise | Payload (JSON) |
|---|---|---|---|
| 1 | controller → host | `e` | none |
| 2 | host → controller | `e, ee, s, es` | `host_hello`: protocol, product version, source URL, device key, signature |
| 3 | controller → host | `s, se` | `client_hello`: protocol, product version, device key, signature, requested permissions, two OPAQUE credential requests, and a resume token when resuming |

The signature is Ed25519 by the device key over `kyokai/noise-static/v1` followed by the sender's
Noise static public key. The controller refuses a host whose device key does not derive the Device
ID it dialed, or that differs from the key it pinned for that ID
([identity-and-access.md](identity-and-access.md)). `host_hello` carries the host's source URL,
which the controller shows (AGPL-3.0 §13).

## Password authentication

OPAQUE (`opaque-ke` 4: Ristretto255, TripleDH with SHA-512, Argon2id) runs inside the Noise channel
with the host's device key as the server identifier (the records exist before any controller is
known) and the Noise handshake hash as context, so a login cannot be replayed into another channel. The host has two slots, the temporary and the permanent password;
the controller sends one credential request per slot, both from the password the person typed.

| Frame | Direction | Message |
|---|---|---|
| 4 | host → controller | `challenge`: one credential response per slot; an unset slot answers with a fake record, so a controller cannot tell which slots exist |
| 5 | controller → host | `login`: the slot whose response the password opened and its credential finalization, or no slot when neither opened |
| 6 | host → controller | `verdict`: accepted with the granted permissions and a resume token, or refused with a reason name (`password`, `denied`, `timeout`, `busy`, `rate-limited`) |

A failed `login` counts as one failure ([identity-and-access.md](identity-and-access.md)). After a
correct password the host asks its user to confirm (up to 60 seconds) before the verdict.

## Resuming

A dropped connection (not a `bye`) can be resumed for 60 seconds: the controller dials again
every 3 seconds and sends the token from the last verdict. A token is single-use, bound to the
controller's device key, kept in the host's memory only and valid for 2 minutes; a valid one
answers frame 3 with an accepting verdict at once (same permissions, no password, no
confirmation, a new token); anything else falls back to the login. Nothing that was in flight is
sent again.

## Session messages

After an accepted verdict both sides exchange `{"type": …}` documents: `ping` / `pong` every
5 seconds, and `bye`. A session with no frame for 15 seconds is closed. Later phases add video,
input and AI messages to this channel.

## Decoders

Every frame is size-checked before it is read, decrypted before it is parsed, and parsed into
fixed types; unknown message types close the session. The protocol is versioned: a mismatch closes
the connection with a reason.
