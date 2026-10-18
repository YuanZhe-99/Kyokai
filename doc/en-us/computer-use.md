# Computer use

How a controller sees and operates a host, and how the host's AI operates it (Phase 3 manual
control, Phase 4 AI tasks).

## Video

| Stage | Where | What |
|---|---|---|
| Capture | host engine | one display — the primary unless the viewer chose another — through RustDesk's DXGI desktop duplication (`core/third_party/rustdesk-dxgi`), or its GDI capturer where duplication cannot open the display or DXGI lists none (no GPU output, some virtual machines and remote sessions); a still screen yields no frame (GDI compares each picture with the last) |
| Scale and convert | host engine | BGRA into NV12 for Media Foundation and VideoToolbox, or planar I420 (I444 in full colour) for libvpx and libaom — BT.601, limited range — scaled down to fit 1920×1080, sides even, by the nearest pixel (each column's and row's source found once per size; an unscaled picture goes row by row) |
| Encode | host engine | H.264 through Media Foundation: the GPU's encoder (an asynchronous transform, fed system-memory NV12 when it asks for input) if it produces output within its first 30 black frames — one per process, shut down when its stream ends; other streams use the software encoder — else Microsoft's synchronous software encoder; low latency (set before the media types; the first output follows the first frame), CBR 4 Mbit/s, no B-frames, a key frame every 4 seconds and on request; 30 fps |
| Encode (macOS) | host engine | H.264 and HEVC through VideoToolbox (`core/kyokai-core/src/vt.rs`): the hardware encoder required, with low-latency rate control where it takes it (its low-latency encoder, `rtvc`, runs on the hardware though VideoToolbox's encoder list calls it software: 1080p takes 8 ms a frame on an M1 either way, 2 ms of it CPU for our own conversion, 2026-10-18), then without, then any encoder where there is no hardware; real time, no frame reordering, the average bitrate with at most one and a half times that in any second (a picture after a still screen stays small), a key frame every 4 seconds and on request; NV12 from the session's pixel buffer pool in, Annex B out with the parameter sets before each key frame, as Media Foundation's encoders give it (`annexb.rs`) |
| Encode (software) | host engine | VP9 (`vp9-sw`) and VP8 through libvpx, AV1 through libaom — RustDesk's wrappers (`core/third_party/rustdesk-codec`) and settings: real-time, CBR at RustDesk's quality ratio of the base bitrate, a key frame every 4 seconds and on request; VP9 profile 1 and AV1 profile 1 carry 4:4:4 |
| Carry | session | media chunks in the Noise channel ([protocol.md](protocol.md)); one frame waits for the session at most: the capture loop takes a picture only once the session has room for its frame, so what goes out is the newest picture, not one that waited behind the network. The host keeps at most 3 frames ahead of the controller's acknowledgements (`received`, sent as each frame's last chunk arrives), so on a slow path (a relay passing a few hundred kbit/s) the frames wait at the capture rather than in the transport's buffers, and the picture and the delay probe stay a round trip behind, not seconds. Frames from the helper's session agent arrive as it captures and get one more place; one that finds none is left out with the frames after it until a key frame, which the agent is asked for |
| Forward | controller engine | the session's media queue → `session.media` on the local API; a GUI that is 3 frames behind loses the frames waiting and the frames after them until a key frame (a frame without the ones before it cannot be decoded), which the controller asks the host for, again each second until it comes |
| Decode | GUI | the GUI's own copy of the core (`kyokai_decoder_*`): Media Foundation's decoder for `h264`, `hevc` and `vp9` in low-latency mode (Microsoft's for H.264, the Store's video extensions for HEVC and VP9; HEVC on the GPU through Direct3D 11), NV12 into RGBA, cropped to the stream's size; on macOS VideoToolbox for `h264` and `hevc` (NV12 out, into RGBA the same way); libvpx or libaom for `vp9-sw`, `vp8` and `av1`, I420 or I444 into RGBA. Media Foundation decodes HEVC on the GPU and H.264 and VP9 in software (they get no Direct3D device); each decoder says which it is and whether it runs in hardware (`kyokai_decoder_info`) |
| Show | GUI | the viewer window's bitmap from three pictures: the one shown, the newest decoded one waiting for the main thread, and the one the next frame is decoded into; decoding never waits for drawing, a picture not drawn yet gives way to a newer one, and the pictures of a new stream are ready before its first frame |

