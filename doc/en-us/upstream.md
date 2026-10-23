# Upstream contracts

What Kyokai relies on from each upstream, verified by reading the pinned source or the official
documentation on 2026-10-16. Measured results from the Phase 0 spikes are added to each section
when they run; anything not listed here is not assumed.

## MyGo v0.6.3

- **0.6.3 (checked 2026-10-23).** Nothing Kyokai relies on changed. On Windows a message dialog's
  results stay off the moving Go stack, and Direct3D paces frames that change nothing by the
  display's refresh; presses go through a hidden scroll bar's track; a focused text field takes its
  keys, so Edit › Paste's Ctrl+V pastes once on Windows (the picture is no text field: its keys are
  unchanged); WebKit content rules bring in `golang.org/x/net` (`publicsuffix`).
- **Since 0.6 (checked 2026-10-21).** `plugins/loading` draws loading-dev's 29 indicators natively
  (`loading.Arc(c, loading.Options{Size, Color}).Label(...)`; a static pose when the system asks for reduced
  motion); `transfer.Result.Escaped` tells a drag given up with Escape from one dropped outside every window;
  `ui.Tester.Click`, `ClickWith` and `RightClick` return an error for an element outside the window; a
  window that is off screen draws nothing. `plugins/terminal` (since 0.5) shows a terminal fed by any
  `io.ReadWriteCloser` (`Options.Conn`; one with `Resize(cols, rows int) error` is told its size), drawn by
  libghostty-vt, which `mygo build` bundles. Plugins are packages of the mygo module.

- **Rendering.** Own GPU renderer: Direct3D 11 on Windows, Metal on macOS, a CPU rasterizer as
  fallback. OS calls go through purego and hand-written COM; everything builds with
  `CGO_ENABLED=0`. The UI is immediate-mode with retained per-identity state; `Window.Update(fn)`
  runs `fn` on the main thread and rebuilds the view.
- **Live pictures.** `(*ui.Bitmap).Update(img)` keeps the texture and swaps the pixels; an
  `*image.RGBA` whose stride is 4 × width is taken without a copy and must stay unchanged until the
  next `Update`. It runs on the main thread. Every frame is a full premultiplied-RGBA upload (no
  BGRA, YUV, dirty rectangles or shared textures); Metal waits for frames in flight. A bitmap drawn
  at half size or less is box-filtered on the CPU after every update, unless it is drawn through
  `ui/shader`. `ui.Image(c, bm).Fit(...)` scales; `Pixelated()` gives nearest-neighbour.
