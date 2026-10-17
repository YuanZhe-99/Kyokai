# Computer use

How a controller sees and operates a host. This page grows with Phase 3 (manual control) and
Phase 4 (AI).

## Video

| Stage | Where | What |
|---|---|---|
| Capture | host engine | the primary display through RustDesk's DXGI desktop duplication (`core/third_party/rustdesk-dxgi`); a still screen yields no frame |
| Scale and convert | host engine | BGRA into NV12 (BT.601, limited range), scaled down to fit 1920×1080, sides even |
| Encode | host engine | H.264 through Media Foundation's synchronous Microsoft encoder: low latency (set before the media types; the first output follows the first frame), CBR 4 Mbit/s, no B-frames, a key frame every 4 seconds and on request; 30 fps |
| Carry | session | media chunks in the Noise channel ([protocol.md](protocol.md)); the producer waits while the session is busy, so capture runs at the speed the network takes |
| Forward | controller engine | the session's media queue (newest 3 frames kept) → `session.media` on the local API |
| Decode | GUI | the GUI's own copy of the core (`kyokai_decoder_*`): Media Foundation's H.264 decoder in low-latency mode, NV12 into RGBA, cropped to the stream's size |
| Show | GUI | the viewer window's bitmap, two pictures alternating |

While a frame waits inside the encoder, the host repeats the last picture so it comes out even when
the screen stays still. A host that cannot capture (no interactive desktop, such as a service or an
SSH session) or has no H.264 encoder (Windows N, Server editions without Media Foundation) sends the
320×180 test pattern instead (`raw-rgba`).

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

The host injects with `SendInput`: absolute moves over the captured display, extended-key flags
for the navigation keys, modifiers pressed and released to match each key, every event tagged as
Kyokai's (`dwExtraInfo`) so local input can be told apart. Nothing is injected while a Kyokai
window has the focus (checked at most every 200 ms), and whatever a session holds is released
when it ends. Malformed events are dropped without ending the session.

## Not yet

The remote cursor, other monitors, hardware encoders and the other codecs are Phase 3 steps that
follow (PLAN.md).
