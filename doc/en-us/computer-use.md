# Computer use

How a controller sees and operates a host, and how the host's AI operates it (Phase 3 manual
control, Phase 4 AI tasks).

## Video

| Stage | Where | What |
|---|---|---|
| Capture | host engine | one display — the primary unless the viewer chose another — through RustDesk's DXGI desktop duplication (`core/third_party/rustdesk-dxgi`), or its GDI capturer where duplication cannot open the display or DXGI lists none (no GPU output, some virtual machines and remote sessions); a still screen yields no frame (GDI compares each picture with the last) |
| Scale and convert | host engine | BGRA into NV12 (BT.601, limited range), scaled down to fit 1920×1080, sides even |
| Encode | host engine | H.264 through Media Foundation: the GPU's encoder (an asynchronous transform, fed system-memory NV12 when it asks for input) if it produces output within its first 30 black frames — one per process, shut down when its stream ends; other streams use the software encoder — else Microsoft's synchronous software encoder; low latency (set before the media types; the first output follows the first frame), CBR 4 Mbit/s, no B-frames, a key frame every 4 seconds and on request; 30 fps |
| Carry | session | media chunks in the Noise channel ([protocol.md](protocol.md)); the producer waits while the session is busy, so capture runs at the speed the network takes |
| Forward | controller engine | the session's media queue (newest 3 frames kept) → `session.media` on the local API |
| Decode | GUI | the GUI's own copy of the core (`kyokai_decoder_*`): Media Foundation's decoder for the stream's codec in low-latency mode (Microsoft's for H.264, the Store's video extensions for HEVC and VP9), NV12 into RGBA, cropped to the stream's size |
| Show | GUI | the viewer window's bitmap, two pictures alternating |

While a frame waits inside the encoder, the host repeats the last picture so it comes out even when
the screen stays still. A host that cannot capture (no interactive desktop, such as a service or an
SSH session) or has no H.264 encoder (Windows N, Server editions without Media Foundation) sends the
320×180 test pattern instead (`raw-rgba`). When the display is lost — a mode change, the lock
screen or a UAC prompt — the host releases it and opens the primary display again every half
second; the stream keeps its size and a new mode is scaled into it (a changed aspect ratio shows
stretched until the next session).

With more than one display the host sends the list (the primary first, the others left to
right) with the stream, and the viewer offers them by number; choosing one restarts capture on it
and moves input and the pointer there. AI tasks always use the primary display.

Desktop duplication leaves the pointer out of the picture, so the host reports where its pointer
is — at most 20 times a second, when it moves or hides — and the viewer marks it with a small ring
that lets clicks through.

The controller says which codecs it decodes as the session starts, and the host waits up to a
second for that before it starts the video: H.264 whenever the controller decodes it, else HEVC,
else VP9 (a controller without H.264, such as Windows N). The HEVC and VP9 encoders and decoders
come from the GPU or the Store's video extensions. HEVC from this machine's GPU encoder did not
decode at 720p and above with Microsoft's HEVC decoder (it did at 320×192, and the software HEVC
encoder's stream decoded at 1080p), so H.264 stays first until that is understood.

The viewer asks for a key frame when it opens; the host then describes the stream again and starts
a new group of pictures.

## Quality and bandwidth

The host runs RustDesk's video QoS (`core/third_party/rustdesk-qos`, its upstream tests included):
once a second it sends a delay probe in the video stream, which the controller echoes at once, so
the probe measures the queue in front of the video, not just the path. The replies, any probe still
outstanding and the frames sent per second drive the frame rate (from 15 up to 30) and the quality
ratio; the encoder's bitrate is RustDesk's base bitrate for the picture size times that ratio
(Balanced: 0.67, about 1.4 Mbit/s at 1080p), changed while it runs. Once a second the host also
reports the delay, frame rate and bitrate it chose; the viewer shows the delay.

The viewer's status bar offers RustDesk's presets — Faster (ratio 0.5), Balanced (0.67) and
Sharper (1.5) — and a frame-rate cap of 15, 30 or 60; they reach the host as `{"type":
"quality", preset, fps}` and set that viewer's image quality and custom frame rate in the QoS.

## Input

The viewer sends input only for a session granted `control-input`; the host checks it again.

| Event | Sent as |
|---|---|
| pointer move, press, release | `{kind: "pointer", action, button (0 primary, 1 secondary, 2 middle), x, y}`, x and y as fractions of the picture; presses on the bars around it are dropped, releases always sent |
| scroll | `{kind: "scroll", dx, dy}` in lines (touchpad pixels ÷ 40); positive `dy` moves the content down |
| a key without Ctrl, Alt or Win that types | nothing: its text follows |
| text | `{kind: "text", text}`: typed as Unicode characters, so the controller's layout and dead keys decide |
| other keys and shortcuts | `{kind: "key", key, down, mods}` by name (`enter`, `f5`, `page-down`, `c` with Ctrl, …); a release only when its press was sent |

The host injects with `SendInput`: absolute moves over the display shown (mapped onto the virtual
desktop, so a display left of or above the primary works too), extended-key flags
for the navigation keys, modifiers pressed and released to match each key, every event tagged as
Kyokai's (`dwExtraInfo`) so local input can be told apart. Nothing is injected into a Kyokai
window: pointer presses and moves are dropped when the window under the point is Kyokai's, the
wheel when the window under the pointer is, keys and text when Kyokai's window has the focus;
releases always pass. Kyokai's windows are left out of capture, so a check by focus alone refused
clicks on windows the viewer and the model do see while Kyokai's unseen window was in front.
Whatever a session holds is released when it ends. Malformed events are dropped without ending the session.

## Clipboard

While a session has `clipboard`, both engines look at their clipboard twice a second (on Windows
reading it only when its sequence number changed) and offer what was copied since the session
began to the other side, which puts it on its own clipboard; what arrived this way is not offered
back. What was on a clipboard before the session is not offered,
nor is text that Kyokai itself copied (the Device ID, a password: on Windows the clipboard's owner
is a Kyokai process). Text up to 1 MiB, or an image up to 3840×2160 sent as PNG (at most 3 MiB; text wins when both are
there); files come later. In the helper modes images from a controller are not taken, because the
helper and its agent are privileged and never decode remote media ([run-modes.md](run-modes.md)).

## AI tasks

A task runs on the host, with the host's provider key, on the host's primary display. The host's
user starts it on the AI page (or with `kyokai-svc ctl ai-start`). A controller granted
`run-ai-task` starts and steers its own tasks from the viewer's AI panel
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

## Not yet

The remote cursor, other monitors, hardware encoders and the other codecs are Phase 3 steps that
follow; AI tasks on macOS come with the Mac pass (PLAN.md).
