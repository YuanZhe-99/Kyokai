# Wire protocol

A controller reaches a host's engine over the EasyTier data plane: TCP to the host's virtual IPv4
address, port 47310 ([networking.md](networking.md)), or, connecting by address, plain TCP to the
host's own address (port 47310 unless given). Neither path is trusted, so the session is
authenticated and encrypted end to end on top of it, the same on both. The protocol version is 10 (2 added the AI and clipboard messages, 3 the session actions, 4 the scan-code key event of the keyboard modes and files on the clipboard, 5 asking without a password, 6 the host's one AI record and the shared draft, 7 the codec offer and the viewer's codec, and the host's encode time in its stats, 8 the frame acknowledgements and the encoder in the stream description, 9 the host's sound, 10 the `hear-sound` permission and 10 ms sound packets).

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
| 3 | controller → host | `s, se` | `client_hello`: protocol, product version, device key, signature, requested permissions, two OPAQUE credential requests (or none, with the controller's description `about`, to ask without a password), a resume token when resuming, proofs with sign-in keys (see Key login), the service of a tunnel (see Tunnels), and `watch` for a status watch (see Status watchers) |

The signature is Ed25519 by the device key over `kyokai/noise-static/v1` followed by the sender's
Noise static public key. To learn whom an address reaches, the controller sends frame 1, reads
frame 2, checks its signature and closes the connection (`session.identify`; also how a
controller looks at a known host's addresses, [networking.md](networking.md), "Routes"); the host
keeps no session for it. Which route a controller takes changes nothing on the wire. The controller refuses a host whose device key does not derive the Device
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
| 6 | host → controller | `verdict`: accepted with the granted permissions, a resume token, the host's description (`about`) and how the controller got in (`via`: `key` with the admitting `key`, `password`, `confirmation` or `resume`; absent from 0.4.0), or refused with a reason name (`password`, `denied`, `timeout`, `busy`, `rate-limited`, `absent`: nobody can confirm, `service-down`: a tunnel's service did not answer) |

A failed `login` counts as one failure ([identity-and-access.md](identity-and-access.md)). A host that
refuses flushes its verdict and keeps the connection open until the controller closed it (at most 3
seconds): a data plane that drops unsent bytes with its socket would otherwise lose the reason. After a
correct password the host answers at once when the request is within its password grants, and
otherwise asks its user (up to 60 seconds) before the verdict. A `client_hello` without credential
requests skips frames 4 and 5: the host asks its user and sends the verdict as frame 4.

## Key login

To a host that announces `key-login`, and never when resuming, the controller adds `keys` to its
`client_hello`: for each key it offers, `{key, signature}`, the Ed25519 public key and its
signature over `kyokai/key-login/v1` followed by h2, the host's device key and the controller's
device key (32 bytes each), both base64. h2 is the Noise handshake hash after frame 2: the host
reads it right after writing frame 2, the controller right after reading it, so a proof holds only
in its own channel and between these two devices. With keys the hello carries the controller's
description (`about`) too, since a key may admit before any login. The host looks at most at 8
proofs ([identity-and-access.md](identity-and-access.md), "Keys"): a key that admits skips frames
4 and 5, and the verdict follows at once, or after the host's user decided when more was asked
for; otherwise the password login (or the request without one) goes on as above. An authorized
key whose proof fails is refused as `password`. A controller that offered keys waits for its first
answer as long as for a confirmation.

## Resuming

A dropped connection (not a `bye`) can be resumed for 60 seconds from that drop (a session that resumed and drops again has 60 seconds anew): the controller dials again
every 3 seconds and sends the token from the last verdict. A token is single-use, bound to the
controller's device key, kept in the host's memory only and valid for 2 minutes; a valid one
answers frame 3 with an accepting verdict at once (same permissions, no password, no
confirmation, a new token) and ends the session it resumes, which the host may not have noticed
dropping (it would keep its capture and encoder until its idle limit; as after any drop, an AI
task that controller started pauses); a resume is never refused as busy, since it takes that
session's place. Anything else falls back to the login. Nothing that was in flight is sent again.

## Session messages

After an accepted verdict both sides exchange `{"type": …}` documents: `ping` / `pong` every
5 seconds, and `bye`. A side that ends the session (closed, or its engine stopping: the engine ends
every session before it stops, waiting up to a second) sends `bye` and keeps the connection open
until the other side, which ends its session on it, closes it, or 500 ms passed: closed first,
the other side's next write would fail and it would take the connection for lost and resume it. A
session with no frame for 15 seconds is closed. Video and input follow
under Media, AI tasks under their own heading.

A running session hands its plaintexts to a writer of its own and never waits on the network. The
writer sends what is urgent first — everything but the video's chunks and file contents — and the
video's chunks in between; a stream's description (`video-start` and the displays message) keeps
its place among the video's chunks, after the old stream's frames and before the new one's, and
file contents wait while 8 or more video and file plaintexts are queued, so a copy neither fills
memory nor holds up input. Messages may therefore arrive with urgent ones ahead of a frame's
remaining chunks; a receiver takes other plaintexts between a frame's chunks, as it always took
sound.

## Media

A plaintext that starts with byte `0x01` is a media chunk, not JSON: the frame number (u32), the
chunk index and count (u16 each), flags (bit 0: key frame) and the presentation time in
microseconds (u64), all big-endian, then a part of the frame. A frame is split into as many chunks
as it needs and put back together on the other side; a chunk of a newer frame drops an unfinished
older one (latest frame wins), and frames over 64 MiB are dropped. As the session starts the
controller sends `{"type": "codecs", decode, i444}`: the codecs it decodes (`h264` and `hevc`
through Media Foundation or VideoToolbox, `vp9` through Media Foundation, `vp9-sw`, `av1` and `vp8`
in software, and `mjpeg`, a NanoKVM's own pictures) and those it decodes in
full colour (`vp9-sw`, `av1`; a field older builds leave out). The host answers with
`{"type": "codec-offer", codecs}`: what it can send that the controller decodes, in its order; the
controller may choose one with `{"type": "codec", codec}`, and asks for the host's sound with
`{"type": "audio", on}` (the host sends it only to a controller granted `hear-sound`, as
plaintexts that start with byte `0x04` followed by one 10 ms Opus packet of 48 kHz stereo, none
while it is silent, the packets waiting when a frame's chunk was sent going out before its next
chunk, [computer-use.md](computer-use.md), "Sound") (`auto`, or a name of the offer; another
name changes nothing), and a resumed controller sends its viewer's `quality`, `codec` and `audio` again
before its `codecs`. Before its first frame the host
sends `{"type": "video-start", codec, width, height, i444, encoder, hardware}` (`i444` and `hardware` only when true; `encoder`: `videotoolbox`, `media-foundation`, `libvpx` or `libaom`, as the encoder reports, left out for the test pattern; a `recovery` host adds `placeholder` to the test pattern's: `asleep`, `no-display`, `agent`, `no-encoder`, `retrying`, `no-codec`, `cannot-capture` or `nanokvm`). As the last chunk of each frame arrives the controller sends `{"type": "received", frame}` (the frame's number); the host sends a new frame only while fewer than 3 are unacknowledged, takes only an acknowledgement of a frame it sent and that was not acknowledged, and waits for acknowledgements however long they take: every controller (protocol 10) acknowledges every frame over an ordered, reliable stream, so a late one means a slow path, not a lost frame (a dead connection ends by the idle limit). The controller may send `{"type":
"key-frame"}` (the host then describes the stream, its displays and the codec offer again and
sends a key frame, unless the last key frame it sent is not acknowledged yet: that one is waited for) — its GUI's requests (as it opens, after a frame it lost or failed to decode,
each second until a key frame comes), and the controller's own while its queue leaves frames out
(at most once a second, on a timer of its own, so also while no frame comes), and to a host that announced `recovery`, `{"type": "no-picture", for_ms, why}` when its viewer has shown no picture for 5 seconds (`why`: `decode-error`, `waiting` or `stale`; at most once a second; the host takes it as trouble unless its last key frame is not acknowledged yet or a picture has been due for less than 5 seconds, [computer-use.md](computer-use.md), "Video") — and,
with `control-input`, `{"type": "input", event}`; to a host that announced `input-sync` an
event may be `{"kind": "sync", held: {scans, keys, buttons}}`, the keys (by scan code and by
name) and buttons the controller holds after a stall or a resume, and the host lets go of
whatever else that session holds (at most 64 entries, all of them known, or it is dropped). While it sends video the host sends `{"type":
"test-delay"}` once a second, which the controller echoes with `reply: true`, and `{"type":
"stats", delay_ms, fps, bitrate, encode_ms, frames}` (`encode_ms`: the last second's frames' average from
picture to frame; `frames`: how many frames it sent that second, which 0.4.0 leaves out); a `recovery` host adds `video`: `live`, `dark`, `recovering` or `placeholder`) ([computer-use.md](computer-use.md)); the controller may set
the quality with `{"type": "quality", preset, fps, i444}`, where `i444` asks for full colour
(4:4:4) where both sides do it. With more than one display the host also sends `{"type": "displays", list, current}` (each
display's place and size, the primary first) and the controller may ask for another with
`{"type": "display", index}`. While it sends video the host also sends `{"type": "cursor", x, y, visible}`, its pointer as
fractions of the picture, when it moves; `visible` says whether it is on the display shown. With `control-input` the host sends
`{"type": "actions", ctrl_alt_del, blocked, privacy}` as the session starts and whenever the block
or privacy mode changes, and the controller may send `{"type": "action", action}` with `lock`,
`ctrl-alt-del`, `block`, `unblock`, `privacy-on`, `privacy-off` or, to a host that announced `wake`, `wake` (an older host gets a Shift press and release from the controller's engine instead) ([computer-use.md](computer-use.md)). With `clipboard`, either side sends
`{"type": "clipboard", text}` for text copied on its side (at most 1 MiB) and
`{"type": "clipboard-image", png}` for an image (base64 of an 8-bit RGBA PNG, at most 3 MiB and
3840×2160). With `files`, either side offers files it copied with `{"type": "clipboard-files", id,
files}`, each file `{path, size, dir}` (a relative path with `/` between names; at most 1000
entries and 1 GiB); the other side accepts with `{"type": "files-get", id}` and may later ask the
sender to stop with `{"type": "files-stop", id}`, and the sender gives up an accepted offer with
`{"type": "files-cancel", id}` ([computer-use.md](computer-use.md), "Files"). The contents follow
as plaintexts that start with byte `0x03` and the offer's id (u64, big-endian), then bytes: every
file of the offer in its order, each exactly its `size`; a newer offer replaces an older one. The host sends video only while the
controller may view the screen: its screen in the codec chosen from `codecs`, or the 320×180 raw RGBA test pattern (codec
`raw-rgba`) while it cannot capture, which a `recovery` host never takes for final.

## Tunnels

A session may carry a tunnel to one of the host's own services instead of the desktop
([remote-services.md](remote-services.md)). To a host that announced `tunnel` the controller names
the service in `client_hello` (`tunnel`: `ssh`, `rdp` or `vnc`) and asks for its permission alone
(`use-ssh`, `use-rdp` or `use-vnc`); a controller never sends it to a host that did not announce
`tunnel`, and stops after frame 2 instead (`unsupported`). The host admits it like any permission,
then connects to its service on its loopback (127.0.0.1, then ::1) and sends the accepting verdict
only once that connection is open; when the service does not answer within 3 seconds it refuses
with `service-down`. A tunnel's verdict carries no resume token. After it the bytes travel as
plaintexts that start with byte `0x05`, each followed by up to one frame's worth of the stream, in
order, both ways, besides `ping`, `pong` and `bye`; there is no video, input or other message. A
side whose service or client ends sends `bye` after the bytes it read, and the other side ends
too. A tunnel with no frame from its peer for 60 seconds is closed.

## NanoKVM sessions

A session may carry the computer wired to the host's NanoKVM USB instead of the host's desktop
([remote-services.md](remote-services.md), "NanoKVM USB"). To a host that announced `nanokvm` the
controller sets `nanokvm: true` in `client_hello` and asks for `use-nanokvm` alone; it never sends
it to a host that did not announce `nanokvm`, and stops after frame 2 instead (`unsupported`). The
host refuses it as `service-down` when it does not offer its NanoKVM (before anyone is asked) or
its serial port does not open, and as `busy` while another is connected; otherwise it admits it
like any permission, with no resume token. The session then runs as a desktop's — the video with
`codecs`, `codec-offer` and acknowledgements, input events, `actions` — on the wired computer:
the codec offer adds `mjpeg` (each frame one JPEG, a key frame) when the host can send the card's
own pictures and the controller decodes them, the host offers the actions `ctrl-alt-del` and
`wake` (a Shift press), and there is no sound, clipboard, files, display list, pointer or AI.

## Status watchers

A session may watch the host's status instead of carrying the desktop
([computer-use.md](computer-use.md), "Host status"). To a host that announced `status` the
controller sends `watch: true` in `client_hello` and asks for `see-status` alone; it never sends it
to a host that did not announce `status`, and stops after frame 2 instead (`unsupported`). The host
admits a watcher only with a correct password or a key within its password grants while Connect
without asking is on, and never asks its user: anything else is refused as `denied`. A watcher's
verdict carries no resume token. After it the host sends `{"type": "system", at, uptime_s, cpu,
memory, gpu, disk, disk_io, net, battery, services, offers, ai}` with each sample, every 2 seconds, besides `ping`,
`pong` and `bye`; there is no video, input or other message. The watcher says the pace it wants
with `{"type": "pace", "slow": bool}` as the watch starts and whenever it changes, and the host
samples every 5 seconds while every watcher asked for `slow`; hosts before 0.6.3 ignore it, as
every host that announced `status` ignores message types it does not know, and keep to 2 seconds.
The host ends the watch with `bye` when `see-status` leaves its password grants, and either side
closes one with no frame for 15 seconds.

## AI tasks

| Message | Direction | Needs | Does |
|---|---|---|---|
| `{"type": "ai-start", key, instruction}` | controller → host | `run-ai-task` | starts a task on the host for this controller; a repeated key starts one task |
| `{"type": "ai-command", id, command, text}` | controller → host | `run-ai-task` | `pause`, `resume`, `cancel` or `reply` (with `text`) for the host's task, whoever started it |
| `{"type": "ai-decide", id, approve}` | controller → host | `approve-ai-action` | answers the approval of the host's task |
| `{"type": "ai-draft", text}` | either way | `run-ai-task` (controller → host) | the draft of the next instruction: a controller's reaches the host, which passes the shared draft (as anyone typed it last) to every controller |
| `{"type": "ai-task", task}` | host → controller | — | the host's newest task, the same for every controller whoever started it, whenever it changes and when a session (re)starts; its latest 20 actions and 3 progress notes |
| `{"type": "ai-error", error}` | host → controller | — | why a request was refused |

Commands are never sent again after a reconnect; the host sends the current task instead. Input
from a controller with `control-input` pauses a task that holds the desktop (a take-over) before it
is injected; pointer moves alone are dropped while a task acts. The controller's engine passes
`ai-task`, `ai-error` and `ai-draft` to its GUI as media packets 5, 6 and 10; a newer task replaces one still
waiting, like the stats (packet 4).

## Compatibility

The wire stays protocol 10; abilities added since 0.4.0 are announced, never assumed. `host_hello`
and `client_hello` carry `features`, a list of tokens; a build that sends the list (even empty)
ignores message types, permission names, session actions and AI commands it does not know, while
0.4.0 and older send none and end a session on them. So a newer build sends a new message type, a
new permission (in `want` or `granted`) or a new refusal only to a peer that announced the token it
belongs to, and a missing list means the 0.4.0 set. Both hellos are otherwise unchanged, and 0.4.0
ignores fields it does not know, so the two talk with what both have. A peer whose protocol lies
outside the range this build speaks (10 to 10) is refused as incompatible.

| Token | Brings |
|---|---|
| `key-login` | sign-in with an SSH-style key ([identity-and-access.md](identity-and-access.md)) |
| `tunnel` | tunnels to the host's own SSH, Remote Desktop and VNC; permissions `use-ssh`, `use-rdp`, `use-vnc` |
| `status` | quiet status watchers and the `system` message; permission `see-status` |
| `input-sync` | the controller's `sync` input event (the keys and buttons it holds) |
| `wake` | the `wake` session action (the host wakes its displays) |
| `recovery` | the controller's `no-picture`; the host's `placeholder` in `video-start` and `video` in its stats |
| `nanokvm` | sessions of the computer wired to the host's NanoKVM USB (`client_hello`'s `nanokvm`); permission `use-nanokvm` |

## Decoders

Every frame is size-checked before it is read, decrypted before it is parsed, and parsed into
fixed types; a message type or a name this build does not know is ignored (see Compatibility),
anything else malformed closes the session. The protocol is versioned: a peer outside the range
this build speaks is refused with a reason.
Randomized tests feed both ends garbage, wrongly framed and truncated input, also after a real
first handshake message, and damaged identity backups: every connection ends, nothing panics, and a
damaged backup never changes the identity.
