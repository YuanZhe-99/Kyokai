# Computer use

How a controller sees and operates a host, and how the host's AI operates it (Phase 3 manual
control, Phase 4 AI tasks).

## Video

| Stage | Where | What |
|---|---|---|
| Capture | host engine | one display — the primary unless the viewer chose another — through RustDesk's DXGI desktop duplication (`core/third_party/rustdesk-dxgi`), or its GDI capturer where duplication cannot open the display or DXGI lists none (no GPU output, some virtual machines and remote sessions; a capture that fell back to GDI while DXGI lists the display tries duplication again every 2 seconds, as it fails for a moment around a UAC prompt); a still screen yields no frame (desktop duplication's wait times out, GDI compares each picture with the last), which never counts as a lost display |
| Scale and convert | host engine | the GPU scales a duplicated desktop to fit 1920×1080, sides even, and turns a rotated display the right way up before the picture leaves it (a shader of four bilinear taps per pixel, an exact 2×2 average at 4K → 1080p, `dxgi/scale.rs`), through one staging texture kept per size: at 4K, 8 MB cross to the CPU instead of 33 MB, as ScreenCaptureKit hands a Mac host its pictures. GDI pictures, a desktop image kept in system memory and a GPU the pass fails on are scaled by the CPU by the nearest pixel (each column's and row's source found once per size; an unscaled picture goes row by row). Then BGRA into NV12 for Media Foundation and VideoToolbox, or planar I420 (I444 in full colour) for libvpx and libaom — BT.601, limited range |
| Encode | host engine | H.264 through Media Foundation: the GPU's encoder (an asynchronous transform, fed system-memory NV12 when it asks for input) if it produces output within its first 30 black frames — one per process, shut down when its stream ends; other streams use the software encoder, and a stream that replaces another (a display, codec or full-colour change, a resumed session) waits up to a second for the old one to let go of it — else Microsoft's synchronous software encoder; low latency (set before the media types; the first output follows the first frame), CBR 4 Mbit/s, no B-frames, a key frame every 4 seconds and on request; 30 fps. As VideoToolbox takes pictures from its pool, the picture is converted straight into one of at most 4 input samples, each used again once the frame it carried came out (its output's time says which; a picture repeated while it is still inside the transform goes in a copy), and a transform that wants its output sample given gets the same one each time, its attributes cleared: nothing is allocated or copied per frame beyond the conversion |
| Encode (macOS) | host engine | H.264 and HEVC through VideoToolbox (`core/kyokai-core/src/vt.rs`): the hardware encoder required, with low-latency rate control where it takes it (its low-latency encoder, `rtvc`, runs on the hardware though VideoToolbox's encoder list calls it software: 1080p takes 8 ms a frame on an M1 either way, 2 ms of it CPU for our own conversion, 2026-10-18), then without, then any encoder where there is no hardware; real time, no frame reordering, the average bitrate with at most one and a half times that in any second (a picture after a still screen stays small), a key frame every 4 seconds and on request; NV12 from the session's pixel buffer pool in, Annex B out with the parameter sets before each key frame, as Media Foundation's encoders give it (`annexb.rs`) |
| Encode (software) | host engine | VP9 (`vp9-sw`) and VP8 through libvpx, AV1 through libaom — RustDesk's wrappers (`core/third_party/rustdesk-codec`) and settings: real-time, CBR at RustDesk's quality ratio of the base bitrate, a key frame every 4 seconds and on request; VP9 profile 1 and AV1 profile 1 carry 4:4:4 |
| Carry | session | media chunks in the Noise channel ([protocol.md](protocol.md)); one frame waits for the session at most: the capture loop takes a picture only once the session has room for its frame — room the acknowledgements leave, which the session tells the capture (and the helper its agent, with the frame rate and bitrate) — so what goes out is the newest picture, not one that waited behind the network or for an acknowledgement. The host keeps at most 3 frames ahead of the controller's acknowledgements (`received`, sent as each frame's last chunk arrives), so on a slow path (a relay passing a few hundred kbit/s) the frames wait at the capture rather than in the transport's buffers, and the picture and the delay probe stay a round trip behind, not seconds. Every controller acknowledges every frame over an ordered, reliable stream, so a late acknowledgement means a slow path, never a lost frame: the host waits for it however long it takes (a dead connection ends by the 15-second idle limit) and logs "the path is slow" once after 5 seconds (taking the frames for lost after 3 seconds, as RustDesk's bounded wait does, sent three more and a fresh key frame every 3 seconds onto a path that had not delivered the last ones; at a few hundred kbit/s the backlog grew without end, the delay reached seconds, the picture froze, input paused and the session broke and resumed every 25 seconds or so). The session hands its chunks to a writer of its own, which sends input, acknowledgements, sound and the session's other messages ahead of the video's chunks waiting ([protocol.md](protocol.md), "Session messages"). Frames from the helper's session agent arrive as it captures and get as many places as the host keeps ahead; one that finds none is left out with the frames after it until a key frame, which the agent is asked for, again every 250 ms while frames are left out (a key frame that found no place too) |
| Forward | controller engine | the session's media queue → `session.media` on the local API; a GUI that is 3 frames behind loses the frames waiting, except the newest key frame among them, and the frames after them until a key frame (a frame without the ones before it cannot be decoded), which the controller asks the host for, again each second until it comes, on a timer of its own (a still screen sends nothing to wait for) |
| Decode | GUI | the GUI's own copy of the core (`kyokai_decoder_*`): Media Foundation's decoder for `h264`, `hevc` and `vp9` in low-latency mode (Microsoft's for H.264, the Store's video extensions for HEVC and VP9; through Direct3D 11 on the GPU for HEVC, and for H.264 where the GPU has an H.264 decoder, as a Mac decodes both through VideoToolbox), NV12 into RGBA, cropped to the stream's size, read in place from the decoder's buffer (a GPU surface mapped with its own pitch) as from VideoToolbox's pixel buffer; on macOS VideoToolbox for `h264` and `hevc`; libvpx or libaom for `vp9-sw`, `vp8` and `av1`, I420 or I444 into RGBA. VP9 through Media Foundation decodes in software (it gets no Direct3D device); each decoder says which it is and whether it runs in hardware (`kyokai_decoder_info`) |
| Show | GUI | the viewer window's bitmap from three pictures: the one shown, the newest decoded one waiting for the main thread, and the one the next frame is decoded into; decoding never waits for drawing, a picture not drawn yet gives way to a newer one, and the pictures of a new stream are ready before its first frame |

