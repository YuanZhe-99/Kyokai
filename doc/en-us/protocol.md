# Wire protocol

A controller reaches a host's engine over the EasyTier data plane: TCP to the host's virtual IPv4
address, port 47310 ([networking.md](networking.md)). EasyTier is an untrusted pipe, so the session
is authenticated and encrypted end to end on top of it. The protocol version is 4 (2 added the AI and clipboard messages, 3 the session actions, 4 the scan-code key event of the keyboard modes).

## Framing

Every frame is a 2-byte big-endian length followed by that many bytes (at most 65 535). The first
three frames are Noise handshake messages; every later frame is one Noise transport message whose
plaintext is a JSON document, a media chunk (below), or a part of a JSON document longer than one
frame: byte `0x02`, the part's index and the number of parts (u16 each, big-endian), then a piece.
A document's parts arrive in order, one document at a time, at most 4 MiB in all; anything else
closes the session.

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
| 5 | controller → host | `login`: the slot whose response the password opened and its credential finalization with the controller's description (`about`: name, maker, model, system), or no slot when neither opened |
| 6 | host → controller | `verdict`: accepted with the granted permissions, a resume token and the host's description (`about`), or refused with a reason name (`password`, `denied`, `timeout`, `busy`, `rate-limited`, `absent`: nobody can confirm) |

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
5 seconds, and `bye`. A session with no frame for 15 seconds is closed. Video and input follow
under Media, AI tasks under their own heading.

## Media

A plaintext that starts with byte `0x01` is a media chunk, not JSON: the frame number (u32), the
chunk index and count (u16 each), flags (bit 0: key frame) and the presentation time in
microseconds (u64), all big-endian, then a part of the frame. A frame is split into as many chunks
as it needs and put back together on the other side; a chunk of a newer frame drops an unfinished
older one (latest frame wins), and frames over 64 MiB are dropped. As the session starts the
controller sends `{"type": "codecs", decode, i444}`: the codecs it decodes (`h264`, `hevc` and
`vp9` through Media Foundation, `vp9-sw`, `av1` and `vp8` in software) and those it decodes in
full colour (`vp9-sw`, `av1`; a field older builds leave out). Before its first frame the host
sends `{"type": "video-start", codec, width, height, i444}` (`i444` only when true); the controller may send `{"type":
"key-frame"}` (the host then describes the stream again and sends a key frame) and, with
`control-input`, `{"type": "input", event}`. While it sends video the host sends `{"type":
"test-delay"}` once a second, which the controller echoes with `reply: true`, and `{"type":
"stats", delay_ms, fps, bitrate}` ([computer-use.md](computer-use.md)); the controller may set
the quality with `{"type": "quality", preset, fps, i444}`, where `i444` asks for full colour
(4:4:4) where both sides do it. With more than one display the host also sends `{"type": "displays", list, current}` (each
display's place and size, the primary first) and the controller may ask for another with
`{"type": "display", index}`. While it sends video the host also sends `{"type": "cursor", x, y, visible}`, its pointer as
fractions of the picture, when it moves. With `control-input` the host sends
`{"type": "actions", ctrl_alt_del, blocked, privacy}` as the session starts and whenever the block
or privacy mode changes, and the controller may send `{"type": "action", action}` with `lock`,
`ctrl-alt-del`, `block`, `unblock`, `privacy-on` or `privacy-off` ([computer-use.md](computer-use.md)). With `clipboard`, either side sends
`{"type": "clipboard", text}` for text copied on its side (at most 1 MiB) and
`{"type": "clipboard-image", png}` for an image (base64 of an 8-bit RGBA PNG, at most 3 MiB and
3840×2160). The host sends video only while the
controller may view the screen: its screen in the codec chosen from `codecs`, or the 320×180 raw RGBA test pattern (codec
`raw-rgba`) on a host that cannot capture.

## AI tasks

| Message | Direction | Needs | Does |
|---|---|---|---|
| `{"type": "ai-start", key, instruction}` | controller → host | `run-ai-task` | starts a task on the host for this controller; a repeated key starts one task |
| `{"type": "ai-command", id, command, text}` | controller → host | `run-ai-task` | `pause`, `resume`, `cancel` or `reply` (with `text`) for a task this controller started |
| `{"type": "ai-decide", id, approve}` | controller → host | `approve-ai-action` | answers the approval of a task this controller started |
| `{"type": "ai-task", task}` | host → controller | — | the newest task this controller started, whenever it changes and when a session (re)starts; its latest 20 actions and 3 progress notes |
| `{"type": "ai-error", error}` | host → controller | — | why a request was refused |

Commands are never sent again after a reconnect; the host sends the current task instead. Input
from a controller with `control-input` pauses a task that holds the desktop (a take-over) before it
is injected; pointer moves alone are dropped while a task acts. The controller's engine passes
`ai-task` and `ai-error` to its GUI as media packets 5 and 6; a newer task replaces one still
waiting, like the stats (packet 4).

## Decoders

Every frame is size-checked before it is read, decrypted before it is parsed, and parsed into
fixed types; unknown message types close the session. The protocol is versioned: a mismatch closes
the connection with a reason.
Randomized tests feed both ends garbage, wrongly framed and truncated input, also after a real
first handshake message, and damaged identity backups: every connection ends, nothing panics, and a
damaged backup never changes the identity.