While a frame waits inside the encoder, the host repeats the last picture so it comes out even when
the screen stays still. A host that cannot capture (no interactive desktop, such as a service or an
SSH session) sends the 320×180 test pattern instead (`raw-rgba`); a host without Media
Foundation's encoders (Windows N, Server editions without Media Foundation) sends in software. When the display is lost — a mode change, the lock
screen or a UAC prompt — the host releases it and opens the primary display again every half
second; the stream keeps its size and a new mode is scaled into it (a changed aspect ratio shows
stretched until the next session).

With more than one display the host sends the list (the primary first, the others left to
right) with the stream, and the viewer offers them by number; choosing one restarts capture on it
and moves input and the pointer there. AI tasks always use the primary display.

Desktop duplication leaves the pointer out of the picture, so the host reports where its pointer
is — at most 60 times a second, when it moves or hides — and the viewer marks it with a small ring
that lets clicks through.

The controller says which codecs it decodes, and which of them in full colour, as the session
starts; the host waits up to a second for that before it starts the video. It tries, in order,
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
Foundation's registered encoders, VideoToolbox's and the software ones. The viewer's status bar
lists them under Codec by name only, Auto first (H.264, HEVC, VP9, AV1, VP8; a host offering VP9 from
Media Foundation and libvpx is asked for its first, the better one; the stats name the codec in use
the same way). A chosen codec goes first in the host's order, in full colour first
when Full colour is on and the codec carries it, and the others stay behind it, so a codec whose
encoder does not open falls back as before; the video restarts only when the first choice
changes. The viewer sends its quality and codec as its stream starts, and the controller's engine
keeps them and sends them again, before its codecs, when the session resumes, so the host opens
the video as the viewer chose (they are settings, not input).