While a frame waits inside the encoder, the host repeats the last picture so it comes out even when
the screen stays still. A key frame asked for stays asked until one comes out: on a still screen
the last picture is encoded again for it, and the encoder is asked again every half second (Media
Foundation's asynchronous encoder may answer with frames it held; it waits a little for its own
frame's output, and its error event, `MEError`, counts as a failure). An encoder that takes
pictures and gives nothing for 2 seconds has failed too: a GPU's encoder may give no output while
the display is off and stay silent once it is back (Qualcomm's, on the ARM64 test computer:
[platform-notes.md](platform-notes.md)), so a stream takes the GPU's encoder only while the system
reports the display on — a dark display is woken and given 2 seconds first, and the first look
waits for Windows' first report of the display state — uses the software
encoder otherwise, and moves back to the GPU when the display comes on. A failed encoder is logged
with what it was fed and gave, and opened again, with the display, for a first picture (on the
software encoder when the GPU's failed with the display on); a second failure before a key frame
went out ends the stream. When the stream ends, or the capture ends otherwise (in the helper
modes, an agent that went or a capture thread that could not follow the input desktop), the
session's recovery takes over.

The host never gives up on the picture. Its session watches the stream: frames ending, an open
that found no display, no encoder or no agent, no frame for 5 seconds although one was due (since
the start or a key frame asked for; not while 3 frames wait for acknowledgements, since those
frames wait for the path, and the time starts again as acknowledgements make room), key frames
that keep being asked for after one went out, and — from a controller that announced `recovery` —
the viewer saying it shows no picture ([protocol.md](protocol.md)). A request for a key frame, or
the viewer's `no-picture`, while the last key frame sent is not acknowledged yet waits for that one
instead of forcing another (on a slow path a fresh one would queue behind it). Each counts as trouble and takes the next step: open the video again
at once, then after 2 seconds, then in the next codec offered, then a placeholder while the real
capture is tried again after 2, 4 and 8 seconds and then every 10 seconds; a step gets 4 seconds
to work before the next counts. The placeholder (the 320×180 test pattern, `raw-rgba`) carries why
(`video-start`'s `placeholder`: the display asleep, no display, Kyokai Helper's agent away, no
encoder, no codec in common, or the screen not capturable), and the viewer shows that, not the
pattern. A display the system reports back on is tried at once; a picture that keeps coming for 10
seconds starts the count over. An open that finds no display while the system reports it asleep
wakes it and looks again first (macOS leaves a sleeping display out of its list). A host that
cannot capture (no interactive desktop, such as a service or an SSH session, or a system Kyokai
cannot capture on yet, `cannot-capture`) shows the placeholder for good, without recovery steps; a host without Media
Foundation's encoders (Windows N, Server editions without Media Foundation) sends in software. When the display is lost — a mode change, the lock
screen or a UAC prompt — the host releases it and opens the primary display again every half
second; the stream keeps its size and a new mode is scaled into it (a changed aspect ratio shows
stretched until the next session). In the helper modes, where the agent follows the desktop that
receives input ([run-modes.md](run-modes.md), "The session agent"), a capture also compares that
desktop with its own every half second, whatever it captured (GDI goes on copying the old
desktop's black), and takes a change — the lock screen, the sign-in screen or a UAC prompt coming
or going — for a lost display; a display that opened while the input desktop moved, or a GDI
capture whose retry of desktop duplication had to move the thread, opens again on the new desktop.
A new desktop duplication that hands over no picture for a second (a still lock or sign-in screen
draws nothing) gets one from GDI, and duplication is tried again 2 seconds later; GDI that cannot
start is tried again a second later, and a duplication that comes back gets the same fallback. Each
failure to open the display again is logged once.

With more than one display the host sends the list (the primary first, the others left to
right) with the stream, and the viewer offers them by number; choosing one restarts capture on it
and moves input and the pointer there. AI tasks always use the primary display.

Desktop duplication leaves the pointer out of the picture, so the host reports where its pointer
is — at most 60 times a second, when it moves or leaves the display shown — and the viewer marks it
with a small ring that lets clicks through. The ring shows where input lands whether or not the
host's system draws its cursor: Windows hides it with no mouse attached, except during a drag, and
the ring once showed only then.

The controller says which codecs it decodes, and which of them in full colour, as the session
starts; the host waits up to a second for that before it starts the video, once: every later
opening of the video (a recovery step, another display) keeps the controller's codecs (up to 0.6.2
the elapsed wait ran again whenever a recovery step closed the video, which then opened on the
primary display as for a controller that names no codecs — on a Mac host the "no codec"
placeholder, for good). It tries, in order,
H.264, HEVC and VP9 through Media Foundation (Windows hosts) or H.264 and HEVC through
VideoToolbox (macOS hosts), then VP9, AV1 and VP8 in software, and sends in the first one the
controller decodes whose encoder opens — so a host or controller without the system's codecs
(Windows N) falls back to software VP9. HEVC goes before H.264 when the host has a free hardware
HEVC encoder as the controller's codecs arrive (Media Foundation's, checked in the engine's
process and kept for the session, or the Mac's media engine; a Windows encoder that then fails
its trial falls back to the software HEVC encoder). A Mac controller decodes H.264, and HEVC
where its media engine does, through VideoToolbox; both platforms' streams are Annex B, so a Mac
decodes a Windows host's stream and the other way round. The HEVC and VP9 encoders and decoders of Media Foundation come from the
GPU or the Store's video extensions. A controller decodes HEVC on its GPU only (Direct3D 11) and
offers it only when its GPU has an HEVC decoder: Qualcomm's hardware HEVC encoder splits pictures
wider than 1024 pixels into uneven tile columns, and the HEVC Video Extension's software decoder
accepts such streams but never outputs a picture. A controller that names no codecs (an older
build) gets H.264.

**Codec choice.** After the controller's codecs the host offers what it can send that the
controller decodes (`codec-offer`, in the host's order): the codecs it has an encoder for — Media
Foundation's registered encoders, VideoToolbox's and the software ones. The viewer's toolbar
lists them under Quality, Codec, by name only, Auto first (H.264, HEVC, VP9, AV1, VP8; a host offering VP9 from
Media Foundation and libvpx is asked for its first, the better one; the stats name the codec in use
the same way). A chosen codec goes first in the host's order, in full colour first
when Full colour is on and the codec carries it, and the others stay behind it, so a codec whose
encoder does not open falls back as before; the video restarts only when the first choice
changes (so too when the controller's codecs arrive after the host started in H.264, or Full
colour switches). The viewer sends its quality and codec as its stream starts, and the controller's engine
keeps them and sends them again, before its codecs, when the session resumes, so the host opens
the video as the viewer chose (they are settings, not input).

**Full colour (4:4:4).** The viewer's Quality menu has a Full colour choice. When it is on and the
controller decodes software VP9 or AV1 in 4:4:4 (every current build does), the host puts those
first in full colour (I444: chroma at every pixel, so one-pixel coloured text and lines stay sharp)
and restarts the video; switching it off goes back to the normal order. Full colour costs more
bandwidth and encoder time than 4:2:0, and the stream description says when it is on (the toolbar's
path tooltip shows "4:4:4"). libaom on Windows ARM64 is built without its SIMD code (vcpkg's port), so AV1
is slow there; VP9 comes first.

The viewer asks for a key frame when it opens; the host then describes the stream, its displays
and the codec offer again and starts a new group of pictures.

As RustDesk's client does, the viewer's reading never waits on decoding: one goroutine reads the
session's packets and hands frames to a video goroutine (on its own OS thread, which the decoder is
tied to; up to 4 frames wait) and sound to a sound goroutine (up to 50 packets); the host's
reports it handles itself. When the video queue is full a frame is left out, and so is every frame
after it until a key frame, which alone lets frames through again; the viewer asks the host for
one at once and again each second until it comes, and so it does after a frame its decoder fails
on (the decoder then takes nothing but a key frame). A key frame that leaves a fresh decoder
without a picture because its parameter sets are missing is such a failure, not a wait. Once a
second it also looks at the picture: when none came for 2 seconds although the host says it sent
frames (`frames` in its stats; a still screen sends none, a 0.4.0 host does not say), or none has
come yet, it asks for a key frame and says "Recovering the picture…" over the picture shown; after
5 seconds without one it tells a `recovery` host (`no-picture`, with why: a decode error, still
waiting, or a stale picture), and the GUI log gets one line for the stretch. The controller's own
queue, when it overflows for a reader that is behind, keeps the newest key frame waiting and
skips to it, so the reader gets one decodable picture rather than none. A `recovery` host says
what its video is doing in its stats (`video`: live, dark, recovering or placeholder), and the
viewer shows that — the screen asleep or off and being woken, Kyokai Helper getting ready, the
picture starting again — instead of guessing; it offers **Wake it** only when the host says its
display is dark or asleep (an older host: after 3 seconds without a picture, as before). While the
host says it will not try again (no codec in common, a screen it cannot capture), or no picture of
its screen has shown for 10 seconds, the viewer offers **Reconnect**: the session is closed and
dialed again, as Reconnect after an end does, and the new one starts its video afresh. Sound
that finds its queue full is left out. A slow picture so never holds up sound, input or the
host's reports.

## Quality and bandwidth

The host runs RustDesk's video QoS (`core/third_party/rustdesk-qos`, its upstream tests included):
once a second it sends a delay probe in the video stream, which the controller echoes at once, so
the probe measures the queue in front of the video, not just the path. The replies, any probe still
outstanding and the frames sent per second drive the frame rate (from 15 up to 30) and the quality
ratio; the encoder's bitrate is RustDesk's base bitrate for the picture size times that ratio
(Balanced: 0.67, about 1.4 Mbit/s at 1080p), changed while it runs. Once a second the host also
reports the delay, frame rate and bitrate it chose; the viewer shows the delay.

The viewer's Quality menu offers RustDesk's presets — Faster (ratio 0.5), Balanced (0.67) and
Sharper (1.5) — a frame-rate cap of 15, 30 or 60, and Full colour; they reach the host as
`{"type": "quality", preset, fps, i444}` and set that viewer's image quality and custom frame
rate in the QoS, and whether it gets full colour (above).

RustDesk's QoS knows nothing of input: it raises the frame rate only from its probes, by 1 a second
below the preset's normal rate and by 5 after three good probes in a row, from 15. Dragging a window
or typing needs every frame at once, so for 2 seconds after a person's input (a key, a click, a
pointer move) the host sends at the viewer's cap while the measured delay is under 100 ms, and the
first input after a quiet spell applies it at once rather than at the next probe; with more delay
the QoS decides. Pointer moves cross as RustDesk's do, each as it comes, except that the viewer
keeps only the newest move not sent yet (one every 8 ms at most), so a stalled path never queues
stale moves ahead of a click.

With the stats the host reports how long its frames took from picture to frame (`encode_ms`, the
average of the last second's; from the moment the capture took the picture, so a Windows host's
copy to system memory counts too), and its stream description names its encoder and whether it runs in
hardware (`encoder`, `hardware`, as the encoder reports). The tooltip of the viewer's numbers shows
what encodes on the host and what decodes here (for example "HEVC, VideoToolbox in hardware"; AV1
is always libaom in software), the host's encode time, the network delay (the probe), the viewer's
own decode and draw times and the frame rate against the host's target.
Beside its icons the toolbar says whether the network reaches the device directly (Direct) or
through the server (Relayed, with a hint to turn on direct connections on both devices,
[networking.md](networking.md)).

## Sound

The host sends its sound to a controller granted `hear-sound` (its own permission, asked for and
granted by a correct password by default) that asks for it (`{"type": "audio", on}`; the viewer asks
as its stream starts, and a resumed controller asks again). What it shares is the host user's choice
in Access, **Sound to share** (the GUI's `sound_source`, sent to the engine as it attaches and when
it changes, `sound.source`; the helper passes it to its agent; a running stream switches at once):
what the computer plays, or one input device (`sound.devices` lists them: Core Audio's devices with
inputs on macOS, the active capture endpoints on Windows). `audio.rs` captures what the computer
plays on macOS through a ScreenCaptureKit stream of the main display that carries only sound
(Kyokai's own left out; the Screen Recording permission covers it; a display sleep ends that stream,
and a stream that stopped or could not start is started again every 2 seconds) and an input through an audio
queue (the Microphone permission: both apps carry its usage text and the `audio-input`
entitlement, and choosing an input asks for it); on Windows through WASAPI, loopback of the default
output device or the input device, in their own format. It turns the sound into 48 kHz stereo
(resampling linearly at another rate, doubling mono) and encodes 10 ms Opus packets in Opus'
low-delay mode at 96 kbit/s (`opus.rs`, libopus built by vcpkg; as RustDesk's), and sends none after
half a second of silence until sound returns (RustDesk's zero gate). In the helper's modes the
session agent captures and encodes it and the link carries the packets. The packets travel as
plaintexts tagged `0x04`, beside the video and outside its acknowledgements, and sound waiting
while a picture is sent goes between its chunks; a session that falls
behind drops sound (at most half a second waits in the host, 200 ms in the controller's queue). The
viewer decodes them in its own copy of the core (`kyokai_audio_*`) and plays them through MyGo's
audio output through a jitter buffer: it holds sound back until a target is gathered, at the start
and each time playing ran dry, then passes it on as it comes. The target starts at 80 ms, grows by
40 ms each time playing ran dry while sound came (a pause over 300 ms is the host's silence, not
jitter), up to 400 ms, and shrinks by 10 ms after 10 s without that, down to 40 ms; sound more than
60 ms beyond it is cut back to it, oldest first, so the sound stays with the picture. RustDesk
plays each packet as it comes from a buffer of up to 3 s, fades over 5 ms where it runs dry, and
trims the buffer when it stays above 600 ms. The toolbar's
**Sound** toggle (on unless chosen, kept for each device) stops playing and asks the host to stop
capturing. On this Mac (2026-10-18) an 8-second watch received 386 packets, 7.7 s of sound, with
the picture, and the input list named the built-in microphone.

## Input

The viewer sends input only for a session granted `control-input`; the host checks it again.

| Event | Sent as |
|---|---|
| pointer move, press, release | `{kind: "pointer", action, button (0 primary, 1 secondary, 2 middle), x, y}`, x and y as fractions of the picture; presses and moves on the black bars around it are dropped (the toolbar over it takes its own), releases always sent; a move with no button held goes with button 0 (the host knows what it holds). The viewer sends the newest move at most every 8 ms, and any other event sends a waiting move first (see "Input is a stream" below) |
| scroll | `{kind: "scroll", dx, dy}` in lines (touchpad pixels ÷ 40); positive `dy` moves the content down |
| a key, in Map or Translate mode | `{kind: "scan", code, down, caps, num}`: the key's scan code as Windows numbers it (the set-1 code, 0xE0xx for extended keys, 0xE11D for Pause) with the controller's CapsLock and NumLock ([Keyboard modes](#keyboard-modes)) |
| text | `{kind: "text", text}`: typed as Unicode characters, so the controller's layout and dead keys decide (Translate and Legacy) |
| Legacy: a key without Ctrl, Alt or Win that types | nothing: its text follows |
| Legacy: other keys and shortcuts | `{kind: "key", key, down, mods}` by name (`enter`, `f5`, `page-down`, `c` with Ctrl, …); a release only when its press was sent |

The host injects with `SendInput`: absolute moves over the display shown (mapped onto the virtual
desktop, so a display left of or above the primary works too), scan-code keys with
`KEYEVENTF_SCANCODE` (and the extended flag for 0xE0 codes; Pause by its virtual key), named keys
with extended-key flags for the navigation keys and modifiers pressed and released to match each key, every event tagged as
Kyokai's (`dwExtraInfo`) so local input can be told apart. Nothing is injected into a Kyokai
window: pointer presses and moves are dropped when the window under the point is Kyokai's, the
wheel when the window under the pointer is, keys and text when Kyokai's window has the focus;
releases always pass. Only a person's input passes there too, and only while the host user turned
Security's Hide Kyokai from capture off (`input.kyokai`) and neither a controller waits for the
host user's answer nor the AI for an approval; the session decides per event and the helper's
agent follows what it is told with each event. Kyokai's windows are left out of capture, so a check by focus alone refused
clicks on windows the viewer and the model do see while Kyokai's unseen window was in front.
Whatever a session holds is released when it ends. Malformed events are dropped without ending the session.

**Input is a stream.** When the path stalls, input that waited would arrive late, as a burst
of stale keys, so the controller's engine never lets the GUI wait (`kyokai_media_write`): while
the session cannot take input — it is reconnecting, or a host that sends video has said nothing
for 1.5 seconds, or the session's queue is full — pointer moves, presses, clicks and text are
refused as behind and dropped. Releases are never refused: the engine keeps the keys and buttons
the session holds on the host, and a release it cannot deliver it owes. Once the path moves and
the queue is empty, it reconciles: to a host that announced `input-sync` ([protocol.md](protocol.md))
one `{"kind": "sync", held}` event with what is held now, and the host lets go of whatever else
this session holds; to a 0.4.0 host the releases themselves (those pressed again since left out).
Input and AI commands queued while a session reconnected are dropped as it resumes, never
delivered late (the host sends its current AI task instead; the viewer's quality, codec, sound and
clipboard choices among them are kept), and a resumed host session starts holding nothing: the
connection it replaces let go of what it held as it ended. The GUI hears whether input is paused
(media packet 15) and its toolbar says "Input paused — the connection is behind"; the viewer's
queue to the engine never drops a release either (a full queue keeps it aside and sends it once
the queue drained). Neither side's session waits on the network: input, acknowledgements, pings
and the host's stats go out ahead of the video's chunks waiting, and are read whatever the video
does (a session that wrote each frame whole before it read again read no input for seconds on a
slow path, and both directions stalled).

The keyboard grab steps aside while the session stalls: the viewer ends it while the session is
reconnecting, input was paused for more than half a second, or no frame and no stats came for 3
seconds from a host that sends video, and grabs again once input flows; meanwhile the
controller's keyboard is its own (Alt+Tab, the Windows key, its input method, other programs), and
what is typed in the picture goes as in Legacy mode, refused while behind. The grab takes keys only
while the GUI read one within the last second (a GUI that stopped reading never traps the
keyboard), and the hook never waits for its lock: a key that comes while the GUI's call holds it
passes on.

### Keyboard modes

The viewer offers RustDesk's three keyboard modes; its toolbar's Keyboard menu chooses one for every viewer and Kyokai
remembers it (Map unless chosen). Map and Translate need the controller to grab the keyboard: a
low-level hook on Windows, an event tap on macOS (as RustDesk's grab does there; it needs Kyokai's
Accessibility permission on the controlling Mac, and without it the viewer works in Legacy mode and
says so). A Mac's tap takes the keys going to Kyokai while a viewer grabs, by their Mac key codes
turned into the PC scan codes the host maps back (Command is the Windows key); keys no scan code
names (Fn, the media keys) stay the Mac's, and Caps Lock, which a Mac reports only as it toggles,
goes as a press and a release. The tap takes keys only while Kyokai is the frontmost application and
the viewer reads them (it lets every key pass when either stops), a viewer whose pane is hidden or
whose connection ended releases its grab, and the tap is switched off and invalidated with its
thread (left installed, it froze every program's keyboard until Kyokai quit). Each tap turns itself
on again with its own handle when macOS switches it off, and its run loop runs in half-second
slices that look at a stop flag, so a stop asked before it ran is not lost (in 0.6.1 a stopping tap
could clear the shared handle a newer tap needed, which then stayed off once macOS switched it off,
and a stop asked too early left a tap running).

The keyboard follows the pointer. In Map and Translate a viewer grabs while its picture shows and the
pointer is anywhere in its pane — its header, its toolbar, its clipboard panel, the picture — and the
picture keeps the text focus meanwhile, so typed text and keys go to the same session; with panes
side by side the keys go to the connection under the pointer. A Remote Desktop or VNC screen does
the same. With the pointer outside the panes (the sidebar, Home, outside the window) nothing is
grabbed and keys go to Kyokai's own interface (up to 0.6.1 the grab ended at the picture's edge, so
pointing at the toolbar ended it). The grab steps aside while Kyokai's own controls need the keys:
the Settings sheet, search, the + picker, and a pane's menu or split list; a menu ends every grab as
it opens (it shows after the frame, and MyGo says when an item is chosen, not when a menu is
dismissed), and the grab comes back with the choice or the next pointer movement over a picture.
The core takes the keys only while Kyokai's window is in front; on Windows
also only while it is the topmost window at the pointer (the root of `WindowFromPoint` is the
foreground window), so another program's window over the pointer takes them back, and keys the
grab sent down are released on the host. A Mac's tap asks only that Kyokai be the frontmost
application (NSWorkspace's, the one macOS sends the keys to; up to 0.6.5 the frontmost normal
window of the on-screen window list stood for it, and with Kyokai's window tiled to half the screen
the second dot turned amber and keys passed) and leaves the pointer to the viewer's grab (0.5.4 also asked that Kyokai own the window at the event's
location, read from the whole on-screen window list, where invisible windows of other programs and
of the system cover Kyokai's: the tap let every key pass while the viewer took itself for grabbing,
so a Mac sent no Enter, Tab, Delete, Shift or Command — letters and digits still arrived as text —
except with the pointer off the picture). The core holds one grab per window, so the GUI keeps which pane
holds each window's grab and only that pane ends it (`App.grabKeyboard`; as the shown pane changed,
the pane just hidden ended the grab the newly shown one had taken, which still dropped the window's
own key events, so only text reached the host: no Tab, Enter, Delete or Shift). The picture takes
text while it has the focus: macOS makes text of keys only for an element that does, and Legacy
sends letters as that text (with it missing, no
letter reached the host from a Mac). In Legacy mode a Mac's menu takes Cmd+C, X, V, A, Z and
Shift+Cmd+Z before the window sees them; their edit commands are sent as those shortcuts again.

A grab can stop taking keys without Kyokai being told: macOS switches a tap off when it waited too
long, and hands no key to any tap while another program holds Secure Event Input (Terminal's secure
keyboard entry, a password prompt); Windows removes a low-level hook that kept it waiting too long.
So a viewer renews its grab when its window becomes active again, when the pointer comes back into
its pane, after a key got past it, and when the core says the tap or hook is off, the grab gone or
another window's: on macOS the tap is turned on again, or started anew if its thread ended; on
Windows the hook is installed again. A key that reaches the window although the grab should have
taken it — in Map every key, in Translate Enter, Esc, Backspace, Tab, Delete, Insert, the arrows,
Home, End, Page Up and Down, the F keys and shortcuts — is sent as Legacy mode sends it, and so are
a Mac menu's edit commands (regression: such keys were dropped, so only letters reached the host, as
text, until Kyokai restarted). While it grabs, a viewer asks the core twice a second how its grab
stands (`kyokai_keyboard_state`): taking the keys, the tap or hook off, Secure Event Input with the
program holding it, another window's grab, the reader gone, another program in front. Two dots
under the toolbar's keyboard icon show it in Map and Translate: the first while Kyokai holds the
grab, the second green while the keys reach it and amber when they do not or a key got past it
within the last three seconds, the button's tooltip saying why in the person's words; both grey
without a grab. The GUI log notes when a grab starts letting keys pass and why, when it takes them
again, and each key that got past with what the core last said (another program in front is not
noted on its own: switching programs does that).

The controller's input method is off in the picture, in every mode, and in a Remote Desktop or VNC
screen too (MyGo's `InputMethod(false)`): only for that element while it has the keyboard focus —
Windows detaches the window's input context (`ImmAssociateContextEx(hwnd, 0, 0)`) and ends an open
composition, a Mac's keys skip the input method — so keys type the layout's characters and nothing
composes. It is back as the focus leaves the picture; the system's input-method setting is never
changed. Chinese and Japanese are typed with the host's own input method, switched on there
(Win+Space, for example), in Map mode.

**Swap Ctrl and Cmd** (in the toolbar's Keyboard menu where a Mac is on either side, kept for each device) exchanges
Ctrl and Cmd, the Windows key, in what the viewer sends: in a key's modifiers and in the left and
right keys sent by position. It is on unless chosen between a Mac and Windows, so Cmd+C on a Mac
copies on Windows and Ctrl+C on Windows copies on a Mac (RustDesk has the same switch, off by
default). A key sent by name brings its modifiers on the host and releases them with it, and none
are held while text is typed (they stayed down after a shortcut, so the letters typed next came
with Ctrl or Cmd).

| Mode | On the controller | On the host |
|---|---|---|
| Map | while the pointer is over the picture and its window is in front (and, on Windows, topmost there), a low-level keyboard hook in the core takes every key by scan code before the controller's windows see it: Win, Alt+Tab, Alt+F4 and every shortcut go to the host; CapsLock, NumLock and ScrollLock act on the controller too | each key by scan code, so the host's layout and input method decide what it types, as in RustDesk |
| Translate | a key that makes a character on the controller's layout with the modifiers held (a dead key too; not with Win, or with Ctrl or Alt alone) stays with the controller, whose layout types it, and its text is sent; Shift, Ctrl, Alt and the lock keys go both ways; AltGr stays the controller's; every other key and shortcut is taken by scan code. Nothing is held back for an input method (after a key that typed on a Chinese, Japanese or Korean layout, Enter, Esc, Backspace, Tab, the arrows, Space and the digits passed to the controller's for up to a second, and an Enter typed then could be lost) | text as Unicode, the other keys by scan code |
| Legacy | the GUI toolkit's key events as above: no Win key, no shortcut the window consumes, no left or right modifier, numpad keys by their meaning | named keys and Unicode |

Each key the grab takes carries the controller's CapsLock and NumLock: before a letter the host
presses its own CapsLock if it differs, before a numpad key whose meaning NumLock changes its
NumLock, as RustDesk's input service does. The keys Windows makes up (the Ctrl that comes with
AltGr, the Shift around numpad keys) are not sent; the host's layout makes its own. Keys the grab
sent down are released when their window leaves the front or the pointer, its grab ends or the mode changes. Keys
another program injects on the controller — another remote-control program operating it, as when
Kyokai runs inside a RustDesk session — are taken like typed ones; only Kyokai's own (tagged) and
characters injected as text (`VK_PACKET`, RustDesk's Translate and Legacy modes) are left alone,
and such text reaches the viewer, which sends it as text in every mode. The hook is installed with the first grab
and removed with the last. Ctrl+Alt+Del cannot be taken (Windows shows it to no hook); the Actions
menu sends it in the helper modes.

### Session actions

With `control-input` the viewer also offers RustDesk's session actions; each takes over from the
host's AI task, like input:

| Action | On the host |
|---|---|
| Lock screen | `LockWorkStation` in the user's session (the agent's, in the helper modes) |
| Ctrl+Alt+Del | only in the helper modes: the helper, a SYSTEM service, calls `SendSAS`; the policy that lets services do that (`SoftwareSASGeneration`) is switched on for the call and put back as it was |
| Block their keyboard and mouse | low-level hooks swallow the host's own keyboard and mouse while any session holds the block; Kyokai's injected input passes. The host user lifts it with Ctrl+Alt+Shift+Esc (which also stops an AI task) or Ctrl+Alt+Del, which no hook sees; it ends with the session |
| Privacy mode | RustDesk's privacy mode: a black, click-through, topmost window over the whole virtual desktop, left out of capture (`WDA_EXCLUDEFROMCAPTURE`), so the people at the host see black while the picture shows the screen; it blocks their keyboard and mouse too and is lifted the same way. Refused where Windows cannot leave the window out of capture |

The host reports whether Ctrl+Alt+Del is available, whether the block holds and whether privacy
mode is on; the viewer's switches show that report, not the request.

## Sleep

While a session is connected the host keeps the computer and its displays awake (on Windows a
power request, on macOS an idle system sleep and an idle display sleep power assertion, both named
"Kyokai: someone is connected"; taken when the first session connects and released with the
last; in the helper modes the session agent, in the user's session, holds one for the displays
while it streams video, since Windows ignores a display request from session 0), so the idle
timer neither cuts the session nor darkens the captured display. When the computer goes to sleep anyway, the AI task in progress pauses
("the computer went to sleep") and waits for a person to resume it; after waking, sessions
resume through the network as after any drop.

A dark display gives the capture nothing to send, just as a still screen does. A capture that
has had no picture for a second (none yet, or its display lost), or whose display Windows has
reported off for a second (the console display state, locked or not), wakes the displays as a
person's input would — on Windows, on the desktop that receives input (the lock screen too),
`SetThreadExecutionState(ES_DISPLAY_REQUIRED)` and an absolute pointer move one pixel aside and
back, tagged as Kyokai's input so the host user's input watch leaves it out; on macOS
`IOPMAssertionDeclareUserActivity` — at most every 5 seconds, in the user's session (the
agent's, in the helper modes). On Windows the execution state turns a dark display on within the
second (Kernel-Power's event 566 names it, reason 8); the pointer move runs on a thread of its own
on the desktop that receives input, so the capture thread keeps its desktop. A wake asked for
while the helper's agent is away is sent to the next agent. macOS reports a display asleep when
the main display sleeps or no display is active while one is connected
(`CGDisplayIsAsleep`, `CGGetOnlineDisplayList`); the capture, its recovery and the viewer see it
as Windows' display state. A viewer whose host says its display is dark or asleep (an older
host: after 3 seconds without a picture) says so and, with `control-input`, offers
**Wake it**: the `wake` session action, which wakes the displays the same way at once and asks
for a key frame; it takes nothing over from the host's AI and is not stopped by the guard that
keeps remote input off Kyokai's windows. A host without `wake` (0.5.1 and older) gets a Shift
press and release instead. A computer that sleeps or hibernates (Modern Standby too) has left the
network, which Windows cuts, as Tailscale and RustDesk find too: waking it needs Wake-on-LAN or
hardware, outside Kyokai.

## Clipboard

While a session has `clipboard`, both engines look at their clipboard twice a second (on Windows
reading it only when its sequence number changed) and offer what was copied since the session
began to the other side, which puts it on its own clipboard; what arrived this way is not offered
back. What was on a clipboard before the session is not offered,
nor is text that Kyokai itself copied (the Device ID, a password: on Windows the clipboard's owner
is a Kyokai process). Text up to 1 MiB, or an image up to 3840×2160 sent as PNG (at most 3 MiB; text wins when both are
there; files are under Files). In the helper modes images from a controller are not taken, because the
helper and its agent are privileged and never decode remote media ([run-modes.md](run-modes.md)).

On a controller with several connections, only the one whose pane is in front — the one in focus in
the Kyokai window used last; none while that is Home — exchanges the clipboard, as
RustDesk's client does: what is copied here goes to that device alone, and only that device's
copies reach this computer's clipboard. A copy made while a pane is behind waits, and goes when the
pane comes to the front (copied in another program, then pasted there); the other device's copies
that arrive meanwhile are dropped. The viewer's **Share the clipboard with this device** switch
(kept for each device) stops it altogether: nothing is offered or taken, transfers of files under
way end (`files-cancel`, `files-stop`), and switching it on again sends only later copies. The GUI
tells its engine both with `{"type": "clipboard-use", share, front}` on the session's media
connection, which the engine keeps for the session (a resumed one included) and never sends to the
host, so the wire protocol is unchanged; an engine that does not know it shares as before. A
host's side is unchanged: it offers its copies to every controller allowed `clipboard`.

The controller's engine reports what crossed — text, an image as PNG, or files as the offer lists
them — with its side (`from`: `controller` or `host`) and time to its GUI (media packet 13), which
lists it in the viewer's clipboard panel, newest first, at most 30 entries, in memory only: never
written to disk, gone when the pane closes. A GUI that does not read them keeps at most 8 waiting.

### Files

With the separate `files` permission, files and folders copied on one side (on Windows, Explorer's
file list, `CF_HDROP`) can be pasted on the other; `clipboard` alone carries no files, and `files`
alone carries no text. The copying side lists what was copied — folders walked, links and
junctions skipped — and offers each entry's path and size: at most 1000 entries and 1 GiB in all,
anything larger is not offered. The other side checks every path and the limits again and accepts
at once; it refuses paths that are empty, absolute, contain `.` or `..`, a character Windows does
not allow in a name (`< > : " / \ | ? *`, control characters), end in a dot or a space, name a
device (`CON`, `NUL`, `COM1`, `LPT1` and the like) or appear twice. The copying side then streams
the contents in pieces of up to 64 KiB, one file after another, reading as the connection takes
them, so a file is never held in memory whole. The receiving side writes them into a folder of
its own for the session (`Kyokai\<random>` in the temporary folder) and, once everything arrived,
puts those files on its clipboard, so they paste anywhere. A new copy replaces a transfer in
progress, and a file whose size changed while it was sent ends its transfer. The folder is
deleted when the session ends; files pasted before stay where they were pasted.

The contents cross as soon as the offer is accepted, not at the moment of pasting: Windows asks
the program that copied for delayed data while the pasting program waits, so a large transfer
would freeze it.

In the helper modes the agent reads and writes the clipboard (only the list of paths crosses
the pipe), and the helper reads and writes the files while impersonating the user signed in at
the console, so they are read with that user's rights and land in that user's temporary folder;
with nobody signed in, no files are shared. The helper only moves the bytes and never interprets
them.

## Host status

A controller with a saved password for a host, or a sign-in key the host admitted, watches that
host's status quietly while its main window is open, slowly while Home is hidden
([protocol.md](protocol.md), "Status watchers"): a
session of its own that asks only for `see-status`, carries no picture, input or sound, keeps no
awake request on the host and shows no banner there. The host admits a watcher only with a
correct password or key within its password grants while Connect without asking is on — it never
asks its user, and refuses (`denied`) instead — and unattended only with the permanent password or
a key. `see-status` is in the password grants by default; turned off there, a watch ends at its
next sample. A host serves at most 16 watchers besides its other sessions; they are not listed
among its sessions, leave no trace among its trusted devices, and the status lists them apart
(`watchers`: `{device_id, about, since}`).

While anyone watches, the host samples itself every 2 seconds on a thread of its own — every 5
seconds while all its watchers asked for the slow pace (`pace`), and again at once when one asks
for the usual pace and the last sample is older than 2 seconds — and sends each sample to every
watcher as a `system` message:

| Field | Windows | macOS | Linux |
|---|---|---|---|
| `cpu` `{usage, cores, user, system}` | `GetSystemTimes` (system: kernel time less idle) | `host_statistics` (`HOST_CPU_LOAD_INFO`; user with nice) | `/proc/stat` (user with nice; system with interrupts) |
| `memory` `{used, total}` | `GlobalMemoryStatusEx` | `hw.memsize`; app memory, wired and compressed from `host_statistics64` | `/proc/meminfo`: total less `MemAvailable` |
| `gpu` `{usage}` or null | the 3D engines' `Utilization Percentage` counters (PDH), as Task Manager sums them | the busiest `IOAccelerator`'s `Device Utilization %` | null (not read yet) |
| `disk` `{used, total}` | the disk Windows is on | the data volume | the file system of the home folder (`statvfs`) |
| `disk_io` `{read_bps, write_bps}` or null | the `PhysicalDisk(_Total)` read and write byte counters (PDH) | the `IOBlockStorageDriver`s' `Statistics`, turned into rates | null (not read yet) |
| `net` `{rx_bps, tx_bps, rx_total, tx_total}` | `GetIfTable2`, the hardware interfaces that are up | `getifaddrs`, the interfaces that are up, loopback left out | `/proc/net/dev`, the interfaces with a device in sysfs (no loopback, bridges or tunnels) |
| `battery` `{percent, charging}` or null | `GetSystemPowerStatus` | the power sources (IOKit) | the first system battery in `/sys/class/power_supply` |
| `uptime_s`, `at` | `GetTickCount64`; the host's clock (Unix milliseconds) | `kern.boottime` | `/proc/uptime` |
| `services` | which of SSH, Remote Desktop and VNC accept a connection on its loopback on the ports its user set, looked at every 30 seconds ([remote-services.md](remote-services.md)) | the same | the same |
| `offers` | per watcher: the permissions its password or key grants without asking (the password grants while Connect without asking is on, else none), so the controller offers only what will work | the same | the same |
| `ai` | per watcher: whether the host's AI could start a task now (a model chosen and, unless its server is on the host, its provider's key present) | the same | the same |

The controller's GUI holds a lease on the hosts it shows (`watch.set` `{devices, slow}`, at most
8, `slow` those of them at the slow pace; renewed every 10 seconds while the main window is open
or a status pane is, and at once as Home shows or hides; it lapses after 30 seconds). Home's hosts
are at the slow pace while Home is hidden — the main window under Settings or showing other panes
— and at the usual pace again as it shows, over the same watch; closing the main window lets them
go (up to 0.6.2 a hidden Home let them go, and every host was dialed again as it showed). A watch
session tells its host the lease's pace as it starts and whenever it changes. The engine watches
only recent hosts that announced `status` and for which it keeps a password or a working key;
otherwise it says why and dials nothing (`unsupported`, `no-credential`). It dials like a desktop
session, never resumes, and after a drop waits 10 seconds, doubling up to 5 minutes; a refused
password is forgotten and the watch stops (`refused`), and `rate-limited` waits 10 minutes. The
status carries the result in memory only: `watching` `[{device_id, state, sample, received_at}]`,
where `state` is `connecting`, `watching`, `no-credential`, `unsupported`, `refused` or `offline`
and `received_at` (Unix milliseconds) when the sample arrived here, so that an old sample is never
shown as current.

## AI tasks

A task runs on the host, with the host's provider key, on the host's primary display. The host's
user starts it in an AI session's pane ([gui.md](gui.md), "AI sessions"; or with
`kyokai-svc ctl ai-start`). A controller granted `run-ai-task` starts tasks and steers the
host's task, whoever started it (the host keeps one AI record for every controller), from an AI
session's pane for the host, over a session of its own asking only for `run-ai-task` and
`approve-ai-action`; the draft of the next instruction is shared live with the host's user and
every other controller ([protocol.md](protocol.md), "AI tasks"); with `approve-ai-action` it
also answers their approvals, which the host's user can always answer too. One task holds the
desktop at a time, and starting twice with one idempotency key starts one task.

### Providers

The host's user sets up **sources** (Settings › AI; `ai.json`): each has an id, a name,
the template it came from, a protocol, a base URL and the models used from it, and the task uses one
chosen model of one source. A new host has two: Claude (`https://api.anthropic.com/v1`) and OpenAI
(`https://api.openai.com/v1`). Templates add OpenRouter, Google Gemini, Groq, Together, Fireworks,
Deep Infra, Mistral, xAI, Moonshot, Z.ai / BigModel, Alibaba Cloud Model Studio, SiliconFlow,
Volcengine Ark, MiniMax, StepFun, Baidu Qianfan, Tencent Hunyuan, NVIDIA NIM, Hugging Face,
GitHub Models, Ollama, LM Studio and llama.cpp server (MyApps-AI's catalog, keeping the APIs that
take images and tool calls; sites with several endpoints offer them by name); Custom takes any
address and protocol. A base URL is HTTPS, or plain HTTP only to this computer (`localhost`,
`127.0.0.0/8`, `::1`), because screenshots go there; such a local chat server needs no key. Each
source's key is `ai.<id>.key` in the secrets store, and removing a source removes its key.
**List models** asks a source for its models (`GET <base>/models`; OpenAI-style, Ollama's and
OpenRouter's lists, without embedding, speech, image and moderation models) only when the user
asks; nothing is fetched by itself.

| Protocol | Used by | Request |
|---|---|---|
| `anthropic` | Claude | Messages API with the computer toolset (below) |
| `openai-responses` | OpenAI | Responses API with the `computer` tool (below) |
| `openai-chat` | every other template, Custom | chat completions with Kyokai's actions as function tools named and shaped like the computer toolset's members (`left_click` with `coordinate`, `key` with `text`, `scroll` with `scroll_direction` and `scroll_amount`, …), so one parser checks all three; the screen goes in user messages as an image, each call is answered with a `tool` message and the screen after the batch follows; only the newest 3 screenshots stay in the history. The model must accept images and call tools |

Models are shown as OpenRouter writes them, `Vendor: Model` (`Anthropic: Claude Opus 5.5`,
`Qwen: Qwen2.5 VL 72B Instruct`), with the source after a model reached through another one; the
name comes from the provider's own list or, by rule, from the id (MyApps-AI's naming rules, ported
in `internal/gui/modelname.go`).

| | Claude | OpenAI |
|---|---|---|
| Request | Messages API with `computer_toolset_20260801`, adaptive thinking with progress notes, effort `high`, server-side fallbacks, old tool results cleared by the server after 100,000 input tokens (the newest 5 stay) | Responses API with the `computer` tool, `previous_response_id`, `truncation: "auto"`, the instructions on every request |
| Answer | one `tool_result` per action; the history is append-only | one `computer_call_output` per call with the screen after its batch, and a note naming the actions that failed or did not run; safety checks acknowledged only after the host user approved them |
| Default model | `claude-opus-5-5` | `gpt-6.1-sol` |

Requests go over HTTPS (rustls with the ring provider and the system's roots), five minutes at
most each, retried three times on overload and server errors. Errors shown to the user never
contain the key.

### Screenshots and actions

The host captures its primary display with RustDesk's GDI capturer at the display's pixel size
(the capturing thread is per-monitor DPI aware), scales it to fit 1366×768 by averaging, and sends
it as PNG. A zoom crops a fresh capture and is never scaled up. Coordinates refer to the latest full
screenshot; the host maps them onto the display and injects through the input service above, with
the same Kyokai-window guard and tag, which the AI never passes.

| Action (`kind`) | Fields | Runs as |
|---|---|---|
| `screenshot`, `zoom` | `region` for a zoom | a capture, returned to the model |
| `cursor-position` | — | the pointer in screenshot pixels, as text |
| `move` | `to`, `held` keys | modifiers pressed, the pointer moved |
| `click` | `at` (or the pointer), `button`, `count`, `modifiers` | moves, then presses and releases (40 ms apart when repeated) |
| `mouse-down`, `mouse-up` | `button` | a press or release where the pointer is |
| `drag` | `path` of points, `modifiers` | press, eight moves per segment 10 ms apart, release |
| `scroll` | `at`, `dx`, `dy` (120 per notch), `modifiers` | wheel notches |
| `type` | `text` | Unicode text, one character every 30 ms (apps such as Windows 11 Notepad garble a faster burst); each line break presses Enter |
| `key-combo` | `keys`, `repeat` | the keys pressed with the combination's modifiers |
| `hold-key` | `keys`, `duration_ms` | pressed, held, released |
| `wait` | `duration_ms` | a pause |

Before each action the host checks that it parsed, that its points lie inside the latest
screenshot, its limits (text up to 16 KiB, up to 100 repeats, waits and holds up to 300 s, up to
100 notches, up to 256 drag points), that the screen still has the size it had in that screenshot
(otherwise the action is stale and rejected), and whether the host user must approve it. The first
failure stops the turn: the actions after it are answered "not executed". Every action ends by
releasing whatever it holds.

The approval policy is Flagged (the default) or Every action. Flagged asks when the provider raised
a safety check for the turn (once, before its first action) and for shortcuts with the Windows /
Command key. A declined action fails with "The host user declined this action", and the model
continues from there.

### States

| Event | From | To |
|---|---|---|
| pause (the host user, local input, the controller, a take-over, the controller's session ending) | running, approval | paused |
| resume | paused | running |
| cancel (also the emergency stop) | running, approval, paused | cancelled |
| an action needs approval | running | approval |
| the host user decides | approval | running |
| the model finishes | running, paused | done |
| an error | running, approval, paused | failed |
| the host user replies | done | running |

### The host user always wins

While a task runs, low-level keyboard and mouse hooks watch for local input: any event that Kyokai
did not inject, after the task was last given the desktop (started, resumed or an action
approved). A key, a button or the wheel counts at once; a pointer move after 1.5 seconds, so a hand
leaving the mouse after clicking Resume or Allow is not a take-over; while the task waits for
approval, local input does not count. Local input pauses a running task at once, whether the model
is thinking or an action is running; a held key or button is released. A person at a controller
who uses the keyboard or mouse in the viewer takes over the same way: the task pauses before their
input is injected, and pointer moves alone are not injected while the task acts. Every pause and
cancel moves the task's lease on: a model response that arrives afterwards is recorded but none of
its actions run, and its answer goes out only after a resume, telling the model that nothing ran
and that it should take a new screenshot. A cancel also drops the request in flight.
Ctrl+Alt+Shift+Esc (the emergency stop, while a task runs), the tray's Stop AI task item and
`ai.stop` cancel the task, and so does stopping the engine.

### History and settings

`ai.json` in the state directory holds the sources, the chosen model and the approval policy (a
file from the first versions, with one provider and a model each for Claude and OpenAI, is read
into the two default sources); the keys live in the secrets store (`ai.<source>.key`) and are never
shown again or sent anywhere but to their source. `ai-tasks.json` keeps the finished tasks — the instruction,
the model's text and progress notes, the actions and what became of them, never a screenshot — up
to 100; Settings › AI clears it (Recent tasks), with the AI sessions the GUI keeps in `gui.json`.
A finished task's conversation stays in memory, so a reply continues it until the engine stops.

## macOS

| Part | How |
|---|---|
| Permissions | Screen Recording for capture, Accessibility for input, Input Monitoring for the local-input watch, and the Keychain item of the secrets store ([security.md](security.md)); the engine reports them (`permissions` in the status); the GUI asks for them on its first start per version and through Allow on its permissions card — the privacy permissions in its own process (`kyokai_privacy_request`), so that a prompt belongs to the app, whose grant covers the engine (macOS counts the engine as part of it), and the Keychain through the engine (`permission.request` `keychain`). macOS 27 shows no prompt for either process: Allow opens the pane, where Kyokai is switched on (Accessibility's pane is called Device Control and Data Access there), which also opens the pane in System Settings. A Screen Recording grant counts once Kyokai restarts, which the card's Reopen Kyokai does (it stops the engine first) |
| Displays | Core Graphics' active displays in points, the unit of its global coordinates (a Retina display is half its pixels); the main display is the primary |
| Capture | a ScreenCaptureKit stream of the display at its pixel size scaled to fit 1920×1080, BGRA, without the pointer (the viewer draws it from the pointer messages) and, while Security's Hide Kyokai from capture is on (the default), without the windows of Kyokai's processes and app: the GUI tells the engine the setting as it attaches and whenever it changes (`capture.show`; in the helper modes the helper passes it to its agent, again whenever an agent starts), a running stream opens again on the new filter (half a second without pictures; updating its filter in place was tried once, and Kyokai stayed hidden when that did not take), and AI screenshots follow it too; an unchanged screen sends no picture; a program without a bundle identifier is never taken for Kyokai. Without the permission (checked before any ScreenCaptureKit call, so nothing waits on a prompt) the host shows the placeholder (the screen not capturable) and sends no sound at once. A stream that stops logs ScreenCaptureKit's error domain and code. A stream ends by removing its output and stopping, waiting up to 2 s; its completion keeps the stream alive until ScreenCaptureKit answers, and a start that did not answer in time is stopped the same way. Every callback from the system frameworks (pictures, sound, VideoToolbox, the event taps, the audio queue) catches its own panic, which the engine log records |
| Encode | VideoToolbox's H.264 and HEVC (above), then the software codecs (`vp9-sw`, `av1`, `vp8`) |
| Decode | the viewer decodes `h264` and `hevc` through VideoToolbox and the software codecs through libvpx and libaom |
| Input | Core Graphics events posted at the HID level and tagged as Kyokai's: pointer events in points with click counts for double and triple clicks, wheel notches as lines, named keys and modifiers (`ctrl` → Control, `alt` → Option, `meta` → Command) with the held modifiers' flags, text as Unicode, scan codes through a table to Mac key codes (Caps Lock follows the controller's; a Mac has no NumLock). The guard looks up the owner of the window under the point (or the frontmost normal window for keys) and drops events for any of Kyokai's processes — the app, the engine, Kyokai Helper — told by their executable as on Windows (before 0.4.0 it matched this engine and its parent, which missed the app in the helper modes, where launchd starts the agent) |
| Local input | a listen-only event tap (Input Monitoring) notes the host user's presses and moves, skips Kyokai's tagged events and watches for Ctrl+Option+Shift+Esc; the tap is switched off and invalidated with its thread, which ends even when the watch is dropped before its run loop runs |
| AI tasks | the main display at full resolution through a short ScreenCaptureKit stream per screenshot, the same input and watch |

## Linux

| Part | How |
|---|---|
| Session | the environment tells the session: Wayland when `$WAYLAND_DISPLAY` is set or `$XDG_SESSION_TYPE` says so, else X11 when `$DISPLAY` is set, else none (a service or an SSH login: the placeholder, the screen not capturable) |
| Wayland portal | one portal session for the engine, opened when a capture, an input or the display list first needs it and closed 10 seconds after the last use: Kyokai registers as `com.yuanzhe.kyokai` with the portal's host registry, then asks for RemoteDesktop with ScreenCast (keyboard and pointer, every monitor, the pointer drawn into the picture), else ScreenCast alone (no input); each first kept (persist mode "until revoked", the restore token in the state folder's `portal-token`, so the host user is asked once), then not where the portal keeps nothing; the host user declining ends it (the placeholder) |
| Displays | X11: RandR's monitors, the primary first (the leftmost when none is marked), the whole screen as one without RandR; Wayland: the portal's streams in its logical coordinates, the first taken for the primary |
| Capture (Wayland) | the display's PipeWire stream on the portal's remote, BGRx, BGRA, RGBx or RGBA in shared memory (RGB turned into BGR), copied tight; the compositor sends pictures only when the screen changes. Wayland tells no program where others' windows are, so Kyokai's windows can be neither blacked out nor kept from input: the GUI hides them while the host is controlled and says when one shows anyway (`kyokai.shown`), and while one does the picture holds still and no remote or AI input is injected (releases still are), unless Hide Kyokai from capture is off |
| Capture | the display's rectangle of the root window through MIT-SHM into a System V segment that only this user can read and that is removed as soon as the X server attached it, BGRA at the display's pixel size, without the pointer; a picture identical to the last is no picture (still screen), looked at up to 60 times a second; a display that moved or changed size is lost and opened again. While Hide Kyokai from capture is on, the parts of Kyokai's windows the screen shows (each Kyokai window less other programs' windows above it, from the window manager's stacking list, else the root's children, read every 250 ms) are blacked out before the picture leaves the capture; a window's process comes from the X-Resource extension, else `_NET_WM_PID`, and is Kyokai's by its executable as on the other systems |
| Encode | the software codecs (`vp9-sw`, `av1`, `vp8`) |
| Input | XTest: pointer moves and buttons on the root window, the wheel as buttons 4–7 by whole notches, named keys and scan codes through evdev codes (the X keycode is the evdev code plus 8, so the host's layout decides what a key types, as Map mode wants), Caps Lock and Num Lock following the controller's before a key they change; text through spare keycodes (those with no keysym), each run of characters mapped at once, one code per character, given 50 ms before its keys and before the codes change again (X programs read a changed mapping only when they next look a key up). The guard drops presses over Kyokai's windows (the topmost window at the point, the active window for keys) as on the other systems |
| Input (Wayland) | the RemoteDesktop portal on the portal thread: absolute moves on the display's stream, evdev buttons and keys (scan codes by place; the compositor keeps its own lock state), the wheel by whole notches, text as keysyms |
| Local input | X11: XInput 2 raw key, button and motion events of the master devices, XTest's virtual devices (Kyokai's own input) left out; Wayland: the input devices themselves (`/dev/input/event*`; Kyokai's input goes through the compositor and never shows there), which only Kyokai Helper or a member of the `input` group may read, so without them no AI task starts on Wayland; Ctrl+Alt+Shift+Esc is the emergency stop on both |
| Display power | DPMS tells whether the display is off; waking turns DPMS on and resets the screen saver |
| AI tasks | the primary display at full resolution through one MIT-SHM picture (X11) or the stream's newest picture (Wayland) per screenshot, the same input and watch; on Wayland the cursor is where the task last put it |

## Not yet

The session actions on macOS come with the Mac pass (PLAN.md).