- **Input.** `Element.Focusable().HandleInput(func(ui.InputEvent) bool)` delivers key down/up,
  text, IME composition, commands, pointer down/up/move, scroll (`DX`, `DY`, `Precise`) with
  element-local coordinates in DIPs. `TextCaret` turns the system's input methods on for a focused
  element and `InputMethod(false)` off again, keys still typing the layout's characters as text
  (checked 2026-10-23: Windows takes the IMM context off the window,
  `ImmAssociateContextEx(hwnd, 0, 0)`, completing an open composition; macOS skips
  `interpretKeyEvents:` and inserts the event's `characters`); browsers cannot.
  Gaps: no scancodes; about 70 `Key` values (no numpad keys,
  CapsLock, NumLock, PrintScreen, F13–F24, media keys); modifiers only as a bitmask (no left/right);
  Windows drops unmapped keys and always passes `WM_SYSKEYDOWN` (Alt, F10, Alt+F4) to
  `DefWindowProc`; X1/X2 arrive as `KeyBack`/`KeyForward` on release; macOS turns Control-click
  into a right click, ignores CapsLock and lets menu shortcuts (Cmd+Q/W/H/C/V) win; no pointer lock,
  no low-level hooks. A pointer move with no button held comes with `Button` −1 (a drag with the
  button held), so a filter on buttons 0–2 drops every hover move (Kyokai's did until 0.2.0).
- **Windows of the app.** Several windows, full screen, bounds, always-on-top,
  `SetContentProtection`, and `NativeHandle()` (HWND / NSWindow).
- **App services.** Tray (menu, icon), notifications (balloons on Windows), clipboard (text,
  HTML, PNG image; no change event), dialogs, shell open, a single-instance lock keyed by the app
  name, open at login, power events (lock/unlock, idle time), `App.Path` kinds (no separate
  config kind), `Relaunch`, cancelable quit hooks.
- **Updater.** `go tool mygo keygen` writes an Ed25519 pair (base64 of the 64-byte private key and
  of the 32-byte public key) into `-o DIR`, by default a per-user folder shared by every MyGo
  project; it refuses to overwrite without `-force`. With `MYGO_UPDATER_PRIVATE_KEY` set,
  `mygo build` signs the whole-app archive per target and up to three deltas; `update-<target>.json`
  itself is unsigned. Apps read the manifest of the latest GitHub release over HTTPS and check size
  and signature before installing. Windows renames the old files aside (a loaded DLL can be
  renamed); macOS swaps the `.app`. Updating needs write access to the install location.
- **Packaging.** Windows: `.exe` and an NSIS installer that installs per user
  (`%LOCALAPPDATA%\Programs\<Name>`, no elevation). macOS: `.app` and `.dmg`, signed only on a
  macOS host. Files under `resources/` and `resources/<goos>-<goarch>/` ship next to the exe or in
  `Contents/Resources` and are found with `App.Path(PathResources)`. Nested Mach-O files are
  re-signed with the app's identity; a non-ad-hoc identity adds `--options runtime --timestamp`
  (the `.dmg` gets `--timestamp` too). `macos.helperEntitlements` gives nested code its own
  entitlements; `minimumSystemVersion` defaults to 12.0.
- **purego.** On Windows libraries load with `syscall.LoadLibrary` (purego's `Dlopen` is not
  built there); on macOS with `purego.Dlopen`. Callbacks are never freed (shared limit of about
  1000–2000), cannot take floats, and on Windows ARM64 calls cannot pass floats at all.
- **Measured (S2, Windows ARM64, `spikes/s2-viewer`).** 1920×1080 RGBA frames from a Rust
  library shown through `Bitmap.Update` beside a 500-row list: 59–60 frames shown per second,
  MyGo's frame statistics show vsync-bound frames of about 16 ms with build and layout near 1 ms;
  the viewer costs about 21–25 % of one core (process 31–37 % minus the test pattern's drawing).

## EasyTier v2.7.0

- **Source.** Prerelease of 2026-10-06 (latest stable: v2.6.4). Root license LGPL-3.0 (no "or
  later" grant). crates.io stops at 2.0.3, so the crate is a git dependency pinned by commit
  (`8b7f1f0196edb5b580829ed5f135050fb5853478`); several of its own dependencies are git
  dependencies too. Edition 2024, rust-version 1.95. The moving `nightly` tag is not a release.
- **Build.** Needs `protoc` (the build script downloads an x64 one on Windows only); `ring` needs
  clang on `aarch64-pc-windows-msvc`; on x86/x64 Windows `thunk-rs` downloads VC-LTL/YY-Thunks and
  WinDivert is compiled from C. The `kcp` feature needs libclang. v2.7.0 links `Packet` only with
  `faketcp`.
- **Features.** TUN is a feature and can be left out (untested upstream on Windows and macOS).
  Without `wireguard`, `aes-gcm` or `openssl-crypto` the network cipher silently falls back to
  XOR.
- **Embedding API.** `easytier::instance::factory::create_native_instance(TomlConfig)` →
  `start()` / `stop()`; `route_snapshots()`, `peer_snapshots()`, `node_snapshot()`,
  `add_connector()`. The data plane: `data_plane_tcp_connect`, `data_plane_tcp_bind`,
  `data_plane_udp_bind` return tokio streams over the virtual network (IPv4 only, 128 KiB
  buffers, smoltcp without congestion control); `accept()` reports the peer's virtual address;
  streams close when the node's IPv4 changes.
- **`no_tun` inbound.** Packets to the node's own virtual IPv4 terminate in smoltcp (16 KiB
  window) and are forwarded to `127.0.0.1:<same port>`, so every loopback service is reachable from
  the network unless `tcp_whitelist` / `udp_whitelist` restrict it; the application sees the caller
  as `127.0.0.1`. Inbound needs an IPv4 (static or DHCP; DHCP assigns once a peer route exists).
- **Peers and routes.** Route entries carry `inst_id` (the configured `instance_id`),
  `hostname` (control characters stripped, at most 32 characters), IPv4, NAT type, version and
  feature flags; `peer_id` changes every run. Only reachable peers are listed; unreachable entries
  are purged after 90 s. There is no free-form metadata field.
- **Network secret.** Legacy mode checks membership with a non-cryptographic hash of name +
  secret (sent in the first handshake, also to public servers, so weak secrets can be guessed
  offline) and encrypts with one static AES-128-GCM key per network, without forward secrecy.
  Relays forward ciphertext. Secure mode (Noise per link) must match across the whole network;
  stock public servers likely refuse it.
- **Server URLs.**

| Scheme | Default port | Notes |
|---|---|---|
| `tcp`, `udp` | 11010 | |
| `wg` | 11011 | |
| `quic` | 11012 | |
| `faketcp` | 11013 | needs the `faketcp` feature |
| `ws`, `wss` | 80, 443 | |
| `http`, `https` | — | connector only: follows a redirect or reads a list of URLs |
| `txt` | — | connector only: a DNS TXT record of URLs |
| `srv` | — | connector only: `_easytier._<proto>.<domain>` |

- **Public servers.** A public server outside network N joins it as a `PublicServer_…` peer,
  always relays RPC and relays data unless it is configured not to; nodes of N try UDP hole
  punching toward every reachable peer they are not directly connected to.
- **Direct connections.** The direct connector reaches only a peer that lists a listener: for a
  `0.0.0.0` listener it tries each of the peer's interface addresses and its public address,
  preferring `default_protocol`'s scheme, then UDP. Without listeners only hole punching remains,
  which needs the shared router to send a device's packets back into its own network, so two
  devices on one LAN went through the relay (seen 2026-10-18). A configured listener must bind,
  or the instance's `start()` fails; `disable_p2p` stops direct connections both ways (a peer
  honours the other's flag).
- **Measured (S1, Windows ARM64, `spikes/s1-easytier`).** Two embedded nodes and a stock v2.7.0
  `easytier-core` (also as a foreign-network public server): routes carry `inst_id` and hostname;
  `data_plane_tcp_bind/connect` works both ways over tcp, udp, ws, quic and wg connectors and
  `accept()` reports the peer's virtual address; `tcp_whitelist` blocks other ports (as a connect
  timeout) without breaking outbound; ten stop/start cycles leak nothing; no admin, no TUN, no
  `wintun`/`Packet.dll`. Two nodes may announce the same `inst_id` and both stay routable, so routes
  are hints, never identity. Same-machine throughput about 1 Gbit/s relayed, 1.4 Gbit/s direct.
  At v2.7.0 an unavailable cipher is an error, not a silent XOR fallback. Build: clang on PATH for
  `ring`; `protoc` is auto-downloaded unverified unless `PROTOC` is set; on x64 `thunk-rs` downloads
  VC-LTL and relinks the binary against `msvcrt.dll`. Not proven here: hole punching (this network
  is symmetric NAT on both sides).
  Also verified through a real public server running stable v2.6.4 (the network carried as a foreign
  network; connect in 45 ms). The minimal feature set cannot resolve STUN servers (no TXT DNS
  resolution), which NAT detection and hole punching need; the `dns-resolver` feature (hickory,
  without the magic-DNS server) brings it back. `create_native_instance` must run inside a Tokio
  runtime (`Runtime::enter`). EasyTier enables `thunk-rs`'s `win7` feature unconditionally, so it
  can only be replaced, not configured: Kyokai patches in a no-op crate. EasyTier's crates carry no
  license field (the repository is LGPL-3.0), and its pins keep hickory-proto at 0.25 and zip at 8,
  whose advisories `core/deny.toml` records with the reason each does not apply.

## RustDesk 1.5.0

| Module | License status | Kyokai's use |
|---|---|---|
| repository | AGPL-3.0 | vendored files keep their notices |
| `libs/scrap` | upstream scrap was MIT; RustDesk's additions are AGPL | the Windows DXGI capturer, decoupled |
| `libs/enigo` | MIT file from upstream enigo, with RustDesk changes | logic for virtual-desktop moves and scancodes |
| `libs/clipboard` | AGPL, plus Apache-2.0 C code from FreeRDP | not used (file copy over RDP) |
| `libs/hbb_common` | no license grant | never copied |
| `rustdesk-org/hwcodec` | no license grant | never copied |

- `libs/scrap` imports `hbb_common` and `libs/base` and reads RustDesk's configuration store; it
  cannot be used as a crate. Windows capture is DXGI Desktop Duplication with HDR tone mapping and
  a GDI fallback; rotated screens go through libyuv. macOS capture is CGDisplayStream, obsoleted in
  the macOS 15 SDK, with black frames reported on macOS 15 and 26.
- Codecs: libvpx (VP8/VP9), aom (AV1) and libyuv through vcpkg, plus FFmpeg-based hardware codecs;
  Windows ARM64 builds (shipped since 1.4.8) encode in software only.
- Upstream `enigo` 0.6.1 (MIT) scales absolute moves to the primary monitor only; RustDesk's fork
  spreads them over the virtual desktop with `MOUSEEVENTF_VIRTUALDESK`.
- macOS permissions: `CGPreflightScreenCaptureAccess` / `CGRequestScreenCaptureAccess`,
  `AXIsProcessTrustedWithOptions`, `IOHIDCheckAccess` / `IOHIDRequestAccess`.
- Without its SYSTEM service RustDesk cannot capture the UAC secure desktop or the lock screen, and
  `SendInput` into elevated windows is dropped (UIPI).
- **Measured (S3-lite, Windows ARM64, `spikes/s3-capture`).** The DXGI capturer from `libs/scrap/src/dxgi`
  builds on its own once `hbb_common`, `base` and libyuv are replaced by a small shim (frame types,
  rotate/mirror in plain Rust) and the Magnification module is dropped; it captures a 3840×2160
  display at about 60 fps.
- **A still screen times out.** `dxgi/mod.rs` reports `AcquireNextFrame`'s
  `DXGI_ERROR_WAIT_TIMEOUT` as `io::ErrorKind::TimedOut`; scrap's own wrapper around it turns that
  into `WouldBlock`, which the vendored module alone does not. Read as a lost display, it made a
  Windows host tear its capture down and open it again half a second later at every still frame
  period (0.1.0–0.3.2). Upstream also creates a new staging texture for every frame.
- **Codecs in Kyokai.** `libs/scrap/src/common/{vpxcodec,vpx,aom}.rs` build on their own once
  their `hbb_common`, protobuf and libyuv uses are cut away (`core/third_party/rustdesk-codec`);
  the bindings are RustDesk's headers and bindgen settings, generated ahead of time. RustDesk
  pins vcpkg at `9e593bb` (release 2026.07.29: libvpx 1.16.0, aom 3.13.3); Kyokai follows the newer
  2026.10.16 (`5d4a3dc`: libvpx 1.16.0, aom 3.15.2, Opus 1.6.1, checked 2026-10-21).
  Contracts relied on: libvpx and libaom take a forced key frame per call (`VPX_EFLAG_FORCE_KF`,
  `AOM_EFLAG_FORCE_KF`); `vpx_codec_enc_config_set` changes the bitrate of a running encoder;
  with no look-ahead (`g_lag_in_frames` 0, which Kyokai sets for libvpx; libaom's real-time
  usage has it) both return a frame per picture or drop it, so nothing needs flushing; VP9
  profile 1 and AV1 profile 1 take I444 input, and their decoders report I444 pictures by format.
  vcpkg builds aom for Windows ARM64 as generic C (`AOM_TARGET_CPU=generic`) and libvpx for
  Windows ARM64 without SVE.


## OpenAI computer use

Verified against the computer use guide and the openai-python types on 2026-10-16.

- Responses API with `tools: [{"type": "computer"}]` (no other fields). Models named by the guide:
  `gpt-6.1-sol` for the computer tool; `gpt-6-astra` prefers code execution, which Kyokai does not
  use. `computer-use-preview` remains for older integrations.
- Output items `computer_call`: `id`, `call_id`, `status`, `pending_safety_checks` (`id`, `code`,
  `message`), and the actions as an ordered `actions` list (a legacy single `action` also exists).
- Actions: `click` (`button`: left, right, wheel, back, forward; `x`, `y`; optional `keys`),
  `double_click` (`x`, `y`, `keys`), `drag` (`path` of points, `keys`), `keypress` (`keys`), `move`
  (`x`, `y`, `keys`), `screenshot`, `scroll` (`x`, `y`, `scroll_x`, `scroll_y`, `keys`), `type`
  (`text`), `wait`.
- The reply is a `computer_call_output` with the `call_id`, `output: {type: "computer_screenshot",
  image_url: "data:image/png;base64,…"}` and, after approval, `acknowledged_safety_checks`; the
  request carries `previous_response_id`. Coordinates are in the screenshot's pixel space; a
  downscaled screenshot needs its coordinates mapped back.

## Claude computer use

Verified against Anthropic's computer use documentation and API reference on 2026-10-16.

- Tool entry `{"type": "computer_toolset_20260801"}`: GA on the Claude API, no beta header, no
  `name` and no display size; optional `configs` turns members off (all 17 are on); it cannot share
  a request with `computer_20251124`. Claude Opus 5.5 and Sonnet 5.5 accept only the toolset on the
  Claude API. Models: Fable 5.1, Fable 5, Opus 5.5, Opus 5, Sonnet 5.5, Sonnet 5, Opus 4.8, Haiku
  5.5.
- Calls are `tool_use` blocks whose `name` is the member, with `"toolset_name": "computer"`;
  several can come in one turn and run in order. Every block gets one `tool_result` in the next user
  message, each echoing `"toolset_name": "computer"`; after the first failure the rest are answered
  with `is_error` ("Not executed …"). `screenshot` and `zoom` return images, `cursor_position`
  text, the rest a short `OK`.

| Member | Input |
|---|---|
| `screenshot`, `cursor_position`, `left_mouse_down`, `left_mouse_up` | none |
| `zoom` | `region` `[x0, y0, x1, y1]` |
| `left_click`, `right_click`, `middle_click`, `double_click`, `triple_click` | optional `coordinate` `[x, y]`, optional modifier `text` (`shift`, `ctrl`, `alt`, `super`, joined with `+`) |
| `left_click_drag` | `start_coordinate`, `coordinate`, optional `text` |
| `mouse_move` | `coordinate` |
| `scroll` | `scroll_direction` (up, down, left, right), `scroll_amount`, optional `coordinate`, `text` |
| `type` | `text` |
| `key` | `text` (`Return`, `ctrl+s`, `alt+Tab`), optional `repeat` 1–100 |
| `hold_key` | `text`, `duration` seconds up to 300 |
| `wait` | `duration` seconds up to 300 |

- Coordinates are in the pixel space of the full screenshots returned, also after `zoom`. Images:
  at most 2576 px on the long edge and about 3.75 MP, never downscaled by the API; once a request
  holds more than 20 images, at most 2000 px per side. Recommended: about 1280×720 to 1366×768; a
  macOS Retina capture is downscaled or its coordinates halved.
- The history is append-only: editing or removing an earlier turn (such as pruning old
  screenshots) invalidates later thinking blocks and is rejected on new accounts; context is
  bounded with server-side tool-result clearing (`context-management-2025-06-27`) or compaction.
- Claude Opus 5.5: adaptive thinking cannot be turned off, effort defaults to `medium`, forced
  `tool_choice` returns 400, refusals arrive as `stop_reason: "refusal"` with `stop_details`;
  server-side fallbacks (`server-side-fallback-2026-07-01`, `fallbacks: "default"`) retry on
  another model. Progress notes between tool calls come back as `thinking` blocks, readable with
  `thinking.display: "updates"` (`thinking-display-updates-2026-08-18`).
- Classifiers scan returned screenshots for prompt injection and steer the model to confirm with
  the user; Anthropic still asks for human confirmation of consequential actions.
- **Measured (S4, `spikes/s4-providers`).** A live two-turn round trip with `claude-opus-5-5`: turn 1
  returned three batched members (`left_click`, `wait`, `screenshot`) for a synthetic 1280×720
  screenshot; our `tool_result`s with the second screenshot were accepted and turn 2 ended the task
  (`end_turn`). Both requests carried `fallbacks: "default"` and the two beta headers without error.
- **Measured (the core's runtime, 2026-10-17).** The same exchange through the product's client
  also carried `context_management` with `clear_tool_uses_20250919` and the
  `context-management-2025-06-27` beta; it was accepted and the task finished in three actions.

## Operating systems

- **macOS:** since Sequoia, apps that capture the screen without the system picker are asked to
  re-confirm monthly; the "Persistent Content Capture" entitlement that avoids it needs Apple's
  approval. Privacy grants follow the code signature: an ad-hoc signature changes with every build.
  A process that connects to devices on the local network needs the Local Network permission
  (asked once, for the app the process belongs to; root daemons are not asked). ScreenCaptureKit
  leaves windows out by its content filter; a window's sharing type does not decide what
  Kyokai's own capture sees.
- **VideoToolbox:** H.264 on every Mac, HEVC encoding and decoding in Apple silicon's media
  engine; `VTCopyVideoEncoderList` names the hardware encoders and `VTIsHardwareDecodeSupported`
  the hardware decoders. Sessions work in length-prefixed units with the parameter sets in a
  format description; a decompression session without the asynchronous flag calls its output
  callback before `DecodeFrame` returns, and `CompleteFrames` waits for the frames up to a time.
  Kyokai gives it NV12 and takes NV12 back, so colours do not depend on which system converts
  (BT.601 limited range throughout).
- **Windows:** a normal user process captures and controls the normal desktop only; elevated
  windows (UIPI), the UAC secure desktop and the lock screen need a SYSTEM process.