**Full colour (4:4:4).** The viewer's status bar has a Full colour switch. When it is on and the
controller decodes software VP9 or AV1 in 4:4:4 (every current build does), the host puts those
first in full colour (I444: chroma at every pixel, so one-pixel coloured text and lines stay sharp)
and restarts the video; switching it off goes back to the normal order. Full colour costs more
bandwidth and encoder time than 4:2:0, and the stream description says when it is on (the status
bar shows "4:4:4"). libaom on Windows ARM64 is built without its SIMD code (vcpkg's port), so AV1
is slow there; VP9 comes first.

The viewer asks for a key frame when it opens; the host then describes the stream, its displays
and the codec offer again and starts a new group of pictures.

## Quality and bandwidth

The host runs RustDesk's video QoS (`core/third_party/rustdesk-qos`, its upstream tests included):
once a second it sends a delay probe in the video stream, which the controller echoes at once, so
the probe measures the queue in front of the video, not just the path. The replies, any probe still
outstanding and the frames sent per second drive the frame rate (from 15 up to 30) and the quality
ratio; the encoder's bitrate is RustDesk's base bitrate for the picture size times that ratio
(Balanced: 0.67, about 1.4 Mbit/s at 1080p), changed while it runs. Once a second the host also
reports the delay, frame rate and bitrate it chose; the viewer shows the delay.

The viewer's status bar offers RustDesk's presets — Faster (ratio 0.5), Balanced (0.67) and
Sharper (1.5) — a frame-rate cap of 15, 30 or 60, and the Full colour switch; they reach the host as
`{"type": "quality", preset, fps, i444}` and set that viewer's image quality and custom frame
rate in the QoS, and whether it gets full colour (above).

With the stats the host reports how long its frames took from picture to frame (`encode_ms`, the
average of the last second's), and its stream description names its encoder and whether it runs in
hardware (`encoder`, `hardware`, as the encoder reports). The tooltip of the viewer's numbers shows
what encodes on the host and what decodes here (for example "HEVC, VideoToolbox in hardware"; AV1
is always libaom in software), the host's encode time, the network delay (the probe), the viewer's
own decode and draw times and the frame rate against the host's target.
Beside the numbers the bar says whether the network reaches the device directly (Direct) or
through the server (Relayed, with a hint to turn on direct connections on both devices,
[networking.md](networking.md)).

## Sound

The host sends what the computer plays to a controller that may view the screen and asks for it
(`{"type": "audio", on}`; the viewer asks as its stream starts, and a resumed controller asks again).
`audio.rs` captures it — on macOS through a ScreenCaptureKit stream of the main display that carries
only sound (Kyokai's own left out; the Screen Recording permission covers it), on Windows through
WASAPI loopback of the default output device in its own format — turns it into 48 kHz stereo
(resampling linearly when the device runs at another rate, doubling mono) and encodes 20 ms Opus
packets at 96 kbit/s (`opus.rs`, libopus built by vcpkg). In the helper's modes the session agent
captures and encodes it and the link carries the packets. The packets travel as plaintexts tagged
`0x04`, beside the video and outside its acknowledgements; a session that falls behind drops sound
(at most half a second waits in the host, 200 ms in the controller's queue). The viewer decodes them
in its own copy of the core (`kyokai_audio_*`) and plays them through MyGo's audio output, keeping no
more than 150 ms ahead of what was played: a burst, or a host whose clock runs fast, loses sound
rather than drifting behind the picture. The bar's **Sound** switch (on unless chosen, kept for each
device) stops playing and asks the host to stop capturing. On this Mac (2026-10-18) an 8-second
watch received 386 packets, 7.7 s of sound, with the picture.

## Input

The viewer sends input only for a session granted `control-input`; the host checks it again.

| Event | Sent as |
|---|---|
| pointer move, press, release | `{kind: "pointer", action, button (0 primary, 1 secondary, 2 middle), x, y}`, x and y as fractions of the picture; presses and moves on the bars around it are dropped, releases always sent; a move with no button held goes with button 0 (the host knows what it holds). The viewer sends the newest move at most every 8 ms, and any other event sends a waiting move first; the controller's engine may leave out a move while the session is behind (a newer one follows), and every other event waits for the session |
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
releases always pass. Kyokai's windows are left out of capture, so a check by focus alone refused
clicks on windows the viewer and the model do see while Kyokai's unseen window was in front.
Whatever a session holds is released when it ends. Malformed events are dropped without ending the session.

### Keyboard modes

The viewer offers RustDesk's three keyboard modes; its bar chooses one for every viewer and Kyokai
remembers it (Map unless chosen). Map and Translate need the controller to grab the keyboard: a
low-level hook on Windows, an event tap on macOS (as RustDesk's grab does there; it needs Kyokai's
Accessibility permission on the controlling Mac, and without it the viewer works in Legacy mode and
says so). A Mac's tap takes the keys going to Kyokai while a viewer grabs, by their Mac key codes
turned into the PC scan codes the host maps back (Command is the Windows key); keys no scan code
names (Fn, the media keys) stay the Mac's, and Caps Lock, which a Mac reports only as it toggles,
goes as a press and a release. The tap takes keys only while Kyokai owns the frontmost window and
the viewer reads them (it lets every key pass when either stops), a viewer whose tab is hidden or
whose connection ended releases its grab, and the tap is switched off and invalidated with its
thread (left installed, it froze every program's keyboard until Kyokai quit). The picture takes text while it has the focus: macOS makes text of
keys only for an element that does, and Legacy sends letters as that text (with it missing, no
letter reached the host from a Mac). In Legacy mode a Mac's menu takes Cmd+C, X, V, A, Z and
Shift+Cmd+Z before the window sees them; their edit commands are sent as those shortcuts again.

**Swap Ctrl and Cmd** (in the bar where a Mac is on either side, kept for each device) exchanges
Ctrl and Cmd, the Windows key, in what the viewer sends: in a key's modifiers and in the left and
right keys sent by position. It is on unless chosen between a Mac and Windows, so Cmd+C on a Mac
copies on Windows and Ctrl+C on Windows copies on a Mac (RustDesk has the same switch, off by
default). A key sent by name brings its modifiers on the host and releases them with it, and none
are held while text is typed (they stayed down after a shortcut, so the letters typed next came
with Ctrl or Cmd).

| Mode | On the controller | On the host |
|---|---|---|
| Map | while the picture has the focus and its window is in front, a low-level keyboard hook in the core takes every key by scan code before the controller's windows see it: Win, Alt+Tab, Alt+F4 and every shortcut go to the host; CapsLock, NumLock and ScrollLock act on the controller too | each key by scan code, so the host's layout decides what it types |
| Translate | a key that makes a character on the controller's layout with the modifiers held (a dead key too; not with Win, or with Ctrl or Alt alone) stays with the controller, whose layout and input method type it, and its text is sent; Shift, Ctrl, Alt and the lock keys go both ways; AltGr stays the controller's; every other key and shortcut is taken by scan code | text as Unicode, the other keys by scan code |
| Legacy | the GUI toolkit's key events as above: no Win key, no shortcut the window consumes, no left or right modifier, numpad keys by their meaning | named keys and Unicode |

Each key the grab takes carries the controller's CapsLock and NumLock: before a letter the host
presses its own CapsLock if it differs, before a numpad key whose meaning NumLock changes its
NumLock, as RustDesk's input service does. The keys Windows makes up (the Ctrl that comes with
AltGr, the Shift around numpad keys) are not sent; the host's layout makes its own. Keys the grab
sent down are released when their window leaves the front, its grab ends or the mode changes; keys
other programs inject on the controller are left alone. The hook is installed with the first grab
and removed with the last. Ctrl+Alt+Del cannot be taken (Windows shows it to no hook); the bar's
button sends it in the helper modes.

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

While a session is connected the host keeps the computer and its displays awake (a power
request, "Kyokai: someone is connected"; in the helper modes the session agent holds it for the
displays while it streams video, since Windows ignores a display request from session 0), so the idle timer neither cuts the session nor darkens
the captured display. When the computer goes to sleep anyway, the AI task in progress pauses
("the computer went to sleep") and waits for a person to resume it; after waking, sessions
resume through the network as after any drop.

## Clipboard

While a session has `clipboard`, both engines look at their clipboard twice a second (on Windows
reading it only when its sequence number changed) and offer what was copied since the session
began to the other side, which puts it on its own clipboard; what arrived this way is not offered
back. What was on a clipboard before the session is not offered,
nor is text that Kyokai itself copied (the Device ID, a password: on Windows the clipboard's owner
is a Kyokai process). Text up to 1 MiB, or an image up to 3840×2160 sent as PNG (at most 3 MiB; text wins when both are
there; files are under Files). In the helper modes images from a controller are not taken, because the
helper and its agent are privileged and never decode remote media ([run-modes.md](run-modes.md)).

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

## AI tasks

A task runs on the host, with the host's provider key, on the host's primary display. The host's
user starts it on the AI page (or with `kyokai-svc ctl ai-start`). A controller granted
`run-ai-task` starts tasks and steers the host's task, whoever started it (the host keeps one AI
record for every controller), from the viewer's AI panel, where the draft of the next instruction
is shared live with the host's AI page and every other controller
([protocol.md](protocol.md), "AI tasks"); with `approve-ai-action` it also answers their approvals,
which the host's user can always answer too. One task holds the desktop at a time, and starting
twice with one idempotency key starts one task.

### Providers

The host's user sets up **sources** (Settings of the AI page; `ai.json`): each has an id, a name,
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
the same Kyokai-window guard and tag.

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
to 100; the AI page clears it. A finished task's conversation stays in memory, so a reply
continues it until the engine stops.

## macOS

| Part | How |
|---|---|
| Permissions | Screen Recording for capture, Accessibility for input, Input Monitoring for the local-input watch, and the Keychain item of the secrets store ([security.md](security.md)); the engine reports them (`permissions` in the status); the GUI asks for them on its first start per version and through Allow on its permissions card — the privacy permissions in its own process (`kyokai_privacy_request`), so that a prompt belongs to the app, whose grant covers the engine (macOS counts the engine as part of it), and the Keychain through the engine (`permission.request` `keychain`). macOS 27 shows no prompt for either process: Allow opens the pane, where Kyokai is switched on (Accessibility's pane is called Device Control and Data Access there), which also opens the pane in System Settings. A Screen Recording grant counts once Kyokai restarts, which the card's Reopen Kyokai does (it stops the engine first) |
| Displays | Core Graphics' active displays in points, the unit of its global coordinates (a Retina display is half its pixels); the main display is the primary |
| Capture | a ScreenCaptureKit stream of the display at its pixel size scaled to fit 1920×1080, BGRA, without the pointer (the viewer draws it from the pointer messages) and, while Settings' Hide Kyokai from capture is on (the default), without the windows of Kyokai's processes and app: the GUI tells the engine the setting as it attaches and whenever it changes (`capture.show`; in the helper modes the helper passes it to its agent), a running stream takes the new filter at once, and AI screenshots follow it too; an unchanged screen sends no picture; without the permission the host sends the test pattern |
| Encode | VideoToolbox's H.264 and HEVC (above), then the software codecs (`vp9-sw`, `av1`, `vp8`) |
| Decode | the viewer decodes `h264` and `hevc` through VideoToolbox and the software codecs through libvpx and libaom |
| Input | Core Graphics events posted at the HID level and tagged as Kyokai's: pointer events in points with click counts for double and triple clicks, wheel notches as lines, named keys and modifiers (`ctrl` → Control, `alt` → Option, `meta` → Command) with the held modifiers' flags, text as Unicode, scan codes through a table to Mac key codes (Caps Lock follows the controller's; a Mac has no NumLock). The guard looks up the owner of the window under the point (or the frontmost normal window for keys) and drops events for this engine or the app that started it |
| Local input | a listen-only event tap (Input Monitoring) notes the host user's presses and moves, skips Kyokai's tagged events and watches for Ctrl+Option+Shift+Esc |
| AI tasks | the main display at full resolution through a short ScreenCaptureKit stream per screenshot, the same input and watch |

## Not yet

The session actions on macOS come with the Mac pass (PLAN.md).
